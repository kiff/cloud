package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"strings"
	"testing"
	"time"
)

// A proof test key, and a person's own phone key: P-256 SPKI DER, as the stores take them.
var testKey, otherKey = p256DER(), p256DER()

func p256DER() []byte {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		panic(err)
	}
	return der
}

func guardFor(tenants ...string) *TestTenantGuard {
	return NewTestTenantGuard(tenants, func(der []byte) bool { return bytes.Equal(der, testKey) })
}

// A test tenant takes only test keys, whatever path adds one; every other
// tenant takes any key. A refusal names the key's fingerprint and nothing else.
func TestATestTenantTakesOnlyTestKeys(t *testing.T) {
	ctx := context.Background()
	g := guardFor("tnt_eval", " ")
	if err := g.Check(ctx, "tnt_eval", testKey); err != nil {
		t.Fatalf("a test key on the test tenant: %v", err)
	}
	err := g.Check(ctx, "tnt_eval", otherKey)
	var e *TestTenantKeyError
	if !errors.Is(err, ErrTestTenantKey) || !errors.As(err, &e) || e.Fingerprint != DeviceKeyFingerprint(otherKey) {
		t.Fatalf("another key on the test tenant: %v", err)
	}
	if strings.Contains(err.Error(), DeviceKeyFingerprint(testKey)) {
		t.Fatalf("the refusal names a test key: %v", err)
	}
	if err := g.Check(ctx, "tnt_real", otherKey); err != nil {
		t.Fatalf("a key on an account that isn't a test tenant: %v", err)
	}
	if err := g.Check(ctx, "tnt_eval", nil); err != nil {
		t.Fatalf("no key: %v", err)
	}
}

// No listed tenant or no test key holds nothing, as KeyRestriction; a listed
// tenant that isn't KIFF's own is never held, and one whose plan can't be
// read refuses rather than take the key.
func TestTheGuardHoldsOnlyKIFFsOwnListedTenants(t *testing.T) {
	ctx := context.Background()
	if g := NewTestTenantGuard(nil, func([]byte) bool { return true }); g != nil {
		t.Fatal("no tenant listed")
	}
	if g := NewTestTenantGuard([]string{"tnt_eval"}, nil); g != nil {
		t.Fatal("no test key")
	}
	var none *TestTenantGuard
	if err := none.Check(ctx, "tnt_eval", otherKey); err != nil || none.WithOwn(nil) != nil {
		t.Fatalf("a nil guard: %v", err)
	}
	real := guardFor("tnt_eval").WithOwn(func(context.Context, string) (bool, error) { return false, nil })
	if err := real.Check(ctx, "tnt_eval", otherKey); err != nil {
		t.Fatalf("a listed tenant that isn't KIFF's own: %v", err)
	}
	unread := guardFor("tnt_eval").WithOwn(func(context.Context, string) (bool, error) { return false, errors.New("db down") })
	if err := unread.Check(ctx, "tnt_eval", otherKey); err == nil || errors.Is(err, ErrTestTenantKey) {
		t.Fatalf("a plan that can't be read: %v", err)
	}
	if err := unread.Check(ctx, "tnt_eval", testKey); err != nil {
		t.Fatalf("a test key needs no plan read: %v", err)
	}
}

