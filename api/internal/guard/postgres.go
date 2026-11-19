package guard

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/guard_connections.sql
var guardConnectionsSQL string

func SchemaSQL() string { return guardConnectionsSQL }

func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "guard/guard_connections", guardConnectionsSQL); err != nil {
		return fmt.Errorf("apply guard connections schema: %w", err)
	}
	return nil
}

type PostgresStore struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{
		pool: pool,
		now:  func() time.Time { return time.Now().UTC() },
	}
}

func (s *PostgresStore) Touch(ctx context.Context, tenantID string, input TouchInput) (Connection, error) {
	input, err := input.normalized(s.now())
	if err != nil {
		return Connection{}, err
	}
	const stmt = `
		INSERT INTO cloud_meta.guard_connections
			(tenant_id, project, environment, agent_id, workflow, adapter, sdk_version, mode, key_id, first_seen_at, last_seen_at, seen_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10, 1)
		ON CONFLICT (tenant_id, project, environment, agent_id, workflow)
		DO UPDATE SET
			adapter = EXCLUDED.adapter,
			sdk_version = EXCLUDED.sdk_version,
			mode = EXCLUDED.mode,
			-- Only refresh key_id when this touch carried one, so a
			-- JWT-authed heartbeat doesn't wipe the last known key.
			key_id = CASE WHEN EXCLUDED.key_id <> '' THEN EXCLUDED.key_id
			              ELSE cloud_meta.guard_connections.key_id END,
			last_seen_at = EXCLUDED.last_seen_at,
			seen_count = cloud_meta.guard_connections.seen_count + 1
		RETURNING tenant_id, project, environment, agent_id, workflow, adapter, sdk_version, mode, key_id, first_seen_at, last_seen_at, seen_count
	`
	row := s.pool.QueryRow(ctx, stmt,
		tenantID, input.Project, input.Environment, input.AgentID, input.Workflow,
		input.Adapter, input.SDKVersion, input.Mode, input.KeyID, input.SeenAt,
	)
	return scanConnection(row)
}

func (s *PostgresStore) List(ctx context.Context, tenantID string) ([]Connection, error) {
	const stmt = `
		SELECT tenant_id, project, environment, agent_id, workflow, adapter, sdk_version, mode, key_id, first_seen_at, last_seen_at, seen_count
		FROM cloud_meta.guard_connections
		WHERE tenant_id = $1
		ORDER BY last_seen_at DESC, project, environment, agent_id, workflow
	`
	rows, err := s.pool.Query(ctx, stmt, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list guard connections: %w", err)
	}
	defer rows.Close()

	out := []Connection{}
	for rows.Next() {
		row, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *PostgresStore) SaveObservation(ctx context.Context, tenantID string, input ObservationInput) (Observation, error) {
	input, touch, err := input.normalized(s.now())
	if err != nil {
		return Observation{}, err
	}
	if _, err := s.Touch(ctx, tenantID, touch); err != nil {
		return Observation{}, err
	}
	tools, err := json.Marshal(input.Tools)
	if err != nil {
		return Observation{}, fmt.Errorf("marshal guard observation tools: %w", err)
	}
	const stmt = `
		INSERT INTO cloud_meta.guard_observations
			(tenant_id, project, environment, agent_id, workflow, tools, observed_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $7)
		ON CONFLICT (tenant_id, project, environment, agent_id, workflow)
		DO UPDATE SET
			tools = EXCLUDED.tools,
			updated_at = EXCLUDED.updated_at
		RETURNING tenant_id, project, environment, agent_id, workflow, tools, observed_at, updated_at
	`
	row := s.pool.QueryRow(ctx, stmt,
		tenantID, input.Project, input.Environment, input.AgentID, input.Workflow,
		string(tools), input.SeenAt,
	)
	return scanObservation(row)
}

func (s *PostgresStore) GetObservation(ctx context.Context, tenantID string, project string, environment string, agentID string, workflow string) (Observation, error) {
	project = cleanDefault(project, DefaultProject)
	environment = cleanDefault(environment, DefaultEnvironment)
	workflow = cleanDefault(workflow, DefaultWorkflow)
	agentID = strings.TrimSpace(agentID)
	const stmt = `
		SELECT tenant_id, project, environment, agent_id, workflow, tools, observed_at, updated_at
		FROM cloud_meta.guard_observations
		WHERE tenant_id = $1
			AND project = $2
			AND environment = $3
			AND agent_id = $4
			AND workflow = $5
	`
	obs, err := scanObservation(s.pool.QueryRow(ctx, stmt, tenantID, project, environment, agentID, workflow))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Observation{}, ErrObservationNotFound
		}
		return Observation{}, err
	}
	return obs, nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanConnection(row scannable) (Connection, error) {
	var c Connection
	var count int64
	if err := row.Scan(
		&c.TenantID,
		&c.Project,
		&c.Environment,
		&c.AgentID,
		&c.Workflow,
		&c.Adapter,
		&c.SDKVersion,
		&c.Mode,
		&c.KeyID,
		&c.FirstSeenAt,
		&c.LastSeenAt,
		&count,
	); err != nil {
		return Connection{}, fmt.Errorf("scan guard connection: %w", err)
	}
	c.SeenCount = uint64(count)
	return c, nil
}

func scanObservation(row scannable) (Observation, error) {
	var obs Observation
	var tools []byte
	if err := row.Scan(
		&obs.TenantID,
		&obs.Project,
		&obs.Environment,
		&obs.AgentID,
		&obs.Workflow,
		&tools,
		&obs.ObservedAt,
		&obs.UpdatedAt,
	); err != nil {
		return Observation{}, fmt.Errorf("scan guard observation: %w", err)
	}
	if err := json.Unmarshal(tools, &obs.Tools); err != nil {
		return Observation{}, fmt.Errorf("decode guard observation tools: %w", err)
	}
	return obs, nil
}
