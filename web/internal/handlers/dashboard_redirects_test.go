package handlers_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

// dashboard_redirects_test.go covers the redirects from retired/legacy
// dashboard paths to the new IA. Bookmarks and external links to old
// paths shouldn't 404; they should land somewhere sensible:
//   - /dashboard/start* → /dashboard (retired Onboarding; 301)
//   - /dashboard/examples → /cards           (retired sandbox gallery; 301)
//   - /dashboard/activity, /dashboard/proposals, /dashboard/receipts,
//     /dashboard/approvals → the domain Activity view (the authenticated-surface design;
//     303, since the target is resolved per-tenant at request time)

// TestDashboardRedirects_LegacyPaths confirms the static legacy paths
// 301 to their new home, and the folded tenant-wide surfaces 303 into
// the domain Activity view (resolved per request — apex when no single
// domain is resolvable, as with this nil-api wiring). The handler
// tolerates a nil api for the redirect paths; auth still applies (the
// redirect sits behind RequireSession), so a session cookie is forwarded.
func TestDashboardRedirects_LegacyPaths(t *testing.T) {
	t.Parallel()
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)

	// Build the dashboard handler with a nil api — the redirect
	// routes never read it. The handler tolerates nil for tabs
	// that try to call upstream (renders an "API not configured"
	// notice) but the redirects bail out before that branch.
	dashH := handlers.NewDashboardHandler(nil, mgr, handlers.StaticBearerSource(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	inner := http.NewServeMux()
	dashH.Register(inner)
	gated := handlers.RequireSession(mgr)(inner)

	// The dashboard root is where sign-in lands; home is the cards.
	for _, from := range []string{"/dashboard", "/dashboard/"} {
		req := httptest.NewRequest(http.MethodGet, from, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if loc := rec.Result().Header.Get("Location"); rec.Code != http.StatusFound || loc != "/cards" {
			t.Errorf("%s: got %d → %q, want 302 → /cards", from, rec.Code, loc)
		}
	}

	cases := []struct {
		from string
		to   string
	}{
		{"/dashboard/start", "/dashboard"},
		{"/dashboard/start/", "/dashboard"},
		{"/dashboard/examples", "/cards"}, // retired sandbox gallery (the first-decision design amendment)
	}
	for _, c := range cases {
		t.Run(c.from, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.from, nil)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			gated.ServeHTTP(rec, req)

			if rec.Code != http.StatusMovedPermanently {
				t.Errorf("status: got %d, want 301; body=%s", rec.Code, rec.Body.String())
			}
			if loc := rec.Result().Header.Get("Location"); loc != c.to {
				t.Errorf("Location: got %q, want %q", loc, c.to)
			}
		})
	}

	// The retired tenant-wide surfaces (/dashboard/activity, plus
	// /dashboard/proposals, /dashboard/receipts, /dashboard/approvals)
	// fold into the domain's Activity view. They resolve the tenant's
	// domain; with a nil api (no domain resolvable) they land on the
	// domains apex via a 303.
	for _, from := range []string{
		"/dashboard/activity",
		"/dashboard/proposals",
		"/dashboard/receipts",
		"/dashboard/approvals",
	} {
		t.Run(from, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, from, nil)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			gated.ServeHTTP(rec, req)

			if rec.Code != http.StatusSeeOther {
				t.Errorf("status: got %d, want 303; body=%s", rec.Code, rec.Body.String())
			}
			if loc := rec.Result().Header.Get("Location"); loc != "/dashboard/domains" {
				t.Errorf("Location: got %q, want /dashboard/domains", loc)
			}
		})
	}
}

// issueSessionWithLastDomain issues a session cookie carrying a
// remembered LastDomain, so the redirect resolver can be exercised in
// the multi-domain case (where len(list) != 1 and only the remembered
// selection resolves a target).
func issueSessionWithLastDomain(t *testing.T, mgr *session.Manager, lastDomain string) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := mgr.Issue(rec, session.Session{
		Subject: "alice", Bearer: "alice", Email: "alice@example.com",
		DisplayName: "Alice", ExpiresAt: time.Now().UTC().Add(time.Hour),
		LastDomain: lastDomain,
	}); err != nil {
		t.Fatalf("Issue session: %v", err)
	}
	cs := rec.Result().Cookies()
	if len(cs) == 0 {
		t.Fatalf("Issue session: no cookie")
	}
	return cs[0]
}

