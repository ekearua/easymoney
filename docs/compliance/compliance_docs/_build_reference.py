#!/usr/bin/env python3
"""Build custom-reference.docx - a navy-branded pandoc reference document.

Modifies the default reference docx styles (title, headings, body) and adds a
footer with the Xego brand line and "Page X of Y" field codes. Output is used
by pandoc --reference-doc when rendering the compliance documents to Word.
"""
import re
import shutil
import zipfile

HERE = r"C:\Users\User\Mobile\Whatsapp Payment\docs\compliance\compliance_docs"
SRC = HERE + r"\_default_reference.docx"
OUT = HERE + r"\custom-reference.docx"

NAVY = "1F3864"
BLUE = "2E5E8C"
GRAY = "595959"

STYLE_EDITS = {
    "Title":       dict(color=NAVY, sz=44),
    "Subtitle":    dict(color=BLUE, sz=30, italic=True),
    "Author":      dict(color=GRAY, sz=22),
    "Date":        dict(color=GRAY, sz=22),
    "Heading1":    dict(color=NAVY, sz=32),
    "Heading2":    dict(color=BLUE, sz=28),
    "Heading3":    dict(color=BLUE, sz=26),
    "Heading4":    dict(color=BLUE, sz=24),
    "Heading5":    dict(color=BLUE, sz=24),
    "Heading6":    dict(color=BLUE, sz=24),
    "Heading7":    dict(color=BLUE, sz=24),
    "Heading8":    dict(color=BLUE, sz=24),
    "Heading9":    dict(color=BLUE, sz=24),
    "Hyperlink":   dict(color=BLUE),
}

TABLE_BORDERS = (
    '<w:tblBorders>'
    '<w:top w:val="single" w:sz="4" w:space="0" w:color="7F7F7F"/>'
    '<w:left w:val="single" w:sz="4" w:space="0" w:color="7F7F7F"/>'
    '<w:bottom w:val="single" w:sz="4" w:space="0" w:color="7F7F7F"/>'
    '<w:right w:val="single" w:sz="4" w:space="0" w:color="7F7F7F"/>'
    '<w:insideH w:val="single" w:sz="4" w:space="0" w:color="7F7F7F"/>'
    '<w:insideV w:val="single" w:sz="4" w:space="0" w:color="7F7F7F"/>'
    '</w:tblBorders>'
)

FOOTER = (
    '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n'
    '<w:ftr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">'
    '<w:p><w:pPr><w:jc w:val="center"/>'
    '<w:pBdr><w:top w:val="single" w:sz="6" w:space="4" w:color="1F3864"/></w:pBdr></w:pPr>'
    '<w:r><w:rPr><w:color w:val="595959"/><w:sz w:val="16"/></w:rPr>'
    '<w:t xml:space="preserve">Xego Compliance Program\xa0\xa0|\xa0\xa0Page\xa0</w:t></w:r>'
    '<w:r><w:rPr><w:color w:val="595959"/><w:sz w:val="16"/></w:rPr><w:fldChar w:fldCharType="begin"/></w:r>'
    '<w:r><w:instrText xml:space="preserve"> PAGE </w:instrText></w:r>'
    '<w:r><w:rPr><w:color w:val="595959"/><w:sz w:val="16"/></w:rPr><w:fldChar w:fldCharType="separate"/></w:r>'
    '<w:r><w:rPr><w:color w:val="595959"/><w:sz w:val="16"/></w:rPr><w:t>1</w:t></w:r>'
    '<w:r><w:rPr><w:color w:val="595959"/><w:sz w:val="16"/></w:rPr><w:fldChar w:fldCharType="end"/></w:r>'
    '<w:r><w:rPr><w:color w:val="595959"/><w:sz w:val="16"/></w:rPr><w:t xml:space="preserve">\xa0of\xa0</w:t></w:r>'
    '<w:r><w:rPr><w:color w:val="595959"/><w:sz w:val="16"/></w:rPr><w:fldChar w:fldCharType="begin"/></w:r>'
    '<w:r><w:instrText xml:space="preserve"> NUMPAGES </w:instrText></w:r>'
    '<w:r><w:rPr><w:color w:val="595959"/><w:sz w:val="16"/></w:rPr><w:fldChar w:fldCharType="separate"/></w:r>'
    '<w:r><w:rPr><w:color w:val="595959"/><w:sz w:val="16"/></w:rPr><w:t>1</w:t></w:r>'
    '<w:r><w:rPr><w:color w:val="595959"/><w:sz w:val="16"/></w:rPr><w:fldChar w:fldCharType="end"/></w:r>'
    '</w:p></w:ftr>'
)

