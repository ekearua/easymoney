# -*- coding: utf-8 -*-
"""Build EXTERNAL_SERVICE_COST_MODEL.xlsx from editable assumption cells.

Raw-OpenXML generator (stdlib only; openpyxl segfaults on this box), same
approach as the docx builders. Workbook layout:

  sheet "Assumptions"       - every driver and unit price as an editable cell;
                              volatile prices carry Low/High planning ranges;
                              USD and EUR exchange rates are editable cells
  sheet "Monthly per 1,000" - per-line build-up (base, low, high, all formulas)
  sheet "Scale totals"      - 1,000/5,000/10,000/50,000/100,000-user columns with
                              base, low and high all-in and per-payment figures,
                              plus a total row
  sheet "Compliance compare"- vendor price ranges in original currency plus naira
                              equivalents, with vendor minimum/maximum rows
  sheet "Servers compare"   - hosting families in USD plus naira equivalents and
                              a total row

Finance edits the yellow assumption cells; every downstream total recomputes
in Excel on open (formulas, not cached numbers).

The WhatsApp driver is itemized: seven per-payment send-path components
(page opener, status template, completion message, failure templates, receipt
media, business replies, in-chat uplift) sum to 5.0 expected messages, and an
editable planning contingency multiplier (default 2.8) raises the billing
driver to 14. Setting the multiplier to 1 bills the bare expected path.
"""
import os
import sys
import zipfile

# Output path defaults to the repo root twin of the markdown; override with
# COST_MODEL_OUT (or argv[1]) so the workbook can be rebuilt to a scratch path
# while the live copy is open and locked by Excel.
OUT = sys.argv[1] if len(sys.argv) > 1 else os.environ.get(
    "COST_MODEL_OUT", "EXTERNAL_SERVICE_COST_MODEL.xlsx")

# ---------- minimal OpenXML helpers (same style as sync_manual_schedule) ---
NS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"

def col_name(i):  # 0-based -> A, B, ...
    s = ""
    i += 1
    while i:
        i, r = divmod(i - 1, 26)
        s = chr(65 + r) + s
    return s

def esc(t):
    return (t.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;"))

def cell(ref, value=None, formula=None, style=0):
    v = ""
    if formula is not None:
        v = "<f>%s</f>" % esc(formula)
    elif value is not None:
        if isinstance(value, (int, float)):
            v = "<v>%s</v>" % value
        else:
            v = "<v></v>"  # inline via t=inlineStr below
    if isinstance(value, str) and formula is None:
        return '<c r="%s" s="%d" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>' % (ref, style, esc(value))
    return '<c r="%s" s="%d">%s</c>' % (ref, style, v)

def row(n, cells):
    return '<row r="%d">%s</row>' % (n, "".join(cells))

def sheet_xml(rows, cols=None):
    # CT_Worksheet element order matters to Excel: dimension, sheetViews,
    # cols, then sheetData. Emitting cols after sheetData makes Excel reject
    # the whole file, even though the XML is well-formed.
    colxml = ""
    end_col = "A"
    if cols:
        colxml = "<cols>%s</cols>" % "".join(
            '<col min="%d" max="%d" width="%s" customWidth="1"/>' % c for c in cols)
        end_col = col_name(max(c[0] for c in cols) - 1)
    maxrow = max(int(r.split('"')[1]) for r in rows) if rows else 1
    return ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<worksheet xmlns="%s">'
            '<dimension ref="A1:%s%d"/>'
            '<sheetViews><sheetView workbookViewId="0"/></sheetViews>'
            '%s<sheetData>%s</sheetData>'
            '</worksheet>' % (NS, end_col, maxrow + 2, colxml, "".join(rows)))

# ---------- styles: 0 plain, 1 bold header, 2 editable input (yellow),
#                     3 formula/result (light gray), 4 money format -----------
STYLES = '''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="%s">
<numFmts count="2"><numFmt numFmtId="164" formatCode="#,##0"/><numFmt numFmtId="165" formatCode="#,##0.00"/></numFmts>
<fonts count="3">
 <font><sz val="11"/><name val="Calibri"/></font>
 <font><b/><sz val="11"/><name val="Calibri"/></font>
 <font><b/><sz val="12"/><name val="Calibri"/></font>
</fonts>
<fills count="4">
 <fill><patternFill patternType="none"/></fill>
 <fill><patternFill patternType="gray125"/></fill>
 <fill><patternFill patternType="solid"><fgColor rgb="FFFFF2CC"/><bgColor indexed="64"/></patternFill></fill>
 <fill><patternFill patternType="solid"><fgColor rgb="FFF2F2F2"/><bgColor indexed="64"/></patternFill></fill>
</fills>
<borders count="2">
 <border><left/><right/><top/><bottom/><diagonal/></border>
 <border><left style="thin"><color auto="1"/></left><right style="thin"><color auto="1"/></right><top style="thin"><color auto="1"/></top><bottom style="thin"><color auto="1"/></bottom><diagonal/></border>
</borders>
<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
<cellXfs count="7">
 <xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>
 <xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0"/>
 <xf numFmtId="164" fontId="0" fillId="2" borderId="1" xfId="0" applyFill="1" applyBorder="1" applyNumberFormat="1"/>
 <xf numFmtId="164" fontId="0" fillId="3" borderId="1" xfId="0" applyFill="1" applyBorder="1" applyNumberFormat="1"/>
 <xf numFmtId="164" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1" applyNumberFormat="1"/>
 <xf numFmtId="165" fontId="1" fillId="3" borderId="1" xfId="0" applyFill="1" applyBorder="1" applyNumberFormat="1"/>
 <xf numFmtId="165" fontId="0" fillId="2" borderId="1" xfId="0" applyFill="1" applyBorder="1" applyNumberFormat="1"/>
</cellXfs>
</styleSheet>''' % NS

