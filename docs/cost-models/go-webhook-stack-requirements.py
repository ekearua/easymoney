import xlsxwriter

output_path = r"C:\Users\User\Mobile\Whatsapp Payment\docs\cost-models\go-webhook-stack-requirements-v2.xlsx"
wb = xlsxwriter.Workbook(output_path)

# Formats
header_fmt = wb.add_format({'bold': True, 'font_color': 'white', 'bg_color': '#2F5496',
                            'border': 1, 'text_wrap': True, 'align': 'center', 'valign': 'vcenter'})
section_fmt = wb.add_format({'bold': True, 'bg_color': '#D6E4F0', 'border': 1, 'text_wrap': True})
money_fmt = wb.add_format({'num_format': '$#,##0', 'border': 1})
money_bold_fmt = wb.add_format({'num_format': '$#,##0', 'bold': True, 'bg_color': '#D6E4F0', 'border': 1})
num_fmt = wb.add_format({'num_format': '#,##0', 'border': 1})
text_fmt = wb.add_format({'border': 1, 'text_wrap': True, 'valign': 'top'})
bold_text_fmt = wb.add_format({'border': 1, 'bold': True, 'bg_color': '#D6E4F0'})
highlight_row = wb.add_format({'border': 1, 'bg_color': '#E2EFDA'})
warn_fmt = wb.add_format({'border': 1, 'bg_color': '#FFF2CC', 'text_wrap': True, 'valign': 'top'})
fail_fmt = wb.add_format({'border': 1, 'bg_color': '#FCE4EC', 'text_wrap': True, 'valign': 'top'})
pass_fmt = wb.add_format({'border': 1, 'bg_color': '#E8F5E9', 'text_wrap': True, 'valign': 'top'})
wrap_fmt = wb.add_format({'border': 1, 'text_wrap': True, 'valign': 'top'})
priority_high = wb.add_format({'border': 1, 'text_wrap': True, 'valign': 'top', 'font_color': '#C62828', 'bold': True})
priority_med = wb.add_format({'border': 1, 'text_wrap': True, 'valign': 'top', 'font_color': '#E65100'})
priority_low = wb.add_format({'border': 1, 'text_wrap': True, 'valign': 'top', 'font_color': '#2E7D32'})

# ============================================================
# Sheet 1: Production Readiness Checklist
# ============================================================
ws1 = wb.add_worksheet("Readiness Checklist")
ws1.set_column('A:A', 4)
ws1.set_column('B:B', 24)
ws1.set_column('C:C', 44)
ws1.set_column('D:D', 14)
ws1.set_column('E:E', 14)
ws1.set_column('F:F', 50)

headers1 = ["#", "Category", "Item", "Priority", "Status", "Action Required"]
for c, h in enumerate(headers1):
    ws1.write(0, c, h, header_fmt)

