package guard

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	DefaultProject     = "default"
	DefaultEnvironment = "dev"
	DefaultWorkflow    = "default"

	ModeObserve = "observe"
	ModeEnforce = "enforce"
)

var (
	ErrInvalidConnection   = errors.New("guard: invalid connection")
	ErrObservationNotFound = errors.New("guard: observation not found")
)

// Connection is one guard runtime identity inside a tenant. A single
// organization can have many agents and many workflows; this row is the
// lightweight heartbeat that lets Cloud group them before we ingest tool
// observations.
type Connection struct {
	TenantID    string `json:"tenant_id"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	AgentID     string `json:"agent_id"`
	Workflow    string `json:"workflow"`
	Adapter     string `json:"adapter"`
	SDKVersion  string `json:"sdk_version,omitempty"`
	Mode        string `json:"mode"`
	// KeyID is the API key that most recently authenticated this runtime
	// (server-stamped from the request identity, not client-supplied).
	// It lets the dashboard revoke exactly this runtime's key — the only
	// control lever, since the guard API has no per-runtime mutation.
	// Empty when the runtime authenticated via a session JWT.
	KeyID       string    `json:"key_id,omitempty"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	SeenCount   uint64    `json:"seen_count"`
}

// ToolObservation is one tool/action candidate observed by a connected
// guard runtime. It is intentionally small and structured: adapters know
// tool names and parameter schemas deterministically, while Studio can
// turn those facts into a reviewable draft without asking the user to
// start from YAML.
type ToolObservation struct {
	Name              string          `json:"name"`
	Description       string          `json:"description,omitempty"`
	ParameterSchema   json.RawMessage `json:"parameter_schema,omitempty"`
	EntityArg         string          `json:"entity_arg,omitempty"`
	Action            string          `json:"action,omitempty"`
	EntityType        string          `json:"entity_type,omitempty"`
	Required          []string        `json:"required,omitempty"`
	ObservedCallCount uint64          `json:"observed_call_count,omitempty"`
}

// Observation is the latest observed tool catalog for one runtime.
type Observation struct {
	TenantID    string            `json:"tenant_id"`
	Project     string            `json:"project"`
	Environment string            `json:"environment"`
	AgentID     string            `json:"agent_id"`
	Workflow    string            `json:"workflow"`
	Tools       []ToolObservation `json:"tools"`
	ObservedAt  time.Time         `json:"observed_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// TouchInput is the client-provided connection heartbeat. TenantID and
// timestamps are server-controlled.
type TouchInput struct {
	Project     string
	Environment string
	AgentID     string
	Workflow    string
	Adapter     string
	SDKVersion  string
	Mode        string
	// KeyID is server-stamped from the authenticated identity's API key
	// id (handlers set it; clients cannot). Empty for JWT-authed touches.
	KeyID  string
	SeenAt time.Time
}

// ObservationInput is the client-provided observed catalog. TenantID and
// timestamps remain server-controlled.
type ObservationInput struct {
	Project     string
	Environment string
	AgentID     string
	Workflow    string
	Adapter     string
	SDKVersion  string
	Mode        string
	// KeyID is server-stamped from the authenticated identity (as with
	// TouchInput), so an observation heartbeat also refreshes the
	// runtime's revocable key.
	KeyID  string
	Tools  []ToolObservation
	SeenAt time.Time
}

func (in TouchInput) normalized(now time.Time) (TouchInput, error) {
	in.Project = cleanDefault(in.Project, DefaultProject)
	in.Environment = cleanDefault(in.Environment, DefaultEnvironment)
	in.Workflow = cleanDefault(in.Workflow, DefaultWorkflow)
	in.AgentID = strings.TrimSpace(in.AgentID)
	in.Adapter = strings.TrimSpace(strings.ToLower(in.Adapter))
	in.SDKVersion = strings.TrimSpace(in.SDKVersion)
	in.Mode = strings.TrimSpace(strings.ToLower(in.Mode))
	in.KeyID = strings.TrimSpace(in.KeyID)
	if in.SeenAt.IsZero() {
		in.SeenAt = now
	}
	in.SeenAt = in.SeenAt.UTC()

	if in.AgentID == "" {
		return TouchInput{}, errors.Join(ErrInvalidConnection, errors.New("agent_id is required"))
	}
	if in.Adapter == "" {
		return TouchInput{}, errors.Join(ErrInvalidConnection, errors.New("adapter is required"))
	}
	switch in.Mode {
	case "", ModeObserve:
		in.Mode = ModeObserve
	case ModeEnforce:
		// ok
	default:
		return TouchInput{}, errors.Join(ErrInvalidConnection, errors.New("mode must be observe or enforce"))
	}
	return in, nil
}

func (in ObservationInput) normalized(now time.Time) (ObservationInput, TouchInput, error) {
	touch, err := TouchInput{
		Project:     in.Project,
		Environment: in.Environment,
		AgentID:     in.AgentID,
		Workflow:    in.Workflow,
		Adapter:     in.Adapter,
		SDKVersion:  in.SDKVersion,
		Mode:        in.Mode,
		KeyID:       in.KeyID,
		SeenAt:      in.SeenAt,
	}.normalized(now)
	if err != nil {
		return ObservationInput{}, TouchInput{}, err
	}
	in.Project = touch.Project
	in.Environment = touch.Environment
	in.AgentID = touch.AgentID
	in.Workflow = touch.Workflow
	in.Adapter = touch.Adapter
	in.SDKVersion = touch.SDKVersion
	in.Mode = touch.Mode
	in.KeyID = touch.KeyID
	in.SeenAt = touch.SeenAt
	in.Tools = normalizeTools(in.Tools)
	return in, touch, nil
}

func normalizeTools(tools []ToolObservation) []ToolObservation {
	out := make([]ToolObservation, 0, len(tools))
	seen := map[string]bool{}
	for _, tool := range tools {
		tool.Name = strings.TrimSpace(tool.Name)
		if tool.Name == "" || seen[tool.Name] {
			continue
		}
		seen[tool.Name] = true
		tool.Description = strings.TrimSpace(tool.Description)
		tool.EntityArg = strings.TrimSpace(tool.EntityArg)
		tool.Action = strings.TrimSpace(tool.Action)
		tool.EntityType = strings.TrimSpace(tool.EntityType)
		tool.Required = cleanStrings(tool.Required)
		if len(tool.ParameterSchema) == 0 {
			tool.ParameterSchema = nil
		}
		out = append(out, tool)
	}
	return out
}

func cleanStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func cleanDefault(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	return s
}

// Store persists guard connection heartbeats.
type Store interface {
	Touch(ctx context.Context, tenantID string, input TouchInput) (Connection, error)
	List(ctx context.Context, tenantID string) ([]Connection, error)
	SaveObservation(ctx context.Context, tenantID string, input ObservationInput) (Observation, error)
	GetObservation(ctx context.Context, tenantID string, project string, environment string, agentID string, workflow string) (Observation, error)
}
