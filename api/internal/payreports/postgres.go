package payreports

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/payment_reports.sql
var reportsSQL string

// ApplySchema creates the reports table if absent. Idempotent. Run after
// cloud_meta.tenants exists.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "payreports/payment_reports", reportsSQL); err != nil {
		return fmt.Errorf("apply payment reports schema: %w", err)
	}
	return nil
}

// PostgresStore is the production Store.
type PostgresStore struct{ pool *pgxpool.Pool }

// NewPostgresStore wraps pool; the caller applies the schema.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

const columns = `tenant_id, trace_id, proposal_id, agent_id, reported_at, reported_by`

// Report keeps the first report of a receipt, in one statement, so two at
// once keep one.
func (s *PostgresStore) Report(ctx context.Context, r Report) (Report, error) {
	if !r.valid() {
		return Report{}, ErrInvalid
	}
	scan := func(row pgx.Row) (Report, error) {
		var got Report
		err := row.Scan(&got.TenantID, &got.TraceID, &got.ProposalID, &got.AgentID, &got.ReportedAt, &got.ReportedBy)
		return got, err
	}
	got, err := scan(s.pool.QueryRow(ctx, `
		INSERT INTO cloud_meta.payment_reports (`+columns+`)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, trace_id) DO NOTHING
		RETURNING `+columns, r.TenantID, r.TraceID, r.ProposalID, r.AgentID, r.ReportedAt.UTC(), r.ReportedBy))
	if errors.Is(err, pgx.ErrNoRows) { // reported already
		got, err = scan(s.pool.QueryRow(ctx, `SELECT `+columns+` FROM cloud_meta.payment_reports
			WHERE tenant_id = $1 AND trace_id = $2`, r.TenantID, r.TraceID))
	}
	if err != nil {
		return Report{}, fmt.Errorf("report a payment: %w", err)
	}
	return got, nil
}

func (s *PostgresStore) Reported(ctx context.Context, tenant string, traces []string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	if len(traces) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT trace_id, reported_at FROM cloud_meta.payment_reports
		WHERE tenant_id = $1 AND trace_id = ANY($2)`, tenant, traces)
	if err != nil {
		return nil, fmt.Errorf("read payment reports: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var trace string
		var at time.Time
		if err := rows.Scan(&trace, &at); err != nil {
			return nil, err
		}
		out[trace] = at
	}
	return out, rows.Err()
}
