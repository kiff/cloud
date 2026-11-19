package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kiff/cloud/web/internal/handlers"
)

// okHandler is the downstream the middleware wraps; a 200 means the
// request was NOT redirected (it passed through).
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestCanonicalAppHost_RedirectsGatedPathsFromApex(t *testing.T) {
	t.Parallel()
	mw := handlers.CanonicalAppHost("app.kiff.dev")(okHandler())

	cases := []struct {
		name string
		host string
		path string
		want string // expected Location
	}{
		{"sign-in on apex", "kiff.dev", "/sign-in?next=/dashboard/build", "https://app.kiff.dev/sign-in?next=/dashboard/build"},
		{"callback on apex", "kiff.dev", "/sign-in/callback?next=%2Fdashboard%2F", "https://app.kiff.dev/sign-in/callback?next=%2Fdashboard%2F"},
		{"dashboard on apex", "kiff.dev", "/dashboard", "https://app.kiff.dev/dashboard"},
		{"dashboard child on apex", "kiff.dev", "/dashboard/domains", "https://app.kiff.dev/dashboard/domains"},
		{"sign-out on apex", "kiff.dev", "/sign-out", "https://app.kiff.dev/sign-out"},
		{"ops on apex", "kiff.dev", "/ops/tenants", "https://app.kiff.dev/ops/tenants"},
		{"www apex", "www.kiff.dev", "/sign-in", "https://app.kiff.dev/sign-in"},
		{"apex with port", "kiff.dev:443", "/dashboard", "https://app.kiff.dev/dashboard"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tc.host+tc.path, nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)
			if rec.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302", rec.Code)
			}
			if got := rec.Header().Get("Location"); got != tc.want {
				t.Fatalf("Location = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCanonicalAppHost_PassesThroughOnAppHost(t *testing.T) {
	t.Parallel()
	mw := handlers.CanonicalAppHost("app.kiff.dev")(okHandler())

	// Already on the canonical host: gated paths must NOT redirect,
	// otherwise the sign-in flow would loop on the app host itself.
	for _, path := range []string{"/sign-in", "/sign-in/callback", "/dashboard", "/dashboard/domains", "/ops"} {
		req := httptest.NewRequest(http.MethodGet, "http://app.kiff.dev"+path, nil)
		req.Host = "app.kiff.dev"
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("path %q on app host: status = %d, want 200 (pass-through)", path, rec.Code)
		}
	}
}

func TestCanonicalAppHost_LeavesMarketingPathsAlone(t *testing.T) {
	t.Parallel()
	mw := handlers.CanonicalAppHost("app.kiff.dev")(okHandler())

	// Public paths on the apex are served where requested — only the
	// authenticated surface is canonicalized.
	for _, path := range []string{"/", "/pricing", "/why", "/whitepaper", "/llms.txt", "/healthz", "/sign-inflate"} {
		req := httptest.NewRequest(http.MethodGet, "http://kiff.dev"+path, nil)
		req.Host = "kiff.dev"
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("marketing path %q: status = %d, want 200 (pass-through)", path, rec.Code)
		}
	}
}

func TestCanonicalAppHost_NoopWhenUnset(t *testing.T) {
	t.Parallel()
	// Empty appHost (local single-host dev) disables canonicalization
	// entirely: every path passes through on every host.
	mw := handlers.CanonicalAppHost("")(okHandler())
	for _, path := range []string{"/sign-in", "/dashboard", "/ops"} {
		req := httptest.NewRequest(http.MethodGet, "http://localhost:8181"+path, nil)
		req.Host = "localhost:8181"
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("path %q with unset appHost: status = %d, want 200", path, rec.Code)
		}
	}
}

// The public site requested on the app host redirects permanently to the
// same path on kiff.dev, so search engines see one copy. The app's own
// routes, assets, robots.txt and the app host's home stay.
func TestCanonicalAppHost_SendsPublicPagesHome(t *testing.T) {
	t.Parallel()
	mw := handlers.CanonicalAppHost("app.kiff.dev")(okHandler())
	for path, want := range map[string]string{
		"/blog":                         "https://kiff.dev/blog",
		"/docs/quickstart":              "https://kiff.dev/docs/quickstart",
		"/tafs":                         "https://kiff.dev/tafs",
		"/legal/dpa":                    "https://kiff.dev/legal/dpa",
		"/demo/dashboard?page=36&tab=x": "https://kiff.dev/demo/dashboard?page=36&tab=x",
		"/sitemap.xml":                  "https://kiff.dev/sitemap.xml",
	} {
		req := httptest.NewRequest(http.MethodGet, "http://app.kiff.dev"+path, nil)
		req.Host = "app.kiff.dev"
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d %q, want 301 %q", path, rec.Code, rec.Header().Get("Location"), want)
		}
	}
	for _, path := range []string{"/", "/robots.txt", "/static/css/site.css", "/cards/new", "/needs-you", "/dashboard", "/sign-in", "/tafsx", "/docsearch"} {
		for _, host := range []string{"app.kiff.dev", "kiff.dev"} {
			req := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
			req.Host = host
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)
			if rec.Code == http.StatusMovedPermanently {
				t.Errorf("%s%s: redirected to %q, want it served", host, path, rec.Header().Get("Location"))
			}
		}
	}
}
