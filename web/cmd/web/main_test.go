package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/kiff/cloud/web/internal/content/apps"
	"github.com/kiff/cloud/web/internal/content/desktop"
)

// Every top-level path of the signed-in app must reach it rather than
// fall through to the landing page's "GET /". /agents shipped
// unreachable once; this pins the paths the dashboard registers.
func TestSignedInAppMounts(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Served-By", "landing")
	}))
	mountSignedInApp(mux, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Served-By", "app")
	}))
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/dashboard"},
		{http.MethodGet, "/dashboard/domains"},
		{http.MethodPost, "/dashboard/keys"},
		{http.MethodGet, "/auth/device"},
		{http.MethodGet, "/cards"},
		{http.MethodGet, "/cards/refund-agent-refunds/decisions"},
		{http.MethodPost, "/cards/refund-agent-refunds/test"},
		{http.MethodGet, "/agent-cards"},
		{http.MethodGet, "/agents"},
		{http.MethodGet, "/agents/new"},
		{http.MethodGet, "/agents/refund-agent"},
		{http.MethodGet, "/agents/refund-agent/cards"},
		{http.MethodPost, "/agents"},
		{http.MethodPost, "/agents/refund-agent/key"},
		{http.MethodGet, "/needs-you"},
		{http.MethodGet, "/needs-you/exc-1"},
		{http.MethodPost, "/needs-you/exc-1/approve-once"},
		{http.MethodGet, "/welcome"},
		{http.MethodPost, "/welcome"},
		{http.MethodGet, "/tools"},
		{http.MethodGet, "/tools/new"},
		{http.MethodPost, "/tools"},
		{http.MethodPost, "/tools/discover"},
		{http.MethodPost, "/tools/issue_refund/remove"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if got := rec.Header().Get("X-Served-By"); got != "app" {
			t.Errorf("%s %s served by %q, want the signed-in app", c.method, c.path, got)
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pricing", nil))
	if rec.Header().Get("X-Served-By") != "landing" {
		t.Error("a public path no longer reaches the landing page")
	}
}

// The server refuses a cross-origin state change before any route sees
// it, on the signed-in app's routes and every other one.
func TestServerRefusesCrossOriginPosts(t *testing.T) {
	reached := 0
	mux := http.NewServeMux()
	mux.Handle("GET /", http.NotFoundHandler())
	mountSignedInApp(mux, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		w.WriteHeader(http.StatusNoContent)
	}))
	h := serverHandler(mux, "")
	post := func(path, site, origin string) int {
		req := httptest.NewRequest(http.MethodPost, "http://app.kiff.dev"+path, nil)
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, path := range []string{"/needs-you/exc-1/approve-once", "/tools/issue_refund/remove", "/agents/refund-agent/gateway-key", "/dashboard/keys"} {
		if got := post(path, "same-site", "https://status.kiff.dev"); got != http.StatusForbidden {
			t.Errorf("%s from a sibling subdomain: %d", path, got)
		}
		if got := post(path, "", "https://evil.example"); got != http.StatusForbidden {
			t.Errorf("%s from another site (no Sec-Fetch-Site): %d", path, got)
		}
	}
	if reached != 0 {
		t.Fatalf("a refused request reached the app %d times", reached)
	}
	if got := post("/tools/issue_refund/remove", "same-origin", "http://app.kiff.dev"); got != http.StatusNoContent || reached != 1 {
		t.Fatalf("same-origin post: %d, reached %d", got, reached)
	}
}

// A removed page's URL keeps working: it redirects, permanently, to the docs
// page that carries it.
func TestRemovedPagesRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mountRemovedPages(mux)
	for from, to := range map[string]string{
		"/builders": "/docs/quickstart", "/integrate": "/docs/connect-an-agent",
		"/agent": "/docs/what-kiff-is", "/how-it-works": "/docs/connect-an-agent",
		"/why": "/", "/on-rails": "/pricing", "/ai-act": "/docs/ai-act", "/de/ai-act": "/docs/ai-act", "/pl/ai-act": "/docs/ai-act",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, from, nil))
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != to {
			t.Errorf("%s: %d to %q, want 301 to %q", from, rec.Code, rec.Header().Get("Location"), to)
		}
	}
}

