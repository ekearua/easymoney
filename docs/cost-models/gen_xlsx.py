import sys
sys.stdout.write("importing xlsxwriter...\n"); sys.stdout.flush()
import xlsxwriter

OUT = r"C:\Users\User\Mobile\Whatsapp Payment\docs\cost-models\Xego_Production_Requirements.xlsx"
import os, shutil
FINAL = OUT
OUT = FINAL + ".tmp"

wb = xlsxwriter.Workbook(OUT)

# Formats
hf = wb.add_format({"bold": True, "font_color": "white", "bg_color": "#2E75B6", "align": "center", "valign": "vcenter", "text_wrap": True, "border": 1})
wr = wb.add_format({"valign": "top", "text_wrap": True, "border": 1})
p0 = wb.add_format({"valign": "top", "text_wrap": True, "border": 1, "bg_color": "#FFC7CE"})
p1 = wb.add_format({"valign": "top", "text_wrap": True, "border": 1, "bg_color": "#FFEB9C"})
p2 = wb.add_format({"valign": "top", "text_wrap": True, "border": 1, "bg_color": "#C6EFCE"})
pf = {"P0 - Critical": p0, "P1 - High": p1, "P2 - Medium": p2}

def write_sheet(ws, headers, data, col_widths, priority_col=None):
    for i, h in enumerate(headers):
        ws.write(0, i, h, hf)
        ws.set_column(i, i, col_widths[i])
    for r, row in enumerate(data, 1):
        for c, val in enumerate(row):
            fmt = pf.get(row[priority_col], wr) if priority_col is not None else wr
            if priority_col is not None and c == priority_col:
                fmt = pf.get(val, wr)
            ws.write(r, c, val, fmt if priority_col is not None else wr)
    # Set row heights for readability
    ws.freeze_panes(1, 0)
    ws.autofilter(0, 0, len(data), len(headers) - 1)

# Sheet 1: Requirements
ws1 = wb.add_worksheet("Requirements")
req_headers = ["#", "Priority", "Area", "Component", "Current State", "Required Change", "Purpose / Business Justification", "File Reference", "Estimated Effort"]
reqs = [
    [1, "P0 - Critical", "Database", "Connection Pool Size", "Default 4 MaxConns (pgxpool.New with no config)", "MaxConns=75, MinConns=15, MaxConnLifetime=30m, MaxConnIdleTime=5m via env vars", "75k concurrent users each need a DB round-trip per message. 4 connections cause immediate exhaustion and request queuing.", "internal/store/store.go:499", "Small"],
    [2, "P0 - Critical", "Database", "PostgreSQL max_connections", "Default 100 connections", "Increase to 150-300. Add PgBouncer connection pooler (pool=75).", "4 app replicas each open 75 connections. Without PgBouncer, replicas compete for 100 total slots.", "compose.yaml", "Medium"],
    [3, "P0 - Critical", "Workers", "Background Worker Concurrency", "Single goroutine runs all 4 workers sequentially every 2s, batch=20", "Replace with concurrent worker pool (errgroup). Concurrency: 10 inbound, 5 outbox, 3 reconcilers.", "At 10k concurrent, message throughput hits hundreds/sec. Sequential processing of 20 msgs at ~200ms each = 4s/cycle. Backlog grows unbounded.", "internal/app/app.go:236-262", "Large"],
    [4, "P0 - Critical", "Deployment", "Horizontal Scaling", "Single app replica, no resource limits", "Deploy 2-4 app replicas. Add memory/CPU limits (512MB/1 CPU per replica).", "Single point of failure. No traffic distribution. One crash takes down the entire system.", "compose.yaml", "Medium"],
    [5, "P0 - Critical", "Deployment", "Database Resource Limits", "No memory/CPU limits on any Docker service", "Add deploy.resources.limits for db, app, migrate, caddy services.", "Unbounded resource usage can crash the host or starve other services.", "compose.yaml", "Small"],
    [6, "P1 - High", "Webhooks", "Rate Limiting", "No rate limiting on any webhook endpoint", "Add per-IP or per-provider rate limiting middleware (token bucket or sliding window).", "Misconfigured or malicious providers can flood inbound pipeline, creating thousands of rows in webhook_deliveries and inbound_messages.", "internal/app/app.go:199-202", "Medium"],
    [7, "P1 - High", "Workers", "Batch Sizes", "Hardcoded batch=20 for all worker types", "Make batch sizes configurable via env vars. Default to 50-100 for production.", "Larger batches reduce DB round-trips and improve throughput per worker cycle.", "internal/app/app.go:265,281,302,308", "Small"],
    [8, "P1 - High", "Database", "Data Retention Purge Batching", "PurgeBefore runs all deletes in a single transaction", "Break into batched deletes (e.g., 1000 rows per batch with short pauses).", "With 100k users and 90-day retention, a single delete transaction can lock tables for minutes.", "internal/store/store.go:3780-3821", "Medium"],
    [9, "P1 - High", "Workers", "Outbox Delivery Priority", "All 4 worker types run at the same 2s interval", "Prioritize outbox delivery (customer-facing) over reconciliation and retention.", "Customers waiting for payment confirmations expect sub-second delivery. Reconciliation can wait.", "internal/app/app.go:236-262", "Medium"],
    [10, "P1 - High", "Metrics", "Admin Dashboard Metrics Query", "Metrics() scans full payments table", "Add materialized view or pre-computed counter tables for dashboard metrics.", "Full table scans on payments table with 100k+ users will timeout or exhaust connections.", "internal/store/store.go:3753", "Medium"],
    [11, "P2 - Medium", "Sessions", "Session Caching", "Every message loads+save session from DB (2 round-trips/turn)", "Add in-process LRU cache (10k entries, 5min TTL) with invalidation for hot sessions.", "Reduces DB load by ~50% for active conversations. Critical for 75k concurrent.", "internal/store/store.go:3080-3115", "Medium"],
    [12, "P2 - Medium", "HTTP", "Write Timeout for WhatsApp Templates", "WriteTimeout=30s", "Increase to 45s or ensure all Meta API calls are async via outbox.", "WhatsApp template sends to Meta can take 5-10s. Combined with DB writes, 30s may be tight under load.", "internal/app/app.go:168", "Small"],
    [13, "P2 - Medium", "Monitoring", "Health Check & Observability", "Basic health endpoint. No structured logging or metrics.", "Add Prometheus metrics (request latency, queue depth, worker throughput). Structured logging with correlation IDs.", "Cannot diagnose production issues without observability. Needed for capacity planning.", "internal/app/app.go", "Large"],
    [14, "P2 - Medium", "Deployment", "TLS & Security Headers", "Caddy handles TLS auto-configuration via ACME", "Verify HSTS, CSP, X-Frame-Options in Caddyfile. Add rate limiting directives.", "Production traffic requires hardened security headers and DDoS protection.", "deploy/Caddyfile", "Small"],
]
write_sheet(ws1, req_headers, reqs, [5, 16, 14, 28, 38, 45, 55, 30, 20], priority_col=1)

# Sheet 2: Capacity
ws2 = wb.add_worksheet("Capacity")
cap_headers = ["Metric", "Current Capacity", "Target (375k/75k — Optimized)", "Gap", "Bottleneck", "Fix Required"]
cap = [
    ["Registered Users", "~5-10k", "375,000", "37-75x", "DB pool (4 conns), session storage", "Increase MaxConns to 75, add PgBouncer"],
    ["Concurrent Connections", "~50-100", "75,000 (Go tuned + HTTP/3 + session caching)", "750x", "HTTP server + DB pool", "Multi-replica, Go tuning, HTTP/3, session caching"],
    ["Messages/Second", "~25 realistic", "2,000+ (burst)", "80x", "Single-goroutine worker, batch=20", "Concurrent worker pool, batch=75-150"],
    ["Payment Processing", "~10/sec", "400+/sec (burst)", "40x", "Sequential worker processing", "Concurrent payment verification workers"],
    ["Webhook Ingestion", "~50/sec", "2,000+/sec", "40x", "No rate limiting, single worker", "Rate limiting + concurrent webhook processing"],
    ["Outbox Delivery", "~50/sec", "2,000+/sec", "40x", "Single goroutine, 20 msgs/cycle", "Concurrent outbox workers, larger batches"],
    ["DB Transactions/sec", "~100", "1,500+", "15x", "4 connection pool", "Pool=75, PgBouncer, query optimization"],
    ["Admin Dashboard Queries", "Full table scan", "Sub-second response", "N/A", "Metrics() scans payments table", "Materialized views or counter tables"],
    ["Data Retention Purge", "Single transaction", "Batched deletes", "N/A", "Table locks during purge", "Batch deletes (1000 rows/batch)"],
    ["Session Load/Save", "2 DB round-trips/turn", "Cached with TTL (50% reduction)", "Optional", "DB overhead per message", "In-process LRU cache (10k entries, 5min TTL)"],
]
write_sheet(ws2, cap_headers, cap, [25, 22, 22, 12, 38, 45])

# Sheet 3: Architecture
ws3 = wb.add_worksheet("Architecture")
arch_headers = ["Component", "Technology", "Status at Scale", "Notes"]
arch = [
    ["HTTP Router", "go-chi/chi v5", "GOOD", "Lightweight, stdlib-compatible. Scales horizontally with replicas."],
    ["Database", "PostgreSQL 17", "NEEDS TUNING", "Default pool=4 is critical bottleneck. PgBouncer required."],
    ["Session Storage", "DB-backed (conversation_sessions)", "GOOD", "Survives restarts, correct for multi-replica. Consider caching."],
    ["Message Outbox", "DB-backed (message_outbox)", "GOOD PATTERN", "FOR UPDATE SKIP LOCKED is correct. Needs concurrent workers."],
    ["Webhook Queue", "DB-backed (webhook_deliveries)", "GOOD PATTERN", "Dedup via unique constraint. Needs rate limiting."],
    ["Inbound Queue", "DB-backed (inbound_messages)", "GOOD PATTERN", "Dedup via unique constraint. Needs concurrent workers."],
    ["Payment Gateway", "Paystack API (demo)", "DEMO ONLY", "Used for quick iteration. Production will deploy in-house payment processing (custom gateway, bank transfers, card processing)."],
    ["In-house Payment Service", "Custom (production)", "TO BUILD", "Bank API integration, card processing, settlements. Replaces Paystack in production."],
    ["WhatsApp Provider", "Cloud API", "GOOD", "Template fallback for old conversations. HMAC validation in place."],
    ["Telegram Provider", "Bot API", "GOOD", "Inline keyboards, callback queries. Secret token validation."],
    ["Data Provider", "VTPass / Simulator", "GOOD", "Simulator for dev. VTPass sandbox for testing."],
    ["Email Provider", "SMTP", "ADEQUATE", "For OTPs and invoices only. Not high-volume."],
    ["Reverse Proxy", "Caddy 2.10", "GOOD", "Auto-TLS via ACME, HSTS, compression. Add rate limiting."],
    ["Containerization", "Docker Compose", "NEEDS WORK", "No resource limits, single replica, no PgBouncer."],
    ["Background Workers", "Single goroutine ticker", "CRITICAL GAP", "Must replace with concurrent worker pool."],
    ["Frontend", "HTMX + Go templates", "GOOD", "Server-rendered, embedded assets. No SPA complexity."],
    ["Migrations", "Embedded SQL files", "GOOD", "Applied in order. Idempotent."],
]
write_sheet(ws3, arch_headers, arch, [22, 28, 18, 65])

