package auth_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
)

// checkPasskeys runs the PasskeyStore contract: a credential once per
// account, found as kept, its counter only forward, and removed with its
// device alone.
func checkPasskeys(t *testing.T, store auth.PasskeyStore, tenantID, otherTenant string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	key := p256(t)
	pc := auth.StoredPasskey{Passkey: auth.Passkey{ID: []byte("cred-pc-" + tenantID), Tenant: tenantID, PublicKey: key},
		Subject: "human-1", DeviceID: "dev_pc_" + tenantID, CreatedAt: now}
	if err := store.Insert(ctx, pc); err != nil {
		t.Fatal(err)
	}
	if err := store.Insert(ctx, pc); !errors.Is(err, auth.ErrPasskeyDuplicate) {
		t.Fatalf("the same credential twice: %v", err)
	}
	// The same credential id on another account is that account's own.
	elsewhere := pc
	elsewhere.Tenant, elsewhere.DeviceID = otherTenant, "dev_pc_"+otherTenant
	if err := store.Insert(ctx, elsewhere); err != nil {
		t.Fatalf("the same id on another account: %v", err)
	}
	got, err := store.Find(ctx, tenantID, pc.ID)
	if err != nil || got.Subject != "human-1" || got.DeviceID != pc.DeviceID || !bytes.Equal(got.PublicKey, key) || got.IsRevoked() || got.SignCount != 0 {
		t.Fatalf("found: %+v %v", got, err)
	}
	if _, err := store.Find(ctx, tenantID, []byte("nobody")); !errors.Is(err, auth.ErrPasskeyNotFound) {
		t.Fatalf("an unknown credential: %v", err)
	}
	if got, _ := store.Find(ctx, tenantID, pc.ID); got.Synced != nil {
		t.Fatalf("synced before any assertion: %v", *got.Synced)
	}
	for _, c := range []uint32{5, 3} {
		if err := store.SetSignCount(ctx, tenantID, pc.ID, c, false); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := store.Find(ctx, tenantID, pc.ID); got.SignCount != 5 || got.Synced == nil || *got.Synced {
		t.Fatalf("the counter went backwards, or synced isn't kept: %+v", got)
	}

	// The person's own passkeys, registered for the account: listed, and
	// removed one by one by KIFF's id, only by their person.
	phone := auth.StoredPasskey{Passkey: auth.Passkey{ID: []byte("cred-phone-" + tenantID), Tenant: tenantID, PublicKey: p256(t)},
		Subject: "human-1", KeyID: auth.NewPasskeyID(), Name: "My phone", CreatedAt: now.Add(time.Second)}
	if err := store.Insert(ctx, phone); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSignCount(ctx, tenantID, phone.ID, 0, true); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListForPerson(ctx, tenantID, "human-1")
	if err != nil || len(list) != 2 || list[1].KeyID != phone.KeyID || list[1].Name != "My phone" || list[1].Synced == nil || !*list[1].Synced || list[1].DeviceID != "" {
		t.Fatalf("the person's passkeys: %+v %v", list, err)
	}
	if others, _ := store.ListForPerson(ctx, tenantID, "human-2"); len(others) != 0 {
		t.Fatalf("another person's list: %+v", others)
	}
	if err := store.Revoke(ctx, tenantID, "human-2", phone.KeyID, now); !errors.Is(err, auth.ErrPasskeyNotFound) {
		t.Fatalf("another person removing it: %v", err)
	}
	if err := store.Revoke(ctx, tenantID, "human-1", phone.KeyID, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, tenantID, "human-1", phone.KeyID, now); !errors.Is(err, auth.ErrPasskeyNotFound) {
		t.Fatalf("removing it twice: %v", err)
	}
	if got, _ := store.Find(ctx, tenantID, phone.ID); !got.IsRevoked() {
		t.Fatal("the removed passkey still counts")
	}
	// A computer's own passkey has no key id, so no id removes it on its own:
	// it goes with its device.
	if err := store.Revoke(ctx, tenantID, "human-1", "", now); !errors.Is(err, auth.ErrPasskeyNotFound) {
		t.Fatalf("an empty id removed a passkey: %v", err)
	}
	if got, _ := store.Find(ctx, tenantID, pc.ID); got.IsRevoked() {
		t.Fatal("an empty id removed the computer's own passkey")
	}
	if err := store.RevokeForDevice(ctx, pc.DeviceID, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Find(ctx, tenantID, pc.ID); !got.IsRevoked() {
		t.Fatal("the device's passkey outlived it")
	}
	if got, _ := store.Find(ctx, otherTenant, pc.ID); got.IsRevoked() {
		t.Fatal("another device's passkey was removed")
	}
}

func TestMemoryPasskeys(t *testing.T) {
	checkPasskeys(t, auth.NewMemoryPasskeyStore(), "t_one", "t_two")
}

func TestPostgresPasskeys(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	if err := auth.ApplyPasskeysSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := auth.ApplyPasskeysSchema(ctx, pool); err != nil {
		t.Fatalf("the schema twice: %v", err)
	}
	tenantID, otherTenant := tenant.NewID(), tenant.NewID()
	seedTenant(t, pool, tenantID)
	seedTenant(t, pool, otherTenant)
	checkPasskeys(t, auth.NewPostgresPasskeyStore(pool), tenantID, otherTenant)
}

// A passkey computer's pairing keeps its credential through Postgres.
func TestPostgresPairingKeepsTheCredential(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	for _, apply := range []func(context.Context) error{
		// The devices' schema reads the sessions' (who held an approval key).
		func(ctx context.Context) error { return auth.ApplyDeveloperSessionsSchema(ctx, pool) },
		func(ctx context.Context) error { return auth.ApplyDevicesSchema(ctx, pool) },
		func(ctx context.Context) error { return auth.ApplyPairingsSchema(ctx, pool) },
		func(ctx context.Context) error { return auth.ApplyPairingsSchema(ctx, pool) },
	} {
		if err := apply(ctx); err != nil {
			t.Fatal(err)
		}
	}
	tenantID := tenant.NewID()
	seedTenant(t, pool, tenantID)
	store := auth.NewPostgresPairingStore(pool)
	now := time.Now().UTC().Truncate(time.Second)
	nonce := bytes.Repeat([]byte{7}, 32)
	nonce[0] = byte(now.UnixNano())
	p := auth.Pairing{ID: auth.NewPairingID(), Tenant: tenantID, Subject: "human-1", ApprovedBy: "dev_phone", PublicKey: p256(t),
		Name: "tafs on this Windows PC", Platform: "windows", Kind: "passkey", CredentialID: []byte("cred-1"), CredentialKey: p256(t), DeviceKind: "other",
		KeyStore: auth.KeyStoreSoftware, NonceHash: nonce, Expires: now.Add(5 * time.Minute), CreatedAt: now}
	if err := store.Insert(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := store.ByNonce(ctx, nonce)
	if err != nil || got.Kind != "passkey" || !bytes.Equal(got.CredentialID, p.CredentialID) || !bytes.Equal(got.CredentialKey, p.CredentialKey) || got.ApprovalKey != nil || got.DeviceKind != "other" || got.KeyStore != auth.KeyStoreSoftware {
		t.Fatalf("the pairing as kept: %+v %v", got, err)
	}
}
