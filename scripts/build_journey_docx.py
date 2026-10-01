# -*- coding: utf-8 -*-
"""Builds ONBOARDING_PAYMENTS_JOURNEY.docx: the end-to-end individual and
business journey (onboarding through the highest KYC/KYB tier, payments,
external services, Trust Fabric, AML/CFT controls), with three rendered
diagrams. House document style is cloned from CBN_AML_COMPLIANCE_SPEC.docx
(same raw-OOXML approach as spec2docx: python-docx segfaults on this box).

    python scripts/build_journey_docx.py
"""
import os, re, zipfile
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.patches import FancyBboxPatch, FancyArrowPatch

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TMP = os.path.join(os.environ.get("TEMP", "/tmp"), "faya_journey")
os.makedirs(TMP, exist_ok=True)
SRC_DOCX = os.path.join(ROOT, "CBN_AML_COMPLIANCE_SPEC.docx")
OUT_DOCX = os.path.join(ROOT, "ONBOARDING_PAYMENTS_JOURNEY.docx")

GREEN = "#157a5f"; MINT = "#dff5ec"; INK = "#17211d"; AMBER = "#b7791f"
AMBERBG = "#fff2cc"; RED = "#b42318"; PAPER = "#f7faf9"
GREY = "#5b6b64"; LINE = "#9db4ab"

# ---------------------------------------------------------------- diagrams --
def box(ax, x, y, w, h, title, lines, face="#ffffff", edge=GREEN, titlec=INK,
        tfs=10.5, lfs=8.6, radius=0.06, lw=1.4, align="center"):
    ax.add_patch(FancyBboxPatch((x, y), w, h, boxstyle=f"round,pad=0.02,rounding_size={radius}",
                                facecolor=face, edgecolor=edge, linewidth=lw, zorder=3))
    ha = "center" if align == "center" else "left"
    tx = x + w / 2 if align == "center" else x + 0.12
    if title:
        ax.text(tx, y + h - 0.30, title, ha=ha, va="top", fontsize=tfs,
                fontweight="bold", color=titlec, zorder=4)
        body = y + h - 0.62
    else:
        body = y + h - 0.18
    for i, ln in enumerate(lines):
        ax.text(tx, body - i * 0.30, ln, ha=ha, va="top", fontsize=lfs,
                color=INK if not ln.startswith("!") else GREY, zorder=4)

def tag(ax, x, y, w, text, face=MINT, edge=GREEN, fs=7.8):
    ax.add_patch(FancyBboxPatch((x, y), w, 0.42, boxstyle="round,pad=0.02,rounding_size=0.16",
                                facecolor=face, edgecolor=edge, linewidth=1.0, zorder=4))
    ax.text(x + w / 2, y + 0.21, text, ha="center", va="center", fontsize=fs,
            fontweight="bold", color=GREEN, zorder=5)

def arrow(ax, x1, y1, x2, y2, color=GREY, lw=1.6, style="-|>", ls="-", ms=13):
    ax.add_patch(FancyArrowPatch((x1, y1), (x2, y2), arrowstyle=style, linestyle=ls,
                                 mutation_scale=ms, color=color, linewidth=lw, zorder=2))

def band(ax, x, y, w, h, label, face=PAPER, edge="#cfe0d9"):
    ax.add_patch(FancyBboxPatch((x, y), w, h, boxstyle="round,pad=0.02,rounding_size=0.08",
                                facecolor=face, edgecolor=edge, linewidth=1.1, zorder=1))
    ax.text(x + 0.14, y + h - 0.16, label, ha="left", va="top", fontsize=9.5,
            fontweight="bold", color=GREY, zorder=2)

