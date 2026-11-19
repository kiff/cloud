package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSameOriginOnly(t *testing.T) {
	t.Parallel()
	h := SameOriginOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, c := range []struct {
		name, method, site, origin string
		want                       int
	}{
		{"same-origin form", http.MethodPost, "same-origin", "https://app.kiff.dev", 204},
		{"typed or bookmarked", http.MethodPost, "none", "", 204},
		{"another site", http.MethodPost, "cross-site", "https://evil.example", 403},
		// The case SameSite=Lax lets through: a sibling subdomain.
		{"sibling subdomain", http.MethodPost, "same-site", "https://status.kiff.dev", 403},
		{"delete from another site", http.MethodDelete, "cross-site", "", 403},
		{"old browser, same origin", http.MethodPost, "", "https://app.kiff.dev", 204},
		{"old browser, sibling subdomain", http.MethodPost, "", "https://www.kiff.dev", 403},
		{"old browser, opaque origin", http.MethodPost, "", "null", 403},
		{"not a browser", http.MethodPost, "", "", 204},
		{"a cross-site GET still reads", http.MethodGet, "cross-site", "https://evil.example", 204},
	} {
		req := httptest.NewRequest(c.method, "https://app.kiff.dev/tools/issue_refund/remove", nil)
		if c.site != "" {
			req.Header.Set("Sec-Fetch-Site", c.site)
		}
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, rec.Code, c.want)
		}
	}
}