CONTENT_TYPES = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
 '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">'
 '<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>'
 '<Default Extension="xml" ContentType="application/xml"/>'
 '<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>'
 '<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>'
 '%s</Types>')

RELS = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
 '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
 '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>'
 '</Relationships>')

def workbook_xml(names):
    sheets = "".join(
        '<sheet name="%s" sheetId="%d" r:id="rId%d"/>' % (n, i + 1, i + 1)
        for i, n in enumerate(names))
    return ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<workbook xmlns="%s" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">'
            '<sheets>%s</sheets>'
            '<calcPr calcId="124519" fullCalcOnLoad="1"/></workbook>' % (NS, sheets))

def wb_rels(n):
    rels = "".join(
        '<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>' % (i + 1, i + 1)
        for i in range(n))
    rels += ('<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>' % (n + 1))
    return ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">%s</Relationships>' % rels)

# ================= sheet 1: Assumptions (the editable model) ===============
# rows: (label, value_or_formula, unit, note, style)  style 2=editable 3=computed
# RANGES maps a price label to its (low, high) planning range; the builder
# emits yellow Low and High cells for those rows and every consumer derives
# a min-max cost band from them.
ASSUMPTIONS = [
    ("DRIVERS (per 1,000 users per month)", None, "", "", 1),
    ("Active payer share", 0.40, "share", "share of registered users transacting monthly", 2),
    ("Payments per active payer", 4, "payments", "monthly", 2),
    ("Payments per 1,000 users", "1000*B5*B6", "payments", "= 1,000 users x payer share x payment frequency", 3),
    ("Average payment (naira)", 5000, "naira", "drives gross merchandise value (GMV), not the expense model", 2),
    ("Gross merchandise value (GMV) per 1,000 users (naira)", "B7*B8", "naira", "= payments x average payment", 3),
    ("New registrations per month", 100, "users", "10 percent of the user base", 2),
    ("Central Bank of Nigeria (CBN) Tier 2 share of new users", 0.25, "share", "complete Bank Verification Number (BVN) and National Identification Number (NIN) checks in the month", 2),
    ("Identity lookups per 1,000 users", "B10*B11", "lookups", "the same count applies to BVN and to NIN separately", 3),
    ("Screening rescreens per 1,000 users", 50, "searches", "open high-risk cases", 2),
    ("Screening searches per 1,000 users", "B10+B13", "searches", "new-user onboarding plus rescreens", 3),
    ("New Know Your Business (KYB) merchants per 1,000 users", 2, "merchants", "one Corporate Affairs Commission (CAC) check, one Federal Inland Revenue Service (FIRS) check, and one geocode each", 2),
    ("Artificial intelligence (AI) sessions per 1,000 users", 4000, "sessions", "one ask-bar parse per payment, plus optical character recognition (OCR), speech-to-text (STT), and assistant chats", 2),
    ("WHATSAPP MESSAGES PER PAYMENT (itemized send-path build-up)", None, "", "", 1),
    ("Interactive payment-page opener (sendLink or SendCheckout)", 1, "messages", "the chat message carrying the page button, once per payment", 2),
    ("Pending or status template after the gateway redirect", 1, "messages", "SendTemplate status update while the gateway processes", 2),
    ("Payment-received or automatic-refund confirmation", 1, "messages", "SendText completion message carrying the receipt link", 2),
    ("Failure and retry templates (expected share)", 0.5, "messages", "declines need a template plus a reopen call to action; 0.5 is the expected share across payments", 2),
    ("Receipt image or receipt message", 0.5, "messages", "Instagram and TikTok deliver the receipt as a media caption", 2),
    ("Business replies to customer questions", 0.5, "messages", "service-class business replies inside the 24-hour window; inbound customer messages are free", 2),
    ("In-chat flow uplift (Telegram and TikTok)", 0.5, "messages", "method-selection and confirmation turns instead of a page opener", 2),
    ("Expected messages per payment", "SUM(B18:B24)", "messages", "= sum of the seven components above (the bare expected path)", 3),
    ("Planning contingency multiplier", 2.8, "multiplier", "headroom for retries, duplicate attempts, redelivered templates, and support surges; set to 1 to bill the bare expected path", 2),
    ("WhatsApp messages per payment (planning)", "B25*B26", "messages", "= expected x contingency; this is the billing driver used downstream", 3),
    ("Free WhatsApp service messages per month", 1000, "messages", "per business number (Meta rate card, October 2026)", 2),
    ("Chargeable WhatsApp messages per 1,000 users", "B7*B27-B28", "messages", "= payments x messages per payment - free tier", 3),
    ("WhatsApp authentication messages per 1,000 users", 200, "messages", "login-style flows", 2),
    ("Short Message Service (SMS) segments per 1,000 users", 300, "segments", "fallback and alerts", 2),
    ("Email messages per 1,000 users", 2500, "messages", "confirmation codes, receipts, invoices", 2),
    ("Whisper speech-to-text minutes per 1,000 users", 100, "minutes", "voice notes", 2),
    ("UNIT PRICES (naira)", None, "", "", 1),
    ("WhatsApp service or utility message", 11, "naira", "US$0.0067 at the rate below", 2),
    ("WhatsApp authentication message", 23, "naira", "US$0.0145", 2),
    ("Short Message Service (SMS) segment", 4, "naira", "Nigerian published rates from Termii and Africa's Talking", 2),
    ("Email per message", 0.5, "naira", "bulk Simple Mail Transfer Protocol (SMTP) relay", 2),
    ("Bank Verification Number (BVN) lookup", 640, "naira", "US$0.40; vendor range US$0.40-0.80", 2),
    ("National Identification Number (NIN) lookup", 640, "naira", "US$0.40", 2),
    ("Politically exposed person (PEP) and sanctions screening search", 400, "naira", "US$0.25 per search, or the vendor monthly floor", 2),
    ("Corporate Affairs Commission (CAC) status check", 500, "naira", "registry application programming interface (API)", 2),
    ("Federal Inland Revenue Service (FIRS) Tax Identification Number (TIN) check", 200, "naira", "registry API", 2),
    ("Geocoding query", 50, "naira", "address confirmation", 2),
    ("AI session (chat plus optical character recognition (OCR))", 24, "naira", "about 6,000 tokens, small vision model class, US$0.015", 2),
    ("Whisper speech-to-text per minute", 9.6, "naira", "US$0.006 per minute", 2),
    ("United States dollars (USD) to naira exchange rate", 1600, "naira per US dollar", "edit as rates move; every naira column on the comparison sheets recomputes", 2),
    ("Euros (EUR) to naira exchange rate", 1750, "naira per euro", "used by the euro-priced vendor row on the compliance sheet", 2),
    ("PLATFORM FEES (defaults from internal/service/fees.go)", None, "", "", 1),
    ("Card or wallet checkout fee rate", 200, "basis points", "XEGO_FEE_CARD_BPS; wallet payments bill under the same card parameters", 2),
    ("Card or wallet fixed fee", 100, "naira", "XEGO_FEE_CARD_FIXED_KOBO/100", 2),
    ("Card or wallet fee cap", 3500, "naira", "XEGO_FEE_CARD_CAP_KOBO/100", 2),
    ("In-app bank-transfer fee rate", 150, "basis points", "XEGO_FEE_DVA_BPS; the simulated in-app transfer rail", 2),
    ("In-app bank-transfer fee cap", 1500, "naira", "XEGO_FEE_DVA_CAP_KOBO/100", 2),
    ("Bank-transfer checkout fee rate", 180, "basis points", "XEGO_FEE_TRANSFER_BPS", 2),
    ("Bank-transfer checkout fee cap", 2500, "naira", "XEGO_FEE_TRANSFER_CAP_KOBO/100", 2),
    ("NIP payout fee", 100, "naira", "XEGO_FEE_NIP_PAYOUT_FLAT_KOBO/100; deducted from the payout, not billed to Xego", 2),
    ("Rail mix: card or wallet checkout", 0.60, "share", "share of collections billed at the card rate; edit so the three shares sum to 1", 2),
    ("Rail mix: in-app bank transfer", 0.25, "share", "share billed at the DVA rate", 2),
    ("Rail mix: bank-transfer checkout", 0.15, "share", "share billed at the transfer rate", 2),
]

