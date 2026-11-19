package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
)

// newDeleteRig sets up a tenant with the given status and mounts
// the AccountHandler behind a fake admin identity.
func newDeleteRig(t *testing.T, status tenant.Status) (http.Handler, *auth.MemoryAPIKeyStore, string) {
	t.Helper()
	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{
		ID:     tenant.NewID(),
		Slug:   "tenant-delete",
		Status: status,
	}
	if err := tenants.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	// Seed an active API key so we can verify it gets revoked.
	keys := auth.NewMemoryAPIKeyStore()
	var hash [auth.APIKeyHashSize]byte
	hash[0] = 0x11
	if err := keys.Insert(context.Background(), auth.APIKeyRecord{
		ID:        "key-delete-1",
		Tenant:    tnt.ID,
		Subject:   "principal-delete",
		Label:     "main",
		KeyHash:   hash,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("insert key: %v", err)
	}

	h := handlers.NewAccountHandler(tenants, keys, receipt.NewMemoryStore())
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{
		Tenant:  tnt.ID,
		Subject: "principal-delete",
		Method:  auth.MethodAPIKey,
		APIKey:  true,
		Roles:   []string{"admin"},
	}))
	return wrapped, keys, tnt.ID
}

// TestDelete_SoftDeletesActiveTenant verifies the happy path:
// an admin can soft-delete an active tenant and the response
// includes grace-window timestamps.
func TestDelete_SoftDeletesActiveTenant(t *testing.T) {
	t.Parallel()
	mux, keys, tenantID := newDeleteRig(t, tenant.StatusActive)

	req := httptest.NewRequest(http.MethodPost, "/v1/me/delete", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["status"] != "deleted_pending" {
		t.Errorf(`status = %q, want "deleted_pending"`, got["status"])
	}
	if got["deleted_at"] == "" || got["grace_window_ends"] == "" {
		t.Errorf("expected deleted_at and grace_window_ends in response, got %v", got)
	}

	// API key for this tenant must be revoked.
	active, err := keys.ListActive(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("expected 0 active keys after soft-delete, got %d", len(active))
	}
}

// TestDelete_IdempotentOnDeletedPending verifies a second call
// returns the existing timestamps rather than resetting the clock.
func TestDelete_IdempotentOnDeletedPending(t *testing.T) {
	t.Parallel()
	mux, _, _ := newDeleteRig(t, tenant.StatusActive)

	// First call.
	req1 := httptest.NewRequest(http.MethodPost, "/v1/me/delete", nil)
	rec1 := httptest.NewRecorder()
	mux.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first delete: status = %d", rec1.Code)
	}
	var first map[string]string
	if err := json.Unmarshal(rec1.Body.Bytes(), &first); err != nil {
		t.Fatalf("unmarshal first: %v", err)
	}

	// Second call — must return the same timestamps, not reset.
	req2 := httptest.NewRequest(http.MethodPost, "/v1/me/delete", nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second delete: status = %d, body = %s", rec2.Code, rec2.Body.String())
	}
	var second map[string]string
	if err := json.Unmarshal(rec2.Body.Bytes(), &second); err != nil {
		t.Fatalf("unmarshal second: %v", err)
	}
	if first["deleted_at"] != second["deleted_at"] {
		t.Errorf("deleted_at changed on second call: %q → %q", first["deleted_at"], second["deleted_at"])
	}
	if first["grace_window_ends"] != second["grace_window_ends"] {
		t.Errorf("grace_window_ends changed on second call: %q → %q", first["grace_window_ends"], second["grace_window_ends"])
	}
}

// TestDelete_RequiresAdminRole verifies a non-admin key gets 403.
func TestDelete_RequiresAdminRole(t *testing.T) {
	t.Parallel()
	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "t-nonop", Status: tenant.StatusActive}
	_ = tenants.Create(context.Background(), tnt)
	h := handlers.NewAccountHandler(tenants, auth.NewMemoryAPIKeyStore(), receipt.NewMemoryStore())
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{
		Tenant:  tnt.ID,
		Subject: "operator-only",
		Method:  auth.MethodAPIKey,
		APIKey:  true,
		Roles:   []string{"operator"}, // not admin
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/me/delete", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
}

