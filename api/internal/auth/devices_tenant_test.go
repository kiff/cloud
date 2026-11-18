package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
)

// Every person's active devices on an account, for the proofs' boot check:
// never another account's, never a removed one.
func checkDevicesInTenant(t *testing.T, store auth.DeviceStore, tenantID, otherTenant string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	mk := func(tn, subject string) auth.ApprovalDevice {
		d := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: tn, Subject: subject, Name: "phone", Platform: "android", PublicKey: p256(t), CreatedAt: now}
		if err := store.Insert(ctx, d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	a, _, gone := mk(tenantID, "human-1"), mk(tenantID, "human-2"), mk(tenantID, "human-3")
	mk(otherTenant, "human-1")
	if err := store.Revoke(ctx, gone.ID, now); err != nil {
		t.Fatal(err)
	}
	all, err := store.ListActiveInTenant(ctx, tenantID)
	if err != nil || len(all) != 2 {
		t.Fatalf("the account's devices: %+v %v", all, err)
	}
	for _, d := range all {
		if d.Tenant != tenantID || d.ID == gone.ID {
			t.Errorf("listed %+v", d)
		}
	}
	if all[0].ID != a.ID && all[1].ID != a.ID {
		t.Errorf("human-1's device missing: %+v", all)
	}
	if none, _ := store.ListActiveInTenant(ctx, tenant.NewID()); len(none) != 0 {
		t.Errorf("an account with no devices: %+v", none)
	}
}

func TestMemoryDevicesInTenant(t *testing.T) {
	checkDevicesInTenant(t, auth.NewMemoryDeviceStore(), "t_one", "t_two")
}

func TestPostgresDevicesInTenant(t *testing.T) {
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
	checkDevicesInTenant(t, auth.NewPostgresDeviceStore(pool), tenantID, otherTenant)
}