checklist = [
    # Infrastructure
    ["", "INFRASTRUCTURE", "", "", "", ""],
    [1, "Compute", "Move off Oracle Free Tier to paid VPS", "HIGH", "FAIL", "Upgrade to Oracle A1 Flex (4 OCPU / 24GB RAM) or Hetzner CX32"],
    [2, "Compute", "Production domain (not sslip.io)", "HIGH", "FAIL", "Register domain, configure DNS, update PUBLIC_HOST / BASE_URL"],
    [3, "Compute", "Second app server for HA", "HIGH", "FAIL", "Deploy 2nd Go binary behind load balancer"],
    [4, "Compute", "Load balancer (HAProxy or Nginx)", "HIGH", "FAIL", "Add HAProxy/Nginx in front of app servers"],
    [5, "Compute", "Automated OS hardening", "LOW", "FAIL", "Unattended-upgrades, UFW firewall rules, SSH key-only"],

    # Database
    ["", "DATABASE", "", "", "", ""],
    [6, "Database", "Configure pgxpool max connections", "HIGH", "FAIL", "Set pool size to 25-50 per app instance, max lifetime 30m"],
    [7, "Database", "PostgreSQL read replica", "MED", "FAIL", "Add replica for admin dashboard reads and analytics queries"],
    [8, "Database", "Automated backups (pg_dump cron)", "HIGH", "FAIL", "Daily pg_dump to S3/object storage, 30-day retention"],
    [9, "Database", "WAL archiving for PITR", "MED", "FAIL", "Enable archive_mode, configure archive_command to S3"],
    [10, "Database", "Connection pooling (PgBouncer)", "MED", "FAIL", "Deploy PgBouncer between app and PostgreSQL if >1 app server"],

    # Application
    ["", "APPLICATION", "", "", "", ""],
    [11, "Workers", "Increase outbox batch size (20 -> 100+)", "HIGH", "FAIL", "Change ClaimOutbox/ClaimInboundMessages limit to 100"],
    [12, "Workers", "Reduce worker tick interval (2s -> 500ms)", "HIGH", "FAIL", "Change outboxTicker from 2s to 500ms"],
    [13, "Workers", "Add concurrent worker goroutines", "MED", "FAIL", "Process batches in parallel with worker pool (e.g. 4-8 goroutines)"],
    [14, "Workers", "Graceful shutdown drain", "MED", "WARN", "Ensure in-flight webhooks complete before server stops (15s timeout exists)"],
    [15, "Concurrency", "Configure HTTP server MaxConnsPerIP", "MED", "FAIL", "Add MaxConnsPerIP to limit abuse per source IP"],
    [16, "Concurrency", "Configure HTTP server MaxHeaderBytes", "LOW", "FAIL", "Set MaxHeaderBytes = 1 << 20 to prevent oversized headers"],
    [17, "Security", "Rate-limit webhook endpoints", "HIGH", "FAIL", "Add per-IP rate limiting on /webhooks/* (e.g. 100 req/s per IP)"],
    [18, "Security", "Rotate webhook secrets on schedule", "LOW", "FAIL", "Implement quarterly secret rotation for WhatsApp/Telegram/Paystack"],
    [19, "Security", "HSTS header", "LOW", "FAIL", "Add Strict-Transport-Security header in production"],
    [20, "Logging", "Structured request logging middleware", "MED", "FAIL", "Log method, path, status, latency, IP for every request"],

    # Monitoring
    ["", "MONITORING", "", "", "", ""],
    [21, "Metrics", "Prometheus metrics endpoint", "HIGH", "FAIL", "Expose /metrics with request count, latency histogram, error rate, DB pool stats"],
    [22, "Metrics", "Grafana dashboards", "HIGH", "FAIL", "Dashboards for request rate, p50/p95/p99 latency, DB connections, outbox depth"],
    [23, "Alerting", "App health check alerting", "HIGH", "FAIL", "Alert if /health/ready fails for >2 minutes"],
    [24, "Alerting", "High error rate alert", "HIGH", "FAIL", "Alert if 5xx rate >1% over 5-minute window"],
    [25, "Alerting", "Outbox backlog alert", "MED", "FAIL", "Alert if pending outbox messages >500"],
    [26, "Alerting", "DB connection pool exhaustion alert", "HIGH", "FAIL", "Alert if pool utilization >80%"],
    [27, "Logging", "Centralized log aggregation", "MED", "FAIL", "Ship journalctl logs to Loki/CloudWatch/external syslog"],
    [28, "Uptime", "External uptime monitoring", "HIGH", "FAIL", "UptimeRobot/BetterStack pinging /health/live every 60s"],

    # Deployment
    ["", "DEPLOYMENT", "", "", "", ""],
    [29, "CI/CD", "Automated test pipeline", "HIGH", "PASS", "go test + go vet already in rebuild script; add to git push hook"],
    [30, "CI/CD", "Zero-downtime deployment", "HIGH", "FAIL", "Blue-green or rolling restart via systemd + load balancer"],
    [31, "CI/CD", "Database migration safety", "LOW", "PASS", "Migrate runs before app starts; ensure backward-compatible migrations"],
    [32, "Backups", "Restore drill schedule", "MED", "FAIL", "Quarterly backup restore test to verify pg_dump integrity"],

    # Scaling Path
    ["", "SCALING PATH", "", "", "", ""],
    [33, "Scaling", "Horizontal app scaling", "HIGH", "FAIL", "Stateless Go binary; add instances behind LB as needed"],
    [34, "Scaling", "Database read scaling", "MED", "FAIL", "Route admin dashboard + analytics to read replica"],
    [35, "Scaling", "Media/CDN offload", "LOW", "PASS", "Static assets served by Caddy; no media in chat currently"],
]

