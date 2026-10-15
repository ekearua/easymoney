#!/usr/bin/env python3
"""Build a branded pandoc reference.docx (navy theme) used to style all Word
compliance documents, and dump the default styles.xml for inspection."""
import subprocess
import zipfile
import sys
import os

HERE = os.path.dirname(os.path.abspath(__file__))
PANDOC = r"C:\Users\User\anaconda3\Scripts\pandoc.exe"
DEFAULT = os.path.join(HERE, "_default_reference.docx")
DUMP = os.path.join(HERE, "_styles_dump.xml")

p = subprocess.run([PANDOC, "--print-default-data-file", "reference.docx"], capture_output=True)
assert p.returncode == 0, p.stderr.decode(errors="replace")
with open(DEFAULT, "wb") as f:
    f.write(p.stdout)
print("default reference bytes:", len(p.stdout))

with zipfile.ZipFile(DEFAULT) as z:
    xml = z.read("word/styles.xml").decode("utf-8")
with open(DUMP, "w", encoding="utf-8") as f:
    f.write(xml)
print("styles.xml written to", DUMP)
