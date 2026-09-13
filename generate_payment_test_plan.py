#!/usr/bin/env python3
"""Generate the payment pipeline test-plan workbook."""

import pathlib
from openpyxl import Workbook
from openpyxl.styles import Font, PatternFill, Alignment, Border, Side
from openpyxl.utils import get_column_letter

HEADER_FONT = Font(name="Calibri", bold=True, color="FFFFFF", size=11)
HEADER_FILL = PatternFill(start_color="2F5496", end_color="2F5496", fill_type="solid")
CELL_FONT = Font(name="Calibri", size=10)
WRAP = Alignment(wrap_text=True, vertical="top")
THIN_BORDER = Border(
    left=Side(style="thin"),
    right=Side(style="thin"),
    top=Side(style="thin"),
    bottom=Side(style="thin"),
)

HEADERS = [
    "ID",
    "Scenario",
    "Preconditions",
    "Steps",
    "Expected HTTP/DB Status",
    "Expected Result",
    "Automated Test",
]

COL_WIDTHS = [6, 34, 32, 52, 22, 42, 48]

# ── Card ──────────────────────────────────────────────────────────────────────

CARD = [
    (
        "C1",
        "Merchant payment success (card checkout)",
        "Active merchant, funded test card, CARD capability enabled",
        (
            "1. MENU > Make payment > pick merchant > enter amount\n"
            "2. Pick Card\n"
            "3. Review + confirm\n"
            "4. Complete hosted-fields checkout on /checkout/:token/pay\n"
            "5. Return page hits /payments/return → VerifyAndApply"
        ),
        "Payment: succeeded\nLedger: balanced",
        (
            "Payment status → succeeded. Ledger nets zero "
            "(1100→2100 + 2100→2300_xego + 2100→3100_merchant). "
            "2 WhatsApp status messages sent. Money-in allowance released."
        ),
        (
            "TestSimulateMakePaymentAndPayIndividual — simulate_flows_test.go:235\n"
            "TestLiveInterswitchPaymentLedger — live_interswitch_ledger_test.go:43"
        ),
    ),
    (
        "C2",
        "Card payment declined",
        "Active merchant, card flagged / insufficient funds",
        (
            "1. Same as C1 steps 1-4\n"
            "2. Gateway returns decline during VerifyAndApply"
        ),
        "Payment: initialized (no change)\nNo ledger posting",
        (
            "Payment stays at initialized. No ledger entry created. "
            "Money-in allowance stays reserved. User sees retry prompt."
        ),
        "No automated test — requires provider decline simulation",
    ),
    (
        "C3",
        "Card payment expired (stale requery)",
        "Draft payment created, user never completes checkout",
        (
            "1. Start card checkout (create draft)\n"
            "2. Do NOT complete hosted-fields page\n"
            "3. Wait for expiry window\n"
            "4. ReconcileWorker runs expireStale"
        ),
        "Payment: expired\nAllowance released",
        (
            "Draft marked expired. Money-in allowance released back to customer. "
            "No ledger entry."
        ),
        (
            "TestReconcilePayments — store_integration_test.go\n"
            "expireStale — payments.go:936"
        ),
    ),
    (
        "C4",
        "Card fee validation (200bps + 10000 flat, 350000 cap)",
        "Fee config defaults (XEGO_FEE_CARD_*)",
        (
            "1. Create 100000 kobo card payment\n"
            "2. Verify fee = max(100000*200/10000 + 10000, 350000)\n"
            "   = max(12000, 350000) = 12000\n"
            "3. Check split postings in ledger"
        ),
        "Fee: 12000 kobo\nLedger splits correct",
        (
            "Collection fee = 12000 kobo. Ledger: 1100→2100 (base), "
            "2100→2300_xego_payable (fee), 2100→3100_merchant (remainder)."
        ),
        "Pattern verified in fees.go, simulate_flows_test ledger assertion",
    ),
    (
        "C5",
        "Webhook-driven card success (idempotent)",
        "Webhook TRANSACTION.COMPLETED arrives before /payments/return",
        (
            "1. Start card checkout\n"
            "2. Gateway sends webhook before user hits return page\n"
            "3. VerifyAndApply runs from webhook path\n"
            "4. User later hits /payments/return"
        ),
        "Payment: succeeded (single)\nNo double-count",
        (
            "First VerifyAndApply succeeds. Second call is idempotent — "
            "no duplicate ledger entries or allowance charges."
        ),
        "Idempotency guard at payments.go:965-972",
    ),
    (
        "C6",
        "Invoice payment via card",
        "Approved merchant, invoice with positive balance, hosted-fields checkout",
        (
            "1. Open /invoice/:ref/pay\n"
            "2. Pick card checkout\n"
            "3. Complete hosted-fields payment\n"
            "4. Return page → VerifyAndApply"
        ),
        "Payment: succeeded\nInvoice updated",
        (
            "Invoice amount_paid_kobo incremented. Ledger correct. "
            "Receipt message sent."
        ),
        "No dedicated test yet — invoice card path in checkout.go:443-493",
    ),
]

