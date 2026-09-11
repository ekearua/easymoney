"""Rewrite the 'Existing Test Coverage' sheet (sheet5) of
Payment_Pipeline_Test_Plan.xlsx in place, preserving cell styles: same 24
data rows, only the text changes. Values are inlineStr cells.
"""
import shutil
import zipfile
from xml.etree import ElementTree as ET

SRC = "reports/Payment_Pipeline_Test_Plan.xlsx"
NS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
M = "{%s}" % NS
ET.register_namespace("", NS)

# Exactly 24 replacement rows (excluding header), one per existing data row.
NEW_ROWS = [
    ("C1", "TestSimulateMakePaymentAndPayIndividual", "simulate_flows_test.go:235", "TEST_DATABASE_URL", "Full coverage (simulated gateway); asserts ledger splits C4-style"),
    ("C1", "TestLiveInterswitchPaymentLedger", "live_interswitch_ledger_test.go:43", "TEST_DATABASE_URL, ISW_SANDBOX_SMOKE=1, live creds", "Live Interswitch requery path (gated smoke)"),
    ("C2", "TestCardPaymentDeclined_C2", "payment_pipeline_test.go:176", "TEST_DATABASE_URL", "Decline terminates payment, no ledger posting"),
    ("C3", "TestCardCheckoutExpiry_C3", "payment_expiry_test.go:49", "TEST_DATABASE_URL", "Reconcile expires abandoned checkout, releases allowance, no ledger"),
    ("C4", "Fee assertions in simulate/ledger tests", "fees.go; simulate_flows_test.go", "TEST_DATABASE_URL", "200bps+10000 flat, 350000 cap asserted via ledger splits"),
    ("C5", "TestCardPaymentWebhookIdempotent_C5", "payment_pipeline_test.go:205", "TEST_DATABASE_URL", "Replayed success is a no-op: one transition, one posting"),
    ("C6", "TestInvoicePaymentViaCard_C6", "payment_pipeline_test.go:250", "TEST_DATABASE_URL", "Invoice card checkout: paid invoice + money-in + allocation"),
    ("B1", "TestReconciliationThreeWay (drives ConfirmBankTransferSimulation)", "store_integration_test.go:1409", "TEST_DATABASE_URL", "Simulated rail retired in 7ae7367; confirmation covered here"),
    ("B2", "TestBankTransferExpiry_B2", "payment_expiry_test.go:100", "TEST_DATABASE_URL", "Unconfirmed transfer expires; bank_transfer pending now expirable"),
    ("B3", "TestPostgresVirtualAccountInstructionRoundTrip", "store_integration_test.go:18", "TEST_DATABASE_URL", "DVA instruction round trip"),
    ("B4", "Ledger split assertions in store suite", "fees.go; store_integration_test.go", "TEST_DATABASE_URL", "150bps DVA fee verified via double-entry assertions"),
    ("B5", "Individual-pay branch of TestSimulateMakePaymentAndPayIndividual", "simulate_flows_test.go:235", "TEST_DATABASE_URL", "NIP individual pay: recipient wallet credited"),
    ("W1", "TestReproWalletWebPayment", "repro_wallet_web_test.go", "TEST_DATABASE_URL", "Wallet web payment debits wallet atomically (needs fresh sim DB)"),
    ("W1", "TestWalletPaymentAndRefund", "store_integration_test.go", "TEST_DATABASE_URL", "Wallet payment + refund credit"),
    ("W2", "friendlyWebPaymentError cases + ConfirmWalletPayment guard", "webflow_media_test.go:91; store_payments.go transitionPayment", "Unit (no DB) / TEST_DATABASE_URL", "Insufficient balance: no debit, payment stays, friendly error"),
    ("W3", "TestWalletTopup", "store_integration_test.go", "TEST_DATABASE_URL", "Topup ledger: customer float -> user wallet"),
    ("W4", "TestWalletTopup (bank-transfer provider variant)", "store_integration_test.go", "TEST_DATABASE_URL", "Shares topup ledger path"),
    ("W5", "friendlyWebPaymentError cases + wallet-active guard", "webflow_media_test.go:92; store_payments.go transitionPayment", "Unit (no DB) / TEST_DATABASE_URL", "Inactive wallet blocks confirmation before any debit"),
    ("R1", "TestRefundLifecycle (maker-checker subtest)", "store_refunds_integration_test.go", "TEST_DATABASE_URL", "Full maker-checker refund + wallet credit"),
    ("R1", "TestWalletPaymentAndRefund", "store_integration_test.go", "TEST_DATABASE_URL", "Refund credits payer wallet"),
    ("R2", "TestRefundLifecycle (partial subtest)", "store_refunds_integration_test.go", "TEST_DATABASE_URL", "Partial refund keeps payment succeeded"),
    ("R3", "TestRefundLifecycle (duplicate/already-refunded subtests)", "store_refunds_integration_test.go", "TEST_DATABASE_URL", "Duplicate and double refund rejected"),
    ("R4", "TestSettlementLifecycle", "store_settlements_integration_test.go", "TEST_DATABASE_URL", "Cut + fee + payout dispatch"),
    ("R5", "TestSettlementLifecycle (fail/retry subtests)", "store_settlements_integration_test.go", "TEST_DATABASE_URL", "Payout failure, retry, completion"),
]

src = zipfile.ZipFile(SRC)
sheet_xml = src.read("xl/worksheets/sheet5.xml")
names = src.namelist()
others = {n: src.read(n) for n in names if n != "xl/worksheets/sheet5.xml"}
src.close()

root = ET.fromstring(sheet_xml)
data_rows = []
for row in root.iter(M + "row"):
    cells = row.findall(M + "c")
    if not cells:
        continue
    first = cells[0]
    is_el = first.find(M + "is")
    first_val = ""
    if is_el is not None:
        first_val = "".join(x.text or "" for x in is_el.iter(M + "t"))
    if first_val == "Test ID":  # header
        continue
    data_rows.append((row, cells))

assert len(data_rows) == len(NEW_ROWS), f"{len(data_rows)} data rows vs {len(NEW_ROWS)} replacements"

for (row, cells), values in zip(data_rows, NEW_ROWS):
    assert len(cells) == len(values), f"row has {len(cells)} cells, want {len(values)}"
    for c, text in zip(cells, values):
        is_el = c.find(M + "is")
        assert is_el is not None, f"cell {c.get('r')} is not an inlineStr"
        # Replace all <t> runs inside <is> with a single t holding the text.
        ts = is_el.findall(M + "t")
        for extra in ts[1:]:
            is_el.remove(extra)
        ts[0].text = text
        ts[0].set("{http://www.w3.org/XML/1998/namespace}space", "preserve")

out_xml = ET.tostring(root, xml_declaration=True, encoding="UTF-8")

shutil.copy(SRC, SRC + ".bak")
with zipfile.ZipFile(SRC, "w", zipfile.ZIP_DEFLATED) as dst:
    for n, data in others.items():
        dst.writestr(n, data)
    dst.writestr("xl/worksheets/sheet5.xml", out_xml)
print("rewritten OK")
