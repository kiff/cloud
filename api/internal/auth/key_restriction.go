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
	// short are the test tenants whose test keys' sessions last
	// TestKeySessionLifetime (WithShortSessions); on any other, a test
	// key's session lasts as long as any other.
	short map[string]bool
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
// operator has it suspended (POST /v1/ops/tenants/{id}/suspend), nothing on
// it reaches any route (TenantSuspendedCode): its test device's sessions, its
// agents' keys, and anyone else's session. A check that fails refuses too.
// Other tenants aren't asked. It returns k; on a nil k it does nothing.
func (k *KeyRestriction) WithSuspendedCheck(suspended func(ctx context.Context, tenant string) (bool, error)) *KeyRestriction {
	if k != nil {
		k.suspended = suspended
	}
	return k
}

// WithShortSessions sets the test tenants whose test keys' sessions last
// TestKeySessionLifetime: a robot test user's
// (KIFF_PROOF_SHORT_SESSION_TENANTS), not the CI's, whose phones sign in
// once and run for months. A tenant here that isn't one of k's is ignored.
// It returns k; on a nil k it does nothing.
func (k *KeyRestriction) WithShortSessions(tenants []string) *KeyRestriction {
	if k == nil {
		return k
	}
	k.short = map[string]bool{}
	for _, t := range tenants {
		if t = strings.TrimSpace(t); t != "" && k.tenants[t] {
			k.short[t] = true
		}
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
// lives from when it's made, on a tenant WithShortSessions names: its test
// device signs in again every two weeks, rather than holding a session for
// the usual 90 days.
const TestKeySessionLifetime = 14 * 24 * time.Hour

// shortens reports that a session on tenant holding keys is held to
// TestKeySessionLifetime.
func (k *KeyRestriction) shortens(tenant string, keys ...[]byte) bool {
	return k != nil && k.short[tenant] && k.Holds(keys...)
}

// SessionLifetime is the lifetime of a session made on tenant with keys:
// ttl, or TestKeySessionLifetime when it's shorter, the tenant is one
// WithShortSessions names and any key is restricted.
func (k *KeyRestriction) SessionLifetime(ttl time.Duration, tenant string, keys ...[]byte) time.Duration {
	if k.shortens(tenant, keys...) && ttl > TestKeySessionLifetime {
		return TestKeySessionLifetime
	}
	return ttl
}

// SessionOutlived reports that a session on tenant, made at created and
// holding keys, is past TestKeySessionLifetime at now, whatever its own
// expiry says (one made before the limit, or given a restricted approval key
// after it was made), on a tenant WithShortSessions names. A session with no
// creation time can't be measured and isn't.
func (k *KeyRestriction) SessionOutlived(tenant string, created, now time.Time, keys ...[]byte) bool {
	return !created.IsZero() && k.shortens(tenant, keys...) && !now.Before(created.Add(TestKeySessionLifetime))
}

// TestKeyDeniedRoutes are what a restricted key never does, even on its own
// test tenant: delete the account, pay (Stripe's checkout and portal, an App
// Store or Google Play purchase), make, change or register a key (an account
// key or a change to one, an outside approver's secret, a passkey, an
// approval key or its re-link, a computer's pairing, or the approval of
// another device's, app's or tool's sign-in, which mints its session), or
// change who's on an account (inviting someone, accepting an invitation,
// changing a member's role, or reactivating one). A key a test session made
// would be no test key, outside every limit here, so none is made. A taf's payments (browse_pay) go through the
// gateway with its agent's key, never a device's session, so they're held
// where the gateway's tools run, not here.
// Finishing a deletion (POST /v1/account-deletions/finish) is public: its
// authority is the signature an accepted deletion gives, which only POST
// /v1/account-deletions makes, and that's here.
var TestKeyDeniedRoutes = []string{
	"POST /v1/me/delete",
	"POST /v1/account-deletions",
	"POST /v1/me/billing/checkout",
	"POST /v1/me/billing-portal",
	"POST /v1/me/billing/apple/transactions",
	"POST /v1/me/billing/google/purchases",
	"POST /v1/keys",
	"PATCH /v1/keys/{id}",
	"POST /v1/me/approver-grants",
	"POST /v1/me/passkeys",
	"POST /v1/me/device/approval-key",
	"POST /v1/me/device/relink",
	"POST /v1/me/pairings",
	"POST /v1/auth/device/approve",
	"POST /v1/auth/app/approve",
	"POST /v1/oauth/device/approve",
	"POST /v1/tenant/invites",
	"POST /v1/invites/accept",
	"PUT /v1/tenant/members/{subject}/role",
	"POST /v1/tenant/members/{subject}/reactivate",
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
// (anything on a listed test tenant while it's suspended) or TestKeyRouteCode
// (a restricted key on a route in TestKeyDeniedRoutes). Only an identity on
// a listed test tenant, or holding a restricted key, is ever refused here.
func (k *KeyRestriction) Refusal(r *http.Request, id Identity) string {
	if k == nil {
		return ""
	}
	if !k.AllowsIdentity(r.Context(), id) {
		return KeyRestrictedCode
	}
	if k.suspended != nil && k.tenants[id.Tenant] {
		if s, err := k.suspended(r.Context(), id.Tenant); err != nil || s {
			return TenantSuspendedCode
		}
	}
	if !k.Holds(id.DeviceKey, id.ApprovalKey) {
		return ""
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
			"message": "this device's key is a test key: it can't delete the account, pay, make or register a key, or change who's on the account"})
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
