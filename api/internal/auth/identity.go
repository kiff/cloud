// Package auth defines the cloud's authentication boundary.
//
// The framework's actor.Actor stays
// untouched. The cloud owns the mapping from a verified identity
// (Clerk session, API key, or any future provider) to a tenant id
// plus an actor.Actor instance. This package holds:
//
//   - Identity: the verified-request shape produced by a session or
//     API-key check, before tenant resolution.
//   - WalletLink: the optional wallet attached to an identity per
//     the May 2026 amendment.
//   - ActorResolver: the interface that turns Identity into
//     (actor.Actor, tenantID, error).
//
// Slice 1 of issue #3 ships only the types, the interface, and a
// small in-memory resolver suitable for tests. Clerk, API keys, the
// HTTP middleware, and the wallet-link endpoints land in subsequent
// slices.
package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/membership"
)

// AuthMethod identifies how a request was authenticated. Stored on
// Identity so downstream code (audit, rate limiting, future
// per-method policy) can branch without re-parsing the auth header.
type AuthMethod string

const (
	// MethodSession means a verified user-session token (Clerk in
	// v0.1). The Identity.Subject is the OIDC subject identifier.
	MethodSession AuthMethod = "session"
	// MethodAPIKey means a verified API key. Identity.Subject is
	// the key id (the prefix portion of `kiff_live_<tenant>_<rnd>`),
	// not the full secret.
	MethodAPIKey AuthMethod = "api_key"
	// MethodDeveloper means a verified developer session:
	// a KIFF-issued token minted through the device-authorization
	// flow and stored in the developer's keychain. Like a session it
	// represents a human (owner-plane), so it is not APIKey=true; but
	// unlike a Clerk session its tenant is carried on the record, not
	// resolved from an IdP. Subject is the human's subject id.
	MethodDeveloper AuthMethod = "developer"
)

// WalletLink is the optional wallet the cloud attaches to an
// authenticated user. It is not
// a sign-in mechanism; it carries attribution onto actor.Actor
// metadata so audit records can record "this human plus this wallet
// did the action."
//
// Address is the lowercase 0x-prefixed hex address. ChainID is the
// EVM chain on which the linking signature was verified (mainnet,
// Base, Sepolia, etc.). LinkedAt is the wall-clock time the link
// was recorded; later than this is fine, earlier shouldn't happen.
type WalletLink struct {
	Address  string    `json:"address"`
	ChainID  uint64    `json:"chain_id"`
	LinkedAt time.Time `json:"linked_at"`
}

// Validate returns nil when the link is structurally well-formed.
// Use it after deserializing from storage; the wallet handler
// validates more deeply at write time.
func (w WalletLink) Validate() error {
	if w.Address == "" {
		return errors.New("wallet: empty address")
	}
	if len(w.Address) != 42 || !strings.EqualFold(w.Address[:2], "0x") {
		return fmt.Errorf("wallet: address %q must be 0x-prefixed hex of length 42", w.Address)
	}
	if w.Address != strings.ToLower(w.Address) {
		return fmt.Errorf("wallet: address %q must be lowercase", w.Address)
	}
	if w.ChainID == 0 {
		return errors.New("wallet: chain_id must be non-zero")
	}
	if w.LinkedAt.IsZero() {
		return errors.New("wallet: linked_at must be set")
	}
	return nil
}

