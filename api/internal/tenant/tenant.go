// Package tenant defines the cloud-side tenant data boundary.
// It owns the Tenant model, the bookkeeping store
// (`cloud_meta.tenants`), and the schema-per-tenant provisioner.
//
// What this package is not:
//
//   - An auth provider. Tenant resolution accepts a tenant id from
//     wherever the caller knows it (a request header for dev, a Clerk
//     session in production, an admin tool for ops). Auth integration
//     lands in a separate package once the auth-direction decision
//     resolves.
//
//   - A billing or metering layer. Tenants are just the cloud's data
//     boundary; usage counters, anchoring, and billing all attach
//     later, on top of this foundation.
//
//   - A signup flow. v0.1 creates tenants via the kiff-tenant CLI;
//     a public signup path lands when the cloud beta opens.
package tenant

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// Status is the lifecycle stage of a tenant. The status field on a
// row in `cloud_meta.tenants` tracks where the tenant is between
// provisioning and tear-down.
type Status string

const (
	// StatusProvisioning is set when the row is created and before
	// the tenant's Postgres schema is applied. A tenant in this
	// state is not safe to serve requests for.
	StatusProvisioning Status = "provisioning"

	// StatusActive is set once the tenant's schema is applied and
	// the tenant is ready to serve requests.
	StatusActive Status = "active"

	// StatusSuspended is set when the tenant is held offline but
	// not deleted (e.g. for billing or moderation actions).
	StatusSuspended Status = "suspended"

	// StatusDeletedPending is set when the tenant owner has
	// requested erasure (self-serve or via privacy@kiff.dev). The
	// per-tenant schema is retained during the 30-day grace window
	// so a restore is possible. API keys are revoked and the
	// runtime halts new proposals. After the grace window the
	// operator (or an automated sweep, M2-late) runs the hard
	// delete via Provisioner.Deprovision.
	StatusDeletedPending Status = "deleted_pending"

	// StatusDeleted is set when the tenant's data has been removed.
	// The row is kept for audit; the schema is dropped.
	StatusDeleted Status = "deleted"
)

// GracePeriodDays is the number of days the tenant's data is
// retained in StatusDeletedPending before a hard delete is safe.
// Matches the privacy policy's 30-day restoration window.
const GracePeriodDays = 30

// Metadata keys written by SoftDelete. Stored in the existing
// Metadata map so no schema migration is required for v0.1.
const (
	// MetadataKeyDeletedAt is the RFC 3339 timestamp when the
	// deletion was requested. Written by SoftDelete.
	MetadataKeyDeletedAt = "deletion_requested_at"

	// MetadataKeyGraceWindowEnds is the RFC 3339 timestamp when
	// the grace window expires and a hard delete becomes safe.
	// Always deleted_at + GracePeriodDays.
	MetadataKeyGraceWindowEnds = "grace_window_ends"
)

// IsErasing reports whether the tenant is in a deletion-pending or
// deleted state and should not serve new governed actions.
func (t Tenant) IsErasing() bool {
	return t.Status == StatusDeletedPending || t.Status == StatusDeleted
}

// IsServable reports whether the tenant may serve governed actions.
// Only active tenants are servable; provisioning, suspended, and the
// erasing states are not. The proposal gate and the runtime resolver
// share this so "not servable" is one definition, not several
// status checks scattered across the hot path.
func (t Tenant) IsServable() bool {
	return t.Status == StatusActive
}

// Tenant is the cloud's view of a single isolated workspace.
//
// The ID is a stable identifier (ULID, lowercased). The Slug is a
// human-readable handle, unique across active tenants. Anchoring,
// billing, and per-tenant runtime resolution all key on the ID, not
// the slug — slugs may change; ids do not.
//
// RedactionSalt is a per-tenant secret (32 random bytes) generated
// at row creation. It feeds two cloud-side primitives:
//
//   - The anchor leaf hash for this tenant's records, so two
//     tenants with the same canonical-record bytes commit to
//     different leaves and the on-chain anchor cannot be used as a
//     confirmation oracle for guessed-but-unverified records.
//
//   - The per-domain parameter redaction config, where
//     fields marked `redact: hash` land in audit as
//     sha256(value || salt) so the original value never persists.
//
// The salt never leaves the cloud's database. Destroying the salt
// (e.g. on tenant deletion) makes historical on-chain hashes for
// that tenant permanently unreconstructable — the GDPR
// right-to-erasure mechanism baked into the anchoring layer.
type Tenant struct {
	ID            string         `json:"id"`
	Slug          string         `json:"slug"`
	DisplayName   string         `json:"displayName"`
	Status        Status         `json:"status"`
	CreatedAt     time.Time      `json:"createdAt"`
	ProvisionedAt *time.Time     `json:"provisionedAt,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`

	// RedactionSalt is the per-tenant secret described above.
	// Encoded as `json:"-"` so the salt never escapes through the
	// kiff-tenant CLI's stdout/stderr (which marshals Tenant to
	// JSON), through any future HTTP surface that returns Tenant,
	// or through deploy-side log capture. Operators don't need to
	// see the salt for any documented procedure: creation
	// generates it server-side; list shows metadata; set-domain /
	// set-plan / mint-key never read it. The only consumer
	// outside this package goes through the SaltResolver
	// interface, which is a function call, not JSON.
	//
	// Compare with HasRedactionSalt rather than checking len(...)
	// directly so a future "salt is rotating" sentinel value can
	// be added without touching every call site.
	RedactionSalt []byte `json:"-"`
}

// RedactionSaltSize is the canonical length in bytes of a tenant's
// RedactionSalt. 32 bytes (256 bits) gives the keccak-256 leaf hash
// preimage enough entropy that a salt-aware off-chain attacker
// cannot brute-force the tenant identifier from a public anchor.
const RedactionSaltSize = 32

