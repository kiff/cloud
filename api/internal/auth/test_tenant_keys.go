package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A proof test tenant (KIFF_PROOF_TEST_TENANTS) is a test account only while
// every device and passkey on it is a proof test key (KIFF_PROOF_TEST_KEYS):
// proofUseCheck refuses its test keys from the moment another key is there.
// TestTenantGuard keeps that from happening: a key that isn't a test key
// never joins a test tenant, by any path. It sits on the two stores that hold
// a tenant's keys (GuardDevices, GuardPasskeys) and on the sessions' approval
// keys (GuardSessions), so a device sign-in, a pairing, a view-only computer,
// a passkey registration and an approval key's enrolment or re-link all pass
// through it, and so does any path added later.
//
// The rule reads only the key being added, never the tenant's other keys, so
// two adds at once can't race past it: each is checked on its own.

// ErrTestTenantKey is a key refused on a proof test tenant because it isn't
// one of the test keys. TestTenantKeyError carries the key's fingerprint.
var ErrTestTenantKey = errors.New("auth: this account is a test account: add the key to the test keys first")

// TestTenantKeyError is ErrTestTenantKey for one key: its fingerprint
// (DeviceKeyFingerprint), so the owner can add it. It names no other key.
type TestTenantKeyError struct {
	Fingerprint string
}

func (e *TestTenantKeyError) Error() string {
	return fmt.Sprintf("%s (key %s)", ErrTestTenantKey.Error(), e.Fingerprint)
}

func (e *TestTenantKeyError) Is(target error) bool { return target == ErrTestTenantKey }

// TestTenantGuard says whether a key may join a tenant. A nil guard allows
// every key.
type TestTenantGuard struct {
	tenants   map[string]bool
	isTestKey func(der []byte) bool
	// own reports whether a listed tenant is KIFF's own account (plan
	// internal), as the boot check requires: a real account listed by mistake
	// is never held to test keys. Nil: every listed tenant is.
	own func(ctx context.Context, tenant string) (bool, error)
}

// NewTestTenantGuard holds tenants to the keys isTestKey reports. Nil when no
// tenant is listed or no test key is (isTestKey nil): then no tenant is a
// test tenant, as KeyRestriction has it.
func NewTestTenantGuard(tenants []string, isTestKey func(der []byte) bool) *TestTenantGuard {
	if isTestKey == nil {
		return nil
	}
	g := &TestTenantGuard{tenants: map[string]bool{}, isTestKey: isTestKey}
	for _, t := range tenants {
		if t = strings.TrimSpace(t); t != "" {
			g.tenants[t] = true
		}
	}
	if len(g.tenants) == 0 {
		return nil
	}
	return g
}

// WithOwn sets the check that a listed tenant is KIFF's own account. It
// returns g; on a nil g it does nothing.
func (g *TestTenantGuard) WithOwn(own func(ctx context.Context, tenant string) (bool, error)) *TestTenantGuard {
	if g != nil {
		g.own = own
	}
	return g
}

// Check refuses key on tenant with a *TestTenantKeyError when tenant is a
// listed test tenant of KIFF's own and key isn't a test key. A listed tenant
// whose plan can't be read refuses too: the add waits rather than turn a test
// account into a real one.
func (g *TestTenantGuard) Check(ctx context.Context, tenant string, key []byte) error {
	if g == nil || !g.tenants[tenant] || len(key) == 0 || g.isTestKey(key) {
		return nil
	}
	if g.own != nil {
		own, err := g.own(ctx, tenant)
		if err != nil {
			return fmt.Errorf("auth: the test tenant's plan couldn't be read: %w", err)
		}
		if !own {
			return nil
		}
	}
	return &TestTenantKeyError{Fingerprint: DeviceKeyFingerprint(key)}
}

// GuardDevices is store with each Insert checked by g first. A nil g
// returns store as it is.
func GuardDevices(store DeviceStore, g *TestTenantGuard) DeviceStore {
	if g == nil || store == nil {
		return store
	}
	return guardedDevices{DeviceStore: store, g: g}
}

