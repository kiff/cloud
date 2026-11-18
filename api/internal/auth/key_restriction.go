package auth

import (
	"context"
	"net/http"
	"strings"
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
}

// NewKeyRestriction keeps the keys restricted reports to tenants. Nil
// restricted gives nil: nothing is restricted.
func NewKeyRestriction(restricted func(der []byte) bool, tenants []string) *KeyRestriction {
	if restricted == nil {
		return nil
	}
	k := &KeyRestriction{restricted: restricted, tenants: map[string]bool{}}
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