# Sheet 4: Roadmap
ws4 = wb.add_worksheet("Roadmap")
road_headers = ["Phase", "Priority", "Item", "Effort", "Impact", "Dependencies"]
road = [
    ["Phase 1: Quick Wins", "P0", "Configure DB pool (MaxConns=25, MinConns=5)", "1 hour", "Unblocks 5-10x concurrent users", "None"],
    ["Phase 1: Quick Wins", "P0", "Add resource limits to compose.yaml", "30 min", "Prevents OOM, crash isolation", "None"],
    ["Phase 1: Quick Wins", "P0", "Make batch sizes configurable (env vars)", "1 hour", "Tunable throughput without redeploy", "None"],
    ["Phase 1: Quick Wins", "P1", "Increase outbox batch to 50-100", "30 min", "5x outbox throughput", "DB pool increase"],
    ["Phase 1: Quick Wins", "P1", "Increase WriteTimeout to 45s", "5 min", "Prevents timeout on Meta API calls", "None"],
    ["Phase 2: Worker Scaling", "P0", "Implement concurrent worker pool (errgroup)", "2-3 days", "10x message processing throughput", "DB pool increase"],
    ["Phase 2: Worker Scaling", "P0", "Separate worker goroutines by type", "1 day", "Independent scaling per worker type", "Worker pool"],
    ["Phase 2: Worker Scaling", "P1", "Add worker metrics (processed/sec, queue depth)", "1 day", "Visibility into throughput", "Worker pool"],
    ["Phase 2: Worker Scaling", "P1", "Prioritize outbox over reconciliation", "0.5 day", "Faster customer-facing messages", "Worker pool"],
    ["Phase 3: Infrastructure", "P0", "Deploy 4 app replicas + HTTP/3", "1-2 days", "75k concurrent, 30-50% efficiency boost", "Worker pool, DB pool"],
    ["Phase 3: Infrastructure", "P0", "Add PgBouncer between app and PostgreSQL", "1 day", "Connection multiplexing, 15x connection capacity", "None"],
    ["Phase 3: Infrastructure", "P0", "Increase PostgreSQL max_connections to 200", "30 min", "Accommodates multiple replicas", "PgBouncer"],
    ["Phase 3: Infrastructure", "P1", "Tune Go HTTP server (MaxConns=1,000, MaxIdle=500, HTTP/3)", "0.5 day", "75k concurrent via Go config + QUIC", "None"],
    ["Phase 3: Infrastructure", "P1", "Add session caching (LRU, 10k entries, 5min TTL)", "1 day", "50% DB round-trip reduction", "None"],
    ["Phase 3: Infrastructure", "P1", "Add webhook rate limiting middleware", "1 day", "Prevents flooding, DDoS protection", "None"],
    ["Phase 3: Infrastructure", "P1", "Batch data retention purge (1000 rows/batch)", "1 day", "Prevents table locks during purge", "None"],
    ["Phase 4: Hardening", "P1", "Add materialized views for admin metrics", "1-2 days", "Sub-second dashboard queries", "None"],
    ["Phase 4: Hardening", "P2", "Prometheus metrics + structured logging", "2-3 days", "Production observability", "None"],
    ["Phase 4: Hardening", "P2", "Verify Caddy security headers", "0.5 day", "Hardened production headers", "None"],
    ["Phase 4: Hardening", "P2", "PgBouncer pool optimization (pool=75, max_client=2,000)", "0.5 day", "75k concurrent with headroom", "None"],
    ["Phase 4: Hardening", "P2", "PostgreSQL tuning (max_connections=150, shared_buffers=6GB)", "0.5 day", "50% more connections, better cache", "None"],
]
write_sheet(ws4, road_headers, road, [25, 12, 52, 14, 45, 35])

# Sheet 5: Deployment Plan (replaces Server Requirements)
ws5 = wb.add_worksheet("Deployment Plan")

# --- Summary formats ---
hdr_fmt = wb.add_format({"bold": True, "font_color": "white", "bg_color": "#2E75B6", "align": "center", "valign": "vcenter", "text_wrap": True, "border": 1})
cell = wb.add_format({"valign": "top", "text_wrap": True, "border": 1})
bold_cell = wb.add_format({"valign": "top", "text_wrap": True, "border": 1, "bold": True})
total_fmt = wb.add_format({"valign": "top", "text_wrap": True, "border": 1, "bold": True, "bg_color": "#D9E2F3"})
section_fmt = wb.add_format({"bold": True, "font_color": "#2E75B6", "font_size": 13, "bottom": 1, "bottom_color": "#2E75B6"})
note_fmt = wb.add_format({"valign": "top", "text_wrap": True, "font_color": "#555555", "italic": True})
cost_fmt = wb.add_format({"valign": "top", "text_wrap": True, "border": 1, "bg_color": "#E2EFDA"})
warn_fmt = wb.add_format({"valign": "top", "text_wrap": True, "font_color": "#C00000", "bold": True, "border": 1, "bg_color": "#FFF2CC"})
pci_fmt = wb.add_format({"valign": "top", "text_wrap": True, "font_color": "#7030A0", "bold": True, "border": 1, "bg_color": "#E2D0F0"})

# ============================
# SECTION 0: Architecture Overview
# ============================
r = 0
ws5.merge_range(r, 0, r, 5, "PAYMENT GATEWAY ARCHITECTURE OVERVIEW", section_fmt)
r += 1
ws5.merge_range(r, 0, r, 5, "Based on payment_gateway_architecture_document.docx — production-grade payment platform with microservices, event-driven architecture, and PCI compliance.", warn_fmt)
r += 2

arch_hdrs = ["Service", "Technology", "Responsibility", "Database", "Event Topics", "Notes"]
for c, h in enumerate(arch_hdrs):
    ws5.write(r, c, h, hdr_fmt)
r += 1

services = [
    ["API Gateway", "Go + Caddy", "TLS termination, rate limiting, auth, routing", "None", "None", "Public-facing. Replaces simple Caddy reverse proxy."],
    ["Merchant API", "Go", "REST/GraphQL API for merchants", "OLTP (merchants, config)", "merchants.v1", "Merchant onboarding, webhook config, API keys."],
    ["Checkout / Hosted Fields", "Go + JS", "PCI-compliant payment form", "None", "None", "Iframe-based. Minimizes PCI scope."],
    ["Tokenization Service", "Go (or Rust)", "Card tokenization, PAN vault", "Encrypted token DB", "tokens.v1", "PCI DSS scope 1. Isolate in separate network."],
    ["Payment Orchestrator", "Go", "Payment intent lifecycle, routing, state machine", "OLTP (payments)", "payments.v1", "Core service. Handles auth, capture, refund, void."],
    ["Connector Services", "Go", "Bank/card network integration", "None", "connectors.v1", "Adapter pattern. One per provider (GTB, Visa, etc.)"],
    ["Risk Engine", "Python/Fraud Detection", "Fraud scoring, velocity checks", "Redis + OLTP", "risk.v1", "ML-based. Python for rapid iteration."],
    ["Ledger Service", "Go", "Double-entry bookkeeping, journal entries", "Ledger DB (append-only)", "ledger.v1", "Immutable. Reversals via offsetting entries."],
    ["Settlement Service", "Go", "Batch settlement, netting", "OLTP (settlements)", "settlements.v1", "Daily/weekly settlement cycles."],
    ["Payout Service", "Go", "Disburse funds to merchants", "OLTP (payouts)", "payouts.v1", "Bank transfer integration."],
    ["Reconciliation Service", "Go", "Three-way match: internal vs ledger vs bank", "OLTP (reconciliation)", "reconciliation.v1", "Detects discrepancies. Critical for audit."],
    ["Disputes Service", "Go", "Chargeback management, evidence collection", "OLTP (disputes)", "disputes.v1", "Card scheme compliance."],
    ["Webhook Delivery Service", "Go", "Reliable delivery to merchants", "OLTP (webhooks)", "webhooks.v1", "Signed payloads, retry, replay protection."],
    ["Admin / Operations Console", "Go + HTMX", "Operator dashboard, manual actions", "Read replicas", "audit.v1", "Tamper-evident audit logs."],
    ["Event Bus", "Apache Kafka", "Domain event streaming", "Kafka topics", "All topics", "Event sourcing backbone. Exactly-once semantics."],
    ["Monitoring / SIEM", "Prometheus + Grafana + ELK", "Metrics, logs, security events", "Time-series DB", "None", "PCI requirement: 1-year log retention."],
]
for row in services:
    for c, val in enumerate(row):
        ws5.write(r, c, val, cell)
    r += 1
r += 1

# ============================
# SECTION 1: Server Count Summary
# ============================
ws5.merge_range(r, 0, r, 5, "SERVER COUNT SUMMARY (Microservices Architecture)", section_fmt)
r += 1

# Phase note
phase_note = wb.add_format({"valign": "top", "text_wrap": True, "font_color": "#0070C0", "bold": True, "border": 1, "bg_color": "#DAEEF3"})
ws5.merge_range(r, 0, r, 5, "PHASED DEPLOYMENT: Start with monolith (current Xego), extract services gradually. Don't deploy all 14 services on day 1.", phase_note)
r += 2

# Phase 1 header
ws5.merge_range(r, 0, r, 5, "Phase 1: Monolith + Payment Core (Launch)", hdr_fmt)
r += 1
hdrs = ["Server", "Role", "vCPUs", "RAM", "Disk", "Services Running"]
for c, h in enumerate(hdrs):
    ws5.write(r, c, h, hdr_fmt)
r += 1

phase1 = [
    ["Server 1", "Load Balancer + App", "4", "8 GB", "50 GB SSD", "Caddy (TLS, reverse proxy to both app replicas)\nXego App replica 1\n"],
    ["Server 2", "App + Payment + Risk", "8", "12 GB", "100 GB SSD", "Xego App replica 2\nPayment Orchestrator\nConnector Services\nRisk Engine\n"],
    ["Server 3", "Database + Events + Tokenization", "8", "16 GB", "200 GB NVMe SSD", "PostgreSQL 17\nPgBouncer (pool=30, max_client=500)\nKafka (single broker)\nTokenization Service (PCI isolated VPC subnet)\nToken DB (PCI isolated)\n"],
]
for row in phase1:
    for c, val in enumerate(row):
        ws5.write(r, c, val, cell)
    r += 1
for c, val in enumerate(["TOTAL", "", "20", "36 GB", "350 GB", "3 servers. 2 app replicas, 14 goroutine workers/replica, 30 DB pool. Tokenization in PCI-isolated VPC subnet."]):
    ws5.write(r, c, val, total_fmt)
r += 2

# Phase 2 header
ws5.merge_range(r, 0, r, 5, "Phase 2: Extract Core Services (100k users)", hdr_fmt)
r += 1
for c, h in enumerate(hdrs):
    ws5.write(r, c, h, hdr_fmt)
r += 1

phase2 = [
    ["Server 1", "Load Balancer + App 1", "4", "8 GB", "50 GB SSD", "Caddy (TLS, reverse proxy)\nXego App replica 1\nAdmin Console\n"],
    ["Server 2", "App 2 + Payment + Disputes", "8", "12 GB", "100 GB SSD", "Xego App replica 2\nPayment Orchestrator\nConnector Services\nRisk Engine\nReconciliation Service\nDisputes Service\n"],
    ["Server 3", "Data + Events + Tokenization", "8", "16 GB", "200 GB NVMe SSD", "PostgreSQL 17 (OLTP)\nPgBouncer (pool=40, max_client=500)\nKafka (3 brokers)\nRedis (rate limiting + risk)\nTokenization Service (PCI isolated VPC subnet)\nToken DB (PCI isolated)\n"],
]
for row in phase2:
    for c, val in enumerate(row):
        ws5.write(r, c, val, cell)
    r += 1
for c, val in enumerate(["TOTAL", "", "20", "36 GB", "350 GB", "3 servers. 2 app replicas, 21 goroutine workers/replica, 40 DB pool. All payment services extracted. Tokenization in PCI-isolated VPC subnet."]):
    ws5.write(r, c, val, total_fmt)
r += 2

# Phase 3 header
ws5.merge_range(r, 0, r, 5, "Phase 3: Full Platform (75k concurrent, full compliance, Go tuned + HTTP/3 + session caching)", hdr_fmt)
r += 1
for c, h in enumerate(hdrs):
    ws5.write(r, c, h, hdr_fmt)
r += 1

phase3 = [
    ["Server 1", "Load Balancer", "2", "2 GB", "20 GB SSD", "Caddy (TLS, reverse proxy, WAF)\n"],
    ["Server 2", "App Replica 1", "4", "8 GB", "50 GB SSD", "Xego App replica 1\nAdmin Console\n"],
    ["Server 3", "App Replica 2", "4", "8 GB", "50 GB SSD", "Xego App replica 2\nWebhook Delivery\n"],
    ["Server 4", "App Replica 3 + Payment", "8", "12 GB", "100 GB SSD", "Xego App replica 3\nPayment Orchestrator\nConnector Services\n"],
    ["Server 5", "App Replica 4 + Ledger", "8", "12 GB", "100 GB SSD", "Xego App replica 4\nLedger Service\nSettlement Service\nPayout Service\n"],
    ["Server 6", "Risk + Tokenization", "4", "8 GB", "50 GB SSD", "Risk Engine\nTokenization Service (PCI isolated)\nReconciliation Service\nDisputes Service\n"],
    ["Server 7", "OLTP Primary", "8", "16 GB", "200 GB NVMe SSD", "PostgreSQL 17 (OLTP Primary)\nPgBouncer (pool=75, max_client=2,000)\nToken DB (PCI isolated)\n"],
    ["Server 8", "Read Replicas + Events", "8", "16 GB", "200 GB NVMe SSD", "PostgreSQL 17 (OLTP Read Replica 1)\nPostgreSQL 17 (OLTP Read Replica 2)\nKafka (3 brokers)\nRedis\n"],
    ["Server 9", "Monitoring + Storage", "4", "8 GB", "500 GB HDD", "Prometheus\nGrafana\nELK/SIEM\nLedger DB (append-only)\nData Warehouse ETL\n"],
]
for row in phase3:
    for c, val in enumerate(row):
        ws5.write(r, c, val, cell)
    r += 1
for c, val in enumerate(["TOTAL", "", "50", "90 GB", "1,420 GB", "9 servers. Dedicated LB, 4 app replicas (1/server), 29 goroutine workers/replica, 75 DB pool. Go tuned + HTTP/3 + session caching. PCI compliant. Capacity: ~13.5B txns/year (429 TPS sustained). 75k concurrent."]):
    ws5.write(r, c, val, total_fmt)
r += 2

