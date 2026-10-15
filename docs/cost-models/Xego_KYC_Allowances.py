#!/usr/bin/env python3
"""Generate Xego_KYC_Allowances.xlsx — executive overview of the tiered KYC/KYB
allowance ladder, the requirements to reach each level, and how allowances reset.

Run:  py -3.13 Xego_KYC_Allowances.py
Output: reports/Xego_KYC_Allowances.xlsx
"""
import os

from openpyxl import Workbook
from openpyxl.styles import Alignment, Font, PatternFill
from openpyxl.utils import get_column_letter

HDR = "1F4E79"          # dark blue
SECT = "D6E4F0"         # light blue
SUBS = "E2EFDA"         # light green
NOTE = "FCE4D6"         # light orange
BAND = "F2F7FB"         # very light row band


def solid(color):
    return PatternFill("solid", start_color=color, end_color=color)


def hdr_style(ws, cell):
    c = ws[cell]
    c.font = Font(bold=True, color="FFFFFF", size=12)
    c.fill = solid(HDR)
    c.alignment = Alignment(horizontal="center", vertical="center", wrap_text=True)


def section(ws, cell, text):
    c = ws[cell]
    c.value = text
    c.font = Font(bold=True, color=HDR, size=13)
    c.fill = solid(SECT)
    c.alignment = Alignment(vertical="center", wrap_text=True)


def subtitle(ws, cell, text):
    c = ws[cell]
    c.value = text
    c.font = Font(italic=True, size=11, color="595959")
    c.alignment = Alignment(vertical="center", wrap_text=True)


def title(ws, cell, text):
    c = ws[cell]
    c.value = text
    c.font = Font(bold=True, size=18, color=HDR)
    c.alignment = Alignment(vertical="center")


def init_sheet(ws, headers, widths, title_text, subtitle_text, freeze="A3"):
    ws.title = headers[0] if headers else ""
    for col, w in zip(widths.keys(), widths.values()):
        ws.column_dimensions[col].width = w
    title(ws, "A1", title_text)
    subtitle(ws, "A2", subtitle_text)
    ws.freeze_panes = freeze
    return ws


def add_row(ws, r, values, band=False):
    for i, v in enumerate(values, start=1):
        c = ws.cell(row=r, column=i, value=v)
        c.alignment = Alignment(vertical="top", wrap_text=True)
        if band:
            c.fill = solid(BAND)
    return r + 1


def build_ladder(wb):
    ws = wb.active
    init_sheet(
        ws,
        ["Ladder & Limits"],
        {"A": 22, "B": 10, "C": 14, "D": 18, "E": 18, "F": 18, "G": 16, "H": 16, "I": 18},
        "Xego \u2014 KYC / KYB Allowance Ladder",
        "Transaction ceilings per KYC level (kobo and naira). Single, daily and monthly caps apply to both money-in and money-out.",
    )
    ws.merge_cells("A1:I1")
    ws.merge_cells("A2:I2")

    headers = ("Segment", "Level", "Direction", "Single (kobo)", "Daily (kobo)",
               "Monthly (kobo)", "Single (\u20a6)", "Daily (\u20a6)", "Monthly (\u20a6)")
    for i, h in enumerate(headers, start=1):
        hdr_style(ws, ws.cell(row=3, column=i, value=h).coordinate)

    groups = [
        ("Individuals (KYC)", [
            ("L0 \u2014 Intro", 2_000_000, 2_000_000, 10_000_000),
            ("L1 \u2014 Basic", 5_000_000, 5_000_000, 30_000_000),
            ("L2 \u2014 Enhanced", 20_000_000, 20_000_000, 50_000_000),
            ("L3 \u2014 Premium", 100_000_000, 100_000_000, 1_000_000_000),
            ("L4 \u2014 High Value", 500_000_000, 500_000_000, 5_000_000_000),
        ]),
        ("Businesses (KYB)", [
            ("B0 \u2014 Intro", 20_000_000, 20_000_000, 100_000_000),
            ("B1 \u2014 Basic", 100_000_000, 100_000_000, 500_000_000),
            ("B2 \u2014 Enhanced", 500_000_000, 500_000_000, 5_000_000_000),
            ("B3 \u2014 High Value", 1_000_000_000, 1_000_000_000, 10_000_000_000),
        ]),
    ]

    r = 4
    band = False
    for segment, rows in groups:
        for level, single, daily, month in rows:
            values = [segment, level, "in / out", single, daily, month,
                      single / 100, daily / 100, month / 100]
            for i, v in enumerate(values, start=1):
                c = ws.cell(row=r, column=i, value=v)
                c.alignment = Alignment(vertical="center", horizontal="right" if i >= 4 else "left")
                if i in (4, 5, 6):
                    c.number_format = "#,##0"
                elif i in (7, 8, 9):
                    c.number_format = "\u20a6#,##0"
                if band:
                    c.fill = solid(BAND)
            r += 1
        band = not band
        r += 1  # blank separator between segments

    note_r = r
    notes = [
        "Ceilings are cumulative as a customer rises through levels \u2014 higher thresholds are unlocked by completing the requirements in the \u201cKYC Requirements\u201d sheet.",
        "The same ceilings apply to money moving in and money moving out.",
        "Daily window = rolling 24 hours (WAT). Monthly window = calendar month (WAT).",
        "A blocked compliance re-screen reduces the account to the entry level (L0 / B0) and its ceilings apply until review is cleared.",
    ]
    for i, n in enumerate(notes):
        c = ws.cell(row=note_r + i, column=1, value=n)
        c.font = Font(italic=True, size=10, color="595959")
        c.alignment = Alignment(vertical="center", wrap_text=True)
        c.fill = solid(NOTE)
        ws.merge_cells(start_row=note_r + i, start_column=1, end_row=note_r + i, end_column=9)
    return ws


