package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/proofs"
	"github.com/kiff/cloud/api/internal/tenant"
)

// failingTenants is a tenant store that can't be read.
type failingTenants struct{ tenant.Store }

func (failingTenants) Get(context.Context, string) (tenant.Tenant, error) {
	return tenant.Tenant{}, errors.New("db down")
}

// failingDevices is a device store that can't be listed.
type failingDevices struct{ auth.DeviceStore }

func (failingDevices) ListActiveInTenant(context.Context, string) ([]auth.ApprovalDevice, error) {
	return nil, errors.New("db down")
}

// failingPasskeys is a passkey store that can't be listed.
type failingPasskeys struct{ auth.PasskeyStore }

func (failingPasskeys) ListActiveInTenant(context.Context, string) ([]auth.StoredPasskey, error) {
	return nil, errors.New("db down")
}

// newKey makes a P-256 public key (SPKI DER) and its fingerprint, at run
// time, so no literal here looks like a key to the secret scan.
func newKey(t *testing.T) (der []byte, fingerprint string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err = x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return der, hex.EncodeToString(sum[:])
}

type proofFixture struct {
	stores  proofTenantStores
	keys    proofs.TestKeys
	testKey []byte // the proof test phone's key, listed in keys
}

func newProofFixture(t *testing.T) *proofFixture {
	t.Helper()
	der, fp := newKey(t)
	keys, bad := proofs.ParseTestKeys([]string{fp})
	if len(bad) != 0 {
		t.Fatal(bad)
	}
	return &proofFixture{
		stores:  proofTenantStores{tenants: tenant.NewMemoryStore(), devices: auth.NewMemoryDeviceStore(), passkeys: auth.NewMemoryPasskeyStore()},
		keys:    keys,
		testKey: der,
	}
}

var slugN int

func (f *proofFixture) tenant(t *testing.T, plan string) string {
	t.Helper()
	slugN++
	id := tenant.NewID()
	tn := tenant.Tenant{ID: id, Slug: "proof-t" + strings.Repeat("x", slugN), Metadata: map[string]any{tenant.MetadataKeyPlan: plan}}
	if err := f.stores.tenants.(*tenant.MemoryStore).Create(context.Background(), tn); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *proofFixture) device(t *testing.T, tenantID, subject string, key []byte) {
	t.Helper()
	d := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: tenantID, Subject: subject, Name: "phone",
		Platform: "android", PublicKey: key, CreatedAt: time.Now()}
	if err := f.stores.devices.Insert(context.Background(), d); err != nil {
		t.Fatal(err)
	}
}

func (f *proofFixture) passkey(t *testing.T, tenantID, subject string, key []byte) {
	t.Helper()
	p := auth.StoredPasskey{Passkey: auth.Passkey{ID: []byte(auth.NewPasskeyID()), Tenant: tenantID, PublicKey: key},
		Subject: subject, KeyID: auth.NewPasskeyID(), CreatedAt: time.Now()}
	if err := f.stores.passkeys.Insert(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

func (f *proofFixture) vet(ids ...string) proofTenantVet {
	return vetProofTestTenants(context.Background(), f.stores, f.keys, ids, discardLogger())
}

func refusedWhy(vet proofTenantVet, id string) string {
	for _, r := range vet.refused {
		if r.ID == id {
			return r.Why
		}
	}
	return ""
}

func setProofOIDC(t *testing.T) {
	t.Helper()
	t.Setenv(envProofOIDCAudience, "kiff-proofs")
	t.Setenv(envProofOIDCRepository, "kiff/proof-runner")
	t.Setenv(envProofOIDCRef, "refs/heads/main")
	t.Setenv(envProofOIDCWorkflow, "proofs.yml")
	t.Setenv(envProofOIDCRepositoryID, "1")
	t.Setenv(envProofOIDCOwnerID, "2")
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)) }

// ingestMounts reports whether the ingest mounts with this vetting.
func ingestMounts(vet proofTenantVet) bool {
	deps := routerDeps{proofStore: proofs.NewMemoryStore(), proofTestTenants: vet.own,
		proofTenantsRefused: len(vet.refused) > 0, logger: discardLogger()}
	return buildProofIngest(deps) != nil
}

