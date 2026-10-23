package auth_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
)

// TestMemoryAPIKeyStore_DomainBinding confirms the optional per-domain
// binding round-trips through the memory store and
// that IsDomainScoped reflects it.
func TestMemoryAPIKeyStore_DomainBinding(t *testing.T) {
	t.Parallel()
	store := auth.NewMemoryAPIKeyStore()
	ctx := context.Background()

	scopedHash := sha256.Sum256([]byte("scoped"))
	wideHash := sha256.Sum256([]byte("wide"))
	now := time.Now().UTC()

	scoped := auth.APIKeyRecord{
		ID: "k-scoped", Tenant: "ten-1", Subject: "svc", DomainID: "refunds",
		KeyHash: scopedHash, CreatedAt: now,
	}
	wide := auth.APIKeyRecord{
		ID: "k-wide", Tenant: "ten-1", Subject: "svc",
		KeyHash: wideHash, CreatedAt: now,
	}
	if err := store.Insert(ctx, scoped); err != nil {
		t.Fatalf("insert scoped: %v", err)
	}
	if err := store.Insert(ctx, wide); err != nil {
		t.Fatalf("insert wide: %v", err)
	}

	gotScoped, err := store.GetByHash(ctx, scopedHash)
	if err != nil {
		t.Fatalf("get scoped: %v", err)
	}
	if gotScoped.DomainID != "refunds" || !gotScoped.IsDomainScoped() {
		t.Errorf("scoped key: DomainID=%q IsDomainScoped=%v, want refunds/true", gotScoped.DomainID, gotScoped.IsDomainScoped())
	}

	gotWide, err := store.GetByHash(ctx, wideHash)
	if err != nil {
		t.Fatalf("get wide: %v", err)
	}
	if gotWide.DomainID != "" || gotWide.IsDomainScoped() {
		t.Errorf("wide key: DomainID=%q IsDomainScoped=%v, want empty/false", gotWide.DomainID, gotWide.IsDomainScoped())
	}

	list, err := store.ListActive(ctx, "ten-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byID := map[string]auth.APIKeyRecord{}
	for _, r := range list {
		byID[r.ID] = r
	}
	if byID["k-scoped"].DomainID != "refunds" {
		t.Errorf("ListActive lost scoped binding: %q", byID["k-scoped"].DomainID)
	}
	if byID["k-wide"].DomainID != "" {
		t.Errorf("ListActive wide binding not empty: %q", byID["k-wide"].DomainID)
	}
}

// TestPostgresAPIKeyStore_DomainBinding confirms the binding column
// round-trips through Postgres (Insert → GetByHash / ListActive).
func TestPostgresAPIKeyStore_DomainBinding(t *testing.T) {
	pool := requirePostgres(t)
	truncateAPIKeys(t, pool)

	tenantID := tenant.NewID()
	seedTenant(t, pool, tenantID)
	store := auth.NewPostgresAPIKeyStore(pool)
	ctx := context.Background()

	scopedHash := sha256.Sum256([]byte("pg-scoped-" + tenantID))
	wideHash := sha256.Sum256([]byte("pg-wide-" + tenantID))
	now := time.Now().UTC().Truncate(time.Microsecond)

	if err := store.Insert(ctx, auth.APIKeyRecord{
		ID: "k-scoped-" + tenantID, Tenant: tenantID, Subject: "svc",
		DomainID: "payouts", KeyHash: scopedHash, CreatedAt: now,
	}); err != nil {
		t.Fatalf("insert scoped: %v", err)
	}
	if err := store.Insert(ctx, auth.APIKeyRecord{
		ID: "k-wide-" + tenantID, Tenant: tenantID, Subject: "svc",
		KeyHash: wideHash, CreatedAt: now,
	}); err != nil {
		t.Fatalf("insert wide: %v", err)
	}

	gotScoped, err := store.GetByHash(ctx, scopedHash)
	if err != nil {
		t.Fatalf("get scoped: %v", err)
	}
	if gotScoped.DomainID != "payouts" || !gotScoped.IsDomainScoped() {
		t.Errorf("scoped: DomainID=%q IsDomainScoped=%v, want payouts/true", gotScoped.DomainID, gotScoped.IsDomainScoped())
	}

	gotWide, err := store.GetByHash(ctx, wideHash)
	if err != nil {
		t.Fatalf("get wide: %v", err)
	}
	if gotWide.DomainID != "" {
		t.Errorf("wide: DomainID=%q, want empty (tenant-wide)", gotWide.DomainID)
	}
}

// TestPostgresAPIKeyStore_AgentBinding confirms the agent binding
// round-trips through Postgres (Insert → GetByHash / ListActive), and
// that an unbound key reads back unbound.
func TestPostgresAPIKeyStore_AgentBinding(t *testing.T) {
	pool := requirePostgres(t)
	truncateAPIKeys(t, pool)

	tenantID := tenant.NewID()
	seedTenant(t, pool, tenantID)
	store := auth.NewPostgresAPIKeyStore(pool)
	ctx := context.Background()

	boundHash := sha256.Sum256([]byte("pg-bound-" + tenantID))
	unboundHash := sha256.Sum256([]byte("pg-unbound-" + tenantID))
	now := time.Now().UTC().Truncate(time.Microsecond)

	if err := store.Insert(ctx, auth.APIKeyRecord{
		ID: "k-bound-" + tenantID, Tenant: tenantID, Subject: "owner",
		AgentID: "refund-agent", KeyHash: boundHash, CreatedAt: now,
	}); err != nil {
		t.Fatalf("insert bound: %v", err)
	}
	if err := store.Insert(ctx, auth.APIKeyRecord{
		ID: "k-unbound-" + tenantID, Tenant: tenantID, Subject: "owner",
		KeyHash: unboundHash, CreatedAt: now,
	}); err != nil {
		t.Fatalf("insert unbound: %v", err)
	}

	gotBound, err := store.GetByHash(ctx, boundHash)
	if err != nil {
		t.Fatalf("get bound: %v", err)
	}
	if gotBound.AgentID != "refund-agent" || !gotBound.IsAgentBound() {
		t.Errorf("bound: AgentID=%q, want refund-agent", gotBound.AgentID)
	}
	gotUnbound, err := store.GetByHash(ctx, unboundHash)
	if err != nil {
		t.Fatalf("get unbound: %v", err)
	}
	if gotUnbound.AgentID != "" {
		t.Errorf("unbound: AgentID=%q, want empty", gotUnbound.AgentID)
	}

	list, err := store.ListActive(ctx, tenantID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range list {
		if r.ID == "k-bound-"+tenantID && r.AgentID != "refund-agent" {
			t.Errorf("ListActive lost the binding: %q", r.AgentID)
		}
	}
}

// The environment binding round-trips through Postgres.
func TestPostgresAPIKeyStore_EnvironmentBinding(t *testing.T) {
	pool := requirePostgres(t)
	truncateAPIKeys(t, pool)

	tenantID := tenant.NewID()
	seedTenant(t, pool, tenantID)
	store := auth.NewPostgresAPIKeyStore(pool)
	ctx := context.Background()
	hash := sha256.Sum256([]byte("pg-env-" + tenantID))
	if err := store.Insert(ctx, auth.APIKeyRecord{
		ID: "k-env-" + tenantID, Tenant: tenantID, Subject: "owner",
		Environment: "production", KeyHash: hash, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := store.GetByHash(ctx, hash)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Environment != "production" || !got.IsEnvironmentBound() {
		t.Errorf("Environment = %q, want production", got.Environment)
	}
	list, err := store.ListActive(ctx, tenantID)
	if err != nil || len(list) != 1 || list[0].Environment != "production" {
		t.Errorf("list = %+v (err %v), want the production binding", list, err)
	}
}

// AddRoles appends only missing roles, in order, and only to an
// active key of the tenant.
func TestPostgresAPIKeyStore_AddRoles(t *testing.T) {
	pool := requirePostgres(t)
	truncateAPIKeys(t, pool)
	tenantID, otherTenant := tenant.NewID(), tenant.NewID()
	seedTenant(t, pool, tenantID)
	seedTenant(t, pool, otherTenant)
	store := auth.NewPostgresAPIKeyStore(pool)
	ctx := context.Background()
	for _, k := range []struct{ id, tenant string }{{"k-roles-" + tenantID, tenantID}, {"k-revoked-" + tenantID, tenantID}} {
		if err := store.Insert(ctx, auth.APIKeyRecord{
			ID: k.id, Tenant: k.tenant, Subject: "owner", Roles: []string{"ops_agent"}, AgentID: "refund-agent",
			KeyHash: sha256.Sum256([]byte(k.id)), CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("insert %s: %v", k.id, err)
		}
	}
	if err := store.Revoke(ctx, "k-revoked-"+tenantID, time.Now()); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	got, err := store.AddRoles(ctx, tenantID, "k-roles-"+tenantID, []string{"support", "ops_agent", "billing", "support"})
	if err != nil {
		t.Fatalf("AddRoles: %v", err)
	}
	if strings.Join(got.Roles, ",") != "ops_agent,support,billing" || got.AgentID != "refund-agent" {
		t.Errorf("after AddRoles: roles %v agent %q, want [ops_agent support billing] refund-agent", got.Roles, got.AgentID)
	}
	again, err := store.AddRoles(ctx, tenantID, "k-roles-"+tenantID, []string{"support"})
	if err != nil || strings.Join(again.Roles, ",") != "ops_agent,support,billing" {
		t.Errorf("adding a held role: roles %v err %v, want unchanged", again.Roles, err)
	}
	for _, c := range []struct{ tenant, id string }{
		{otherTenant, "k-roles-" + tenantID},
		{tenantID, "k-revoked-" + tenantID},
		{tenantID, "k-missing"},
	} {
		if _, err := store.AddRoles(ctx, c.tenant, c.id, []string{"support"}); !errors.Is(err, auth.ErrAPIKeyNotFound) {
			t.Errorf("AddRoles(%s, %s): err %v, want ErrAPIKeyNotFound", c.tenant, c.id, err)
		}
	}
}
