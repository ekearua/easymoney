#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Render an AWS Phase-1 estimate PDF for eu-north-1, NAT Gateway included.

The AWS Pricing Calculator export in docs/cost-models/Aws Pricing Estimate.pdf
was priced for Africa (Cape Town) = af-south-1 and omitted the NAT gateway,
public IPv4 addresses and EBS snapshots. This script rebuilds the estimate for
the project's assigned Region (eu-north-1) using rates pulled from the AWS
Price List API, so the number reflects what deploy/aws/setup-phase1.sh
actually creates.

Rates below were read from the public price list on 2026-10-22:
  https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonEC2/current/eu-north-1/index.json
  https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonVPC/current/eu-north-1/index.json
Re-run the `aws pricing get-products` queries noted next to each rate to
refresh them; usage types are stable.

Usage:
    python scripts/render_aws_estimate_pdf.py [pdf_path]
"""
import html
import io
import os
import shutil
import subprocess
import sys
import tempfile
from datetime import date
from urllib.parse import quote

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
COST_MODELS = os.path.join(REPO, "docs", "cost-models")
DEFAULT_PDF = os.path.join(COST_MODELS, "Aws Pricing Estimate (eu-north-1, incl NAT).pdf")

REGION_LABEL = "EU (Stockholm)"
REGION_CODE = "eu-north-1"
HOURS = 730.0

# --- rates (USD), eu-north-1, On-Demand, Linux, Shared tenancy --------------
# usagetype EUN1-BoxUsage:t3.medium
T3_MEDIUM_HR = 0.0432
# usagetype EUN1-BoxUsage:m6i.large
M6I_LARGE_HR = 0.1020
# usagetype EUN1-EBS:VolumeUsage.gp3 (baseline 3000 IOPS / 125 MiB/s included)
GP3_GB_MO = 0.0836
# usagetype EUN1-EBS:SnapshotUsage
SNAPSHOT_GB_MO = 0.0475
# usagetype EUN1-NatGateway-Hours
NAT_HR = 0.0460
# usagetype EUN1-NatGateway-Bytes
NAT_GB = 0.0460
# usagetype EUN1-PublicIPv4:InUseAddress
PUBLIC_IPV4_HR = 0.0050

# --- assumptions -----------------------------------------------------------
APP_COUNT = 2
APP_ROOT_GB = 40
DB_ROOT_GB = 50
DB_DATA_GB = 240
NAT_DATA_GB_MO = 100          # egress through the NAT gateway
SNAPSHOT_COUNT_MO = 60        # 2x daily
SNAPSHOT_DELTA_GB = 3         # changed blocks per snapshot

CHROME_CANDIDATES = [
    r"C:\Program Files\Google\Chrome\Application\chrome.exe",
    r"C:\Program Files (x86)\Google\Chrome\Application\chrome.exe",
    r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
    r"C:\Program Files\Microsoft\Edge\Application\msedge.exe",
    "chrome",
    "msedge",
]


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


def money(v):
    return "{:,.2f}".format(v)


def build_lines():
    app_hours = APP_COUNT * HOURS * T3_MEDIUM_HR
    db_hours = HOURS * M6I_LARGE_HR
    app_root = APP_COUNT * APP_ROOT_GB * GP3_GB_MO
    db_root = DB_ROOT_GB * GP3_GB_MO
    db_data = DB_DATA_GB * GP3_GB_MO
    snap_gb = SNAPSHOT_COUNT_MO * SNAPSHOT_DELTA_GB
    snapshots = snap_gb * SNAPSHOT_GB_MO
    nat_hours = HOURS * NAT_HR
    nat_data = NAT_DATA_GB_MO * NAT_GB
    public_ips = 2 * HOURS * PUBLIC_IPV4_HR

    return [
        {
            "name": "Amazon EC2",
            "desc": "Faya - 1 (app hosts)",
            "monthly": app_hours,
            "config": ("Tenancy (Shared Instances), Operating system (Linux), Workload "
                       "(Consistent, Number of instances: %d), EC2 instance (t3.medium), "
                       "Pricing strategy (On-Demand, 100%% Utilized/Month), Monitoring "
                       "(disabled), EBS Storage amount (%d GB per instance), "
                       "DT Inbound/Outbound (not selected)"
                       % (APP_COUNT, APP_ROOT_GB)),
        },
        {
            "name": "Amazon EC2",
            "desc": "Phase 1 - database host",
            "monthly": db_hours,
            "config": ("Tenancy (Shared Instances), Operating system (Linux), Workload "
                       "(Consistent, Number of instances: 1), EC2 instance (m6i.large), "
                       "Pricing strategy (On-Demand, 100%% Utilized/Month), Monitoring "
                       "(disabled), EBS Storage amount (%d GB), DT Inbound/Outbound "
                       "(not selected)" % DB_ROOT_GB),
        },
        {
            "name": "Amazon Elastic Block Store (EBS)",
            "desc": "App root volumes (gp3)",
            "monthly": app_root,
            "config": ("Number of volumes (%d), Average duration of volume (%d hours per "
                       "month), Storage amount per volume (%d GB), Volume type "
                       "(General Purpose SSD gp3), Provisioned IOPS (3000), Throughput "
                       "(125 MBps)" % (APP_COUNT, HOURS, APP_ROOT_GB)),
        },
        {
            "name": "Amazon Elastic Block Store (EBS)",
            "desc": "Database root volume (gp3)",
            "monthly": db_root,
            "config": ("Number of volumes (1), Average duration of volume (%d hours per "
                       "month), Storage amount per volume (%d GB), Volume type "
                       "(General Purpose SSD gp3), Provisioned IOPS (3000), Throughput "
                       "(125 MBps)" % (HOURS, DB_ROOT_GB)),
        },
        {
            "name": "Amazon Elastic Block Store (EBS)",
            "desc": "Database data volume (gp3) - PostgreSQL data + WAL",
            "monthly": db_data,
            "config": ("Number of volumes (1), Average duration of volume (%d hours per "
                       "month), Storage amount per volume (%d GB), Volume type "
                       "(General Purpose SSD gp3), Provisioned IOPS (3000), Throughput "
                       "(125 MBps), Encrypted (yes), Delete on termination (no)"
                       % (HOURS, DB_DATA_GB)),
        },
        {
            "name": "Amazon Elastic Block Store (EBS)",
            "desc": "EBS snapshots - 2x daily",
            "monthly": snapshots,
            "config": ("Snapshot frequency (%dx daily), Amount changed per snapshot "
                       "(%d GB), Retained snapshot data (%d GB-month), Storage type "
                       "(Standard snapshot)" % (2, SNAPSHOT_DELTA_GB, snap_gb)),
        },
        {
            "name": "Amazon Virtual Private Cloud",
            "desc": "NAT Gateway - hours (added)",
            "monthly": nat_hours,
            "config": ("NAT gateway (1), Average duration (%d hours per month), "
                       "Availability Zone usage (1)" % HOURS),
        },
        {
            "name": "Amazon Virtual Private Cloud",
            "desc": "NAT Gateway - data processed (added)",
            "monthly": nat_data,
            "config": ("Data processed through NAT gateway (%d GB per month), "
                       "Rate (%.4f USD per GB)" % (NAT_DATA_GB_MO, NAT_GB)),
        },
        {
            "name": "Amazon Virtual Private Cloud",
            "desc": "Public IPv4 addresses (added)",
            "monthly": public_ips,
            "config": ("In-use public IPv4 addresses (2: edge Elastic IP + NAT gateway "
                       "Elastic IP), Average duration (%d hours per month), "
                       "Rate (%.4f USD per hour)" % (HOURS, PUBLIC_IPV4_HR)),
        },
    ]


def build_html(lines):
    total = sum(l["monthly"] for l in lines)
    twelve = total * 12

    o = io.StringIO()
    o.write("""<!doctype html><html><head><meta charset="utf-8">
