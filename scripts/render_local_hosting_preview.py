#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Render docs/architecture/local_hosting_preview.html from the GRID in add_local_hosting_sheet.py."""
import ast
import html
import io
import os

SRC = os.path.join(os.path.dirname(os.path.abspath(__file__)), "add_local_hosting_sheet.py")
OUT = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                   "docs", "architecture", "local_hosting_preview.html")

src = io.open(SRC, encoding="utf-8").read()
tree = ast.parse(src)
COLS = NCOLS = ROWS = None
for node in tree.body:
    if isinstance(node, ast.Assign):
        names = [getattr(t, "id", "") for t in node.targets]
        if "COLS" in names:
            COLS = ast.literal_eval(node.value)
        elif "ROWS" in names:
            ROWS = ast.literal_eval(node.value)
NCOLS = len(COLS)

CSS_CLASS = set(["title", "sub", "sec", "th", "td", "num", "tot", "note",
                 "pass", "warn", "na", "term", "numtot"])


def render():
    o = io.StringIO()
    o.write('<!doctype html><html><head><meta charset="utf-8"><title>Local Hosting Options</title><style>\n')
    o.write('body{font-family:Segoe UI,Arial,sans-serif;font-size:12px;margin:12px;background:#fff;color:#111}\n')
    o.write('table{border-collapse:collapse;table-layout:fixed}\n')
    o.write('td{border:1px solid #b8c4d0;padding:4px 6px;vertical-align:top;word-wrap:break-word;white-space:normal}\n')
    o.write('td.num,td.numtot{text-align:right}\n')
    o.write('tr.title td{background:#2E75B6;color:#fff;font-weight:bold;font-size:13px}\n')
    o.write('tr.sub td{background:#FFF2CC}\n')
    o.write('tr.sec td{background:#D9E2F3;color:#1F4E79;font-weight:bold;font-size:12px}\n')
    o.write('td.th,tr.th td{background:#2E75B6;color:#fff;font-weight:bold;text-align:center}\n')
    o.write('td.tot,td.numtot{background:#D9E2F3;color:#002060;font-weight:bold}\n')
    o.write('td.pass{background:#C6EFCE}\n')
    o.write('td.warn{background:#FFEB9C}\n')
    o.write('td.na{background:#FFC7CE}\n')
    o.write('td.term,tr.term td{color:#002060;font-weight:bold}\n')
    o.write('tr.note td{border:none;padding:2px 4px}\n')
    o.write('tr.spacer td{border:none;height:10px}\n')
    o.write('</style></head><body><table><colgroup>')
    for _, w in COLS:
        o.write('<col style="width:%dch">' % w)
    o.write('</colgroup><tbody>')

    for style, cells in ROWS:
        if style == "spacer" or not cells:
            o.write('<tr class="spacer"><td colspan="%d"></td></tr>' % NCOLS)
            continue
        if style in ("title", "sub", "sec", "note"):
            o.write('<tr class="%s"><td colspan="%d">%s</td></tr>' % (style, NCOLS, html.escape(str(cells[0]))))
            continue
        o.write('<tr class="%s">' % style)
        cidx = 0
        for cell in cells:
            if cidx >= NCOLS:
                break
            if isinstance(cell, tuple):
                if len(cell) == 3:
                    text, cstyle, span = cell
                else:
                    text, cstyle = cell
                    span = 1
            else:
                text, cstyle, span = cell, style, 1
            o.write('<td class="%s"%s>%s</td>' % (
                cstyle, (' colspan="%d"' % span) if span > 1 else "", html.escape(str(text))))
            cidx += span
        while cidx < NCOLS:
            o.write('<td class="%s"></td>' % style)
            cidx += 1
        o.write('</tr>')
    o.write('</tbody></table></body></html>\n')
    return o.getvalue()


io.open(OUT, "w", encoding="utf-8", newline="").write(render())
print("preview written:", OUT, "rows:", len(ROWS))
