package domain

import (
	"context"
	"errors"
)

// Source loads a tenant's domain YAML by tenant id. The cloud's
// runtime resolver consults a Source on first request for a tenant
// and caches the parsed runtime keyed by (tenantID, version).
//
// Implementations:
//
//   - PostgresSource (production) reads from cloud_meta.tenant_domains.
//   - MemorySource (tests) holds an in-process map.
//
// A filesystem-backed source is not part of v0.1; tenant domain
// config is cloud state and belongs alongside the tenant row.
type Source interface {
	// Load returns the YAML and a monotonically increasing version
	// number for the tenant. The version changes whenever the
	// tenant's YAML is updated; callers compare the version against
	// their cached value to decide whether to rebuild the runtime.
	//
	// Load resolves the tenant's DEFAULT domain. With
	// the composite (tenant_id, domain_id) model a tenant may have
	// more than one domain; the singular path stays on the default
	// so existing callers are unchanged. Use LoadByDomain to address
	// a specific domain.
	Load(ctx context.Context, tenantID string) (TenantDomain, error)

	// LoadByDomain returns a specific domain for the tenant by its
	// immutable domain_id or current public slug.
	// An exact domain_id match takes precedence if the selector could
	// also name another row's slug. An empty selector resolves the
	// tenant's default domain — identical to Load — so callers that
	// don't yet carry a domain selector keep working. Returns
	// ErrDomainNotFound when the tenant has no such domain.
	LoadByDomain(ctx context.Context, tenantID, selector string) (TenantDomain, error)

	// List returns all of the tenant's domains, the tenant's default
	// first. The multi-domain read projection (the
	// domains index) consumes this; a tenant with one domain gets a
	// one-element slice, a tenant with none an empty slice (not an
	// error). Order is stable: default first, then oldest-created,
	// then domain_id, so the index and switcher render consistently.
	List(ctx context.Context, tenantID string) ([]TenantDomain, error)

	// Create persists an additional, non-default domain for the
	// tenant from validated YAML. The
	// caller is the trust boundary: the YAML must already have been
	// parsed and validated against the executor registry, exactly as
	// the activate path requires. The (domain_id, slug) are derived
	// from the YAML. Returns ErrDomainExists when the tenant already
	// has a domain with that domain_id — create never overwrites; a
	// re-upload of an existing domain goes through the default
	// upsert (Save) or the draft activate path instead.
	//
	// Create owns *additional* domains only: it always writes
	// is_default = FALSE. The tenant's default is established and
	// maintained by the activate path (the default upsert), so a
	// tenant reaches Create only after its first/default domain
	// already exists.
	Create(ctx context.Context, tenantID string, yaml []byte) error

	// Save persists the YAML for a tenant. Increments the version.
	// The Source does not validate the YAML; that is the parser's
	// job and is performed by the caller before Save (so a Source
	// always holds well-formed yaml... eventually consistent with
	// validation: the caller is the trust boundary).
	Save(ctx context.Context, tenantID string, yaml []byte) error

	// Delete removes the tenant's domain row. Callers usually
	// invoke this when deprovisioning a tenant.
	Delete(ctx context.Context, tenantID string) error

	// DeleteByDomain removes a single domain by its domain_id.
	// Returns ErrDomainNotFound when the tenant has no
	// such domain. Callers (the delete endpoint) guard the default
	// domain themselves — this is a pure delete of the addressed row,
	// not a default-aware operation. Historical evidence stamped with
	// the domain_id (decisions, receipts, usage) is untouched; it is
	// immutable audit that outlives the domain config.
	DeleteByDomain(ctx context.Context, tenantID, domainID string) error

	// UpdateByDomain re-authors a specific domain's YAML in place,
	// keyed by domain_id: it replaces the stored
	// yaml and increments the version while preserving the immutable
	// domain_id, the slug, and the is_default flag. Returns
	// ErrDomainNotFound when the tenant has no such domain. The caller
	// is the trust boundary (validates the YAML first) and is
	// responsible for keeping the domain's identity stable — the slug
	// is intentionally NOT re-derived here, so an in-place edit can
	// never silently move the {domain} URL. (Rename — a deliberate
	// slug change — is a separate operation.)
	UpdateByDomain(ctx context.Context, tenantID, domainID string, yaml []byte) error
}

// TenantDomain is what Load returns.
//
// DomainID and Slug are the multi-domain identity values.
// They are derived from the domain YAML's authored name at
// load time — Slug is the URL-safe token for the {domain} path
// segment, DomainID is the stable per-tenant identity (equal to Slug
// at migration Decision 6, diverging only once explicit
// rename lands). Both are empty when the stored YAML carries no
// readable `domain:` name. Nothing keys on them yet; they are the
// foundation the resolver, metering, and record-stamping build on.
type TenantDomain struct {
	TenantID string
	YAML     []byte
	Version  int
	DomainID string
	Slug     string
}

// ErrDomainNotFound is returned when a tenant has no domain YAML
// uploaded yet.
var ErrDomainNotFound = errors.New("domain: tenant has no domain configured")

// ErrDomainExists is returned by Source.Create when the tenant
// already has a domain with the derived domain_id. Create never
// overwrites an existing domain; the caller maps this to a 409.
var ErrDomainExists = errors.New("domain: tenant already has a domain with this id")
