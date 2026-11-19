package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// A gated GET keeps its query through sign-in, so a prefilled Card link
// arrives filled in; a POST returns to its path only.
func TestSignInNextKeepsGetQuery(t *testing.T) {
	get := httptest.NewRequest(http.MethodGet, "/cards/new?task=refunds&limit=5000", nil)
	if got := signInNext(get); got != "/cards/new?task=refunds&limit=5000" || !isSafeNext(got) {
		t.Errorf("GET next %q", got)
	}
	post := httptest.NewRequest(http.MethodPost, "/cards/x/test?a=1", nil)
	if got := signInNext(post); got != "/cards/x/test" {
		t.Errorf("POST next %q", got)
	}
}

func TestRedirectTargetKeepsQueryAndStaysLocal(t *testing.T) {
	for next, want := range map[string]string{
		"/cards/new?task=refunds&limit=5000": "/cards/new?task=refunds&limit=5000",
		"/cards":                             "/cards",
		"https://evil.example/x?a=1":         "/dashboard",
		"//evil.example/x":                   "/dashboard",
		// Browsers read a backslash as a slash: each of these would
		// leave the site.
		`/\evil.example`:        "/dashboard",
		`/%5Cevil.example`:      "/dashboard",
		`/%5c%5cevil.example/x`: "/dashboard",
		"/\t/evil.example":      "/dashboard",
		"/%2F/evil.example":     "/dashboard",
		"/cards/a%20b?x=1":      "/cards/a%20b?x=1",
	} {
		r := httptest.NewRequest(http.MethodGet, "/sign-in?next="+url.QueryEscape(next), nil)
		if got := redirectTarget(r); got != want {
			t.Errorf("redirectTarget(%q) = %q, want %q", next, got, want)
		}
	}
}

// The Clerk callback hands next to the browser as is, so isSafeNext
// alone must refuse what a browser would read as another site.
func TestIsSafeNextRefusesWhatLeavesTheSite(t *testing.T) {
	for _, next := range []string{`/\evil.example`, `/%5Cevil.example`, "/\t/evil.example", "//evil.example", "/%2F/evil.example", "https://evil.example"} {
		if isSafeNext(next) {
			t.Errorf("isSafeNext(%q) = true", next)
		}
	}
	for _, next := range []string{"/dashboard", "/cards/new?task=refunds&limit=5000"} {
		if !isSafeNext(next) {
			t.Errorf("isSafeNext(%q) = false", next)
		}
	}
}
