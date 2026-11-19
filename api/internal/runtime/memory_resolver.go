// Package runtime — in-memory Resolver for dev / test boots that
// don't have Postgres. The cloud's production resolver is
// CloudResolver (resolver.go); this one shares its public contract
// but uses framework-side in-memory stores so a fresh checkout
// can `go run ./cmd/api` and exercise the runtime surface without
// docker-compose up.
package runtime

import (
	"context"
	"fmt"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"
	kiffstore "github.com/kiff/kiff/pkg/kiff/store"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/tenant"
)

// MemoryResolver is the dev/test counterpart of CloudResolver. It
// uses an in-memory tenant.Store and domain.Source plus the
// framework's NewInMemoryBundle, so the runtime is self-contained
// and the per-tenant data lives in the process. Restarts wipe state.
//
// Production deployments use CloudResolver. Use MemoryResolver when
// KIFF_DATABASE_URL is unset; the binary fails to do anything
// stateful in that mode by design — the resolver is just enough to
// prove the routing tree.
type MemoryResolver struct {
	tenants  tenant.Store
	source   domain.Source
	registry *executors.Registry
	manifest ManifestResolver
}

// NewMemoryResolver wires the in-memory dependencies. The caller is
// responsible for seeding the tenant.Store (with at least one
// active tenant) and the domain.Source (with that tenant's YAML).
// Without seeding, Resolve returns the same errors as CloudResolver.
func NewMemoryResolver(tenants tenant.Store, source domain.Source, registry *executors.Registry) *MemoryResolver {
	return &MemoryResolver{
		tenants:  tenants,
		source:   source,
		registry: registry,
		manifest: NewManifestResolver(tenants, source),
	}
}

// Resolve implements Resolver. Each call rebuilds a runtime from
// the in-memory bundle, so per-tenant state does not leak across
// resolutions; the TenantRuntime cache (runtime.go) keeps the
// runtime alive across requests within a single tenant.
//
// Like CloudResolver since the manifest, the in-memory resolver asks the
// ManifestResolver first so the construction order matches what
// production runs.
func (r *MemoryResolver) Resolve(ctx context.Context, tenantID, domainID string) (Resolved, error) {
	manifest, err := r.manifest.ResolveManifest(ctx, tenantID, domainID)
	if err != nil {
		return Resolved{}, err
	}
	if manifest.TenantStatus != tenant.StatusActive {
		return Resolved{}, fmt.Errorf("%w: tenant %s status=%s", ErrManifestTenantInactive, manifest.TenantID, manifest.TenantStatus)
	}

	td, err := r.source.LoadByDomain(ctx, manifest.TenantID, manifest.DomainID)
	if err != nil {
		return Resolved{}, fmt.Errorf("load tenant domain %s/%s: %w", manifest.TenantID, manifest.DomainID, err)
	}

	parsed, err := domain.ParseAndBuild(td.YAML, r.registry)
	if err != nil {
		return Resolved{}, fmt.Errorf("parse tenant domain %s: %w", manifest.TenantID, err)
	}

	bundle := kiffstore.NewInMemoryBundle()
	cfg := kiffruntime.Config{
		Stores:           &bundle,
		PermissionPolicy: parsed.Policy,
	}
	rt, err := kiffruntime.NewForDomain(parsed.Definition, cfg)
	if err != nil {
		return Resolved{}, fmt.Errorf("build runtime for %s: %w", manifest.TenantID, err)
	}

	// Register a passthrough adapter named "cloud" so external
	// callers can POST /v1/events/raw with adapter="cloud" and have
	// the framework normalize the payload into a KIFF event without
	// per-tenant adapter configuration. v0.1: one adapter per
	// tenant; v0.2 will let tenants register their own.
	passthrough, err := kiffadapter.NewPassthroughAdapter("cloud")
	if err != nil {
		return Resolved{}, fmt.Errorf("build cloud passthrough adapter: %w", err)
	}
	if err := rt.RegisterAdapter(passthrough); err != nil {
		return Resolved{}, fmt.Errorf("register cloud adapter: %w", err)
	}
	return Resolved{Runtime: rt, Spec: parsed.Spec, DomainID: manifest.DomainID}, nil
}