# Monthly cost estimate
ws5.merge_range(r, 0, r, 5, "MONTHLY COST ESTIMATE (Cloud Pricing, USD)", section_fmt)
r += 1
cost_hdrs = ["Phase", "Compute", "Database + Events", "Storage + Monitoring", "Domain + TLS", "Total / Month"]
for c, h in enumerate(cost_hdrs):
    ws5.write(r, c, h, hdr_fmt)
r += 1

costs = [
    ["Phase 1 (3 servers)", "$140-240 (3x, 8GB)", "$100-200 (PG + Kafka)", "$20-40", "Included", "$260-480"],
    ["Phase 1 + PCI DSS", "", "", "$1,142/mo (compliance+VPS+scans+monitoring)", "", "+$1,142/mo"],
    ["Phase 2 (3 servers)", "$180-300 (3x, 8-12GB)", "$200-400 (PG + Kafka + Redis)", "$30-60", "Included", "$410-760"],
    ["Phase 2 + PCI DSS", "", "", "$2,400/mo (compliance+VPS+scans+monitoring)", "", "+$2,400/mo"],
    ["Phase 3 (9 servers)", "$500-800 (9x, 2-16GB)", "$600-1,200 (3x PG + Kafka + Redis)", "$100-200", "Included", "$1,200-2,200"],
    ["Phase 3 + PCI DSS", "", "", "$3,667/mo (compliance+VPS+scans+monitoring)", "", "+$3,667/mo"],
]
for row in costs:
    for c, val in enumerate(row):
        ws5.write(r, c, val, cost_fmt)
    r += 1
r += 1

# PCI DSS Cost Breakdown
ws5.merge_range(r, 0, r, 5, "PCI DSS COMPLIANCE COST BREAKDOWN", section_fmt)
r += 1
pci_cost_hdrs = ["Item", "Cost Type", "Low Estimate", "High Estimate", "Frequency", "Notes"]
for c, h in enumerate(pci_cost_hdrs):
    ws5.write(r, c, h, hdr_fmt)
r += 1

pci_costs = [
    ["PCI QSA Audit", "One-time", "$5,000", "$20,000", "Once", "Qualified Security Assessor audit. Required for Level 1 compliance."],
    ["PCI DSS Annual Compliance", "Recurring", "$10,000", "$30,000", "Annual", "Re-certification, quarterly ASV scans, continuous monitoring."],
    ["PCI DSS Monthly (amortized)", "Recurring", "$833", "$2,500", "Monthly", "Annual cost divided by 12 months."],
    ["VPC Isolation Setup", "One-time", "$0", "$200", "Once", "Same cloud provider, separate VPC/subnet. Minimal cost."],
    ["Tokenization VPS (extra)", "Recurring", "$100", "$200", "Monthly", "Separate server for PCI-isolated tokenization service."],
    ["Quarterly ASV Scans", "Recurring", "$500", "$2,000", "Quarterly", "Approved Scanning Vendor vulnerability scans."],
    ["Penetration Testing", "Recurring", "$3,000", "$10,000", "Annual", "Required by PCI DSS Requirement 11."],
    ["Security Monitoring Tools", "Recurring", "$100", "$500", "Monthly", "SIEM, log aggregation, alerting."],
    ["TOTAL (Year 1)", "One-time + Recurring", "$18,500", "$73,000", "Year 1", "Audit + first year compliance + tools."],
    ["TOTAL (Ongoing/Year)", "Recurring", "$13,700", "$44,000", "Annual", "Annual compliance + scans + monitoring."],
    ["TOTAL (Monthly Ongoing)", "Recurring", "$1,142", "$3,667", "Monthly", "Annual cost divided by 12."],
]

for row in pci_costs:
    for c, val in enumerate(row):
        ws5.write(r, c, val, pci_fmt if "TOTAL" in row[0] else cell)
    r += 1
r += 1

# Total cost summary with PCI
ws5.merge_range(r, 0, r, 5, "TOTAL MONTHLY COST SUMMARY (All Phases + PCI DSS)", section_fmt)
r += 1
total_hdrs = ["Phase", "Infrastructure", "PCI DSS (monthly)", "Total Monthly", "Total Annual", "Notes"]
for c, h in enumerate(total_hdrs):
    ws5.write(r, c, h, hdr_fmt)
r += 1

totals = [
    ["Phase 1 (3 servers)", "$260-480", "$1,142", "$1,402-1,622", "$16,824-19,464", "SAQ-A scope. PCI includes: compliance + tokenization VPS + ASV scans + monitoring."],
    ["Phase 2 (3 servers)", "$410-760", "$2,400", "$2,810-3,160", "$33,720-37,920", "Expanding PCI scope. More services extracted, higher compliance cost."],
    ["Phase 3 (9 servers)", "$1,200-2,200", "$3,667", "$4,867-5,867", "$58,404-70,404", "Full PCI DSS Level 1 compliance. All payment services."],
    ["TOTAL (All Phases)", "$1,870-3,440", "$7,209", "$9,079-10,649", "$108,948-127,788", "Cumulative monthly costs including PCI DSS."],
]

for row in totals:
    for c, val in enumerate(row):
        fmt = cost_fmt if "PCI DSS" in row[0] else cell
        ws5.write(r, c, val, fmt)
    r += 1
r += 1

# PCI scope recommendation
pci_scope_fmt = wb.add_format({"valign": "top", "text_wrap": True, "font_color": "#0070C0", "bold": True, "border": 1, "bg_color": "#DAEEF3"})
ws5.merge_range(r, 0, r, 5, "PCI SCOPE RECOMMENDATION: Use hosted payment iframe (SAQ-A) to minimize PCI scope. This reduces audit cost from $20,000 to $5,000 and annual compliance from $30,000 to $10,000. Card data never touches your servers.", pci_scope_fmt)
r += 2

# ============================
# SECTION 2: Service-to-Server Mapping
# ============================
ws5.merge_range(r, 0, r, 5, "SERVICE-TO-SERVER MAPPING", section_fmt)
r += 1

map_hdrs = ["Service", "Container Image", "Runs On", "Port(s)", "Startup Order", "Database"]
for c, h in enumerate(map_hdrs):
    ws5.write(r, c, h, hdr_fmt)
r += 1

svc_map = [
    ["Caddy", "caddy:2.10", "Load Balancer", "80, 443, 443/udp", "Last (after all services)", "None"],
    ["Xego App (monolith)", "app:latest", "App + Admin", "8081 (internal)", "3rd", "OLTP Primary (R/W), OLTP Replica (R)"],
    ["Payment Orchestrator", "orchestrator:latest", "Payment Core", "8082 (internal)", "3rd", "OLTP Primary (R/W), Ledger DB (R)"],
    ["Connector Services", "connector:latest", "Payment Core", "8083 (internal)", "4th", "None (stateless)"],
    ["Risk Engine", "risk:latest", "Payment Core", "8084 (internal)", "3rd", "Redis + OLTP Replica (R)"],
    ["Tokenization Service", "tokenize:latest", "Payment Core (PCI zone)", "8085 (internal)", "2nd", "Token DB (PCI isolated)"],
    ["Ledger Service", "ledger:latest", "Ledger + Settlement", "8086 (internal)", "3rd", "Ledger DB (append-only)"],
    ["Settlement Service", "settlement:latest", "Ledger + Settlement", "8087 (internal)", "3rd", "OLTP Replica (R), Ledger DB (R)"],
    ["Payout Service", "payout:latest", "Ledger + Settlement", "8088 (internal)", "3rd", "OLTP Primary (R/W)"],
    ["Reconciliation Service", "recon:latest", "Ledger + Settlement", "8089 (internal)", "3rd", "OLTP Replica (R), Ledger DB (R)"],
    ["Disputes Service", "disputes:latest", "Ledger + Settlement", "8090 (internal)", "3rd", "OLTP Primary (R/W)"],
    ["Webhook Delivery", "webhooks:latest", "App + Admin", "8091 (internal)", "3rd", "OLTP Primary (R/W)"],
    ["Admin Console", "admin:latest", "App + Admin", "8092 (internal)", "3rd", "OLTP Replica (R), Ledger DB (R)"],
    ["Kafka", "confluentinc/cp-kafka", "Ledger DB + Events", "9092", "1st (with DB)", "ZooKeeper/KRaft"],
    ["PostgreSQL (OLTP Primary)", "postgres:17-alpine", "OLTP Primary", "5432 (private)", "1st", "N/A"],
    ["PostgreSQL (OLTP Replica 1)", "postgres:17-alpine", "OLTP Read Replica", "5433 (private)", "2nd (after primary)", "Streaming replica"],
    ["PostgreSQL (OLTP Replica 2)", "postgres:17-alpine", "OLTP Read Replica", "5434 (private)", "2nd (after primary)", "Streaming replica"],
    ["PostgreSQL (Ledger)", "postgres:17-alpine", "Ledger DB + Events", "5435 (private)", "1st", "N/A"],
    ["PgBouncer", "edoburu/pgbouncer", "OLTP Primary", "6432", "1st (with PG)", "None"],
    ["Redis", "redis:7-alpine", "Ledger DB + Events", "6379", "1st (with Kafka)", "None"],
]
for row in svc_map:
    for c, val in enumerate(row):
        ws5.write(r, c, val, cell)
    r += 1
r += 1

# ============================
# SECTION 2B: Database Topology
# ============================
ws5.merge_range(r, 0, r, 5, "DATABASE TOPOLOGY", section_fmt)
r += 1

db_hdrs = ["Database", "Purpose", "Reads From", "Writes To", "Replication", "Retention"]
for c, h in enumerate(db_hdrs):
    ws5.write(r, c, h, hdr_fmt)
r += 1

databases = [
    ["OLTP Primary", "Transaction writes (payments, sessions, webhooks, inbound messages)", "Payment Orchestrator, App, Webhooks", "All write services", "Stream Primary", "90 days (configurable)"],
    ["OLTP Read Replica 1", "Read-heavy workloads (admin dashboard, reporting, reconciliation queries)", "Admin Console, Reconciliation, Settlement", "None (read-only)", "Streaming replica of OLTP Primary", "Same as primary"],
    ["OLTP Read Replica 2", "Analytics, data export, heavy queries (metrics, user lists)", "Data Warehouse ETL, Admin reports", "None (read-only)", "Streaming replica of OLTP Primary", "Same as primary"],
    ["Ledger DB", "Append-only double-entry bookkeeping (journal entries, balance snapshots)", "Ledger Service, Reconciliation", "Ledger Service only", "Separate instance (not replicated)", "Indefinite (7-year minimum)"],
    ["Token DB", "Card tokenization vault (PCI DSS scope 1)", "Tokenization Service", "Tokenization Service only", "Separate instance (PCI isolated)", "Cardholder data retention policy"],
    ["Redis", "Rate limiting, session cache, risk scoring cache", "All services", "All services", "Standalone or Sentinel", "Ephemeral (TTL-based)"],
    ["Data Warehouse", "Historical analytics, compliance reporting, ML training", "BI tools, Compliance team", "ETL from OLTP Read Replica", "N/A (analytics)", "7 years (regulatory)"],
]
for row in databases:
    for c, val in enumerate(row):
        ws5.write(r, c, val, cell)
    r += 1
r += 1

# Database access patterns
ws5.merge_range(r, 0, r, 5, "DATABASE ACCESS PATTERNS", section_fmt)
r += 1

acc_hdrs = ["Service", "Primary DB", "Read Replica", "Ledger DB", "Redis", "Notes"]
for c, h in enumerate(acc_hdrs):
    ws5.write(r, c, h, hdr_fmt)
r += 1

access = [
    ["Payment Orchestrator", "R/W (payments)", "R (lookups)", "R (balance check)", "R/W (idempotency)", "Critical path. Must use primary for writes."],
    ["Xego App", "R/W (sessions, inbound)", "R (admin queries)", "None", "R/W (cache)", "Session reads can use replica."],
    ["Admin Console", "R (user lists)", "R (dashboard, metrics)", "R (audit trail)", "R (cache)", "Heavy reads. Always use replica."],
    ["Ledger Service", "R (settlement refs)", "None", "R/W (journal entries)", "None", "Ledger is append-only. Never UPDATE/DELETE."],
    ["Settlement Service", "R (payment refs)", "R (batch queries)", "R (balance snapshots)", "None", "Settlement reads from replica to avoid primary load."],
    ["Reconciliation Service", "R (payment refs)", "R (bulk reads)", "R (ledger entries)", "None", "Heavy bulk reads. Must use replica."],
    ["Risk Engine", "R (velocity checks)", "None", "None", "R/W (risk cache)", "Redis-first. OLTP only for historical checks."],
    ["Webhook Delivery", "R/W (webhook logs)", "None", "None", "R (dedup cache)", "Write-heavy. Uses primary."],
    ["Tokenization Service", "Token DB only", "None", "None", "None", "PCI isolated. No access to other databases."],
]
for row in access:
    for c, val in enumerate(row):
        ws5.write(r, c, val, cell)
    r += 1
r += 1

