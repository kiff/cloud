package handlers

import (
	"net/http"
	"strings"
)

// RequireOperator gates the /ops route tree to the operator
// allow-list. It layers ON TOP of RequireSession: a request has
// already been authenticated and the verified session is on the
// context by the time this runs.
//
// A signed-in subject that is not in the allow-list gets 404 — not
// 403 — so a tenant session cannot even confirm /ops exists. The
// surface must be invisible to non-operators. (Unauthenticated
// requests never reach here; RequireSession bounced them to
// /sign-in.)
//
// This is the apps/web edge gate. The real security boundary is the
// independent allow-list re-check on the apps/api /v1/ops/* endpoints
// — the console reads no data of its own, so even a bypass of this
// gate yields no cross-tenant data without an allow-listed bearer.
//
// subjects is the parsed KIFF_OPS_SUBJECTS list. When empty the gate
// rejects everyone (404), which matches "no operators configured =
// no operator surface."
func RequireOperator(subjects []string) func(http.Handler) http.Handler {
	allow := make(map[string]struct{}, len(subjects))
	for _, s := range subjects {
		if s = strings.TrimSpace(s); s != "" {
			allow[s] = struct{}{}
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s, ok := SessionFrom(r.Context())
			if !ok {
				http.NotFound(w, r)
				return
			}
			if _, isOp := allow[s.Subject]; !isOp {
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(r.Context()))
		})
	}
}

// ParseOperatorSubjects splits the comma-separated KIFF_OPS_SUBJECTS
// value into a trimmed, non-empty slice. Empty input yields nil. The
// web binary and the api binary parse the same env var the same way.
func ParseOperatorSubjects(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}