RANGES = {
    "WhatsApp service or utility message": (9, 14),
    "WhatsApp authentication message": (20, 28),
    "Short Message Service (SMS) segment": (3, 6),
    "Email per message": (0.3, 1),
    "Bank Verification Number (BVN) lookup": (640, 1280),
    "National Identification Number (NIN) lookup": (640, 1280),
    "Politically exposed person (PEP) and sanctions screening search": (400, 1600),
    "Corporate Affairs Commission (CAC) status check": (500, 1000),
    "Federal Inland Revenue Service (FIRS) Tax Identification Number (TIN) check": (200, 500),
    "Geocoding query": (50, 100),
    "AI session (chat plus optical character recognition (OCR))": (24, 60),
    "Whisper speech-to-text per minute": (9.6, 19.2),
    "United States dollars (USD) to naira exchange rate": (1500, 1800),
    "Euros (EUR) to naira exchange rate": (1650, 1950),
}

def assum_row(label):
    for i, (l, *_rest) in enumerate(ASSUMPTIONS):
        if l == label:
            return i + 4
    raise KeyError(label)

def build_assumptions():
    rows = [row(1, [cell("A1", "Faya external service cost model - edit the yellow cells, totals recompute", style=4)]),
            row(3, [cell("A3", "Assumption", style=1), cell("B3", "Value", style=1),
                    cell("C3", "Unit", style=1), cell("D3", "Note", style=1),
                    cell("E3", "Low", style=1), cell("F3", "High", style=1)])]
    for i, (label, val, unit, note, st) in enumerate(ASSUMPTIONS):
        n = i + 4
        ref = "B%d" % n
        if val is None:  # section header row
            rows.append(row(n, [cell("A%d" % n, label, style=1)]))
            continue
        if isinstance(val, str) and val.startswith("="):
            val = val[1:]
        rng = RANGES.get(label)
        extra = []
        if rng:
            extra = [cell("E%d" % n, value=rng[0], style=2), cell("F%d" % n, value=rng[1], style=2)]
        if isinstance(val, str):
            rows.append(row(n, [cell("A%d" % n, label), cell(ref, formula=val, style=3),
                                cell("C%d" % n, unit), cell("D%d" % n, note)] + extra))
        else:
            rows.append(row(n, [cell("A%d" % n, label), cell(ref, value=val, style=st),
                                cell("C%d" % n, unit), cell("D%d" % n, note)] + extra))
    # Computed fee rows - naira fee per payment per rail, blended across the
    # (editable) rail mix, and monthly collection-fee earnings per 1,000
    # users. Mirrors XegoCollectionFee in internal/service/fees.go:
    # fee = min(bps * amount / 10000 + fixed, cap, amount).
    avg, card_r, card_f, card_c = [assum_row(x) for x in
        ("Average payment (naira)", "Card or wallet checkout fee rate",
         "Card or wallet fixed fee", "Card or wallet fee cap")]
    dva_r, dva_c = [assum_row(x) for x in
        ("In-app bank-transfer fee rate", "In-app bank-transfer fee cap")]
    trf_r, trf_c = [assum_row(x) for x in
        ("Bank-transfer checkout fee rate", "Bank-transfer checkout fee cap")]
    m_card, m_dva, m_trf = [assum_row(x) for x in
        ("Rail mix: card or wallet checkout", "Rail mix: in-app bank transfer",
         "Rail mix: bank-transfer checkout")]
    n = len(ASSUMPTIONS) + 4
    card_fee = "MIN(B%d*B%d/10000+B%d, B%d, B%d)" % (avg, card_r, card_f, card_c, avg)
    dva_fee = "MIN(B%d*B%d/10000, B%d, B%d)" % (avg, dva_r, dva_c, avg)
    trf_fee = "MIN(B%d*B%d/10000, B%d, B%d)" % (avg, trf_r, trf_c, avg)
    fee_rows = []
    for label, f in [
        ("Card or wallet fee per payment", card_fee),
        ("In-app bank-transfer fee per payment", dva_fee),
        ("Bank-transfer checkout fee per payment", trf_fee),
    ]:
        rows.append(row(n, [cell("A%d" % n, label), cell("B%d" % n, formula=f, style=3),
                            cell("C%d" % n, "naira"),
                            cell("D%d" % n, "collected from the sender; fee = min(bps x naira/10000 + fixed, cap, amount)")]))
        fee_rows.append(n)
        n += 1
    rows.append(row(n, [cell("A%d" % n, "Blended collection fee per payment", style=4),
                        cell("B%d" % n, formula="B%d*B%d + B%d*B%d + B%d*B%d" % (m_card, fee_rows[0], m_dva, fee_rows[1], m_trf, fee_rows[2]), style=5),
                        cell("C%d" % n, "naira"), cell("D%d" % n, "sum of the three rail fees weighted by the editable rail mix")]))
    bf = n
    n += 1
    rows.append(row(n, [cell("A%d" % n, "Collection-fee earnings per 1,000 users per month", style=4),
                        cell("B%d" % n, formula="B%d*B7" % bf, style=5),
                        cell("C%d" % n, "naira"), cell("D%d" % n, "= blended fee x payments per 1,000 users")]))
    return rows, n, bf

