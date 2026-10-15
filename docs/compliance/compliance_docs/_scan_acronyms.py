#!/usr/bin/env python3
"""Scan compliance templates for uppercase acronyms used in each document."""
import glob
import os
import re
from collections import Counter

HERE = r"C:\Users\User\Mobile\Whatsapp Payment\docs\compliance\compliance_docs"
PAT = re.compile(r"\b[A-Z][A-Z0-9]{1,}(?:[&\-/][A-Z0-9]+)*\b")

files = sorted(glob.glob(os.path.join(HERE, "*.md")))
all_ac = Counter()
per_file = {}
for f in files:
    txt = open(f, encoding="utf-8").read()
    if "```{=openxml}" in txt:
        body = txt.split("```{=openxml}", 1)[1]
    else:
        body = txt
    acs = set(m.group(0) for m in PAT.finditer(body))
    per_file[os.path.basename(f)] = sorted(acs)
    for a in acs:
        all_ac[a] += 1

for f, acs in per_file.items():
    print(f, len(acs))

print("---- most common ----")
for a, n in all_ac.most_common(150):
    print(a, n)
