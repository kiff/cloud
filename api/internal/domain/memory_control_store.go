package domain

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryControlStore is the in-process ControlStore for tests.
type MemoryControlStore struct {
	mu       sync.RWMutex
	controls []Control
}

// NewMemoryControlStore returns an empty in-memory store.
func NewMemoryControlStore() *MemoryControlStore {
	return &MemoryControlStore{}
}

// Create implements ControlStore.
func (m *MemoryControlStore) Create(_ context.Context, c Control) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Supersede the current active control for this tenant in the SAME
	// (domain, environment). Activations are per-(tenant, domain,
	// environment) (domain added for multi-domain), so
	// activating one domain does not supersede another domain's
	// control, and activating prod does not supersede active staging.
	c = c.WithIdentityDefaults()
	maxVersion := 0
	for i := range m.controls {
		if m.controls[i].TenantID == c.TenantID &&
			m.controls[i].Domain == c.Domain &&
			m.controls[i].Environment == c.Environment {
			if m.controls[i].Version > maxVersion {
				maxVersion = m.controls[i].Version
			}
			if m.controls[i].Status == "active" {
				m.controls[i].Status = "superseded"
			}
		}
	}
	if c.ActivatedAt.IsZero() {
		c.ActivatedAt = time.Now().UTC()
	}
	// Monotonic version per (tenant, domain, environment) lineage.
	c.Version = maxVersion + 1
	c.Status = "active"
	m.controls = append(m.controls, c)
	return nil
}

// SetMode implements ControlStore: flips the active control's posture.
func (m *MemoryControlStore) SetMode(_ context.Context, tenantID, controlID, mode string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.controls {
		if m.controls[i].TenantID == tenantID && m.controls[i].ID == controlID && m.controls[i].Status == "active" {
			m.controls[i].Mode = mode
			return nil
		}
	}
	return ErrControlNotFound
}

// List implements ControlStore.
func (m *MemoryControlStore) List(_ context.Context, tenantID string) ([]Control, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Control
	for _, c := range m.controls {
		if c.TenantID == tenantID {
			cp := c
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ActivatedAt.After(out[j].ActivatedAt)
	})
	return out, nil
}
