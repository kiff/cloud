package auth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
)

func p256(t *testing.T) []byte {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	return der
}

// The approval devices in Postgres: kept with KIFF's id, listed per
// person and account, removed, and a session names its device. A phone that
// signed in before approval devices gets its device at startup, once.
func TestPostgresDeviceStore(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	if err := auth.ApplyDeveloperSessionsSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := auth.ApplyDevicesSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// Twice: the schema is idempotent, foreign key included.
	if err := auth.ApplyDevicesSchema(ctx, pool); err != nil {
		t.Fatalf("second ApplyDevicesSchema: %v", err)
	}
	tenantID := tenant.NewID()
	seedTenant(t, pool, tenantID)
	store := auth.NewPostgresDeviceStore(pool)
	now := time.Now().UTC().Truncate(time.Second)

	key := p256(t)
	ipad := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: tenantID, Subject: "human-1", Name: "tafs on iPad", Platform: "ipados", PublicKey: key, CreatedAt: now}
	if err := store.Insert(ctx, ipad); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := store.Insert(ctx, ipad); !errors.Is(err, auth.ErrDeviceDuplicate) {
		t.Errorf("a second insert: %v", err)
	}
	twin := ipad
	twin.ID = auth.NewDeviceID()
	if err := store.Insert(ctx, twin); !errors.Is(err, auth.ErrDeviceDuplicate) {
		t.Errorf("a second active device with the same key: %v", err)
	}
	if err := store.Relabel(ctx, ipad.ID, "tafs on iPad", "ipados"); err != nil {
		t.Fatal(err)
	}
	if err := store.Relabel(ctx, ipad.ID, "x", "palmos"); err == nil {
		t.Error("relabelled to an unknown platform")
	}
	if err := store.Relabel(ctx, "dev_nope", "x", "ios"); !errors.Is(err, auth.ErrDeviceNotFound) {
		t.Errorf("relabel unknown: %v", err)
	}
	bad := ipad
	bad.ID, bad.Platform = auth.NewDeviceID(), "palmos"
	if err := store.Insert(ctx, bad); err == nil {
		t.Error("an unknown platform was kept")
	}
	got, err := store.Get(ctx, ipad.ID)
	if err != nil || got.Platform != "ipados" || string(got.PublicKey) != string(key) || got.Attestation != "" || got.RevokedAt != nil {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := store.Get(ctx, "dev_nope"); !errors.Is(err, auth.ErrDeviceNotFound) {
		t.Errorf("unknown device: %v", err)
	}
	if err := store.Touch(ctx, ipad.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if list, err := store.ListActive(ctx, tenantID, "human-1"); err != nil || len(list) != 1 || list[0].LastUsedAt == nil {
		t.Fatalf("list = %+v, %v", list, err)
	}
	// At most once a minute.
	if err := store.Touch(ctx, ipad.ID, now.Add(90*time.Second)); err != nil {
		t.Fatal(err)
	}
	if d, _ := store.Get(ctx, ipad.ID); !d.LastUsedAt.Equal(now.Add(time.Minute)) {
		t.Errorf("touched within the minute: last used %v", d.LastUsedAt)
	}
	if found, err := store.FindByKey(ctx, tenantID, "human-1", key); err != nil || found.ID != ipad.ID {
		t.Errorf("find by key = %+v, %v", found, err)
	}
	if _, err := store.FindByKey(ctx, tenantID, "human-2", key); !errors.Is(err, auth.ErrDeviceNotFound) {
		t.Errorf("another person's key: %v", err)
	}

	// A session names its device.
	sessions := auth.NewPostgresDeveloperSessionStore(pool)
	tok, _ := auth.NewDevSessionToken()
	if err := sessions.Insert(ctx, auth.DeveloperSession{ID: "sess-ipad-" + tenantID, Subject: "human-1", Tenant: tenantID, Label: "tafs on iPad",
		TokenHash: tok.Hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour), DevicePublicKey: key, DeviceID: ipad.ID}); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if s, err := sessions.GetByHash(ctx, tok.Hash, now); err != nil || s.DeviceID != ipad.ID {
		t.Fatalf("session's device = %q, %v", s.DeviceID, err)
	}
	// A CLI's session names none.
	cli, _ := auth.NewDevSessionToken()
	if err := sessions.Insert(ctx, auth.DeveloperSession{ID: "sess-cli-" + tenantID, Subject: "human-1", Tenant: tenantID, Label: "cli",
		TokenHash: cli.Hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if s, err := sessions.GetByHash(ctx, cli.Hash, now); err != nil || s.DeviceID != "" {
		t.Fatalf("the CLI's session's device = %q, %v", s.DeviceID, err)
	}

	if err := store.Revoke(ctx, ipad.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, ipad.ID, now.Add(time.Hour)); err != nil {
		t.Errorf("revoke twice: %v", err)
	}
	if d, _ := store.Get(ctx, ipad.ID); d.RevokedAt == nil || !d.RevokedAt.Equal(now) {
		t.Errorf("revoked at = %v; the first revoke counts", d.RevokedAt)
	}
	if list, _ := store.ListActive(ctx, tenantID, "human-1"); len(list) != 0 {
		t.Errorf("a removed device is listed: %+v", list)
	}

	// A phone signed in before approval devices: its key on the session, no device.
	phoneKey := p256(t)
	old, _ := auth.NewDevSessionToken()
	oldID := "sess-old-" + tenantID
	if err := sessions.Insert(ctx, auth.DeveloperSession{ID: oldID, Subject: "human-1", Tenant: tenantID, Label: "tafs on iPhone",
		TokenHash: old.Hash, CreatedAt: now.Add(-24 * time.Hour), ExpiresAt: now.Add(time.Hour), DevicePublicKey: phoneKey}); err != nil {
		t.Fatal(err)
	}
	// The same phone signed in twice before approval devices: one device for both.
	old2, _ := auth.NewDevSessionToken()
	if err := sessions.Insert(ctx, auth.DeveloperSession{ID: "sess-old2-" + tenantID, Subject: "human-1", Tenant: tenantID, Label: "tafs on iPhone",
		TokenHash: old2.Hash, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), DevicePublicKey: phoneKey}); err != nil {
		t.Fatal(err)
	}
	if n, err := auth.LinkLegacyDevices(ctx, pool, now); err != nil || n < 2 {
		t.Fatalf("link: %d, %v", n, err)
	}
	if s1, s2 := mustSession(t, sessions, old.Hash, now), mustSession(t, sessions, old2.Hash, now); s1.DeviceID == "" || s1.DeviceID != s2.DeviceID {
		t.Fatalf("the phone's two sessions name devices %q and %q; want one", s1.DeviceID, s2.DeviceID)
	}
	s, err := sessions.GetByHash(ctx, old.Hash, now)
	if err != nil || s.DeviceID == "" {
		t.Fatalf("the old phone's session after linking = %+v, %v", s, err)
	}
	d, err := store.Get(ctx, s.DeviceID)
	if err != nil || d.Platform != "ios" || d.Name != "tafs on iPhone" || string(d.PublicKey) != string(phoneKey) ||
		d.Tenant != tenantID || d.Subject != "human-1" || !d.CreatedAt.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("the old phone's device = %+v, %v", d, err)
	}
	// Again: nothing more to link for it, and it keeps its device.
	if _, err := auth.LinkLegacyDevices(ctx, pool, now); err != nil {
		t.Fatal(err)
	}
	if again, _ := sessions.GetByHash(ctx, old.Hash, now); again.DeviceID != s.DeviceID {
		t.Errorf("linking again changed the device: %q then %q", s.DeviceID, again.DeviceID)
	}
	if list, _ := store.ListActive(ctx, tenantID, "human-1"); len(list) != 1 || list[0].ID != s.DeviceID {
		t.Errorf("devices after linking twice: %+v", list)
	}
}