# ── Bank Transfer ─────────────────────────────────────────────────────────────

BANK = [
    (
        "B1",
        "Simulated bank transfer success",
        "BANK_TRANSFER_MODE=simulate (default)",
        (
            "1. MENU > Make payment > pick merchant > amount\n"
            "2. Pick bank transfer\n"
            "3. Select bank from picker\n"
            "4. Receive simulated transfer instructions + reference\n"
            "5. Send CONFIRM in chat"
        ),
        "Payment: succeeded\nLedger balanced",
        (
            "Payment marked succeeded. Simulated bank confirmation processed. "
            "Ledger: 1100→2100. Status messages sent."
        ),
        (
            "simulateBankTransferConfirm — simulate_flows_test.go\n"
            "TestConfirmBankTransferSimulation — store_integration_test.go"
        ),
    ),
    (
        "B2",
        "Simulated bank transfer expiry",
        "User receives instructions but never sends CONFIRM",
        (
            "1. Same as B1 steps 1-4\n"
            "2. Do NOT send CONFIRM\n"
            "3. Expiry window passes\n"
            "4. expireStale runs"
        ),
        "Payment: expired\nNo ledger",
        (
            "Draft marked expired. No ledger entry. "
            "No status message sent."
        ),
        "expireStale — payments.go:936",
    ),
    (
        "B3",
        "Interswitch DVA success",
        "BANK_TRANSFER_MODE=interswitch, Interswitch DVA credentials set",
        (
            "1. MENU > Make payment > bank transfer\n"
            "2. One-time virtual account assigned via Transfer\n"
            "3. Customer pays to virtual account\n"
            "4. Webhook arrives (or requery completes)"
        ),
        "Payment: succeeded\nVA created",
        (
            "Virtual account created. Payment succeeded. Ledger correct. "
            "DVA instruction page served at /transfer/:ref."
        ),
        (
            "TestPostgresVirtualAccountInstructionRoundTrip — store_integration_test.go:18\n"
            "TestLiveInterswitchPaymentLedger — live_interswitch_ledger_test.go:43"
        ),
    ),
    (
        "B4",
        "DVA fee validation (150bps, 0 fixed, 150000 cap)",
        "Fee config defaults (XEGO_FEE_DVA_*)",
        (
            "1. Create 200000 kobo bank-transfer payment\n"
            "2. Fee = 200000*150/10000 + 0 = 3000\n"
            "3. Verify ledger splits"
        ),
        "Fee: 3000 kobo\nLedger splits correct",
        "Fee verified via ledger double-entry assertions in store_integration_test.go",
    ),
    (
        "B5",
        "Individual pay via bank transfer (NIP)",
        "L2 approved individual, recipient phone number",
        (
            "1. MENU > Pay individual > enter phone > amount 5000\n"
            "2. Pick bank transfer\n"
            "3. Complete simulated transfer + CONFIRM"
        ),
        "Payment: succeeded\nRecipient wallet credited",
        (
            "Recipient wallet credited: 5000 - NIP fee (10000 kobo). "
            "Ledger: 1100→2100, 2300_user_payable credited, "
            "2301_user_wallet debited on sender side."
        ),
        "simulatePayIndividual — simulate_flows_test.go:473",
    ),
]