row = 1
for item in checklist:
    num, cat, text, priority, status, action = item
    if num == "":
        # Section header
        for c in range(6):
            ws1.write(row, c, cat if c == 1 else "", section_fmt)
    else:
        ws1.write(row, 0, num, text_fmt)
        ws1.write(row, 1, cat, text_fmt)
        ws1.write(row, 2, text, text_fmt)
        pfmt = priority_high if priority == "HIGH" else (priority_med if priority == "MED" else priority_low)
        ws1.write(row, 3, priority, pfmt)
        sfmt = fail_fmt if status == "FAIL" else (warn_fmt if status == "WARN" else pass_fmt)
        ws1.write(row, 4, status, sfmt)
        ws1.write(row, 5, action, text_fmt)
    row += 1

# Summary at bottom
row += 1
total_items = len([i for i in checklist if i[0] != ""])
failed = len([i for i in checklist if i[0] != "" and i[4] == "FAIL"])
warn = len([i for i in checklist if i[0] != "" and i[4] == "WARN"])
passed = len([i for i in checklist if i[0] != "" and i[4] == "PASS"])
high_fail = len([i for i in checklist if i[0] != "" and i[4] == "FAIL" and i[3] == "HIGH"])

ws1.write(row, 0, "", section_fmt)
ws1.write(row, 1, "SUMMARY", section_fmt)
ws1.write(row, 2, "", section_fmt)
ws1.write(row, 3, "", section_fmt)
ws1.write(row, 4, "", section_fmt)
ws1.write(row, 5, "", section_fmt)
row += 1
ws1.write(row, 1, "Total items", bold_text_fmt)
ws1.write(row, 2, total_items, num_fmt)
row += 1
ws1.write(row, 1, "FAIL (not done)", fail_fmt)
ws1.write(row, 2, failed, num_fmt)
row += 1
ws1.write(row, 1, "HIGH priority FAIL", priority_high)
ws1.write(row, 2, high_fail, num_fmt)
row += 1
ws1.write(row, 1, "WARN", warn_fmt)
ws1.write(row, 2, warn, num_fmt)
row += 1
ws1.write(row, 1, "PASS", pass_fmt)
ws1.write(row, 2, passed, num_fmt)

# ============================================================
# Sheet 2: Server Specifications (Minimum)
# ============================================================
ws2 = wb.add_worksheet("Server Specs - Minimum")
ws2.set_column('A:A', 36)
ws2.set_column('B:B', 10)
ws2.set_column('C:C', 8)
ws2.set_column('D:D', 12)
ws2.set_column('E:E', 12)
ws2.set_column('F:F', 12)
ws2.set_column('G:G', 18)
ws2.set_column('H:H', 20)

headers2 = ["Server Role", "Qty", "vCPU", "RAM (GB)", "Storage (GB)", "Storage Type", "OS", "Monthly Cost (USD est.)"]
for c, h in enumerate(headers2):
    ws2.write(0, c, h, header_fmt)