func TestAnInternalTenantWithOnlyTestKeysIsATestTenant(t *testing.T) {
	setProofOIDC(t)
	f := newProofFixture(t)
	id := f.tenant(t, string(tenant.PlanInternal))
	f.device(t, id, "proof-runner", f.testKey)

	vet := f.vet(id)
	if !vet.vetted || strings.Join(vet.own, ",") != id || len(vet.refused) != 0 {
		t.Fatalf("got %+v, want %s accepted", vet, id)
	}
	if !ingestMounts(vet) {
		t.Error("the ingest didn't mount with KIFF's own test tenant")
	}
}

func TestAnInternalTenantWithNoDevicesIsATestTenant(t *testing.T) {
	f := newProofFixture(t)
	id := f.tenant(t, string(tenant.PlanInternal))
	if vet := f.vet(id); strings.Join(vet.own, ",") != id {
		t.Errorf("got %+v, want %s accepted", vet, id)
	}
}

func TestAnInternalTenantWithAPersonsDeviceIsRefused(t *testing.T) {
	setProofOIDC(t)
	f := newProofFixture(t)
	phone := f.tenant(t, string(tenant.PlanInternal))
	f.device(t, phone, "proof-runner", f.testKey)
	other, _ := newKey(t)
	f.device(t, phone, "the-owner", other)

	computer := f.tenant(t, string(tenant.PlanInternal))
	f.passkey(t, computer, "the-owner", other)

	vet := f.vet(phone, computer)
	if len(vet.own) != 0 {
		t.Errorf("vetted %v, want none", vet.own)
	}
	if refusedWhy(vet, phone) != proofs.RefusedDevices || refusedWhy(vet, computer) != proofs.RefusedDevices {
		t.Errorf("refused %+v, want both for devices that aren't proof test keys", vet.refused)
	}
	if ingestMounts(vet) {
		t.Error("the ingest mounted with a person's device on a test tenant")
	}

	var buf bytes.Buffer
	proofs.LogBoot(slog.New(slog.NewTextHandler(&buf, nil)), proofBootConfig(nil, f.keys, vet))
	if !strings.Contains(buf.String(), "tenant="+phone) || !strings.Contains(buf.String(), "has devices that aren't proof test keys") {
		t.Errorf("want the refusal logged naming the id and why:\n%s", buf.String())
	}
}

func TestWithNoTestKeyListedATenantWithAnyDeviceIsRefused(t *testing.T) {
	f := newProofFixture(t)
	id := f.tenant(t, string(tenant.PlanInternal))
	f.device(t, id, "proof-runner", f.testKey)
	f.keys = proofs.TestKeys{}
	if why := refusedWhy(f.vet(id), id); why != proofs.RefusedDevices {
		t.Errorf("why = %q, want refused for its device", why)
	}
}

func TestANonInternalOrUnknownTestTenantIsRefused(t *testing.T) {
	setProofOIDC(t)
	f := newProofFixture(t)
	own := f.tenant(t, string(tenant.PlanInternal))
	customer := f.tenant(t, "team")
	unknown := tenant.NewID()

	vet := f.vet(own, customer, unknown)
	if strings.Join(vet.own, ",") != own {
		t.Errorf("vetted %v, want only %s", vet.own, own)
	}
	if refusedWhy(vet, customer) != proofs.RefusedNotOwn || refusedWhy(vet, unknown) != proofs.RefusedMissing {
		t.Errorf("refused %+v", vet.refused)
	}
	if ingestMounts(vet) {
		t.Error("the ingest mounted with a test tenant that isn't KIFF's own")
	}
}

func TestAnUnreadableStoreVetsNoTestTenant(t *testing.T) {
	setProofOIDC(t)
	f := newProofFixture(t)
	id := f.tenant(t, string(tenant.PlanInternal))

	for name, stores := range map[string]proofTenantStores{
		"tenants":  {tenants: failingTenants{}, devices: f.stores.devices, passkeys: f.stores.passkeys},
		"devices":  {tenants: f.stores.tenants, devices: failingDevices{}, passkeys: f.stores.passkeys},
		"passkeys": {tenants: f.stores.tenants, devices: f.stores.devices, passkeys: failingPasskeys{}},
	} {
		vet := vetProofTestTenants(context.Background(), stores, f.keys, []string{id}, discardLogger())
		if len(vet.own) != 0 || refusedWhy(vet, id) != proofs.RefusedUnreadable {
			t.Errorf("%s unreadable: got %+v, want nothing vetted", name, vet)
		}
		if ingestMounts(vet) {
			t.Errorf("%s unreadable: the ingest mounted", name)
		}
	}
}