# ── Wallet ────────────────────────────────────────────────────────────────────

WALLET = [
    (
        "W1",
        "Wallet payment success (sufficient balance)",
        "Active wallet with balance > payment amount",
        (
            "1. MENU > Make payment > pick merchant > amount\n"
            "2. Pick wallet\n"
            "3. Review balance warning (if applicable)\n"
            "4. Confirm"
        ),
        "Payment: succeeded\nWallet debited",
        (
            "Atomic debit. Payment → succeeded via TransitionPaymentWithOutbox. "
            "Ledger: 2301_user_wallet→2100. Outbox sends 2 status messages."
        ),
        (
            "TestReproWalletWebPayment — repro_wallet_web_test.go:45\n"
            "TestWalletPaymentAndRefund — store_integration_test.go:178"
        ),
    ),
    (
        "W2",
        "Wallet payment insufficient balance",
        "Active wallet with balance < payment amount",
        (
            "1. Same as W1 steps 1-3\n"
            "2. Confirm → ConfirmWalletPayment fails"
        ),
        "Payment: initialized (no change)\nNo debit",
        (
            "ErrInsufficientWalletBalance returned. No ledger entry. "
            "Payment stays at initialized. Error surfaced to user."
        ),
        "wfRoutePaymentFailed — webflow_money.go:508",
    ),
    (
        "W3",
        "Wallet topup via card",
        "Active wallet, valid test card",
        (
            "1. MENU > Fund wallet > enter amount > pick card\n"
            "2. Complete card checkout\n"
            "3. PaymentHookWalletTopup fires on success"
        ),
        "Topup payment: succeeded\nWallet credited",
        (
            "ApplyWalletTopup posts 2100_customer_float → 2301_user_wallet. "
            "Wallet balance increases by topup amount."
        ),
        "TestWalletTopup — store_integration_test.go:309",
    ),
    (
        "W4",
        "Wallet topup via bank transfer",
        "Active wallet",
        (
            "1. Same as W3 but pick bank transfer\n"
            "2. Complete bank transfer + CONFIRM"
        ),
        "Topup payment: succeeded\nWallet credited",
        "Same ledger result as W3 (2100→2301_user_wallet).",
        "Same test pattern as W3",
    ),
    (
        "W5",
        "Wallet on inactive account",
        "Inactive wallet (KYC level insufficient for wallet)",
        (
            "1. Same as W1 steps 1-3\n"
            "2. Wallet state check fails before confirm"
        ),
        "ErrWalletNotActive\nNo debit",
        (
            "wfWalletStatePreview returns error. User prompted to "
            "complete KYC before wallet usage."
        ),
        "wfWalletStatePreview — webflow_money.go:474",
    ),
]

# ── Refunds & Settlements ────────────────────────────────────────────────────