servers1 = [
    ["Application Server (Go + Caddy)", 1, 4, 8, 100, "SSD", "Ubuntu 22.04 LTS", 50],
    ["Database Server (PostgreSQL 15+)", 1, 4, 16, 500, "SSD", "Ubuntu 22.04 LTS", 80],
    ["Load Balancer (HAProxy/Nginx)", 1, 2, 4, 50, "SSD", "Ubuntu 22.04 LTS", 25],
]
for r, row_data in enumerate(servers1, start=1):
    for c, val in enumerate(row_data):
        if c == 7:
            ws2.write(r, c, val, money_fmt)
        elif c in (1, 2, 3, 4):
            ws2.write(r, c, val, num_fmt)
        else:
            ws2.write(r, c, val, text_fmt)

tr = len(servers1) + 1
ws2.write(tr, 0, "TOTAL", bold_text_fmt)
ws2.write(tr, 1, sum(r[1] for r in servers1), num_fmt)
ws2.write(tr, 2, sum(r[2] for r in servers1), num_fmt)
ws2.write(tr, 3, sum(r[3] for r in servers1), num_fmt)
ws2.write(tr, 4, sum(r[4] for r in servers1), num_fmt)
ws2.write(tr, 5, "", bold_text_fmt)
ws2.write(tr, 6, "", bold_text_fmt)
ws2.write(tr, 7, sum(r[7] for r in servers1), money_bold_fmt)

row_label = tr + 2
ws2.write(row_label, 0, "Note: Minimum = single app server, no DB replica, no redundancy", wrap_fmt)

# ============================================================
# Sheet 3: Server Specifications (Production)
# ============================================================
ws3 = wb.add_worksheet("Server Specs - Production")
ws3.set_column('A:A', 36)
ws3.set_column('B:B', 10)
ws3.set_column('C:C', 8)
ws3.set_column('D:D', 12)
ws3.set_column('E:E', 12)
ws3.set_column('F:F', 12)
ws3.set_column('G:G', 18)
ws3.set_column('H:H', 20)

for c, h in enumerate(headers2):
    ws3.write(0, c, h, header_fmt)

servers2 = [
    ["Application Server (Go + Caddy)", 2, 4, 8, 100, "SSD", "Ubuntu 22.04 LTS", 100],
    ["Database Primary (PostgreSQL 15+)", 1, 4, 16, 500, "SSD", "Ubuntu 22.04 LTS", 80],
    ["Database Replica (PostgreSQL 15+)", 1, 4, 16, 500, "SSD", "Ubuntu 22.04 LTS", 80],
    ["Load Balancer (HAProxy/Nginx)", 1, 2, 4, 50, "SSD", "Ubuntu 22.04 LTS", 25],
]
for r, row_data in enumerate(servers2, start=1):
    for c, val in enumerate(row_data):
        if c == 7:
            ws3.write(r, c, val, money_fmt)
        elif c in (1, 2, 3, 4):
            ws3.write(r, c, val, num_fmt)
        else:
            ws3.write(r, c, val, text_fmt)

tr2 = len(servers2) + 1
ws3.write(tr2, 0, "TOTAL", bold_text_fmt)
ws3.write(tr2, 1, sum(r[1] for r in servers2), num_fmt)
ws3.write(tr2, 2, sum(r[2] for r in servers2), num_fmt)
ws3.write(tr2, 3, sum(r[3] for r in servers2), num_fmt)
ws3.write(tr2, 4, sum(r[4] for r in servers2), num_fmt)
ws3.write(tr2, 5, "", bold_text_fmt)
ws3.write(tr2, 6, "", bold_text_fmt)
ws3.write(tr2, 7, sum(r[7] for r in servers2), money_bold_fmt)

row_label2 = tr2 + 2
ws3.write(row_label2, 0, "Note: Production = HA app servers, DB replica, automated backups, monitoring", wrap_fmt)

# ============================================================
# Sheet 4: Load Targets
# ============================================================
ws4 = wb.add_worksheet("Load Targets")
ws4.set_column('A:A', 30)
ws4.set_column('B:B', 20)
ws4.set_column('C:C', 55)

headers4 = ["Metric", "Value", "Notes"]
for c, h in enumerate(headers4):
    ws4.write(0, c, h, header_fmt)

