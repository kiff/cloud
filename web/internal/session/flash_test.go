package session_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kiff/cloud/web/internal/session"
)

func TestFlash_RoundTrip_ReadOnce(t *testing.T) {
	t.Parallel()
	mgr := newTestManager(t, testSecretA)
	flash := session.Flash{
		Kind:    session.FlashKeyRevealed,
		Message: "minted",
		Key: &session.FlashKeyPayload{
			ID: "k1", Plaintext: "kiff_live_abc", Label: "test",
		},
	}

	rec := httptest.NewRecorder()
	if err := mgr.IssueFlash(rec, flash); err != nil {
		t.Fatalf("IssueFlash: %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one cookie, got %d", len(cookies))
	}

	// First read returns the flash + clears the cookie.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookies[0])
	respRec := httptest.NewRecorder()
	got, ok := mgr.ReadFlash(respRec, req)
	if !ok {
		t.Fatalf("ReadFlash returned false on populated cookie")
	}
	if got.Kind != session.FlashKeyRevealed {
		t.Fatalf("kind = %q", got.Kind)
	}
	if got.Key == nil || got.Key.Plaintext != "kiff_live_abc" {
		t.Fatalf("key payload missing: %+v", got.Key)
	}

	// The response should carry an expired cookie (clear).
	clears := respRec.Result().Cookies()
	if len(clears) != 1 || clears[0].MaxAge >= 0 {
		t.Fatalf("expected expired clear cookie")
	}
}

func TestFlash_NoCookie_ReturnsFalse(t *testing.T) {
	t.Parallel()
	mgr := newTestManager(t, testSecretA)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	respRec := httptest.NewRecorder()
	if got, ok := mgr.ReadFlash(respRec, req); ok || got != nil {
		t.Fatalf("expected (nil, false); got (%+v, %v)", got, ok)
	}
}

func TestFlash_TamperedCookie_ReturnsFalse(t *testing.T) {
	t.Parallel()
	mgr := newTestManager(t, testSecretA)
	rec := httptest.NewRecorder()
	if err := mgr.IssueFlash(rec, session.Flash{Kind: session.FlashEventSent, Message: "hi"}); err != nil {
		t.Fatalf("IssueFlash: %v", err)
	}
	c := rec.Result().Cookies()[0]
	c.Value = "AAAA." + c.Value // mangle
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	respRec := httptest.NewRecorder()
	if got, ok := mgr.ReadFlash(respRec, req); ok || got != nil {
		t.Fatalf("expected tampered cookie to be rejected; got (%+v, %v)", got, ok)
	}
}

func TestIssueFlash_RejectsEmptyKind(t *testing.T) {
	t.Parallel()
	mgr := newTestManager(t, testSecretA)
	rec := httptest.NewRecorder()
	if err := mgr.IssueFlash(rec, session.Flash{}); err == nil {
		t.Fatalf("expected error on empty Kind")
	}
}
