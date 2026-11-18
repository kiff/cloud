package agents

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/agents.sql
var agentsSQL string

// ApplySchema creates the agents table if absent. Idempotent. Run after
// cloud_meta.tenants exists.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "agents/agents", agentsSQL); err != nil {
		return fmt.Errorf("apply agents schema: %w", err)
	}
	return nil
}

// PostgresStore is the production Store.
type PostgresStore struct{ pool *pgxpool.Pool }

// NewPostgresStore wraps pool; the caller applies the schema.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) Create(ctx context.Context, a Agent) error {
	if err := checkStored(a.ID); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cloud_meta.agents (tenant_id, id, created_at, created_by, kind, name)
		VALUES ($1, $2, $3, $4, $5, $6)`, a.TenantID, a.ID, a.CreatedAt.UTC(), a.CreatedBy, a.Kind, a.Name)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrExists
	}
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}
	return nil
}

func (s *PostgresStore) Ensure(ctx context.Context, a Agent) error {
	if err := checkStored(a.ID); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO cloud_meta.agents (tenant_id, id, created_at, created_by, kind, name)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, id) DO UPDATE SET
		       kind = CASE WHEN cloud_meta.agents.kind = '' AND EXCLUDED.kind <> '' THEN EXCLUDED.kind ELSE cloud_meta.agents.kind END,
		       name = CASE WHEN EXCLUDED.name <> '' THEN EXCLUDED.name ELSE cloud_meta.agents.name END
		 WHERE (cloud_meta.agents.kind = '' AND EXCLUDED.kind <> '') OR (EXCLUDED.name <> '' AND EXCLUDED.name <> cloud_meta.agents.name)`, a.TenantID, a.ID, a.CreatedAt.UTC(), a.CreatedBy, a.Kind, a.Name); err != nil {
		return fmt.Errorf("ensure agent: %w", err)
	}
	return nil
}

func (s *PostgresStore) List(ctx context.Context, tenantID string) ([]Agent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, created_at, created_by, archived_at, kind, name
		  FROM cloud_meta.agents
		 WHERE tenant_id = $1
		 ORDER BY created_at, id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		a := Agent{TenantID: tenantID}
		if err := rows.Scan(&a.ID, &a.CreatedAt, &a.CreatedBy, &a.ArchivedAt, &a.Kind, &a.Name); err != nil {
			return nil, fmt.Errorf("list agents: scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *PostgresStore) Archive(ctx context.Context, tenantID, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE cloud_meta.agents SET archived_at = COALESCE(archived_at, $3)
		 WHERE tenant_id = $1 AND id = $2`, tenantID, id, at.UTC())
	if err != nil {
		return fmt.Errorf("archive agent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
