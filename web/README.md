# apps/web

The cloud's web binary. Serves both the public marketing site at
`kiff.dev` and (in later issues) the authenticated dashboard at
`app.kiff.dev`. Stack is fixed by the web-stack design:
Go + [`templ`](https://templ.guide) + [htmx](https://htmx.org), styled
with hand-written CSS layered on [Open Props](https://open-props.style)
for design tokens. No Node toolchain at build or runtime.

## Layout

```
web/
├── cmd/web/                 # the binary
├── internal/
│   ├── handlers/            # one http.Handler per route + tests
│   ├── pages/               # templ components per page
│   ├── components/          # shared templ partials (site shell, app shell)
│   └── assets/              # embed.FS for static files
│       └── static/          # vendored CSS + tokens, hand-written app.css,
│                            #   per-surface css/, vendored fonts/
├── Makefile
└── go.mod
```

## Running

A fresh clone runs with the Go toolchain alone — generated `_templ.go`
files are committed.

```bash
cd apps/web
go run ./cmd/web                 # serves on :8081
curl http://localhost:8081/      # placeholder landing
curl http://localhost:8081/whitepaper
curl http://localhost:8081/healthz
```

`make run`, `make build`, `make test`, and `make vet` are thin wrappers.

## Whitepaper

`/whitepaper` renders the KIFF Cards product paper (CC BY 4.0).
The canonical markdown lives at
`internal/content/whitepaper/whitepaper.md` and is embedded into the
binary via `embed.FS`. Rendering is server-side, memoized per process.

Edit the markdown here and update its revision record in
`internal/content/whitepaper/VERSIONS.md`, the paper's date and the page
header together. Check claims against the commercial canon and relevant
RFCs; distinguish implementation from deployment and future direction.

The [framework technical paper](https://github.com/kiff/kiff/blob/main/docs/whitepaper.md)
remains in the public framework repo and appears among the product paper's
technical references. It is no longer vendored into `/whitepaper`.

## Editing templates

Edit `.templ` files in `internal/pages/` or `internal/components/`,
then regenerate the `_templ.go` companions:

```bash
go install github.com/a-h/templ/cmd/templ@latest   # one-time
make templ                                         # regenerate
```

CI verifies that `make templ` produces no diff against the committed
output.

## Styling

CSS is hand-written, layered on Open Props tokens. No build step, no
preprocessor, no Node. Everything lives in `internal/assets/static/`,
is served at `/static/*`, and is embedded via `embed.FS`. The layering
(refactored from a single `app.css` monolith so each surface
loads only what it needs):

- `open-props.min.css` — vendored design tokens. Update by replacing
  the file and bumping the version in `VERSIONS.md`.
- `app.css` — the shared foundation loaded on **every** page: design
  tokens, reset, base typography, the button family, shared section
  headers. Depends on Open Props variables.
- `css/<surface>.css` — one stylesheet per surface
  (`landing`, `pricing`, `docs`, `prose`, `receipt`, `security`,
  `legal`, `whitepaper`, `dashboard`, `studio`, `playground`, `site`).
  A page loads the foundation plus its own sheet via the layout's
  `extraCSS` mechanism.
- `css/fonts.css` — self-hosted `@font-face` declarations
  (DM Serif Display / IBM Plex Mono / Geist). The woff2 files are
  vendored under `static/fonts/`; no runtime `<link>` to Google Fonts
  (the web-stack design). Re-vendoring is documented in `VERSIONS.md`.

### The two shells

Two layout families in `internal/components/`, each with its own font
and chrome wiring:

- **Marketing** (`LayoutSite`) — the public "paper" theme. Loads
  `css/fonts.css` + `css/site.css` and scopes its light palette to
  `body.site`.
- **Authenticated app** (`LayoutApp*`) — the dark dashboard surfaces.
  The frame (topbar, sidebar, nav grid) and the app webfont bindings
  live in `css/app-shell.css`, shared by every signed-in surface so
  the chrome and typography are defined once:
  - `DashboardLayout` — frame + `css/dashboard.css` (content styles).
  - `DashboardLayoutWithCSS` — same, plus one extra sheet (Playground).
  - `AppShellLayout` — frame only, no dashboard content layer, for
    surfaces that style themselves end-to-end (Studio). This is why
    Studio doesn't carry the whole dashboard stylesheet just for the
    chrome.

## Conventions

- Generated code lives next to its source and is committed.
- Tests sit next to the file they test (`landing_test.go` next to
  `landing.go`).
- The web binary depends on `github.com/a-h/templ` and nothing else
  outside the standard library at v0.1.

## Deploy

Per the web-stack design, the web binary ships next to the API binary on the same
EC2 instance behind Caddy in `us-east-1`. The deploy mechanics live
with issue #5 (infrastructure).
