package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
)

// exportRig mounts the AccountHandler behind a fake identity for the
// given tenant, with seeded key + receipt stores.
type exportRig struct {
	mux      http.Handler
	tenantID string
	slug     string
	keyID    string
	traceID  string
}

func newExportRig(t *testing.T) *exportRig {
	t.Helper()

	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "tenant-export",
		DisplayName: "Tenant Export",
		Status:      tenant.StatusActive,
	}
	// A recognizable salt; the export must never serialize it.
	salt := make([]byte, tenant.RedactionSaltSize)
	for i := range salt {
		salt[i] = 0xAB
	}
	tnt.RedactionSalt = salt
	if err := tenants.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	keys := auth.NewMemoryAPIKeyStore()
	var hash [auth.APIKeyHashSize]byte
	hash[0] = 0x01
	keyID := "key-export-1"
	if err := keys.Insert(context.Background(), auth.APIKeyRecord{
		ID:        keyID,
		Tenant:    tnt.ID,
		Subject:   "principal-export",
		Label:     "dashboard",
		Roles:     []string{"admin"},
		KeyHash:   hash,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("insert key: %v", err)
	}

	// A key belonging to a *different* tenant — must not leak.
	var otherHash [auth.APIKeyHashSize]byte
	otherHash[0] = 0x02
	if err := keys.Insert(context.Background(), auth.APIKeyRecord{
		ID:        "key-other-tenant",
		Tenant:    "other-tenant",
		Subject:   "principal-other",
		Label:     "other",
		KeyHash:   otherHash,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("insert other key: %v", err)
	}

	receipts := receipt.NewMemoryStore()
	traceID := "trace-export-1"
	if err := receipts.Save(context.Background(), receipt.Receipt{
		SchemaVersion: "kiff.receipt.v1",
		TenantID:      tnt.ID,
		TraceID:       traceID,
		EntityID:      "entity-1",
		IssuedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save receipt: %v", err)
	}
	// A receipt for a different tenant — must not leak.
	if err := receipts.Save(context.Background(), receipt.Receipt{
		SchemaVersion: "kiff.receipt.v1",
		TenantID:      "other-tenant",
		TraceID:       "trace-other",
		EntityID:      "entity-other",
		IssuedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save other receipt: %v", err)
	}

	h := handlers.NewAccountHandler(tenants, keys, receipts)
	inner := http.NewServeMux()
	h.Register(inner)

	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{
		Tenant:  tnt.ID,
		Subject: "principal-export",
		Email:   "owner@example.test",
		Method:  auth.MethodAPIKey,
		APIKey:  true,
		Roles:   []string{"admin"},
	}))

	return &exportRig{mux: wrapped, tenantID: tnt.ID, slug: tnt.Slug, keyID: keyID, traceID: traceID}
}

func TestExport_BundleShapeAndScoping(t *testing.T) {
	t.Parallel()
	rig := newExportRig(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/me/export", nil)
	rec := httptest.NewRecorder()
	rig.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want attachment", cd)
	}

	body := rec.Body.String()

	// The redaction salt must never appear in an export bundle.
	// 0xAB repeated → "abab..." in hex; also guard the field name.
	if strings.Contains(strings.ToLower(body), "redaction") || strings.Contains(strings.ToLower(body), "abababab") {
		t.Fatalf("export leaked redaction salt; body = %s", body)
	}

	var got struct {
		SchemaVersion string `json:"schema_version"`
		Subject       string `json:"subject"`
		Tenant        struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"tenant"`
		APIKeys []struct {
			ID      string `json:"id"`
			KeyHash string `json:"key_hash"`
		} `json:"api_keys"`
		Receipts []struct {
			TraceID string `json:"trace_id"`
		} `json:"receipts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v; body = %s", err, body)
	}

	if got.SchemaVersion != handlers.ExportSchemaVersion {
		t.Errorf("schema_version = %q, want %q", got.SchemaVersion, handlers.ExportSchemaVersion)
	}
	if got.Tenant.ID != rig.tenantID || got.Tenant.Slug != rig.slug {
		t.Errorf("tenant = %+v, want id=%s slug=%s", got.Tenant, rig.tenantID, rig.slug)
	}

	// Exactly the caller's key + receipt; the other tenant's must
	// not appear (store-layer tenant scoping).
	if len(got.APIKeys) != 1 || got.APIKeys[0].ID != rig.keyID {
		t.Fatalf("api_keys = %+v, want exactly %s", got.APIKeys, rig.keyID)
	}
	if got.APIKeys[0].KeyHash == "" {
		t.Errorf("api key hash should be present (hash, not plaintext)")
	}
	if len(got.Receipts) != 1 || got.Receipts[0].TraceID != rig.traceID {
		t.Fatalf("receipts = %+v, want exactly %s", got.Receipts, rig.traceID)
	}
}