def build_requirements(wb):
    ws = wb.create_sheet(
        "",  # placeholder; will set below
    )
    ws.title = "KYC Requirements"
    init_sheet(
        ws,
        ["KYC Requirements"],
        {"A": 18, "B": 64},
        "What is Required to Reach Each Level",
        "The documentation and checks a customer must provide to be upgraded. Higher levels require everything from the lower levels plus the additions listed.",
    )
    ws.merge_cells("A1:B1")
    ws.merge_cells("A2:B2")

    hdr_style(ws, ws.cell(row=3, column=1, value="Level").coordinate)
    hdr_style(ws, ws.cell(row=3, column=2, value="Requirements to Reach This Level").coordinate)

    sections = [
        ("Individuals (KYC)", [
            ("L0 \u2014 Intro", "Auto-granted on first sign-up. Phone number verified (OTP) and WhatsApp channel confirmed (evidence: channel_confirmed). No identity documents required."),
            ("L1 \u2014 Basic",
             "Reached once the channel is confirmed and identification is submitted.\nSelf-service in WhatsApp: name, date of birth, and government-issued photo ID (NIN, Driver's Licence, Passport, Voter's Card) with a selfie / liveness check.\nRequirement evidence: channel_confirmed."),
            ("L2 \u2014 Enhanced",
             "Identity is on file \u2014 granted automatically when a clear sanctions / PEP screening result is returned after the L1 documents are submitted.\nRequirement evidence: channel_confirmed + identity_on_file.\nA screening hit routes the account to a manual compliance review instead of an automatic upgrade."),
            ("L3 \u2014 Premium",
             "NIN or BVN verified in WhatsApp by an identity provider.\nRequirement evidence: nin_bvn_verified.\nUser sends their 11-digit NIN or BVN in chat; on a verified match the level is advanced automatically and the risk score recomputed."),
            ("L4 \u2014 High Value",
             "Enhanced due diligence (EDD) completed. Not self-service \u2014 raised through a compliance review case.\nRequirement evidence: edd_completed, granted by a compliance-officer approval of the tier_upgrade case."),
        ]),
        ("Businesses (KYB)", [
            ("B0 \u2014 Intro", "Auto-granted on registration approval. Business phone and representative verified (OTP). Evidence: registration_approved."),
            ("B1 \u2014 Basic",
             "RC / CAC registration documents and directors verified.\nRequirement evidence: business_docs_verified.\nSelf-service: merchant submits an upgrade request from WhatsApp (KYB status \u2192 Request KYB upgrade); an admin approves it in the KYB console, which advances the tier."),
            ("B2 \u2014 Enhanced",
             "Settlement bank verified against the registered business identity.\nRequirement evidence: business_bank_verified.\nMerchant requests the upgrade in WhatsApp; the admin approves it after verifying the bank is matched to the business. Advancing to B2 and above requires a clear screening decision."),
            ("B3 \u2014 High Value",
             "Enhanced due diligence (EDD) on beneficial owners and source of funds.\nRequirement evidence: business_edd_completed.\nMerchant requests the upgrade in WhatsApp; the admin approves it at compliance-head level."),
        ]),
    ]

    r = 4
    first = True
    for segment, rows in sections:
        section(ws, ws.cell(row=r, column=1, value=segment).coordinate, segment)
        ws.merge_cells(start_row=r, start_column=1, end_row=r, end_column=2)
        r += 1
        for level, req in rows:
            c = ws.cell(row=r, column=1, value=level)
            c.font = Font(bold=True, color=HDR)
            c.alignment = Alignment(vertical="top")
            cnt_extras = max(len(x) for x in req.split("\n"))
            ws.cell(row=r, column=2, value=req)
            ws.cell(row=r, column=2).alignment = Alignment(vertical="top", wrap_text=True)
            r += 1
        r += 1  # blank separator
    return ws


