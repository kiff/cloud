// Package runtime holds the cloud's per-tenant wrapper around
// github.com/kiff/kiff/pkg/kiff/runtime.
//
// For tenant isolation, every request resolves to a
// tenant id and a Postgres schema; the cloud sets search_path on
// the connection and hands a *runtime.Runtime to the handler. v0.1
// is a placeholder: the type, the cache shape, and the lifecycle
// are sketched so issue #2 (this file) compiles, but actual
// per-tenant wiring lands with the tenant-provisioning issue.
package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"

	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/domain"
)

// TenantRuntime resolves a tenant id to a *kiffruntime.Runtime.
//
// At v0.1 it is empty; subsequent issues add:
//   - a *pgxpool.Pool that sets search_path per checkout (tenant isolation)
//   - a per-tenant *domain.Definition cache loaded from kiff.yaml
//   - a permission.Policy seeded from the cloud's role mapping
//   - a Metrics implementation tied to the billing store
type TenantRuntime struct {
	mu       sync.RWMutex
	cache    map[string]Resolved
	resolver Resolver
}

// Resolved is one materialized tenant domain: the framework runtime
// that answers proposals, plus the cloud-side Spec.
//
// The Spec is carried because the framework's own types deliberately
// do not know about cloud-only parts of an action contract. The
// run-context dependency is the first: `ActionContract` has no field
// for it and should not grow one just so the cloud can read it back,
// since that would put a cloud concept in the public framework API and
// force a public PR before the cloud could move. Resolving both
// together also keeps them from disagreeing — they come from one parse
// of one YAML document, not two.
type Resolved struct {
	Runtime *kiffruntime.Runtime
	Spec    domain.Spec
	// DomainID is the domain the resolver actually loaded: for an
	// empty selector, the tenant's default domain. TenantRuntime keys
	// its cache on it, so every selector that names the same domain
	// shares one runtime. Empty when the resolver cannot say.
	DomainID string
}

// Resolver builds a Resolved for a given tenant id and domain id. The
// cloud injects one at startup. An empty domainID resolves the
// tenant's default domain, so callers without a domain
// selector keep working.
type Resolver interface {
	Resolve(ctx context.Context, tenantID, domainID string) (Resolved, error)
}

// runtimeCacheKey composes the (tenant_id, domain_id) cache key. The
// NUL separator can't appear in a ULID tenant id or a slug domain id,
// so the join is unambiguous. An empty domainID (the default-domain
// path) yields a distinct, stable key per tenant.
func runtimeCacheKey(tenantID, domainID string) string {
	return tenantID + "\x00" + domainID
}

// New constructs an empty TenantRuntime that uses the given resolver
// to materialize per-tenant runtimes on first request and caches
// them for subsequent calls.
func New(r Resolver) *TenantRuntime {
	return &TenantRuntime{
		cache:    make(map[string]Resolved),
		resolver: r,
	}
}

// ErrNoResolver is returned when For is called on a TenantRuntime
// constructed without a resolver. At v0.1 this is the expected path:
// no resolver is wired yet, so callers should treat ErrNoResolver as
// "tenancy is not implemented yet" rather than a runtime failure.
var ErrNoResolver = errors.New("kiff-cloud: tenant resolver not configured")

// For returns the *kiffruntime.Runtime for the tenant's DEFAULT
// domain. Back-compat shorthand for ForDomain(ctx, tenantID, "").
func (t *TenantRuntime) For(ctx context.Context, tenantID string) (*kiffruntime.Runtime, error) {
	return t.ForDomain(ctx, tenantID, "")
}

// ForDomain returns the *kiffruntime.Runtime for (tenantID, domainID),
// building it on first request and caching per (tenant, domain). An
// empty domainID resolves the tenant's default domain.
// The cache is invalidated by ResetTenant.
func (t *TenantRuntime) ForDomain(ctx context.Context, tenantID, domainID string) (*kiffruntime.Runtime, error) {
	res, err := t.resolvedForDomain(ctx, tenantID, domainID)
	if err != nil {
		return nil, err
	}
	return res.Runtime, nil
}

// SpecFor returns the cloud-side Spec for the tenant's DEFAULT domain.
func (t *TenantRuntime) SpecFor(ctx context.Context, tenantID string) (domain.Spec, error) {
	return t.SpecForDomain(ctx, tenantID, "")
}

// SpecForDomain returns the cloud-side Spec for (tenantID, domainID),
// from the same resolution and cache entry that produced the runtime —
// so a caller reading an action's run-context dependency and
// a caller evaluating that action against the framework contract can
// never be looking at two different parses of the YAML.
func (t *TenantRuntime) SpecForDomain(ctx context.Context, tenantID, domainID string) (domain.Spec, error) {
	res, err := t.resolvedForDomain(ctx, tenantID, domainID)
	if err != nil {
		return domain.Spec{}, err
	}
	return res.Spec, nil
}

// DomainIDFor returns the immutable domain_id of the domain a selector
// resolves to: for an empty selector, the tenant's default. It comes
// from the same cache entry as the runtime, so it names the domain whose
// runtime answers the request. Unlike the YAML's name, it survives a
// rename.
func (t *TenantRuntime) DomainIDFor(ctx context.Context, tenantID, domainID string) (string, error) {
	res, err := t.resolvedForDomain(ctx, tenantID, domainID)
	if err != nil {
		return "", err
	}
	return res.DomainID, nil
}

func (t *TenantRuntime) resolvedForDomain(ctx context.Context, tenantID, domainID string) (Resolved, error) {
	if t.resolver == nil {
		return Resolved{}, ErrNoResolver
	}
	key := runtimeCacheKey(tenantID, domainID)

	t.mu.RLock()
	if res, ok := t.cache[key]; ok {
		t.mu.RUnlock()
		return res, nil
	}
	t.mu.RUnlock()

	res, err := t.resolver.Resolve(ctx, tenantID, domainID)
	if err != nil {
		return Resolved{}, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if existing, ok := t.cache[key]; ok {
		// Lost the race to another goroutine; use the cached value.
		return existing, nil
	}
	// One domain, one runtime. Entity state lives in the
	// runtime, so an unscoped request (the default domain) and a
	// request that names that same domain must share an instance;
	// otherwise events ingested through one are invisible to decisions
	// made through the other. Alias every selector to the entry for the
	// domain the resolver actually loaded.
	if res.DomainID != "" && res.DomainID != domainID {
		canonical := runtimeCacheKey(tenantID, res.DomainID)
		if existing, ok := t.cache[canonical]; ok {
			t.cache[key] = existing
			return existing, nil
		}
		t.cache[canonical] = res
	}
	t.cache[key] = res
	return res, nil
}

// ResetTenant evicts every cached runtime for a tenant (all of its
// domains). Used when a tenant's schema is dropped or one of its
// domain definitions changes; dropping all of the tenant's entries is
// the conservative choice — the next request rebuilds only what it
// needs.
func (t *TenantRuntime) ResetTenant(tenantID string) {
	prefix := tenantID + "\x00"
	t.mu.Lock()
	defer t.mu.Unlock()
	for key := range t.cache {
		if strings.HasPrefix(key, prefix) {
			delete(t.cache, key)
		}
	}
}