// Identity is the cloud's verified-request shape. It is produced by
// the auth middleware after a session or API-key check has succeeded
// and before tenant resolution runs. The framework never sees this
// type; the ActorResolver collapses it down to actor.Actor + a
// tenant id.
//
// Subject is the stable identifier from the source: the OIDC sub for a
// session, or the subject that minted an API key. The API key's own stable
// identifier is Metadata["api_key_id"]. Email is best-effort and may be empty
// for service-to-service API-key callers. Tenant is
// the tenant id the identity belongs to; the resolver fills this
// (the auth check itself does not). Roles carry cloud-side role
// assignments such as "admin" or "operator"; the tenant's permission
// policy maps these to framework permissions. Method records how
// the request authenticated. APIKey is true when Method ==
// MethodAPIKey; kept as a convenience boolean. Metadata
// carries arbitrary string-string attribution that downstream code
// may want to surface (Clerk organization id, feature flag bucket,
// etc.). Wallet is the optional wallet link; nil when absent.
type Identity struct {
	Subject  string            `json:"subject"`
	Email    string            `json:"email,omitempty"`
	Tenant   string            `json:"tenant"`
	Roles    []string          `json:"roles,omitempty"`
	Method   AuthMethod        `json:"method"`
	APIKey   bool              `json:"api_key,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Wallet   *WalletLink       `json:"wallet,omitempty"`

	// DomainID is the domain the authenticating API key is bound to,
	// or "" when the key is tenant-wide or the
	// request is session-authenticated. The decide path treats a
	// non-empty value as an isolation boundary: a request that names a
	// different domain is refused. Resolved server-side from the key
	// record, never from the request body.
	DomainID string `json:"domain_id,omitempty"`

	// AgentID is the agent (card holder) the authenticating API key is
	// bound to, or "" when the key is unbound or the request is
	// session-authenticated. The decide path matches it against the
	// proposal's actor_id. Resolved server-side from the key record,
	// never from the request body.
	AgentID string `json:"agent_id,omitempty"`

	// Environment is the environment the authenticating API key is bound
	// to, or "" when the key may act in any environment or the
	// request is session-authenticated. The decide path decides a bound
	// key's requests in that environment and refuses a request naming
	// another. Resolved server-side from the key record, never from the
	// request body.
	Environment string `json:"environment,omitempty"`

	// MemberRole is the role of the person's membership in Tenant (RFC
	// 043), resolved server-side from the membership store on every
	// request; "" for keys and for deploys without memberships. Never
	// read from a token or header (M3).
	MemberRole membership.Role `json:"member_role,omitempty"`

	// FactorAge is how long ago the session's sign-in factor was
	// verified (Clerk's fva claim), when the token carries it. Step-up
	// checks read it; nil means unknown.
	FactorAge *time.Duration `json:"-"`

	// DeviceKey is the phone's public key (SPKI DER) when the request
	// authenticates with a developer session that has one, and
	// DeviceSessionID that session. Resolved from the session record,
	// never from the request. An answer to a held call from such a
	// session must carry the key's signature.
	DeviceKey       []byte `json:"-"`
	DeviceSessionID string `json:"-"`
	DeviceName      string `json:"-"`
	// DeviceID is the approval device the session signed in from,
	// when it names one; DeviceKey is then that device's key.
	DeviceID string `json:"-"`
}

// ActorID returns the authenticated principal that should appear in framework
// audit records. An API key is a runtime identity distinct from the person who
// minted it, so API-key calls use the key id. Interactive session and developer
// identities use Subject directly.
//
// The Subject fallback for an API-key identity is defensive compatibility for
// synthetic/internal identities. verifyAPIKey always stamps api_key_id.
func (id Identity) ActorID() string {
	if id.Method == MethodAPIKey {
		if keyID := strings.TrimSpace(id.Metadata["api_key_id"]); keyID != "" {
			return keyID
		}
	}
	return id.Subject
}

// APIKeyID is the id of the API key the request came with, or "" for
// a person's session.
func (id Identity) APIKeyID() string {
	if id.Method != MethodAPIKey && !id.APIKey {
		return ""
	}
	return strings.TrimSpace(id.Metadata["api_key_id"])
}

// Validate returns nil when the identity carries the minimum fields
// downstream code needs. The auth layer constructs valid Identities;
// callers reading from storage or wire format should validate.
//
// Tenant is intentionally optional at this layer: the auth check
// produces an Identity before tenant lookup, and the resolver fills
// Tenant in. Tenant being empty is therefore not an error in v0.1;
// the resolver's contract is the place that requires it.
func (id Identity) Validate() error {
	if id.Subject == "" {
		return errors.New("identity: empty subject")
	}
	switch id.Method {
	case MethodSession, MethodAPIKey, MethodDeveloper:
	case "":
		return errors.New("identity: empty method")
	default:
		return fmt.Errorf("identity: unknown method %q", id.Method)
	}
	if id.Method == MethodAPIKey && !id.APIKey {
		return errors.New("identity: method=api_key requires APIKey=true")
	}
	if id.Method == MethodSession && id.APIKey {
		return errors.New("identity: method=session must not set APIKey=true")
	}
	if id.Method == MethodDeveloper && id.APIKey {
		return errors.New("identity: method=developer must not set APIKey=true")
	}
	if id.Wallet != nil {
		if err := id.Wallet.Validate(); err != nil {
			return fmt.Errorf("identity: %w", err)
		}
	}
	return nil
}

// HasRole reports whether the identity carries the given role. The
// match is case-sensitive; cloud-side role names are conventionally
// lowercase ("admin", "operator", "service").
func (id Identity) HasRole(role string) bool {
	for _, r := range id.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// ManagesAccount reports whether the identity may act for the account's
// people: tenant_owner or admin, and not a key bound to an agent. A key
// bound to an agent acts as that agent whatever roles it holds;
// otherwise it could mint itself an unbound key, author the contract it
// is governed by, or answer for its own Card.
func (id Identity) ManagesAccount() bool {
	return id.AgentID == "" && !id.IsConnector() && (id.HasRole("tenant_owner") || id.HasRole("admin"))
}

// AccountRoles are the roles that let a credential act for the
// account's people. A key bound to an agent never holds them: minting
// or granting one onto such a key is refused, and a stored key that has
// one anyway resolves without it, so no domain permission bound to
// these roles reaches an agent through its own key.
var AccountRoles = []string{"tenant_owner", "admin"}

// IsAccountRole reports whether role is one of AccountRoles.
func IsAccountRole(role string) bool {
	for _, r := range AccountRoles {
		if r == role {
			return true
		}
	}
	return false
}

// rolesInEffect returns the roles a key acts with. A key bound to an
// agent acts as that agent: a record that holds an account role anyway
// (minted before such keys were refused) acts, and is listed, without it.
func rolesInEffect(rec APIKeyRecord) []string {
	out := make([]string, 0, len(rec.Roles))
	for _, r := range rec.Roles {
		if rec.AgentID == "" || !IsAccountRole(r) {
			out = append(out, r)
		}
	}
	return out
}
