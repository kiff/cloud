package agents

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore is the in-memory Store for tests and local dev.
type MemoryStore struct {
	mu     sync.Mutex
	agents map[string]Agent // tenant \x00 id
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore { return &MemoryStore{agents: map[string]Agent{}} }

func memKey(tenantID, id string) string { return tenantID + "\x00" + id }

func (s *MemoryStore) Create(_ context.Context, a Agent) error {
	if err := checkStored(a.ID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.agents[memKey(a.TenantID, a.ID)]; ok {
		return ErrExists
	}
	s.agents[memKey(a.TenantID, a.ID)] = a
	return nil
}

func (s *MemoryStore) Ensure(ctx context.Context, a Agent) error {
	if err := s.Create(ctx, a); err != nil && err != ErrExists {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	had := s.agents[memKey(a.TenantID, a.ID)]
	if had.Kind == "" && a.Kind != "" {
		had.Kind = a.Kind
	}
	if a.Name != "" {
		had.Name = a.Name
	}
	s.agents[memKey(a.TenantID, a.ID)] = had
	return nil
}

func (s *MemoryStore) List(_ context.Context, tenantID string) ([]Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Agent
	for _, a := range s.agents {
		if a.TenantID == tenantID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *MemoryStore) Archive(_ context.Context, tenantID, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[memKey(tenantID, id)]
	if !ok {
		return ErrNotFound
	}
	if a.ArchivedAt == nil {
		a.ArchivedAt = &at
		s.agents[memKey(tenantID, id)] = a
	}
	return nil
}
