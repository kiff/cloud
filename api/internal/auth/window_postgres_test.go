package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
)

// The approval windows in Postgres (kiff/tafs#494): one open per device,
// restarted, closed once, ended together by a halt, a sign-out or a
// revoked device, and shared by every API process.
func TestPostgresWindowStore(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	if err := auth.ApplyDeveloperSessionsSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := auth.ApplyDevicesSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := auth.ApplyWindowsSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := auth.ApplyWindowsSchema(ctx, pool); err != nil {
		t.Fatalf("second ApplyWindowsSchema: %v", err)
	}
	tenantID := tenant.NewID()
	seedTenant(t, pool, tenantID)
	devices := auth.NewPostgresDeviceStore(pool)
	now := time.Now().UTC().Truncate(time.Second)
	phone := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: tenantID, Subject: "human-1", Name: "tafs on Pixel", Platform: "android", PublicKey: p256(t), CreatedAt: now}
	other := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: tenantID, Subject: "human-2", Name: "tafs on iPhone", Platform: "ios", PublicKey: p256(t), CreatedAt: now}
	for _, d := range []auth.ApprovalDevice{phone, other} {
		if err := devices.Insert(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	store := auth.NewPostgresWindowStore(pool)
	w := auth.ApprovalWindow{ID: auth.NewWindowID(), Tenant: tenantID, Subject: "human-1", DeviceID: phone.ID, SessionID: "sess-1",
		OpenedAt: now, ExpiresAt: now.Add(auth.WindowLength), CapAt: now.Add(auth.WindowCap), OpenedBy: "3f2a 91c0 7b1e 0d44"}
	if err := store.Open(ctx, w); err != nil {
		t.Fatalf("open: %v", err)
	}
	got, err := store.Get(ctx, w.ID)
	if err != nil || got.ID != w.ID || got.DeviceID != phone.ID || !got.OpenedAt.Equal(now) || !got.CapAt.Equal(w.CapAt) || got.OpenedBy != w.OpenedBy || got.ClosedAt != nil {
		t.Fatalf("read back: %v %+v", err, got)
	}
	if _, err := store.Get(ctx, "win_none"); !errors.Is(err, auth.ErrWindowNotFound) {
		t.Errorf("an unknown window: %v", err)
	}
	if o, err := store.OpenFor(ctx, phone.ID, now.Add(time.Minute)); err != nil || o.ID != w.ID {
		t.Errorf("open for the device: %v %+v", err, o)
	}
	if _, err := store.OpenFor(ctx, phone.ID, now.Add(6*time.Minute)); !errors.Is(err, auth.ErrWindowNotFound) {
		t.Errorf("after expiry: %v", err)
	}
	if _, err := store.OpenFor(ctx, other.ID, now); !errors.Is(err, auth.ErrWindowNotFound) {
		t.Errorf("the other device: %v", err)
	}
	// Restarted at 4 minutes: open at 8; a restart of none is not found.
	if err := store.Restart(ctx, w.ID, now.Add(9*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if o, err := store.OpenFor(ctx, phone.ID, now.Add(8*time.Minute)); err != nil || !o.ExpiresAt.Equal(now.Add(9*time.Minute)) {
		t.Errorf("restarted: %v %v", err, o.ExpiresAt)
	}
	if err := store.Restart(ctx, "win_none", now); !errors.Is(err, auth.ErrWindowNotFound) {
		t.Errorf("restarting none: %v", err)
	}
	// A second window on the device replaces the first.
	w2 := w
	w2.ID, w2.OpenedAt = auth.NewWindowID(), now.Add(time.Minute)
	if err := store.Open(ctx, w2); err != nil {
		t.Fatal(err)
	}
	if old, _ := store.Get(ctx, w.ID); old.ClosedAt == nil || old.ClosedBy != auth.WindowClosedByReplaced {
		t.Errorf("the first after a second: %+v", old)
	}
	// Closed once.
	if err := store.Close(ctx, w2.ID, now.Add(2*time.Minute), auth.WindowClosedByApp); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(ctx, w2.ID, now.Add(3*time.Minute), auth.WindowClosedByHalt); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get(ctx, w2.ID); got.ClosedBy != auth.WindowClosedByApp || !got.ClosedAt.Equal(now.Add(2*time.Minute)) {
		t.Errorf("closed twice: %+v", got)
	}
	if err := store.Close(ctx, "win_none", now, auth.WindowClosedByApp); !errors.Is(err, auth.ErrWindowNotFound) {
		t.Errorf("closing none: %v", err)
	}
	// Ended together: by session, then by tenant.
	w3 := w
	w3.ID, w3.SessionID, w3.OpenedAt = auth.NewWindowID(), "sess-2", now.Add(4*time.Minute)
	w4 := auth.ApprovalWindow{ID: auth.NewWindowID(), Tenant: tenantID, Subject: "human-2", DeviceID: other.ID, SessionID: "sess-3",
		OpenedAt: now.Add(4 * time.Minute), ExpiresAt: now.Add(9 * time.Minute), CapAt: now.Add(19 * time.Minute)}
	for _, x := range []auth.ApprovalWindow{w3, w4} {
		if err := store.Open(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := store.CloseAll(ctx, auth.WindowMatch{Session: "sess-2"}, now.Add(5*time.Minute), auth.WindowClosedBySignOut); err != nil || n != 1 {
		t.Errorf("sign-out closed %d: %v", n, err)
	}
	if n, err := store.CloseAll(ctx, auth.WindowMatch{Tenant: tenantID}, now.Add(5*time.Minute), auth.WindowClosedByHalt); err != nil || n != 1 {
		t.Errorf("the halt closed %d: %v", n, err)
	}
	if got, _ := store.Get(ctx, w4.ID); got.ClosedBy != auth.WindowClosedByHalt {
		t.Errorf("the other device's window after the halt: %+v", got)
	}
}
