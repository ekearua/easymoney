#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Render a Google Cloud Pricing Calculator CSV export into a PDF estimate.

The GCP calculator can export an estimate as CSV (docs/cost-models/*.csv).
That CSV is flat and loses the estimate-summary layout of the PDF the
calculator can also produce, so this script rebuilds the PDF from the CSV:

  docs/cost-models/<name>.csv
      -> docs/cost-models/<name>.html      (intermediate, inspectable)
      -> docs/cost-models/<name>.pdf       (final)

The HTML is printed by a headless Chrome/Edge, so the layout is real HTML/CSS
rather than a hand-rolled PDF writer. No third-party Python packages needed.

Usage:
    python scripts/render_gcp_estimate_pdf.py [csv_path] [pdf_path]

Defaults to the most recent *.csv in docs/cost-models/ and a PDF named after it.
"""
import csv
import html
import io
import os
import shutil
import subprocess
import sys
import tempfile
from datetime import datetime
from urllib.parse import quote

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
COST_MODELS = os.path.join(REPO, "docs", "cost-models")

# Quantity units are not carried in the CSV export, so they are recovered
# from the SKU. Anything unknown falls back to a plain unit count.
SKU_UNITS = {
    "868F-5971-152F": "Hours",        # vCPU
    "855D-C33E-F06B": "GB-Hours",     # RAM
    "104E-81CB-48E3": "GB-Months",    # Balanced PD
}

CHROME_CANDIDATES = [
    r"C:\Program Files\Google\Chrome\Application\chrome.exe",
    r"C:\Program Files (x86)\Google\Chrome\Application\chrome.exe",
    r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
    r"C:\Program Files\Microsoft\Edge\Application\msedge.exe",
    "chrome",
    "msedge",
]


def find_csv():
    if len(sys.argv) > 1:
        return sys.argv[1]
    cands = [os.path.join(COST_MODELS, f) for f in os.listdir(COST_MODELS)
             if f.lower().endswith(".csv")]
    if not cands:
        raise SystemExit("no CSV export found in %s" % COST_MODELS)
    return max(cands, key=os.path.getmtime)


def find_browser():
    for c in CHROME_CANDIDATES:
        if os.path.sep in c:
            if os.path.exists(c):
                return c
        else:
            found = shutil.which(c)
            if found:
                return found
    raise SystemExit("no Chrome/Edge binary found for PDF printing")


def money(value):
    """Format a float the way the calculator does: 2dp with thousands sep."""
    if value is None:
        return "-"
    return "{:,.2f}".format(float(value))


def qty_label(quantity, sku):
    raw = (quantity or "").strip()
    if not raw:
        return "-"
    try:
        num = float(raw)
    except ValueError:
        return raw
    shown = ("%g" % num)
    unit = SKU_UNITS.get((sku or "").strip(), "")
    return ("%s %s" % (shown, unit)).strip()


def short_name(name):
    """'E2 Custom Instance Core running in Johannesburg' -> 'E2 Custom Instance Core'."""
    text = (name or "").strip()
    for suffix in (" running in ", " in "):
        idx = text.find(suffix)
        if idx > 0 and ("running" in text[idx:] or "Region" in text[idx:]):
            text = text[:idx]
            break
    return text


def parse_csv(path):
    """Split the flat export into items, total, effective date, url, disclaimer."""
    with io.open(path, encoding="utf-8-sig", newline="") as fh:
        rows = list(csv.reader(fh))

    header = rows[0] if rows else []
    items, total, effective, url, disclaimer = [], None, None, None, None

    for row in rows[1:]:
        cells = [(c or "").strip() for c in row]
        if not any(cells):
            continue

        if cells[0].startswith("Prices are in US dollars"):
            effective = cells[0]
            continue
        if cells[0].startswith("The estimated fees provided by Google Cloud"):
            disclaimer = cells[0]
            continue
        if cells[0].startswith("Url to the estimate"):
            url = next((c for c in cells[1:] if c.startswith("http")), None)
            continue

        # The total row carries its label in the second-to-last column and the
        # label keeps its trailing colon ("Total Price:"), so match on the
        # colon-stripped value rather than the raw cell.
        if any(c.rstrip(": ") == "Total Price" for c in cells):
            value = next((c for c in cells if c and c.replace(".", "", 1).isdigit()), None)
            if value is not None:
                total = float(value)
                continue

        # An item row has a service and a price.
        if cells[0] and any(c for c in cells[2:]):
            items.append({
                "service": cells[0],
                "name": cells[1],
                "quantity": cells[2],
                "region": cells[3],
                "service_id": cells[4],
                "sku": cells[5],
                "price": cells[6] if len(cells) > 6 else "",
                "notes": cells[7] if len(cells) > 7 else "",
            })

    return header, items, total, effective, url, disclaimer


def build_html(items, total, effective, url, disclaimer, source_name):
    total = total or sum(float(i["price"]) for i in items if i["price"])

    effective_dt = None
    if effective and "effective date is" in effective:
        stamp = effective.split("effective date is", 1)[1].strip()
        try:
            effective_dt = datetime.strptime(stamp, "%Y-%m-%dT%H:%M:%S.%fZ")
        except ValueError:
            try:
                effective_dt = datetime.strptime(stamp, "%Y-%m-%dT%H:%M:%SZ")
            except ValueError:
                effective_dt = None
    export_date = effective_dt.strftime("%Y-%m-%d") if effective_dt else ""

    o = io.StringIO()
    o.write("""<!doctype html><html><head><meta charset="utf-8">
<title>Google Cloud Pricing Calculator Estimate</title>
<style>
@page { size: A4; margin: 14mm 12mm; }
* { box-sizing: border-box; }
body { font-family: "Google Sans", Roboto, Arial, Helvetica, sans-serif;
       color: #202124; margin: 0; font-size: 10.5px; line-height: 1.5; }
.wordmark { font-size: 21px; font-weight: 500; letter-spacing: -0.4px; color: #5f6368; }
.wordmark b { color: #4285f4; font-weight: 500; }
h1 { font-size: 15px; font-weight: 500; color: #202124; margin: 2px 0 0; }
.head-row { display: flex; align-items: flex-end; }
.head-row .left { flex: 1; }
.ref { text-align: right; font-size: 11px; color: #5f6368; padding-bottom: 2px; }
.rule { height: 3px; background: linear-gradient(90deg,#4285f4 0 25%,#ea4335 25% 50%,#fbbc04 50% 75%,#34a853 75% 100%);
        margin: 12px 0 16px; border-radius: 2px; }
.meta { display: flex; gap: 18px; margin-bottom: 18px; }
.meta div { flex: 1; border-left: 2px solid #dadce0; padding-left: 10px; }
.meta .k { font-size: 8.5px; letter-spacing: 0.8px; color: #5f6368; text-transform: uppercase; }
.meta .v { font-size: 10px; color: #202124; word-break: break-all; }
h2 { font-size: 12px; font-weight: 500; color: #202124; margin: 0 0 8px; }
.summary { display: flex; gap: 12px; margin-bottom: 20px; }
.tile { flex: 1; border: 1px solid #dadce0; border-radius: 6px; padding: 10px 12px; }
.tile .k { font-size: 8.5px; letter-spacing: 0.8px; color: #5f6368; text-transform: uppercase; }
.tile .v { font-size: 19px; font-weight: 500; color: #202124; margin-top: 2px; }
.tile .v small { font-size: 11px; color: #5f6368; font-weight: 400; }
.item { border: 1px solid #e8eaed; border-left: 3px solid #4285f4; border-radius: 6px;
        padding: 10px 12px; margin-bottom: 10px; }
.item .head { display: flex; }
.item .t { flex: 1; font-size: 11.5px; font-weight: 500; }
.item .p { font-weight: 500; white-space: nowrap; }
.item .p span { font-size: 9.5px; color: #5f6368; font-weight: 400; }
.item .m { font-size: 9.5px; color: #5f6368; margin-top: 3px; }
.item .c { font-size: 9.5px; color: #3c4043; margin-top: 3px; }
.item .c b { font-weight: 500; color: #5f6368; }
.total { border: 1px solid #dadce0; border-radius: 6px; padding: 10px 12px;
         display: flex; font-weight: 500; background: #f8f9fa; margin-top: 4px; }
.total .t { flex: 1; }
.ack { font-size: 8.5px; color: #5f6368; margin-top: 18px; border-top: 1px solid #e8eaed;
       padding-top: 8px; line-height: 1.45; }
footer { position: fixed; bottom: 0; left: 0; right: 0; font-size: 8.5px; color: #5f6368;
         border-top: 1px solid #e8eaed; padding-top: 5px; display: flex; }
footer .s { flex: 1; }
</style></head><body>
<div class="head-row">
  <div class="left">
    <div class="wordmark"><b>Google</b> Cloud</div>
    <h1>Google Cloud Pricing Calculator</h1>
  </div>
  <div class="ref">Estimate Reference</div>
</div>
<div class="rule"></div>
""")

    o.write('<div class="meta">')
    o.write('<div><div class="k">Export date / Effective date</div><div class="v">%s</div></div>'
            % html.escape(export_date or (effective or "")))
    o.write('<div><div class="k">Currency</div><div class="v">USD ($)</div></div>')
    o.write('<div><div class="k">Estimate URL</div><div class="v">%s</div></div>'
            % (("<a href=\"%s\" style=\"color:#1a73e8\">%s</a>" % (html.escape(url), html.escape(url)))
               if url else "-"))
    o.write('</div>')

    o.write('<h2>Estimate Summary</h2><div class="summary">')
    o.write('<div class="tile"><div class="k">Upfront cost</div><div class="v">0.00 <small>USD</small></div></div>')
    o.write('<div class="tile"><div class="k">Total monthly cost</div><div class="v">%s <small>USD</small></div></div>'
            % money(total))
    o.write('<div class="tile"><div class="k">Total 12 months cost</div><div class="v">%s <small>USD</small></div></div>'
            % money(total * 12))
    o.write('</div>')

    o.write('<h2>Detailed Cost Breakdown</h2>')
    for item in items:
        title = "%s - %s" % (item["service"], short_name(item["name"]))
        o.write('<div class="item">')
        o.write('<div class="head"><div class="t">%s</div><div class="p">%s <span>USD / mo</span></div></div>'
                % (html.escape(title), money(item["price"])))
        o.write('<div class="m">Region: %s &nbsp;&middot;&nbsp; Service ID: %s &nbsp;&middot;&nbsp; SKU: %s</div>'
                % (html.escape(item["region"] or "-"),
                   html.escape(item["service_id"] or "-"),
                   html.escape(item["sku"] or "-")))
        o.write('<div class="c"><b>Config summary:</b> Service Display: %s | Quantity: %s | Notes: %s</div>'
                % (html.escape(item["service"]),
                   html.escape(qty_label(item["quantity"], item["sku"])),
                   html.escape(item["notes"] or "-")))
        o.write('</div>')

    o.write('<div class="total"><div class="t">Total Price</div><div class="p">%s USD</div></div>'
            % money(total))

    o.write('<div class="ack">%s</div>' % html.escape(disclaimer or
               "The estimated fees provided by Google Cloud Pricing Calculator are for discussion "
               "purposes only and are not binding on either you or Google. Your actual fees may be "
               "higher or lower than the estimate."))
    if source_name:
        o.write('<div style="font-size:8.5px;color:#5f6368;margin-top:6px">Source export: %s</div>'
                % html.escape(source_name))

    o.write('<footer><div class="s">Google Cloud Pricing Calculator Estimate</div>'
            '<div>Page 1 of 1</div></footer>')
    o.write('</body></html>\n')
    return o.getvalue()


def html_to_pdf(browser, html_path, pdf_path):
    url = "file:///" + quote(os.path.abspath(html_path).replace("\\", "/"))
    profile = tempfile.mkdtemp(prefix="gcp-pdf-")
    cmd = [
        browser,
        "--headless=new",
        "--disable-gpu",
        "--no-sandbox",
        "--no-first-run",
        "--no-pdf-header-footer",
        "--run-all-compositor-stages-before-draw",
        "--virtual-time-budget=8000",
        "--user-data-dir=" + profile,
        "--print-to-pdf=" + os.path.abspath(pdf_path),
        url,
    ]
    try:
        proc = subprocess.run(cmd, capture_output=True, timeout=180)
        if not os.path.exists(pdf_path):
            raise SystemExit("PDF was not produced. stderr:\n%s" % proc.stderr.decode("utf-8", "replace"))
    finally:
        shutil.rmtree(profile, ignore_errors=True)


def main():
    csv_path = find_csv()
    base = os.path.splitext(os.path.basename(csv_path))[0]
    # The HTML is an intermediate render target, not an artifact: keep it in a
    # temp dir so the repository only gains the PDF.
    html_path = os.path.join(tempfile.mkdtemp(prefix="gcp-estimate-"), base + ".html")
    pdf_path = (os.path.abspath(sys.argv[2]) if len(sys.argv) > 2
                else os.path.join(COST_MODELS, base + ".pdf"))

    header, items, total, effective, url, disclaimer = parse_csv(csv_path)
    # The export's total row is authoritative, but a hand-edited or truncated
    # CSV may omit it, so fall back to the sum of the line items.
    total = total or sum(float(i["price"]) for i in items if i["price"])
    io.open(html_path, "w", encoding="utf-8", newline="").write(
        build_html(items, total, effective, url, disclaimer, os.path.basename(csv_path)))

    browser = find_browser()
    html_to_pdf(browser, html_path, pdf_path)

    print("csv   :", csv_path)
    print("html  :", html_path)
    print("pdf   :", pdf_path, "(%d bytes)" % os.path.getsize(pdf_path))
    print("items :", len(items), "| monthly USD %.2f" % (total or 0.0))


if __name__ == "__main__":
    main()