type guardedDevices struct {
	DeviceStore
	g *TestTenantGuard
}

func (s guardedDevices) Insert(ctx context.Context, d ApprovalDevice) error {
	if err := s.g.Check(ctx, d.Tenant, d.PublicKey); err != nil {
		return err
	}
	return s.DeviceStore.Insert(ctx, d)
}

// GuardPasskeys is store with each Insert checked by g first. A nil g
// returns store as it is.
func GuardPasskeys(store PasskeyStore, g *TestTenantGuard) PasskeyStore {
	if g == nil || store == nil {
		return store
	}
	return guardedPasskeys{PasskeyStore: store, g: g}
}

type guardedPasskeys struct {
	PasskeyStore
	g *TestTenantGuard
}

func (s guardedPasskeys) Insert(ctx context.Context, p StoredPasskey) error {
	if err := s.g.Check(ctx, p.Tenant, p.PublicKey); err != nil {
		return err
	}
	return s.PasskeyStore.Insert(ctx, p)
}

// SessionTenants is what GuardSessions needs of a session store beside
// DeveloperSessionStore: the account a session is on, read before its
// approval key changes.
type SessionTenants interface {
	TenantOf(ctx context.Context, id string) (string, error)
}

// GuardSessions is store with each key a session takes checked by g first:
// a new session's device and approval keys, and an approval key set or
// replaced later, on the session's own account. A store that can't say a
// session's account refuses those changes rather than let a key past. A nil
// g returns store as it is.
func GuardSessions(store DeveloperSessionStore, g *TestTenantGuard) DeveloperSessionStore {
	if g == nil || store == nil {
		return store
	}
	return guardedSessions{DeveloperSessionStore: store, g: g}
}

type guardedSessions struct {
	DeveloperSessionStore
	g *TestTenantGuard
}

func (s guardedSessions) Insert(ctx context.Context, sess DeveloperSession) error {
	for _, k := range [][]byte{sess.DevicePublicKey, sess.ApprovalPublicKey} {
		if err := s.g.Check(ctx, sess.Tenant, k); err != nil {
			return err
		}
	}
	return s.DeveloperSessionStore.Insert(ctx, sess)
}

func (s guardedSessions) checkSession(ctx context.Context, id string, key []byte) error {
	tenants, ok := s.DeveloperSessionStore.(SessionTenants)
	if !ok {
		return errors.New("auth: the session store can't say a session's account")
	}
	tenant, err := tenants.TenantOf(ctx, id)
	if err != nil {
		return err
	}
	return s.g.Check(ctx, tenant, key)
}

func (s guardedSessions) SetApprovalKey(ctx context.Context, id string, key []byte, now time.Time) (DeveloperSession, error) {
	if err := s.checkSession(ctx, id, key); err != nil {
		return DeveloperSession{}, err
	}
	return s.DeveloperSessionStore.SetApprovalKey(ctx, id, key, now)
}

func (s guardedSessions) ReplaceApprovalKey(ctx context.Context, id string, key []byte, now time.Time) ([]byte, error) {
	if err := s.checkSession(ctx, id, key); err != nil {
		return nil, err
	}
	return s.DeveloperSessionStore.ReplaceApprovalKey(ctx, id, key, now)
}

// TenantOf passes through, so a guarded store still says a session's account.
func (s guardedSessions) TenantOf(ctx context.Context, id string) (string, error) {
	tenants, ok := s.DeveloperSessionStore.(SessionTenants)
	if !ok {
		return "", errors.New("auth: the session store can't say a session's account")
	}
	return tenants.TenantOf(ctx, id)
}

// ApprovalKeysInTenant passes through, for the proofs' check.
func (s guardedSessions) ApprovalKeysInTenant(ctx context.Context, tenant string, now time.Time) ([][]byte, error) {
	keys, ok := s.DeveloperSessionStore.(interface {
		ApprovalKeysInTenant(ctx context.Context, tenant string, now time.Time) ([][]byte, error)
	})
	if !ok {
		return nil, errors.New("auth: the session store can't list approval keys")
	}
	return keys.ApprovalKeysInTenant(ctx, tenant, now)
}