# ================= sheet 2: Monthly per 1,000 users (all formulas) =========
# (label, driver_ref, price_ref) -- driver/price refer to Assumptions column B;
# price rows with RANGES entries also expose Assumptions columns E (low) and
# F (high), which feed this sheet's Low and High cost columns.
LINES = [
    ("WhatsApp service/utility", "B29", "B35"),
    ("WhatsApp authentication",  "B30", "B36"),
    ("Short Message Service (SMS) segments", "B31", "B37"),
    ("Email",                    "B32", "B38"),
    ("Bank Verification Number (BVN) lookups", "B12", "B39"),
    ("National Identification Number (NIN) lookups", "B12", "B40"),
    ("PEP and sanctions screening (onboarding plus rescreens)", "B14", "B41"),
    ("Corporate Affairs Commission (CAC) checks", "B15", "B42"),
    ("Federal Inland Revenue Service (FIRS) TIN checks", "B15", "B43"),
    ("Geocoding",                "B15", "B44"),
    ("AI chat and optical character recognition (OCR) sessions", "B16", "B45"),
    ("OpenAI Whisper speech-to-text (STT)", "B33", "B46"),
    ("Telegram / Instagram / TikTok", None, None),
]

def build_monthly(total_row_out):
    rows = [row(1, [cell("A1", "Monthly variable cost per 1,000 users (all formulas - do not type over)", style=4)]),
            row(3, [cell("A3", "Line", style=1), cell("B3", "Volume", style=1),
                    cell("C3", "Rate (naira)", style=1), cell("D3", "Cost (naira)", style=1),
                    cell("E3", "Low cost (naira)", style=1), cell("F3", "High cost (naira)", style=1)])]
    n = 4
    for label, drv, price in LINES:
        if drv is None:
            rows.append(row(n, [cell("A%d" % n, label), cell("B%d" % n, value=0, style=0),
                                cell("C%d" % n, value=0, style=0), cell("D%d" % n, value=0, style=0),
                                cell("E%d" % n, value=0, style=0), cell("F%d" % n, value=0, style=0)]))
        else:
            prown = int(price[1:])
            price_label = ASSUMPTIONS[prown - 4][0]
            ranged = price_label in RANGES
            low_f = "Assumptions!E%d" % prown if ranged else "Assumptions!%s" % price
            high_f = "Assumptions!F%d" % prown if ranged else "Assumptions!%s" % price
            rows.append(row(n, [cell("A%d" % n, label),
                                cell("B%d" % n, formula="Assumptions!%s" % drv, style=3),
                                cell("C%d" % n, formula="Assumptions!%s" % price, style=3),
                                cell("D%d" % n, formula="B%d*C%d" % (n, n), style=3),
                                cell("E%d" % n, formula="B%d*%s" % (n, low_f), style=3),
                                cell("F%d" % n, formula="B%d*%s" % (n, high_f), style=3)]))
        n += 1
    tr = n
    rows.append(row(tr, [cell("A%d" % tr, "TOTAL per 1,000 users", style=4),
                         cell("B%d" % tr, ""), cell("C%d" % tr, ""),
                         cell("D%d" % tr, formula="SUM(D4:D%d)" % (tr - 1), style=4),
                         cell("E%d" % tr, formula="SUM(E4:E%d)" % (tr - 1), style=4),
                         cell("F%d" % tr, formula="SUM(F4:F%d)" % (tr - 1), style=4)]))
    total_row_out.append(tr)
    checks = [
        ("Variable cost per payment (base)", "D%d/Assumptions!B7" % tr),
        ("Variable cost per payment (low)", "E%d/Assumptions!B7" % tr),
        ("Variable cost per payment (high)", "F%d/Assumptions!B7" % tr),
        ("Variable cost per registered user (base)", "D%d/1000" % tr),
        ("Gross merchandise value (GMV) per 1,000 users (cross-check)", "Assumptions!B9"),
        ("External cost as percent of GMV (base)", "100*D%d/Assumptions!B9" % tr),
        ("External cost as percent of GMV (low)", "100*E%d/Assumptions!B9" % tr),
        ("External cost as percent of GMV (high)", "100*F%d/Assumptions!B9" % tr),
    ]
    n = tr + 2
    for label, f in checks:
        rows.append(row(n, [cell("A%d" % n, label), cell("B%d" % n, formula=f, style=5)]))
        n += 1
    return rows

