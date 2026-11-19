// Package runtime — concrete Resolver wired against the cloud's
// data plane. See runtime.go for the cache shape.
package runtime

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"
	kiffpostgres "github.com/kiff/kiff/pkg/kiff/store/postgres"

	"github.com/kiff/cloud/api/internal/approvals"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/events"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// CloudResolver builds a *kiffruntime.Runtime for a tenant by
// loading the tenant's domain YAML and wiring framework-side
// Postgres stores against a per-tenant connection pool whose
// search_path is pinned to the tenant's schema.
//
// The resolver caches the per-tenant pool. The runtime cache lives
// in TenantRuntime. After updating a tenant's domain YAML, callers
// should TenantRuntime.ResetTenant to drop the cached runtime; the
// pool is reused (the schema does not change).
type CloudResolver struct {
	databaseURL string
	tenants     tenant.Store
	source      domain.Source
	registry    *executors.Registry
	usage       *usage.Aggregator // optional; nil disables metering
	manifest    ManifestResolver  // typed projection of per-tenant config

	mu    sync.Mutex
	pools map[string]*pgxpool.Pool // tenantID -> pool with search_path = kiff_t_<id>
}

// NewCloudResolver wires the dependencies. databaseURL is the same
// URL used by the cloud's tenant Store; the resolver opens
// per-tenant pgxpools against it with AfterConnect hooks set.
//
// Usage is optional; pass nil to disable per-tenant metering. The
// runtime defaults to NoopMetrics in that case.
//
// Resolve uses NewManifestResolver(tenants, source) by default to
// answer the "what known runtime configuration does this request
// use?" question before constructing the runtime. Tests
// can substitute a different ManifestResolver via
// NewCloudResolverWithManifest when they need to drive specific
// fields without touching the underlying stores.
func NewCloudResolver(
	databaseURL string,
	tenants tenant.Store,
	source domain.Source,
	registry *executors.Registry,
	usageAgg *usage.Aggregator,
) *CloudResolver {
	return NewCloudResolverWithManifest(
		databaseURL, tenants, source, registry, usageAgg,
		NewManifestResolver(tenants, source),
	)
}

// NewCloudResolverWithManifest is the same as NewCloudResolver
// but accepts an explicit ManifestResolver. Production wiring
// uses the default; tests use this seam to drive
// inactive-tenant / missing-domain / unknown-metadata paths
// deterministically.
func NewCloudResolverWithManifest(
	databaseURL string,
	tenants tenant.Store,
	source domain.Source,
	registry *executors.Registry,
	usageAgg *usage.Aggregator,
	manifest ManifestResolver,
) *CloudResolver {
	return &CloudResolver{
		databaseURL: databaseURL,
		tenants:     tenants,
		source:      source,
		registry:    registry,
		usage:       usageAgg,
		manifest:    manifest,
		pools:       map[string]*pgxpool.Pool{},
	}
}

// Close releases all per-tenant pools. Call once at process exit.
func (r *CloudResolver) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.pools {
		p.Close()
	}
	r.pools = map[string]*pgxpool.Pool{}
}

