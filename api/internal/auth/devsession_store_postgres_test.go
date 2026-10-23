package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
)

func truncateDevSessions(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "TRUNCATE cloud_meta.developer_sessions"); err != nil {
		t.Fatalf("truncate developer_sessions: %v", err)
	}
}

func TestPostgresDeveloperSessionStore_Roundtrip(t *testing.T) {
	pool := requirePostgres(t)
	if err := auth.ApplyDeveloperSessionsSchema(context.Background(), pool); err != nil {
		t.Fatalf("ApplyDeveloperSessionsSchema: %v", err)
	}
	truncateDevSessions(t, pool)

	tenantID := tenant.NewID()
	seedTenant(t, pool, tenantID)
	store := auth.NewPostgresDeveloperSessionStore(pool)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	gen, err := auth.NewDevSessionToken()
	if err != nil {
		t.Fatalf("NewDevSessionToken: %v", err)
	}
	sess := auth.DeveloperSession{
		ID: "sess-" + tenantID, Subject: "human-1", Tenant: tenantID, Label: "laptop",
		TokenHash: gen.Hash, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	if err := store.Insert(ctx, sess); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Active lookup. A session without a device key reads back without one.
	got, err := store.GetByHash(ctx, gen.Hash, now)
	if err != nil || got.ID != sess.ID || got.Tenant != tenantID || got.Subject != "human-1" || got.DevicePublicKey != nil {
		t.Fatalf("get active: %v (%+v)", err, got)
	}

	// A phone's session keeps its key, in the lookup and the list.
	phoneGen, _ := auth.NewDevSessionToken()
	key := []byte{0x30, 0x59, 0x01, 0x02, 0x03}
	if err := store.Insert(ctx, auth.DeveloperSession{ID: "phone-" + tenantID, Subject: "human-2", Tenant: tenantID, Label: "tafs on iPhone",
		TokenHash: phoneGen.Hash, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), DevicePublicKey: key}); err != nil {
		t.Fatalf("insert phone: %v", err)
	}
	if got, err := store.GetByHash(ctx, phoneGen.Hash, now); err != nil || string(got.DevicePublicKey) != string(key) {
		t.Fatalf("phone session key = %x, %v", got.DevicePublicKey, err)
	}
	if list, err := store.ListActiveForSubject(ctx, "human-2", now); err != nil || len(list) != 1 || string(list[0].DevicePublicKey) != string(key) {
		t.Fatalf("phone session in list = %+v, %v", list, err)
	}

	// Touch + list-active.
	if err := store.Touch(ctx, sess.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("touch: %v", err)
	}
	list, err := store.ListActiveForSubject(ctx, "human-1", now)
	if err != nil || len(list) != 1 {
		t.Fatalf("list active: %v (n=%d)", err, len(list))
	}

	// Expired past TTL.
	if _, err := store.GetByHash(ctx, gen.Hash, now.Add(48*time.Hour)); err != auth.ErrDevSessionExpired {
		t.Errorf("want ErrDevSessionExpired, got %v", err)
	}

	// Per-device revoke via hash (the logout shape).
	if err := store.RevokeByHash(ctx, gen.Hash, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("revoke by hash: %v", err)
	}
	if _, err := store.GetByHash(ctx, gen.Hash, now.Add(3*time.Minute)); err != auth.ErrDevSessionRevoked {
		t.Errorf("want ErrDevSessionRevoked, got %v", err)
	}
	// Revoked drops out of the active list.
	if list, _ := store.ListActiveForSubject(ctx, "human-1", now.Add(3*time.Minute)); len(list) != 0 {
		t.Errorf("revoked session should not be active-listed; got %d", len(list))
	}
	// RevokeByHash is idempotent.
	if err := store.RevokeByHash(ctx, gen.Hash, now.Add(4*time.Minute)); err != nil {
		t.Errorf("second revoke should be idempotent, got %v", err)
	}
}

func TestPostgresDeveloperSessionStore_NotFoundAndDuplicate(t *testing.T) {
	pool := requirePostgres(t)
	if err := auth.ApplyDeveloperSessionsSchema(context.Background(), pool); err != nil {
		t.Fatalf("ApplyDeveloperSessionsSchema: %v", err)
	}
	truncateDevSessions(t, pool)

	tenantID := tenant.NewID()
	seedTenant(t, pool, tenantID)
	store := auth.NewPostgresDeveloperSessionStore(pool)
	ctx := context.Background()
	now := time.Now().UTC()

	gen, _ := auth.NewDevSessionToken()
	if _, err := store.GetByHash(ctx, gen.Hash, now); err != auth.ErrDevSessionNotFound {
		t.Errorf("unknown hash: want ErrDevSessionNotFound, got %v", err)
	}
	sess := auth.DeveloperSession{
		ID: "dup-" + tenantID, Subject: "h", Tenant: tenantID,
		TokenHash: gen.Hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.Insert(ctx, sess); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Insert wraps ErrDevSessionDuplicate, so errors.Is matches.
	if err := store.Insert(ctx, sess); !errors.Is(err, auth.ErrDevSessionDuplicate) {
		t.Errorf("duplicate insert: want ErrDevSessionDuplicate, got %v", err)
	}
}