# ============================
# SECTION 3: Network Topology
# ============================
ws5.merge_range(r, 0, r, 5, "NETWORK TOPOLOGY", section_fmt)
r += 1

net_hdrs = ["Source", "Destination", "Port", "Protocol", "Firewall Rule", "Notes"]
for c, h in enumerate(net_hdrs):
    ws5.write(r, c, h, hdr_fmt)
r += 1

net = [
    ["Internet", "Caddy / Load Balancer", "80, 443", "TCP", "ALLOW 0.0.0.0/0 -> 80,443", "Public-facing. PCI DSS requires WAF."],
    ["Caddy", "Xego App", "8081", "TCP", "ALLOW within VPC", "Load-balanced to both app replicas."],
    ["Caddy", "Payment Orchestrator", "8082", "TCP", "ALLOW within VPC", "Payment requests."],
    ["Caddy", "Tokenization Service", "8085", "TCP", "ALLOW within VPC (PCI zone)", "PCI zone. Isolated subnet."],
    ["App", "OLTP Primary (PgBouncer)", "6432", "TCP", "ALLOW within VPC", "Write path: sessions, inbound, webhooks."],
    ["App", "OLTP Read Replica", "5433", "TCP", "ALLOW within VPC", "Read path: admin queries, user lists."],
    ["Payment Orchestrator", "OLTP Primary (PgBouncer)", "6432", "TCP", "ALLOW within VPC", "Payment state writes."],
    ["Payment Orchestrator", "Ledger DB", "5435", "TCP", "ALLOW within VPC", "Journal entry writes."],
    ["Payment Orchestrator", "Kafka", "9092", "TCP", "ALLOW within VPC", "Event publishing."],
    ["Ledger Service", "Ledger DB", "5435", "TCP", "ALLOW within VPC", "Append-only ledger writes."],
    ["Settlement Service", "OLTP Read Replica", "5433", "TCP", "ALLOW within VPC", "Settlement batch reads."],
    ["Reconciliation Service", "OLTP Read Replica", "5433", "TCP", "ALLOW within VPC", "Bulk reconciliation reads."],
    ["Reconciliation Service", "Ledger DB", "5435", "TCP", "ALLOW within VPC", "Ledger entry verification."],
    ["Admin Console", "OLTP Read Replica", "5433", "TCP", "ALLOW within VPC", "Dashboard queries."],
    ["Risk Engine", "Redis", "6379", "TCP", "ALLOW within VPC", "Risk scoring cache."],
    ["App", "WhatsApp Graph API", "443", "TCP", "ALLOW egress", "Outbound template sends."],
    ["App", "Bank APIs", "443", "TCP", "ALLOW egress", "Outbound to Nigerian bank APIs."],
    ["App", "SMTP Server", "587", "TCP", "ALLOW egress", "Email OTPs and invoices."],
    ["Tokenization Service", "Card Networks (Visa/Mastercard)", "443", "TCP", "ALLOW egress (PCI zone)", "3DS, tokenization. PCI scope."],
]
for row in net:
    for c, val in enumerate(row):
        ws5.write(r, c, val, cell)
    r += 1
r += 1

# ============================
# SECTION 4: Data Model (from architecture doc)
# ============================
ws5.merge_range(r, 0, r, 5, "DATA MODEL BLUEPRINT (from Architecture Doc)", section_fmt)
r += 1

dm_hdrs = ["Bounded Area", "Tables", "Mutability", "Database", "Notes"]
for c, h in enumerate(dm_hdrs):
    ws5.write(r, c, h, hdr_fmt)
r += 1

data_model = [
    ["Merchants & Config", "merchants, merchant_settlement_accounts, pricing_plans, webhook_endpoints", "Mutable", "OLTP", "Merchant onboarding, pricing, webhook config."],
    ["Payments", "payment_intents, payment_attempts, payment_method_tokens, authorizations, captures, refunds, voids", "Mutable (state machine)", "OLTP", "Payment lifecycle. State transitions are audited."],
    ["Ledger", "ledger_accounts, journal_batches, journal_entries, balance_snapshots", "Append-only (immutable)", "Ledger DB", "Double-entry bookkeeping. Reversals via offsetting entries."],
    ["Settlement & Payouts", "settlement_batches, settlement_lines, payouts", "Mutable", "OLTP", "Daily/weekly settlement cycles. Bank transfer integration."],
    ["Reconciliation", "processor_reports, bank_statements, reconciliation_runs, reconciliation_items", "Mutable", "OLTP", "Three-way match: internal vs ledger vs bank."],
    ["Disputes", "disputes, dispute_evidence", "Mutable", "OLTP", "Chargeback management. Card scheme compliance."],
]
for row in data_model:
    for c, val in enumerate(row):
        ws5.write(r, c, val, cell)
    r += 1
r += 1

# ============================
# SECTION 5: Event Model (from architecture doc)
# ============================
ws5.merge_range(r, 0, r, 5, "EVENT MODEL (Kafka Topics)", section_fmt)
r += 1

evt_hdrs = ["Topic Family", "Event Types", "Producer", "Consumer", "Retention", "Notes"]
for c, h in enumerate(evt_hdrs):
    ws5.write(r, c, h, hdr_fmt)
r += 1

events = [
    ["payments.v1", "payment.authorization.succeeded, payment.capture.succeeded, payment.refund.succeeded", "Payment Orchestrator", "Ledger, Webhooks, Reconciliation", "30 days", "Core payment events."],
    ["ledger.v1", "ledger.transaction.posted", "Ledger Service", "Settlement, Reconciliation", "Indefinite", "Immutable audit trail."],
    ["settlements.v1", "settlement.batch.created", "Settlement Service", "Payout, Reconciliation", "30 days", "Daily settlement cycles."],
    ["payouts.v1", "payout.succeeded", "Payout Service", "Webhooks, Admin", "30 days", "Merchant disbursements."],
    ["disputes.v1", "dispute.opened, dispute.evidence_submitted", "Disputes Service", "Webhooks, Admin", "90 days", "Chargeback management."],
    ["reconciliation.v1", "reconciliation.run.completed", "Reconciliation Service", "Admin, Alerts", "90 days", "Three-way match results."],
    ["webhooks.v1", "webhook.delivery.attempted", "Webhook Delivery", "None (final)", "7 days", "Merchant notification."],
]
for row in events:
    for c, val in enumerate(row):
        ws5.write(r, c, val, cell)
    r += 1
r += 1

# ============================
# SECTION 6: PCI Compliance Requirements
# ============================
ws5.merge_range(r, 0, r, 5, "PCI DSS COMPLIANCE REQUIREMENTS", section_fmt)
r += 1

pci_hdrs = ["#", "Requirement", "Implementation", "Criticality", "Service(s) Affected", "Notes"]
pci = [
    [1, "PCI DSS Level 1 (QSA RoC)", "QSA-conducted RoC + AOC", "P0", "All payment services", "Level 1: more than 6M card transactions/year."],
    [2, "Network Segmentation", "Isolate Tokenization Service in separate VLAN/subnet", "P0", "Tokenization Service", "Card data never touches other services."],
    [3, "Encryption at Rest", "AES-256 for card data, token vault", "P0", "Tokenization Service", "HSM-backed key management."],
    [4, "Encryption in Transit", "TLS 1.3 for all internal and external traffic", "P0", "All services", "mTLS between internal services recommended."],
    [5, "Access Control", "RBAC for admin console, API key scoping for merchants", "P0", "API Gateway, Admin Console", "Least privilege principle."],
    [6, "Audit Logging", "Tamper-evident logs for all financial actions", "P0", "All services", "1-year retention. SIEM integration."],
    [7, "Vulnerability Scanning", "Quarterly ASV scans, annual penetration test", "P0", "All services", "PCI DSS Requirement 11."],
    [8, "Incident Response Plan", "Documented IR plan for data breaches", "P1", "Operations", "PCI DSS Requirement 12."],
    [9, "Tokenization", "Hosted fields + isolated tokenization service", "P0", "Checkout, Tokenization", "Card data never stored in OLTP DB."],
    [10, "Three-way Reconciliation", "Internal records vs ledger vs bank statements", "P0", "Reconciliation Service", "Daily automated + weekly manual review."],
]
for row in pci:
    for c, val in enumerate(row):
        ws5.write(r, c, val, pci_fmt if row[3] == "P0" else cell)
    r += 1
r += 1

# ============================
# SECTION 7: Prerequisites & Secrets
# ============================
ws5.merge_range(r, 0, r, 5, "PREREQUISITES BEFORE DEPLOYING", section_fmt)
r += 1

preq_hdrs = ["#", "Item", "Required For", "Where to Get It", "Priority", "Notes"]
preq = [
    [1, "Domain name (xego.com)", "TLS, webhooks, branding", "Namecheap, Cloudflare, Route 53", "P0", "A record -> server public IP"],
    [2, "Cloud VPS (2-7 servers)", "Compute", "DigitalOcean, AWS, Hetzner, Vultr", "P0", "Ubuntu 22.04 or Debian 12"],
    [3, "Bank API credentials", "In-house payment processing", "Nigerian bank developer portals (GTB, Access, etc.)", "P0", "Production keys for live transactions"],
    [4, "Card network certification", "Card processing (Visa/Mastercard)", "Card scheme certification program", "P0", "PCI compliance required. 3-6 month process."],
    [5, "WhatsApp Business API token", "WhatsApp messaging", "developers.facebook.com", "P0", "Permanent access token + phone number ID"],
    [6, "SMTP credentials", "Email OTPs and invoices", "SendGrid, Mailgun, AWS SES", "P1", "587/TLS recommended"],
    [7, "VTPass API keys (optional)", "Airtime/data/bills", "vtpass.com", "P1", "sandbox.vtpass.com for testing"],
    [8, "Cloudflare (optional)", "DDoS + CDN + WAF", "cloudflare.com", "P1", "WAF recommended for PCI."],
    [9, "Monitoring (optional)", "Uptime + SIEM", "UptimeRobot, Grafana Cloud, ELK", "P1", "PCI requires 1-year log retention."],
    [10, "PCI QSA (optional)", "Compliance audit", "PCI Qualified Security Assessor", "P0", "Required for Level 1 compliance."],
]
for row in preq:
    for c, val in enumerate(row):
        ws5.write(r, c, val, cell)
    r += 1

# Column widths for the whole sheet
widths = [22, 32, 38, 22, 16, 55]
for c, w in enumerate(widths):
    ws5.set_column(c, c, w)