def build_examples(wb):
    ws = wb.create_sheet("")
    ws.title = "Tier Movement Examples"
    init_sheet(
        ws,
        ["Tier Movement Examples"],
        {"A": 24, "B": 28, "C": 34, "D": 62},
        "Practical Examples: Users Moving Through the Tiers",
        "Walk-throughs showing how an individual and a business rise through the ladder, what triggers each step, and who performs it.",
    )
    ws.merge_cells("A1:D1")
    ws.merge_cells("A2:D2")

    headers = ("Actor", "Step", "Trigger / Owner", "What actually happens")
    for i, h in enumerate(headers, start=1):
        hdr_style(ws, ws.cell(row=3, column=i, value=h).coordinate)

    examples = [
        ("Individual \u2014 Amaka",
         "Sign-up (L0)",
         "Customer, automatic",
         "Amaka verifies her phone number and confirms her WhatsApp channel. Xego auto-creates her KYC profile at L0 with a \u20a620,000 single cap and \u20a6100,000 monthly cap."),
        ("Individual \u2014 Amaka",
         "To L1",
         "Customer, automatic",
         "She completes the Become-individual flow in chat: legal name, date of birth and a government photo ID with a selfie. The channel-confirmed evidence grants L1 (single \u20a650,000, monthly \u20a6300,000)."),
        ("Individual \u2014 Amaka",
         "To L2",
         "System (auto) or Compliance",
         "Her identity is screened against sanctions / PEP. On a clear result, Xego auto-advances her to L2 (single \u20a6200,000, monthly \u20a6500,000). If the screen flags anything, the account routes to a manual compliance review case instead of auto-advancing."),
        ("Individual \u2014 Amaka",
         "To L3",
         "Customer + identity provider",
         "She sends her 11-digit NIN or BVN in chat. The provider verifies it; on a match Xego records nin_bvn_verified and advances her to L3 (single \u20a61,000,000, monthly \u20a610,000,000), recalculating her risk band."),
        ("Individual \u2014 Amaka",
         "To L4",
         "Compliance review case",
         "She wants the highest ceilings (single \u20a65,000,000, monthly \u20a650,000,000). EDD is not self-service \u2014 Xego raises a tier_upgrade case, a compliance officer approves it, and the edd_completed evidence advances her to L4."),
        ("Individual \u2014 Amaka",
         "Re-screen hit",
         "System, automatic",
         "At the next 90-day re-screen a name matches an adverse list. Compliance downgrades her and restores the lower ceilings until the issue is cleared."),
        ("Business \u2014 Bello's Foods",
         "Registration (B0)",
         "Admin, automatic",
         "The business registers and is approved. Xego creates its KYB profile at B0 with a \u20a6200,000 single cap and \u20a61,000,000 monthly payout cap."),
        ("Business \u2014 Bello's Foods",
         "To B1",
         "Merchant requests, Admin approves",
         "Bello wants higher payouts. In WhatsApp he opens KYB status \u2192 Request KYB upgrade; the request targets B1 (business_docs_verified). A compliance admin reviews and approves it in the KYB console, advancing the tier to B1 (single \u20a61,000,000, monthly \u20a65,000,000)."),
        ("Business \u2014 Bello's Foods",
         "To B2",
         "Merchant requests, Admin approves",
         "For settlement-scale payouts (single \u20a65,000,000, monthly \u20a650,000,000) he requests the next step. The bank must be verified against the business identity (business_bank_verified) and the screening decision must be clear before the admin approves B1 \u2192 B2."),
        ("Business \u2014 Bello's Foods",
         "To B3",
         "Merchant requests, Admin (compliance-head) approves",
         "The top tier (single \u20a610,000,000, monthly \u20a6100,000,000) requires enhanced due diligence on beneficial owners and source of funds (business_edd_completed). The merchant requests it in WhatsApp and compliance-head approves."),
        ("Business \u2014 Bello's Foods",
         "Re-screen blocked",
         "System, automatic",
         "A scheduled re-screen returns a blocked decision. Xego automatically downgrades the business to B0 and applies the entry-level ceilings until the review is cleared."),
    ]

    r = 4
    for i, (actor, step, trigger, desc) in enumerate(examples):
        vals = (actor, step, trigger, desc)
        for ci, v in enumerate(vals, start=1):
            c = ws.cell(row=r, column=ci, value=v)
            c.alignment = Alignment(vertical="top", wrap_text=True)
            if ci <= 2:
                c.font = Font(bold=True, color=HDR if ci == 1 else "000000")
            if i % 2 == 1:
                c.fill = solid(BAND)
        r += 1
    return ws