def onboarding_diagram(path):
    fig, ax = plt.subplots(figsize=(16.4, 9.2), dpi=200)
    ax.set_xlim(0, 16.4); ax.set_ylim(0, 9.2); ax.axis("off")
    ax.text(8.2, 8.95, "Faya onboarding — individuals and businesses, to the highest tier",
            ha="center", fontsize=15, fontweight="bold", color=INK)
    ax.text(8.2, 8.55, "One chat channel entry; every tier is a verified-evidence step. Ceilings are the platform defaults per movement / daily / monthly (CBN caps sit at or above them).",
            ha="center", fontsize=9.5, color=GREY)

    band(ax, 0.3, 4.62, 15.8, 3.72, "INDIVIDUAL — KYC ladder (CBN tiers 1-3)")
    box(ax, 0.7, 6.55, 2.55, 1.45, "L0  Start",
        ["Join on WhatsApp / Telegram /", "Instagram / TikTok (or web).",
         "Channel + email confirmed.", "!chat channel · email (SMTP)"], face="#ffffff")
    box(ax, 3.75, 6.55, 3.55, 1.45, "L1  CBN Tier 1  ·  ₦50k / ₦50k / ₦300k",
        ["One aggregated message: legal name,", "DOB (18+), gender, phone, address.",
         "!name screening (screening vendor)", "!risk score begins (Trust Fabric)"], face=MINT)
    box(ax, 7.85, 6.55, 3.55, 1.45, "L2  CBN Tier 2  ·  ₦200k / ₦200k / ₦500k",
        ["Government photo ID uploaded;", "BVN matched (NIBSS) + NIN (NIMC).",
         "!identity vendor (BVN · NIN)", "!PEP hit → EDD case + SM approval"], face=MINT)
    box(ax, 11.95, 6.55, 3.75, 1.45, "L3  CBN Tier 3  ·  ₦1m / ₦1m / ₦10m",
        ["Address proof (utility bill / geo)", "+ EDD: liveness & source of funds.",
         "!location vendor · EDD step", "!high risk → EDD before limits"], face=MINT)

    band(ax, 0.3, 0.42, 15.8, 3.9, "BUSINESS — KYB ladder (merchants; owner's individual tier rides along)")
    box(ax, 0.7, 2.32, 2.55, 1.45, "B0  Registered",
        ["Merchant onboards via chat form", "or web KYB form; wallet ready.",
         "!reviewer check on submission"], face="#ffffff")
    box(ax, 3.75, 2.32, 3.55, 1.45, "B1  ·  ₦1m / ₦1m / ₦5m",
        ["CAC certificate, TIN, UBOs, operating", "address (geo-confirmed), SCUML —",
         "document upload, per-doc review.", "!CAC · FIRS · location · review"], face=MINT)
    box(ax, 7.85, 2.32, 3.55, 1.45, "B2  ·  ₦5m / ₦5m / ₦50m",
        ["Settlement bank account registered", "and reviewer-verified to the business.",
         "!reviewer bank verification"], face=MINT)
    box(ax, 11.95, 2.32, 3.75, 1.45, "B3  ·  ₦10m / ₦10m / ₦100m",
        ["Business EDD: beneficial owners,", "source of funds; reviewer-approved.",
         "!EDD review · SM oversight"], face=MINT)

    for gx in (3.30, 7.42, 11.50):
        tag(ax, gx, 7.06, 0.42, "✓", face=AMBERBG, edge=AMBER, fs=8.5)
    for gx, gy in ((3.30, 2.86), (7.42, 2.86), (11.50, 2.86)):
        tag(ax, gx, gy, 0.42, "✓", face=AMBERBG, edge=AMBER, fs=8.5)
    for x1, x2, y in ((3.25, 3.75, 7.27), (7.30, 7.85, 7.27), (11.40, 11.95, 7.27),
                      (3.25, 3.75, 3.04), (7.30, 7.85, 3.04), (11.40, 11.95, 3.04)):
        arrow(ax, x1, y, x2, y)
    arrow(ax, 1.97, 6.55, 1.97, 3.77, color=LINE, ls=(0, (4, 3)))
    ax.text(2.10, 5.15, "the owner climbs\ntheir own ladder too", fontsize=7.6, color=GREY, va="center")

    ax.text(0.42, 0.30, "! = external service touchpoint:  screening vendor (watchlists/PEP, fuzzy match) · NIBSS (BVN) · NIMC (NIN) · CAC registry · FIRS TIN ·",
            fontsize=7.8, color=GREY, ha="left")
    ax.text(0.42, 0.06, "location/geo · EDD step (liveness + source of funds; live biometrics vendor pending) · SMTP email · Interswitch rails.  CBN caps: Tier 1 ₦50k/₦200k/₦500k · Tier 2 ₦200k/₦500k/₦3m · Tier 3 uncapped.  Amber ✓ = evidence gate + reviewer/screening decision.",
            fontsize=7.8, color=GREY, ha="left")
    fig.savefig(path, bbox_inches="tight", facecolor="white")
    plt.close(fig)

