"""Convert the latest QR pairing code from a bridge log into an SVG file.

Usage: python payload2svg.py <bridge-log-path> <output-svg-path>

Reads the most recent "Emitting QR code <payload>" line that whatsmeow's
QRChannel debug logger writes, strips ANSI color codes, and renders the
payload as a compact SVG QR code. Requires the `qrcode` package
(pip install qrcode).
"""
import sys, io, re
import qrcode

log_path, out_path = sys.argv[1], sys.argv[2]
with io.open(log_path, encoding="utf-8", errors="replace") as f:
    text = f.read()

matches = re.findall(r"Emitting QR code (\S+)", text)
if not matches:
    print("NO_CODE")
    sys.exit(1)

code = re.sub(r"\x1b\[[0-9;]*m", "", matches[-1]).strip()

qr = qrcode.QRCode(error_correction=qrcode.constants.ERROR_CORRECT_L, border=4)
qr.add_data(code)
qr.make(fit=True)
matrix = qr.get_matrix()
n = len(matrix)
path = []
for y, row in enumerate(matrix):
    x = 0
    while x < n:
        if row[x]:
            run = x
            while x < n and row[x]:
                x += 1
            path.append(f"M{run} {y}h{x-run}v1h-{x-run}z")
        else:
            x += 1
svg = (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {n} {n}" width="440" height="440" '
       f'shape-rendering="crispEdges"><rect width="{n}" height="{n}" fill="#ffffff"/>'
       f'<path d="{"".join(path)}" fill="#000000"/></svg>')
with io.open(out_path, "w", encoding="utf-8") as f:
    f.write(svg)
print(f"OK codes={len(matches)}")
