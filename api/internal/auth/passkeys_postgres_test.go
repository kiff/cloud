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
	// Every person's on the account, for the proofs' boot check: the
	// computer's own and the person's, never another account's.
	if all, err := store.ListActiveInTenant(ctx, tenantID); err != nil || len(all) != 2 {
		t.Fatalf("the account's passkeys: %+v %v", all, err)
	}
	if all, _ := store.ListActiveInTenant(ctx, otherTenant); len(all) != 1 || all[0].Tenant != otherTenant {
		t.Fatalf("the other account's passkeys: %+v", all)
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
	if all, _ := store.ListActiveInTenant(ctx, tenantID); len(all) != 1 || all[0].KeyID == phone.KeyID {
		t.Fatalf("a removed passkey is still listed for the account: %+v", all)
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

// checkExpiredPairings: an unclaimed pairing a day past its exp goes, on every account; one still
// within the day stays; a claimed one stays however old, since it records which phone let that
// computer in (approved_by).
func checkExpiredPairings(t *testing.T, store interface {
	auth.PairingStore
	DeleteExpired(context.Context, time.Time) (int64, error)
}, tenants ...string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	pairing := func(tenantID string, expires time.Time, i byte) auth.Pairing {
		nonce := bytes.Repeat([]byte{i}, 32)
		nonce[0], nonce[1] = byte(now.UnixNano()), byte(now.UnixNano()>>8)
		return auth.Pairing{ID: auth.NewPairingID(), Tenant: tenantID, Subject: "human-1", ApprovedBy: "dev_phone", PublicKey: p256(t),
			Name: "a computer", Platform: "windows", Kind: "hello", NonceHash: nonce, Expires: expires, CreatedAt: expires.Add(-5 * time.Minute)}
	}
	old, otherAccount, recent := pairing(tenants[0], now.Add(-25*time.Hour), 1), pairing(tenants[1], now.Add(-26*time.Hour), 2), pairing(tenants[0], now.Add(-23*time.Hour), 3)
	claimed := pairing(tenants[0], now.Add(-40*24*time.Hour), 4)
	for _, p := range []auth.Pairing{old, otherAccount, recent, claimed} {
		if err := store.Insert(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Claim(ctx, claimed.ID, claimed.CreatedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	n, err := store.DeleteExpired(ctx, now.Add(-auth.PairingKeptAfterExpiry))
	if err != nil || n < 2 {
		t.Fatalf("deleted %d, %v", n, err)
	}
	for _, p := range []auth.Pairing{old, otherAccount} {
		if _, err := store.ByNonce(ctx, p.NonceHash); !errors.Is(err, auth.ErrPairingNotFound) {
			t.Errorf("an unclaimed pairing a day past its exp is kept (%s): %v", p.Tenant, err)
		}
	}
	if _, err := store.ByNonce(ctx, recent.NonceHash); err != nil {
		t.Errorf("a pairing within its day went: %v", err)
	}
	if got, err := store.ByNonce(ctx, claimed.NonceHash); err != nil || got.ApprovedBy != "dev_phone" {
		t.Errorf("a claimed pairing went, and with it which phone let the computer in: %+v %v", got, err)
	}
}

func TestMemoryExpiredPairingsGo(t *testing.T) {
	checkExpiredPairings(t, auth.NewMemoryPairingStore(), "t_one", "t_two")
}

func TestPostgresExpiredPairingsGo(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	for _, apply := range []func(context.Context) error{
		func(ctx context.Context) error { return auth.ApplyDeveloperSessionsSchema(ctx, pool) },
		func(ctx context.Context) error { return auth.ApplyDevicesSchema(ctx, pool) },
		func(ctx context.Context) error { return auth.ApplyPairingsSchema(ctx, pool) },
	} {
		if err := apply(ctx); err != nil {
			t.Fatal(err)
		}
	}
	one, two := tenant.NewID(), tenant.NewID()
	seedTenant(t, pool, one)
	seedTenant(t, pool, two)
	checkExpiredPairings(t, auth.NewPostgresPairingStore(pool), one, two)
}

// checkPairingRefusals runs the refusals' contract: kept by nonce, the latest
// replacing an earlier one, read only until it expires, another nonce never
// touched, and swept with the expired pairings.
func checkPairingRefusals(t *testing.T, store interface {
	auth.PairingStore
	DeleteExpired(context.Context, time.Time) (int64, error)
}) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	nonce := func(i byte) []byte {
		n := bytes.Repeat([]byte{i}, 32)
		n[0], n[1] = byte(now.UnixNano()), byte(now.UnixNano()>>8)
		return n
	}
	one, other := nonce(11), nonce(12)
	if _, err := store.RefusalByNonce(ctx, one, now); !errors.Is(err, auth.ErrPairingNotRefused) {
		t.Fatalf("before any refusal: %v", err)
	}
	if err := store.Refuse(ctx, auth.PairingRefusal{NonceHash: one, PubHash: bytes.Repeat([]byte{1}, 32), Reason: "invalid_pairing", Expires: now.Add(4 * time.Minute), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	pubHash := bytes.Repeat([]byte{0xab}, 32)
	if err := store.Refuse(ctx, auth.PairingRefusal{NonceHash: one, PubHash: pubHash, Reason: "key_registered", Expires: now.Add(5 * time.Minute), CreatedAt: now}); err != nil {
		t.Fatalf("a second refusal for the same nonce: %v", err)
	}
	if got, err := store.RefusalByNonce(ctx, one, now); err != nil || got.Reason != "key_registered" || !got.Expires.Equal(now.Add(5*time.Minute)) || !bytes.Equal(got.PubHash, pubHash) {
		t.Fatalf("the latest refusal: %+v %v", got, err)
	}
	if _, err := store.RefusalByNonce(ctx, other, now); !errors.Is(err, auth.ErrPairingNotRefused) {
		t.Fatalf("another nonce reads a refusal: %v", err)
	}
	if _, err := store.RefusalByNonce(ctx, one, now.Add(5*time.Minute+time.Second)); !errors.Is(err, auth.ErrPairingNotRefused) {
		t.Fatalf("a refusal past its expiry: %v", err)
	}
	// The sweep takes a refusal that expired before its cut-off, and keeps a later one.
	stale := nonce(13)
	if err := store.Refuse(ctx, auth.PairingRefusal{NonceHash: stale, PubHash: bytes.Repeat([]byte{2}, 32), Reason: "key_registered", Expires: now.Add(-25 * time.Hour), CreatedAt: now.Add(-25 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteExpired(ctx, now.Add(-auth.PairingKeptAfterExpiry)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RefusalByNonce(ctx, stale, now.Add(-26*time.Hour)); !errors.Is(err, auth.ErrPairingNotRefused) {
		t.Fatalf("a refusal a day past its expiry is kept: %v", err)
	}
	if _, err := store.RefusalByNonce(ctx, one, now); err != nil {
		t.Fatalf("a live refusal went with the sweep: %v", err)
	}
}

func TestMemoryPairingRefusals(t *testing.T) {
	checkPairingRefusals(t, auth.NewMemoryPairingStore())
}

func TestPostgresPairingRefusals(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	for _, apply := range []func(context.Context) error{
		func(ctx context.Context) error { return auth.ApplyDeveloperSessionsSchema(ctx, pool) },
		func(ctx context.Context) error { return auth.ApplyDevicesSchema(ctx, pool) },
		func(ctx context.Context) error { return auth.ApplyPairingsSchema(ctx, pool) },
		func(ctx context.Context) error { return auth.ApplyPairingsSchema(ctx, pool) },
	} {
		if err := apply(ctx); err != nil {
			t.Fatal(err)
		}
	}
	checkPairingRefusals(t, auth.NewPostgresPairingStore(pool))
}