load_data = [
    ["Registered users", "100,000", "Have completed onboarding (name, email, confirmed phone)"],
    ["Unregistered / transient users", "50,000-400,000", "Invoice customers, thrift joiners, data buyers, SMS users"],
    ["Total user pool", "150,000-500,000", "Registered + Unregistered"],
    ["Peak concurrent conversations", "1,725-13,200", "Active chat sessions at peak hour (both user types)"],
    ["Concurrent webhook requests", "~1,000-3,000", "Subset actively sending messages at any moment"],
    ["Avg webhook payload size", "~2 KB", "WhatsApp Cloud API JSON payload"],
    ["Peak inbound webhook rate", "~40-400 req/s", "Estimated during flash traffic"],
    ["Outbound message rate", "~16-165 msg/s", "WhatsApp Cloud API sends"],
    ["Database connections needed", "50-100", "With pgxpool connection pooling"],
    ["WebSocket connections", "0", "Webhook-based architecture; no persistent connections"],
    ["Redis", "Not required", "No session state to cache; stateless HTTP"],
    ["Message queue", "Not required", "Webhooks processed via Go worker goroutines"],
]
for r, row_data in enumerate(load_data, start=1):
    for c, val in enumerate(row_data):
        ws4.write(r, c, val, text_fmt)

# ============================================================
# Sheet 5: Cost Comparison
# ============================================================
ws5 = wb.add_worksheet("Cost Comparison")
ws5.set_column('A:A', 28)
ws5.set_column('B:B', 14)
ws5.set_column('C:C', 14)
ws5.set_column('D:D', 14)
ws5.set_column('E:E', 14)
ws5.set_column('F:F', 14)
ws5.set_column('G:G', 20)

headers5 = ["Setup", "App Servers", "Database", "Redis", "Message Queue", "Load Balancer", "Total Monthly (USD)"]
for c, h in enumerate(headers5):
    ws5.write(0, c, h, header_fmt)

cost_data = [
    ["Current (Oracle Free Tier)", "$0", "$0", "$0", "$0", "$0", "$0"],
    ["Minimum (Go webhook)", "$50", "$80", "$0", "$0", "$25", "$155"],
    ["Production (Go webhook)", "$100", "$160", "$0", "$0", "$25", "$285"],
    ["Node.js + Socket.io (ref.)", "$200+", "$200+", "$80+", "$80+", "$50+", "$610+"],
]
for r, row_data in enumerate(cost_data, start=1):
    if r == 3:
        fmt = highlight_row
    elif r == 1:
        fmt = warn_fmt
    else:
        fmt = text_fmt
    for c, val in enumerate(row_data):
        ws5.write(r, c, val, fmt)

# ============================================================
# Sheet 6: Infrastructure Notes
# ============================================================
ws6 = wb.add_worksheet("Infrastructure Notes")
ws6.set_column('A:A', 24)
ws6.set_column('B:B', 80)

headers6 = ["Category", "Detail"]
for c, h in enumerate(headers6):
    ws6.write(0, c, h, header_fmt)

notes = [
    ["Architecture", "Webhook-based: WhatsApp Cloud API / Telegram Bot API -> Go service -> PostgreSQL"],
    ["No WebSockets", "Real-time chat is handled by Meta/Telegram servers; Go receives callbacks"],
    ["No Redis", "Stateless request processing; no in-memory session cache needed"],
    ["No Message Queue", "Webhooks processed via Go goroutines; outbox claim pattern for delivery"],
    ["TLS", "Caddy auto-provisions and renews Let's Encrypt certificates"],
    ["Database", "PostgreSQL 15+ with pgx/v5 driver and pgxpool connection pooling"],
    ["Deployment", "Docker Compose or native systemd on Ubuntu VPS"],
    ["Cloud Providers", "Oracle A1 Flex, AWS t3/t4g, GCP e2/n2, Hetzner CX32"],
    ["Scaling Path", "Add app server replicas + load balancer when exceeding single-server capacity"],
    ["Linux Tuning", "Increase file descriptor limits, TCP backlog, ephemeral port range for webhook bursts"],
    ["Retention", "PII purged after 90 days via daily retention worker"],
    ["Security", "Webhook signature validation (X-Hub-Signature-256, X-Telegram-Bot-Api-Secret-Token)"],
    ["Worker Tuning", "Outbox batch 100+, tick 500ms, parallel goroutine workers for throughput"],
]
for r, row_data in enumerate(notes, start=1):
    for c, val in enumerate(row_data):
        ws6.write(r, c, val, wrap_fmt)

