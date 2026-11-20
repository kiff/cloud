package region

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Code is the error a refused caller gets; the apps show their own words for it.
const Code = "region_unsupported"

// Message is the plain line beside Code.
const Message = "KIFF isn't available where you are. It works only in the countries where the AI it runs on is offered."

// Gate refuses the routes that start a session to an address outside the
// supported countries. An address no range holds is refused too: the check
// fails closed. A call from the box itself (another KIFF service through
// Caddy's loopback site) isn't a person's, and isn't checked.
type Gate struct {
	table  *Table
	routes map[string]struct{}
	log    *slog.Logger
}

// SessionRoutes are the API's routes that start a session or a sign-in: a
// person signed in already isn't cut off mid-task by a trip abroad, but can't
// start a new session from there.
var SessionRoutes = []string{
	"GET /oauth/authorize",
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
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": Code, "error_description": Message})
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

// ClientAddr is the caller's address. Caddy, on the same box, is the only
// proxy: the last X-Forwarded-For entry is the one it appended, and it's
// trusted only when the connection itself comes from loopback. A loopback or
// private last hop is a call from the box (internal); a connection that isn't
// from loopback is taken by its own address, whatever it sends.
func ClientAddr(r *http.Request) (addr netip.Addr, internal bool) {
	peer := peerAddr(r.RemoteAddr)
	if !peer.IsValid() {
		return netip.Addr{}, false
	}
	if !peer.IsLoopback() {
		return peer, isInternal(peer)
	}
	f := r.Header.Values("X-Forwarded-For")
	if len(f) == 0 {
		return peer, true
	}
	parts := strings.Split(f[len(f)-1], ",")
	last, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1]))
	if err != nil {
		return netip.Addr{}, false
	}
	last = last.Unmap()
	return last, isInternal(last)
}

func isInternal(a netip.Addr) bool {
	return a.IsLoopback() || a.IsPrivate()
}

func peerAddr(remote string) netip.Addr {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

func orUnknown(c string) string {
	if c == "" {
		return "unknown"
	}
	return c
}
