package usage

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore is the in-process Store used by unit tests of higher
// layers. Production wiring uses the Postgres store.
type MemoryStore struct {
	mu   sync.Mutex
	rows map[memKey]uint64
}

type memKey struct {
	TenantID    string
	CounterName string
	EntityType  string
	DomainID    string
	HourBucket  time.Time
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rows: map[memKey]uint64{}}
}

// Flush implements Store.
func (m *MemoryStore) Flush(_ context.Context, counters []Counter) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range counters {
		k := memKey{c.TenantID, c.CounterName, c.EntityType, c.DomainID, c.HourBucket}
		m.rows[k] += c.Count
	}
	return nil
}

// Snapshot implements Store. Results are returned in deterministic
// order: tenant, counter, entity, hour. Tests can compare slices
// directly without sorting.
func (m *MemoryStore) Snapshot(_ context.Context, filter Filter) ([]Counter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Counter, 0, len(m.rows))
	for k, v := range m.rows {
		if filter.TenantID != "" && k.TenantID != filter.TenantID {
			continue
		}
		if filter.CounterName != "" && k.CounterName != filter.CounterName {
			continue
		}
		if !filter.HourFrom.IsZero() && k.HourBucket.Before(filter.HourFrom) {
			continue
		}
		if !filter.HourTo.IsZero() && k.HourBucket.After(filter.HourTo) {
			continue
		}
		if filter.DomainID != "" && k.DomainID != filter.DomainID {
			continue
		}
		out = append(out, Counter{
			TenantID:    k.TenantID,
			CounterName: k.CounterName,
			EntityType:  k.EntityType,
			DomainID:    k.DomainID,
			HourBucket:  k.HourBucket,
			Count:       v,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		if out[i].CounterName != out[j].CounterName {
			return out[i].CounterName < out[j].CounterName
		}
		if out[i].EntityType != out[j].EntityType {
			return out[i].EntityType < out[j].EntityType
		}
		if out[i].DomainID != out[j].DomainID {
			return out[i].DomainID < out[j].DomainID
		}
		return out[i].HourBucket.Before(out[j].HourBucket)
	})
	return out, nil
}
