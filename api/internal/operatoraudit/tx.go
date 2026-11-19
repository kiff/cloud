package operatoraudit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the subset of *pgxpool.Pool / pgx.Tx the tx-aware insert
// uses. Lets an audit row be written in the SAME transaction as the
// state change it records (the atomic-audit guarantee).
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// RecordQ inserts an audit entry on the given querier (pool or tx),
// stamping id/created_at when zero. Same SQL as PostgresStore.Record.
func RecordQ(ctx context.Context, q Querier, e Entry) error {
	if e.ID == "" {
		e.ID = newID()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	meta := e.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal audit metadata: %w", err)
	}
	var tenantID any
	if e.TargetTenantID != "" {
		tenantID = e.TargetTenantID
	}
	if _, err := q.Exec(ctx, sqlRecordAudit,
		e.ID, e.OperatorSubject, e.Action, tenantID, e.Reason, string(metaJSON), e.CreatedAt,
	); err != nil {
		return fmt.Errorf("record operator audit: %w", err)
	}
	return nil
}