def payments_diagram(path):
    fig, ax = plt.subplots(figsize=(16.4, 9.6), dpi=200)
    ax.set_xlim(0, 16.4); ax.set_ylim(0, 9.6); ax.axis("off")
    ax.text(8.2, 9.35, "Faya payments — money in, money out, and the controls around them",
            ha="center", fontsize=15, fontweight="bold", color=INK)

    band(ax, 0.3, 5.05, 3.6, 3.85, "1 · THE CUSTOMER ASKS")
    box(ax, 0.55, 7.05, 3.1, 1.55, "Chat conversation",
        ["WhatsApp · Telegram · Instagram ·", "TikTok. Text, photo or voice note;",
         "menus for everything else.", "!AI: OCR (image) · STT (voice)"], face="#ffffff")
    box(ax, 0.55, 5.35, 3.1, 1.45, "Web checkout (browser)",
        ["AI ask-bar: \"pay ade kitchen 2500\"", "parses merchant + amount server-side;",
         "stepper pages: pick → review → pay."], face="#ffffff")

    band(ax, 4.2, 5.05, 5.6, 3.85, "2 · MONEY IN (funding rails)")
    box(ax, 4.45, 7.05, 2.5, 1.55, "Card — Interswitch",
        ["Hosted-fields secure page;", "PAN never touches Faya.",
         "!Interswitch gateway"], face=MINT)
    box(ax, 7.05, 7.05, 2.5, 1.55, "Bank transfer (DVA)",
        ["One-time virtual account;", "webhook credit applied once.", "!Interswitch transfer rails"],
        face=MINT)
    box(ax, 4.45, 5.35, 5.1, 1.45, "Wallet — ledger balance",
        ["Top up by card/transfer; pay from balance; receive payouts.",
         "!double-entry ledger (030)"], face="#ffffff")

    band(ax, 10.1, 5.05, 6.0, 3.85, "3 · MONEY OUT — G2 SCREENING GATE")
    box(ax, 10.35, 7.05, 2.6, 1.55, "Merchant settlement",
        ["Batch cut → payout to the", "merchant's verified account.",
         "!Interswitch NIP payout"], face=MINT)
    box(ax, 13.1, 7.05, 2.75, 1.55, "Individual pay / cash-out",
        ["Send-to-anyone recipient payout;", "wallet withdrawal to bank.",
         "!Interswitch NIP payout"], face=MINT)
    box(ax, 10.35, 5.35, 5.5, 1.45, "Screen every movement (ReserveAllowance → dispatch)",
        ["clear → dispatch · possible/strong → HOLD (allowance stays reserved) ·",
         "blocked → interdict · vendor down → hold, never clear.",
         "!payment screening vendor (G2; simulated until signed)"], face=AMBERBG, edge=AMBER)

    band(ax, 0.3, 2.6, 15.8, 2.1, "4 · CONTROLS AROUND EVERY PAYMENT")
    box(ax, 0.55, 2.72, 3.6, 1.55, "Transaction monitoring",
        ["Rules on every movement:", "structuring, velocity, high value.",
         "!alert queue with rail filter"], face="#ffffff")
    box(ax, 4.45, 2.72, 3.6, 1.55, "Allowance ladder (C9)",
        ["Tier ceilings enforced per movement —", "₦ single / daily / monthly, in & out.",
         "!reservations are replay-safe"], face="#ffffff")
    box(ax, 8.35, 2.72, 3.6, 1.55, "Reviewer queue",
        ["Alerts → cases → disposition;", "PEP EDD needs senior-management",
         "sign-off before tiers advance.", "!audit trail on every act"], face="#ffffff")
    box(ax, 12.25, 2.72, 3.6, 1.55, "Regulator reporting",
        ["STR 24h clock · CTR / PEP by the 7th;",
         "CSV + goAML XML downloads.", "!NFIU (goAML) filing"], face="#ffffff")

    band(ax, 0.3, 0.35, 15.8, 1.95, "5 · TRUST FABRIC — signals feed every decision above",
         face="#eef6f2", edge=GREEN)
    layers = [
        ("L1 Identity", "verified who\n(KYC/KYB ladder)"),
        ("L2 Signal", "device, channel,\nrisk events"),
        ("L3 Decision", "risk band bites:\nlimits, step-up"),
        ("L4 Network", "shared devices &\naccounts → rings"),
        ("L5 Intelligence", "KPIs, tuning,\ndecay of old alerts"),
    ]
    for i, (t, d) in enumerate(layers):
        x = 0.7 + i * 3.16
        box(ax, x, 0.5, 2.85, 1.25, "", [d], face="#ffffff", edge=GREEN, lw=1.1)
        ax.text(x + 1.425, 1.62, t, ha="center", va="center", fontsize=9.2,
                fontweight="bold", color=GREEN, zorder=5)

    arrow(ax, 3.65, 7.8, 4.45, 7.8, color=GREEN, lw=2.0)
    arrow(ax, 3.65, 6.05, 4.45, 6.05, color=GREEN, lw=2.0)
    arrow(ax, 6.95, 7.05, 6.95, 6.80, color=GREY)
    arrow(ax, 9.55, 7.8, 10.35, 7.8, color=GREEN, lw=2.0)
    arrow(ax, 9.55, 6.05, 10.35, 6.05, color=GREEN, lw=2.0)
    arrow(ax, 11.65, 7.05, 12.6, 6.80, color=GREY)
    arrow(ax, 14.4, 7.05, 14.4, 6.80, color=GREY)
    arrow(ax, 2.35, 2.6, 2.35, 2.3, color=GREY)
    arrow(ax, 8.2, 2.6, 8.2, 2.3, color=GREY)
    fig.savefig(path, bbox_inches="tight", facecolor="white")
    plt.close(fig)