# ============================================================
# Sheet 7: Transaction Volume Breakdown
# ============================================================
ws7 = wb.add_worksheet("Transaction Volume")
ws7.set_column('A:A', 30)
ws7.set_column('B:B', 16)
ws7.set_column('C:C', 16)
ws7.set_column('D:D', 16)
ws7.set_column('E:E', 50)

# --- Assumptions section ---
row = 0
ws7.write(row, 0, "ASSUMPTIONS", section_fmt)
ws7.write(row, 1, "Conservative", section_fmt)
ws7.write(row, 2, "Moderate", section_fmt)
ws7.write(row, 3, "Aggressive", section_fmt)
ws7.write(row, 4, "Basis", section_fmt)
row += 1

assumptions = [
    ["Registered users (completed onboarding)", "100,000", "100,000", "100,000", "Fixed target; have name, email, confirmed phone"],
    ["Unregistered users (transient / one-off)", "50,000", "150,000", "400,000", "Invoice customers, thrift joiners, data buyers, SMS users"],
    ["Total user pool", "150,000", "250,000", "500,000", "Registered + Unregistered"],
    ["Registered DAU (% of registered)", "10%", "20%", "30%", "Nigerian fintech avg 10-25% DAU/MAU"],
    ["Unregistered daily actives (% of unregistered)", "3%", "5%", "8%", "Lower engagement; one-off transactions"],
    ["Transactions per registered active / day", "1.5", "2.5", "4.0", "Menu navigation, multiple actions"],
    ["Transactions per unregistered active / day", "1.0", "1.2", "1.5", "Single-purpose: pay invoice, buy data, join thrift"],
    ["Peak concurrent (% of total daily active)", "15%", "20%", "25%", "Users online at busiest hour"],
    ["Avg conversation messages per transaction", "4", "4", "4", "Menu navigation + confirmation + receipt"],
    ["Outbound messages per transaction", "2", "2", "3", "Confirmation + status update + receipt"],
]
for a in assumptions:
    for c, val in enumerate(a):
        ws7.write(row, c, val, text_fmt)
    row += 1

row += 1

# --- Daily Breakdown ---
ws7.write(row, 0, "DAILY VOLUME", section_fmt)
ws7.write(row, 1, "Conservative", section_fmt)
ws7.write(row, 2, "Moderate", section_fmt)
ws7.write(row, 3, "Aggressive", section_fmt)
ws7.write(row, 4, "Notes", section_fmt)
row += 1

# Conservative: Reg 100k*10%=10k DAU *1.5=15k + Unreg 50k*3%=1.5k*1.0=1.5k = 16.5k
# Moderate:     Reg 100k*20%=20k DAU *2.5=50k + Unreg 150k*5%=7.5k*1.2=9k = 59k
# Aggressive:   Reg 100k*30%=30k DAU *4.0=120k + Unreg 400k*8%=32k*1.5=48k = 168k