# ================= sheet 3: Scale totals ===================================
# (name, users, infra_per_month, screening_discount, identity_discount)
# Discounts apply ONLY to the screening line and the BVN/NIN identity lines,
# matching the committed-tier pricing in the markdown footnotes; all other
# lines (WhatsApp, AI, SMS, ...) scale at list price.
SCALES = [
    ("1,000",   1000,   80000,   0.0,  0.0),
    ("5,000",   5000,   208000,  0.0,  0.0),
    ("10,000",  10000,  528000,  0.0,  0.0),
    ("50,000",  50000,  1600000, 0.20, 0.20),
    ("100,000", 100000, 4800000, 0.25, 0.15),
]

def scale_var_formula(total_ref, screen_col, id_col, n):
    # (monthly total - screening cost x screening discount - identity cost x
    #  identity discount) scaled to B:n/1000 users. Screen/identity cells on the
    # Monthly sheet are rows 10 and 8/9; screen_col/id_col select D (base),
    # E (low) or F (high) variants.
    return ("(%s - ('Monthly per 1,000'!%s10*H%d"
            "  + ('Monthly per 1,000'!%s8+'Monthly per 1,000'!%s9)*I%d))*B%d/1000"
            % (total_ref, screen_col, n, id_col, id_col, n, n))

