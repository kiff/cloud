# Writes the QR code /tafs/download shows on a computer: it opens the page
# on a phone. Run when the URL changes, and commit the SVG it writes:
#   python3 -m pip install segno==1.6.6
#   python3 scripts/download_qr.py internal/assets/static/img/download/qr-download.svg  (from the web module's directory)
import sys

import segno

URL = "https://kiff.dev/tafs/download"
qr = segno.make(URL, error="m", micro=False)
qr.save(sys.argv[1], kind="svg", scale=6, border=2, dark="#11141b", light="#ffffff", xmldecl=False, svgns=True, title="QR code: kiff.dev/tafs/download")
