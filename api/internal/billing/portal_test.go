package billing_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/billing"
)

func TestNewPortalClient_NilWhenNoSecret(t *testing.T) {
	t.Parallel()
	if billing.NewPortalClient("") != nil {
		t.Fatal("empty secret should yield a nil client (route stays unmounted)")
	}
	if billing.NewPortalClient("   ") != nil {
		t.Fatal("whitespace secret should yield a nil client")
	}
	if billing.NewPortalClient("sk_test_x") == nil {
		t.Fatal("non-empty secret should yield a client")
	}
}

func TestPortalClient_CreateSession_Success(t *testing.T) {
	t.Parallel()

	var gotAuth, gotForm string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/billing_portal/sessions" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotForm = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"bps_1","url":"https://billing.stripe.com/p/session/test_abc"}`)
	}))
	defer srv.Close()

	c := billing.NewPortalClient("sk_test_secret", billing.WithAPIBase(srv.URL), billing.WithHTTPClient(srv.Client()))
	got, err := c.CreateSession(context.Background(), "cus_123", "https://app.kiff.dev/dashboard/settings")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if got != "https://billing.stripe.com/p/session/test_abc" {
		t.Errorf("url = %q", got)
	}
	if gotAuth != "Bearer sk_test_secret" {
		t.Errorf("auth header = %q, want bearer secret", gotAuth)
	}
	form, _ := url.ParseQuery(gotForm)
	if form.Get("customer") != "cus_123" {
		t.Errorf("customer = %q", form.Get("customer"))
	}
	if form.Get("return_url") != "https://app.kiff.dev/dashboard/settings" {
		t.Errorf("return_url = %q", form.Get("return_url"))
	}
}

func TestPortalClient_CreateSession_StripeError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"No such customer: cus_x","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()

	c := billing.NewPortalClient("sk_test_secret", billing.WithAPIBase(srv.URL), billing.WithHTTPClient(srv.Client()))
	_, err := c.CreateSession(context.Background(), "cus_x", "https://app.kiff.dev/dashboard/settings")
	if err == nil {
		t.Fatal("expected error on Stripe 400")
	}
	if !strings.Contains(err.Error(), "No such customer") {
		t.Errorf("error should surface the Stripe message, got %v", err)
	}
}

func TestPortalClient_CreateSession_EmptyCustomer(t *testing.T) {
	t.Parallel()
	c := billing.NewPortalClient("sk_test_secret")
	if _, err := c.CreateSession(context.Background(), "", "https://app.kiff.dev"); err == nil {
		t.Fatal("expected error for empty customer id")
	}
}