<title>AWS Pricing Estimate - Phase 1 (eu-north-1)</title>
<style>
@page { size: A4; margin: 14mm 12mm; }
* { box-sizing: border-box; }
body { font-family: "Amazon Ember", "Helvetica Neue", Arial, sans-serif;
       color: #16191f; font-size: 10.5px; line-height: 1.45; margin: 0; }
.top { display: flex; border-bottom: 2px solid #232f3e; padding-bottom: 8px; margin-bottom: 14px; }
.top .l { flex: 1; }
.brand { font-size: 17px; font-weight: 700; color: #232f3e; letter-spacing: -0.2px; }
.brand span { color: #ff9900; }
.sub { font-size: 11px; color: #545b64; margin-top: 1px; }
.top .r { text-align: right; font-size: 9px; color: #545b64; margin-top: 2px; }
h2 { font-size: 13px; margin: 16px 0 7px; color: #232f3e;
     border-bottom: 1px solid #d5dbdb; padding-bottom: 4px; }
.summary { width: 100%%; border-collapse: collapse; margin-bottom: 4px; }
.summary td { border: 1px solid #d5dbdb; padding: 7px 10px; }
.summary td.k { width: 30%%; background: #f2f3f3; color: #545b64; font-size: 9px;
                text-transform: uppercase; letter-spacing: 0.6px; }
.summary td.v { font-weight: 700; font-size: 12px; }
.note { font-size: 9px; color: #545b64; }
.item { border: 1px solid #d5dbdb; border-left: 3px solid #ff9900;
        padding: 8px 10px; margin-bottom: 8px; page-break-inside: avoid; }
.item table { width: 100%%; border-collapse: collapse; }
.item td { padding: 1.5px 0; vertical-align: top; font-size: 9.5px; }
.item td.k { width: 22%%; color: #545b64; }
.item .nm { font-size: 11.5px; font-weight: 700; color: #232f3e; }
.item .cfg { color: #3f4b57; margin-top: 3px; }
.item .price { font-weight: 700; }
.tot { display: flex; border: 1px solid #232f3e; padding: 8px 10px; margin-top: 4px;
       background: #f2f3f3; font-weight: 700; font-size: 11.5px; }
.tot .s { flex: 1; }
.ack { font-size: 8.5px; color: #545b64; border-top: 1px solid #d5dbdb;
       margin-top: 14px; padding-top: 7px; }
footer { position: fixed; bottom: 0; left: 0; right: 0; border-top: 1px solid #d5dbdb;
         padding-top: 4px; font-size: 8.5px; color: #545b64; display: flex; }
footer .s { flex: 1; }
</style></head><body>
<div class="top">
  <div class="l">
    <div class="brand">aws <span>| Pricing Calculator</span></div>
    <div class="sub">Estimate summary &mdash; Phase 1, %s (%s)</div>
  </div>
  <div class="r">Export date: %s<br>Language: English</div>
</div>
""" % (html.escape(REGION_LABEL), html.escape(REGION_CODE), date.today().strftime("%d/%m/%Y")))

    o.write('<h2>Estimate summary</h2>')
    o.write('<table class="summary">')
    o.write('<tr><td class="k">Upfront cost</td><td class="v">0.00 USD</td></tr>')
    o.write('<tr><td class="k">Monthly cost</td><td class="v">%s USD</td></tr>' % money(total))
    o.write('<tr><td class="k">Total 12 months cost</td><td class="v">%s USD</td></tr>' % money(twelve))
    o.write('</table>')
    o.write('<div class="note">Includes upfront cost. Generated from AWS Price List API '
            'rates for %s; no data transfer or Route 53 charges included.</div>'
            % html.escape(REGION_CODE))

    o.write('<h2>Detailed estimate</h2>')
    for line in lines:
        o.write('<div class="item"><table>')
        o.write('<tr><td class="k">Name</td><td colspan="3" class="nm">%s</td></tr>' % html.escape(line["name"]))
        o.write('<tr><td class="k">Group</td><td>-</td><td class="k" style="width:18%%">Region</td><td>%s</td></tr>'
                % html.escape(REGION_LABEL))
        o.write('<tr><td class="k">Upfront cost</td><td>0.00 USD</td>'
                '<td class="k">Monthly cost</td><td class="price">%s USD</td></tr>' % money(line["monthly"]))
        o.write('<tr><td class="k">Status</td><td>-</td><td class="k">Description</td><td>%s</td></tr>'
                % html.escape(line["desc"]))
        o.write('</table><div class="cfg"><b>Config summary:</b> %s</div></div>'
                % html.escape(line["config"]))

    o.write('<div class="tot"><div class="s">Total monthly cost</div><div>%s USD</div></div>' % money(total))
    o.write('<div class="ack">AWS Pricing Calculator provides only an estimate of your AWS '
            'fees and doesn\'t include any taxes that might apply. Your actual fees depend '
            'on a variety of factors, including your actual usage of AWS services.</div>')
    o.write('<footer><div class="s">AWS Pricing Calculator estimate &mdash; %s</div>'
            '<div>Page 1 of 1</div></footer></body></html>\n' % html.escape(REGION_CODE))
    return o.getvalue(), total, twelve


def html_to_pdf(browser, html_path, pdf_path):
    url = "file:///" + quote(os.path.abspath(html_path).replace("\\", "/"))
    profile = tempfile.mkdtemp(prefix="aws-pdf-")
    try:
        proc = subprocess.run([
            browser, "--headless=new", "--disable-gpu", "--no-sandbox",
            "--no-first-run", "--no-pdf-header-footer",
            "--run-all-compositor-stages-before-draw", "--virtual-time-budget=8000",
            "--user-data-dir=" + profile,
            "--print-to-pdf=" + os.path.abspath(pdf_path), url,
        ], capture_output=True, timeout=180)
        if not os.path.exists(pdf_path):
            raise SystemExit("PDF was not produced. stderr:\n%s"
                             % proc.stderr.decode("utf-8", "replace"))
    finally:
        shutil.rmtree(profile, ignore_errors=True)


def main():
    pdf_path = sys.argv[1] if len(sys.argv) > 1 else DEFAULT_PDF
    lines = build_lines()
    doc, total, twelve = build_html(lines)

    html_path = os.path.join(tempfile.mkdtemp(prefix="aws-estimate-"), "estimate.html")
    io.open(html_path, "w", encoding="utf-8", newline="").write(doc)
    html_to_pdf(find_browser(), html_path, pdf_path)

    for line in lines:
        print("%8.2f  %-38s %s" % (line["monthly"], line["name"], line["desc"]))
    print("%8.2f  TOTAL monthly" % total)
    print("%8.2f  TOTAL 12 months" % twelve)
    print("pdf    :", pdf_path, "(%d bytes)" % os.path.getsize(pdf_path))


if __name__ == "__main__":
    main()
