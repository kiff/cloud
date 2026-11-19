package guard

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryStore is used by local dev and handler tests.
type MemoryStore struct {
	mu           sync.Mutex
	rows         map[memKey]Connection
	observations map[memKey]Observation
	now          func() time.Time
}

type memKey struct {
	TenantID    string
	Project     string
	Environment string
	AgentID     string
	Workflow    string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		rows:         map[memKey]Connection{},
		observations: map[memKey]Observation{},
		now:          func() time.Time { return time.Now().UTC() },
	}
}

func (s *MemoryStore) Touch(_ context.Context, tenantID string, input TouchInput) (Connection, error) {
	input, err := input.normalized(s.now())
	if err != nil {
		return Connection{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	k := memKey{
		TenantID:    tenantID,
		Project:     input.Project,
		Environment: input.Environment,
		AgentID:     input.AgentID,
		Workflow:    input.Workflow,
	}
	row, ok := s.rows[k]
	if !ok {
		row = Connection{
			TenantID:    tenantID,
			Project:     input.Project,
			Environment: input.Environment,
			AgentID:     input.AgentID,
			Workflow:    input.Workflow,
			FirstSeenAt: input.SeenAt,
		}
	}
	row.Adapter = input.Adapter
	row.SDKVersion = input.SDKVersion
	row.Mode = input.Mode
	// Only refresh key_id when this touch carried one, so a JWT-authed
	// heartbeat doesn't wipe the last known revocable key.
	if input.KeyID != "" {
		row.KeyID = input.KeyID
	}
	row.LastSeenAt = input.SeenAt
	row.SeenCount++
	s.rows[k] = row
	return row, nil
}

func (s *MemoryStore) SaveObservation(_ context.Context, tenantID string, input ObservationInput) (Observation, error) {
	input, touch, err := input.normalized(s.now())
	if err != nil {
		return Observation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	k := memKey{
		TenantID:    tenantID,
		Project:     input.Project,
		Environment: input.Environment,
		AgentID:     input.AgentID,
		Workflow:    input.Workflow,
	}
	row, ok := s.rows[k]
	if !ok {
		row = Connection{
			TenantID:    tenantID,
			Project:     input.Project,
			Environment: input.Environment,
			AgentID:     input.AgentID,
			Workflow:    input.Workflow,
			FirstSeenAt: input.SeenAt,
		}
	}
	row.Adapter = touch.Adapter
	row.SDKVersion = touch.SDKVersion
	row.Mode = touch.Mode
	if touch.KeyID != "" {
		row.KeyID = touch.KeyID
	}
	row.LastSeenAt = touch.SeenAt
	row.SeenCount++
	s.rows[k] = row

	obs, ok := s.observations[k]
	if !ok {
		obs = Observation{
			TenantID:    tenantID,
			Project:     input.Project,
			Environment: input.Environment,
			AgentID:     input.AgentID,
			Workflow:    input.Workflow,
			ObservedAt:  input.SeenAt,
		}
	}
	obs.Tools = append([]ToolObservation(nil), input.Tools...)
	obs.UpdatedAt = input.SeenAt
	s.observations[k] = obs
	return obs, nil
}

func (s *MemoryStore) GetObservation(_ context.Context, tenantID string, project string, environment string, agentID string, workflow string) (Observation, error) {
	project = cleanDefault(project, DefaultProject)
	environment = cleanDefault(environment, DefaultEnvironment)
	workflow = cleanDefault(workflow, DefaultWorkflow)
	agentID = strings.TrimSpace(agentID)

	s.mu.Lock()
	defer s.mu.Unlock()
	obs, ok := s.observations[memKey{
		TenantID:    tenantID,
		Project:     project,
		Environment: environment,
		AgentID:     agentID,
		Workflow:    workflow,
	}]
	if !ok {
		return Observation{}, ErrObservationNotFound
	}
	obs.Tools = append([]ToolObservation(nil), obs.Tools...)
	return obs, nil
}

func (s *MemoryStore) List(_ context.Context, tenantID string) ([]Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Connection, 0, len(s.rows))
	for _, row := range s.rows {
		if row.TenantID == tenantID {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastSeenAt.Equal(out[j].LastSeenAt) {
			return out[i].LastSeenAt.After(out[j].LastSeenAt)
		}
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		if out[i].Environment != out[j].Environment {
			return out[i].Environment < out[j].Environment
		}
		if out[i].AgentID != out[j].AgentID {
			return out[i].AgentID < out[j].AgentID
		}
		return out[i].Workflow < out[j].Workflow
	})
	return out, nil
}