def sequence_diagram(path):
    """One card payment end to end: ask → parse → draft → hosted fields →
    gateway webhook (requery-authoritative) → receipt → later settlement with
    the G2 screening gate."""
    fig, ax = plt.subplots(figsize=(16.4, 9.8), dpi=200)
    ax.set_xlim(0, 16.4); ax.set_ylim(0, 9.8); ax.axis("off")
    ax.text(8.2, 9.62, "One card payment, end to end — the ask, the gateway, the receipt, the settlement",
            ha="center", fontsize=15, fontweight="bold", color=INK)

    lanes = [
        (1.20, "Customer"),
        (3.60, "Chat / web\n+ AI parse"),
        (6.30, "Faya payments\ndraft · checkout · ledger"),
        (9.30, "Interswitch\ngateway"),
        (12.15, "Faya webhook\n+ receipt"),
        (14.90, "Faya settlement\nbatch · payout · G2 screen"),
    ]
    for x, name in lanes:
        ax.add_patch(FancyBboxPatch((x - 1.05, 8.78), 2.1, 0.66,
                                    boxstyle="round,pad=0.02,rounding_size=0.10",
                                    facecolor=MINT, edgecolor=GREEN, lw=1.2, zorder=3))
        ax.text(x, 9.11, name, ha="center", va="center", fontsize=8.4,
                fontweight="bold", color=INK, zorder=4)
        ax.plot([x, x], [1.02, 8.78], color=LINE, lw=1.0, ls=(0, (3, 3)), zorder=1)

    def msg(y, x1, x2, text, color=GREY, dashed=False, fs=8.3):
        arrow(ax, x1, y, x2, y, color=color, lw=1.5, ls=(0, (5, 3)) if dashed else "-")
        ax.text((x1 + x2) / 2, y + 0.10, text, ha="center", va="bottom",
                fontsize=fs, color=INK if not dashed else GREY, zorder=4)

    def note(x, y, w, text, face="#ffffff", edge=GREEN, fs=7.9, tc=INK):
        ax.add_patch(FancyBboxPatch((x, y), w, 0.52, boxstyle="round,pad=0.03,rounding_size=0.08",
                                    facecolor=face, edgecolor=edge, lw=1.1, zorder=3))
        ax.text(x + w / 2, y + 0.26, text, ha="center", va="center", fontsize=fs,
                color=tc, zorder=4)

    band(ax, 0.25, 4.42, 15.9, 4.30, "PAYMENT — seconds", face="#fbfdfc")
    msg(8.42, 1.20, 3.60, "1  \"pay kora books 2500\" — typed, photographed or spoken")
    msg(7.98, 3.60, 6.30, "2  parsed intent: merchant + amount (OCR for images · STT for voice)")
    msg(7.54, 6.30, 1.20, "3  draft created · money-in allowance reserved → checkout link (/checkout/{token})", dashed=True)
    msg(7.10, 1.20, 6.30, "4  open checkout · choose Card")
    msg(6.66, 6.30, 9.30, "5  hosted-fields page (/checkout/interswitch/{ref}) — PAN stays in the gateway's iframes")
    msg(6.22, 1.20, 9.30, "6  card details + pay (inside the gateway's secure fields)")
    msg(5.78, 9.30, 12.15, "7  POST /webhooks/interswitch (signed)")
    note(10.35, 5.12, 3.6, "8  signature verified · recorded idempotently — the webhook is not trusted", face="#f3f7f5", edge=LINE, fs=7.7)
    msg(4.86, 12.15, 6.30, "9  VerifyAndApply: requery is authoritative")
    msg(4.56, 6.30, 9.30, "10  requery payment status")
    msg(4.50, 9.30, 6.30, "", dashed=True)
    note(3.62, 3.86, 5.4, "11  requery returns succeeded → payment marked succeeded · ledger splits (platform fee + merchant payable) · receipt outbox event", face=MINT, fs=7.7)
    msg(3.86, 12.15, 1.20, "12  receipt message → /receipts/{token}", dashed=True)

    band(ax, 0.25, 0.62, 15.9, 3.00, "SETTLEMENT — later (the merchant's balance becomes money in their bank)", face="#fdfbf4")
    note(11.55, 2.62, 4.3, "13  batch cut: 3100 merchant_payable → 3200 settlement_payable", face="#ffffff", edge=GREY, fs=7.7)
    note(11.55, 2.02, 4.3, "14  payout queued · money-out allowance reserved (batch-keyed, replay-safe)", face="#ffffff", edge=GREY, fs=7.7)
    note(11.55, 1.42, 4.3, "15  G2 screen the destination — clear → dispatch · possible/strong → HELD (allowance stays reserved) · blocked → interdicted", face=AMBERBG, edge=AMBER, fs=7.7)
    msg(1.02, 14.90, 9.30, "16  NIP payout — only past a clear screen")
    msg(0.78, 9.30, 14.90, "17  completed → ledger 3200 → operating bank", dashed=True)
    ax.text(0.45, 0.42, "A hold parks the payout until a reviewer releases it (screening marked manually cleared) or rejects it (payout reversed, reservation released, batch reopens); a screener outage holds — it never clears. Receipts and checkout links are served by the app (SMTP/WhatsApp delivery).",
            fontsize=7.7, color=GREY, ha="left")
    fig.savefig(path, bbox_inches="tight", facecolor="white")
    plt.close(fig)