// TestExport_MissingTenantUnauthorized: when the identity points at a
// tenant row that does not exist (ErrNotFound), export returns 401 so
// the dashboard treats it as "sign in again" — mirrors MeHandler.
// (A tenant whose row exists but is hard-deleted is covered by
// TestExport_HardDeletedTenantGone.)
func TestExport_MissingTenantUnauthorized(t *testing.T) {
	t.Parallel()
	tenants := tenant.NewMemoryStore()
	h := handlers.NewAccountHandler(tenants, auth.NewMemoryAPIKeyStore(), receipt.NewMemoryStore())
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	// Identity points at a tenant the store doesn't have.
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{
		Tenant:  "ten-missing",
		Subject: "ghost",
		Method:  auth.MethodAPIKey,
		APIKey:  true,
		Roles:   []string{"admin"},
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/me/export", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %s", rec.Code, rec.Body.String())
	}
}

// exportRigWithStatus mounts the AccountHandler for a tenant in the
// given status, so the deleted / deleted_pending paths can be
// exercised. Returns the wrapped mux.
func exportRigWithStatus(t *testing.T, status tenant.Status) http.Handler {
	t.Helper()
	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "tenant-status",
		DisplayName: "Tenant Status",
		Status:      tenant.StatusActive,
	}
	if err := tenants.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if status != tenant.StatusActive {
		if err := tenants.SetStatus(context.Background(), tnt.ID, status, nil); err != nil {
			t.Fatalf("set status %q: %v", status, err)
		}
	}
	h := handlers.NewAccountHandler(tenants, auth.NewMemoryAPIKeyStore(), receipt.NewMemoryStore())
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{
		Tenant:  tnt.ID,
		Subject: "principal-status",
		Method:  auth.MethodAPIKey,
		APIKey:  true,
		Roles:   []string{"admin"},
	}))
	return wrapped
}

// TestExport_HardDeletedTenantGone: a hard-deleted tenant keeps its
// cloud_meta.tenants row but its schema is dropped, so export returns
// 410 Gone rather than a misleadingly empty 200 bundle.
func TestExport_HardDeletedTenantGone(t *testing.T) {
	t.Parallel()
	mux := exportRigWithStatus(t, tenant.StatusDeleted)

	req := httptest.NewRequest(http.MethodGet, "/v1/me/export", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410; body = %s", rec.Code, rec.Body.String())
	}
}

