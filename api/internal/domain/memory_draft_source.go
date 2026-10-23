package domain

import (
	"context"
	"sync"
	"time"
)

// MemoryDraftSource is the in-process DraftSource used by tests
// of the Studio handlers and the activate transaction.
type MemoryDraftSource struct {
	mu  sync.RWMutex
	all map[string]TenantDraft
}

// NewMemoryDraftSource returns an empty draft store.
func NewMemoryDraftSource() *MemoryDraftSource {
	return &MemoryDraftSource{all: map[string]TenantDraft{}}
}

// Load implements DraftSource.
func (m *MemoryDraftSource) Load(_ context.Context, tenantID string) (TenantDraft, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.all[tenantID]
	if !ok {
		return TenantDraft{}, ErrDraftNotFound
	}
	// Defensive copy so the caller can't mutate the slice we
	// hold internally.
	out := t
	out.YAML = append([]byte(nil), t.YAML...)
	return out, nil
}

// Save implements DraftSource. Last write wins; UpdatedAt is
// stamped on every call.
func (m *MemoryDraftSource) Save(_ context.Context, tenantID string, yaml []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.all[tenantID] = TenantDraft{
		TenantID:  tenantID,
		YAML:      append([]byte(nil), yaml...),
		UpdatedAt: time.Now().Unix(),
	}
	return nil
}

// Delete implements DraftSource.
func (m *MemoryDraftSource) Delete(_ context.Context, tenantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.all, tenantID)
	return nil
}