REFUNDS = [
    (
        "R1",
        "Full refund (maker-checker)",
        "Succeeded payment, no active refund on this payment",
        (
            "1. Admin: RequestRefund(payment_id, amount, reason)\n"
            "2. Reviewer: ApproveRefund(refund_id)\n"
            "3. Provider dispatch (simulated or Interswitch FULL)\n"
            "4. CompleteRefund → ledger reversal + wallet credit"
        ),
        "Refund: succeeded\nPayment: refunded",
        (
            "Payment → refunded. Ledger reversed via PostLedgerReversal. "
            "User wallet credited with refund amount."
        ),
        (
            "TestRefundLifecycle — store_refunds_integration_test.go:15\n"
            "TestWalletPaymentAndRefund — store_integration_test.go:178"
        ),
    ),
    (
        "R2",
        "Partial refund",
        "Succeeded payment, refund amount < payment amount",
        (
            "1. RequestRefund with partial amount\n"
            "2. ApproveRefund\n"
            "3. Provider partial refund dispatch"
        ),
        "Refund: succeeded\nPayment: succeeded (partial)",
        (
            "Original payment stays succeeded. Ledger partially reversed. "
            "Refund record tracks partial amount."
        ),
        "TestRefundLifecycle partial branch — store_refunds_integration_test.go",
    ),
    (
        "R3",
        "Duplicate refund blocked",
        "Active (pending_approval) refund already exists for payment",
        (
            "1. RequestRefund → success\n"
            "2. RequestRefund again for same payment → blocked"
        ),
        "Second request rejected",
        (
            "RequestRefund checks for active refunds. "
            "Second request returns error, does not create duplicate."
        ),
        "TestRefundLifecycle duplicate guard — store_refunds_integration_test.go",
    ),
    (
        "R4",
        "Settlement cut and payout",
        "Succeeded merchant payments in batch, merchant has wallet",
        (
            "1. CutSettlement on batch (automated or manual)\n"
            "2. Fee deducted: batch_total * 250bps\n"
            "3. W1 settlement: 3200→3101_business_wallet\n"
            "4. CreatePayout → dispatch to bank"
        ),
        "Settlement: cut\nPayout: dispatched",
        (
            "STL:<batch> journal created. Fee to 5200_settlement_fees. "
            "Business wallet credited. Payout dispatched to bank."
        ),
        "TestSettlementLifecycle — store_settlements_integration_test.go:19",
    ),
    (
        "R5",
        "Settlement payout failure and retry",
        "Payout dispatched, simulated bank 011 always fails",
        (
            "1. RequestPayout → dispatch with bank 011\n"
            "2. FailPayout (provider returns error)\n"
            "3. RetryPayout with valid bank\n"
            "4. CompletePayout"
        ),
        "Payout: failed → retried → completed",
        (
            "FailPayout records failure. RetryPayout resubmits. "
            "CompletePayout credits 1100_operating_bank."
        ),
        "TestSettlementLifecycle retry branch — store_settlements_integration_test.go",
    ),
]

# ── Coverage map (Sheet 5) ───────────────────────────────────────────────────

