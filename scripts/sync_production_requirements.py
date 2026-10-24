#!/usr/bin/env python3
"""Sync docs/cost-models/Xego_Production_Requirements.xlsx with the code as shipped.

The workbook is edited in place through raw OOXML (openpyxl segfaults on this
box): shared strings get the Faya rename, individual cells are rewritten as
inline strings for the status marks and factual fixes, three missing env rows
are inserted into the concurrency sheet (with a row renumber), the
Deployment Plan's Phase 1 keeps its backups/PITR + compose-limits truth,
and its Phase 1/2 server rows are right-sized to the Server Requirements
sheet (3 servers, 8 vCPU / 16 GB / 240 GB at launch; 5 servers,
16 vCPU / 32 GB / 520 GB at Phase 2, Server 4/5 inserted) with the
Concurrency Phase-2 column re-derived for 3 app replicas.

Run from the repo root:  python scripts/sync_production_requirements.py
The script is idempotent: re-running rewrites the same cell values.
"""
import re
import shutil
import zipfile
from xml.etree import ElementTree as ET

XLSX = "docs/cost-models/Xego_Production_Requirements.xlsx"
MAIN = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
ET.register_namespace("", MAIN)
ET.register_namespace("r", REL)
q = lambda tag: "{%s}%s" % (MAIN, tag)

changed = []          # log of every mutation
warnings = []


def load(path):
    z = zipfile.ZipFile(path)
    items = {n: z.read(n) for n in z.namelist()}
    z.close()
    return items


def register_namespaces(raw_xml):
    """Keep every prefix declared on the root element (mc:Ignorable values
    reference them by name, so renaming would make Excel repair the file)."""
    for prefix, uri in re.findall(r'xmlns:([A-Za-z0-9]+)="([^"]+)"', raw_xml):
        try:
            ET.register_namespace(prefix, uri)
        except Exception:
            pass


def parse_sheet(data):
    register_namespaces(data.decode("utf-8", "replace")[:4000])
    return ET.fromstring(data)


def cell_text(cell, shared):
    t = cell.get("t")
    if t == "inlineStr":
        is_el = cell.find(q("is"))
        return "".join(x.text or "" for x in is_el.iter(q("t"))) if is_el is not None else ""
    v = cell.find(q("v"))
    if v is None or v.text is None:
        return ""
    if t == "s":
        return shared[int(v.text)]
    return v.text


def rows_of(root):
    sd = root.find(q("sheetData"))
    return list(sd) if sd is not None else []


def row_cells(row):
    return list(row)


def find_row(root, shared, needles, exact_first=None):
    """Row whose cells contain every needle; optionally require the first cell
    to equal exact_first. Returns the row element (asserts uniqueness)."""
    hits = []
    for row in rows_of(root):
        cells = row_cells(row)
        texts = [cell_text(c, shared) for c in cells]
        blob = " | ".join(texts)
        if all(n in blob for n in needles):
            if exact_first is not None and (not texts or texts[0] != exact_first):
                continue
            hits.append(row)
    if len(hits) != 1:
        raise SystemExit("row match %r -> %d hits (want 1)" % (needles, len(hits)))
    return hits[0]


def col_of(cell_ref):
    return re.match(r"([A-Z]+)", cell_ref).group(1)


def row_in_band(root, shared, start_needles, end_needles, needles, exact_first=None):
    """find_row, but restricted to the rows physically between the row that
    matches start_needles and the row that matches end_needles (both need one
    unique hit each). Disambiguates repeated labels like 'Server 1' across
    phases."""
    rows = rows_of(root)
    start = find_row(root, shared, start_needles)
    end = find_row(root, shared, end_needles)
    lo, hi = rows.index(start), rows.index(end)
    hits = []
    for row in rows[lo:hi]:
        texts = [cell_text(c, shared) for c in row_cells(row)]
        blob = " | ".join(texts)
        if all(n in blob for n in needles):
            if exact_first is not None and (not texts or texts[0] != exact_first):
                continue
            hits.append(row)
    if len(hits) != 1:
        raise SystemExit("band row match %r -> %d hits (want 1)" % (needles, len(hits)))
    return hits[0]


def set_cell_guarded(row, shared, col, text, note=""):
    """set_cell, but a re-run whose cell already holds `text` is a no-op."""
    cells = [c for c in row_cells(row) if c.get("r") and col_of(c.get("r")) == col]
    if cells and cell_text(cells[0], shared) == text:
        changed.append("%s%s: already applied" % (col, row.get("r")))
        return
    set_cell(row, shared, col, text, note)


