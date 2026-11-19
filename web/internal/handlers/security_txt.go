package handlers

import (
	"fmt"
	"net/http"
	"time"
)

// SecurityTxt serves GET /.well-known/security.txt (RFC 9116) — the
// standard, machine-readable vulnerability-disclosure pointer. Contact
// and policy point at surfaces that exist; Expires is generated one year
// out at serve time so it never goes stale in the source.
func SecurityTxt() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expires := time.Now().UTC().AddDate(1, 0, 0).Format(time.RFC3339)
		body := fmt.Sprintf(`# KIFF security disclosure
Contact: mailto:security@kiff.dev
Policy: https://kiff.dev/security
Canonical: https://kiff.dev/.well-known/security.txt
Preferred-Languages: en
Expires: %s
`, expires)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write([]byte(body))
	})
}