def build_scale(base_ref, low_ref, high_ref, blended_ref):
    rows = [row(1, [cell("A1", "Totals at scale - yellow cells are editable. Screening and identity discounts apply only to those lines (narrative footnotes); every other line scales at list price. Fee earnings follow the Assumptions rail mix.", style=4)]),
            row(3, [cell("A3", "Scale", style=1), cell("B3", "Users", style=1),
                    cell("C3", "Variable per month (base)", style=1), cell("D3", "Infrastructure per month", style=1),
                    cell("E3", "All-in per month (base)", style=1), cell("F3", "Per user per month", style=1),
                    cell("G3", "Per payment (base)", style=1), cell("H3", "Screening discount", style=1),
                    cell("I3", "Identity discount", style=1), cell("J3", "All-in per month (low)", style=1),
                    cell("K3", "All-in per month (high)", style=1), cell("L3", "Per payment (low)", style=1),
                    cell("M3", "Per payment (high)", style=1), cell("N3", "Fee earnings per month", style=1),
                    cell("O3", "Net per payment (earnings - all-in)", style=1)])]
    n = 4
    for name, users, infra, scr, ids in SCALES:
        rows.append(row(n, [
            cell("A%d" % n, name),
            cell("B%d" % n, value=users, style=2),
            cell("C%d" % n, formula=scale_var_formula(base_ref, "D", "D", n), style=3),
            cell("D%d" % n, value=infra, style=2),
            cell("E%d" % n, formula="C%d+D%d" % (n, n), style=3),
            cell("F%d" % n, formula="E%d/B%d" % (n, n), style=3),
            cell("G%d" % n, formula="E%d*1000/(Assumptions!B7*B%d)" % (n, n), style=3),
            cell("H%d" % n, value=scr, style=6),
            cell("I%d" % n, value=ids, style=6),
            cell("J%d" % n, formula=scale_var_formula(low_ref, "E", "E", n) + "+D%d" % n, style=3),
            cell("K%d" % n, formula=scale_var_formula(high_ref, "F", "F", n) + "+D%d" % n, style=3),
            cell("L%d" % n, formula="J%d*1000/(Assumptions!B7*B%d)" % (n, n), style=3),
            cell("M%d" % n, formula="K%d*1000/(Assumptions!B7*B%d)" % (n, n), style=3),
            cell("N%d" % n, formula="%s*Assumptions!B7*B%d/1000" % (blended_ref, n), style=3),
            cell("O%d" % n, formula="(N%d-E%d)*1000/(Assumptions!B7*B%d)" % (n, n, n), style=3),
        ]))
        n += 1
    last = n - 1
    rows.append(row(n, [cell("A%d" % n, "TOTAL (sum of the five scenario rows)", style=4),
                        cell("B%d" % n, ""),
                        cell("C%d" % n, formula="SUM(C4:C%d)" % last, style=4),
                        cell("D%d" % n, formula="SUM(D4:D%d)" % last, style=4),
                        cell("E%d" % n, formula="SUM(E4:E%d)" % last, style=4),
                        cell("F%d" % n, ""), cell("G%d" % n, ""),
                        cell("H%d" % n, ""), cell("I%d" % n, ""),
                        cell("J%d" % n, formula="SUM(J4:J%d)" % last, style=4),
                        cell("K%d" % n, formula="SUM(K4:K%d)" % last, style=4),
                        cell("L%d" % n, ""), cell("M%d" % n, ""),
                        cell("N%d" % n, formula="SUM(N4:N%d)" % last, style=4),
                        cell("O%d" % n, "")]))
    rows.append(row(n + 2, [cell("A%d" % (n + 2),
        "The five rows are alternative scenarios, so the total row sums money columns only; per-user and per-payment cells are deliberately left blank. Low/high columns apply the Assumptions price ranges to the variable cost; infrastructure is held constant across the band. The screening and identity discount cells (0.20 at 50,000 users; 0.25 screening / 0.15 identity at 100,000) apply committed-tier savings only to those line families - WhatsApp, AI, SMS, email, CAC, FIRS and geocoding always scale at list price. Fee earnings use the blended collection fee and rail mix on the Assumptions sheet; Net per payment = (fee earnings - all-in cost) per payment, so a positive value means fees cover the external bill. The hosting mix follows section 6 of EXTERNAL_SERVICE_COST_MODEL.md.",
        style=0)]))
    return rows

# ================= sheets 4-5: comparisons =================================
# (vendor, pricing shape, low/high at 1k, 10k, 100k; original currency)
COMPLIANCE = [
    ("ComplyAdvantage", "Starter floor US$99.99-179 per month, 100 searches included, overage above",
     179, 179, 180, 400, 1900, 3800, "USD",
     "Strongest adverse media; payment screening product new (May 2026)"),
    ("Napier AI", "Custom contract; real-time heritage (estimates until quoted)",
     1000, 2000, 2000, 4000, 2000, 5000, "USD",
     "Best payment-screening pedigree; one engine for both customer and payment screening"),
    ("dilisense", "First 100 searches free per month, then EUR 0.10 per call falling to about EUR 0.01",
     5, 15, 140, 140, 150, 1500, "EUR",
     "Cheapest; thinner adverse media; no payment-screening module; weaker list provenance"),
    ("OpenSanctions (self-hosted)", "Open data; self-hosted infrastructure only (about US$50-100 per month)",
     0, 0, 0, 0, 0, 0, "USD",
     "No adverse media/PEP depth/provenance/service-level agreement (SLA) - development only, fails CBN Baseline Standards"),
]

