package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/tenant"
)

// portalRig mounts the BillingPortalHandler under a synthetic
// identity, with the portal client pointed at a fake Stripe server.
func newPortalRig(t *testing.T, metadata map[string]any, stripe http.HandlerFunc) (http.Handler, string) {
	t.Helper()
	store := tenant.NewMemoryStore()
	tnt := tenant.Tenant{
		ID:       tenant.NewID(),
		Slug:     "tenant-portal",
		Status:   tenant.StatusActive,
		Metadata: metadata,
	}
	if err := store.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	var portal *billing.PortalClient
	if stripe != nil {
		srv := httptest.NewServer(stripe)
		t.Cleanup(srv.Close)
		portal = billing.NewPortalClient("sk_test_x", billing.WithAPIBase(srv.URL), billing.WithHTTPClient(srv.Client()))
	}

	h := handlers.NewBillingPortalHandler(store, portal, "https://app.kiff.dev", nil)
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{
		Tenant: tnt.ID, Subject: "principal-portal", Method: auth.MethodAPIKey, APIKey: true,
	}))
	return wrapped, tnt.ID
}

func postPortal(t *testing.T, mux http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/me/billing-portal", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestBillingPortal_MintsSessionForLinkedCustomer: a tenant with a
// stripe_customer_id gets a 200 + the Stripe portal URL.
func TestBillingPortal_MintsSessionForLinkedCustomer(t *testing.T) {
	t.Parallel()
	var gotForm string
	mux, _ := newPortalRig(t, map[string]any{"stripe_customer_id": "cus_abc"},
		func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			gotForm = string(b)
			_, _ = io.WriteString(w, `{"url":"https://billing.stripe.com/p/session/live_xyz"}`)
		})

	rec := postPortal(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.URL != "https://billing.stripe.com/p/session/live_xyz" {
		t.Errorf("url = %q", body.URL)
	}
	if want := "customer=cus_abc"; !strings.Contains(gotForm, want) {
		t.Errorf("stripe form %q missing %q", gotForm, want)
	}
	if want := "return_url=https%3A%2F%2Fapp.kiff.dev%2Fdashboard%2Fsettings"; !strings.Contains(gotForm, want) {
		t.Errorf("stripe form %q missing return_url to settings", gotForm)
	}
}

// TestBillingPortal_NoCustomerIs404: a tenant that never subscribed
// has no portal — the web gates the action on has_billing, this is
// defence in depth.
func TestBillingPortal_NoCustomerIs404(t *testing.T) {
	t.Parallel()
	mux, _ := newPortalRig(t, nil, func(w http.ResponseWriter, r *http.Request) {
		t.Error("stripe must not be called when no customer is linked")
	})
	rec := postPortal(t, mux)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestBillingPortal_UnmountedWhenNoSecret: with no portal client
// (KIFF_STRIPE_SECRET_KEY unset) the route is never registered, so a
// POST falls through to 404 — the endpoint does not pretend billing
// is wired.
func TestBillingPortal_UnmountedWhenNoSecret(t *testing.T) {
	t.Parallel()
	mux, _ := newPortalRig(t, map[string]any{"stripe_customer_id": "cus_abc"}, nil)
	rec := postPortal(t, mux)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (route unmounted)", rec.Code)
	}
}
