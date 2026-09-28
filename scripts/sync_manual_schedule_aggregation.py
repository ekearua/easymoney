"""Sync MANUAL_TEST_SCHEDULE.xlsx with the aggregated-collection manual cases.

Repoints the affected cells at new shared strings (matching the wording in
MANUAL_TEST_SCHEDULE.md) without disturbing the rest of the workbook:

  P2  TC-02-002  steps + expected   (aggregated Tier-1 details, code gate)
  P2  TC-02-004  steps + expected   (Telegram verifies the emailed code)
  P8  TC-08-001  steps + expected   (Become individual: one message per block)
  P8  TC-08-002  steps + expected   (Create thrift: concat accepted)
  P10 TC-10-001  steps + expected   (Register merchant: one-message KYB form)

Usage: python scripts/sync_manual_schedule_aggregation.py
"""

import shutil
import zipfile
import xml.etree.ElementTree as ET

SRC = "MANUAL_TEST_SCHEDULE.xlsx"
NS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
RNS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
ET.register_namespace("", NS)
M = "{%s}" % NS


TC02002_STEPS = (
    "1. Reply with a name (e.g. 'Ada Obi').\n"
    "2. Observe email prompt.\n"
    "3. Reply 'not-an-email' first, then a valid email.\n"
    "4. Observe the 6-digit confirmation code (delivered by email; with "
    "EMAIL_DEMO_CODE_IN_CHAT=true for demos, in the chat itself).\n"
    "5. Reply '000000' (a wrong code), then the correct 6-digit code.\n"
    "6. Observe the aggregated Tier-1 details prompt (one message for DOB, "
    "gender, address).\n"
    "7. Reply with a partial message ('1992-05-24' only) and note the re-ask "
    "echoes the captured DOB and names what is missing.\n"
    "8. Reply with all three in one message: 'DOB: 1992-05-24', "
    "'Gender: Female', and the residential address."
)
TC02002_EXPECTED = (
    "Acknowledges the name, asks for email. Invalid email is rejected with a "
    "retry prompt; a valid email triggers a 6-digit confirmation code. A wrong "
    "code gets the re-ask offering RESEND / CHANGE EMAIL; the correct code "
    "advances to the aggregated Tier-1 details step and sets email_verified_at. "
    "Five wrong entries lock code entry (start over or use CHANGE EMAIL); "
    "RESEND issues a fresh code after the cooldown. The details step accepts "
    "one message with DOB (YYYY-MM-DD or DD-MM-YYYY), gender (MALE/FEMALE), "
    "and address; a partial or invalid message re-asks naming exactly what is "
    "missing without discarding captured fields; the complete message advances "
    "to the number-confirmation step."
)
TC02004_STEPS = (
    "1. Open the Telegram bot and send /start.\n"
    "2. Complete name, email, code, and confirmation prompts (chat onboarding "
    "verifies the emailed 6-digit code in chat - same gate as the browser "
    "flow; RESEND / CHANGE EMAIL supported)."
)
TC02004_EXPECTED = (
    "Same onboarding flow runs; the code step requires the emailed code "
    "(RESEND / CHANGE EMAIL supported); completion lands on the main menu; "
    "Telegram identity is confirmed natively."
)
TC08001_STEPS = (
    "1. Choose 'Become individual' / the CBN Tier 2 upgrade flow.\n"
    "2. Verify the email with the 6-digit code (in chat: RESEND / CHANGE "
    "EMAIL supported).\n"
    "3. Reply with the profile details block in one message - name, DOB "
    "(YYYY-MM-DD), gender (MALE/FEMALE), address, occupation; a garbled or "
    "partial message is re-asked naming exactly what is missing while echoing "
    "what was captured.\n"
    "4. Reply with the photo ID block in one message - type (PASSPORT, "
    "DRIVERS LICENCE, NATIONAL ID, VOTERS CARD) plus the ID number.\n"
    "5. Reply with the 11-digit BVN and NIN in one message; a short or "
    "missing number is re-asked without discarding the captured one.\n"
    "6. Reply with the Tier-3 block in one message - address proof plus "
    "source of funds - then complete the liveness turn.\n"
    "7. Observe the KYC result."
)
TC08001_EXPECTED = (
    "KYC advances to L2 (approved individual, screening clear); the customer "
    "can now create/join thrift groups and send individual payments. Each "
    "aggregated step accepts one message for the whole block (labeled lines "
    "like 'Name: ...' or a line per fact), re-asks name the missing fields "
    "without discarding captured ones, and legacy single-field messages still "
    "route to the same handlers. Admin can review the case in /admin/kyc."
)
TC08002_STEPS = (
    "1. Choose 'Create thrift'.\n"
    "2. Enter group name, fixed contribution amount, weekly/monthly "
    "frequency, and 2-12 target members (all fields in one message is "
    "accepted: 'My Circle, 5000, weekly, 5')."
)
TC08002_EXPECTED = (
    "Group is created with an invite code (XG-THRIFT-...). Creator can share "
    "the invite code. A single-field message still works step by step, and a "
    "mixed message that parses fully skips straight to review."
)
TC10001_STEPS = (
    "1. Choose 'Register merchant' from the menu.\n"
    "2. Enter the 6-digit email OTP (RESEND / CHANGE EMAIL supported in "
    "chat).\n"
    "3. Reply with all three details in one message - business name, category "
    "(Food, Retail, Services, Health, Education, Logistics, ...), and a short "
    "description (e.g. \"Name: Ade's Kitchen / Category: Food / Description: "
    "Home-cooked Nigerian meals delivered across Lagos\").\n"
    "4. Try a garbled message first ('A' alone): the re-ask must name the "
    "business-name rule without discarding the session. Then a partial "
    "message with a too-short description: the re-ask must echo the captured "
    "business name and demand only the missing fields."
)
TC10001_EXPECTED = (
    "The aggregated details message creates the request with status "
    "awaiting_approval; the confirmation shows the business name, category "
    "and reference; the customer lands back on the menu. Appears in "
    "/admin/merchants. User becomes the merchant owner upon admin approval. "
    "A wrong code at the OTP step gets the friendly re-ask; five wrong "
    "entries lock code entry."
)

