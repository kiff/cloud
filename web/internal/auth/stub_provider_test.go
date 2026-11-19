package auth_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/auth"
)

func TestStubProvider_VerifySignIn_HappyPath(t *testing.T) {
	t.Parallel()
	p := auth.NewStubProvider(nil)
	form := url.Values{"subject": {"alice@example.com"}}
	req := httptest.NewRequest("POST", "/sign-in", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	id, err := p.VerifySignIn(context.Background(), req)
	if err != nil {
		t.Fatalf("VerifySignIn: %v", err)
	}
	if id.Subject != "alice@example.com" {
		t.Fatalf("subject = %q", id.Subject)
	}
	if id.Email != "alice@example.com" {
		t.Fatalf("email = %q (expected populated for an @-shaped subject)", id.Email)
	}
	if id.Bearer != "alice@example.com" {
		t.Fatalf("bearer = %q (stub mode mirrors subject)", id.Bearer)
	}
}

func TestStubProvider_VerifySignIn_RejectsEmptySubject(t *testing.T) {
	t.Parallel()
	p := auth.NewStubProvider(nil)
	req := httptest.NewRequest("POST", "/sign-in", strings.NewReader("subject="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := p.VerifySignIn(context.Background(), req); err == nil {
		t.Fatalf("expected error on empty subject")
	}
}

func TestStubProvider_AllowList_RejectsUnknown(t *testing.T) {
	t.Parallel()
	p := auth.NewStubProvider(nil, "alice@example.com")
	form := url.Values{"subject": {"mallory@example.com"}}
	req := httptest.NewRequest("POST", "/sign-in", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := p.VerifySignIn(context.Background(), req); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
}

func TestStubProvider_AllowList_AcceptsListed(t *testing.T) {
	t.Parallel()
	p := auth.NewStubProvider(nil, "alice@example.com")
	form := url.Values{"subject": {"alice@example.com"}}
	req := httptest.NewRequest("POST", "/sign-in", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := p.VerifySignIn(context.Background(), req); err != nil {
		t.Fatalf("listed subject rejected: %v", err)
	}
}

func TestStubProvider_SignInForm_Renders(t *testing.T) {
	t.Parallel()
	p := auth.NewStubProvider(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/sign-in", nil)
	p.SignInForm(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `name="subject"`) {
		t.Fatalf("default form missing subject input; body=%q", body)
	}
	if !strings.Contains(body, "Sign in") {
		t.Fatalf("default form missing button label; body=%q", body)
	}
}

func TestIdentity_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		id   auth.Identity
		ok   bool
	}{
		{"happy", auth.Identity{Subject: "s", Bearer: "b"}, true},
		{"empty subject", auth.Identity{Bearer: "b"}, false},
		{"empty bearer", auth.Identity{Subject: "s"}, false},
		{"whitespace subject", auth.Identity{Subject: "   ", Bearer: "b"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.id.Validate()
			if (err == nil) != tt.ok {
				t.Fatalf("Validate err=%v, want ok=%v", err, tt.ok)
			}
		})
	}
}
