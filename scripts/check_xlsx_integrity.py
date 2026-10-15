#!/usr/bin/env python3
"""Fast integrity guard for the generated spreadsheets.

One workbook in this repo was silently zero-filled (every byte replaced with
NUL) by a crashed edit session, and the damage was only discovered when a
later task tried to open it. This check fails loudly instead: any docs/**/*.xlsx
or repo-root *.xlsx that is not a healthy OOXML zip is named with the reason
and the process exits 1.

Checks per file (cheap - these workbooks are a few hundred KB at most):
  - the file is a zip container (a NUL-filled or truncated file fails here)
  - every member passes a CRC check (testzip)
  - the OOXML skeleton is present: [Content_Types].xml, xl/workbook.xml,
    and the workbook declares at least one sheet
  - every XML/rels part parses

Excel lock files (~$*) and backups (*.bak) are skipped by design.

Run from the repo root:  python scripts/check_xlsx_integrity.py
Exit 0 = every workbook healthy; exit 1 = at least one corrupt (named).
"""
import glob
import os
import re
import sys
import zipfile
from xml.etree import ElementTree as ET


def workbooks():
    found = []
    seen = set()
    for pattern in ("docs/**/*.xlsx", "*.xlsx"):
        for path in sorted(glob.glob(pattern, recursive=True)):
            base = os.path.basename(path)
            if base.startswith("~$") or base.endswith(".bak"):
                continue
            if path in seen:
                continue
            seen.add(path)
            found.append(path)
    return found


def check(path):
    """Return None if healthy, else a short reason string."""
    try:
        z = zipfile.ZipFile(path)
    except zipfile.BadZipFile:
        return "not a zip container (zero-filled or truncated)"
    except OSError as e:
        return "unreadable: %s" % e
    try:
        bad = z.testzip()
        if bad is not None:
            return "CRC failure in %s" % bad
        names = z.namelist()
        for required in ("[Content_Types].xml", "xl/workbook.xml"):
            if required not in names:
                return "missing %s" % required
        wb = z.read("xl/workbook.xml").decode("utf-8", "replace")
        if not re.search(r"<sheet\b", wb):
            return "workbook declares no sheets"
        for name in names:
            if name.endswith(".xml") or name.endswith(".rels"):
                ET.fromstring(z.read(name))
    except ET.ParseError as e:
        return "XML parse error: %s" % e
    finally:
        z.close()
    return None


def main():
    paths = workbooks()
    if not paths:
        print("no workbooks found (run from the repo root)")
        return 1
    failed = []
    for path in paths:
        reason = check(path)
        size = os.path.getsize(path)
        if reason is None:
            print("OK   %-55s %8d bytes" % (path, size))
        else:
            failed.append((path, reason))
            print("FAIL %-55s %s" % (path, reason))
    if failed:
        print()
        print("!!! %d corrupt workbook(s) detected:" % len(failed))
        for path, reason in failed:
            print("    %s -> %s" % (path, reason))
        print("Restore from a backup or regenerate with its scripts/ builder.")
        return 1
    print("\nall %d workbooks healthy" % len(paths))
    return 0


if __name__ == "__main__":
    sys.exit(main())