def build_compliance():
    rows = [row(1, [cell("A1", "Compliance vendor comparison - prices editable, naira columns recompute at the Assumptions exchange rates (recommendation: ComplyAdvantage primary, Napier alternate)", style=4)]),
            row(3, [cell("A3", "AML screening vendor", style=1), cell("B3", "Pricing shape", style=1),
                    cell("C3", "1,000 users low", style=1), cell("D3", "1,000 users high", style=1),
                    cell("E3", "10,000 users low", style=1), cell("F3", "10,000 users high", style=1),
                    cell("G3", "100,000 users low", style=1), cell("H3", "100,000 users high", style=1),
                    cell("I3", "Currency", style=1),
                    cell("J3", "Naira 1,000 low", style=1), cell("K3", "Naira 1,000 high", style=1),
                    cell("L3", "Naira 100,000 low", style=1), cell("M3", "Naira 100,000 high", style=1)])]
    n = 4
    first = n
    for r in COMPLIANCE:
        vendor, shape, l1, h1, l10, h10, l100, h100, cur, tradeoff = r
        rate = "Assumptions!B48" if cur == "EUR" else "Assumptions!B47"
        rows.append(row(n, [cell("A%d" % n, vendor, style=1), cell("B%d" % n, shape),
                            cell("C%d" % n, value=l1, style=2), cell("D%d" % n, value=h1, style=2),
                            cell("E%d" % n, value=l10, style=2), cell("F%d" % n, value=h10, style=2),
                            cell("G%d" % n, value=l100, style=2), cell("H%d" % n, value=h100, style=2),
                            cell("I%d" % n, cur),
                            cell("J%d" % n, formula="C%d*%s" % (n, rate), style=3),
                            cell("K%d" % n, formula="D%d*%s" % (n, rate), style=3),
                            cell("L%d" % n, formula="G%d*%s" % (n, rate), style=3),
                            cell("M%d" % n, formula="H%d*%s" % (n, rate), style=3)]))
        n += 1
    last = n - 1
    rows.append(row(n, [cell("A%d" % n, "Vendor minimum across the four options (naira)", style=4),
                        cell("B%d" % n, "lowest naira low at each decision scale"),
                        cell("J%d" % n, formula="MIN(J%d:J%d)" % (first, last), style=4),
                        cell("L%d" % n, formula="MIN(L%d:L%d)" % (first, last), style=4)]))
    rows.append(row(n + 1, [cell("A%d" % (n + 1), "Vendor maximum across the four options (naira)", style=4),
                        cell("B%d" % (n + 1), "highest naira high at each decision scale"),
                        cell("K%d" % (n + 1), formula="MAX(K%d:K%d)" % (first, last), style=4),
                        cell("M%d" % (n + 1), formula="MAX(M%d:M%d)" % (first, last), style=4)]))
    rows.append(row(n + 3, [cell("A%d" % (n + 3), "The vendor rows are alternatives, so the aggregate rows report the minimum and maximum rather than a sum. Identity (Bank Verification Number (BVN) / National Identification Number (NIN)) at 100,000-user scale (5,000 lookups per month): ninbvnportal NGN 1-2 million | Dojah NGN 2.4-4.8 million | Prembly NGN 4-8 million | Youverify NGN 4-8 million | Smile ID (full electronic Know Your Customer (eKYC) with liveness) NGN 6-16 million. Hybrid: the inexpensive record match for Tier 2, full eKYC for Tier 3/high-risk only.", style=0)]))
    return rows