func TestWithNoTenantStoreTheTestTenantsAreKeptAndUnchecked(t *testing.T) {
	vet := vetProofTestTenants(context.Background(), proofTenantStores{}, proofs.TestKeys{}, []string{"t-dev"}, discardLogger())
	if vet.vetted || strings.Join(vet.own, ",") != "t-dev" {
		t.Errorf("got %+v, want t-dev kept, unvetted", vet)
	}
}

func TestTheProofBootLineNeverShowsAToken(t *testing.T) {
	// Built at run time: no literal here looks like a token.
	marks := []string{strings.Repeat("A", 6), strings.Repeat("B", 6), strings.Repeat("C", 6)}
	tokA, tokB := "planque-read-"+marks[0], "planque-read-"+marks[1]
	gh := "ghp" + "_" + marks[2]
	t.Setenv(envProofStateTokens, tokA+", "+tokB)
	t.Setenv(envProofGitHubToken, gh)
	t.Setenv(envProofRunnerVersions, "v1.2.0,v1.3.0")
	t.Setenv(envProofReleaseAnyway, "on")
	_, fp := newKey(t)
	keys, _ := proofs.ParseTestKeys([]string{fp})

	var buf bytes.Buffer
	proofs.LogBoot(slog.New(slog.NewTextHandler(&buf, nil)),
		proofBootConfig(nil, keys, proofTenantVet{own: []string{"t-eval"}, vetted: true}))
	out := buf.String()
	for _, secret := range append([]string{tokA, tokB, gh}, marks...) {
		if strings.Contains(out, secret) {
			t.Errorf("the boot line shows %q:\n%s", secret, out)
		}
	}
	for _, want := range []string{"state_tokens=2", "runner_versions=2", "release_anyway=true",
		"test_key_fingerprints=" + fp[:16], "test_tenant_ids=t-eval"} {
		if !strings.Contains(out, want) {
			t.Errorf("the boot line lacks %q:\n%s", want, out)
		}
	}
}

// A session's approval key signs approvals as a device's key does, so it
// counts too: a test tenant whose devices are all test keys but where a
// session holds a person's security key as its approval key is refused.
func TestAnApprovalKeyThatIsntATestKeyRefusesTheTestTenant(t *testing.T) {
	f := newProofFixture(t)
	sessions := auth.NewMemoryDeveloperSessionStore()
	f.stores.sessions = sessions
	id := f.tenant(t, string(tenant.PlanInternal))
	f.device(t, id, "proof-runner", f.testKey)
	if vet := f.vet(id); strings.Join(vet.own, ",") != id {
		t.Fatalf("before any approval key: %+v", vet)
	}
	other, _ := newKey(t)
	if err := sessions.Insert(context.Background(), auth.DeveloperSession{ID: "sess-1", Tenant: id, Subject: "the-owner",
		TokenHash: [32]byte{1}, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), DevicePublicKey: f.testKey,
		ApprovalPublicKey: other}); err != nil {
		t.Fatal(err)
	}
	if vet := f.vet(id); len(vet.own) != 0 || refusedWhy(vet, id) != proofs.RefusedDevices {
		t.Fatalf("with a person's approval key: %+v, want refused for its keys", vet)
	}
}

