package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/tenant"
)

func newBudgetRig(t *testing.T) (http.Handler, tenant.Store, string) {
	t.Helper()
	store := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-budget", Status: tenant.StatusActive}
	if err := store.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create: %v", err)
	}
	inner := http.NewServeMux()
	handlers.NewBillingBudgetHandler(store, nil).Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{Tenant: tnt.ID, Subject: "p", APIKey: true}))
	return wrapped, store, tnt.ID
}

func budgetReq(t *testing.T, mux http.Handler, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, "/v1/me/billing-budget", strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, "/v1/me/billing-budget", nil)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

func TestBillingBudget_SetGetClear(t *testing.T) {
	t.Parallel()
	mux, store, id := newBudgetRig(t)

	// Default: unset → 0.
	rec := budgetReq(t, mux, http.MethodGet, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d", rec.Code)
	}
	var got struct {
		BudgetOps int `json:"budget_ops"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.BudgetOps != 0 {
		t.Errorf("default budget = %d, want 0", got.BudgetOps)
	}

	// Set to 30000.
	rec = budgetReq(t, mux, http.MethodPut, `{"budget_ops":30000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, body=%s", rec.Code, rec.Body.String())
	}
	// Persisted as the alerter's metadata key.
	tt, _ := store.Get(context.Background(), id)
	if tt.Metadata[billing.MetaBudgetOps] != "30000" {
		t.Errorf("stored budget = %v, want \"30000\"", tt.Metadata[billing.MetaBudgetOps])
	}
	// Reads back.
	rec = budgetReq(t, mux, http.MethodGet, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.BudgetOps != 30000 {
		t.Errorf("get after set = %d, want 30000", got.BudgetOps)
	}

	// Clear with 0 → key removed.
	rec = budgetReq(t, mux, http.MethodPut, `{"budget_ops":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d", rec.Code)
	}
	tt, _ = store.Get(context.Background(), id)
	if _, ok := tt.Metadata[billing.MetaBudgetOps]; ok {
		t.Errorf("budget key should be removed on 0; metadata=%v", tt.Metadata)
	}
}

func TestBillingBudget_RejectsNegative(t *testing.T) {
	t.Parallel()
	mux, _, _ := newBudgetRig(t)
	rec := budgetReq(t, mux, http.MethodPut, `{"budget_ops":-5}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for negative budget", rec.Code)
	}
}

func TestBillingBudget_RequiresTenantIdentity(t *testing.T) {
	t.Parallel()
	// No identity middleware → handler sees no tenant.
	inner := http.NewServeMux()
	store := tenant.NewMemoryStore()
	handlers.NewBillingBudgetHandler(store, nil).Register(inner)
	rec := budgetReq(t, inner, http.MethodGet, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without identity", rec.Code)
	}
}
