package main

import (
	"context"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
)

// The restriction main builds, over the fixture's stores, with the tenants
// vetted at boot.
func (f *proofFixture) restriction(t *testing.T, stores proofTenantStores, ids ...string) *auth.KeyRestriction {
	t.Helper()
	vet := f.vet(ids...)
	if len(vet.own) != len(ids) {
		t.Fatalf("boot vetted %v of %v: %+v", vet.own, ids, vet.refused)
	}
	return proofKeyRestriction(routerDeps{proofTestKeys: f.keys, proofTestTenants: vet.own,
		tenantStore: stores.tenants, deviceStore: stores.devices, passkeys: stores.passkeys, logger: discardLogger()})
}

// The check once ran only at boot, so a real
// person's device paired on a test tenant after boot left the test phone
// approving holds there until the next deploy. Now the next use refuses it.
func TestATestKeyIsRefusedOnceAPersonsDevicePairsAfterBoot(t *testing.T) {
	ctx := context.Background()
	for _, added := range []string{"device", "passkey"} {
		f := newProofFixture(t)
		id := f.tenant(t, string(tenant.PlanInternal))
		f.device(t, id, "proof-runner", f.testKey)
		k := f.restriction(t, f.stores, id)
		if !k.Allows(ctx, id, f.testKey) {
			t.Fatalf("%s: the test key refused on its vetted tenant before anything changed", added)
		}

		person, _ := newKey(t)
		if added == "device" {
			f.device(t, id, "a-person", person)
		} else {
			f.passkey(t, id, "a-person", person)
		}
		if k.Allows(ctx, id, f.testKey) || k.Allows(ctx, id, nil, f.testKey) {
			t.Errorf("%s: the test key still acts on a tenant a person's %s joined after boot", added, added)
		}
		if !k.Allows(ctx, id, person) {
			t.Errorf("%s: the person's own key was refused; only test keys are kept out", added)
		}
	}
}

// An internal tenant with no devices passes at boot,
// and could gain a real device a minute later.
func TestATestKeyIsRefusedOnceAnEmptyTestTenantGainsARealDevice(t *testing.T) {
	ctx := context.Background()
	f := newProofFixture(t)
	id := f.tenant(t, string(tenant.PlanInternal))
	k := f.restriction(t, f.stores, id)
	if !k.Allows(ctx, id, f.testKey) {
		t.Fatal("the test key refused on an empty vetted tenant")
	}
	person, _ := newKey(t)
	f.device(t, id, "a-person", person)
	if k.Allows(ctx, id, f.testKey) {
		t.Fatal("the test key still acts after a real device joined the empty tenant")
	}
}

// A tenant whose plan changes after boot stops being a test tenant too.
func TestATestKeyIsRefusedOnceItsTenantIsNoLongerInternal(t *testing.T) {
	ctx := context.Background()
	f := newProofFixture(t)
	id := f.tenant(t, string(tenant.PlanInternal))
	k := f.restriction(t, f.stores, id)
	if err := f.stores.tenants.(*tenant.MemoryStore).SetMetadata(ctx, id, tenant.MetadataKeyPlan, "pro"); err != nil {
		t.Fatal(err)
	}
	if k.Allows(ctx, id, f.testKey) {
		t.Fatal("the test key still acts on a tenant that's no longer plan internal")
	}
}

// At use, as at boot, a store that can't be read refuses the test key, and
// a key that isn't a test key never reaches the stores.
func TestAnUnreadableStoreAtUseRefusesTheTestKey(t *testing.T) {
	ctx := context.Background()
	f := newProofFixture(t)
	id := f.tenant(t, string(tenant.PlanInternal))
	for name, stores := range map[string]proofTenantStores{
		"tenants":  {tenants: failingTenants{}, devices: f.stores.devices, passkeys: f.stores.passkeys},
		"devices":  {tenants: f.stores.tenants, devices: failingDevices{}, passkeys: f.stores.passkeys},
		"passkeys": {tenants: f.stores.tenants, devices: f.stores.devices, passkeys: failingPasskeys{}},
	} {
		k := f.restriction(t, stores, id)
		if k.Allows(ctx, id, f.testKey) {
			t.Errorf("%s unreadable at use: the test key acted", name)
		}
		other, _ := newKey(t)
		if !k.Allows(ctx, "a-customer", other) || !k.Allows(ctx, id, other) {
			t.Errorf("%s unreadable at use: a key that isn't a test key was refused", name)
		}
	}
}
