package tenant

import (
	"context"
	"sync"
	"time"
)

// MemoryStore is an in-memory Store implementation. Used by unit
// tests of higher layers (handlers, runtime resolvers) that do not
// need a real Postgres instance. Production wiring uses the
// Postgres-backed store.
type MemoryStore struct {
	mu     sync.RWMutex
	byID   map[string]Tenant
	bySlug map[string]string // slug -> id
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		byID:   map[string]Tenant{},
		bySlug: map[string]string{},
	}
}

// Create implements Store.
func (s *MemoryStore) Create(_ context.Context, t Tenant) error {
	if err := t.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existingID, ok := s.bySlug[t.Slug]; ok {
		if existing, ok := s.byID[existingID]; ok && existing.Status != StatusDeleted {
			return ErrDuplicateSlug
		}
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	// Mirror PostgresStore: stamp a fresh salt at creation when
	// the caller didn't supply one. Keeps test ergonomics simple
	// (callers pass plain Tenant{} literals) and means
	// HasRedactionSalt is true for every tenant that exists in
	// memory, just like in Postgres after BackfillRedactionSalts
	// has run.
	if len(t.RedactionSalt) == 0 {
		salt, err := NewRedactionSalt()
		if err != nil {
			return err
		}
		t.RedactionSalt = salt
	}
	s.byID[t.ID] = t
	s.bySlug[t.Slug] = t.ID
	return nil
}

// Get implements Store.
func (s *MemoryStore) Get(_ context.Context, id string) (Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.byID[id]
	if !ok {
		return Tenant{}, ErrNotFound
	}
	return t, nil
}

// GetBySlug implements Store.
func (s *MemoryStore) GetBySlug(_ context.Context, slug string) (Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.bySlug[slug]
	if !ok {
		return Tenant{}, ErrNotFound
	}
	t, ok := s.byID[id]
	if !ok || t.Status == StatusDeleted || t.Status == StatusDeletedPending {
		return Tenant{}, ErrNotFound
	}
	return t, nil
}

// List implements Store.
func (s *MemoryStore) List(_ context.Context, filter ListFilter) ([]Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Tenant, 0, len(s.byID))
	for _, t := range s.byID {
		if filter.Status != "" && t.Status != filter.Status {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// SetStatus implements Store.
func (s *MemoryStore) SetStatus(_ context.Context, id string, status Status, provisionedAt *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return ErrNotFound
	}
	t.Status = status
	if provisionedAt != nil {
		stamp := provisionedAt.UTC()
		t.ProvisionedAt = &stamp
	}
	s.byID[id] = t
	return nil
}

// AddAIGoOn implements Store.
func (s *MemoryStore) AddAIGoOn(_ context.Context, id string, period time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return 0, ErrNotFound
	}
	if t.Metadata == nil {
		t.Metadata = map[string]any{}
	}
	at := period.UTC().Format(time.RFC3339)
	n := 1
	if p, _ := t.Metadata[MetadataKeyAIGoOn].(string); p == at {
		switch c := t.Metadata[MetadataKeyAIGoOns].(type) {
		case float64:
			n = int(c) + 1
		case int:
			n = c + 1
		}
	}
	t.Metadata[MetadataKeyAIGoOn] = at
	t.Metadata[MetadataKeyAIGoOns] = float64(n)
	s.byID[id] = t
	return n, nil
}

// SetMetadata implements Store.
func (s *MemoryStore) SetMetadata(_ context.Context, id, key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return ErrNotFound
	}
	if t.Metadata == nil {
		t.Metadata = map[string]any{}
	}
	if value == nil {
		delete(t.Metadata, key)
	} else {
		t.Metadata[key] = value
	}
	s.byID[id] = t
	return nil
}

// Delete implements Store.
func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return ErrNotFound
	}
	delete(s.bySlug, t.Slug)
	delete(s.byID, id)
	return nil
}

// NullRedactionSalt implements Store.
func (s *MemoryStore) NullRedactionSalt(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return ErrNotFound
	}
	t.RedactionSalt = nil
	s.byID[id] = t
	return nil
}
