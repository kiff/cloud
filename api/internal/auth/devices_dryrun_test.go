package auth_test

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
)

const day = 24 * time.Hour

func seedDevice(t *testing.T, store auth.DeviceStore, tenantID, subject, name, platform string, created time.Time, lastUsed *time.Time) auth.ApprovalDevice {
	t.Helper()
	d := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: tenantID, Subject: subject, Name: name, Platform: platform, PublicKey: p256(t), CreatedAt: created}
	if err := store.Insert(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if lastUsed != nil {
		if err := store.Touch(context.Background(), d.ID, *lastUsed); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func ago(now time.Time, d time.Duration) *time.Time { t := now.Add(-d); return &t }

// The dry run counts, per account, the devices unused for 30 days and the
// older keys of a reinstalled phone, and changes no device.
func TestDeviceCleanupDryRun(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	store := auth.NewMemoryDeviceStore()

	// Account a: one phone reinstalled three times, four keys under one name.
	// The newest is in use; the older three were last used before it signed in.
	a := "ten_a"
	seedDevice(t, store, a, "tafs", "tafs on Android phone", "android", now.Add(-20*day), ago(now, 12*day))
	seedDevice(t, store, a, "tafs", "tafs on Android phone", "android", now.Add(-11*day), ago(now, 11*day))
	seedDevice(t, store, a, "tafs", "tafs on Android phone", "android", now.Add(-10*day), ago(now, 9*day))
	seedDevice(t, store, a, "tafs", "tafs on Android phone", "android", now.Add(-8*day), ago(now, 4*day))

	// Account b: the 30-day boundary, and same-name devices that aren't one phone.
	b := "ten_b"
	seedDevice(t, store, b, "ana", "Work", "ios", now.Add(-60*day), ago(now, 29*day)) // not unused
	seedDevice(t, store, b, "ana", "Old", "ios", now.Add(-60*day), ago(now, 31*day))  // unused
	seedDevice(t, store, b, "ana", "Never", "ios", now.Add(-31*day), nil)             // unused: never used since sign-in
	// Same name, different people or platforms: not superseded.
	seedDevice(t, store, b, "ana", "Phone", "android", now.Add(-5*day), ago(now, 5*day))
	seedDevice(t, store, b, "bo", "Phone", "android", now.Add(-2*day), ago(now, 2*day))
	seedDevice(t, store, b, "ana", "Phone", "ios", now.Add(-1*day), ago(now, 1*day))
	// Same person, platform and name, the older still used after the newer
	// signed in: two phones, not superseded.
	seedDevice(t, store, b, "ana", "iPad", "ipados", now.Add(-9*day), ago(now, 1*day))
	seedDevice(t, store, b, "ana", "iPad", "ipados", now.Add(-3*day), ago(now, 2*day))
	// No name: never matched.
	seedDevice(t, store, b, "ana", "", "windows", now.Add(-9*day), ago(now, 8*day))
	seedDevice(t, store, b, "ana", "", "windows", now.Add(-3*day), ago(now, 2*day))
	// Revoked: not counted at all.
	gone := seedDevice(t, store, b, "ana", "Old", "ios", now.Add(-90*day), nil)
	if err := store.Revoke(ctx, gone.ID, now.Add(-80*day)); err != nil {
		t.Fatal(err)
	}

	before, _ := store.ListAllActive(ctx)
	beforeByID := map[string]auth.ApprovalDevice{}
	for _, d := range before {
		beforeByID[d.ID] = d
	}
	var logs bytes.Buffer
	got, err := auth.DeviceCleanupDryRun(ctx, store, now, auth.DeviceUnusedAfter, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]auth.DeviceCleanupCounts{
		a: {Active: 4, Unused: 0, LikelySuperseded: 3, Either: 3},
		b: {Active: 10, Unused: 2, LikelySuperseded: 0, Either: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("counts:\n got %+v\nwant %+v", got, want)
	}

	// Negative: the dry run changed no device.
	after, _ := store.ListAllActive(ctx)
	if len(after) != len(before) {
		t.Fatalf("active devices: %d before, %d after", len(before), len(after))
	}
	for _, d := range after {
		if !reflect.DeepEqual(d, beforeByID[d.ID]) {
			t.Errorf("device changed:\n%+v\n%+v", beforeByID[d.ID], d)
		}
	}
	if g, _ := store.Get(ctx, gone.ID); !g.IsRevoked() {
		t.Error("the revoked device came back")
	}

	// One line per account and a total; counts and the account id only.
	out := logs.String()
	for _, w := range []string{
		"devices dry run: account", "tenant=ten_a", "likely_superseded=3", "tenant=ten_b", "unused=2",
		"devices dry run: total", "accounts=2", "active=14", "would_end=5", "ending=false",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("logs lack %q:\n%s", w, out)
		}
	}
	for _, leak := range []string{"Android phone", "dev_", "tafs on", "ana"} {
		if strings.Contains(out, leak) {
			t.Errorf("logs carry %q:\n%s", leak, out)
		}
	}
}

// Postgres lists every account's active devices, revoked ones left out.
func TestPostgresListAllActive(t *testing.T) {
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
	store := auth.NewPostgresDeviceStore(pool)
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 4; i++ {
		seedDevice(t, store, tenantID, "tafs", "tafs on Android phone", "android", now.Add(time.Duration(i-10)*day), ago(now, time.Duration(10-i)*day-time.Hour))
	}
	gone := seedDevice(t, store, tenantID, "tafs", "tafs on Android phone", "android", now.Add(-40*day), nil)
	if err := store.Revoke(ctx, gone.ID, now); err != nil {
		t.Fatal(err)
	}
	all, err := store.ListAllActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var mine []auth.ApprovalDevice
	for _, d := range all {
		if d.Tenant == tenantID {
			mine = append(mine, d)
		}
	}
	if len(mine) != 4 {
		t.Fatalf("active devices: %d, want 4", len(mine))
	}
	if c := auth.CountDeviceCleanup(mine, now, auth.DeviceUnusedAfter)[tenantID]; c.LikelySuperseded != 3 || c.Unused != 0 {
		t.Errorf("counts: %+v", c)
	}
}