def build_servers():
    hdr = row(3, [cell("A3", "Scale", style=1), cell("B3", "Hyperscale (Amazon Web Services (AWS) / Google Cloud Platform (GCP) / Microsoft Azure)", style=1),
                  cell("C3", "Value cloud (Hetzner / DigitalOcean / Vultr)", style=1), cell("D3", "Oracle Cloud ARM (A1)", style=1),
                  cell("E3", "Nigerian local infrastructure as a service (IaaS; Layer3)", style=1),
                  cell("F3", "Naira: hyperscale", style=1), cell("G3", "Naira: value cloud", style=1),
                  cell("H3", "Naira: Oracle ARM", style=1), cell("I3", "Naira: Nigerian local IaaS", style=1)])
    data = [
        ("1,000 users (1 application + 1 database)", 85, 25, 12, 60),
        ("5,000 users (2 application + database + Redis/Kafka)", 220, 70, 35, 150),
        ("10,000 users (2 application, 16-GB database, 3-node Kafka)", 500, 160, 80, 350),
        ("50,000 users (3 application, 32-GB database + replica)", 1400, 450, 220, 1000),
        ("100,000 users (multiple availability zones (AZ), 64-GB database + synchronous replica)", 3800, 1200, 600, 2800),
    ]
    rows = [row(1, [cell("A1", "Server cost comparison (United States dollars (USD) per month, with naira equivalents at the Assumptions exchange rate; managed PostgreSQL on hyperscale, self-managed elsewhere)", style=4)]), hdr]
    n = 4
    first = n
    for r in data:
        rows.append(row(n, [cell("A%d" % n, r[0], style=1),
                            cell("B%d" % n, value=r[1], style=2), cell("C%d" % n, value=r[2], style=2),
                            cell("D%d" % n, value=r[3], style=2), cell("E%d" % n, value=r[4], style=2),
                            cell("F%d" % n, formula="B%d*Assumptions!B47" % n, style=3),
                            cell("G%d" % n, formula="C%d*Assumptions!B47" % n, style=3),
                            cell("H%d" % n, formula="D%d*Assumptions!B47" % n, style=3),
                            cell("I%d" % n, formula="E%d*Assumptions!B47" % n, style=3)]))
        n += 1
    last = n - 1
    rows.append(row(n, [cell("A%d" % n, "TOTAL (sum of the five scale rows)", style=4),
                        cell("B%d" % n, formula="SUM(B%d:B%d)" % (first, last), style=4),
                        cell("C%d" % n, formula="SUM(C%d:C%d)" % (first, last), style=4),
                        cell("D%d" % n, formula="SUM(D%d:D%d)" % (first, last), style=4),
                        cell("E%d" % n, formula="SUM(E%d:E%d)" % (first, last), style=4),
                        cell("F%d" % n, formula="SUM(F%d:F%d)" % (first, last), style=4),
                        cell("G%d" % n, formula="SUM(G%d:G%d)" % (first, last), style=4),
                        cell("H%d" % n, formula="SUM(H%d:H%d)" % (first, last), style=4),
                        cell("I%d" % n, formula="SUM(I%d:I%d)" % (first, last), style=4)]))
    rows.append(row(n + 2, [cell("A%d" % (n + 2), "The scale rows are a capacity ladder, so the total row is the sum across the ladder, not a monthly figure. Using the planning infrastructure from the Scale-totals sheet, infrastructure is about 16 percent of the all-in bill at 1,000 users and falls to roughly 7-11 percent above 5,000 users; even the most expensive fully-managed hyperscale family stays under a quarter of the all-in bill. Optimize service costs first. Nigerian local buys latency plus Nigeria Data Protection Act (NDPA) residency; recommended shape: hyperscale credits, value cloud from 10,000 users, Nigerian local when residency demands.", style=0)]))
    return rows

# ================= write the workbook ======================================
def main():
    arows, _assum_rows, blended_row = build_assumptions()
    total_row = []
    mrows = build_monthly(total_row)
    tr = total_row[0]
    srows = build_scale("'Monthly per 1,000'!D%d" % tr,
                        "'Monthly per 1,000'!E%d" % tr,
                        "'Monthly per 1,000'!F%d" % tr,
                        "Assumptions!B%d" % blended_row)
    crows = build_compliance()
    vrows = build_servers()

    names = ["Assumptions", "Monthly per 1,000", "Scale totals", "Compliance compare", "Servers compare"]
    sheets = [sheet_xml(arows, cols=[(1, 34, 30), (2, 14, 12), (3, 12, 10), (4, 60, 46), (5, 10, 9), (6, 10, 9)]),
              sheet_xml(mrows, cols=[(1, 36, 32), (2, 12, 10), (3, 12, 10), (4, 16, 14), (5, 16, 14), (6, 16, 14)]),
              sheet_xml(srows, cols=[(1, 9, 8), (2, 10, 9), (3, 20, 18), (4, 16, 14), (5, 18, 16), (6, 13, 12), (7, 13, 12), (8, 15, 13), (9, 13, 12), (10, 18, 16), (11, 18, 16), (12, 13, 12), (13, 13, 12), (14, 18, 16), (15, 20, 18)]),
              sheet_xml(crows, cols=[(1, 24, 22), (2, 40, 34), (3, 13, 12), (4, 13, 12), (5, 13, 12), (6, 13, 12), (7, 14, 13), (8, 14, 13), (9, 10, 9), (10, 15, 14), (11, 15, 14), (12, 16, 15), (13, 16, 15)]),
              sheet_xml(vrows, cols=[(1, 40, 36), (2, 22, 20), (3, 22, 20), (4, 16, 15), (5, 22, 20), (6, 16, 15), (7, 16, 15), (8, 14, 13), (9, 18, 16)])]

    ct = CONTENT_TYPES % "".join(
        '<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>' % (i + 1)
        for i in range(len(sheets)))
    core = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" '
            'xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Faya external service cost model</dc:title>'
            '<dc:creator>Faya</dc:creator></cp:coreProperties>')
    app = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
           '<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"/>')

    with zipfile.ZipFile(OUT, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("[Content_Types].xml", ct)
        z.writestr("_rels/.rels", RELS)
        z.writestr("docProps/core.xml", core)
        z.writestr("docProps/app.xml", app)
        z.writestr("xl/workbook.xml", workbook_xml(names))
        z.writestr("xl/_rels/workbook.xml.rels", wb_rels(len(sheets)))
        z.writestr("xl/styles.xml", STYLES)
        for i, s in enumerate(sheets):
            z.writestr("xl/worksheets/sheet%d.xml" % (i + 1), s)
    print("written", OUT)

if __name__ == "__main__":
    main()
