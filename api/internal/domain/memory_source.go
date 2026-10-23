package domain

import (
	"context"
	"sync"
)

// MemorySource is the in-process Source used by unit tests of
// higher layers (runtime resolver, end-to-end test stubs). It now
// holds a slice of domains per tenant: index 0 is
// the tenant's default — the row Save upserts and Load resolves —
// and Create appends additional non-default domains after it. A
// single-domain tenant therefore behaves exactly as before.
type MemorySource struct {
	mu  sync.RWMutex
	all map[string][]TenantDomain
}

// NewMemorySource returns an empty source.
func NewMemorySource() *MemorySource {
	return &MemorySource{all: map[string][]TenantDomain{}}
}

// withDerivedIdentity defends entries seeded before identity
// tracking: a reader never sees an empty identity.
func withDerivedIdentity(t TenantDomain) TenantDomain {
	if t.DomainID == "" {
		t.DomainID, t.Slug = DeriveStorageIdentity(t.YAML)
	}
	return t
}

// Load implements Source. Returns the tenant's default domain
// (index 0), matching PostgresSource's default-first resolution.
func (m *MemorySource) Load(_ context.Context, tenantID string) (TenantDomain, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ds := m.all[tenantID]
	if len(ds) == 0 {
		return TenantDomain{}, ErrDomainNotFound
	}
	return withDerivedIdentity(ds[0]), nil
}

// LoadByDomain implements Source. An empty selector returns the
// default (index 0); a selector resolves an immutable domain_id or
// the domain's current public slug, with an exact id taking priority.
func (m *MemorySource) LoadByDomain(_ context.Context, tenantID, selector string) (TenantDomain, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ds := m.all[tenantID]
	if len(ds) == 0 {
		return TenantDomain{}, ErrDomainNotFound
	}
	if selector == "" {
		return withDerivedIdentity(ds[0]), nil
	}
	for _, d := range ds {
		dd := withDerivedIdentity(d)
		if dd.DomainID == selector {
			return dd, nil
		}
	}
	for _, d := range ds {
		dd := withDerivedIdentity(d)
		if dd.Slug == selector {
			return dd, nil
		}
	}
	return TenantDomain{}, ErrDomainNotFound
}

// List implements Source. Returns all of the tenant's domains,
// default (index 0) first, then in creation order. A tenant with no
// domains gets an empty slice (not an error).
func (m *MemorySource) List(_ context.Context, tenantID string) ([]TenantDomain, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ds := m.all[tenantID]
	out := make([]TenantDomain, 0, len(ds))
	for _, d := range ds {
		out = append(out, withDerivedIdentity(d))
	}
	return out, nil
}

// Save implements Source. Upserts the tenant's DEFAULT domain (index
// 0): the version increments, the slug follows the latest YAML, and
// the immutable domain_id is preserved across re-uploads — the same
// contract PostgresSource.Save upholds against the one-default
// upsert. A tenant with no domains yet gets this as its default.
func (m *MemorySource) Save(_ context.Context, tenantID string, yaml []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ds := m.all[tenantID]
	derivedID, slug := DeriveStorageIdentity(yaml)
	if len(ds) == 0 {
		m.all[tenantID] = []TenantDomain{{
			TenantID: tenantID,
			YAML:     append([]byte(nil), yaml...),
			Version:  1,
			DomainID: derivedID,
			Slug:     slug,
		}}
		return nil
	}
	prev := ds[0]
	domainID := prev.DomainID
	if domainID == "" {
		domainID = derivedID
	}
	ds[0] = TenantDomain{
		TenantID: tenantID,
		YAML:     append([]byte(nil), yaml...),
		Version:  prev.Version + 1,
		DomainID: domainID,
		Slug:     slug,
	}
	return nil
}

// Create implements Source. Appends an additional, non-default
// domain. Returns ErrDomainExists when the tenant already has a
// domain with the derived domain_id (create never overwrites).
func (m *MemorySource) Create(_ context.Context, tenantID string, yaml []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	domainID, slug := DeriveStorageIdentity(yaml)
	for _, d := range m.all[tenantID] {
		existing := d.DomainID
		if existing == "" {
			existing, _ = DeriveStorageIdentity(d.YAML)
		}
		if existing == domainID {
			return ErrDomainExists
		}
	}
	m.all[tenantID] = append(m.all[tenantID], TenantDomain{
		TenantID: tenantID,
		YAML:     append([]byte(nil), yaml...),
		Version:  1,
		DomainID: domainID,
		Slug:     slug,
	})
	return nil
}

// Delete implements Source. Removes all of the tenant's domains,
// matching PostgresSource.Delete (WHERE tenant_id).
func (m *MemorySource) Delete(_ context.Context, tenantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.all, tenantID)
	return nil
}

// DeleteByDomain implements Source. Removes the single matching
// domain (by domain_id); ErrDomainNotFound when absent.
func (m *MemorySource) DeleteByDomain(_ context.Context, tenantID, domainID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ds := m.all[tenantID]
	for i, d := range ds {
		existing := d.DomainID
		if existing == "" {
			existing, _ = DeriveStorageIdentity(d.YAML)
		}
		if existing == domainID {
			m.all[tenantID] = append(ds[:i:i], ds[i+1:]...)
			return nil
		}
	}
	return ErrDomainNotFound
}

// UpdateByDomain implements Source. Re-authors the addressed domain's
// yaml in place (version++), preserving domain_id and slug.
// ErrDomainNotFound when absent.
func (m *MemorySource) UpdateByDomain(_ context.Context, tenantID, domainID string, yaml []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ds := m.all[tenantID]
	for i := range ds {
		existing := ds[i].DomainID
		if existing == "" {
			existing, _ = DeriveStorageIdentity(ds[i].YAML)
		}
		if existing == domainID {
			ds[i].YAML = append([]byte(nil), yaml...)
			ds[i].Version++
			return nil
		}
	}
	return ErrDomainNotFound
}