# sheet-name prefix -> {cell ref: new text}
UPDATES = {
    "P2_Customer Onboarding": {
        "G6": TC02002_STEPS, "H6": TC02002_EXPECTED,
        "G8": TC02004_STEPS, "H8": TC02004_EXPECTED,
    },
    "P8_L1+": {
        "G5": TC08001_STEPS, "H5": TC08001_EXPECTED,
        "G6": TC08002_STEPS, "H6": TC08002_EXPECTED,
    },
    "P10_Merchant Onboarding": {
        "G5": TC10001_STEPS, "H5": TC10001_EXPECTED,
    },
}


def main():
    backup = SRC + ".bak"
    shutil.copyfile(SRC, backup)

    zin = zipfile.ZipFile(backup)
    entries = {n: zin.read(n) for n in zin.namelist()}

    # Map sheet name -> zip path.
    rels = ET.fromstring(entries["xl/_rels/workbook.xml.rels"])
    rid2target = {r.get("Id"): r.get("Target") for r in rels}
    wbx = ET.fromstring(entries["xl/workbook.xml"])
    sheetmap = {}
    for s in wbx.iter(M + "sheet"):
        rid = s.get("{" + RNS + "}id")
        target = rid2target[rid]
        sheetmap[s.get("name")] = "xl/" + target if not target.startswith("/") else target[1:]

    # Shared strings: parse, remember as element tree for appending.
    sst_root = ET.fromstring(entries["xl/sharedStrings.xml"])

    def append_string(text):
        si = ET.SubElement(sst_root, M + "si")
        t = ET.SubElement(si, M + "t")
        t.set("{http://www.w3.org/XML/1998/namespace}space", "preserve")
        t.text = text
        return len(sst_root) - 1  # index of the new si

    changed = []
    for prefix, cells in UPDATES.items():
        full = next(n for n in sheetmap if n.startswith(prefix))
        path = sheetmap[full]
        ws = ET.fromstring(entries[path])
        seen = set()
        for row in ws.iter(M + "row"):
            for c in row.iter(M + "c"):
                ref = c.get("r")
                if ref not in cells:
                    continue
                if c.get("t") == "s":
                    # Shared string: repoint at a fresh si.
                    idx = append_string(cells[ref])
                    v = c.find(M + "v")
                    if v is None:
                        v = ET.SubElement(c, M + "v")
                    v.text = str(idx)
                    seen.add(ref)
                elif c.get("t") == "inlineStr":
                    # Inline string: replace the whole cell body.
                    for child in list(c):
                        c.remove(child)
                    is_el = ET.SubElement(c, M + "is")
                    t = ET.SubElement(is_el, M + "t")
                    t.set("{http://www.w3.org/XML/1998/namespace}space", "preserve")
                    t.text = cells[ref]
                    seen.add(ref)
        missing = set(cells) - seen
        if missing:
            raise SystemExit(f"sheet {full!r}: cells not found as strings: {sorted(missing)}")
        entries[path] = ET.tostring(ws, xml_declaration=True, encoding="UTF-8")
        changed.append(full)

    sst_root.set("uniqueCount", str(len(sst_root)))
    entries["xl/sharedStrings.xml"] = ET.tostring(
        sst_root, xml_declaration=True, encoding="UTF-8"
    )

    with zipfile.ZipFile(SRC, "w", zipfile.ZIP_DEFLATED) as zout:
        for name in zin.namelist():  # preserve original entry order
            zout.writestr(name, entries[name])
    zin.close()

    print("updated sheets:", ", ".join(changed))
    print("shared strings now:", len(sst_root))


if __name__ == "__main__":
    main()
