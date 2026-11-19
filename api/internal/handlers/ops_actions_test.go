package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/operatoraudit"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// recordingResetter records ResetTenant calls so the test can assert
// the runtime cache was evicted on suspend/unsuspend.
type recordingResetter struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingResetter) ResetTenant(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, id)
}

func (r *recordingResetter) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// opsControlFixture builds an OpsHandler with the control plane wired
// and one active tenant. Returns the mux, the tenant store, the audit
// store, the resetter, and the tenant id.
func opsControlFixture(t *testing.T) (*http.ServeMux, *tenant.MemoryStore, *operatoraudit.MemoryStore, *recordingResetter, string) {
	t.Helper()
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	id := tenant.NewID()
	if err := tenants.Create(ctx, tenant.Tenant{
		ID: id, Slug: "acme", DisplayName: "Acme", Status: tenant.StatusActive,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	audit := operatoraudit.NewMemoryStore()
	resetter := &recordingResetter{}
	h := NewOpsHandler([]string{opsSubject}, tenants, usage.NewMemoryStore(), receipt.NewMemoryStore(),
		nil, nil, nil, WithOpsControlPlane(audit, resetter))
	if h == nil {
		t.Fatal("nil handler")
	}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, tenants, audit, resetter, id
}

func doOpsPost(mux *http.ServeMux, path, subject, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if subject != "" {
		req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{Subject: subject, Method: auth.MethodSession}))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestOps_Suspend_FlipsStatusEvictsCacheAndAudits(t *testing.T) {
	t.Parallel()
	mux, tenants, audit, resetter, id := opsControlFixture(t)

	rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/suspend", opsSubject, `{"reason":"abuse: runaway spend"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("suspend: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Status flipped.
	tt, _ := tenants.Get(context.Background(), id)
	if tt.Status != tenant.StatusSuspended {
		t.Fatalf("status: got %s, want suspended", tt.Status)
	}
	// Runtime cache evicted (the gap that made suspension unreliable).
	if calls := resetter.Calls(); len(calls) != 1 || calls[0] != id {
		t.Fatalf("ResetTenant calls = %v, want [%s]", calls, id)
	}
	// Exactly one audit row, with operator + reason + transition.
	entries, _ := audit.List(context.Background(), id, 10)
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Action != operatoraudit.ActionSuspend || e.OperatorSubject != opsSubject || e.Reason != "abuse: runaway spend" {
		t.Fatalf("audit entry wrong: %+v", e)
	}
	if e.Metadata["from"] != "active" || e.Metadata["to"] != "suspended" {
		t.Fatalf("audit metadata wrong: %+v", e.Metadata)
	}
}

func TestOps_Suspend_NonOperator404(t *testing.T) {
	t.Parallel()
	mux, _, audit, _, id := opsControlFixture(t)
	rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/suspend", tenantSubject, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
	if entries, _ := audit.List(context.Background(), "", 10); len(entries) != 0 {
		t.Fatalf("non-operator must not write audit; got %d", len(entries))
	}
}

func TestOps_Suspend_Idempotent(t *testing.T) {
	t.Parallel()
	mux, _, audit, resetter, id := opsControlFixture(t)
	_ = doOpsPost(mux, "/v1/ops/tenants/"+id+"/suspend", opsSubject, "")
	rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/suspend", opsSubject, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "no change") {
		t.Fatalf("second suspend should be no-op 200; got %d body=%s", rec.Code, rec.Body.String())
	}
	// Only the first suspend audited; the no-op writes nothing.
	if entries, _ := audit.List(context.Background(), id, 10); len(entries) != 1 {
		t.Fatalf("audit entries = %d, want 1 (no-op not audited)", len(entries))
	}
	if calls := resetter.Calls(); len(calls) != 1 {
		t.Fatalf("resetter calls = %d, want 1 (no-op does not evict)", len(calls))
	}
}

func TestOps_Unsuspend_OnlyFromSuspended(t *testing.T) {
	t.Parallel()
	mux, tenants, _, _, id := opsControlFixture(t)
	// Active tenant: unsuspend is a no-op 200.
	rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/unsuspend", opsSubject, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unsuspend active: got %d", rec.Code)
	}
	// Suspend then unsuspend → active again.
	_ = doOpsPost(mux, "/v1/ops/tenants/"+id+"/suspend", opsSubject, "")
	rec = doOpsPost(mux, "/v1/ops/tenants/"+id+"/unsuspend", opsSubject, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unsuspend: got %d body=%s", rec.Code, rec.Body.String())
	}
	tt, _ := tenants.Get(context.Background(), id)
	if tt.Status != tenant.StatusActive {
		t.Fatalf("status: got %s, want active", tt.Status)
	}
}

