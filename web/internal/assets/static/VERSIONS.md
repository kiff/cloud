# Vendored static assets

These files are vendored, not built. Update them by replacing the
files in this directory and bumping the pinned version below.

## Stylesheets and scripts

| File                  | Source                                                        | Version |
|-----------------------|---------------------------------------------------------------|---------|
| `open-props.min.css`  | https://unpkg.com/open-props@1.7.16/open-props.min.css        | 1.7.16  |
| `js/htmx.min.js`      | https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js             | 2.0.4   |

`htmx.min.js` is first used by `/delegate`.

## Webfonts (`fonts/`)

Self-hosted woff2 (latin subset) for the redesigned site (#4). Vendored
from Google Fonts so the web binary stays hermetic — no runtime
`<link>` to fonts.googleapis.com (the web-stack design). `@font-face` declarations
live in `css/fonts.css`. Re-fetch by querying the Google Fonts CSS API
with a modern User-Agent (returns woff2), keeping only the `latin`
subset blocks, and saving each `src` woff2 into this directory.

| Family            | Weights / styles            | Files                                   |
|-------------------|-----------------------------|-----------------------------------------|
| DM Serif Display  | 400, 400 italic             | `dm-serif-display-400*.woff2`           |
| IBM Plex Mono     | 400, 500                    | `ibm-plex-mono-{400,500}.woff2`         |
| Geist             | 300–500 (single file)       | `geist-400.woff2`                       |

Note on Geist: the upstream vendoring copied one blob into
`geist-300/400/500.woff2` (byte-identical), so the three weights never
rendered a real contrast. Collapsed to a single `geist-400.woff2`
declared over the `300 500` range in `css/fonts.css`. To restore true
per-weight contrast, vendor distinct woff2 files for 300/500 and split
the `@font-face` back out.

## /tafs webfont

`/tafs` uses one typeface, Schibsted Grotesk, as a variable font (weights
400 to 900) in two subsets: latin, and latin-ext for Polish and the other
site languages. `@font-face` lives in `css/fonts.css`. Vendored from
`@fontsource-variable/schibsted-grotesk@5.3.0`
(https://cdn.jsdelivr.net/npm/@fontsource-variable/schibsted-grotesk@5.3.0/files/),
the Google Fonts build. License: SIL Open Font License 1.1, Copyright 2023
The Schibsted-Grotesk Project Authors
(https://github.com/schibsted/schibsted-grotesk).

| File | SHA-256 |
|------|---------|
| `schibsted-grotesk-latin-wght.woff2` | `4c8b93f431d462c696e12b9d6a033feb3394d36e66e781357c496b95d8a75e05` |
| `schibsted-grotesk-latin-ext-wght.woff2` | `ee9eeb232cc1fe7979dad813a570f4b19f1eb4496daf56c94ec9de63abdb2936` |

## Customer-ops interactive film webfonts

The film preserves the accepted reference's Latin faces, served from
`/static/fonts/`. Its local declarations live in
the film's own `fonts.css`. The film's web copy was removed with
`/integrate`; these files stay for the film's own capture.
IBM Plex Mono 400/500 reuse the existing byte-identical files above.

| Family | Upstream version | Weights / styles | Files |
|--------|------------------|------------------|-------|
| [Barlow Condensed](https://github.com/google/fonts/tree/main/ofl/barlowcondensed) | Google Fonts v13 | 600, 700, 800 | `barlow-condensed-{600,700,800}.woff2` |
| [IBM Plex Mono](https://github.com/google/fonts/tree/main/ofl/ibmplexmono) | Google Fonts v20 | 600 (400/500 reused) | `ibm-plex-mono-600.woff2` |
| [IBM Plex Sans](https://github.com/google/fonts/tree/main/ofl/ibmplexsans) | Google Fonts v23 | 400, 500, 600 (shared variable face); 400 italic | `ibm-plex-sans-400-600.woff2`, `ibm-plex-sans-400-italic.woff2` |

Pinned SHA-256 of the reference's WOFF2 bytes:

| File | SHA-256 |
|------|---------|
| `barlow-condensed-600.woff2` | `5bc49d41f32810a114765314392332ce2acae2276635eee265a50319a12c1b49` |
| `barlow-condensed-700.woff2` | `e3e520cb7468f2efd60bc2ce96694567c3312f4ef414fffbe605e3ad146b24e6` |
| `barlow-condensed-800.woff2` | `89eec21b1a1d6fd579d3e3c518901c5a6e3579e0e7a4f8b2f17b1473117bc30d` |
| `ibm-plex-mono-400.woff2` | `c36f509c0a8f9f85f29cb44bc8701d8a9e0b14c499e77a884f789ead7093a7ac` |
| `ibm-plex-mono-500.woff2` | `a76f53ca6612e7b3828eec2311098675b7f9849ae4169a8bcef6302aec02a6c0` |
| `ibm-plex-mono-600.woff2` | `ad4580d8cb4b5f627c2d18457656732f7f7b070f7837fbc380e08054157e6f6c` |
| `ibm-plex-sans-400-italic.woff2` | `134dc0ee94e70cf5d52f608a7ac4b864c9b00bdf69779b8919712ce89da253b1` |
| `ibm-plex-sans-400-600.woff2` | `056e4e2459f57a0033c8c9c844ff19d6e42ac8602027803d4345823bcc939818` |

## Partner badges (`img/partners/`)

Vendored unchanged from each program's own download. The badge is never
recolored, reshaped or retexted, and is drawn smaller than the kiff mark.

| File | Source | SHA-256 |
|------|--------|---------|
| `nvidia-inception-1c-blk.svg` | NVIDIA Inception member badge, "Inception Badges 8/8/2023" (`for-screen/nvidia-inception-program-badge-rgb-1c-blk-for-screen.svg`), from https://design.nvidia.com/partners/inception/nvidia-inception-program/member-badge | `d1c5c43dd96702d73e771ed3dfbc8b2bad597da301228f94ab558dc9151fc356` |