def build_notes(wb):
    ws = wb.create_sheet("How Allowances Reset")
    init_sheet(
        ws,
        ["How Allowances Reset"],
        {"A": 30, "B": 84},
        "How Allowances Reset & Review",
        "Rules governing when ceilings refresh and how levels are reviewed over time.",
    )
    ws.merge_cells("A1:B1")
    ws.merge_cells("A2:B2")

    hdr_style(ws, ws.cell(row=3, column=1, value="Rule").coordinate)
    hdr_style(ws, ws.cell(row=3, column=2, value="How it works").coordinate)

    rules = [
        ("Daily allowance", "The daily ceiling is a rolling 24-hour window (WAT). As time passes, earlier transactions age out of the window, gradually freeing up capacity for new activity."),
        ("Monthly allowance", "The monthly ceiling resets at the start of each calendar month (WAT). Any unused monthly capacity does not carry over to the next month."),
        ("Single-transaction cap", "The single cap sets the largest value allowed in one transaction. It cannot be exceeded, regardless of available daily or monthly capacity."),
        ("Money-in vs money-out", "Ceilings apply independently to funds moving in and funds moving out. Each direction has its own running totals."),
        ("Level upgrades", "A customer moves to a higher level once they complete the requirements for that level (see the \u201cKYC Requirements\u201d sheet). The new, higher ceilings apply immediately."),
        ("Periodic re-screening", "Compliance re-screens customers every 90 days. This re-confirms identity and checks sanctions / adverse information on an ongoing basis."),
        ("Blocked / failed screening", "A blocked re-screen immediately lowers the account to the entry level (L0 / B0), applying the lower ceilings and pausing upgrade eligibility until the review is cleared."),
        ("Downgrade on review", "Compliance can downgrade a level at any time if circumstances change (new adverse findings, expired documents, or a change in risk profile), restoring the ceilings of the lower level."),
        ("Admin control", "Compliance and Admin teams can review and adjust a customer's level and, where required, the platform's ceiling schedule through the admin console. All changes are recorded."),
    ]

    r = 4
    for i, (rule, desc) in enumerate(rules):
        c = ws.cell(row=r, column=1, value=rule)
        c.font = Font(bold=True, color=HDR)
        c.alignment = Alignment(vertical="top", wrap_text=True)
        d = ws.cell(row=r, column=2, value=desc)
        d.alignment = Alignment(vertical="top", wrap_text=True)
        if i % 2 == 1:
            c.fill = solid(BAND)
            d.fill = solid(BAND)
        r += 1
    return ws


def main():
    wb = Workbook()
    build_ladder(wb)
    build_requirements(wb)
    build_examples(wb)
    build_notes(wb)

    here = os.path.dirname(os.path.abspath(__file__))
    out = os.path.join(here, "Xego_KYC_Allowances.xlsx")
    wb.save(out)
    print("wrote", out)


if __name__ == "__main__":
    main()
