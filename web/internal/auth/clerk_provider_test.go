package auth_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/auth"
)

func TestNewClerkProvider_RejectsEmptyFrontendAPI(t *testing.T) {
	t.Parallel()
	if _, err := auth.NewClerkProvider("", "pk_test_xxx"); err == nil {
		t.Fatalf("expected error on empty frontend API")
	}
}

func TestNewClerkProvider_NormalizesFrontendAPI(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"clerk.kiff.dev", "https://clerk.kiff.dev", "https://clerk.kiff.dev/"} {
		p, err := auth.NewClerkProvider(raw, "pk_test_xxx")
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if p.FrontendAPI() != "clerk.kiff.dev" {
			t.Fatalf("%q -> %q, want clerk.kiff.dev", raw, p.FrontendAPI())
		}
	}
}

func TestClerkProvider_SignInForm_RedirectsToHostedSignIn(t *testing.T) {
	t.Parallel()
	p, err := auth.NewClerkProvider("clerk.kiff.dev", "pk_live_xyz")
	if err != nil {
		t.Fatalf("NewClerkProvider: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "https://app.kiff.dev/sign-in", nil)
	req.TLS = newDummyTLS()
	rec := httptest.NewRecorder()
	p.SignInForm(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	// Redirect target is the Account Portal, not the frontend API.
	// The portal serves HTML; the frontend API doesn't.
	if !strings.HasPrefix(loc, "https://accounts.kiff.dev/sign-in?redirect_url=") {
		t.Fatalf("Location = %q (expected accounts.kiff.dev, not the frontend API)", loc)
	}
	parsed, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	got := parsed.Query().Get("redirect_url")
	if got != "https://app.kiff.dev/sign-in/callback" {
		t.Fatalf("redirect_url = %q, want https://app.kiff.dev/sign-in/callback", got)
	}
}

func TestClerkProvider_DerivesAccountPortalFromFrontendAPI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		frontendAPI string
		wantPortal  string
	}{
		// Production with custom CNAME: clerk.kiff.dev → accounts.kiff.dev
		{"clerk.kiff.dev", "accounts.kiff.dev"},
		// Dev: <name>.clerk.accounts.dev → accounts.<name>.clerk.accounts.dev
		{"direct-hare-40.clerk.accounts.dev", "accounts.direct-hare-40.clerk.accounts.dev"},
	}
	for _, tt := range tests {
		t.Run(tt.frontendAPI, func(t *testing.T) {
			p, err := auth.NewClerkProvider(tt.frontendAPI, "pk_test_xyz")
			if err != nil {
				t.Fatalf("NewClerkProvider: %v", err)
			}
			if got := p.AccountPortal(); got != tt.wantPortal {
				t.Errorf("AccountPortal() = %q, want %q", got, tt.wantPortal)
			}
		})
	}
}

func TestClerkProvider_AccountPortalOverride(t *testing.T) {
	t.Parallel()
	p, err := auth.NewClerkProvider("clerk.kiff.dev", "pk_live_xyz",
		auth.WithAccountPortal("accounts.example.com"))
	if err != nil {
		t.Fatalf("NewClerkProvider: %v", err)
	}
	if got := p.AccountPortal(); got != "accounts.example.com" {
		t.Fatalf("AccountPortal() = %q, want accounts.example.com", got)
	}
}

func TestClerkProvider_SignInForm_ForwardsNextParam(t *testing.T) {
	t.Parallel()
	p, _ := auth.NewClerkProvider("clerk.kiff.dev", "pk_live_xyz")
	req := httptest.NewRequest(http.MethodGet, "https://app.kiff.dev/sign-in?next=/dashboard/receipts/abc", nil)
	req.TLS = newDummyTLS()
	rec := httptest.NewRecorder()
	p.SignInForm(rec, req)

	loc := rec.Header().Get("Location")
	parsed, _ := url.Parse(loc)
	callbackRaw := parsed.Query().Get("redirect_url")
	callbackURL, _ := url.Parse(callbackRaw)
	if got := callbackURL.Query().Get("next"); got != "/dashboard/receipts/abc" {
		t.Fatalf("next param not forwarded; callback = %q", callbackRaw)
	}
}

func TestClerkProvider_SignOutURL_IncludesReturnURL(t *testing.T) {
	t.Parallel()
	// Regression test: accounts.kiff.dev does NOT have a /sign-out
	// page — hitting it returns 404. Sign-out is now server-side via
	// RevokeSession; this test guards against re-introducing the
	// broken redirect.
	//
	// We just verify that ClerkProvider does not implement the old
	// hostedSignOut interface (i.e. no SignOutURL method) so the
	// handler cannot accidentally call it.
	p, _ := auth.NewClerkProvider("clerk.kiff.dev", "pk_live_xyz")
	type hostedSignOut interface {
		SignOutURL(returnURL string) string
	}
	if _, ok := any(p).(hostedSignOut); ok {
		t.Fatal("ClerkProvider must NOT implement SignOutURL — accounts.kiff.dev/sign-out does not exist and returns 404; use RevokeSession instead")
	}
}

func TestClerkProvider_SignOutURL_OmitsEmptyReturn(t *testing.T) {
	t.Parallel()
	// Same as above: guards against re-introducing SignOutURL.
	p, _ := auth.NewClerkProvider("clerk.kiff.dev", "pk_live_xyz")
	type hostedSignOut interface {
		SignOutURL(returnURL string) string
	}
	if _, ok := any(p).(hostedSignOut); ok {
		t.Fatal("ClerkProvider must NOT implement SignOutURL — use RevokeSession for server-side session revocation")
	}
}

func TestClerkProvider_VerifySignIn_RejectsMissingBody(t *testing.T) {
	t.Parallel()
	p, _ := auth.NewClerkProvider("clerk.kiff.dev", "pk_live_xyz")
	// No Content-Type → handler refuses without a JSON body.
	req := httptest.NewRequest(http.MethodPost, "https://app.kiff.dev/sign-in/callback", nil)
	if _, err := p.VerifySignIn(req.Context(), req); err == nil {
		t.Fatalf("expected error when callback body missing")
	}
}

func TestClerkProvider_VerifySignIn_RejectsEmptyToken(t *testing.T) {
	t.Parallel()
	p, _ := auth.NewClerkProvider("clerk.kiff.dev", "pk_live_xyz")
	req := httptest.NewRequest(http.MethodPost, "https://app.kiff.dev/sign-in/callback",
		strings.NewReader(`{"token":""}`))
	req.Header.Set("Content-Type", "application/json")
	if _, err := p.VerifySignIn(req.Context(), req); err == nil {
		t.Fatalf("expected error on empty token")
	}
}

func TestClerkProvider_VerifySignIn_RejectsMalformedBody(t *testing.T) {
	t.Parallel()
	p, _ := auth.NewClerkProvider("clerk.kiff.dev", "pk_live_xyz")
	req := httptest.NewRequest(http.MethodPost, "https://app.kiff.dev/sign-in/callback",
		strings.NewReader(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	if _, err := p.VerifySignIn(req.Context(), req); err == nil {
		t.Fatalf("expected error on malformed body")
	}
}

func TestClerkProvider_NameIsClerk(t *testing.T) {
	t.Parallel()
	p, _ := auth.NewClerkProvider("clerk.kiff.dev", "pk_live_xyz")
	if p.Name() != "clerk" {
		t.Fatalf("Name() = %q, want clerk", p.Name())
	}
}