// Resolve implements Resolver.
//
// Construction order, since the manifest:
//
//  1. ManifestResolver answers "what is this tenant's known
//     runtime configuration?" — status, schema, domain version,
//     plan/limits projection. This is the single place new
//     per-tenant knobs join the resolution path.
//  2. domain.Source.Load fetches the YAML bytes for the version
//     the manifest reports. Kept separate from the manifest
//     because the YAML is large and only the resolver needs the
//     bytes; downstream consumers of the manifest don't.
//  3. domain.ParseAndBuild materializes the framework definition.
//  4. The per-tenant pgxpool is opened (lazy, cached).
//  5. The framework runtime is built against the resulting
//     stores + permission policy + optional metrics recorder.
//
// Behavior is unchanged from the pre-manifest version: an
// inactive tenant or missing domain still returns an error;
// callers (TenantRuntime.For) cache the runtime per tenant id.
func (r *CloudResolver) Resolve(ctx context.Context, tenantID, domainID string) (Resolved, error) {
	manifest, err := r.manifest.ResolveManifest(ctx, tenantID, domainID)
	if err != nil {
		return Resolved{}, err
	}
	// Defence in depth: ResolveManifest already returns
	// ErrManifestTenantInactive for non-active rows. We keep
	// this check here so a test resolver that builds the
	// manifest by hand cannot smuggle through an inactive
	// tenant.
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

	pool, err := r.poolFor(ctx, manifest.TenantID)
	if err != nil {
		return Resolved{}, err
	}

	bundle := kiffpostgres.NewBundle(pool).AsStoreBundle()
	cfg := kiffruntime.Config{
		Stores:           &bundle,
		PermissionPolicy: parsed.Policy,
	}
	// Entity state lives in the runtime's memory, and this runtime
	// is new on every restart and cache reset. Record each event's domain
	// as it is stored, and rebuild an entity's state from its domain's
	// history the first time this runtime touches it. A domain whose YAML
	// carries no name has no domain_id to record, and keeps the
	// framework's stores as before.
	if manifest.DomainID != "" {
		eventStore, err := events.NewDomainStore(pool, manifest.DomainID, bundle.Events)
		if err != nil {
			return Resolved{}, fmt.Errorf("event store for %s/%s: %w", manifest.TenantID, manifest.DomainID, err)
		}
		machine := newHydratingMachine(parsed.Definition.StateMachine, eventStore.History)
		eventStore.BeforeAppend = machine.hydrate
		cfg.EventStore = eventStore
		cfg.StateMachine = machine
	}
	// Replace the bundle's approval store with one bound to
	// this (tenant, domain). The bundle's store has no domain predicate
	// — the framework has no domain concept — so in a multi-domain
	// tenant it would let an approval granted under one domain satisfy
	// the same action under another. ApprovalStore takes precedence
	// over Stores.Approvals in the framework's runtime config.
	approvalStore, err := approvals.NewDomainScoped(pool, manifest.DomainID)
	if err != nil {
		return Resolved{}, fmt.Errorf("approval store for %s/%s: %w", manifest.TenantID, manifest.DomainID, err)
	}
	cfg.ApprovalStore = approvalStore
	if r.usage != nil {
		cfg.Metrics = r.usage.RecorderForDomain(manifest.TenantID, manifest.DomainID)
	}
	rt, err := kiffruntime.NewForDomain(parsed.Definition, cfg)
	if err != nil {
		return Resolved{}, fmt.Errorf("build runtime for %s: %w", manifest.TenantID, err)
	}

	// Register the cloud's standard passthrough adapter. See
	// memory_resolver.go for the rationale; both resolvers register
	// the same adapter so the wire format does not depend on
	// storage mode.
	passthrough, err := kiffadapter.NewPassthroughAdapter("cloud")
	if err != nil {
		return Resolved{}, fmt.Errorf("build cloud passthrough adapter: %w", err)
	}
	if err := rt.RegisterAdapter(passthrough); err != nil {
		return Resolved{}, fmt.Errorf("register cloud adapter: %w", err)
	}
	return Resolved{Runtime: rt, Spec: parsed.Spec, DomainID: manifest.DomainID}, nil
}

// poolFor returns (and lazily creates) the per-tenant Postgres pool
// whose AfterConnect hook pins search_path to kiff_t_<tenantID>.
// Pools are kept small (MaxConns=4) so 50 tenants stay under 200
// connections.
func (r *CloudResolver) poolFor(ctx context.Context, tenantID string) (*pgxpool.Pool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.pools[tenantID]; ok {
		return p, nil
	}

	cfg, err := pgxpool.ParseConfig(r.databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse db url: %w", err)
	}

	schema := tenant.SchemaNameFor(tenantID)
	cfg.MaxConns = 4
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO "+schema+", public")
		return err
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open per-tenant pool: %w", err)
	}
	// The event-domain table is created here as well as at
	// provisioning, so tenants provisioned before it get it on first use.
	if err := events.ApplySchema(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	// Likewise the approval requesters table.
	if err := approvals.ApplyRequesterSchema(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	r.pools[tenantID] = pool
	return pool, nil
}
