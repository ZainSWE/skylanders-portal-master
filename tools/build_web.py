"""Builds the web version: one self-contained HTML file.

Usage: python3 tools/build_web.py core.wasm wasm_exec.js out.html
"""
import base64, gzip, pathlib, sys

root = pathlib.Path(__file__).resolve().parent.parent
wasm, wasm_exec, out = sys.argv[1], sys.argv[2], sys.argv[3]
html = (root / "web/index.html").read_text()
b64 = lambda p: base64.b64encode(pathlib.Path(p).read_bytes()).decode()

def swap(old, new):
    global html
    assert old in html, old
    html = html.replace(old, new, 1)

swap('url("inter.woff2")', 'url("data:font/woff2;base64,%s")' % b64(root / "web/inter.woff2"))
swap('<link rel="icon" href="icon.png">', '<link rel="icon" href="data:image/png;base64,%s">' % b64(root / "web/icon.png"))
swap('<script src="qrcode.js" defer></script>\n', '')
core = base64.b64encode(gzip.compress(pathlib.Path(wasm).read_bytes(), 9)).decode()
backend = (root / "webapp/webusb.js").read_text()
loader = pathlib.Path(wasm_exec).read_text()
swap('<script>\n// The page talks to its backend', '<script>window.PORTAL_CORE_GZ = "%s";</script>\n<script>\n%s\n</script>\n<script>\n%s\n</script>\n<script>\n// The page talks to its backend' % (core, loader, backend))
pathlib.Path(out).write_text(html)
print(out, len(html) // 1024, "KB")
