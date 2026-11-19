package operatoralerts

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// ErrNotFound is returned by SetStatus when no alert matches the id.
var ErrNotFound = errors.New("operatoralerts: not found")

// MemoryStore is the in-process Store for tests and no-Postgres runs.
type MemoryStore struct {
	mu  sync.RWMutex
	by  map[string]*Alert // id -> alert
	key map[string]string // tenant_id\x00kind -> id
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{by: map[string]*Alert{}, key: map[string]string{}}
}

func dedupKey(tenantID, kind string) string { return tenantID + "\x00" + kind }

// Upsert implements Store.
func (m *MemoryStore) Upsert(_ context.Context, u Upsert, now time.Time) (Alert, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := dedupKey(u.TenantID, u.Kind)
	if id, ok := m.key[k]; ok {
		a := m.by[id]
		a.Count++
		a.LastSeen = now
		a.Detail = u.Detail
		a.Severity = u.Severity
		a.TenantSlug = u.TenantSlug
		if a.Status == StatusResolved {
			a.Status = StatusOpen // re-open
			a.AckedBy = ""
			a.Note = ""
		}
		return *a, nil
	}
	a := &Alert{
		ID: "opal_" + ulid.Make().String(), TenantID: u.TenantID, TenantSlug: u.TenantSlug,
		Kind: u.Kind, Severity: u.Severity, Detail: u.Detail, Status: StatusOpen,
		Count: 1, FirstSeen: now, LastSeen: now,
	}
	m.by[a.ID] = a
	m.key[k] = a.ID
	return *a, nil
}

// List implements Store.
func (m *MemoryStore) List(_ context.Context, openOnly bool, limit int) ([]Alert, error) {
	if limit <= 0 {
		limit = 200
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Alert, 0, len(m.by))
	for _, a := range m.by {
		if openOnly && a.Status == StatusResolved {
			continue
		}
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SetStatus implements Store.
func (m *MemoryStore) SetStatus(_ context.Context, id, status, by, note string) (Alert, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.by[id]
	if !ok {
		return Alert{}, ErrNotFound
	}
	a.Status = status
	a.AckedBy = by
	a.Note = note
	return *a, nil
}
