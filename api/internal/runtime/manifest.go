package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/tenant"
)

// RuntimeManifest is the typed projection of every tenant-scoped
// runtime knob the cloud needs to assemble a *kiffruntime.Runtime.
//
// The point is hygiene, not control plane:
//
//   - Before the manifest, CloudResolver.Resolve and the API's startup
//     hand-wired tenant.Store reads, domain.Source reads,
//     tenant.Metadata lookups, and process defaults. Each new
//     per-tenant knob (plan limits, usage aggregator,
//     receipt mode in the future) had to thread itself through
//     those two files independently.
//   - With the manifest, the resolver answers one question first
//     ("Given tenant_id, what known runtime configuration does
//     this request use?") and then builds the runtime against
//     the answer. The next per-tenant knob plugs into the
//     manifest, not into Resolve's argument list or another
//     hand-wired branch in the API's startup.
//
// What the manifest is NOT:
//
//   - It is not a control plane. There is no scheduler, no infra
//     provisioner, no service-mesh-style routing.
//   - It does not enforce limits. Plan limits ride along as
//     typed projection (Plan, MonthlyProposalLimit) but the
//     usage.ProposalLimiter remains the single enforcement
//     point. Two enforcers would diverge eventually.
//   - It does not replace the auth/role boundary. Identity stays
//     where it is; the manifest does not know about subjects or
//     API keys.
//   - It does not own the domain YAML. domain.Source remains the
//     source of truth; the manifest carries the version + the
//     parsed definition handed to it by the caller.
//
// Cardinality: one manifest per active resolution. Cheap to
// build (a couple of map lookups + one tenant.PlanFor call).
type RuntimeManifest struct {
	// TenantID is the lowercase ULID. Stable across renames.
	TenantID string

	// DomainID is the resolved domain's stable per-tenant identity
	// When resolution requested the tenant's default
	// domain (empty selector), this carries the default's actual
	// domain_id, not "". Pairs with DomainVersion as the runtime
	// cache key dimension.
	DomainID string

	// TenantStatus mirrors the tenant row's status. Resolve
	// callers are expected to refuse non-active tenants before
	// reaching the runtime; this field is here so future code
	// (suspension banners, read-only dashboards) can surface the
	// reason without a second store lookup.
	TenantStatus tenant.Status

	// SchemaName is the per-tenant Postgres schema
	// (kiff_t_<id>). Derived from tenant.SchemaNameFor; carried
	// here so consumers do not concatenate by hand.
	SchemaName string

	// DomainVersion is the monotonic version domain.Source
	// returns alongside the YAML. Useful for cache keys and for
	// telemetry that wants to attribute behavior to a specific
	// domain revision.
	DomainVersion int

	// Plan is the resolved tenant.Plan from
	// tenant.Metadata[plan]. Always set; defaults to free_dev
	// when the metadata key is absent or unrecognized.
	Plan tenant.Plan

	// MonthlyProposalLimit mirrors tenant.PlanFor(t).MonthlyProposalLimit.
	// Surfaced here so non-handler code (dashboards, ops tools)
	// can read the effective ceiling without re-parsing the plan
	// label. Zero or negative means unlimited.
	MonthlyProposalLimit int

	// Features is reserved for boolean per-tenant knobs the cloud
	// wants to gate without a code change. Manifests returned by
	// ResolveManifest are guaranteed to carry a non-nil map (v0.1
	// leaves it empty). Hand-built RuntimeManifest{} values from
	// tests can leave it nil; range-over-nil is safe in Go, so
	// neither path is dangerous, but production callers should
	// only consume manifests that came through ResolveManifest.
	Features map[string]bool
}

// ManifestResolver looks up the typed runtime configuration for
// a tenant. CloudResolver.Resolve calls it once before building
// a runtime; tests can substitute a stub.
//
// The interface is deliberately narrow (one method) so future
// refactors can swap implementations without touching every
// caller.
type ManifestResolver interface {
	ResolveManifest(ctx context.Context, tenantID, domainID string) (RuntimeManifest, error)
}

// Errors surfaced by manifest resolution. Callers wrap them with
// errors.Is so the resolver can branch on the failure mode
// rather than parsing strings.
var (
	// ErrManifestTenantInactive is returned when the tenant row
	// exists but its status is not active. The caller refuses to
	// build a runtime in this case; the manifest returned
	// alongside the error carries TenantID, TenantStatus, and
	// SchemaName populated (every other field zero) so a
	// suspension banner / read-only dashboard can render the
	// reason without a second store lookup.
	ErrManifestTenantInactive = errors.New("runtime: tenant is not active")
)

// manifestResolver is the default ManifestResolver implementation.
// It assembles a RuntimeManifest from the existing tenant.Store +
// domain.Source primitives — no new tables, no new dependencies.
type manifestResolver struct {
	tenants tenant.Store
	source  domain.Source
}

// NewManifestResolver builds the default resolver. Both
// dependencies must be non-nil; the cloud always wires them.
func NewManifestResolver(tenants tenant.Store, source domain.Source) ManifestResolver {
	return &manifestResolver{tenants: tenants, source: source}
}

// ResolveManifest implements ManifestResolver.
//
// The lookup order is:
//
//  1. tenant.Store.Get — ground truth for status, slug, metadata.
//  2. domain.Source.Load — domain YAML version. Missing domain is
//     a hard error; CloudResolver.Resolve already required it
//     before the manifest existed.
//  3. tenant.PlanFor — typed projection of plan/limits from
//     tenant.Metadata. We delegate here rather than re-parsing
//     the plan label so tenant.PlanFor stays the source of truth.
//
// Errors are returned verbatim; callers wrap with their own
// context. Inactive tenants surface as ErrManifestTenantInactive
// so callers can branch deterministically.
func (r *manifestResolver) ResolveManifest(ctx context.Context, tenantID, domainID string) (RuntimeManifest, error) {
	t, err := r.tenants.Get(ctx, tenantID)
	if err != nil {
		return RuntimeManifest{}, fmt.Errorf("manifest: load tenant %s: %w", tenantID, err)
	}
	if t.Status != tenant.StatusActive {
		return RuntimeManifest{
			TenantID:     t.ID,
			TenantStatus: t.Status,
			SchemaName:   tenant.SchemaNameFor(t.ID),
		}, fmt.Errorf("%w: tenant %s status=%s", ErrManifestTenantInactive, t.ID, t.Status)
	}

	td, err := r.source.LoadByDomain(ctx, tenantID, domainID)
	if err != nil {
		return RuntimeManifest{}, fmt.Errorf("manifest: load tenant domain %s: %w", tenantID, err)
	}

	limits := tenant.PlanFor(t)

	return RuntimeManifest{
		TenantID:             t.ID,
		DomainID:             td.DomainID,
		TenantStatus:         t.Status,
		SchemaName:           tenant.SchemaNameFor(t.ID),
		DomainVersion:        td.Version,
		Plan:                 limits.Plan,
		MonthlyProposalLimit: limits.MonthlyProposalLimit,
		Features:             map[string]bool{},
	}, nil
}
