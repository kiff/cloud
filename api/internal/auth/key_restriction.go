package auth

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// KeyRestriction keeps some device keys to some tenants: the integration
// proofs' test phone (KIFF_PROOF_TEST_KEYS) acts only on the managed test
// tenants (KIFF_PROOF_TEST_TENANTS). It is enforced in one place, where the
// middleware hands a request its identity, so a session holding such a key
// on any other tenant reaches no route at all; and where keys are taken
// (sign-in, an approval key, pairing), so one is never registered there.
// A nil *KeyRestriction restricts nothing.
//
// The tenant list is vetted at boot. A use check (WithUseCheck) asks again
// at each use of a restricted key, so a tenant that stops being a test
// tenant after boot (a real person's device paired there) refuses the test
// keys from that moment, not from the next deploy.
type KeyRestriction struct {
	restricted func(der []byte) bool
	tenants    map[string]bool
	stillTest  func(ctx context.Context, tenant string) bool
	// suspended reports an operator's suspension of a test tenant
	// (WithSuspendedCheck); nil reads none.
	suspended func(ctx context.Context, tenant string) (bool, error)
	// denied matches TestKeyDeniedRoutes.
	denied *http.ServeMux
}

// NewKeyRestriction keeps the keys restricted reports to tenants. Nil
// restricted gives nil: nothing is restricted.
func NewKeyRestriction(restricted func(der []byte) bool, tenants []string) *KeyRestriction {
	if restricted == nil {
		return nil
	}
	k := &KeyRestriction{restricted: restricted, tenants: map[string]bool{}, denied: http.NewServeMux()}
	for _, pattern := range TestKeyDeniedRoutes {
		k.denied.Handle(pattern, http.NotFoundHandler())
	}
	for _, t := range tenants {
		if t = strings.TrimSpace(t); t != "" {
			k.tenants[t] = true
		}
	}
	return k
}

// WithUseCheck sets the check asked at each use of a restricted key on a
// listed tenant: false (or a check that can't tell) refuses the key. It
// returns k; on a nil k it does nothing.
func (k *KeyRestriction) WithUseCheck(stillTest func(ctx context.Context, tenant string) bool) *KeyRestriction {
	if k != nil {
		k.stillTest = stillTest
	}
	return k
}

// WithSuspendedCheck sets how a test tenant's suspension is read: while an
// operator has it suspended (POST /v1/ops/tenants/{id}/suspend), a session
// holding a restricted key reaches no route on it (TenantSuspendedCode). A
// check that fails refuses too. It returns k; on a nil k it does nothing.
func (k *KeyRestriction) WithSuspendedCheck(suspended func(ctx context.Context, tenant string) (bool, error)) *KeyRestriction {
	if k != nil {
		k.suspended = suspended
	}
	return k
}

// Holds reports whether any key given is a restricted one.
func (k *KeyRestriction) Holds(keys ...[]byte) bool {
	if k == nil {
		return false
	}
	for _, key := range keys {
		if len(key) > 0 && k.restricted(key) {
			return true
		}
	}
	return false
}

// TestKeySessionLifetime is how long a session holding a restricted key
// lives from when it's made: a test device signs in again every two weeks,
// rather than holding a session for the usual 90 days.
const TestKeySessionLifetime = 14 * 24 * time.Hour

// SessionLifetime is the lifetime of a session made with keys: ttl, or
// TestKeySessionLifetime when it's shorter and any key is restricted.
func (k *KeyRestriction) SessionLifetime(ttl time.Duration, keys ...[]byte) time.Duration {
	if k.Holds(keys...) && ttl > TestKeySessionLifetime {
		return TestKeySessionLifetime
	}
	return ttl
}

// SessionOutlived reports that a session made at created and holding keys
// is past TestKeySessionLifetime at now, whatever its own expiry says: one
// made before the limit, or given a restricted approval key after it was
// made. A session with no creation time can't be measured and isn't.
func (k *KeyRestriction) SessionOutlived(created, now time.Time, keys ...[]byte) bool {
	return !created.IsZero() && k.Holds(keys...) && !now.Before(created.Add(TestKeySessionLifetime))
}