# -------------------------------------------------------------------- docx --
def esc(t): return t.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")

def parse_inline(s):
    s = re.sub(r"\[([^\]]+)\]\(([^)]+)\)", r"\1", s)
    segs = []
    for part in re.split(r"(\*\*.+?\*\*)", s):
        if part.startswith("**") and part.endswith("**") and len(part) > 4:
            for sub in re.split(r"(`[^`]+`)", part[2:-2]):
                if len(sub) > 2 and sub.startswith("`"): segs.append((sub[1:-1], True, True))
                elif sub: segs.append((sub, True, False))
        else:
            for sub in re.split(r"(`[^`]+`)", part):
                if len(sub) > 2 and sub.startswith("`"): segs.append((sub[1:-1], False, True))
                elif sub: segs.append((sub, False, False))
    return segs

def rpr(b, c):
    p = ""
    if b: p += "<w:b/>"
    if c: p += '<w:rFonts w:ascii="Consolas" w:hAnsi="Consolas"/>'
    return "<w:rPr>%s</w:rPr>" % p if p else ""

def run(t, b=False, c=False):
    return '<w:r>%s<w:t xml:space="preserve">%s</w:t></w:r>' % (rpr(b, c), esc(t))

def para(style, segs, ind=None, before=0, after=160):
    p = "<w:pPr>"
    if style: p += '<w:pStyle w:val="%s"/>' % style
    if ind is not None: p += '<w:ind w:left="%d"/>' % ind
    p += '<w:spacing w:before="%d" w:after="%d"/>' % (before, after)
    p += "</w:pPr>"
    return "<w:p>%s%s</w:p>" % (p, "".join(run(t, b, c) for (t, b, c) in segs))

def cell_para(segs, hdr):
    body = "".join(run(t, b or hdr, c) for (t, b, c) in segs)
    return '<w:p><w:pPr><w:spacing w:before="20" w:after="20"/></w:pPr>%s</w:p>' % body

def table(rows):
    n = max(len(r) for r in rows)
    widths = {2: (2800, 6560), 3: (2200, 3600, 3560), 4: (1900, 2900, 2400, 2160),
              5: (1700, 2100, 1900, 1900, 1760)}.get(n, [int(9360 / n)] * n)
    w = ['<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/><w:tblW w:w="9360" w:type="dxa"/></w:tblPr>',
         "<w:tblGrid>" + "".join('<w:gridCol w:w="%d"/>' % wd for wd in widths) + "</w:tblGrid>"]
    for ri, row in enumerate(rows):
        w.append("<w:tr>" + "".join(
            '<w:tc><w:tcPr><w:tcW w:w="%d" w:type="dxa"/>%s</w:tcPr>%s</w:tc>' %
            (widths[ci], "<w:shd w:val=\"clear\" w:fill=\"EDF5F1\"/>" if ri == 0 else "", cell_para(parse_inline(c), ri == 0))
            for ci, c in enumerate(row)) + "</w:tr>")
    w.append("</w:tbl>")
    return "".join(w)

def image_para(rid, cx, cy):
    return ('<w:p><w:pPr><w:jc w:val="center"/><w:spacing w:before="120" w:after="200"/></w:pPr><w:r><w:drawing>'
            '<wp:inline distT="0" distB="0" distL="0" distR="0" '
            'xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing">'
            '<wp:extent cx="%d" cy="%d"/><wp:docPr id="901" name="Figure"/>'
            '<wp:graphic xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">'
            '<wp:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture">'
            '<pic:pic xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture">'
            '<pic:nvPicPr><pic:cNvPr id="901" name="Figure"/><pic:cNvPicPr/></pic:nvPicPr>'
            '<pic:blipFill><a:blip r:embed="%s"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill>'
            '<pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="%d" cy="%d"/></a:xfrm>'
            '<a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr></pic:pic></wp:graphicData>'
            '</wp:graphic></wp:inline></w:drawing></w:r></w:p>'
            % (cx, cy, rid, cx, cy))

