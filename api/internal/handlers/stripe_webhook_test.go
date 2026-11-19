package handlers_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/tenant"
)

const whSecret = "whsec_handler_test"

func sign(payload []byte, secret string, ts time.Time) string {
	tsStr := strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(tsStr))
	mac.Write([]byte("."))
	mac.Write(payload)
	return fmt.Sprintf("t=%s,v1=%s", tsStr, hex.EncodeToString(mac.Sum(nil)))
}

func newWebhookRig(t *testing.T) (*http.ServeMux, tenant.Store, tenant.Tenant) {
	t.Helper()
	store := tenant.NewMemoryStore()
	tt := tenant.Tenant{ID: tenant.NewID(), Slug: "acme", Status: tenant.StatusActive}
	if err := store.Create(context.Background(), tt); err != nil {
		t.Fatalf("create: %v", err)
	}
	proc := billing.NewProcessor(billing.NewMemoryStore(), store, nil)
	mux := http.NewServeMux()
	handlers.NewStripeWebhookHandler(proc, whSecret, nil).Register(mux)
	return mux, store, tt
}

func checkoutJSON(id, plan, slug string) []byte {
	return []byte(fmt.Sprintf(`{
		"id": %q,
		"type": "checkout.session.completed",
		"data": {"object": {
			"customer": "cus_1",
			"subscription": "sub_1",
			"metadata": {"kiff_plan": %q},
			"custom_fields": [{"key": "tenant_slug", "text": {"value": %q}}]
		}}
	}`, id, plan, slug))
}

func TestStripeWebhook_ValidActivates(t *testing.T) {
	t.Parallel()
	mux, store, tt := newWebhookRig(t)
	body := checkoutJSON("evt_1", "solo", "acme")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(string(body)))
	req.Header.Set("Stripe-Signature", sign(body, whSecret, time.Now()))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, _ := store.Get(context.Background(), tt.ID)
	if p := tenant.PlanFor(got); p.Canonical != tenant.PlanSolo {
		t.Errorf("plan = %s, want solo", p.Canonical)
	}
}

func TestStripeWebhook_BadSignatureRejected(t *testing.T) {
	t.Parallel()
	mux, store, tt := newWebhookRig(t)
	body := checkoutJSON("evt_2", "solo", "acme")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(string(body)))
	req.Header.Set("Stripe-Signature", sign(body, "whsec_WRONG", time.Now()))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	// No activation on a rejected signature.
	got, _ := store.Get(context.Background(), tt.ID)
	if p := tenant.PlanFor(got); p.Canonical != tenant.PlanTrial {
		t.Errorf("plan = %s, want trial (no activation on bad sig)", p.Canonical)
	}
}

func TestStripeWebhook_DuplicateDeliveryAcks(t *testing.T) {
	t.Parallel()
	mux, _, _ := newWebhookRig(t)
	body := checkoutJSON("evt_dup", "solo", "acme")
	sig := sign(body, whSecret, time.Now())
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(string(body)))
		req.Header.Set("Stripe-Signature", sig)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("delivery %d status = %d, want 200", i, rec.Code)
		}
	}
}

// An unset secret leaves the route unmounted (nil handler Register is
// a no-op): the path 404s rather than processing unverified posts.
func TestStripeWebhook_UnconfiguredLeavesRouteUnmounted(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemoryStore()
	proc := billing.NewProcessor(billing.NewMemoryStore(), store, nil)
	mux := http.NewServeMux()
	handlers.NewStripeWebhookHandler(proc, "", nil).Register(mux)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (route unmounted)", rec.Code)
	}
}