daily = [
    ["Registered DAU", "10,000", "20,000", "30,000", "100K x DAU%"],
    ["Registered tx / day", "15,000", "50,000", "120,000", "Registered DAU x tx/user"],
    ["Unregistered daily actives", "1,500", "7,500", "32,000", "Unregistered pool x unreg DAU%"],
    ["Unregistered tx / day", "1,500", "9,000", "48,000", "Unreg DAU x tx/user (single-purpose)"],
    ["TOTAL transactions / day", "16,500", "59,000", "168,000", "Registered + Unregistered"],
    ["Inbound webhook messages / day", "66,000", "236,000", "672,000", "Total tx x 4 msgs each"],
    ["Outbound messages / day", "33,000", "118,000", "504,000", "Total tx x 2-3 outbound msgs"],
    ["DB write operations / day", "49,500", "177,000", "504,000", "~3 writes per tx (state, audit, outbox)"],
    ["DB read operations / day", "33,000", "118,000", "336,000", "User lookup, merchant search, status checks"],
    ["Peak concurrent conversations", "1,725", "5,500", "13,200", "Total DAU x peak concurrent %"],
    ["Peak inbound webhooks / sec", "~40", "~135", "~400", "Evenly distributed + burst factor"],
    ["Peak outbound msgs / sec", "~16", "~55", "~165", "WhatsApp Cloud API rate limit: 80 msg/s per phone number"],
]
for d in daily:
    for c, val in enumerate(d):
        ws7.write(row, c, val, text_fmt)
    row += 1

row += 1

# --- Monthly Breakdown ---
ws7.write(row, 0, "MONTHLY VOLUME (x30 days)", section_fmt)
ws7.write(row, 1, "Conservative", section_fmt)
ws7.write(row, 2, "Moderate", section_fmt)
ws7.write(row, 3, "Aggressive", section_fmt)
ws7.write(row, 4, "Notes", section_fmt)
row += 1

monthly = [
    ["Registered tx / month", "450,000", "1,500,000", "3,600,000", "Registered daily x 30"],
    ["Unregistered tx / month", "45,000", "270,000", "1,440,000", "Unregistered daily x 30"],
    ["TOTAL transactions / month", "495,000", "1,770,000", "5,040,000", "All tx x 30"],
    ["Inbound webhook messages / month", "1,980,000", "7,080,000", "20,160,000", "Total daily inbound x 30"],
    ["Outbound messages / month", "990,000", "3,540,000", "15,120,000", "Total daily outbound x 30"],
    ["DB writes / month", "1,485,000", "5,310,000", "15,120,000", "Total daily writes x 30"],
    ["DB reads / month", "990,000", "3,540,000", "10,080,000", "Total daily reads x 30"],
    ["Webhook storage rows / month", "1,980,000", "7,080,000", "20,160,000", "Every inbound = 1 webhook row (retain 90 days)"],
    ["Retained webhook rows (90-day window)", "5,940,000", "21,240,000", "60,480,000", "Needs periodic purge; index on event_key"],
    ["Avg DB storage growth / month", "~2 GB", "~8 GB", "~22 GB", "Estimate ~1KB per row including indexes"],
    ["Unique users touched / month", "~45,000", "~130,000", "~350,000", "Registered active + unregistered pool"],
    ["WhatsApp conversation fees / month", "~$50", "~$175", "~$500", "Meta charges per 24h conversation window"],
]
for m in monthly:
    for c, val in enumerate(m):
        ws7.write(row, c, val, text_fmt)
    row += 1

row += 1

# --- Yearly Breakdown ---
ws7.write(row, 0, "YEARLY VOLUME (x12 months)", section_fmt)
ws7.write(row, 1, "Conservative", section_fmt)
ws7.write(row, 2, "Moderate", section_fmt)
ws7.write(row, 3, "Aggressive", section_fmt)
ws7.write(row, 4, "Notes", section_fmt)
row += 1