// HasRedactionSalt reports whether the tenant has a populated salt.
// Returns false for legacy rows pending backfill, so callers can
// branch on "skip until we have a salt" rather than panicking on a
// nil slice.
func (t Tenant) HasRedactionSalt() bool {
	return len(t.RedactionSalt) == RedactionSaltSize
}

// SchemaName returns the Postgres schema where this tenant's
// framework-side data lives. the convention is
// `kiff_t_<lowercased_ulid>`. The function is the canonical place
// to derive that name; callers must not concatenate by hand.
func (t Tenant) SchemaName() string { return SchemaNameFor(t.ID) }

// SchemaNameFor returns the Postgres schema name for an arbitrary
// tenant id. Exposed so non-tenant code (provisioning helpers, test
// fixtures) can derive the same name without holding a Tenant.
func SchemaNameFor(tenantID string) string {
	return "kiff_t_" + strings.ToLower(tenantID)
}

// Errors surfaced by Store implementations. Keeping them in this
// package lets callers errors.Is against them without importing
// implementation packages.
var (
	// ErrNotFound is returned when no tenant matches the lookup.
	ErrNotFound = errors.New("tenant: not found")

	// ErrDuplicateSlug is returned when a tenant slug already exists.
	ErrDuplicateSlug = errors.New("tenant: slug already exists")

	// ErrInvalidTenant is returned when the tenant fields fail
	// validation (empty id, empty slug, invalid characters).
	ErrInvalidTenant = errors.New("tenant: invalid")
)

// Validate checks the minimum field set required to persist a
// tenant. Status is not validated here — it is the store's job to
// stamp it.
func (t Tenant) Validate() error {
	if t.ID == "" {
		return errors.Join(ErrInvalidTenant, errors.New("id is required"))
	}
	if !validULID(t.ID) {
		return errors.Join(ErrInvalidTenant, errors.New("id must be a ULID"))
	}
	if t.Slug == "" {
		return errors.Join(ErrInvalidTenant, errors.New("slug is required"))
	}
	if !validSlug(t.Slug) {
		return errors.Join(ErrInvalidTenant, errors.New("slug must match [a-z0-9][a-z0-9-]*"))
	}
	return nil
}

// Store is the cloud's persistence boundary for tenant rows. The
// Postgres implementation lives in this package; an in-memory one
// powers unit tests of higher layers.
type Store interface {
	// Create persists a new tenant. Returns ErrDuplicateSlug if the
	// slug is already in use by an existing (non-deleted) tenant.
	Create(ctx context.Context, t Tenant) error

	// Get returns a tenant by id. Returns ErrNotFound when missing.
	Get(ctx context.Context, id string) (Tenant, error)

	// GetBySlug returns a tenant by slug. Returns ErrNotFound when
	// missing or when the tenant is deleted.
	GetBySlug(ctx context.Context, slug string) (Tenant, error)

	// List returns tenants matching the filter. Empty filter returns
	// all tenants regardless of status (useful for ops).
	List(ctx context.Context, filter ListFilter) ([]Tenant, error)

	// SetStatus updates the lifecycle status of a tenant. The
	// transition is not validated here; callers (Provisioner) own
	// the lifecycle logic.
	SetStatus(ctx context.Context, id string, status Status, provisionedAt *time.Time) error

	// SetMetadata sets one key on a tenant's metadata map. The
	// other keys are preserved. Used by the operator CLI to flip
	// tenants between developer plans (free_dev, launch_pilot,
	// team, and legacy builder / agency_team labels)
	// without touching unrelated metadata. Returns ErrNotFound
	// when the tenant id does not exist.
	//
	// value is stored as JSON; pass strings, numbers, booleans, or
	// JSON-serializable structs. Pass nil to delete the key.
	SetMetadata(ctx context.Context, id, key string, value any) error

	// AddAIGoOn records one go-on past the AI fair-use bound for the
	// period that starts at period, in one step: the count goes up by
	// one in the same period and restarts at 1 in a new one, so a
	// period never takes another period's count, and two go-ons at once
	// both count. It returns the period's count. ErrNotFound when the
	// tenant id does not exist.
	AddAIGoOn(ctx context.Context, id string, period time.Time) (int, error)

	// Delete removes the tenant row. Schema teardown is the
	// provisioner's job; this only deletes bookkeeping. Callers
	// should set Status to StatusDeleted and keep the row instead
	// of using Delete unless the tenant truly never existed.
	Delete(ctx context.Context, id string) error

	// NullRedactionSalt zeroes the stored RedactionSalt for the
	// given tenant. Called by Provisioner.Deprovision after the
	// schema is dropped so the /security salt-destruction claim
	// holds: once the salt is gone, historical on-chain hashes
	// for this tenant are permanently unreconstructable
	// (anchoring-layer erasure). The tenant row is kept
	// for audit continuity; only the salt is destroyed.
	NullRedactionSalt(ctx context.Context, id string) error
}

// ListFilter constrains a List call. Empty fields are ignored.
type ListFilter struct {
	Status Status
}

// NewID generates a fresh tenant id (ULID, lowercased). Uses the
// process clock and crypto/rand under the hood. Stable across
// processes, sortable by creation time.
func NewID() string {
	return strings.ToLower(ulid.Make().String())
}

// validULID returns true if s is a 26-char Crockford-base32 string.
// We accept both upper and lower case; the canonical form is lower.
func validULID(s string) bool {
	if len(s) != 26 {
		return false
	}
	_, err := ulid.ParseStrict(strings.ToUpper(s))
	return err == nil
}

// validSlug enforces the slug grammar [a-z0-9][a-z0-9-]*. Length is
// not enforced here; callers can layer policy on top.
func validSlug(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}
