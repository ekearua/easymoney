#!/usr/bin/env python3
"""Render every *.md in this folder to .docx with the branded reference doc,
then verify each output (valid zip, footer part, cover page break)."""
import glob
import os
import subprocess
import sys
import zipfile

sys.modules["lxml"] = None

HERE = os.path.dirname(os.path.abspath(__file__))
PANDOC = r"C:\Users\User\anaconda3\Scripts\pandoc.exe"
REF = os.path.join(HERE, "custom-reference.docx")


def render(md_path):
    docx = md_path[:-3] + ".docx"
    r = subprocess.run(
        [PANDOC, md_path, "--reference-doc=" + REF, "-o", docx],
        capture_output=True,
    )
    return docx, r


def verify(docx):
    problems = []
    try:
        with zipfile.ZipFile(docx) as z:
            bad = z.testzip()
            if bad:
                problems.append("corrupt entry " + bad)
            names = z.namelist()
            if not any(n.endswith("footer1.xml") for n in names):
                problems.append("no footer")
            if "word/footer1.xml" in names:
                f = z.read("word/footer1.xml").decode("utf-8")
                if "PAGE" not in f or "NUMPAGES" not in f:
                    problems.append("footer fields missing")
            if "word/document.xml" in names:
                d = z.read("word/document.xml").decode("utf-8")
                if '<w:br w:type="page"/>' not in d:
                    problems.append("no cover page break")
                if 'w:val="Title"' not in d:
                    problems.append("no title style")
    except Exception as e:  # noqa: BLE001
        problems.append(str(e))
    return problems


def main():
    mds = sorted(glob.glob(os.path.join(HERE, "*.md")))
    print("rendering", len(mds), "files")
    for md in mds:
        docx, r = render(md)
        if r.returncode != 0:
            print("FAIL", os.path.basename(md), r.stderr.decode(errors="replace")[:300])
            continue
        probs = verify(docx)
        status = "OK " if not probs else "PROB"
        print(status, os.path.basename(docx), ("; ".join(probs)) if probs else "")
    print("done")


if __name__ == "__main__":
    main()