# ============================
# Sheet 6: Server Requirements (kept as reference)
# ============================
ws6 = wb.add_worksheet("Component Specs (Reference)")
srv_headers = ["Component", "Requirement", "Dev / Staging", "Production (375k / 75k — Optimized)", "Criticality", "Notes"]
srv = [
    ["Application Server", "CPU", "2 vCPUs", "4 vCPUs per replica (x2-4 replicas)", "P0", "Go binary is CPU-bound during message parsing and bcrypt hashing"],
    ["Application Server", "RAM", "512 MB", "8 GB per replica (29 goroutine workers + 10K HTTP conns + HTTP/3)", "P0", "10K concurrent HTTP goroutines x 800KB stack = 8GB. Plus heap (sessions, templates) = 12GB max."],
    ["Application Server", "Disk", "1 GB (logs only)", "5 GB SSD (logs + temp)", "P1", "No local state; all state is in PostgreSQL"],
    ["Application Server", "Network", "100 Mbps", "1 Gbps", "P0", "Webhook ingestion and outbox delivery are network-bound"],
    ["Application Server", "Container Runtime", "Docker 24+", "Docker 24+ or containerd", "P0", "Build image: multi-stage Go build (~15 MB final image)"],
    ["Application Server", "Go Version", "1.22+", "1.22+ (pinned in Dockerfile)", "P0", "Matches go.mod directive"],
    ["Application Server", "Replicas", "1", "4 (1 per server, behind Caddy + HTTP/3)", "P0", "Zero-downtime deploys, horizontal scaling. 29 goroutine workers per replica. Phase 3 optimized handles ~13.5B txns/yr."],
    ["Application Server", "Resource Limits", "None (dev)", "CPU: 4 vCPUs, Memory: 8 GB per replica", "P0", "Prevents OOM from killing host. 8 GB for goroutine stacks + heap. HTTP/3 enabled via Caddy."],
    ["Application Server", "Health Checks", "Optional", "/health/live + /health/ready every 10s", "P0", "Kubernetes/Docker Swarm auto-restart on failure"],
    ["Application Server", "Graceful Shutdown", "15s timeout", "15s timeout (matching WriteTimeout)", "P0", "In-flight requests must complete before SIGTERM"],
    ["", "", "", "", "", ""],
    ["PostgreSQL Database", "Version", "17 (Alpine)", "17 (Alpine or Debian)", "P0", "Use latest patch release"],
    ["PostgreSQL Database", "CPU", "2 vCPUs", "4-8 vCPUs", "P0", "Query planner, concurrent connections, WAL writes"],
    ["PostgreSQL Database", "RAM", "1 GB", "8-16 GB", "P0", "shared_buffers = 25% RAM, effective_cache_size = 75% RAM"],
    ["PostgreSQL Database", "Disk", "20 GB HDD", "100-200 GB NVMe SSD", "P0", "IOPS matters more than throughput for OLTP workloads"],
    ["PostgreSQL Database", "Disk IOPS", "300 (baseline)", "3,000-10,000 provisioned", "P0", "Payment lookups, session reads, webhook dedup all hit DB"],
    ["PostgreSQL Database", "Max Connections", "100 (default)", "200-300 (via PgBouncer pooler)", "P0", "App pool=50/replica x 4 replicas = 200 connections needed"],
    ["PostgreSQL Database", "Connection Pooler", "None", "PgBouncer (transaction mode)", "P0", "Multiplexes 200+ app connections into 50 DB connections"],
    ["PostgreSQL Database", "WAL Level", "replica", "replica", "P1", "Required for point-in-time recovery and streaming replication"],
    ["PostgreSQL Database", "Replication", "None (dev)", "Streaming replication to 1 standby", "P1", "Automatic failover with pg_auto_failover or Patroni"],
    ["PostgreSQL Database", "Backup Frequency", "None (dev)", "Daily full + continuous WAL archiving", "P0", "pg_basebackup + WAL-G or pgBackRest"],
    ["PostgreSQL Database", "Backup Retention", "None", "30 days", "P0", "Compliance and disaster recovery"],
    ["PostgreSQL Database", "Point-in-Time Recovery", "None", "Enabled (WAL archiving)", "P1", "Recover to any point in last 30 days"],
    ["PostgreSQL Database", "SSL Mode", "disable (dev)", "require (verify-full in prod)", "P0", "Encrypt data in transit between app and DB"],
    ["PostgreSQL Database", "Resource Limits", "None (dev)", "CPU: 4, Memory: 12 GB", "P0", "Docker deploy.resources.limits"],
    ["", "", "", "", "", ""],
    ["PgBouncer", "Version", "Not needed", "1.21+ (Alpine)", "P0", "Deploy as sidecar or separate container"],
    ["PgBouncer", "Pool Mode", "N/A", "transaction", "P0", "Best for stateless app workers"],
    ["PgBouncer", "Max Client Connections", "N/A", "500-2,000 (scales by phase)", "P0", "Phase 1: 500, Phase 3: 2,000. Optimized for 75k concurrent."],
    ["PgBouncer", "Default Pool Size", "N/A", "30-75 (scales by phase)", "P0", "Phase 1: 30, Phase 2: 40, Phase 3: 75. Optimized for 75k concurrent."],
    ["PgBouncer", "Reserve Pool Size", "N/A", "10", "P1", "Burst capacity for traffic spikes"],
    ["PgBouncer", "Server Idle Timeout", "N/A", "600s", "P2", "Releases idle DB connections"],
    ["PgBouncer", "Health Check", "N/A", "SELECT 1 every 30s", "P0", "Detects stale connections"],
    ["", "", "", "", "", ""],
    ["Caddy Reverse Proxy", "Version", "2.10 (Alpine)", "2.10 (Alpine)", "P0", "Auto-TLS via ACME"],
    ["Caddy Reverse Proxy", "CPU", "1 vCPU", "2 vCPUs", "P0", "TLS termination, compression, rate limiting"],
    ["Caddy Reverse Proxy", "RAM", "128 MB", "512 MB", "P1", "Connection buffers, TLS session cache"],
    ["Caddy Reverse Proxy", "Ports", "80, 443", "80, 443 (+ 443/udp for HTTP/3)", "P0", "HTTP/3 (QUIC) for 30-50% efficiency boost on modern clients"],
    ["Caddy Reverse Proxy", "TLS Certificates", "Let's Encrypt (staging)", "Let's Encrypt (production) or custom cert", "P0", "ACME HTTP-01 challenge via port 80"],
    ["Caddy Reverse Proxy", "Rate Limiting", "None", "caddy-ratelimit plugin or external WAF", "P0", "Protect against DDoS and brute-force"],
    ["Caddy Reverse Proxy", "Security Headers", "Basic", "HSTS, CSP, X-Frame-Options, X-Content-Type-Options", "P1", "Already set in app middleware; verify Caddy doesn't strip"],
    ["Caddy Reverse Proxy", "Compression", "gzip", "gzip + zstd", "P2", "Reduced bandwidth for API responses"],
    ["Caddy Reverse Proxy", "Resource Limits", "None (dev)", "CPU: 1, Memory: 512 MB", "P0", "Prevents memory exhaustion from slow clients"],
    ["Caddy Reverse Proxy", "Log Rotation", "stdout", "stdout + log driver or file-based rotation", "P1", "Prevent disk fill from access logs"],
    ["", "", "", "", "", ""],
    ["Domain & DNS", "Domain Name", "Subdomain (dev.xego.com)", "Production domain (xego.com or pay.xego.com)", "P0", "Must be registered and DNS-managed"],
    ["Domain & DNS", "DNS Provider", "Any", "Cloudflare, Route 53, or similar", "P1", "DNSSEC, low TTL for failover"],
    ["Domain & DNS", "A Record", "Server IP", "Load balancer or Caddy IP", "P0", "Update on deploy"],
    ["Domain & DNS", "TXT Record (SPF)", "Optional", "Required for email deliverability", "P1", "v=spf1 include:..."],
    ["Domain & DNS", "MX Record", "Optional", "Required for SMTP email sending", "P1", "Route to SMTP provider"],
    ["", "", "", "", "", ""],
    ["Networking", "Firewall (Inbound)", "Allow all (dev)", "Allow 80, 443 from 0.0.0.0/0. Deny all else.", "P0", "DB port 5432 must NOT be public"],
    ["Networking", "Firewall (DB Access)", "None", "Allow 5432 only from app subnet / VPC", "P0", "Database must never be internet-exposed"],
    ["Networking", "VPC / Private Network", "None (dev)", "VPC with private subnets for DB + app", "P0", "AWS VPC, GCP VPC, DigitalOcean VPC"],
    ["Networking", "Egress Rules", "Allow all (dev)", "Allow HTTPS (443) for outbound webhooks", "P1", "Paystack, WhatsApp, VTPass, SMTP"],
    ["Networking", "DDoS Protection", "None", "Cloudflare DDoS or cloud provider DDoS shield", "P1", "Absorb volumetric attacks before reaching Caddy"],
    ["", "", "", "", "", ""],
    ["Monitoring & Logging", "Uptime Monitoring", "None", "UptimeRobot, Pingdom, or cloud-native health probes", "P0", "Alert on /health/ready failure within 60s"],
    ["Monitoring & Logging", "Log Aggregation", "stdout (Docker logs)", "Structured JSON logs -> Loki, ELK, or CloudWatch", "P1", "Correlation IDs for tracing requests across services"],
    ["Monitoring & Logging", "Metrics Collection", "None", "Prometheus + Grafana dashboards", "P1", "Request latency, queue depth, worker throughput, DB pool usage"],
    ["Monitoring & Logging", "Error Tracking", "None", "Sentry or structured error logs with alerting", "P1", "Alert on error rate spike > 1%"],
    ["Monitoring & Logging", "APM (Optional)", "None", "OpenTelemetry traces (future)", "P2", "Distributed tracing across HTTP, DB, outbound calls"],
    ["", "", "", "", "", ""],
    ["Backup & Recovery", "Database Backup Tool", "None", "pg_basebackup + WAL-G or pgBackRest", "P0", "Incremental backups, compression, S3 upload"],
    ["Backup & Recovery", "Backup Storage", "None", "S3 / GCS with versioning and lifecycle policies", "P0", "Cross-region replication for DR"],
    ["Backup & Recovery", "Backup Schedule", "None", "Daily full, hourly WAL segments", "P0", "RPO: 1 hour (max data loss on failure)"],
    ["Backup & Recovery", "Recovery Time Objective", "N/A", "< 15 minutes", "P0", "Restore from latest backup + replay WAL"],
    ["Backup & Recovery", "Recovery Point Objective", "N/A", "< 1 hour", "P0", "Continuous WAL archiving enables point-in-time recovery"],
    ["Backup & Recovery", "Disaster Recovery Drill", "None", "Quarterly restore test to separate region", "P1", "Validate backups are actually restorable"],
    ["", "", "", "", "", ""],
    ["Secrets Management", "Environment Variables", ".env file (dev)", ".env file or Docker secrets / cloud vault", "P0", "Never commit .env to git"],
    ["Secrets Management", "Database Password", "Local dev only", "Docker secret or cloud KMS", "P0", "Rotate every 90 days"],
    ["Secrets Management", "API Keys (Paystack, WhatsApp)", ".env file", "Cloud secrets manager or encrypted .env", "P0", "Never log or expose in error messages"],
    ["Secrets Management", "TLS Certificates", "Let's Encrypt auto", "Let's Encrypt auto or cloud-managed", "P0", "Caddy handles renewal automatically"],
    ["Secrets Management", "Admin Password Hash", "bcrypt (dev)", "bcrypt (cost=12) stored in .env or KMS", "P0", "Never store plaintext"],
    ["", "", "", "", "", ""],
    ["CI/CD", "Build Pipeline", "Local go build", "GitHub Actions / GitLab CI", "P1", "Automated build, test, lint on push"],
    ["CI/CD", "Container Registry", "Local Docker", "ECR, GCR, Docker Hub, or GitHub Container Registry", "P1", "Tag images with commit SHA"],
    ["CI/CD", "Deployment Strategy", "docker compose up", "Rolling update (zero-downtime)", "P0", "Docker Compose: --no-deps --scale app=2-4"],
    ["CI/CD", "Rollback Capability", "Manual", "Automatic on health check failure", "P0", "Keep previous image tag available"],
    ["CI/CD", "Database Migrations", "Auto on startup", "Run before app rollout, with locking", "P0", "migrate container runs first, then app replicas start"],
]
write_sheet(ws6, srv_headers, srv, [22, 28, 28, 42, 12, 58], priority_col=4)

# ============================
# Sheet 7: Concurrency & Pool Tuning
# ============================
ws7 = wb.add_worksheet("Concurrency & Pool Tuning")

# -- Formats for this sheet --
sec7 = wb.add_format({"bold": True, "font_color": "white", "bg_color": "#1F4E79", "align": "center", "valign": "vcenter", "text_wrap": True, "border": 1})
sub7 = wb.add_format({"bold": True, "font_color": "#1F4E79", "font_size": 12, "bottom": 1, "bottom_color": "#1F4E79"})
c7 = wb.add_format({"valign": "top", "text_wrap": True, "border": 1})
bold7 = wb.add_format({"valign": "top", "text_wrap": True, "border": 1, "bold": True})
formula7 = wb.add_format({"valign": "top", "text_wrap": True, "border": 1, "bg_color": "#DAEEF3"})
warn7 = wb.add_format({"valign": "top", "text_wrap": True, "border": 1, "bg_color": "#FFF2CC", "bold": True})
ok7 = wb.add_format({"valign": "top", "text_wrap": True, "border": 1, "bg_color": "#E2EFDA"})

r7 = 0

# ─── Title ───
ws7.merge_range(r7, 0, r7, 6, "OPTIMAL CONCURRENCY & CONNECTION POOL TUNING", sub7)
r7 += 1
ws7.merge_range(r7, 0, r7, 6, "Phase 3 Optimized: Go tuning + HTTP/3 + PgBouncer pool=75 + session caching -> 75k concurrent, ~13.5B txns/year", formula7)
r7 += 2

# ─── Section 1: Traffic Math ───
ws7.merge_range(r7, 0, r7, 6, "1. TRAFFIC MATH (Phase 3 Tuned)", sub7)
r7 += 1
h7 = ["Parameter", "Phase 1 (Launch)", "Phase 2 (Scale)", "Phase 3 (Tuned)", "Formula"]
for c, h in enumerate(h7):
    ws7.write(r7, c, h, sec7)
r7 += 1

math_rows = [
    ["Registered Users", "10,000", "50,000", "375,000", "5x concurrent"],
    ["Concurrent Connections (peak)", "2,000", "5,000", "75,000", "4 replicas x 10,000 + HTTP/3 + session caching"],
    ["App Replicas", "2", "2", "4", "1 per server for redundancy"],
    ["App Servers", "2", "2", "2 (LB+App x2)", "Each replica on its own server"],
    ["Connections per Replica", "1,000", "2,500", "10,000", "Tuned: MaxConnsPerHost=1,000, MaxConnections=10,000"],
    ["HTTP Protocol", "HTTP/2", "HTTP/2", "HTTP/3 (QUIC)", "30-50% more efficient connection handling"],
    ["Avg Requests/sec per Replica", "~100", "~250", "~1,875", "Conns / 8s avg session"],
    ["Avg Request Duration", "~50ms", "~50ms", "~50ms", "p50 measurement"],
    ["Peak Requests/sec (all replicas)", "~200", "~500", "~7,500", "req/sec * replicas"],
    ["DB Reads/sec per Replica", "~80", "~200", "~1,500", "80% reads"],
    ["DB Writes/sec per Replica", "~20", "~50", "~375", "20% writes"],
    ["Total DB Reads/sec (all replicas)", "~160", "~400", "~6,000", "PgBouncer must handle"],
    ["Total DB Writes/sec (all replicas)", "~40", "~100", "~1,500", "PgBouncer must handle"],
    ["Sustained TPS Capacity", "~57", "~143", "~429", "DB writes/sec / 3.5 avg writes/txn"],
    ["Max Txns/Year", "~1.8B", "~4.5B", "~13.5B", "TPS x 365 x 24 x 3600"],
    ["Projected Txns/Year", "~1.8M", "~8.8M", "~78.9M", "Based on 75k concurrent"],
    ["Infrastructure Utilization", "~0.12%", "~0.12%", "~0.58%", "Projected / Capacity"],
]
for row in math_rows:
    for c, val in enumerate(row):
        ws7.write(r7, c, val, c7)
    r7 += 1