COVERAGE = [
    ("C1", "TestSimulateMakePaymentAndPayIndividual", "simulate_flows_test.go:235", "TEST_DATABASE_URL", "Full coverage (simulated gateway)"),
    ("C1", "TestLiveInterswitchPaymentLedger", "live_interswitch_ledger_test.go:43", "TEST_DATABASE_URL, ISW_SANDBOX_SMOKE=1, live creds", "Live Interswitch requery path"),
    ("C2", "—", "—", "—", "NO AUTOMATED TEST — needs provider decline stub"),
    ("C3", "expireStale (via TestReconcilePayments)", "payments.go:936", "TEST_DATABASE_URL", "Covered by reconcile worker path"),
    ("C4", "TestFeeCalculation pattern", "fees.go:17-46", "Unit test (no DB)", "Fees asserted in simulate tests"),
    ("C5", "Idempotency guard (no dedicated test)", "payments.go:965-972", "—", "Guard exists; no dedicated idempotency test"),
    ("C6", "—", "checkout.go:443-493", "—", "NO AUTOMATED TEST — invoice card checkout"),
    ("B1", "simulateBankTransferConfirm", "simulate_flows_test.go", "TEST_DATABASE_URL", "Full simulated BT path"),
    ("B2", "expireStale", "payments.go:936", "TEST_DATABASE_URL", "Shared expiry path"),
    ("B3", "TestPostgresVirtualAccountInstructionRoundTrip", "store_integration_test.go:18", "TEST_DATABASE_URL", "DVA round trip"),
    ("B4", "Fee assertions in integration tests", "fees.go", "TEST_DATABASE_URL", "Indirect coverage"),
    ("B5", "simulatePayIndividual", "simulate_flows_test.go:473", "TEST_DATABASE_URL", "Full individual pay path"),
    ("W1", "TestReproWalletWebPayment", "repro_wallet_web_test.go:45", "TEST_DATABASE_URL", "Wallet web payment success"),
    ("W1", "TestWalletPaymentAndRefund", "store_integration_test.go:178", "TEST_DATABASE_URL", "Wallet payment + refund"),
    ("W2", "wfRoutePaymentFailed (no dedicated test)", "webflow_money.go:508", "—", "Covered by friendly error path"),
    ("W3", "TestWalletTopup", "store_integration_test.go:309", "TEST_DATABASE_URL", "Topup ledger path"),
    ("W4", "Same pattern as W3", "store_integration_test.go:309", "TEST_DATABASE_URL", "Shares topup ledger path"),
    ("W5", "wfWalletStatePreview guard", "webflow_money.go:474", "—", "Guard exists; no dedicated test"),
    ("R1", "TestRefundLifecycle", "store_refunds_integration_test.go:15", "TEST_DATABASE_URL", "Full maker-checker flow"),
    ("R1", "TestWalletPaymentAndRefund", "store_integration_test.go:178", "TEST_DATABASE_URL", "Refund wallet credit"),
    ("R2", "TestRefundLifecycle (partial branch)", "store_refunds_integration_test.go", "TEST_DATABASE_URL", "Partial refund covered"),
    ("R3", "TestRefundLifecycle (dup guard)", "store_refunds_integration_test.go", "TEST_DATABASE_URL", "Duplicate rejection"),
    ("R4", "TestSettlementLifecycle", "store_settlements_integration_test.go:19", "TEST_DATABASE_URL", "Cut + payout"),
    ("R5", "TestSettlementLifecycle (retry)", "store_settlements_integration_test.go:19", "TEST_DATABASE_URL", "Fail + retry + reverse"),
]


def _write_sheet(wb, title, headers, rows, widths):
    ws = wb.create_sheet(title=title)
    for col_idx, (header, width) in enumerate(zip(headers, widths), 1):
        cell = ws.cell(row=1, column=col_idx, value=header)
        cell.font = HEADER_FONT
        cell.fill = HEADER_FILL
        cell.alignment = WRAP
        cell.border = THIN_BORDER
        ws.column_dimensions[get_column_letter(col_idx)].width = width
    for row_idx, row_data in enumerate(rows, 2):
        for col_idx, value in enumerate(row_data, 1):
            cell = ws.cell(row=row_idx, column=col_idx, value=value)
            cell.font = CELL_FONT
            cell.alignment = WRAP
            cell.border = THIN_BORDER
    ws.auto_filter.ref = ws.dimensions
    ws.freeze_panes = "A2"


def main():
    out = pathlib.Path(__file__).parent / "reports" / "Payment_Pipeline_Test_Plan.xlsx"
    out.parent.mkdir(parents=True, exist_ok=True)

    wb = Workbook()
    wb.remove(wb.active)

    _write_sheet(wb, "Card", HEADERS, CARD, COL_WIDTHS)
    _write_sheet(wb, "Bank Transfer", HEADERS, BANK, COL_WIDTHS)
    _write_sheet(wb, "Wallet", HEADERS, WALLET, COL_WIDTHS)
    _write_sheet(wb, "Refunds & Settlements", HEADERS, REFUNDS, COL_WIDTHS)

    # Coverage map sheet (different columns)
    cov_headers = ["Test ID", "Automated Test", "File:Line", "Env Required", "Coverage Gap"]
    cov_widths = [8, 42, 38, 34, 46]
    _write_sheet(wb, "Existing Test Coverage", cov_headers, COVERAGE, cov_widths)

    wb.save(str(out))
    print(f"Wrote {out}  ({len(CARD)+len(BANK)+len(WALLET)+len(REFUNDS)} tests, {len(COVERAGE)} coverage rows)")


if __name__ == "__main__":
    main()