CONTENT_TYPES_OVERRIDE = (
    '<Override PartName="/word/footer1.xml" '
    'ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.footer+xml"/>'
)

REL_FOOTER = (
    '<Relationship Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/footer" '
    'Id="rId9" Target="footer1.xml" />'
)


def edit_rpr(style_block, color=None, sz=None, italic=False):
    def repl(m):
        rpr = m.group(1)
        if color:
            rpr = re.sub(r'<w:color[^/]*/>', '<w:color w:val="%s" />' % color, rpr)
        if sz:
            rpr = re.sub(r'<w:sz w:val="\d+" />', '<w:sz w:val="%d" />' % sz, rpr)
            rpr = re.sub(r'<w:szCs w:val="\d+" />', '<w:szCs w:val="%d" />' % sz, rpr)
        if italic and '<w:i />' not in rpr and '<w:i/>' not in rpr and '<w:i ' not in rpr:
            rpr = rpr.replace('</w:rPr>', '      <w:i />\n    </w:rPr>', 1)
        return rpr
    return re.sub(r'(<w:rPr>.*?</w:rPr>)', repl, style_block, count=1, flags=re.S)


def transform_styles(xml):
    for style_id, edits in STYLE_EDITS.items():
        pat = re.compile(
            r'(<w:style\b[^>]*w:styleId="%s"[^>]*>.*?</w:style>)' % style_id,
            re.S,
        )
        def repl(m, _id=style_id, _e=edits):
            return edit_rpr(m.group(1), color=_e.get("color"), sz=_e.get("sz"),
                            italic=_e.get("italic", False))
        new_xml, n = pat.subn(repl, xml)
        if n == 0:
            print("WARN: style block not found:", style_id)
        else:
            print("styled:", style_id)
        xml = new_xml
    return xml


def transform_table_style(styles):
    pat = re.compile(r'(<w:style\b[^>]*w:styleId="Table"[^>]*>.*?</w:style>)', re.S)

    def repl(m):
        block = m.group(1)
        if "<w:tblBorders>" not in block:
            block = block.replace("</w:tblPr>", TABLE_BORDERS + "</w:tblPr>", 1)
        block = block.replace('<w:top w:type="dxa" w:w="0" />',
                              '<w:top w:type="dxa" w:w="40" />', 1)
        block = block.replace('<w:bottom w:type="dxa" w:w="0" />',
                              '<w:bottom w:type="dxa" w:w="40" />', 1)
        block = re.sub(
            r'(<w:tblStylePr w:type="firstRow">.*?<w:tcPr>)',
            r'\1<w:shd w:val="clear" w:color="auto" w:fill="DCE6F1" />',
            block, count=1, flags=re.S)
        return block

    new_xml, n = pat.subn(repl, styles)
    print("table style:", "styled" if n else "NOT FOUND")
    return new_xml


def main():
    zin = zipfile.ZipFile(SRC)
    entries = {name: zin.read(name) for name in zin.namelist()}
    zin.close()

    styles = entries["word/styles.xml"].decode("utf-8")
    styles = transform_styles(styles)
    styles = transform_table_style(styles)
    entries["word/styles.xml"] = styles.encode("utf-8")

    doc = entries["word/document.xml"].decode("utf-8")
    assert "<w:sectPr />" in doc, "sectPr not found"
    doc = doc.replace(
        "<w:sectPr />",
        '<w:sectPr><w:footerReference w:type="default" r:id="rId9" /></w:sectPr>',
        1,
    )
    entries["word/document.xml"] = doc.encode("utf-8")

    rels = entries["word/_rels/document.xml.rels"].decode("utf-8")
    assert 'rId9' not in rels
    rels = rels.replace("</Relationships>", REL_FOOTER + "</Relationships>", 1)
    entries["word/_rels/document.xml.rels"] = rels.encode("utf-8")

    ct = entries["[Content_Types].xml"].decode("utf-8")
    assert "footer1.xml" not in ct
    ct = ct.replace("</Types>", CONTENT_TYPES_OVERRIDE + "</Types>", 1)
    entries["[Content_Types].xml"] = ct.encode("utf-8")

    entries["word/footer1.xml"] = FOOTER.encode("utf-8")

    shutil.copy(SRC, OUT)
    with zipfile.ZipFile(OUT, "w", zipfile.ZIP_DEFLATED) as zout:
        for name, data in entries.items():
            zout.writestr(name, data)
    print("written:", OUT)

    with zipfile.ZipFile(OUT) as check:
        bad = check.testzip()
    print("zip valid:", "OK" if bad is None else bad)


if __name__ == "__main__":
    main()