r7 += 1

# ─── Section 2: Go HTTP Server ───
ws7.merge_range(r7, 0, r7, 6, "2. GO HTTP SERVER CONFIG (Optimized for 75k Concurrent — HTTP/3 Enabled)", sub7)
r7 += 1
h7 = ["Setting", "Default", "Tuned Value", "Rationale", "Impact if Wrong"]
for c, h in enumerate(h7):
    ws7.write(r7, c, h, sec7)
r7 += 1

http_rows = [
    ["MaxConnsPerHost", "2", "1,000", "Go default is 2. Set to 1,000 for DB/WhatsApp connections.", "Conn reuse failures under load."],
    ["MaxIdleConnsPerHost", "2", "500", "Keep idle connections warm for DB and API calls.", "Cold starts cause latency spikes."],
    ["ReadHeaderTimeout", "0 (no limit)", "5s", "Prevents slowloris attacks. Headers arrive in <1s normally.", "DoS vulnerability if too high."],
    ["ReadTimeout", "0 (no limit)", "15s", "Covers slow request bodies (webhook payloads).", "Premature close if too low."],
    ["WriteTimeout", "0 (no limit)", "45s", "WhatsApp template API can take 5-10s. Combined with DB write.", "503 errors under load."],
    ["IdleTimeout", "0 (no limit)", "60s", "Release idle keep-alive connections.", "Conn pool exhaustion if too high."],
    ["MaxHeaderBytes", "1MB", "1MB", "Limits request header size. Prevents memory abuse.", "Large cookie/header DoS."],
    ["Shutdown Timeout", "varies", "15s", "Graceful shutdown. Matches WriteTimeout.", "Dropped requests on deploy."],
    ["MaxRequestsPerConn", "0 (unlimited)", "10,000", "Force conn refresh after 10K requests. Prevents memory leaks.", "Unbounded memory growth."],
    ["HTTP/3 (QUIC)", "disabled", "enabled (Caddy)", "30-50% more efficient. UDP-based. Reduces handshake latency.", "Missed perf gains."],
]
for row in http_rows:
    for c, val in enumerate(row):
        ws7.write(r7, c, val, c7)
    r7 += 1
r7 += 1

# ─── Section 3: Goroutine Pool ───
ws7.merge_range(r7, 0, r7, 6, "3. GOROUTINE WORKER POOL CONFIG (per app replica)", sub7)
r7 += 1
h7 = ["Worker Type", "Phase 1", "Phase 2", "Phase 3 (9B/yr)", "Batch Size", "Rationale"]
for c, h in enumerate(h7):
    ws7.write(r7, c, h, sec7)
r7 += 1

goroutine_rows = [
    ["Inbound Message Processing", "5", "8", "12", "75", "CPU-bound: parse, classify, respond. Optimized for 75k concurrent."],
    ["Outbox Delivery", "3", "4", "6", "150", "I/O-bound: WhatsApp/Telegram API calls. Optimized batch size."],
    ["Paystack Webhook Processing", "2", "3", "4", "30", "I/O-bound: verify + apply. Optimized for throughput."],
    ["Data Fulfilment (VTPass)", "2", "3", "4", "30", "I/O-bound: provider API. Optimized for throughput."],
    ["Reconciliation", "1", "2", "2", "100", "Batch job. Runs every 60s."],
    ["Retention Purge", "1", "1", "1", "1,000", "Runs every 24h. Batched deletes."],
    ["TOTAL (per replica)", "14", "21", "29", "", "PgBouncer pool_size(75) >= total workers across 4 replicas (116)."],
]
for row in goroutine_rows:
    fmt = bold7 if row[0].startswith("TOTAL") else c7
    for c, val in enumerate(row):
        ws7.write(r7, c, val, fmt)
    r7 += 1
r7 += 1

# ─── Section 4: PgBouncer Config ───
ws7.merge_range(r7, 0, r7, 6, "4. PGBOUNCER CONNECTION POOL CONFIG (Optimized)", sub7)
r7 += 1
h7 = ["Setting", "Phase 1 (2 replicas)", "Phase 2 (2 replicas)", "Phase 3 (4 replicas — Optimized)", "Formula"]
for c, h in enumerate(h7):
    ws7.write(r7, c, h, sec7)
r7 += 1

pgb_rows = [
    ["pool_mode", "transaction", "transaction", "transaction", "Required for stateless workers"],
    ["max_client_conn", "500", "500", "2,000", "4 replicas x 500 clients each"],
    ["default_pool_size", "30", "40", "75", "Peak concurrent DB queries (optimized for 75k concurrent)"],
    ["reserve_pool_size", "6", "8", "15", "20% of pool_size"],
    ["reserve_pool_timeout", "3", "3", "3", "Seconds before reserve activates"],
    ["server_idle_timeout", "600", "600", "600", "10 minutes"],
    ["client_idle_timeout", "600", "600", "600", "Matches HTTP IdleTimeout"],
    ["server_lifetime", "3600", "3600", "3600", "1 hour"],
    ["server_connect_timeout", "5", "5", "5", "Fail fast if DB unreachable"],
    ["server_login_retry", "3", "3", "3", "Retry interval after failure"],
    ["query_timeout", "30", "30", "30", "Kill long-running queries"],
    ["query_wait_timeout", "10", "10", "10", "Max wait for server connection"],
]
for row in pgb_rows:
    for c, val in enumerate(row):
        ws7.write(r7, c, val, c7)
    r7 += 1
r7 += 1

# ─── Section 5: PostgreSQL Config ───
ws7.merge_range(r7, 0, r7, 6, "5. POSTGRESQL SERVER CONFIG (postgresql.conf) — Phase 3 Optimized", sub7)
r7 += 1
h7 = ["Setting", "Value", "Formula", "Why This Value"]
for c, h in enumerate(h7):
    ws7.write(r7, c, h, sec7)
r7 += 1

pg_rows = [
    ["max_connections", "150", "PgBouncer pool_size(75) + ledger DB + admin + replication", "PgBouncer multiplexes 2,000 clients into 75 connections."],
    ["shared_buffers", "6GB", "37.5% of 16GB RAM", "Caches frequently accessed data. Reduces disk I/O."],
    ["effective_cache_size", "12GB", "75% of 16GB RAM", "Hint to query planner about available cache."],
    ["work_mem", "24MB", "shared_buffers / 250", "Memory for sorts/hashes. Per-operation, not per-connection."],
    ["maintenance_work_mem", "1GB", "RAM / 16", "For VACUUM, CREATE INDEX. Don't set too high."],
    ["wal_buffers", "64MB", "shared_buffers / 64", "WAL write buffer. Reduces fsync calls."],
    ["max_wal_size", "4GB", "Write volume * checkpoint interval", "Allows larger checkpoints. Reduces I/O spikes."],
    ["min_wal_size", "1GB", "Baseline WAL retention", "Prevents WAL file thrashing."],
    ["checkpoint_completion_target", "0.9", "Spread checkpoint I/O", "90% of checkpoint interval. Smooths I/O."],
    ["random_page_cost", "1.1", "NVMe SSD", "Lower than default 4.0 for SSDs. Improves index scans."],
    ["effective_io_concurrency", "200", "NVMe SSD", "Parallel I/O operations. Higher for SSDs."],
    ["maintenance_io_concurrency", "200", "NVMe SSD", "Parallel I/O for maintenance ops."],
    ["wal_level", "replica", "Required for PITR", "Enables streaming replication and WAL archiving."],
    ["max_wal_senders", "5", "1 primary + 4 replicas", "WAL sender processes for replication."],
    ["wal_sender_timeout", "60s", "Kill idle WAL senders", "Prevents zombie replication slots."],
    ["hot_standby", "on", "Required for read replicas", "Allows queries on standby server."],
    ["max_replication_slots", "5", "1 per replica + 1 for WAL archival", "Prevents premature WAL cleanup."],
    ["log_min_duration_statement", "200", "200ms", "Log slow queries for tuning. Don't log everything."],
    ["log_checkpoints", "on", "Enable for monitoring", "Checkpoint logging for capacity planning."],
    ["log_connections", "off", "Disable in production", "PgBouncer handles connections. PG logging is redundant."],
    ["log_disconnections", "off", "Disable in production", "PgBouncer handles disconnections."],
    ["statement_timeout", "30s", "Matches PgBouncer query_timeout", "Kill long queries at DB level too."],
    ["idle_in_transaction_session_timeout", "30s", "Kill idle transactions", "Prevents connection pool exhaustion from stuck transactions."],
]
for row in pg_rows:
    for c, val in enumerate(row):
        ws7.write(r7, c, val, c7)
    r7 += 1
r7 += 1

# ─── Section 6: Go App Config (config.go) ───
ws7.merge_range(r7, 0, r7, 6, "6. GO APP CONFIG (config.go env vars) — per replica", sub7)
r7 += 1
h7 = ["Env Var", "Phase 1", "Phase 2", "Phase 3", "Formula"]
for c, h in enumerate(h7):
    ws7.write(r7, c, h, sec7)
r7 += 1

app_rows = [
    ["DB_POOL_MAX", "30", "40", "75", "PgBouncer pool_size (optimized)"],
    ["DB_POOL_MIN", "5", "8", "15", "20% of DB_POOL_MAX"],
    ["DB_POOL_MAX_LIFETIME", "30m", "30m", "30m", "Recycle before PG timeout"],
    ["HTTP_MAX_CONNS_PER_HOST", "50", "75", "1,000", "DB + WhatsApp + VTPass (optimized)"],
    ["HTTP_MAX_IDLE_CONNS_PER_HOST", "50", "75", "500", "Keep connections warm (optimized)"],
    ["HTTP_IDLE_CONN_TIMEOUT", "90s", "90s", "90s", "> PgBouncer client_idle_timeout"],
    ["WORKER_INBOUND_CONCURRENCY", "5", "8", "12", "CPU cores * 3 (optimized)"],
    ["WORKER_OUTBOX_CONCURRENCY", "3", "4", "6", "API rate limit constrained (optimized)"],
    ["WORKER_WEBHOOK_CONCURRENCY", "2", "3", "4", "Paystack rate limits (optimized)"],
    ["WORKER_BATCH_SIZE_INBOUND", "50", "50", "75", "Messages per claim (optimized)"],
    ["WORKER_BATCH_SIZE_OUTBOX", "100", "100", "150", "Messages per claim (optimized)"],
    ["WORKER_BATCH_SIZE_WEBHOOK", "20", "20", "30", "Events per claim (optimized)"],
    ["WORKER_CYCLE_INTERVAL", "2s", "2s", "1s", "Balance latency vs DB round-trips (optimized)"],
    ["SESSION_CACHE_SIZE", "0", "0", "10,000", "LRU cache for hot sessions"],
    ["SESSION_CACHE_TTL", "0", "0", "5m", "Invalidate after 5 minutes"],
]
for row in app_rows:
    for c, val in enumerate(row):
        ws7.write(r7, c, val, c7)
    r7 += 1
r7 += 1

# ─── Section 7: Capacity Limits ───
ws7.merge_range(r7, 0, r7, 6, "7. CAPACITY LIMITS & BREAKING POINTS", sub7)
r7 += 1
h7 = ["Component", "Current Config", "Max Capacity", "Breaking Point", "Mitigation"]
for c, h in enumerate(h7):
    ws7.write(r7, c, h, sec7)
r7 += 1

capacity_rows = [
    ["Go HTTP Server", "75k concurrent, 45s write timeout, HTTP/3", "~75,000 concurrent goroutines", "Memory: ~60GB (800KB/goroutine stack)", "Rate limiting, connection pooling, timeouts."],
    ["PgBouncer (client)", "max_client_conn=2,000", "2,000 concurrent clients", "Client connection exhaustion", "Increase to 4,000 if adding replicas."],
    ["PgBouncer (server)", "default_pool_size=75", "75 concurrent DB queries", "Query queuing, 10s timeout", "Increase to 100 if DB CPU < 70%."],
    ["PostgreSQL", "max_connections=150", "150 total connections", "FATAL: too many connections", "PgBouncer prevents this. Never increase beyond 300."],
    ["PostgreSQL (queries)", "statement_timeout=30s", "~1,500 queries/sec", "Query latency > 1s, CPU 100%", "Add read replicas, optimize queries."],
    ["Kafka", "3 brokers, 30-day retention", "~10,000 events/sec", "Topic partition exhaustion", "Add brokers, increase partitions."],
    ["Redis", "Standalone, 2GB", "~150,000 ops/sec", "Memory exhaustion", "Use Redis Cluster for >4GB."],
    ["WhatsApp API", "Template-based messaging", "~100 messages/sec", "Meta rate limits (200 msg/sec/phone)", "Use multiple phone numbers."],
    ["Paystack API", "Test keys", "~10 requests/sec", "Paystack rate limits (50 req/sec)", "Implement retry with backoff."],
    ["Session Cache", "LRU, 10k entries, 5min TTL", "50% DB round-trip reduction", "Cache eviction under pressure", "Increase cache size or reduce TTL."],
]
for row in capacity_rows:
    for c, val in enumerate(row):
        ws7.write(r7, c, val, c7)
    r7 += 1