func TestOps_AuditLog(t *testing.T) {
	t.Parallel()
	mux, _, _, _, id := opsControlFixture(t)
	_ = doOpsPost(mux, "/v1/ops/tenants/"+id+"/suspend", opsSubject, `{"reason":"r1"}`)

	req := httptest.NewRequest(http.MethodGet, "/v1/ops/audit", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{Subject: opsSubject, Method: auth.MethodSession}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit log: got %d", rec.Code)
	}
	var out struct {
		Entries []operatoraudit.Entry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Entries) != 1 || out.Entries[0].Action != operatoraudit.ActionSuspend {
		t.Fatalf("audit log entries = %+v", out.Entries)
	}
}

func TestOps_MutationRoutesUnmountedWithoutControlPlane(t *testing.T) {
	t.Parallel()
	// Read-only handler (no WithOpsControlPlane): suspend route is not
	// registered, so it 404s as an unknown path even for an operator.
	mux, _, _ := opsTestFixture(t)
	rec := doOpsPost(mux, "/v1/ops/tenants/whatever/suspend", opsSubject, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 (route not mounted)", rec.Code)
	}
}

// A pending deletion made from the dashboard comes back within its grace
// window, audited. One the tafs app made never does (its chats and
// connections are already erased), and nor does one past its window.
func TestOps_Restore_OnlyAWebDeletionWithinItsWindow(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	for _, tc := range []struct {
		name string
		meta map[string]string
		want int
	}{
		{"from the dashboard", map[string]string{tenant.MetadataKeyDeletedVia: tenant.DeletedViaWeb, tenant.MetadataKeyGraceWindowEnds: future}, http.StatusOK},
		{"from the app", map[string]string{tenant.MetadataKeyDeletedVia: tenant.DeletedViaApp, tenant.MetadataKeyGraceWindowEnds: future}, http.StatusConflict},
		{"an app intent, before deleted_via", map[string]string{"delete_intent": "tafs.delete-account.v2", tenant.MetadataKeyGraceWindowEnds: future}, http.StatusConflict},
		{"past its window", map[string]string{tenant.MetadataKeyDeletedVia: tenant.DeletedViaWeb, tenant.MetadataKeyGraceWindowEnds: past}, http.StatusConflict},
	} {
		mux, tenants, audit, _, id := opsControlFixture(t)
		ctx := context.Background()
		if err := tenants.SetStatus(ctx, id, tenant.StatusDeletedPending, nil); err != nil {
			t.Fatal(err)
		}
		for k, v := range tc.meta {
			if err := tenants.SetMetadata(ctx, id, k, v); err != nil {
				t.Fatal(err)
			}
		}
		rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/restore", opsSubject, `{"reason":"owner asked by email"}`)
		if rec.Code != tc.want {
			t.Errorf("%s: got %d %s, want %d", tc.name, rec.Code, rec.Body.String(), tc.want)
			continue
		}
		got, _ := tenants.Get(ctx, id)
		entries, _ := audit.List(ctx, id, 10)
		if tc.want == http.StatusOK {
			if got.Status != tenant.StatusActive || len(entries) != 1 || entries[0].Action != operatoraudit.ActionRestore {
				t.Errorf("%s: status %s, audit %+v", tc.name, got.Status, entries)
			}
		} else if got.Status != tenant.StatusDeletedPending || len(entries) != 0 {
			t.Errorf("%s: refused, yet status %s, audit %d", tc.name, got.Status, len(entries))
		}
	}
}

// A status change is guarded on the status it was checked against: one
// whose tenant moved on meanwhile (a restore racing the hard-delete
// sweep) changes nothing and writes no audit row.
func TestOps_StatusChangeIsGuardedOnTheStatusRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	id := tenant.NewID()
	if err := tenants.Create(ctx, tenant.Tenant{ID: id, Slug: "acme", Status: tenant.StatusDeleted}); err != nil {
		t.Fatal(err)
	}
	audit := operatoraudit.NewMemoryStore()
	m := newMemOpsMutator(tenants, audit, nil)
	err := m.SetTenantStatus(ctx, id, tenant.StatusDeletedPending, tenant.StatusActive, nil, operatoraudit.Entry{Action: operatoraudit.ActionRestore, TargetTenantID: id})
	if !errors.Is(err, tenant.ErrStatusChanged) {
		t.Fatalf("err = %v, want ErrStatusChanged", err)
	}
	if got, _ := tenants.Get(ctx, id); got.Status != tenant.StatusDeleted {
		t.Errorf("status %s, want still deleted", got.Status)
	}
	if entries, _ := audit.List(ctx, id, 10); len(entries) != 0 {
		t.Errorf("audit rows %d, want 0", len(entries))
	}
}
