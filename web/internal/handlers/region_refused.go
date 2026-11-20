package handlers

import (
	"html/template"
	"net/http"

	"github.com/kiff/cloud/web/internal/words"
)

// RegionRefused answers a new sign-in from where KIFF isn't offered: a 403
// page that says so in the person's language (an app's ui_locales, else the
// request's), with nothing to retry. The region gate decides and calls it;
// country is the code it found, never shown: the words say "where you are",
// since an address's country is a guess. A person already signed in isn't
// sent here: only new sessions are checked.
func RegionRefused(w http.ResponseWriter, r *http.Request, country string) {
	lang := appSignInLang(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Language", lang)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	w.WriteHeader(http.StatusForbidden)
	_ = regionRefusedPage.Execute(w, struct{ Lang, Line string }{lang, words.RegionIn(lang).Here})
}

var regionRefusedPage = template.Must(template.New("regionRefused").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="robots" content="noindex">
  <title>KIFF</title>
  <style>
    :root { color-scheme: light dark; --ink: #1b1b1f; --soft: #5b5b66; --bg: #ffffff; }
    @media (prefers-color-scheme: dark) { :root { --ink: #ececf1; --soft: #a0a0ab; --bg: #111114; } }
    body { margin: 0; font: 16px/1.45 system-ui, -apple-system, sans-serif; color: var(--ink); background: var(--bg); }
    main { max-width: 420px; margin: 0 auto; padding: 24px 16px 40px; }
    h1 { font-size: 24px; margin: 0 0 8px; }
    p { color: var(--soft); margin: 0; }
  </style>
</head>
<body>
<main>
  <h1>KIFF</h1>
  <p role="alert">{{.Line}}</p>
</main>
</body>
</html>
`))