func mustSession(t *testing.T, store *auth.PostgresDeveloperSessionStore, hash [32]byte, now time.Time) auth.DeveloperSession {
	t.Helper()
	s, err := store.GetByHash(context.Background(), hash, now)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Duplicates from before the one-device-per-key rule are merged when the
// schema is applied: the oldest stays, the sessions that
// named the others name it, and the others are revoked. Then the rule holds.
func TestPostgresDevicesMergeDuplicates(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	if err := auth.ApplyDeveloperSessionsSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := auth.ApplyDevicesSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	tenantID := tenant.NewID()
	seedTenant(t, pool, tenantID)
	now := time.Now().UTC().Truncate(time.Second)
	key := p256(t)
	// As an earlier bug left them: two active devices for one phone's key.
	if _, err := pool.Exec(ctx, `DROP INDEX IF EXISTS cloud_meta.approval_devices_one_per_key`); err != nil {
		t.Fatal(err)
	}
	older, newer := auth.NewDeviceID(), auth.NewDeviceID()
	for i, id := range []string{older, newer} {
		if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.approval_devices (id, tenant_id, subject, name, platform, public_key, created_at)
VALUES ($1, $2, 'human-1', 'tafs on iPhone', 'ios', $3, $4)`, id, tenantID, key, now.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	sessions := auth.NewPostgresDeveloperSessionStore(pool)
	tok, _ := auth.NewDevSessionToken()
	if err := sessions.Insert(ctx, auth.DeveloperSession{ID: "sess-dup-" + tenantID, Subject: "human-1", Tenant: tenantID, Label: "tafs on iPhone",
		TokenHash: tok.Hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour), DevicePublicKey: key, DeviceID: newer}); err != nil {
		t.Fatal(err)
	}
	if err := auth.ApplyDevicesSchema(ctx, pool); err != nil {
		t.Fatalf("applying the schema over duplicates: %v", err)
	}
	if s := mustSession(t, sessions, tok.Hash, now); s.DeviceID != older {
		t.Fatalf("the session names %q; want the oldest device %q", s.DeviceID, older)
	}
	store := auth.NewPostgresDeviceStore(pool)
	if d, _ := store.Get(ctx, newer); d.RevokedAt == nil {
		t.Error("the duplicate stayed active")
	}
	if list, _ := store.ListActive(ctx, tenantID, "human-1"); len(list) != 1 || list[0].ID != older {
		t.Fatalf("active devices after merging: %+v", list)
	}
	dup := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: tenantID, Subject: "human-1", Name: "x", Platform: "ios", PublicKey: key, CreatedAt: now}
	if err := store.Insert(ctx, dup); !errors.Is(err, auth.ErrDeviceDuplicate) {
		t.Errorf("after merging, a duplicate insert: %v", err)
	}
}