// issueSessionWithLastWindow mirrors issueSessionWithLastDomain for the
// remembered evidence window (Govern/Activity), so a render with no
// ?window= restores the last selection instead of the 30-day default.
func issueSessionWithLastWindow(t *testing.T, mgr *session.Manager, lastWindow string) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := mgr.Issue(rec, session.Session{
		Subject: "alice", Bearer: "alice", Email: "alice@example.com",
		DisplayName: "Alice", ExpiresAt: time.Now().UTC().Add(time.Hour),
		LastWindow: lastWindow,
	}); err != nil {
		t.Fatalf("Issue session: %v", err)
	}
	cs := rec.Result().Cookies()
	if len(cs) == 0 {
		t.Fatalf("Issue session: no cookie")
	}
	return cs[0]
}

// TestDashboardRedirects_RemembersLastDomain covers the multi-domain
// selection-persistence hotfix: a tenant with >1 domain and no remembered
// selection lands on the apex (can't guess), but once a domain is
// remembered (LastDomain), the slug-free /dashboard/activity resolves to
// THAT domain's Activity — so the scope survives a hop through a
// non-domain tab (Runtimes → Activity). A stale/deleted remembered slug
// falls back to the apex.
func TestDashboardRedirects_RemembersLastDomain(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	gated := newDomainsMux(t, twoDomainAPI(t), mgr)

	// No remembered selection + two domains → apex (unchanged behavior).
	t.Run("no selection → apex", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/activity", nil)
		req.AddCookie(issueOnboardingSession(t, mgr))
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status %d, want 303", rec.Code)
		}
		if loc := rec.Result().Header.Get("Location"); loc != "/dashboard/domains" {
			t.Errorf("Location: got %q, want /dashboard/domains", loc)
		}
	})

	// Remembered domain (valid) → that domain's Activity, not the apex.
	t.Run("remembered → that domain", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/activity", nil)
		req.AddCookie(issueSessionWithLastDomain(t, mgr, "beta"))
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status %d, want 303", rec.Code)
		}
		const want = "/dashboard/domains/beta/evidence"
		if loc := rec.Result().Header.Get("Location"); loc != want {
			t.Errorf("Location: got %q, want %q", loc, want)
		}
	})

	// Stale remembered slug (not in the live list) → apex, never a
	// dead domain.
	t.Run("stale selection → apex", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/activity", nil)
		req.AddCookie(issueSessionWithLastDomain(t, mgr, "deleted-domain"))
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status %d, want 303", rec.Code)
		}
		if loc := rec.Result().Header.Get("Location"); loc != "/dashboard/domains" {
			t.Errorf("Location: got %q, want /dashboard/domains (stale slug must not route to a dead domain)", loc)
		}
	})
}

// TestDashboardRedirects_SingleDomainToActivity covers the resolved
// branch of redirectToDomainActivity that the nil-api test can't reach:
// a one-domain tenant's retired routes 303 straight into that domain's
// Activity (evidence) view, not the apex.
func TestDashboardRedirects_SingleDomainToActivity(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newDomainsMux(t, srv, mgr)

	for _, from := range []string{
		"/dashboard/proposals",
		"/dashboard/receipts",
		"/dashboard/approvals",
		"/dashboard/activity",
	} {
		t.Run(from, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, from, nil)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			gated.ServeHTTP(rec, req)

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status: got %d, want 303; body=%s", rec.Code, rec.Body.String())
			}
			const want = "/dashboard/domains/duplicate-payment/evidence"
			if loc := rec.Result().Header.Get("Location"); loc != want {
				t.Errorf("Location: got %q, want %q", loc, want)
			}
		})
	}
}