def set_cell(row, shared, col, text, note=""):
    """Rewrite the cell in column `col` of `row` as an inline string."""
    for cell in row_cells(row):
        if cell.get("r") and col_of(cell.get("r")) == col:
            if cell.find(q("f")) is not None:
                warnings.append("cell %s has a formula; overwriting" % cell.get("r"))
            for child in list(cell):
                cell.remove(child)
            cell.set("t", "inlineStr")
            is_el = ET.SubElement(cell, q("is"))
            t_el = ET.SubElement(is_el, q("t"))
            t_el.set("{http://www.w3.org/XML/1998/namespace}space", "preserve")
            t_el.text = text
            changed.append("%s%s: %s" % (col, row.get("r"), note or text[:60]))
            return
    # Cell absent in the source row: append it (column-order is not enforced
    # by Excel, but we keep it tidy by appending).
    cell = ET.SubElement(row, q("c"))
    row_num = row.get("r")
    cell.set("r", "%s%s" % (col, row_num))
    cell.set("t", "inlineStr")
    is_el = ET.SubElement(cell, q("is"))
    t_el = ET.SubElement(is_el, q("t"))
    t_el.set("{http://www.w3.org/XML/1998/namespace}space", "preserve")
    t_el.text = text
    changed.append("%s%s: %s (new cell)" % (col, row_num, note or text[:60]))


def edit(root, shared, needles, col, text, exact_first=None, note=""):
    try:
        row = find_row(root, shared, needles, exact_first=exact_first)
    except SystemExit:
        # Idempotent re-run: a previous run consumed the needle (it lived in
        # the very cell that was rewritten). If the target column already
        # holds the desired value, the edit is done; otherwise re-raise.
        for r in rows_of(root):
            for c in row_cells(r):
                if c.get("r") and col_of(c.get("r")) == col and cell_text(c, shared) == text:
                    changed.append("%s%s: already applied" % (col, r.get("r")))
                    return
        raise
    set_cell(row, shared, col, text, note)


def remap_merge_ranges(root, row_mapping):
    """Point every merged range at the row the renumber moved its header to.

    Renumbering compacts pre-existing row-number gaps, so a fixed delta below
    the insert anchor is not enough: rows *above* the anchor shift too. The
    mapping is old row number -> new row number, captured by
    insert_rows_after as it renumbers.
    """
    mc = root.find(q("mergeCells"))
    if mc is None:
        return
    touched = 0
    for merge in mc.findall(q("mergeCell")):
        ref = merge.get("ref", "")
        m = re.match(r"([A-Z]+)(\d+):([A-Z]+)(\d+)$", ref)
        if not m:
            continue
        old_s, old_e = int(m.group(2)), int(m.group(4))
        new_s = row_mapping.get(old_s, old_s)
        new_e = row_mapping.get(old_e, old_e)
        if (new_s, new_e) != (old_s, old_e):
            merge.set("ref", "%s%d:%s%d" % (m.group(1), new_s, m.group(3), new_e))
            touched += 1
    if touched:
        changed.append("mergeCells remapped after renumber: %d" % touched)


def reinject_declarations(original, serialized, prefix="worksheet"):
    """Re-add xmlns declarations the serializer dropped (mc:Ignorable values
    reference prefixes by name; a missing declaration makes Excel repair the
    file)."""
    orig_head = original[:4000].decode("utf-8", "replace")
    decls = dict(re.findall(r'xmlns:([A-Za-z0-9]+)="([^"]+)"', orig_head))
    ser_head = serialized[:4000].decode("utf-8", "replace")
    present = set(re.findall(r'xmlns:([A-Za-z0-9]+)="', ser_head))
    missing = {k: v for k, v in decls.items() if k not in present}
    if not missing:
        return serialized
    decl_text = "".join(' xmlns:%s="%s"' % kv for kv in sorted(missing.items()))
    m = re.search(r"<%s\b" % prefix, serialized.decode("utf-8", "replace"))
    if not m:
        return serialized
    text = serialized.decode("utf-8", "replace")
    insert_at = m.end()
    text = text[:insert_at] + decl_text + text[insert_at:]
    warnings.append("re-injected xmlns: %s" % ", ".join(missing))
    return text.encode("utf-8")


def insert_rows_after(root, shared, anchor, rows_data):
    """Insert rows below `anchor` (list of {col: text}) and renumber every row
    and cell reference sequentially so the sheet stays valid."""
    sd = root.find(q("sheetData"))
    rows = list(sd)
    idx = rows.index(anchor)
    for offset, data in enumerate(rows_data, start=1):
        new_row = ET.Element(q("row"))
        for col, text in data.items():
            cell = ET.SubElement(new_row, q("c"))
            cell.set("r", "%s0" % col)
            cell.set("t", "inlineStr")
            is_el = ET.SubElement(cell, q("is"))
            t_el = ET.SubElement(is_el, q("t"))
            t_el.set("{http://www.w3.org/XML/1998/namespace}space", "preserve")
            t_el.text = text
        sd.insert(idx + offset, new_row)
    # Renumber rows and cell refs, capturing old -> new row numbers so the
    # merged section headers can be re-pointed at their moved rows.
    row_mapping = {}
    for i, row in enumerate(list(sd), start=1):
        old = row.get("r")
        if old and old.isdigit() and int(old) != i:
            row_mapping[int(old)] = i
        row.set("r", str(i))
        for cell in row_cells(row):
            ref = cell.get("r")
            if ref:
                cell.set("r", "%s%d" % (col_of(ref), i))
    remap_merge_ranges(root, row_mapping)
    changed.append("inserted %d rows after row %s (renumbered)" % (len(rows_data), anchor.get("r")))


