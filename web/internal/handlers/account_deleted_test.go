package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

// TestAccountDeleted_SaysWhenTheGraceWindowEnds: the page Settings →
// Delete account lands on says the deletion was accepted, and the
// grace window's last day from the flash. Without that flash it claims
// nothing and sends the visitor home.
func TestAccountDeleted_SaysWhenTheGraceWindowEnds(t *testing.T) {
	t.Parallel()
	m, err := session.NewManager([]byte("a-test-secret-of-at-least-thirty-two-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	h := handlers.AccountDeleted(m)

	set := httptest.NewRecorder()
	if err := m.IssueFlash(set, session.Flash{Kind: session.FlashAccountDeleted, Message: "2026-11-28T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/account/deleted", nil)
	for _, c := range set.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"deletion is requested", "28 November 2026", "mailto:privacy@kiff.dev"} {
		if !strings.Contains(body, want) {
			t.Errorf("page should say %q", want)
		}
	}
	if rec.Header().Get("X-Robots-Tag") != "noindex" {
		t.Error("the page should not be indexed")
	}

	for name, r := range map[string]*http.Request{
		"no flash":      httptest.NewRequest(http.MethodGet, "/account/deleted", nil),
		"another flash": withFlash(t, m, session.Flash{Kind: session.FlashError, Message: "x"}),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
			t.Errorf("%s: got %d to %q, want 302 to /", name, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func withFlash(t *testing.T, m *session.Manager, f session.Flash) *http.Request {
	t.Helper()
	set := httptest.NewRecorder()
	if err := m.IssueFlash(set, f); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/account/deleted", nil)
	for _, c := range set.Result().Cookies() {
		r.AddCookie(c)
	}
	return r
}
