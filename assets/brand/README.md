# Lanfolk brand assets

Every file here (and the icons in `ui/assets/`, the SVGs in
`ui/frontend/dist/brand/` and the images in `installer/assets/`) is generated
from code by `scripts/brand/build.mjs`. Do not edit the outputs by hand; change
the script and run it again:

```sh
cd scripts/brand
npm install
node build.mjs
```

It needs Node 22 or later and Microsoft Edge (used headless to turn the SVGs
into PNG pixels).

## Provenance

- **Mark.** Four nodes on a diamond joined by three links that stop short of
  the nodes. The green left node is "you"; the missing link is the open seat
  that an invite fills. Pure geometry defined in `build.mjs` (node positions,
  radii, link width, gap); no image generator was used for any shipped asset.
  Below 24 px an optical variant (larger nodes, shorter gap) keeps the links
  visible; every tray frame uses it.
- **Wordmark.** "Lanfolk" set in Geist SemiBold 1.3.1 (npm `geist`, SIL Open
  Font License 1.1, see `fonts/Geist-OFL.txt`), tracking -0.02 em, converted to
  outlines with opentype.js 1.3.4. No font file is shipped.
- **Rasters.** PNG and ICO files are rendered from the SVGs above by headless
  Edge (canvas `drawImage`) at each target size; ICO files hold PNG frames.
- **Design history.** Logo concepts explored on 2026-11-04 with an image model
  and by hand were all rejected by the product owner; the shipped mark was
  drawn by hand in code and chosen from four refinements (variant "C").

## Files

| File | Use |
| --- | --- |
| `mark.svg`, `mark-on-dark.svg`, `mark-mono.svg`, `mark-small.svg` | The mark for light and dark grounds, one colour (`currentColor`), and the small-size variant |
| `logo.svg`, `logo-on-dark.svg` (+ `.png`) | Mark and wordmark, horizontal |
| `app-icon.svg`, `app-icon-256/512/1024.png`, `lanfolk.ico` | App icon: cobalt tile, white nodes, mint "you" node |
| `tray-online/connecting/offline/attention.svg` | Tray states (the `.ico` files are in `ui/assets/`) |

## Colours

| Name | Light | Dark | Use |
| --- | --- | --- | --- |
| Cobalt | `#2F5BEA` | `#6B8BFF` | Mark, primary actions, selection |
| Signal | `#14C38E` | `#3DDC97` | The "you" node; "online" |
| Mint | `#7CF2C4` | | The "you" node on the cobalt tile |
| Ink | `#0F172A` | | Wordmark on light grounds |
