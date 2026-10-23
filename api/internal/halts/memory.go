package halts

import (
	"context"
	"sync"
	"time"
)

// MemoryStore is the in-memory Store for tests and local runs.
type MemoryStore struct {
	mu     sync.Mutex
	halts  map[[2]string]Halt
	events []Event
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore { return &MemoryStore{halts: map[[2]string]Halt{}} }

func (s *MemoryStore) Halt(_ context.Context, tenant, agent string, at time.Time, by string) (Halt, bool, error) {
	if err := checkAgent(agent); err != nil {
		return Halt{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := [2]string{tenant, agent}
	h := s.halts[k]
	if h.On {
		return h, false, nil
	}
	h = Halt{TenantID: tenant, AgentID: agent, On: true, HaltedAt: at.UTC(), HaltedBy: by, ResumedAt: h.ResumedAt, ResumedBy: h.ResumedBy}
	s.halts[k] = h
	return h, true, nil
}

func (s *MemoryStore) Resume(_ context.Context, tenant, agent string, at time.Time, by string) (Halt, bool, error) {
	if err := checkAgent(agent); err != nil {
		return Halt{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := [2]string{tenant, agent}
	h, ok := s.halts[k]
	if !ok || !h.On {
		return h, false, nil
	}
	t := at.UTC()
	h.On, h.ResumedAt, h.ResumedBy = false, &t, by
	s.halts[k] = h
	return h, true, nil
}

func (s *MemoryStore) Get(_ context.Context, tenant, agent string) (Halt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.halts[[2]string{tenant, agent}]
	if !ok {
		return Halt{TenantID: tenant, AgentID: agent}, nil
	}
	return h, nil
}

func (s *MemoryStore) AckPlanque(_ context.Context, tenant, agent string, haltedAt, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := [2]string{tenant, agent}
	h, ok := s.halts[k]
	if ok && h.On && h.HaltedAt.Equal(haltedAt.UTC()) && h.PlanqueAck == nil {
		t := at.UTC()
		h.PlanqueAck = &t
		s.halts[k] = h
	}
	return nil
}

func (s *MemoryStore) Log(_ context.Context, e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.At = e.At.UTC()
	s.events = append(s.events, e)
	return nil
}

func (s *MemoryStore) Events(_ context.Context, tenant string, limit int) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for i := len(s.events) - 1; i >= 0 && len(out) < limit; i-- {
		if s.events[i].TenantID == tenant {
			out = append(out, s.events[i])
		}
	}
	return out, nil
}
