package handlers_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

// TestStreamProxy_AnonymousGated confirms /dashboard/stream inherits
// RequireSession — an anonymous request is bounced to sign-in, never
// reaching the proxy.
func TestStreamProxy_AnonymousGated(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	dashMux := http.NewServeMux()
	handlers.NewDashboardHandler(nil, mgr, nil, nil).Register(dashMux)
	protected := handlers.RequireSession(mgr)(dashMux)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/stream", nil)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (RequireSession redirect)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "/sign-in") {
		t.Fatalf("Location = %q, want a /sign-in redirect", loc)
	}
}

// TestStreamProxy_NilAPI confirms an authenticated request degrades to 503
// when no API client is wired (rather than panicking).
func TestStreamProxy_NilAPI(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	dashMux := http.NewServeMux()
	handlers.NewDashboardHandler(nil, mgr, nil, nil).Register(dashMux)
	protected := handlers.RequireSession(mgr)(dashMux)

	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{
		Subject: "u", Bearer: "b", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/dashboard/stream", nil)
	req.AddCookie(issue.Result().Cookies()[0])
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// TestStreamProxy_PipesUpstreamWithBearer confirms the proxy attaches the
// session bearer and pipes the upstream SSE frames through to the browser.
func TestStreamProxy_PipesUpstreamWithBearer(t *testing.T) {
	t.Parallel()

	gotAuth := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		fmt.Fprint(w, "event: decision\ndata: {\"outcome\":\"allowed\"}\n\n")
		if fl != nil {
			fl.Flush()
		}
		<-r.Context().Done() // hold open until the proxy (client) disconnects
	}))
	defer upstream.Close()

	apiClient, err := apiclient.NewSessionAware(upstream.URL, 0)
	if err != nil {
		t.Fatalf("NewSessionAware: %v", err)
	}
	mgr := newTestSessionManager(t)
	dashMux := http.NewServeMux()
	handlers.NewDashboardHandler(apiClient, mgr, handlers.StaticBearerSource(), nil).Register(dashMux)
	protected := handlers.RequireSession(mgr)(dashMux)

	front := httptest.NewServer(protected)
	defer front.Close()

	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{
		Subject: "u", Bearer: "session-bearer", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	cookie := issue.Result().Cookies()[0]

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, front.URL+"/dashboard/stream", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// The proxy must have attached the session bearer upstream.
	select {
	case a := <-gotAuth:
		if a != "Bearer session-bearer" {
			t.Fatalf("upstream Authorization = %q, want Bearer session-bearer", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream never received the request")
	}

	// And the event frame must pipe through.
	lines := make(chan string, 32)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case l := <-lines:
			if strings.HasPrefix(l, "event: decision") {
				return // success
			}
		case <-deadline:
			t.Fatal("did not receive the piped decision event")
		}
	}
}