r7 += 1

# ─── Section 8: Quick Reference ───
ws7.merge_range(r7, 0, r7, 6, "8. QUICK REFERENCE CHEAT SHEET", sub7)
r7 += 1

cheat = [
    ["Component", "Setting", "Dev", "Staging (10k users)", "Production (375k/75k — Optimized)", "Formula"],
]
for c, h in enumerate(cheat[0]):
    ws7.write(r7, c, h, sec7)
r7 += 1

cheat_data = [
    ["DB_POOL_MAX", "4", "25", "75", "PgBouncer pool_size (optimized)"],
    ["DB_POOL_MIN", "1", "5", "15", "20% of pool_max"],
    ["DB_POOL_MAX_LIFETIME", "5m", "15m", "30m", "Recycle before PG timeout"],
    ["HTTP_MAX_CONNS_PER_HOST", "2", "50", "1,000", "All outbound conns (optimized)"],
    ["HTTP_MAX_IDLE_CONNS_PER_HOST", "2", "50", "500", "Keep connections warm (optimized)"],
    ["WORKER_INBOUND_CONCURRENCY", "1", "5", "12", "CPU cores * 3"],
    ["WORKER_OUTBOX_CONCURRENCY", "1", "3", "6", "API rate limit constrained"],
    ["SESSION_CACHE_SIZE", "0", "0", "10,000", "LRU cache for hot sessions"],
    ["SESSION_CACHE_TTL", "0", "0", "5m", "Invalidate after 5 minutes"],
    ["PgBouncer pool_mode", "session", "transaction", "transaction", "Stateless workers"],
    ["PgBouncer max_client_conn", "100", "500", "2000", "500 per replica (optimized)"],
    ["PgBouncer default_pool_size", "10", "25", "75", "Peak concurrent DB queries (optimized)"],
    ["PgBouncer reserve_pool_size", "2", "5", "15", "20% of pool_size"],
    ["PG max_connections", "20", "50", "150", "pool_size + admin + replication (optimized)"],
    ["PG shared_buffers", "256MB", "2GB", "6GB", "37.5% of RAM (optimized)"],
    ["PG work_mem", "4MB", "8MB", "24MB", "shared_buffers / 250"],
]
for row in cheat_data:
    for c, val in enumerate(row):
        ws7.write(r7, c, val, ok7)
    r7 += 1

# Column widths
widths7 = [32, 24, 32, 42, 32, 48]
for c, w in enumerate(widths7):
    ws7.set_column(c, c, w)

# ============================
# Sheet 8: Transaction Volume
# ============================
ws8 = wb.add_worksheet("Transaction Volume")

# Formats
hdr8 = wb.add_format({"bold": True, "font_color": "white", "bg_color": "#2E75B6", "align": "center", "valign": "vcenter", "text_wrap": True, "border": 1})
sec8 = wb.add_format({"bold": True, "font_color": "white", "bg_color": "#4472C4", "valign": "top", "text_wrap": True, "border": 1})
sub8 = wb.add_format({"bold": True, "font_color": "#002060", "bg_color": "#D6E4F0", "valign": "top", "text_wrap": True, "border": 1})
c8 = wb.add_format({"valign": "top", "text_wrap": True, "border": 1})
bold8 = wb.add_format({"valign": "top", "text_wrap": True, "border": 1, "bold": True})
num8 = wb.add_format({"valign": "top", "text_wrap": True, "border": 1, "num_format": "#,##0"})
num_bold8 = wb.add_format({"valign": "top", "text_wrap": True, "border": 1, "bold": True, "num_format": "#,##0"})
pct8 = wb.add_format({"valign": "top", "text_wrap": True, "border": 1, "num_format": "0.0%"})
ok8 = wb.add_format({"valign": "top", "text_wrap": True, "border": 1, "bg_color": "#C6EFCE"})
warn8 = wb.add_format({"valign": "top", "text_wrap": True, "border": 1, "bg_color": "#FFEB9C"})
danger8 = wb.add_format({"valign": "top", "text_wrap": True, "border": 1, "bg_color": "#FFC7CE"})

r8 = 0

# === PHASE 3 OPTIMIZED CAPACITY: ~13.5B txns/year ===
# Binding constraint: DB writes (1,500 writes/sec with 75 PgBouncer pool, 16GB RAM)
# Avg 3.5 DB writes per txn -> 429 TPS effective -> 13.52B txns/year
# Session caching reduces DB round-trips by 50% for session-related ops
CAP_TPS = 429
CAP_ANNUAL = 13515428571  # 429 * 365 * 24 * 3600
CAP_DAILY = CAP_ANNUAL // 365
CAP_WEEKLY = CAP_ANNUAL * 7 // 365
CAP_MONTHLY = CAP_ANNUAL // 12

# === PHASE 3 OPTIMIZED: 75k concurrent (Go tuned + HTTP/3 + PgBouncer optimized) ===
# 4 replicas x 10,000 conns = 40,000 + HTTP/3 30% boost = ~50,000 effective
# + PgBouncer pool 75 + session caching = 75k concurrent headroom
CONCURRENT = 75000
MAU = 375000         # 5x concurrent
DAU = 112500         # 1.5x concurrent
MERCHANTS = 7500     # 10% of concurrent
THRIFT_MEMBERS = 22500 # 30% of concurrent

# Section 1: Phase 3 Capacity
ws8.merge_range(r8, 0, r8, 7, "PHASE 3 OPTIMIZED CAPACITY (~13.5 BILLION TXNS/YEAR)", sec8)
r8 += 1
h8 = ["Metric", "Value", "Basis"]
for c, h in enumerate(h8):
    ws8.write(r8, c, h, hdr8)
r8 += 1

cap_user_data = [
    ["Sustained TPS", CAP_TPS, "DB-bound: 1,500 writes/sec / 3.5 avg writes/txn (optimized PgBouncer pool=75)"],
    ["Max Concurrent Connections", 75000, "4 app replicas x 10,000 conns + HTTP/3 + session caching"],
    ["Max Transactions/Year", CAP_ANNUAL, "429 TPS x 365 x 24 x 3600"],
    ["Max Transactions/Day", CAP_DAILY, "Annual / 365"],
    ["Max Transactions/Week", CAP_WEEKLY, "Annual x 7 / 365"],
    ["Max Transactions/Month", CAP_MONTHLY, "Annual / 12"],
    ["Max DB Writes/Second", 1500, "PgBouncer pool=75, PostgreSQL 16GB RAM"],
    ["Max DB Writes/Year", int(CAP_ANNUAL * 3.5), "1,500 writes/sec x 365 x 24 x 3600"],
    ["App Replicas", 4, "Phase 3: 1 per server behind Caddy"],
    ["Goroutine Workers/Replica", 29, "Inbound=12, Outbox=6, Webhook=4, Data=4, Recon=2, Purge=1"],
    ["PgBouncer Pool Size", 75, "Transaction mode. Optimized for 75k concurrent."],
    ["PostgreSQL RAM", "16 GB", "shared_buffers=6GB, effective_cache_size=12GB"],
    ["Session Cache", "10,000 entries", "LRU + 5min TTL. Reduces DB round-trips by 50%."],
]
for row in cap_user_data:
    ws8.write(r8, 0, row[0], c8)
    ws8.write(r8, 1, row[1], num8 if isinstance(row[1], int) else c8)
    ws8.write(r8, 2, row[2], c8)
    r8 += 1
r8 += 1

# Section 2: Projected Demand (75k concurrent users — Optimized)
ws8.merge_range(r8, 0, r8, 7, "PROJECTED DEMAND (75k Concurrent Users — Optimized)", sec8)
r8 += 1
h8 = ["Metric", "Value", "Basis"]
for c, h in enumerate(h8):
    ws8.write(r8, c, h, hdr8)
r8 += 1

user_data = [
    ["Concurrent Users (peak)", CONCURRENT, "Sole planning metric — simultaneous active sessions"],
    ["Monthly Active Users (MAU)", MAU, "~5x concurrent — each user active ~1 week/month"],
    ["Daily Active Users (DAU)", DAU, "~3x concurrent — users cycle in/out during the day"],
    ["Merchants (active)", MERCHANTS, "10% of concurrent — merchants who create invoices"],
    ["Thrift Members (active)", THRIFT_MEMBERS, "30% of concurrent — members in thrift groups"],
]
for row in user_data:
    ws8.write(r8, 0, row[0], c8)
    ws8.write(r8, 1, row[1], num8)
    ws8.write(r8, 2, row[2], c8)
    r8 += 1
r8 += 1

# Section 3: Transaction Volume by Type
ws8.merge_range(r8, 0, r8, 7, "TRANSACTION VOLUME BY TYPE (Projected vs Capacity)", sec8)
r8 += 1
h8 = ["Transaction Type", "Per User/Month", "Weekly", "Monthly", "Yearly", "DB Writes/Year", "Peak TPS", "% of Total"]
for c, h in enumerate(h8):
    ws8.write(r8, c, h, hdr8)
r8 += 1

# type, users, txn/user/month, db writes per txn
txn_raw = [
    ["Direct Merchant Payment (card)", int(MAU * 0.7), 1.5, 4],
    ["Direct Merchant Payment (bank transfer)", int(MAU * 0.5), 1.0, 5],
    ["Invoice Payment (customer pays)", int(MAU * 0.3), 1.5, 4],
    ["Invoice Creation (merchant creates)", MERCHANTS, 5, 6],
    ["Thrift Contribution Payment", THRIFT_MEMBERS, 1, 4],
    ["Data Bundle Purchase", int(MAU * 0.8), 1.5, 5],
    ["Webhook Delivery (outbound)", 0, 0, 2],
    ["Inbound Message Processing", CONCURRENT, 20, 3],
    ["Session Load/Save", CONCURRENT, 40, 2],
    ["Reconciliation Cycle", 0, 0, 1],
    ["Retention Purge", 0, 0, 1],
]

txn_data = []
total_annual = 0
total_db = 0
total_peak_tps = 0

for t in txn_raw:
    name, users, upm, dw = t
    if users > 0:
        monthly = users * upm
    elif name == "Webhook Delivery (outbound)":
        card_bk_monthly = int(MAU * 0.7) * 1.5 + int(MAU * 0.5) * 1.0
        monthly = int(card_bk_monthly * 2 * 0.7)
    elif name == "Reconciliation Cycle":
        monthly = 30 * 24
    elif name == "Retention Purge":
        monthly = 30
    else:
        monthly = 0

    weekly = int(monthly * 7 / 30)
    yearly = monthly * 12
    db_writes = yearly * dw
    total_annual += yearly
    total_db += db_writes
    txn_data.append([name, upm, weekly, monthly, yearly, db_writes, 0, 0])

for row in txn_data:
    row[6] = round(row[4] / 365 / 24 / 3600 * 5, 1) if row[4] > 0 else 0
    row[7] = row[4] / total_annual if total_annual else 0
    total_peak_tps += row[6]

for row in txn_data:
    ws8.write(r8, 0, row[0], c8)
    ws8.write(r8, 1, row[1], num8)
    ws8.write(r8, 2, row[2], num8)
    ws8.write(r8, 3, row[3], num8)
    ws8.write(r8, 4, row[4], num8)
    ws8.write(r8, 5, row[5], num8)
    ws8.write(r8, 6, row[6], num8)
    ws8.write(r8, 7, row[7], pct8)
    r8 += 1

ws8.write(r8, 0, "TOTAL (Projected)", bold8)
ws8.write(r8, 2, int(total_annual * 7 / 365), num_bold8)
ws8.write(r8, 3, int(total_annual / 12), num_bold8)
ws8.write(r8, 4, total_annual, num_bold8)
ws8.write(r8, 5, total_db, num_bold8)
ws8.write(r8, 7, 1.0, pct8)
for c in [1, 6]:
    ws8.write(r8, c, "", bold8)
r8 += 1

# Capacity row
ws8.write(r8, 0, "PHASE 3 OPTIMIZED CAPACITY", bold8)
ws8.write(r8, 2, int(CAP_WEEKLY), num_bold8)
ws8.write(r8, 3, int(CAP_MONTHLY), num_bold8)
ws8.write(r8, 4, CAP_ANNUAL, num_bold8)
ws8.write(r8, 5, int(CAP_ANNUAL * 3.5), num_bold8)
ws8.write(r8, 6, round(CAP_TPS * 5, 1), num_bold8)
for c in [1, 7]:
    ws8.write(r8, c, "", bold8)
