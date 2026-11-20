package region

import (
	"html/template"
	"log/slog"
	"net/http"
)

// Refuser answers a caller refused by the gate: KIFF's page saying it isn't
// available where they are, in their language. country is the address's
// country ("" when unknown); it's for the page, never logged with the address.
type Refuser func(w http.ResponseWriter, r *http.Request, country string)

// Gate refuses the web's sign-in pages, and a new device's approval, to an
// address outside the supported countries, as the API's gate does for its
// session routes: a person already signed in isn't cut off, but no new session
// starts from there. An address no range holds is refused too.
type Gate struct {
	table   *Table
	routes  map[string]struct{}
	refused Refuser
	log     *slog.Logger
}

// SessionRoutes are the web's routes that start a sign-in, approve a new
// device, or grant a new client access (OAuth consent, and the device grant's
// approval): the pages the API's /oauth/authorize sends a browser on to.
var SessionRoutes = []string{
	"GET /sign-in",
	"POST /sign-in",
	"GET /sign-in/callback",
	"POST /sign-in/callback",
	"GET /sign-in/app",
	"GET /auth/device",
	"GET /oauth/consent",
	"POST /oauth/consent",
	"GET /oauth/device",
	"POST /oauth/device",
}

// NewGate checks routes ("METHOD /path", exact) against table, answering a
// refusal with refused (Refused when nil).
func NewGate(table *Table, routes []string, refused Refuser, log *slog.Logger) *Gate {
	if log == nil {
		log = slog.Default()
	}
	if refused == nil {
		refused = Refused
	}
	g := &Gate{table: table, routes: make(map[string]struct{}, len(routes)), refused: refused, log: log}
	for _, r := range routes {
		g.routes[r] = struct{}{}
	}
	return g
}

// Wrap checks a request to one of the gate's routes, and passes everything
// else through.
func (g *Gate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := g.routes[r.Method+" "+r.URL.Path]; ok {
			if country, allowed := g.Allowed(r); !allowed {
				c := country
				if c == "" {
					c = "unknown"
				}
				g.log.InfoContext(r.Context(), "region: refused", slog.String("route", r.Method+" "+r.URL.Path), slog.String("country", c))
				g.refused(w, r, country)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Allowed reports the caller's country and whether it may sign in.
func (g *Gate) Allowed(r *http.Request) (country string, ok bool) {
	addr, internal := ClientAddr(r)
	if internal {
		return "", true
	}
	if !addr.IsValid() {
		return "", false
	}
	country = g.table.Country(addr)
	return country, country != "" && IsSupported(country)
}

var refusedPage = template.Must(template.New("refused").Parse(`<!doctype html>
<html lang="{{.Lang}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex"><title>KIFF</title></head>
<body><main><p>{{.Words}}</p></main></body></html>
`))

// Refused is the plain stand-in page: the words in the request's language,
// with 403 and no caching.
func Refused(w http.ResponseWriter, r *http.Request, _ string) {
	lang := Language(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Language", lang)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_ = refusedPage.Execute(w, struct{ Lang, Words string }{lang, Here[lang]})
}