// The middleware's rule: a session whose device is revoked signs nothing, so
// its approval key doesn't count, and the tenant stays a test tenant. A live
// device's session with the same key would count.
func TestARevokedDevicesSessionDoesntMakeATestTenantReal(t *testing.T) {
	f := newProofFixture(t)
	sessions := auth.NewMemoryDeveloperSessionStore()
	f.stores.sessions = sessions
	ctx := context.Background()
	id := f.tenant(t, string(tenant.PlanInternal))
	f.device(t, id, "proof-runner", f.testKey)
	ipadKey, _ := newKey(t)
	ipad := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: id, Subject: "the-owner", Name: "tafs on iPad", Platform: "ios",
		PublicKey: ipadKey, CreatedAt: time.Now()}
	if err := f.stores.devices.Insert(ctx, ipad); err != nil {
		t.Fatal(err)
	}
	securityKey, _ := newKey(t)
	if err := sessions.Insert(ctx, auth.DeveloperSession{ID: "sess-ipad", Tenant: id, Subject: "the-owner", TokenHash: [32]byte{3},
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), DevicePublicKey: ipadKey, DeviceID: ipad.ID,
		ApprovalPublicKey: securityKey}); err != nil {
		t.Fatal(err)
	}
	if vet := f.vet(id); len(vet.own) != 0 {
		t.Fatalf("with a person's live iPad and its approval key: %+v, want refused", vet)
	}
	// The iPad revoked, as on 18 Nov, its session left live: the tenant is a test tenant again.
	if err := f.stores.devices.Revoke(ctx, ipad.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if vet := f.vet(id); strings.Join(vet.own, ",") != id {
		t.Fatalf("with the iPad revoked: %+v, want %s accepted", vet, id)
	}
}

// A test tenant that's also pruned (KIFF_PRUNE_TENANTS) is refused at boot:
// a test account's data is never pruned. Its test keys aren't kept to it,
// and the ingest stays unmounted, as for any refused test tenant. Another
// test tenant, not pruned, is still accepted.
func TestATestTenantThatsAlsoPrunedIsRefused(t *testing.T) {
	setProofOIDC(t)
	f := newProofFixture(t)
	pruned := f.tenant(t, string(tenant.PlanInternal))
	kept := f.tenant(t, string(tenant.PlanInternal))
	vet := withoutPruned(f.vet(pruned, kept), []string{"another", pruned}, discardLogger())
	if strings.Join(vet.own, ",") != kept || refusedWhy(vet, pruned) != proofs.RefusedPruned {
		t.Fatalf("got %+v, want %s refused as pruned and %s kept", vet, pruned, kept)
	}
	if ingestMounts(vet) {
		t.Error("the ingest mounted with a test tenant refused")
	}
	if k := proofKeyRestriction(routerDeps{proofTestKeys: f.keys, proofTestTenants: vet.own, logger: discardLogger()}); k.Allows(context.Background(), pruned, f.testKey) {
		t.Error("the test key still acts on the pruned tenant")
	}
	// Nothing pruned: the vetting is as it was.
	if vet := withoutPruned(f.vet(pruned), nil, discardLogger()); strings.Join(vet.own, ",") != pruned || len(vet.refused) != 0 {
		t.Fatalf("with nothing pruned: %+v", vet)
	}
}

// KIFF_PROOF_SHORT_SESSION_TENANTS keeps only tenants that are also kept test
// tenants: one that isn't (unlisted, or refused at boot) gets nothing from
// it. As main wires it, only the kept one's test-key sessions are short; the
// other test tenant's (the CI's) keep the usual lifetime.
func TestShortSessionTenantsMustBeTestTenants(t *testing.T) {
	setProofOIDC(t)
	f := newProofFixture(t)
	pilot := f.tenant(t, string(tenant.PlanInternal))
	ci := f.tenant(t, string(tenant.PlanInternal))
	vet := f.vet(pilot, ci)
	short := shortSessionTenants([]string{pilot, "not-a-test-tenant", f.tenant(t, "solo")}, vet.own, discardLogger())
	if strings.Join(short, ",") != pilot {
		t.Fatalf("kept %v, want only %s", short, pilot)
	}
	k := proofKeyRestriction(routerDeps{proofTestKeys: f.keys, proofTestTenants: vet.own, proofShortSessionTenants: short, logger: discardLogger()})
	const usual = 90 * 24 * time.Hour
	if got := k.SessionLifetime(usual, pilot, f.testKey); got != auth.TestKeySessionLifetime {
		t.Errorf("the pilot's test tenant: %v", got)
	}
	if got := k.SessionLifetime(usual, ci, f.testKey); got != usual {
		t.Errorf("the CI's test tenant: %v", got)
	}
	if got := shortSessionTenants(nil, vet.own, discardLogger()); len(got) != 0 {
		t.Fatalf("unset: %v", got)
	}
}
