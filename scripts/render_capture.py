"""Render a tmux capture-pane -p -e -N capture as SVG (no third-party modules)."""
import html
from pathlib import Path
import re
import sys

source = Path(sys.argv[1])
lines = source.read_text().expandtabs(8).splitlines()
escape = re.compile(r"\x1b\[([0-9;]*)m")
cell, row, margin = 8.5, 21, 24
width = max(len(escape.sub("", line)) for line in lines)
out = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{width*cell+margin*2}" height="{len(lines)*row+margin*2}">',
       '<rect width="100%" height="100%" fill="#0b162b"/>',
       '<g font-family="JetBrainsMono Nerd Font, monospace" font-size="14">']
fg, bg, weight = "#b5d5ff", "#0b162b", "normal"
for y, line in enumerate(lines):
    x, pos = 0, 0
    for match in list(escape.finditer(line)) + [None]:
        end = match.start() if match else len(line)
        chunk = line[pos:end]
        if chunk:
            px, py = margin+x*cell, margin+y*row
            out.append(f'<rect x="{px}" y="{py}" width="{len(chunk)*cell}" height="{row}" fill="{bg}"/>')
            out.append(f'<text x="{px}" y="{py+15}" fill="{fg}" font-weight="{weight}" xml:space="preserve">{html.escape(chunk)}</text>')
            x += len(chunk)
        if not match:
            break
        codes = [int(n or 0) for n in match.group(1).split(";")]
        i = 0
        while i < len(codes):
            code = codes[i]
            if code == 0:
                fg, bg, weight = "#b5d5ff", "#0b162b", "normal"
            elif code == 1:
                weight = "bold"
            elif code == 22:
                weight = "normal"
            elif code == 39:
                fg = "#b5d5ff"
            elif code == 49:
                bg = "#0b162b"
            elif code in (38, 48) and codes[i+1:i+2] == [2] and i+4 < len(codes):
                color = "#%02x%02x%02x" % tuple(codes[i+2:i+5])
                if code == 38:
                    fg = color
                else:
                    bg = color
                i += 4
            i += 1
        pos = match.end()
out.extend(["</g>", "</svg>"])
source.with_suffix(".svg").write_text("\n".join(out))
