#!/usr/bin/env python3
"""Generate deterministic, offline avatar photos for seed/staff.json.

Each image is seeded from the badge number, so reruns produce identical files.
Existing <badge>.jpg files are skipped (resumable); use --force to redo them.
See README.md for the source, licence and style choices.
"""
import argparse
import html
import json
import statistics
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
STAFF = HERE.parent / "staff.json"
MANIFEST = HERE / "manifest.json"
SHEET = HERE / "contact_sheet.html"
EXCLUDE = {"100000"}  # Jason Figge supplies his own photo.
BATCH = 50
DICEBEAR_VERSION = "9.4.2"
LICENCE = "Avataaars by Pablo Stanley, free for personal and commercial use (https://avataaars.com/); DiceBear code MIT"
STYLE_FOR = {"male": "avataaars", "female": "avataaars", "unknown": "avataaars-neutral"}


def load_staff():
    with STAFF.open() as f:  # read-only
        staff = json.load(f)
    people = [p for p in staff if str(p["badge"]) not in EXCLUDE]
    for p in people:
        if p.get("gender") not in STYLE_FOR:
            sys.exit(f"badge {p['badge']}: unexpected gender {p.get('gender')!r}")
    return people


def ensure_deps():
    if not (HERE / "node_modules" / "@dicebear" / "core").exists():
        subprocess.run(["npm", "ci", "--no-audit", "--no-fund"], cwd=HERE, check=True)


def render(people):
    """Render in batches; stop on the first failure."""
    for i in range(0, len(people), BATCH):
        jobs = [{"badge": str(p["badge"]), "gender": p["gender"], "out": str(HERE / f"{p['badge']}.jpg")}
                for p in people[i:i + BATCH]]
        proc = subprocess.run(["node", "render.mjs"], cwd=HERE, input=json.dumps(jobs),
                              text=True, capture_output=True)
        sys.stdout.write(proc.stdout)
        if proc.returncode != 0:
            sys.stderr.write(proc.stderr)
            sys.exit(f"render failed in batch starting at badge {jobs[0]['badge']}; stopping")


def write_manifest(people):
    manifest = {}
    for p in people:
        badge = str(p["badge"])
        if (HERE / f"{badge}.jpg").exists():
            manifest[badge] = {
                "file": f"{badge}.jpg",
                "source": f"DiceBear {DICEBEAR_VERSION} {STYLE_FOR[p['gender']]} (offline), seed={badge}",
                "licence": LICENCE,
            }
    MANIFEST.write_text(json.dumps(manifest, indent=2) + "\n")
    return manifest


def write_sheet(people, manifest):
    shown = [p for p in people if str(p["badge"]) in manifest]
    sizes = [(HERE / manifest[str(p["badge"])]["file"]).stat().st_size for p in shown]
    counts = {g: sum(p["gender"] == g for p in shown) for g in STYLE_FOR}
    summary = (f"{len(shown)} of {len(people)} photos &middot; "
               + " &middot; ".join(f"{g} {n}" for g, n in counts.items()))
    if sizes:
        summary += (f" &middot; size min {min(sizes) / 1024:.1f} KB, median "
                    f"{statistics.median(sizes) / 1024:.1f} KB, max {max(sizes) / 1024:.1f} KB")
    cards = "\n".join(
        f'<figure class="{p["gender"]}"><img src="{manifest[str(p["badge"])]["file"]}" width="128" height="128" alt="">'
        f'<figcaption><b>{html.escape(p["name"])}</b><span>{p["badge"]} &middot; '
        f'<i>{p["gender"]}</i></span></figcaption></figure>'
        for p in shown)
    SHEET.write_text(f"""<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Staff Photo Contact Sheet</title>
<style>
:root {{ --bg:#f6f6f4; --card:#fff; --ink:#1d1d1f; --muted:#6b6b70; --m:#2f6fd6; --f:#c2417a; --u:#7a7a80; }}
@media (prefers-color-scheme: dark) {{ :root {{ --bg:#161618; --card:#222226; --ink:#eee; --muted:#a0a0a8; }} }}
body {{ margin:0; padding:24px 16px; background:var(--bg); color:var(--ink); font:14px/1.4 system-ui, sans-serif; }}
h1 {{ font-size:20px; margin:0 0 4px; }} p {{ color:var(--muted); margin:0 0 20px; }}
main {{ display:grid; grid-template-columns:repeat(auto-fill, minmax(150px, 1fr)); gap:12px; }}
figure {{ margin:0; background:var(--card); border-radius:10px; padding:10px; text-align:center; border-top:4px solid var(--u); }}
figure.male {{ border-top-color:var(--m); }} figure.female {{ border-top-color:var(--f); }}
img {{ border-radius:8px; display:block; margin:0 auto 8px; }}
figcaption b {{ display:block; }} figcaption span {{ color:var(--muted); font-size:12px; }}
.male i {{ color:var(--m); }} .female i {{ color:var(--f); }} i {{ font-style:normal; font-weight:600; }}
</style></head><body>
<h1>Staff photo contact sheet</h1>
<p>{summary}</p>
<main>
{cards}
</main>
</body></html>
""")


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--limit", type=int, help="only the first N eligible people, in staff.json order")
    ap.add_argument("--force", action="store_true", help="re-render files that already exist")
    ap.add_argument("--sheet-only", action="store_true", help="only rewrite manifest.json and contact_sheet.html")
    args = ap.parse_args()

    people = load_staff()
    todo = people[:args.limit] if args.limit else people
    if not args.sheet_only:
        pending = [p for p in todo if args.force or not (HERE / f"{p['badge']}.jpg").exists()]
        print(f"{len(todo) - len(pending)} existing, {len(pending)} to render")
        if pending:
            ensure_deps()
            render(pending)
    manifest = write_manifest(people)
    write_sheet(people, manifest)
    print(f"manifest: {len(manifest)} entries; sheet: {SHEET.name}")


if __name__ == "__main__":
    main()