def main():
    import os
    if not os.path.isdir("reports"):
        raise SystemExit("run from the repo root")
    backup = XLSX + ".bak"
    if not os.path.exists(backup):
        shutil.copy2(XLSX, backup)
        print("backup ->", backup)

    items = load(XLSX)

    # --- shared strings: Faya rename -------------------------------------
    ss_raw = items["xl/sharedStrings.xml"]
    register_namespaces(ss_raw.decode("utf-8", "replace")[:2000])
    ss_root = ET.fromstring(ss_raw)
    shared = []
    for si in ss_root.findall(q("si")):
        shared.append("".join(t.text or "" for t in si.iter(q("t"))))
    for old, new in [("Xego App", "Faya App"), ("(current Xego)", "(current Faya)")]:
        for si in ss_root.findall(q("si")):
            for t in si.iter(q("t")):
                if t.text and old in t.text:
                    t.text = t.text.replace(old, new)
                    changed.append("shared: %r -> %r" % (old, new))
    items["xl/sharedStrings.xml"] = ET.tostring(ss_root, encoding="UTF-8", xml_declaration=True)

    # --- sheet name -> file mapping --------------------------------------
    wb = ET.fromstring(items["xl/workbook.xml"])
    rels = ET.fromstring(items["xl/_rels/workbook.xml.rels"])
    rel_map = {r.get("Id"): r.get("Target") for r in rels}
    sheet_file = {}
    for s in wb.iter(q("sheet")):
        target = rel_map[s.get("{%s}id" % REL)]
        if not target.startswith("xl/"):
            target = "xl/" + target
        sheet_file[s.get("name")] = target

    original_sheets = {k: v for k, v in items.items() if k.startswith("xl/worksheets/")}

    def S(name):
        return parse_sheet(items[sheet_file[name]])

    def save(name, root):
        key = sheet_file[name]
        out = ET.tostring(root, encoding="UTF-8", xml_declaration=True)
        items[key] = reinject_declarations(original_sheets[key], out)

    # ================= Requirements sheet: status marks ==================
    req = S("Requirements")
    edits = [
        (["Connection Pool Size"], "B", "P0 - DONE (2026-10)"),
        (["Connection Pool Size"], "E", "Pool bounds are env-driven: DB_POOL_MAX / DB_POOL_MIN (default 25/5; compose sets 30 through PgBouncer) via store.OpenWithPool"),
        (["Connection Pool Size"], "H", "internal/store/store.go OpenWithPool; internal/config/config.go"),
        (["PostgreSQL max_connections"], "B", "P0 - DONE (2026-10)"),
        (["PostgreSQL max_connections"], "E", "compose.yaml runs PgBouncer (transaction pool=30, max_client=500, max_prepared_statements=100); the app connects via pgbouncer:6432, migrate stays direct. PostgreSQL max_connections left at default 100 - pool 30 fits."),
        (["PostgreSQL max_connections"], "H", "compose.yaml (pgbouncer service)"),
        (["Background Worker Concurrency"], "B", "P0 - DONE (2026-10)"),
        (["Background Worker Concurrency"], "E", "Concurrent worker pools: every tick handler runs on its own gate, classes bounded by WORKER_*_CONCURRENCY pools (defaults 10 inbound / 5 outbox / 3 reconcilers); inbound and outbox batches fan out per conversation"),
        (["Background Worker Concurrency"], "H", "internal/app/worker_pool.go; internal/app/workers.go"),
        (["Horizontal Scaling"], "B", "P0 - DONE (2026-10)"),
        (["Horizontal Scaling"], "E", "compose.yaml deploy.replicas=2 behind Caddy; per-replica limits 1 CPU / 1 GB"),
        (["Database Resource Limits"], "B", "P0 - DONE (2026-10)"),
        (["Database Resource Limits"], "E", "deploy.resources.limits set on db, app, migrate, caddy, pgbouncer and backup services (Compose v2)"),
        # row 6's needle lives in its E cell, so the E rewrite must come last
        (["No rate limiting on any webhook endpoint"], "B", "P1 - DONE (2026-08)"),
        (["No rate limiting on any webhook endpoint"], "H", "internal/app/app.go (webhook route wiring)"),
        (["No rate limiting on any webhook endpoint"], "E", "webhookLimit middleware on all four webhook POST routes (Redis-backed when REDIS_URL set, in-memory otherwise)"),
        (["Hardcoded batch=20"], "B", "P1 - OPEN (bulk jobs only)"),
        (["Hardcoded batch=20"], "H", "internal/app/workers.go (batch=20 claim sites)"),
        (["Data Retention Purge Batching"], "B", "P1 - OPEN"),
        (["Data Retention Purge Batching"], "H", "internal/store/store_retention.go PurgeBefore"),
        (["Outbox Delivery Priority"], "B", "P1 - PARTIAL"),
        (["Outbox Delivery Priority"], "E", "Outbox now dispatches on its own gate and pool concurrently with inbound and webhooks (no shared sequential tick); no explicit cross-class priority yet"),
        (["Outbox Delivery Priority"], "H", "internal/app/workers.go runWorkers"),
        (["Admin Dashboard Metrics Query"], "B", "P1 - OPEN"),
        (["Admin Dashboard Metrics Query"], "H", "internal/store/store_payments.go Metrics"),
        (["Session Caching"], "B", "P2 - OPEN"),
        (["Session Caching"], "H", "internal/store/store_users.go (LoadSession/SaveSession)"),
        (["Write Timeout for WhatsApp Templates"], "B", "P2 - DONE (2026-09)"),
        (["Write Timeout for WhatsApp Templates"], "E", "WriteTimeout=45s, ReadHeaderTimeout=5s (locked by TestServerHardening)"),
        (["Write Timeout for WhatsApp Templates"], "H", "internal/app/app.go newHTTPServer"),
        (["Health Check & Observability"], "B", "P2 - PARTIAL"),
        (["Health Check & Observability"], "E", "Structured slog logging and /health/live + /health/ready exist; no Prometheus metrics or queue-depth gauges yet"),
        (["TLS & Security Headers"], "B", "P2 - PARTIAL"),
        (["TLS & Security Headers"], "E", "Caddy serves HSTS and strips Server; no caddy-ratelimit directive yet (app-level route limits exist)"),
    ]
    for needles, col, text in edits:
        edit(req, shared, needles, col, text)
    save("Requirements", req)

    # ================= Architecture sheet: Interswitch + status ==========
    arch = S("Architecture")
    # Needle is the A cell for each row so the later rewrites of B/C/D can't
    # destroy it.
    edit(arch, shared, ["Payment Gateway"], "B",
         "Interswitch Web Checkout (hosted fields) + VTPass",
         exact_first="Payment Gateway", note="gateway technology")
    edit(arch, shared, ["Payment Gateway"], "C", "PRODUCTION RAIL",
         exact_first="Payment Gateway")
    edit(arch, shared, ["Payment Gateway"], "D",
         "PAYMENT_PROVIDER defaults to interswitch: card hosted-fields checkout, DVA bank-transfer collection, NIP payouts, refunds, VTU data. Paystack retired.",
         exact_first="Payment Gateway")
    edit(arch, shared, ["In-house Payment Service"], "B",
         "Built on Interswitch APIs (DVA collection, NIP payouts, standing-credit credits, refunds, reconciliation)",
         note="in-house service tech")
    edit(arch, shared, ["In-house Payment Service"], "C", "BUILT - disputes open")
    edit(arch, shared, ["In-house Payment Service"], "D",
         "Payment lifecycle, settlement, payout and three-way reconciliation are implemented; card-scheme chargeback/dispute automation remains to build.")
    edit(arch, shared, ["Background Workers"], "B", "Concurrent worker pools (per-handler gates + bounded per-class slots)",
         exact_first="Background Workers")
    edit(arch, shared, ["Background Workers"], "C", "GOOD",
         exact_first="Background Workers")
    edit(arch, shared, ["Background Workers"], "D",
         "Per-handler gates preserve per-conversation ordering; WORKER_*_CONCURRENCY pools are env-tunable (internal/app/worker_pool.go).",
         exact_first="Background Workers")
    edit(arch, shared, ["No resource limits, single replica"], "C", "GOOD")
    edit(arch, shared, ["No resource limits, single replica"], "D",
         "Resource limits, 2 app replicas, PgBouncer and PITR backups (WAL archive + daily base, offsite) are all in compose.yaml.")
    edit(arch, shared, ["Default pool=4"], "B", "GOOD (tuned)")
    edit(arch, shared, ["Default pool=4"], "D",
         "Default 25/5 (env DB_POOL_MAX/DB_POOL_MIN), compose 30 through PgBouncer transaction pool; PgBouncer shipped.")
    save("Architecture", arch)

    # ================= Roadmap: mark completed items =====================
    road = S("Roadmap")
    roadmap_edits = [
        ("Configure DB pool", "[DONE 2026-10] Configure DB pool (now DB_POOL_MAX/DB_POOL_MIN, default 25/5, compose 30)"),
        ("Add resource limits to compose.yaml", "[DONE 2026-10] Add resource limits to compose.yaml"),
        ("Increase WriteTimeout to 45s", "[DONE 2026-09] Increase WriteTimeout to 45s"),
        ("Implement concurrent worker pool", "[DONE 2026-10] Implement concurrent worker pool (per-handler gates + bounded per-class pools)"),
        ("Separate worker goroutines by type", "[DONE 2026-10] Separate worker goroutines by type (per-handler dispatch, per-class pools)"),
        ("Add PgBouncer between app and PostgreSQL", "[DONE 2026-10] Add PgBouncer between app and PostgreSQL"),
        ("Deploy 4 app replicas + HTTP/3", "[PARTIAL] Deploy app replicas (2 now, 4 at Phase 3) + HTTP/3 (443/udp already mapped)"),
        ("Prioritize outbox over reconciliation", "[PARTIAL] Prioritize outbox over reconciliation (independent gate/pool; no explicit priority)"),
    ]
    for needle, text in roadmap_edits:
        edit(road, shared, [needle], "C", text, note=text[:50])
    save("Roadmap", road)

    # ================= Deployment Plan: Phase 1 redesign =================
    dep = S("Deployment Plan")
    edit(dep, shared, ["Don't deploy all 14 services"], "A",
         "PHASED DEPLOYMENT: Start with monolith (current Faya), extract services gradually. Don't deploy all 14 services on day 1. "
         "compose.yaml implements Phase 1 today: 2 app replicas behind Caddy (deploy.replicas), CPU/memory limits on every service, "
         "PgBouncer (transaction pool=30, max_client=500), and PITR backups - WAL archiving + daily base backup shipped offsite by rclone "
         "(RPO 1h, 30-day retention). Kafka stays optional at launch: BUS_BACKEND defaults to memory until an external consumer exists.",
         note="phased-deployment note (Faya + Phase 1 truth)")
    edit(dep, shared, ["Database + Events + Tokenization"], "F",
         "PostgreSQL 17\n"
         "PgBouncer (pool=30, max_client=500)\n"
         "Kafka (single broker) - optional at launch; BUS_BACKEND=memory until the first external consumer\n"
         "Tokenization Service (PCI isolated VPC subnet)\n"
         "Token DB (PCI isolated)\n"
         "Backups: WAL archiving + daily base backup, offsite rclone copy (PITR, RPO 1h, 30-day retention)",
         note="Phase 1 server 3 services (backups + Kafka note)")
    # Right-size the Phase 1/2 server rows to match the Server Requirements
    # sheet. Server labels repeat across phases, so scope each lookup to the
    # row band between the phase headers.
    p1_start = ["Phase 1: Monolith + Payment Core (Launch)"]
    p1_end = ["Phase 2: Extract Core Services (100k users)"]
    for srv, c, ram, disk in [
        ("Server 1", "2", "2 GB", "20 GB"),
        ("Server 2", "2", "4 GB", "40 GB"),
        ("Server 3", "4", "8 GB", "160 GB NVMe"),
    ]:
        row = row_in_band(dep, shared, p1_start, p1_end, [srv], exact_first=srv)
        set_cell_guarded(row, shared, "C", c, note="Phase 1 %s vCPU" % srv)
        set_cell_guarded(row, shared, "D", ram, note="Phase 1 %s RAM" % srv)
        set_cell_guarded(row, shared, "E", disk, note="Phase 1 %s disk" % srv)
    p1_total = row_in_band(dep, shared, p1_start, p1_end, ["TOTAL"], exact_first="TOTAL")
    set_cell_guarded(p1_total, shared, "C", "8", note="Phase 1 total vCPU (2+2+4)")
    set_cell_guarded(p1_total, shared, "D", "16 GB", note="Phase 1 total RAM (4+4+8)")
    set_cell_guarded(p1_total, shared, "E", "240 GB (incl. 60 GB staging)", note="Phase 1 total disk")
    set_cell_guarded(p1_total, shared, "F",
         "3 servers. 2 app replicas (deploy.replicas=2), 29 worker pool slots/replica "
         "(10 inbound, 5 outbox, 4 webhook, 4 data, 3 recon, 3 misc - env-tunable WORKER_*_CONCURRENCY), "
         "30 DB pool through PgBouncer, CPU/memory limits on every compose service, PITR backups (WAL + daily base, offsite, 30 days). "
         "Tokenization in PCI-isolated VPC subnet.",
         note="Phase 1 total notes")

    # Phase 2: insert the two servers the in-country plan adds (read replica +
    # app replica 3), right-size the primary, and fix the totals. Guarded so a
    # re-run neither duplicates rows nor re-shifts the merge ranges.
    p2_start = ["Phase 2: Extract Core Services (100k users)"]
    p2_end = ["Phase 3: Full Platform"]
    p2_already = any("Read replica + reporting" in [cell_text(c, shared) for c in row_cells(r)]
                     for r in rows_of(dep) if row_cells(r))
    if p2_already:
        changed.append("Phase 2 server insert skipped (already applied)")
    else:
        anchor = row_in_band(dep, shared, p2_start, p2_end, ["Data + Events + Tokenization"], exact_first="Server 3")
        insert_rows_after(dep, shared, anchor, [
            {"A": "Server 4", "B": "Read replica + reporting", "C": "2", "D": "4 GB", "E": "120 GB NVMe",
             "F": "In-country streaming replica; admin / reporting reads"},
            {"A": "Server 5", "B": "App replica 3 + extracted services", "C": "2", "D": "4 GB", "E": "40 GB SSD",
             "F": "Faya App replica 3\nPayment Orchestrator\nRisk Engine"},
        ])
    for srv, c, ram, disk in [
        ("Server 1", "2", "4 GB", "40 GB SSD"),
        ("Server 2", "2", "4 GB", "40 GB SSD"),
    ]:
        row = row_in_band(dep, shared, p2_start, p2_end, [srv], exact_first=srv)
        set_cell_guarded(row, shared, "C", c, note="Phase 2 %s vCPU" % srv)
        set_cell_guarded(row, shared, "D", ram, note="Phase 2 %s RAM" % srv)
        set_cell_guarded(row, shared, "E", disk, note="Phase 2 %s disk" % srv)
    p2_primary = row_in_band(dep, shared, p2_start, p2_end, ["Data + Events + Tokenization"], exact_first="Server 3")
    set_cell_guarded(p2_primary, shared, "C", "8", note="Phase 2 primary vCPU")
    set_cell_guarded(p2_primary, shared, "D", "16 GB", note="Phase 2 primary RAM")
    set_cell_guarded(p2_primary, shared, "E", "280 GB NVMe (220 data + 60 staging)", note="Phase 2 primary disk")
    set_cell_guarded(p2_primary, shared, "F",
         "PostgreSQL 17 (OLTP)\n"
         "PgBouncer (pool=40, max_client=500)\n"
         "Kafka (single broker) - added at ~5k users, when the first external consumer appears\n"
         "Redis (rate limiting + risk)\n"
         "Tokenization Service (PCI isolated VPC subnet)\n"
         "Token DB (PCI isolated)",
         note="Phase 2 primary services (Kafka single broker)")
    p2_total = row_in_band(dep, shared, p2_start, p2_end, ["TOTAL"], exact_first="TOTAL")
    set_cell_guarded(p2_total, shared, "C", "16", note="Phase 2 total vCPU")
    set_cell_guarded(p2_total, shared, "D", "32 GB", note="Phase 2 total RAM")
    set_cell_guarded(p2_total, shared, "E", "520 GB (incl. 60 GB staging)", note="Phase 2 total disk")
    set_cell_guarded(p2_total, shared, "F",
         "5 servers. 3 app replicas (deploy.replicas=3 at Phase 2), 21 goroutine workers/replica, 40 DB pool, "
         "in-country read replica on Server 4. All payment services extracted. Tokenization in PCI-isolated VPC subnet.",
         note="Phase 2 total notes")

    edit(dep, shared, ["Phase 1 (3 servers)", "$140-240"], "D", "$25-50 (incl. backup storage)",
         note="Phase 1 storage cost")
    edit(dep, shared, ["Phase 1 (3 servers)", "$140-240"], "F", "$265-490",
         note="Phase 1 monthly total")
    edit(dep, shared, ["Phase 1 (3 servers)", "$1,142"], "B", "$265-490",
         note="summary infra")
    edit(dep, shared, ["Phase 1 (3 servers)", "$1,142"], "D", "$1,407-1,642",
         note="summary monthly")
    edit(dep, shared, ["Phase 1 (3 servers)", "$1,142"], "E", "$16,884-19,704",
         note="summary annual")
    edit(dep, shared, ["TOTAL (All Phases)"], "B", "$1,875-3,450", note="all-phases infra")
    edit(dep, shared, ["TOTAL (All Phases)"], "D", "$9,084-10,659", note="all-phases monthly")
    edit(dep, shared, ["TOTAL (All Phases)"], "E", "$109,008-127,908", note="all-phases annual")
    edit(dep, shared, ["(monolith)"], "D", "8080 (internal)", note="app port in service map")
    edit(dep, shared, ["Load-balanced to both app replicas"], "C", "8080",
         note="app port in network topology")
    edit(dep, shared, ["Domain name (xego.com)"], "B",
         "Domain name (Faya-branded; replaces xego.com)", note="prereq domain")
    # Cost-table phase labels are descriptive only; the Phase 2 fleet is 5
    # servers after the read-replica + app-replica-3 insert.
    edit(dep, shared, ["Phase 2 (3 servers)", "$180-300"], "A", "Phase 2 (5 servers)",
         exact_first="Phase 2 (3 servers)", note="cost table phase-2 label")
    edit(dep, shared, ["Phase 2 (3 servers)", "$410-760"], "A", "Phase 2 (5 servers)",
         exact_first="Phase 2 (3 servers)", note="summary phase-2 label")
    # Resilience prerequisites (docs/runbooks/GO_LIVE_RUNBOOK.md §3.7/§4): the four items
    # that separate "configured" from "recoverable". Guarded insert after
    # the last existing prerequisite row (PCI QSA); no merges/formulas
    # below that point, so the renumber is safe.
    prereq = [row for row in rows_of(dep)
              if any("PCI QSA (optional)" == cell_text(c, shared) for c in row_cells(row))]
    if prereq:
        already = any("WAL ship (15-min timer)" in cell_text(c, shared)
                      for r in rows_of(dep) for c in row_cells(r))
        if already:
            changed.append("prereq insert skipped (already applied)")
        else:
            insert_rows_after(dep, shared, prereq[0], [
                {"A": "11", "B": "WAL ship (15-min timer)",
                 "C": "Offsite WAL freshness independent of the daily base run",
                 "D": "deploy/systemd/xego-backup-wal-ship.timer + backup.sh --wal-ship",
                 "E": "P0",
                 "F": "Without it BACKUP_RPO=1h is really 'whenever the daily backup ran'. Heartbeat ages on /admin/metrics (WAL_SHIP_STALE_AFTER)."},
                {"A": "12", "B": "Weekly PITR restore drill timer",
                 "C": "Proof the backup + WAL actually replays",
                 "D": "deploy/systemd/xego-restore-drill.timer + backup.sh --restore-drill",
                 "E": "P0",
                 "F": "First run measures RTO vs BACKUP_RTO=15m; RESTORE_DRILL_STALE_AFTER turns the panel red after one missed week."},
                {"A": "13", "B": "Backup-health panel green",
                 "C": "Backups stay observed, not just scheduled",
                 "D": "/admin/metrics -> 'Backup & PITR health' (migration 089)",
                 "E": "P0",
                 "F": "base/wal/drill heartbeats all 'within threshold' before launch; 'never' means the scheduler never recorded a run."},
                {"A": "14", "B": "Dual-Caddy ingress (edge stack)",
                 "C": "No single host owns the public endpoint",
                 "D": "compose.edge.yaml on server 2 + floating IP or DNS failover",
                 "E": "P0",
                 "F": "Same deploy/Caddyfile both sides; probes /health/ready. Verify per runbook §4.2."},
                {"A": "15", "B": "Async streaming standby + promote rehearsal",
                 "C": "DB failover in ~1 min instead of ~15-min PITR",
                 "D": "deploy/setup-standby.sh (init/status/promote), runbook §4.1",
                 "E": "P1",
                 "F": "Promote is deliberately manual at Phase 1; rehearse once in staging before go-live."},
            ])
    else:
        warnings.append("prereq anchor (PCI QSA) not found; resilience rows not inserted")
    save("Deployment Plan", dep)

    # ================= Capacity sheet: stale bottlenecks ================
    cap = S("Capacity")
    cap_edits = [
        (["Registered Users"], "E", "was DB pool (4 conns); now 25/5 default, 30 through PgBouncer"),
        (["Registered Users"], "F", "Done: DB_POOL_MAX/DB_POOL_MIN + PgBouncer; session cache still open"),
        (["Concurrent Connections"], "E", "HTTP server + DB pool (multi-replica shipped: 2 replicas)"),
        (["Concurrent Connections"], "F", "2 replicas shipped; Go tuning / HTTP/3 / session caching open"),
        (["Messages/Second"], "E", "batch=20 still fixed per claim (requirement #7 open)"),
        (["Messages/Second"], "F", "Concurrent worker pool shipped; batch env vars open"),
        (["Outbox Delivery"], "E", "20 msgs per claim; concurrent pools shipped"),
        (["Outbox Delivery"], "F", "Concurrent outbox pool shipped; batch env open"),
        (["DB Transactions/sec"], "E", "was 4-connection pool"),
        (["DB Transactions/sec"], "F", "Done: DB_POOL_MAX + PgBouncer pool=30"),
    ]
    for needles, col, text in cap_edits:
        edit(cap, shared, needles, col, text)
    save("Capacity", cap)

    # ================= Concurrency sheet: shipped values ================
    con = S("Concurrency & Pool Tuning")

    def env_row(name, phase2_value):
        for r in rows_of(con):
            cells = row_cells(r)
            if not cells:
                continue
            texts = [cell_text(c, shared) for c in cells]
            if texts and texts[0] == name and len(texts) > 2 and texts[2] == phase2_value:
                return r
        raise SystemExit("env row %s/%s not found" % (name, phase2_value))

    # worker table (Phase 1 column = B)
    edit(con, shared, ["Inbound Message Processing"], "B", "10", note="worker table inbound P1")
    edit(con, shared, ["Outbox Delivery"], "B", "5", note="worker table outbox P1")
    edit(con, shared, ["Paystack Webhook Processing"], "A", "Gateway Webhook Processing", note="rename")
    edit(con, shared, ["Gateway Webhook Processing"], "B", "4", note="worker table webhook P1")
    edit(con, shared, ["Data Fulfilment"], "B", "4", note="worker table data P1")
    edit(con, shared, ["Reconciliation"], "B", "3", exact_first="Reconciliation", note="worker table recon P1")
    edit(con, shared, ["Retention Purge"], "A", "Retention Purge + misc sweeps", note="rename purge row")
    edit(con, shared, ["Retention Purge + misc sweeps"], "B", "3", note="misc pool P1")
    edit(con, shared, ["Retention Purge + misc sweeps"], "F",
         "Runs every 24h. Shared misc pool (WORKER_MISC_CONCURRENCY): retention, checkout expiry, event drain, rescreen, audit chain, monitor, settlement, reminders.",
         note="misc pool rationale")
    edit(con, shared, ["TOTAL (per replica)"], "B", "29", note="phase-1 total slots")
    edit(con, shared, ["TOTAL (per replica)"], "F",
         "Shipped Phase 1: 29 pool slots/replica (10/5/4/4/3/3, env-tunable). PgBouncer pool_size(75) >= total workers across 4 replicas (116).",
         note="phase-1 total note")
    # section-6 env rows, disambiguated by their Phase-2 column value
    for name, p2, p1 in [
        ("WORKER_INBOUND_CONCURRENCY", "8", "10"),
        ("WORKER_OUTBOX_CONCURRENCY", "4", "5"),
        ("WORKER_WEBHOOK_CONCURRENCY", "3", "4"),
    ]:
        r = env_row(name, p2)
        set_cell(r, shared, "B", p1, note="section6 %s P1" % name)
        if name == "WORKER_WEBHOOK_CONCURRENCY":
            set_cell(r, shared, "E", "Gateway rate limits (optimized)", note="formula rename")
    # cheat-sheet rows (Phase 1 column is 1 there)
    for name, staged in [("WORKER_INBOUND_CONCURRENCY", "10"), ("WORKER_OUTBOX_CONCURRENCY", "5")]:
        for r in rows_of(con):
            texts = [cell_text(c, shared) for c in row_cells(r)]
            if texts and texts[0] == name and len(texts) > 2 and texts[1] == "1":
                set_cell(r, shared, "C", staged, note="cheat %s staging" % name)
                break
    # insert the three class knobs the code shipped but the sheet omits
    # (guarded: a re-run must not duplicate them or re-shift the merges)
    already = any(row_cells(r) and cell_text(row_cells(r)[0], shared) == "WORKER_DATA_CONCURRENCY"
                  for r in rows_of(con))
    if already:
        changed.append("insert skipped (already applied)")
    else:
        anchor = env_row("WORKER_WEBHOOK_CONCURRENCY", "3")
        formulas = [c.get("r") for r in rows_of(con) for c in row_cells(r) if c.find(q("f")) is not None]
        if formulas:
            warnings.append("sheet7 formulas around insert: %s" % formulas[:8])
        insert_rows_after(con, shared, anchor, [
        {"A": "WORKER_DATA_CONCURRENCY", "B": "4", "C": "4", "D": "4", "E": "Data fulfilment provider calls"},
        {"A": "WORKER_RECONCILE_CONCURRENCY", "B": "3", "C": "3", "D": "3", "E": "Reconciliation + retention batch jobs"},
        {"A": "WORKER_MISC_CONCURRENCY", "B": "3", "C": "3", "D": "3", "E": "Retention, expiry, event drain, rescreen, audit chain, monitor, settlement, reminders"},
    ])
    edit(con, shared, ["Paystack API"], "A", "Interswitch API", exact_first="Paystack API", note="capacity rename")
    edit(con, shared, ["Interswitch API"], "D", "Interswitch sandbox rate limits (verify + requery)", note="capacity limits")
    # Phase-2 traffic math re-derived for 3 app replicas (was 2): column C.
    for needle, text in [
        ("App Replicas", "3"),
        ("App Servers", "3"),
        ("Connections per Replica", "1,667"),
        ("Avg Requests/sec per Replica", "~167"),
        ("Peak Requests/sec (all replicas)", "~500"),
        ("DB Reads/sec per Replica", "~133"),
        ("DB Writes/sec per Replica", "~33"),
    ]:
        edit(con, shared, [needle], "C", text, exact_first=needle, note="P2 col C: %s" % needle)
    save("Concurrency & Pool Tuning", con)

    # ============== Component Specs: Faya domain + Interswitch ==========
    comp = S("Component Specs (Reference)")
    edit(comp, shared, ["Must be registered and DNS-managed"], "C",
         "Subdomain (dev, Faya-branded)", note="dev domain")
    edit(comp, shared, ["Must be registered and DNS-managed"], "D",
         "Production domain (Faya-branded; replaces xego.com / pay.xego.com)", note="prod domain")
    edit(comp, shared, ["Paystack, WhatsApp, VTPass, SMTP"], "F",
         "Interswitch, WhatsApp, VTPass, SMTP", note="egress hosts")
    edit(comp, shared, ["API Keys (Paystack, WhatsApp)"], "B",
         "API Keys (Interswitch, WhatsApp)", note="secrets row")
    save("Component Specs (Reference)", comp)

    # ---------------- report ----------------
    leftover = sorted({s for s in shared if re.search(r"paystack|xego", s, re.I)})
    print("changed cells/strings: %d" % len(changed))
    for c in changed:
        print("  -", c)
    for w in warnings:
        print("  !", w)
    print("shared strings still mentioning xego/paystack: %d" % len(leftover))
    for s in leftover:
        print("  *", s[:110])

    # ---------------- write back ----------------
    with zipfile.ZipFile(XLSX, "w", zipfile.ZIP_DEFLATED) as out:
        for name, data in items.items():
            out.writestr(name, data)
    # validate: every part must parse
    check = zipfile.ZipFile(XLSX)
    assert check.testzip() is None
    for name in check.namelist():
        if name.endswith(".xml") or name.endswith(".rels"):
            ET.fromstring(check.read(name))
    check.close()
    print("OK: %s rewritten and re-validated" % XLSX)


if __name__ == "__main__":
    main()
