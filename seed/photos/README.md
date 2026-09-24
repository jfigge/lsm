# Staff profile photos

One avatar per person in `../staff.json`, named `<badge>.jpg` (256×256 JPEG).
Badge **100000** (Jason Figge) is intentionally absent; he supplies his own photo.
The loader joins on badge through `manifest.json`. `staff.json` has no photo field.

## Source and licence

- **No real people.** Every image is a cartoon avatar drawn from vector parts. Nothing
  is downloaded from search engines, stock sites, social media or any source of real faces.
- **Generator:** [DiceBear](https://www.dicebear.com/) 9.4.2 (`@dicebear/core`,
  `@dicebear/collection`), run **offline** from `node_modules`. It makes no network calls
  per image. DiceBear code is MIT (© Florian Körner).
- **Artwork:** the styles `avataaars` and `avataaars-neutral` are based on
  [Avataaars](https://avataaars.com/) by Pablo Stanley. Licence: *free for personal
  and commercial use* (see `node_modules/@dicebear/avataaars/LICENSE`).
- **Rasteriser:** [sharp](https://sharp.pixelplumbing.com/) 0.35.4 (Apache-2.0).

## Gender mapping

| `gender` | Style | How it's constrained |
|---|---|---|
| `male` | avataaars | short/male hair styles only; facial hair 35% |
| `female` | avataaars | long/female hair styles (incl. hijab); no facial hair |
| `unknown` | avataaars-neutral | face only: no hair, facial hair or clothing |

All styles use natural skin and hair colours and calm expressions. Glasses appear
15% of the time; sunglasses are excluded.

## Determinism

The seed for each image is its badge number. With the pinned versions in
`package-lock.json`, a rerun produces byte-identical files. Changing the option
lists in `render.mjs` or upgrading DiceBear changes the images.

## Size

Flat vector art compresses well: files come out around 8–12 KB at JPEG quality 95,
below the 20–30 KB target. They are not padded. `render.mjs` caps them at 30 KB.

## Usage

```sh
python3 generate.py              # all staff; skips existing <badge>.jpg (resumable)
python3 generate.py --limit 20   # first 20 in staff.json order
python3 generate.py --force      # re-render existing files
python3 generate.py --sheet-only # only rebuild manifest.json + contact_sheet.html
```

Requires Python 3 and Node. `npm ci` runs automatically the first time.
The script stops on the first render error.

## Files

- `generate.py`: entry point (stdlib only); writes the manifest and contact sheet
- `render.mjs`: DiceBear → SVG → JPEG renderer
- `manifest.json`: `{badge: {file, source, licence}}`
- `contact_sheet.html`: every photo with name, badge and gender, for visual review
