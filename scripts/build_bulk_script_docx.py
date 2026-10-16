# -*- coding: utf-8 -*-
"""Builds docs/user-scripts/BULK_PAYMENTS_USER_SCRIPT.docx: the merchant-and-recipient
walk-through of the location-aware bulk payments feature, written in the same
"conversation script" style as PAYMENT_USER_SCRIPT.docx.

House document style is cloned verbatim from PAYMENT_USER_SCRIPT.docx (same
raw-OOXML approach as build_journey_docx.py: python-docx segfaults on this box).
That document defines only Title, Heading1 and BodyText, so the markdown maps
"# " to Title, "## " to Heading1, "> " to indented italic BodyText, and
everything else to BodyText.

    python scripts/build_bulk_script_docx.py
"""
import os, re, zipfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC_DOCX = os.path.join(ROOT, "docs", "user-scripts", "PAYMENT_USER_SCRIPT.docx")
MD = os.path.join(ROOT, "docs", "user-scripts", "BULK_PAYMENTS_USER_SCRIPT.md")
OUT_DOCX = os.path.join(ROOT, "docs", "user-scripts", "BULK_PAYMENTS_USER_SCRIPT.docx")
TITLE = "Faya bulk payments - the location-aware disbursement script"

# ---------------------------------------------------------------- raw OOXML --
def esc(t):
    return t.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")

def rpr(b, c=False, i=False):
    p = ""
    if b: p += "<w:b/>"
    if i: p += "<w:i/>"
    if c: p += '<w:rFonts w:ascii="Consolas" w:hAnsi="Consolas"/>'
    return "<w:rPr>%s</w:rPr>" % p if p else ""

def run(t, b=False, i=False, c=False):
    return '<w:r>%s<w:t xml:space="preserve">%s</w:t></w:r>' % (rpr(b, c, i), esc(t))

def para(style, segs, ind=None, before=0, after=160, keep=False):
    p = "<w:pPr>"
    if style: p += '<w:pStyle w:val="%s"/>' % style
    if ind is not None: p += '<w:ind w:left="%d"/>' % ind
    p += '<w:spacing w:before="%d" w:after="%d"/>' % (before, after)
    if keep: p += "<w:keepNext/>"
    p += "</w:pPr>"
    return "<w:p>%s%s</w:p>" % (p, "".join(run(t, b, i, c) for (t, b, i, c) in segs))

def cell_para(segs, hdr):
    body = "".join(run(t, b or hdr, i, c) for (t, b, i, c) in segs)
    return '<w:p><w:pPr><w:spacing w:before="20" w:after="20"/></w:pPr>%s</w:p>' % body

def table(rows):
    n = max(len(r) for r in rows)
    widths = {2: (3260, 6100), 3: (2200, 3800, 3360),
              4: (1900, 2900, 2400, 2160)}.get(n, [int(9360 / n)] * n)
    out = ['<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/>'
           '<w:tblW w:w="9360" w:type="dxa"/></w:tblPr>',
           "<w:tblGrid>" + "".join('<w:gridCol w:w="%d"/>' % wd for wd in widths) + "</w:tblGrid>"]
    for ri, row in enumerate(rows):
        trpr = "<w:trPr><w:tblHeader/></w:trPr>" if ri == 0 else ""
        out.append("<w:tr>" + trpr + "".join(
            '<w:tc><w:tcPr><w:tcW w:w="%d" w:type="dxa"/>%s</w:tcPr>%s</w:tc>' %
            (widths[ci], '<w:shd w:val="clear" w:fill="EFEFEF"/>' if ri == 0 else "",
             cell_para(parse_inline(c), ri == 0))
            for ci, c in enumerate(row)) + "</w:tr>")
    out.append("</w:tbl>")
    return "".join(out)

# ------------------------------------------------------------ inline/block --
def parse_inline(s):
    s = re.sub(r"\[([^\]]+)\]\(([^)]+)\)", r"\1", s)
    segs = []
    for part in re.split(r"(\*\*.+?\*\*)", s):
        if part.startswith("**") and part.endswith("**") and len(part) > 4:
            for sub in re.split(r"(`[^`]+`)", part[2:-2]):
                if len(sub) > 2 and sub.startswith("`"): segs.append((sub[1:-1], True, False, True))
                elif sub: segs.append((sub, True, False, False))
        else:
            for sub in re.split(r"(`[^`]+`)", part):
                if len(sub) > 2 and sub.startswith("`"): segs.append((sub[1:-1], False, False, True))
                elif sub: segs.append((sub, False, False, False))
    return segs

def dialogue(s):
    segs = [(t, b, True, c) for (t, b, _i, c) in parse_inline(s)]
    return para("BodyText", segs, ind=567, before=20, after=60)

def md_to_body(md):
    body, lines, i = [], md.split("\n"), 0
    while i < len(lines):
        ln = lines[i]
        if ln.startswith("## "):
            body.append(para("Heading1", parse_inline(ln[3:]), before=280, after=120, keep=True))
        elif ln.startswith("# "):
            body.append(para("Title", parse_inline(ln[2:]), before=0, after=160))
        elif ln.startswith("> "):
            body.append(dialogue(ln[2:].strip()))
        elif ln.startswith("|"):
            rows = []
            while i < len(lines) and lines[i].startswith("|"):
                cells = [c.strip() for c in lines[i].strip("|").split("|")]
                if not set("".join(cells)) <= set("-: "):
                    rows.append(cells)
                i += 1
            body.append(table(rows)); continue
        elif ln.startswith("- "):
            body.append(para("BodyText", parse_inline("• " + ln[2:]), ind=360, after=80))
        elif ln.startswith("---"):
            pass
        elif ln.strip():
            body.append(para("BodyText", parse_inline(ln)))
        i += 1
    return "".join(body)

def build_docx(body_xml):
    src = zipfile.ZipFile(SRC_DOCX)
    xmlns = ('xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" '
             'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"')
    document = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
                '<w:document %s><w:body>%s%s</w:body></w:document>'
                % (xmlns, body_xml,
                   re.search(r"<w:sectPr.*?</w:sectPr>",
                             src.read("word/document.xml").decode("utf8"), re.S).group(0)))
    core = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" '
            'xmlns:dc="http://purl.org/dc/elements/1.1/">'
            '<dc:title>%s</dc:title><dc:creator>Faya</dc:creator></cp:coreProperties>' % esc(TITLE))
    with zipfile.ZipFile(OUT_DOCX, "w", zipfile.ZIP_DEFLATED) as z:
        for name in src.namelist():
            if name.endswith("/"):
                continue
            if name == "word/document.xml":
                z.writestr(name, document)
            elif name == "docProps/core.xml":
                z.writestr(name, core)
            else:
                z.writestr(name, src.read(name))
    src.close()

def main():
    with open(MD, encoding="utf-8") as f:
        md = f.read()
    build_docx(md_to_body(md))
    print("wrote", os.path.relpath(OUT_DOCX, ROOT))

if __name__ == "__main__":
    main()