// The guarded stores refuse before anything is kept, and pass every other
// insert, and every other method, through.
func TestTheGuardedStoresKeepNothingRefused(t *testing.T) {
	ctx := context.Background()
	g := guardFor("tnt_eval")
	now := time.Date(2026, 11, 25, 5, 0, 0, 0, time.UTC)

	devices := NewMemoryDeviceStore()
	guarded := GuardDevices(devices, g)
	phone := ApprovalDevice{ID: NewDeviceID(), Tenant: "tnt_eval", Subject: "user_maria", Name: "Maria's phone", Platform: "android",
		Kind: "phone", PublicKey: otherKey, CreatedAt: now}
	if err := guarded.Insert(ctx, phone); !errors.Is(err, ErrTestTenantKey) {
		t.Fatalf("a person's phone on the test tenant: %v", err)
	}
	if got, _ := devices.ListActiveInTenant(ctx, "tnt_eval"); len(got) != 0 {
		t.Fatalf("kept anyway: %v", got)
	}
	test := phone
	test.ID, test.PublicKey = NewDeviceID(), testKey
	if err := guarded.Insert(ctx, test); err != nil {
		t.Fatalf("the test phone: %v", err)
	}
	if got, _ := guarded.ListActiveInTenant(ctx, "tnt_eval"); len(got) != 1 || got[0].ID != test.ID {
		t.Fatalf("reads pass through: %v", got)
	}

	passkeys := NewMemoryPasskeyStore()
	gp := GuardPasskeys(passkeys, g)
	pk := StoredPasskey{Passkey: Passkey{ID: []byte("cred-1"), Tenant: "tnt_eval", PublicKey: otherKey}, Subject: "user_maria", CreatedAt: now}
	if err := gp.Insert(ctx, pk); !errors.Is(err, ErrTestTenantKey) {
		t.Fatalf("a person's passkey on the test tenant: %v", err)
	}
	if got, _ := passkeys.ListActiveInTenant(ctx, "tnt_eval"); len(got) != 0 {
		t.Fatalf("kept anyway: %v", got)
	}
	pk.Tenant = "tnt_real"
	if err := gp.Insert(ctx, pk); err != nil {
		t.Fatalf("a passkey on another account: %v", err)
	}

	if GuardDevices(devices, nil) != DeviceStore(devices) || GuardPasskeys(passkeys, nil) != PasskeyStore(passkeys) {
		t.Fatal("a nil guard wraps nothing")
	}
}

// A session takes only test keys on a test tenant: as it's made (its device
// and approval keys), and when its approval key is set or replaced later,
// on its own account, read from the store.
func TestASessionOnATestTenantTakesOnlyTestKeys(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 5, 0, 0, 0, time.UTC)
	inner := NewMemoryDeveloperSessionStore()
	sessions := GuardSessions(inner, guardFor("tnt_eval"))
	sess := DeveloperSession{ID: "sess-test", Tenant: "tnt_eval", Subject: "user_maria", TokenHash: [32]byte{1},
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), DevicePublicKey: testKey, ApprovalPublicKey: otherKey}
	if err := sessions.Insert(ctx, sess); !errors.Is(err, ErrTestTenantKey) {
		t.Fatalf("a person's approval key at sign-in: %v", err)
	}
	if _, err := inner.TenantOf(ctx, "sess-test"); !errors.Is(err, ErrDevSessionNotFound) {
		t.Fatalf("kept anyway: %v", err)
	}
	sess.ApprovalPublicKey = nil
	if err := sessions.Insert(ctx, sess); err != nil {
		t.Fatalf("the test phone, with no approval key yet: %v", err)
	}
	if _, err := sessions.SetApprovalKey(ctx, "sess-test", otherKey, now); !errors.Is(err, ErrTestTenantKey) {
		t.Fatalf("enrolling a person's approval key: %v", err)
	}
	if _, err := sessions.ReplaceApprovalKey(ctx, "sess-test", otherKey, now); !errors.Is(err, ErrTestTenantKey) {
		t.Fatalf("re-linking to a person's approval key: %v", err)
	}
	if keys, _ := inner.ApprovalKeysInTenant(ctx, "tnt_eval", now); len(keys) != 0 {
		t.Fatalf("an approval key was kept anyway: %d", len(keys))
	}
	// On another account, any key; the session's account is read, never assumed.
	real := sess
	real.ID, real.Tenant, real.TokenHash, real.DevicePublicKey = "sess-real", "tnt_real", [32]byte{2}, otherKey
	if err := sessions.Insert(ctx, real); err != nil {
		t.Fatalf("a session on another account: %v", err)
	}
	if _, err := sessions.SetApprovalKey(ctx, "sess-real", otherKey, now); err != nil {
		t.Fatalf("an approval key on another account: %v", err)
	}
	if _, err := sessions.SetApprovalKey(ctx, "sess-unknown", otherKey, now); !errors.Is(err, ErrDevSessionNotFound) {
		t.Fatalf("an unknown session: %v", err)
	}
	// A store that can't say a session's account refuses the change rather than let a key past.
	blind := GuardSessions(struct{ DeveloperSessionStore }{inner}, guardFor("tnt_eval"))
	if _, err := blind.SetApprovalKey(ctx, "sess-real", testKey, now); err == nil {
		t.Fatal("a store that can't say the account took the key")
	}
}
