"""Stamps a version (like 9.1.0) into the Windows resources before a release build.

Usage: python3 tools/set_version.py 9.1.0
"""
import pathlib, re, sys

version = sys.argv[1].lstrip("v")
if not re.fullmatch(r"\d+(\.\d+){0,3}", version):
    sys.exit(f"not a version number: {version!r}")
parts = (version.split(".") + ["0"] * 4)[:4]
res = pathlib.Path(__file__).resolve().parent.parent / "res"

rc = (res / "app.rc").read_text()
rc = re.sub(r"(FILEVERSION|PRODUCTVERSION) [\d,]+", lambda m: f"{m.group(1)} {','.join(parts)}", rc)
rc = re.sub(r'("(?:FileVersion|ProductVersion)", )"[^"]*"', lambda m: f'{m.group(1)}"{version}"', rc)
(res / "app.rc").write_text(rc)

man = (res / "app.manifest").read_text()
man = re.sub(r'(name="SkylandersPortalMaster" version=)"[\d.]+"', lambda m: f'{m.group(1)}"{".".join(parts)}"', man)
(res / "app.manifest").write_text(man)
print("version", version)
