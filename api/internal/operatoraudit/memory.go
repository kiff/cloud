package operatoraudit

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// MemoryStore is the in-process Store for tests and no-Postgres runs.
// Concurrent-safe. Append-only: no method mutates an existing entry.
type MemoryStore struct {
	mu   sync.RWMutex
	rows []Entry
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

// Record implements Store.
func (m *MemoryStore) Record(_ context.Context, e Entry) error {
	if e.ID == "" {
		e.ID = newID()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, e)
	return nil
}

// List implements Store.
func (m *MemoryStore) List(_ context.Context, targetTenantID string, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 100
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Entry, 0, len(m.rows))
	for _, e := range m.rows {
		if targetTenantID != "" && e.TargetTenantID != targetTenantID {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func newID() string {
	return "opa_" + ulid.Make().String()
}