yearly = [
    ["Registered tx / year", "5,400,000", "18,000,000", "43,200,000", "Registered monthly x 12"],
    ["Unregistered tx / year", "540,000", "3,240,000", "17,280,000", "Unregistered monthly x 12"],
    ["TOTAL transactions / year", "5,940,000", "21,240,000", "60,480,000", "All tx x 12"],
    ["Inbound webhook messages / year", "23,760,000", "84,960,000", "241,920,000", "Monthly inbound x 12"],
    ["Outbound messages / year", "11,880,000", "42,480,000", "181,440,000", "Monthly outbound x 12"],
    ["DB writes / year", "17,820,000", "63,720,000", "181,440,000", "Monthly writes x 12"],
    ["DB reads / year", "11,880,000", "42,480,000", "120,960,000", "Monthly reads x 12"],
    ["Total DB storage / year", "~24 GB", "~96 GB", "~264 GB", "After retention purge; raw growth much higher"],
    ["WhatsApp conversation fees / year", "~$600", "~$2,100", "~$6,000", "Meta conversation-based pricing"],
    ["Paystack transaction fees / year", "~$178K", "~$637K", "~$1.81M", "1.5% of avg txn value across all users"],
]
for y in yearly:
    for c, val in enumerate(y):
        ws7.write(row, c, val, text_fmt)
    row += 1

row += 1

# --- Revenue Estimate ---
ws7.write(row, 0, "REVENUE ESTIMATE (annual)", section_fmt)
ws7.write(row, 1, "Conservative", section_fmt)
ws7.write(row, 2, "Moderate", section_fmt)
ws7.write(row, 3, "Aggressive", section_fmt)
ws7.write(row, 4, "Basis", section_fmt)
row += 1

revenue = [
    ["Avg tx value - registered (NGN)", "₦1,500", "₦2,000", "₦3,000", "Menu navigation, multiple action types"],
    ["Avg tx value - unregistered (NGN)", "₦800", "₦1,200", "₦1,800", "Single-purpose: invoice pay, data buy"],
    ["Blended avg tx value (NGN)", "₦1,430", "₦1,890", "₦2,830", "Weighted by tx volume ratio"],
    ["Blended avg tx value (USD)", "$0.87", "$1.15", "$1.72", "At ₦1,650/USD"],
    ["Total GMV / year (NGN)", "₦8.5B", "₦40.1B", "₦171.2B", "Yearly tx x blended avg value"],
    ["Total GMV / year (USD)", "$5.1M", "$24.3M", "$103.8M", "Yearly tx x blended avg value"],
    ["Platform fee (1.5% of GMV)", "$76.5K", "$364K", "$1.56M", "Assuming 1.5% take rate"],
    ["Net revenue after Paystack (1.4%)", "$5,100", "$32,100", "$102,200", "Platform fee minus Paystack cost"],
]
for rev in revenue:
    for c, val in enumerate(rev):
        ws7.write(row, c, val, text_fmt)
    row += 1

row += 1

# --- Scaling Triggers ---
ws7.write(row, 0, "SCALING TRIGGERS", section_fmt)
ws7.write(row, 1, "Conservative", section_fmt)
ws7.write(row, 2, "Moderate", section_fmt)
ws7.write(row, 3, "Aggressive", section_fmt)
ws7.write(row, 4, "When to act", section_fmt)
row += 1

triggers = [
    ["Add DB read replica", "50K tx/day", "60K tx/day", "60K tx/day", "When admin dashboard queries slow down"],
    ["Add 2nd app server", "20K tx/day", "35K tx/day", "60K tx/day", "When CPU >70% sustained or p95 >500ms"],
    ["Add PgBouncer", "2 app servers", "2 app servers", "2 app servers", "When DB connections exceed 200 across all app instances"],
    ["Increase outbox batch", "30K tx/day", "90K tx/day", "250K tx/day", "When outbox depth >500 sustained"],
    ["Add DB primary CPU", "100K tx/day", "180K tx/day", "400K tx/day", "When DB CPU >80% or WAL replication lag >10s"],
    ["Shard / archive old data", "5M rows/mo", "20M rows/mo", "60M rows/mo", "When webhook_events table >50M rows"],
    ["Add 3rd app server", "100K tx/day", "250K tx/day", "600K tx/day", "When 2 servers at >80% CPU sustained"],
]
for t in triggers:
    for c, val in enumerate(t):
        ws7.write(row, c, val, text_fmt)
    row += 1

wb.close()
print(f"Created: {output_path}")