// KIFF_APPS_PROVED off reads nothing from planque; on, it reads planque's
// proofs from KIFF_PLANQUE_URL at start.
func TestAppsProvedReadsPlanqueOnlyWhenOn(t *testing.T) {
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apps.ProvedPath {
			http.NotFound(w, r)
			return
		}
		reads.Add(1)
		// Today's proofs, so the list the other tests read stays as apps.json has it.
		_, _ = w.Write([]byte(`{"proved": {"stripe": {"at": "2026-11-13", "how": "key"}, "qonto": {"at": "2026-11-13", "how": "key"},
		  "shippo": {"at": "2026-11-13", "how": "key"}, "intercom": {"at": "2026-11-13", "how": "key"}, "zendesk": {"at": "2026-11-13", "how": "key"},
		  "pipedrive": {"at": "2026-11-13", "how": "key"}, "github": {"at": "2026-11-13", "how": "key"}, "gmail": {"at": "2026-11-13", "how": "oauth"},
		  "typeform": {"at": "2026-11-16", "how": "oauth"}, "googlecalendar": {"at": "2026-11-16", "how": "oauth"}, "googledrive": {"at": "2026-11-16", "how": "oauth"}}}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	for _, off := range []string{"", "off", "true"} {
		if startAppsProved(ctx, env(map[string]string{envAppsProved: off, envPlanqueURL: srv.URL}), logger) {
			t.Errorf("KIFF_APPS_PROVED=%q started the proofs", off)
		}
	}
	if n := reads.Load(); n != 0 {
		t.Fatalf("planque was read %d times with the switch off", n)
	}
	if !startAppsProved(ctx, env(map[string]string{envAppsProved: "on", envPlanqueURL: srv.URL}), logger) {
		t.Fatal("KIFF_APPS_PROVED=on didn't start the proofs")
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("planque was read %d times at start, want 1", n)
	}
	if n := len(apps.Working()); n != 11 {
		t.Errorf("%d apps work under today's proofs, want 11", n)
	}
}

// KIFF_DESKTOP_RELEASES off reads nothing from GitHub and holds nothing back;
// on, it reads each computer's latest release at start, and
// KIFF_DESKTOP_NOT_AVAILABLE holds the computers it names back.
func TestDesktopReleasesReadGitHubOnlyWhenOn(t *testing.T) {
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	defer desktop.ResetForTest()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	for _, off := range []string{"", "off", "true"} {
		if startDesktopReleases(ctx, env(map[string]string{envDesktopReleases: off, envGitHubAPI: srv.URL, envDesktopNotAvailable: "linux"}), logger) {
			t.Errorf("KIFF_DESKTOP_RELEASES=%q started the read", off)
		}
	}
	if n := reads.Load(); n != 0 {
		t.Fatalf("GitHub was read %d times with the switch off", n)
	}
	if s, _ := desktop.Of("linux"); s != desktop.StateComingSoon {
		t.Fatalf("held back with the switch off: %s", s)
	}
	if !startDesktopReleases(ctx, env(map[string]string{envDesktopReleases: "on", envGitHubAPI: srv.URL, envDesktopNotAvailable: "linux"}), logger) {
		t.Fatal("KIFF_DESKTOP_RELEASES=on didn't start the read")
	}
	if n := reads.Load(); n != int32(len(desktop.Channels)) {
		t.Errorf("GitHub was read %d times at start, want %d", n, len(desktop.Channels))
	}
	if s, _ := desktop.Of("linux"); s != desktop.StateNotAvailable {
		t.Errorf("linux: %s", s)
	}
	if s, _ := desktop.Of("macos"); s != desktop.StateComingSoon {
		t.Errorf("macos with no release: %s", s)
	}
}
