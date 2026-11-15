package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/pgschema"
	"github.com/kiff/cloud/api/internal/tenant"
)

// checkApprovalDevices: a person has an approval device while one of their
// devices on the account holds an approval key, and for 30 days after it's
// removed (the desktop client contract, §1.1). Another person's, or another
// account's, doesn't count.
func checkApprovalDevices(t *testing.T, store auth.DeviceStore, tenantID, otherTenant string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	since := now.Add(-auth.ApprovalDeviceKept)
	has := func(tenantID, subject string) bool {
		t.Helper()
		ok, err := store.HasApprovalDevice(ctx, tenantID, subject, since)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	insert := func(tenantID, subject string, approval bool) auth.ApprovalDevice {
		t.Helper()
		d := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: tenantID, Subject: subject, Name: "phone", Platform: "ios",
			PublicKey: p256(t), HasApprovalKey: approval, CreatedAt: now}
		if err := store.Insert(ctx, d); err != nil {
			t.Fatal(err)
		}
		return d
	}

	if has(tenantID, "human-1") {
		t.Fatal("no devices: has an approval device")
	}
	viewOnly := insert(tenantID, "human-1", false)
	if has(tenantID, "human-1") {
		t.Fatal("a device without an approval key counts")
	}
	insert(tenantID, "human-2", true)
	insert(otherTenant, "human-1", true)
	if has(tenantID, "human-1") {
		t.Fatal("another person's, or another account's, approval device counts")
	}
	if err := store.MarkApprovalKey(ctx, viewOnly.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(ctx, viewOnly.ID); err != nil || !got.HasApprovalKey || !has(tenantID, "human-1") {
		t.Fatalf("a device marked as holding an approval key: %+v %v", got, err)
	}
	// Removed 29 days ago it still counts; 31 days ago it doesn't.
	if err := store.Revoke(ctx, viewOnly.ID, now.Add(-29*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !has(tenantID, "human-1") {
		t.Fatal("removed 29 days ago: no longer counts")
	}
	old := insert(tenantID, "human-3", true)
	if err := store.Revoke(ctx, old.ID, now.Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if has(tenantID, "human-3") {
		t.Fatal("removed 31 days ago: still counts")
	}
	if err := store.MarkApprovalKey(ctx, "dev_nope"); err == nil {
		t.Fatal("marked an unknown device")
	}
}

func TestMemoryHasApprovalDevice(t *testing.T) {
	t.Parallel()
	checkApprovalDevices(t, auth.NewMemoryDeviceStore(), "tenant-1", "tenant-2")
}

// The same in Postgres, with the session's approval mode and the device's
// key store kept, and devices kept before has_approval_key taking it from
// their sessions when the schema runs.
func TestPostgresHasApprovalDevice(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	if err := auth.ApplyDeveloperSessionsSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := auth.ApplyDevicesSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	tenantID, otherTenant := tenant.NewID(), tenant.NewID()
	seedTenant(t, pool, tenantID)
	seedTenant(t, pool, otherTenant)
	devices := auth.NewPostgresDeviceStore(pool)
	checkApprovalDevices(t, devices, tenantID, otherTenant)

	now := time.Now().UTC().Truncate(time.Second)
	d := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: tenantID, Subject: "human-4", Name: "mac", Platform: "macos",
		PublicKey: p256(t), KeyStore: auth.KeyStoreSoftware, CreatedAt: now}
	if err := devices.Insert(ctx, d); err != nil {
		t.Fatal(err)
	}
	if got, err := devices.Get(ctx, d.ID); err != nil || got.KeyStore != auth.KeyStoreSoftware || got.HasApprovalKey {
		t.Fatalf("the device's key store: %+v %v", got, err)
	}
	sessions := auth.NewPostgresDeveloperSessionStore(pool)
	hash := sha256.Sum256([]byte("approval-mode-" + tenantID))
	if err := sessions.Insert(ctx, auth.DeveloperSession{ID: "sess-" + tenantID, Tenant: tenantID, Subject: "human-4", TokenHash: hash,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), DevicePublicKey: d.PublicKey, DeviceID: d.ID, ApprovalPublicKey: p256(t),
		ApprovalMode: auth.ApprovalModeKey}); err != nil {
		t.Fatal(err)
	}
	if got, err := sessions.GetByHash(ctx, hash, now); err != nil || got.ApprovalMode != auth.ApprovalModeKey {
		t.Fatalf("the session's approval mode: %q %v", got.ApprovalMode, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.developer_sessions (id, tenant_id, subject, token_hash, expires_at, approval_mode)
VALUES ($1, $2, 'human-4', $3, $4, 'approve')`, "sess-bad-"+tenantID, tenantID, hex.EncodeToString([]byte("bad-mode-"+tenantID)), now.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "23514") {
		t.Fatalf("an unknown approval mode: %v", err)
	}
	// The device was kept before has_approval_key: the schema gives it one
	// from its session's approval key.
	if has, err := devices.HasApprovalDevice(ctx, tenantID, "human-4", now.Add(-auth.ApprovalDeviceKept)); err != nil || has {
		t.Fatalf("before the schema runs again: %v %v", has, err)
	}
	if err := pgschema.Forget(ctx, pool, "auth/approval_devices"); err != nil {
		t.Fatal(err)
	}
	if err := auth.ApplyDevicesSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if has, err := devices.HasApprovalDevice(ctx, tenantID, "human-4", now.Add(-auth.ApprovalDeviceKept)); err != nil || !has {
		t.Fatalf("after the schema runs again: %v %v", has, err)
	}
}