// TestExport_DeletedPendingTenantStillExports: during the grace
// window the tenant's data is retained, so export remains available
// (Articles 15/20 — the owner can pull their data right up to the
// hard scrub).
func TestExport_DeletedPendingTenantStillExports(t *testing.T) {
	t.Parallel()
	mux := exportRigWithStatus(t, tenant.StatusDeletedPending)

	req := httptest.NewRequest(http.MethodGet, "/v1/me/export", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
}

// The Data Act's export carries the customer's own input and what came of
// it: every Card, in force or not, with every version it was issued in,
// and every decision in the Record, past the first page.
func TestExport_CardsWithVersionsAndEveryDecision(t *testing.T) {
	t.Parallel()
	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-cards", DisplayName: "Cards", Status: tenant.StatusActive}
	if err := tenants.Create(context.Background(), tnt); err != nil {
		t.Fatal(err)
	}
	cards := mandate.NewMemoryStore()
	m := mandate.Mandate{ID: "card-refunds", TenantID: tnt.ID, Subject: "refunds", Grants: []mandate.Grant{{Action: "refund"}}}
	if err := cards.Issue(context.Background(), m, "owner"); err != nil {
		t.Fatal(err)
	}
	m.Grants = []mandate.Grant{{Action: "refund"}, {Action: "list_charges"}}
	if err := cards.Issue(context.Background(), m, "owner"); err != nil {
		t.Fatal(err)
	}
	calls := proposals.NewMemoryReader()
	for i := 0; i < proposals.MaxLimit+5; i++ {
		calls.Append(tnt.ID, proposals.DecisionRow{ProposalID: fmt.Sprintf("p-%03d", i), ActionName: "refund", Outcome: "allowed",
			CreatedAt: time.Date(2026, 11, 1, 0, 0, i, 0, time.UTC)})
	}
	h := handlers.NewAccountHandler(tenants, auth.NewMemoryAPIKeyStore(), receipt.NewMemoryStore()).WithExportCards(cards).WithExportDecisions(calls)
	inner := http.NewServeMux()
	h.Register(inner)
	mux := withFakeIdentity(inner, auth.Identity{Tenant: tnt.ID, Subject: "owner", Method: auth.MethodSession, Roles: []string{"admin"}})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/export", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Cards []struct {
			Card     map[string]any   `json:"card"`
			Versions []map[string]any `json:"versions"`
		} `json:"cards"`
		Decisions []map[string]any `json:"decisions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Cards) != 1 || len(got.Cards[0].Versions) != 2 {
		t.Fatalf("cards %+v", got.Cards)
	}
	if len(got.Decisions) != proposals.MaxLimit+5 {
		t.Fatalf("decisions %d, want %d: the export stopped at a page", len(got.Decisions), proposals.MaxLimit+5)
	}
}

// An export cut at its cap says so, which lists and where; a complete one
// says nothing. Not parallel: it lowers the package's cap.
func TestExport_SaysWhenItIsCutShort(t *testing.T) {
	defer handlers.SetExportCapForTest(10)()
	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-cut", DisplayName: "Cut", Status: tenant.StatusActive}
	if err := tenants.Create(context.Background(), tnt); err != nil {
		t.Fatal(err)
	}
	export := func(n int) map[string]any {
		calls := proposals.NewMemoryReader()
		for i := 0; i < n; i++ {
			calls.Append(tnt.ID, proposals.DecisionRow{ProposalID: fmt.Sprintf("p-%03d", i), ActionName: "refund", Outcome: "allowed",
				CreatedAt: time.Date(2026, 11, 1, 0, 0, i, 0, time.UTC)})
		}
		h := handlers.NewAccountHandler(tenants, auth.NewMemoryAPIKeyStore(), receipt.NewMemoryStore()).WithExportDecisions(calls)
		inner := http.NewServeMux()
		h.Register(inner)
		rec := httptest.NewRecorder()
		withFakeIdentity(inner, auth.Identity{Tenant: tnt.ID, Subject: "owner", Method: auth.MethodSession, Roles: []string{"admin"}}).
			ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/export", nil))
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("status %d: %v", rec.Code, err)
		}
		return got
	}
	for _, n := range []int{9, 10} {
		if got := export(n); got["truncated"] != nil || len(got["decisions"].([]any)) != n {
			t.Errorf("%d decisions, cap 10: truncated %v, %d exported", n, got["truncated"], len(got["decisions"].([]any)))
		}
	}
	got := export(11)
	cut, _ := got["truncated"].(map[string]any)
	if cut == nil || cut["decisions"] != true || cut["receipts"] != false || cut["limit"] != float64(10) || len(got["decisions"].([]any)) != 10 {
		t.Fatalf("11 decisions, cap 10: %v, %d exported", got["truncated"], len(got["decisions"].([]any)))
	}
}
