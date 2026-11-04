package auth_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"github.com/kiff/cloud/api/internal/testdb"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
)

// requirePostgres returns a fresh pool for the test or skips the
// test when KIFF_TEST_POSTGRES_URL is unset. Mirrors the helper in
// the tenant package so this file does not depend on test code in
// another package.
func requirePostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping Postgres-backed test (run docker compose up -d first)")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatalf("ApplyCloudMeta: %v", err)
	}
	if err := auth.ApplyAPIKeysSchema(ctx, pool); err != nil {
		t.Fatalf("ApplyAPIKeysSchema: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedTenant inserts a minimal tenant row so api_keys' foreign
// key is satisfied. Idempotent on slug collision (a row may
// already exist from another test on the same pool).
func seedTenant(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	ctx := context.Background()
	store := tenant.NewPostgresStore(pool)
	rec := tenant.Tenant{
		ID:          id,
		Slug:        "slug-" + id,
		DisplayName: "Tenant " + id,
		Status:      tenant.StatusActive,
		CreatedAt:   time.Now().UTC(),
	}
	if err := store.Create(ctx, rec); err != nil {
		// If the row already exists from another test, that's fine
		// for our purposes (we just need the foreign key target).
		if !errors.Is(err, tenant.ErrDuplicateSlug) {
			t.Fatalf("seed tenant: %v", err)
		}
	}
}

// truncateAPIKeys empties cloud_meta.api_keys between tests.
func truncateAPIKeys(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "TRUNCATE cloud_meta.api_keys"); err != nil {
		t.Fatalf("truncate api_keys: %v", err)
	}
}

func TestPostgresAPIKeyStore_Roundtrip(t *testing.T) {
	pool := requirePostgres(t)
	truncateAPIKeys(t, pool)

	tenantID := tenant.NewID()
	seedTenant(t, pool, tenantID)

	store := auth.NewPostgresAPIKeyStore(pool)
	ctx := context.Background()

	hash := sha256.Sum256([]byte("plaintext-" + tenantID))
	rec := auth.APIKeyRecord{
		ID:        "key-" + tenantID,
		Tenant:    tenantID,
		Subject:   "actor-1",
		Label:     "loader",
		Roles:     []string{"operator", "loader.run"},
		KeyHash:   hash,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := store.Insert(ctx, rec); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := store.GetByHash(ctx, rec.KeyHash)
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if got.ID != rec.ID || got.Tenant != rec.Tenant || got.Subject != rec.Subject {
		t.Errorf("retrieved record mismatch: got %+v", got)
	}
	if len(got.Roles) != 2 || got.Roles[0] != "operator" {
		t.Errorf("Roles round-trip failed: got %v", got.Roles)
	}

	// Touch
	at := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.Touch(ctx, rec.ID, at); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got, _ = store.GetByHash(ctx, rec.KeyHash)
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(at) {
		t.Errorf("LastUsedAt: got %v, want %v", got.LastUsedAt, at)
	}

	// Revoke
	revokedAt := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.Revoke(ctx, rec.ID, revokedAt); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := store.GetByHash(ctx, rec.KeyHash); !errors.Is(err, auth.ErrAPIKeyRevoked) {
		t.Errorf("after revoke, GetByHash should return ErrAPIKeyRevoked; got %v", err)
	}
	// Idempotent
	if err := store.Revoke(ctx, rec.ID, revokedAt); err != nil {
		t.Errorf("second Revoke should be idempotent; got %v", err)
	}
}

func TestPostgresAPIKeyStore_DuplicateHash(t *testing.T) {
	pool := requirePostgres(t)
	truncateAPIKeys(t, pool)

	tenantID := tenant.NewID()
	seedTenant(t, pool, tenantID)

	store := auth.NewPostgresAPIKeyStore(pool)
	ctx := context.Background()
	hash := sha256.Sum256([]byte("dup"))

	first := auth.APIKeyRecord{
		ID:        "key-1",
		Tenant:    tenantID,
		Subject:   "actor-1",
		KeyHash:   hash,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.Insert(ctx, first); err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	second := first
	second.ID = "key-2"
	err := store.Insert(ctx, second)
	if !errors.Is(err, auth.ErrAPIKeyDuplicate) {
		t.Fatalf("expected ErrAPIKeyDuplicate on hash collision, got %v", err)
	}
}

func TestPostgresAPIKeyStore_ListActive(t *testing.T) {
	pool := requirePostgres(t)
	truncateAPIKeys(t, pool)

	tenantA := tenant.NewID()
	tenantB := tenant.NewID()
	seedTenant(t, pool, tenantA)
	seedTenant(t, pool, tenantB)

	store := auth.NewPostgresAPIKeyStore(pool)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	for i, tenantID := range []string{tenantA, tenantA, tenantB} {
		hash := sha256.Sum256([]byte(tenantID + ":" + string(rune('a'+i))))
		rec := auth.APIKeyRecord{
			ID:        "k-" + string(rune('a'+i)),
			Tenant:    tenantID,
			Subject:   "actor",
			KeyHash:   hash,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
		}
		if err := store.Insert(ctx, rec); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	// Revoke one of tenant A's keys.
	if err := store.Revoke(ctx, "k-a", now); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	got, err := store.ListActive(ctx, tenantA)
	if err != nil {
		t.Fatalf("ListActive(A): %v", err)
	}
	if len(got) != 1 || got[0].ID != "k-b" {
		t.Errorf("tenant A active list wrong: %+v", got)
	}
	got, err = store.ListActive(ctx, tenantB)
	if err != nil {
		t.Fatalf("ListActive(B): %v", err)
	}
	if len(got) != 1 || got[0].ID != "k-c" {
		t.Errorf("tenant B active list wrong: %+v", got)
	}
}

// A key's expiry is stored and read back by every read (by hash, the
// active list, adding roles); a key minted without one reads as nil.
func TestPostgresAPIKeyStore_Expiry(t *testing.T) {
	pool := requirePostgres(t)
	truncateAPIKeys(t, pool)
	tenantID := tenant.NewID()
	seedTenant(t, pool, tenantID)
	store := auth.NewPostgresAPIKeyStore(pool)
	ctx := context.Background()

	expires := time.Now().UTC().Add(90 * 24 * time.Hour).Truncate(time.Microsecond)
	withExpiry := auth.APIKeyRecord{ID: "key-exp-" + tenantID, Tenant: tenantID, Subject: "owner", Roles: []string{"viewer"},
		KeyHash: sha256.Sum256([]byte("exp-" + tenantID)), CreatedAt: time.Now().UTC().Truncate(time.Microsecond), ExpiresAt: &expires}
	without := auth.APIKeyRecord{ID: "key-none-" + tenantID, Tenant: tenantID, Subject: "owner",
		KeyHash: sha256.Sum256([]byte("none-" + tenantID)), CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	for _, rec := range []auth.APIKeyRecord{withExpiry, without} {
		if err := store.Insert(ctx, rec); err != nil {
			t.Fatalf("Insert %s: %v", rec.ID, err)
		}
	}
	got, err := store.GetByHash(ctx, withExpiry.KeyHash)
	if err != nil || got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Fatalf("GetByHash: %v %v, want %v", got.ExpiresAt, err, expires)
	}
	if got.IsExpired(expires.Add(-time.Second)) || !got.IsExpired(expires) {
		t.Errorf("IsExpired around %v is wrong", expires)
	}
	if got, err := store.GetByHash(ctx, without.KeyHash); err != nil || got.ExpiresAt != nil {
		t.Errorf("a key minted without an expiry: %v %v, want nil", got.ExpiresAt, err)
	}
	recs, err := store.ListActive(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.ID == withExpiry.ID && (r.ExpiresAt == nil || !r.ExpiresAt.Equal(expires)) {
			t.Errorf("ListActive: %v, want %v", r.ExpiresAt, expires)
		}
	}
	added, err := store.AddRoles(ctx, tenantID, withExpiry.ID, []string{"reports"})
	if err != nil || added.ExpiresAt == nil || !added.ExpiresAt.Equal(expires) {
		t.Errorf("AddRoles kept the expiry: %v %v", added.ExpiresAt, err)
	}
}