// TestKeyDeniedRoutes are what a restricted key never does, even on its own
// test tenant: delete the account, pay (Stripe's checkout and portal, an App
// Store or Google Play purchase), or make or register a key (an account key,
// a passkey, an approval key or its re-link, a computer's pairing, or the
// approval of another device's or tool's sign-in, which mints its session).
var TestKeyDeniedRoutes = []string{
	"POST /v1/me/delete",
	"POST /v1/account-deletions",
	"POST /v1/account-deletions/finish",
	"POST /v1/me/billing/checkout",
	"POST /v1/me/billing-portal",
	"POST /v1/me/billing/apple/transactions",
	"POST /v1/me/billing/google/purchases",
	"POST /v1/keys",
	"POST /v1/me/passkeys",
	"POST /v1/me/device/approval-key",
	"POST /v1/me/device/relink",
	"POST /v1/me/pairings",
	"POST /v1/auth/device/approve",
	"POST /v1/oauth/device/approve",
}

// TestKeyRouteCode is the error a restricted key gets on a route in
// TestKeyDeniedRoutes, and TenantSuspendedCode the one it gets on a
// suspended test tenant.
const (
	TestKeyRouteCode    = "test_key_route"
	TenantSuspendedCode = "tenant_suspended"
)

// Refusal is why id may not make r, or "" when it may: KeyRestrictedCode
// (a restricted key outside its tenants, AllowsIdentity), TenantSuspendedCode
// (a restricted key on its test tenant while it's suspended) or
// TestKeyRouteCode (a restricted key on a route in TestKeyDeniedRoutes).
// Only a session holding a restricted key is ever refused here.
func (k *KeyRestriction) Refusal(r *http.Request, id Identity) string {
	if k == nil {
		return ""
	}
	if !k.AllowsIdentity(r.Context(), id) {
		return KeyRestrictedCode
	}
	if !k.Holds(id.DeviceKey, id.ApprovalKey) {
		return ""
	}
	if k.suspended != nil {
		if s, err := k.suspended(r.Context(), id.Tenant); err != nil || s {
			return TenantSuspendedCode
		}
	}
	if _, pattern := k.denied.Handler(r); pattern != "" {
		return TestKeyRouteCode
	}
	return ""
}

// WriteRefusal answers 403 with code, Refusal's.
func WriteRefusal(w http.ResponseWriter, code string) {
	switch code {
	case TestKeyRouteCode:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": code,
			"message": "this device's key is a test key: it can't delete the account, pay, or make or register a key"})
	case TenantSuspendedCode:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": code,
			"message": "this test account is suspended by an operator"})
	default:
		WriteKeyRestricted(w)
	}
}

// Allows reports whether every key given may act on tenant. Empty keys are
// skipped. A restricted key on an empty or unlisted tenant is refused, and
// on a listed one when the use check says it's no longer a test tenant.
func (k *KeyRestriction) Allows(ctx context.Context, tenant string, keys ...[]byte) bool {
	if k == nil {
		return true
	}
	holds := false
	for _, key := range keys {
		if len(key) > 0 && k.restricted(key) {
			if !k.tenants[tenant] {
				return false
			}
			holds = true
		}
	}
	if holds && k.stillTest != nil && !k.stillTest(ctx, tenant) {
		return false
	}
	return true
}

// AllowsIdentity reports whether id's device and approval keys may act on
// id's tenant.
func (k *KeyRestriction) AllowsIdentity(ctx context.Context, id Identity) bool {
	return k.Allows(ctx, id.Tenant, id.DeviceKey, id.ApprovalKey)
}

// KeyRestrictedCode is the error a refused key gets.
const KeyRestrictedCode = "proof_test_key"

// WriteKeyRestricted answers 403 proof_test_key.
func WriteKeyRestricted(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]string{"error": KeyRestrictedCode,
		"message": "this device's key is the integration proofs' test key; it acts only on a test account"})
}

// WithKeyRestriction refuses, on every route, a session holding a key k
// keeps to other tenants.
func WithKeyRestriction(k *KeyRestriction) MiddlewareOption {
	return func(m *Middleware) { m.keys = k }
}