def build_docx(body_xml, images):
    src = zipfile.ZipFile(SRC_DOCX)
    xmlns = ('xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" '
             'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" '
             'xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math" '
             'xmlns:wp14="http://schemas.microsoft.com/office/word/2010/wordprocessingDrawing"')
    document = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
                '<w:document %s><w:body>%s%s</w:body></w:document>'
                % (xmlns, body_xml,
                   re.search(r"<w:sectPr.*?</w:sectPr>", src.read("word/document.xml").decode("utf8"), re.S).group(0)))
    rels = ['<?xml version="1.0" encoding="UTF-8" standalone="yes"?>',
            '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">',
            '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>',
            '<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/settings" Target="settings.xml"/>',
            '<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/fontTable" Target="fontTable.xml"/>',
            '<Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="theme/theme1.xml"/>']
    for i, _ in enumerate(images):
        rels.append('<Relationship Id="rId100%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/fig%d.png"/>' % (i + 1, i + 1))
    rels.append("</Relationships>")
    ct = src.read("[Content_Types].xml").decode("utf8")
    if "png" not in ct:
        ct = ct.replace("</Types>", '<Default Extension="png" ContentType="image/png"/></Types>')
    with zipfile.ZipFile(OUT_DOCX, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("[Content_Types].xml", ct)
        z.writestr("_rels/.rels", src.read("_rels/.rels"))
        z.writestr("word/document.xml", document)
        z.writestr("word/_rels/document.xml.rels", "".join(rels))
        for name in ("word/styles.xml", "word/settings.xml", "word/fontTable.xml",
                     "word/webSettings.xml", "word/theme/theme1.xml"):
            try: z.writestr(name, src.read(name))
            except KeyError: pass
        for i, p in enumerate(images):
            with open(p, "rb") as f:
                z.writestr("word/media/fig%d.png" % (i + 1), f.read())

def md_to_body(md, images):
    body, lines, i = [], md.split("\n"), 0
    while i < len(lines):
        ln = lines[i]
        if ln.startswith("![FIG:"):
            idx = int(ln[6:].strip("[] \t"))
            from PIL import Image
            im = Image.open(images[idx])
            cx = 6_050_000
            cy = int(cx * im.size[1] / im.size[0])
            body.append(image_para("rId100%d" % (idx + 1), cx, cy))
        elif ln.startswith("### "): body.append(para("Heading3", parse_inline(ln[4:])))
        elif ln.startswith("## "): body.append(para("Heading2", parse_inline(ln[3:]), before=240))
        elif ln.startswith("# "): body.append(para("Heading1", parse_inline(ln[2:])))
        elif ln.startswith("|"):
            rows = []
            while i < len(lines) and lines[i].startswith("|"):
                cells = [c.strip() for c in lines[i].strip("|").split("|")]
                if not set("".join(cells)) <= set("-: "):
                    rows.append(cells)
                i += 1
            body.append(table(rows)); continue
        elif ln.startswith("- "): body.append(para(None, parse_inline(ln[2:]), ind=360, after=80))
        elif ln.strip(): body.append(para(None, parse_inline(ln)))
        i += 1
    return "".join(body)

DOC = """
# Faya — onboarding and payments journey
Individuals and businesses, from first contact to the highest KYC/KYB tier: how onboarding works, how payments move, the external services involved, and the Trust Fabric and AML/CFT controls around it all. Every statement reflects the system as built; tiers that only repeat a lower tier with the same evidence are omitted.

## 1. The journey at a glance
![FIG:0]

Onboarding is one ladder per account type, climbed by verified evidence — never by request alone. Amber gates are enforced in code: the required evidence keys must be present (and, for businesses, reviewer-verified against the submitted documents) before a tier advances. The ceilings shown are the platform's configured defaults (per movement / daily / monthly); the CBN correlation caps for individuals sit at or above them, and Tier 3 is uncapped by the CBN — the platform's own ceilings and absolute payment bounds still apply.

## 2. Onboarding, step by step
### 2.1 Entry
A customer joins on WhatsApp, Telegram, Instagram or TikTok, or opens a browser checkout link. The channel identity is confirmed and the email verified with a code (resend cooldown and lockout included). That alone establishes **L0**: a wallet-capable, low-ceiling account.

### 2.2 Individual ladder (L0 to L3)
- **L0 to L1 — CBN Tier 1.** One aggregated message collects the Tier 1 data set: legal name, date of birth (18+), gender, phone and residential address. No more one-field-per-message chains. Platform ceilings: ₦50,000 / ₦50,000 / ₦300,000 — inside the CBN Tier 1 caps of ₦50,000 / ₦200,000 / ₦500,000.
- **L1 to L2 — CBN Tier 2.** A government photo ID (passport, driver's licence, national ID or voter's card) plus electronic BVN matching against the NIBSS database and NIN matching against the NIMC database. Platform ceilings: ₦200,000 / ₦200,000 / ₦500,000 — inside the CBN Tier 2 caps of ₦200,000 / ₦500,000 / ₦3,000,000.
- **L2 to L3 — CBN Tier 3.** Proof of residential address (a utility bill read structurally, or digital geolocation confirmation within the configured tolerance) and enhanced due diligence: a real-time liveness confirmation and a source-of-funds declaration, recorded through the EDD step (the live biometrics vendor is part of the identity purchase). Platform ceilings: ₦1,000,000 / ₦1,000,000 / ₦10,000,000; the CBN sets no Tier 3 cap, so only the platform's ceilings and absolute payment bounds apply.

### 2.3 Business ladder (B0 to B3)
- **B0 to B1.** The corporate package, submitted as documents through the chat form or the browser KYB flow: CAC certificate, TIN certificate, beneficial-ownership declaration, operating address (with a browser-confirmed location step) and SCUML registration. Each document is reviewed individually; the tier advance requires all five evidence keys **verified**, so an upgrade request alone can never create a tier. This is the corporate hard gate. Ceilings: ₦1m / ₦1m / ₦5m.
- **B1 to B2.** The settlement bank account is registered and reviewer-verified against the business identity before payouts can flow to it; the Interswitch account-name-enquiry adapter backs the live check. Ceilings: ₦5m / ₦5m / ₦50m.
- **B2 to B3.** Business enhanced due diligence — beneficial owners and source of funds — approved by a reviewer. Ceilings: ₦10m / ₦10m / ₦100m.

A business owner also climbs the individual ladder: the payout risk step-up reads the owner's personal risk band, so the two ladders reinforce each other.

### 2.4 What can stop or pause a climb
- **Screening.** Every profile is screened against watchlists with fuzzy matching; the decision vocabulary is clear / possible / strong / blocked. A blocking decision halts advancement and opens a review case. The platform's own internal watchlist outranks every vendor: a listed identity is never auto-cleared.
- **PEP hits.** A politically-exposed-person determination opens one enhanced-due-diligence case that blocks every tier advancement until a senior manager approves or rejects it — an audited act in its own right, distinct from ordinary reviewer dispositions.
- **Risk band.** A high ML/FT risk band caps the single-movement ceiling and requires EDD before higher-value activity. Old risk observations decay over time, and a false-positive alert writes a compensating event so the score does not ratchet.
- **Rescreening.** Profiles are re-screened on a configured period, so a once-clear customer does not stay clear by default.

## 3. Payments
![FIG:1]

### 3.1 Money in
The customer asks in plain language — "pay ade kitchen 2,500" as text, a photo of a bill, or a voice note — in chat or in the web checkout's AI ask-bar. The AI provider turns images into text (OCR) and voice into text (STT); the parser extracts merchant, amount and intent server-side, then the stepper flow confirms: choose (or confirm the pre-selected) recipient, review, pay.
- **Card** — Interswitch hosted fields: card data lives in the gateway's iframes and never touches Faya's servers.
- **Bank transfer** — a one-time virtual account is issued; the webhook credit is applied to the wallet or payment exactly once, idempotently.
- **Wallet** — a ledger-backed balance, topped up by card or transfer, spendable at any merchant or to anyone.

### 3.2 Money out — the screening gate
Settlement payouts to merchants, individual-pay recipient payouts and wallet withdrawals all pass the same pre-flight gate inside the window where the movement's allowance is already reserved:
- **clear** — the movement dispatches to the Interswitch NIP rail;
- **possible / strong** — the movement is **held** for review with the allowance still reserved: the money cannot be re-attempted past the screen, and the reviewer releases or rejects it from the queue (a release marks the screening manually cleared; a rejection reverses the payout and reopens the batch);
- **blocked** — the movement is interdicted outright;
- **vendor unavailable** — the movement holds rather than clears: an outage never becomes an approval.
Payout destinations are screened per destination account, so re-pointing a payout to a different bank account always re-screens the new counterparty.

### 3.3 One card payment, end to end
![FIG:2]

The figure traces one card payment from the first message to the money landing in the merchant's bank account. Three properties are load-bearing: the payment draft reserves the payer's money-in allowance before any gateway interaction, so the ceiling check cannot be raced; the card data never leaves Interswitch's hosted fields; and the webhook is treated as untrusted — it only triggers a requery, and the requery is what marks the payment succeeded, splits the ledger and emits the receipt. Settlement happens later: the batch cut freezes the merchant's balance, the payout reserves its money-out allowance, and the G2 gate screens the destination before any NIP dispatch. A hold parks the payout with the allowance still reserved until a reviewer releases or rejects it; a blocked counterparty is interdicted outright.

### 3.4 The controls around every payment
- **Allowance ladder.** Every movement is validated against the subject's tier ceilings — single, daily, monthly, in and out — inside one atomic reservation; replays never double-count.
- **Transaction monitoring.** Rules over every movement raise alerts (structuring around the reporting threshold, velocity, high value); the alert queue filters by rail and links to the exact payment, credit, refund or payout behind the alert.
- **Reviewer queue.** Alerts become cases with dispositions; outcomes feed back as data — a false-positive cancels its risk contribution, a confirmed finding can freeze the whole network ring in one action.
- **Regulator reporting.** STRs (24-hour clock from the determination of suspicion), CTRs and the PEP registry (by the 7th of the following month) are generated from audited data with filing clocks, overdue surfacing, and CSV plus goAML XML downloads. The audit log itself is hash-chained and re-verified on a schedule, with a reviewer disposition if the chain ever breaks.

## 4. External services
| Service | Used for | Individual journey | Business journey | Payments |
|---|---|---|---|---|
| Chat channels (WhatsApp, Telegram, Instagram, TikTok) | Onboarding, commands, media collection | Onboarding, payments, statements | Onboarding, orders, settlement notices | Inbound instructions (text/image/voice) |
| AI provider (OpenAI-compatible) | OCR of images, STT of voice, chat parsing | Bill photos, voice notes | CAC/TIN/utility-bill document OCR | Ask-bar and conversational payment parsing |
| Identity vendor (NIN/BVN portal) | Electronic identity matching | BVN (NIBSS) and NIN (NIMC) at L2 | Owner identity where required | — |
| Screening vendor (watchlists / PEP) | Customer and payment screening, fuzzy matching | Onboarding screening, rescreening, PEP EDD input | Owner and UBO screening | G2 counterparty screening (simulated until signed) |
| CAC corporate registry | Company existence and status | — | B1 corporate hard gate | — |
| FIRS TIN service | Tax identification verification | — | B1 corporate hard gate | — |
| Interswitch account name enquiry | Bank account name resolution | — | B2 settlement verification (adapter ready; reviewer gate today) | Payout destination correctness |
| Location / geo service | Address confirmation | L2-L3 address proof | B1 operating address | — |
| Liveness / EDD step (simulated today) | EDD presence and source of funds | L3 EDD | B3 business EDD | — |
| Email (SMTP) | Verification codes, notifications | L0 email confirmation | Merchant admin accounts | Receipts and notifications |
| Interswitch | Card gateway, virtual accounts, NIP payouts/refunds | Funding and withdrawals | Collections and settlement payouts | All money movement |
| NFIU (goAML) | Regulatory filing destination | STR/CTR/PEP rows as applicable | As applicable | goAML XML filing |

## 5. The Trust Fabric
The five layers turn isolated events into decisions; each layer is live in the platform today.
| Layer | Question it answers | What exists today |
|---|---|---|
| 1 · Identity | Who is this entity? | The KYC/KYB ladders above: evidence keys, verified documents, reviewer dispositions |
| 2 · Signal | What is happening around it? | Trusted-device capture on sign-in and checkout, channel identity, risk events with time decay |
| 3 · Decision | What should the platform do? | Risk bands that cap ceilings and force EDD, screening gates, allowance enforcement, G2 holds |
| 4 · Network | How is it connected? | Entity-link analysis over shared devices, bank accounts and channel identities; ring detection with weighted, recency-scored edges; one-case-covers-the-ring reviewer tooling with allowance freezes |
| 5 · Intelligence | Is the system working? | Trust KPIs on the admin dashboard (median alert resolution, false-positive rate), the rule-tuning queue, and annual independent-validation attestations |

## 6. AML/CFT controls by journey stage
| Stage | Control | Where it lives |
|---|---|---|
| Onboarding | Simplified DD (L1/B1) to enhanced DD (L3/B3) by tier | KYC/KYB ladders, evidence keys |
| Onboarding | Watchlist + PEP screening with fuzzy matching; internal watchlist precedence | Screening lane, `record screening` path |
| Onboarding | PEP EDD with senior-management approval; block while open | pep_edd case type, tier-advance gate |
| Transaction | Tier ceilings enforced atomically per movement | Allowance ladder (C9) |
| Transaction | Payment-time counterparty screening: block / hold / clear | G2 gate on payouts and withdrawals |
| Transaction | Monitoring rules and the alert-to-case pipeline | Transaction monitor, reviewer queue |
| Network | Ring detection across shared devices/accounts; one disposition covers the ring | Trust network view, network cases |
| Governance | Hash-chained audit log, scheduled chain verification, 5-year retention floor | Audit chain, retention policy |
| Governance | Annual independent-validation attestation | Compliance attestations page |
| Reporting | STR 24h / CTR monthly clocks, goAML XML | Filing clocks, goAML encoders |

## 7. The highest tiers in one paragraph
An **L3 individual** or **B3 business** is the platform's fully-verified customer: government ID plus BVN and NIN (L2), then address proof and liveness-based EDD with source of funds (L3) — or the full corporate package, verified settlement account, and business EDD (B3). At the top tier the CBN sets no Tier 3 cap — the platform's default ceilings (₦1m / ₦1m / ₦10m) and the absolute payment bounds still apply, and B3 caps at ₦10m single / ₦100m monthly by platform policy — but everything they do still rides the same gates as everyone else: screening on themselves and on every counterparty they pay, monitoring on every movement, and the Trust Fabric scoring in the background. Reaching the top tier removes friction; it never removes oversight.
"""

if __name__ == "__main__":
    fig1 = os.path.join(TMP, "fig_onboarding.png")
    fig2 = os.path.join(TMP, "fig_payments.png")
    fig3 = os.path.join(TMP, "fig_sequence.png")
    onboarding_diagram(fig1)
    payments_diagram(fig2)
    sequence_diagram(fig3)
    images = [fig1, fig2, fig3]
    body = md_to_body(DOC, images)
    build_docx(body, images)
    print("wrote", OUT_DOCX)
