package region

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Code is the error a refused caller gets, beside its words (Here) in the
// request's language; the apps show the words as sent and match only on Code.
const Code = "region_unsupported"

// Gate refuses the routes that start a session to an address outside the
// supported countries. An address no range holds is refused too: the check
// fails closed. A call from the box itself (another KIFF service through
// Caddy's loopback site) isn't a person's, and isn't checked.
type Gate struct {
	table  *Table
	routes map[string]struct{}
	log    *slog.Logger
}

// SessionRoutes are the API's routes that start a session or a sign-in, the
// ones a client calls itself and reads JSON from: a person signed in already
// isn't cut off mid-task by a trip abroad, but can't start a new session from
// there. /oauth/authorize isn't here: it runs in a browser and sends it on to
// the web's pages (sign-in, consent, the app's sign-in), and the web's gate
// refuses those with a page, not JSON.
var SessionRoutes = []string{
	"POST /oauth/device_authorization",
	"POST /v1/auth/device/start",
	"POST /v1/auth/app/token",
	"POST /v1/pair/claim",
}

// NewGate checks routes ("METHOD /path", exact) against table.
func NewGate(table *Table, routes []string, log *slog.Logger) *Gate {
	if log == nil {
		log = slog.Default()
	}
	g := &Gate{table: table, routes: make(map[string]struct{}, len(routes)), log: log}
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
				// The country only, never the address.
				g.log.InfoContext(r.Context(), "region: refused", slog.String("route", r.Method+" "+r.URL.Path), slog.String("country", orUnknown(country)))
				lang := Language(r)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Language", lang)
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusForbidden)
				// error_description too: the OAuth routes' clients read that field.
				_ = json.NewEncoder(w).Encode(map[string]string{"error": Code, "code": Code, "message": Here[lang], "error_description": Here[lang]})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Allowed reports the caller's country and whether it may start a session.
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

func orUnknown(c string) string {
	if c == "" {
		return "unknown"
	}
	return c
}