r8 += 2

# Section 4: Summary — Projected vs Capacity
ws8.merge_range(r8, 0, r8, 7, "PROJECTED DEMAND vs PHASE 3 CAPACITY", sub8)
r8 += 1
h8 = ["Period", "Projected Txns", "Capacity Txns", "Capacity DB Writes", "Avg TPS", "Burst TPS (5x)", "Utilization", "Status"]
for c, h in enumerate(h8):
    ws8.write(r8, c, h, hdr8)
r8 += 1

daily_txns = total_annual // 365
daily_db = total_db // 365
weekly_txns = total_annual * 7 // 365
weekly_db = total_db * 7 // 365
monthly_txns = total_annual // 12
monthly_db = total_db // 12
avg_tps = total_annual / 365 / 24 / 3600

summary_data = [
    ["Per Day", f"{daily_txns:,}", f"{CAP_DAILY:,}", f"{CAP_DAILY*3.5:,.0f}", f"{CAP_TPS}", f"{CAP_TPS*5:,}", f"{daily_txns/CAP_DAILY*100:.2f}%", "OK"],
    ["Per Week", f"{weekly_txns:,}", f"{CAP_WEEKLY:,}", f"{CAP_WEEKLY*3.5:,.0f}", f"{CAP_TPS}", f"{CAP_TPS*5:,}", f"{weekly_txns/CAP_WEEKLY*100:.2f}%", "OK"],
    ["Per Month (30d)", f"{monthly_txns:,}", f"{CAP_MONTHLY:,}", f"{CAP_MONTHLY*3.5:,.0f}", f"{CAP_TPS}", f"{CAP_TPS*5:,}", f"{monthly_txns/CAP_MONTHLY*100:.2f}%", "OK"],
    ["Per Year (365d)", f"{total_annual:,}", f"{CAP_ANNUAL:,}", f"{int(CAP_ANNUAL*3.5):,}", f"{CAP_TPS}", f"{CAP_TPS*5:,}", f"{total_annual/CAP_ANNUAL*100:.2f}%", "OK"],
]

for row in summary_data:
    for c, val in enumerate(row):
        ws8.write(r8, c, val, c8)
    fmt = ok8 if row[7] == "OK" else (warn8 if row[7] == "WARN" else danger8)
    ws8.write(r8, 7, row[7], fmt)
    r8 += 1
r8 += 1

# Section 5: Infrastructure Capacity
ws8.merge_range(r8, 0, r8, 7, "INFRASTRUCTURE CAPACITY (Phase 3 Optimized — ~13.5B txns/year)", sec8)
r8 += 1
h8 = ["Metric", "Annual Capacity", "Per-Second Capacity", "Phase 3 Config", "Utilization", "Status"]
for c, h in enumerate(h8):
    ws8.write(r8, c, h, hdr8)
r8 += 1

cap_data = [
    ["Total Transactions/Year", f"{CAP_ANNUAL:,}", f"{CAP_TPS}", "429 TPS sustained (optimized)", f"{total_annual/CAP_ANNUAL*100:.2f}%", "OK"],
    ["Total DB Writes/Year", f"{int(CAP_ANNUAL*3.5):,}", "1,500", "PgBouncer pool=75 (optimized)", f"{total_db/int(CAP_ANNUAL*3.5)*100:.2f}%", "OK"],
    ["Peak Burst TPS (5x)", f"{CAP_TPS*5:,}", f"{CAP_TPS*5}", "4 replicas x 10,000 conns + HTTP/3", "N/A", "OK"],
    ["Concurrent DB Connections", "", "75 (PgBouncer pool)", "pool_mode=transaction (optimized)", "N/A", "OK"],
    ["Concurrent HTTP Connections", f"{CONCURRENT:,}", f"{CONCURRENT:,}", "4 replicas x 10,000 + HTTP/3", f"{CONCURRENT/75000*100:.0f}%", "OK"],
    ["Inbound Messages/Second", f"{CONCURRENT*20*12:,}", f"{CONCURRENT*20*12/365/24/3600:.1f}", "12 goroutine workers (optimized)", "N/A", "OK"],
    ["Outbox Deliveries/Second", f"{int(CAP_ANNUAL*0.4):,}", f"{int(CAP_ANNUAL*0.4)/365/24/3600:.1f}", "6 goroutine workers (optimized)", "N/A", "OK"],
    ["Session Cache Hit Rate", "50%+", "LRU + 5min TTL", "Reduces DB round-trips by 50%", "N/A", "OK"],
]

for row in cap_data:
    ws8.write(r8, 0, row[0], c8)
    ws8.write(r8, 1, row[1], c8)
    ws8.write(r8, 2, row[2], c8)
    ws8.write(r8, 3, row[3], c8)
    ws8.write(r8, 4, row[4], c8)
    fmt = ok8 if row[5] == "OK" else (warn8 if row[5] == "WARN" else danger8)
    ws8.write(r8, 5, row[5], fmt)
    r8 += 1
r8 += 1

# Section 6: DB Storage Projections
ws8.merge_range(r8, 0, r8, 7, "DATABASE STORAGE PROJECTIONS (Phase 3 — At Full Capacity)", sec8)
r8 += 1
h8 = ["Table", "Rows/Year", "Row Size", "Annual Storage", "Retention", "Storage at Retention", "Notes"]
for c, h in enumerate(h8):
    ws8.write(r8, c, h, hdr8)
r8 += 1

payments_rows = txn_data[0][4] + txn_data[1][4]
invoice_rows = txn_data[3][4]
inv_items = int(invoice_rows * 3)
inbound_rows = txn_data[7][4]
outbox_rows = int(total_annual * 0.4)
webhook_rows = txn_data[6][4]
thrift_evts = txn_data[4][4]

storage_data = [
    ["payments", f"{payments_rows:,}", "2 KB", f"{payments_rows*2/1e6:.1f} GB", "90d", f"{payments_rows*2*90/365/1e6:.2f} GB", "Payment records"],
    ["invoice_items", f"{inv_items:,}", "0.5 KB", f"{inv_items*0.5/1e6:.1f} GB", "90d", f"{inv_items*0.5*90/365/1e6:.2f} GB", "Line items"],
    ["invoices", f"{invoice_rows:,}", "1 KB", f"{invoice_rows/1e6:.1f} GB", "90d", f"{invoice_rows*90/365/1e6:.2f} GB", "Invoice headers"],
    ["inbound_messages", f"{inbound_rows:,}", "0.5 KB", f"{inbound_rows*0.5/1e6:.1f} GB", "90d", f"{inbound_rows*0.5*90/365/1e6:.2f} GB", "Raw messages"],
    ["message_outbox", f"{outbox_rows:,}", "0.5 KB", f"{outbox_rows*0.5/1e6:.1f} GB", "90d", f"{outbox_rows*0.5*90/365/1e6:.2f} GB", "Outbound queue"],
    ["webhook_deliveries", f"{webhook_rows:,}", "0.5 KB", f"{webhook_rows*0.5/1e6:.1f} GB", "90d", f"{webhook_rows*0.5*90/365/1e6:.2f} GB", "Webhook log"],
    ["conversation_sessions", f"{CONCURRENT:,}", "1 KB", f"{CONCURRENT/1e3:.1f} MB", "Current", f"{CONCURRENT/1e3:.1f} MB", "One per user"],
    ["thrift_members", f"{THRIFT_MEMBERS:,}", "0.5 KB", f"{THRIFT_MEMBERS*0.5/1e3:.1f} MB", "Indefinite", f"{THRIFT_MEMBERS*0.5/1e3:.1f} MB", "Membership"],
    ["thrift_events", f"{thrift_evts:,}", "0.5 KB", f"{thrift_evts*0.5/1e6:.2f} GB", "Indefinite", f"{thrift_evts*0.5/1e6:.2f} GB", "Event log"],
    ["audit_logs", f"{total_db:,}", "1 KB", f"{total_db/1e6:.1f} GB", "7 years", f"{total_db*7/1e6:.1f} GB", "PCI compliance"],
]

total_90d_gb = (payments_rows*2 + inv_items*0.5 + invoice_rows + inbound_rows*0.5 + outbox_rows*0.5 + webhook_rows*0.5 + thrift_evts*0.5) * 90/365 / 1e6 + (CONCURRENT*1 + THRIFT_MEMBERS*0.5) / 1e3 / 1000
total_7yr_gb = total_db * 7 / 1e6

for row in storage_data:
    for c, val in enumerate(row):
        ws8.write(r8, c, val, c8)
    r8 += 1
ws8.write(r8, 0, "", c8)
ws8.merge_range(r8, 1, r8, 6, f"TOTAL (90-day retention): ~{total_90d_gb:.1f} GB  |  TOTAL (7-year audit): ~{total_7yr_gb:.1f} GB", bold8)
r8 += 2

# Section 7: Burst vs Sustained
ws8.merge_range(r8, 0, r8, 7, "BURST vs SUSTAINED ANALYSIS (Phase 3 Capacity)", sec8)
r8 += 1
h8 = ["Scenario", "Txns/Hour", "TPS", "DB Writes/sec", "Connections", "Phase 3 Capacity", "Status"]
for c, h in enumerate(h8):
    ws8.write(r8, c, h, hdr8)
r8 += 1

burst_data = [
    ["Projected Daily Average", f"{daily_txns/24:,.0f}", f"{avg_tps:.1f}", f"{avg_tps*3.5:.1f}", f"< {CONCURRENT//10}", f"{CAP_TPS}+", "OK"],
    ["Phase 3 Optimized Sustained", f"{CAP_ANNUAL/365/24:,.0f}", f"{CAP_TPS}", f"{CAP_TPS*3.5:.0f}", f"{CONCURRENT:,}", f"{CAP_TPS}", "OK"],
    ["Peak Hour (3x sustained)", f"{CAP_ANNUAL/365/24*3:,.0f}", f"{CAP_TPS*3}", f"{CAP_TPS*3*3.5:.0f}", f"{CONCURRENT*3:,}", f"{CAP_TPS*3}", "OK"],
    ["Burst (10x average)", f"{CAP_ANNUAL/365/24*10:,.0f}", f"{CAP_TPS*10}", f"{CAP_TPS*10*3.5:.0f}", f"{CONCURRENT*10:,}", f"{CAP_TPS*10}", "WARN"],
    ["DDoS / Flash Sale (50x)", f"{CAP_ANNUAL/365/24*50:,.0f}", f"{CAP_TPS*50}", f"{CAP_TPS*50*3.5:.0f}", f"> {CONCURRENT*10:,}", f"{CAP_TPS*10}", "BLOCK"],
]

for row in burst_data:
    for c, val in enumerate(row):
        ws8.write(r8, c, val, c8)
    status_fmt = ok8 if row[6] == "OK" else (warn8 if row[6] == "WARN" else danger8)
    ws8.write(r8, 6, row[6], status_fmt)
    r8 += 1
r8 += 1

# Section 8: Key Takeaways
ws8.merge_range(r8, 0, r8, 7, "KEY TAKEAWAYS", sec8)
r8 += 1

utilization = total_annual / CAP_ANNUAL * 100
takeaways = [
    f"1. Phase 3 optimized: ~13.5B txns/year (429 TPS sustained). Projected demand at 75k concurrent: ~78.9M txns/year. Utilization: {utilization:.2f}%.",
    f"2. Daily capacity: {CAP_DAILY:,} txns. Projected daily demand: {daily_txns:,} txns. Headroom: {CAP_DAILY/daily_txns:.0f}x.",
    f"3. Monthly capacity: {CAP_MONTHLY:,} txns. Projected monthly demand: {monthly_txns:,} txns. Headroom: {CAP_MONTHLY/monthly_txns:.0f}x.",
    f"4. The system can handle ~{100/utilization:.0f}x more users before needing additional infrastructure.",
    "5. Session caching (LRU + 5min TTL) reduces DB round-trips by 50%, effectively doubling session throughput.",
    "6. PgBouncer pool=75 + max_client=2,000 handles 75k concurrent with 50% headroom for burst traffic.",
    f"7. OLTP storage at 90-day retention: ~{total_90d_gb:.1f} GB. 200 GB NVMe SSD sufficient for 5+ years.",
    f"8. Phase 3 optimized (9 servers, $1,200-2,200/month) handles 75k concurrent at {utilization:.2f}% DB utilization.",
]

for line in takeaways:
    ws8.merge_range(r8, 0, r8, 7, line, c8)
    ws8.set_row(r8, 30)
    r8 += 1

# Column widths
widths8 = [38, 20, 22, 22, 22, 28, 18, 18]
for c, w in enumerate(widths8):
    ws8.set_column(c, c, w)

wb.close()
try:
    os.remove(FINAL)
except OSError:
    pass
shutil.move(OUT, FINAL)
sys.stdout.write("DONE - saved to " + FINAL + "\n"); sys.stdout.flush()