// TestDelete_GoneForHardDeleted verifies 410 for an already
// hard-deleted tenant.
func TestDelete_GoneForHardDeleted(t *testing.T) {
	t.Parallel()
	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "t-deleted", Status: tenant.StatusActive}
	_ = tenants.Create(context.Background(), tnt)
	_ = tenants.SetStatus(context.Background(), tnt.ID, tenant.StatusDeleted, nil)
	h := handlers.NewAccountHandler(tenants, auth.NewMemoryAPIKeyStore(), receipt.NewMemoryStore())
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{
		Tenant:  tnt.ID,
		Subject: "ghost",
		Method:  auth.MethodAPIKey,
		APIKey:  true,
		Roles:   []string{"admin"},
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/me/delete", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410; body = %s", rec.Code, rec.Body.String())
	}
}

// TestProposals_BlockedForErasingTenant verifies the decide path
// returns 403 when the tenant store reports the tenant is erasing.
func TestProposals_BlockedForErasingTenant(t *testing.T) {
	t.Parallel()

	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "t-erasing", Status: tenant.StatusActive}
	_ = tenants.Create(context.Background(), tnt)
	// Soft-delete it so IsErasing() returns true.
	_ = tenants.SetStatus(context.Background(), tnt.ID, tenant.StatusDeletedPending, nil)

	h := handlers.NewProposalsHandler(
		nilRuntimeAccessor{},
		handlers.WithProposalsTenantStore(tenants),
	)
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{
		Tenant:  tnt.ID,
		Subject: "ghost",
		Method:  auth.MethodAPIKey,
		APIKey:  true,
		Roles:   []string{"admin"},
	}))

	body := `{"id":"p1","action":{"name":"PAY"},"actor":{"id":"a1","type":"human"},"entity_id":"e1"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/proposals/decide", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for erasing tenant; body = %s", rec.Code, rec.Body.String())
	}
}

// nilRuntimeAccessor is a TenantRuntimeAccessor stub that always
// returns nil. Used when we want the erasing-tenant guard to fire
// before the runtime is reached.
type nilRuntimeAccessor struct{}

func (nilRuntimeAccessor) For(_ context.Context, _ string) (*kiffruntime.Runtime, error) {
	return nil, nil
}

func (nilRuntimeAccessor) ForDomain(_ context.Context, _ string, _ string) (*kiffruntime.Runtime, error) {
	return nil, nil
}

// SpecForDomain declares no run-context rules: this fake exists for the
// erasing-tenant guard, not for the trusted-run-context design.
func (nilRuntimeAccessor) DomainIDFor(_ context.Context, _ string, domainID string) (string, error) {
	return domainID, nil
}

func (nilRuntimeAccessor) SpecForDomain(_ context.Context, _ string, _ string) (domain.Spec, error) {
	return domain.Spec{}, nil
}

// TestDelete_RecordsWhereItCameFrom: a deletion says where it came
// from, which decides whether it can be restored. The tafs app (a
// phone's session, with its device key) erased its chats and
// connections first, so its deletion is "app" and can't be; the
// dashboard's or a key's is "web".
func TestDelete_RecordsWhereItCameFrom(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		id   auth.Identity
		want string
	}{
		{"a key", auth.Identity{Method: auth.MethodAPIKey, APIKey: true, Roles: []string{"admin"}}, tenant.DeletedViaWeb},
		{"the phone", auth.Identity{Method: auth.MethodDeveloper, Roles: []string{"admin"}, DeviceKey: []byte{1}}, tenant.DeletedViaApp},
	} {
		tenants := tenant.NewMemoryStore()
		tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-via", Status: tenant.StatusActive}
		if err := tenants.Create(context.Background(), tnt); err != nil {
			t.Fatal(err)
		}
		h := handlers.NewAccountHandler(tenants, auth.NewMemoryAPIKeyStore(), receipt.NewMemoryStore())
		inner := http.NewServeMux()
		h.Register(inner)
		id := tc.id
		id.Tenant, id.Subject = tnt.ID, "principal-via"
		rec := httptest.NewRecorder()
		withFakeIdentity(inner, id).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/delete", nil))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: status %d, body %s", tc.name, rec.Code, rec.Body.String())
		}
		got, err := tenants.Get(context.Background(), tnt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if via, _ := got.Metadata[tenant.MetadataKeyDeletedVia].(string); via != tc.want {
			t.Errorf("%s: deleted_via = %q, want %q", tc.name, via, tc.want)
		}
	}
}
