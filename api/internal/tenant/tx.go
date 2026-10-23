package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the subset of *pgxpool.Pool / pgx.Tx the tx-aware
// mutators use. Both the pool and a transaction satisfy it, so a
// caller can run a tenant state change in the SAME transaction as an
// operator-audit insert.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// SetStatusQ runs the status update on the given querier (pool or
// tx). Identical SQL to PostgresStore.SetStatus; factored out so an
// operator mutation + its audit row can share one transaction.
func SetStatusQ(ctx context.Context, q Querier, id string, status Status, provisionedAt *time.Time) error {
	const stmt = `
		UPDATE ` + MetaSchema + `.tenants
		SET status = $1,
		    provisioned_at = COALESCE($2, provisioned_at)
		WHERE id = $3
	`
	tag, err := q.Exec(ctx, stmt, string(status), provisionedAt, id)
	if err != nil {
		return fmt.Errorf("update tenant status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetMetadataKeyQ merges one non-nil key into the metadata JSONB on
// the given querier. A nil/empty value is rejected (the operator
// console only sets values). Mirrors PostgresStore.SetMetadata's
// jsonb-null-safe merge.
func SetMetadataKeyQ(ctx context.Context, q Querier, id, key string, value any) error {
	if id == "" {
		return ErrNotFound
	}
	if key == "" {
		return fmt.Errorf("tenant: SetMetadataKeyQ requires a non-empty key")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode metadata value: %w", err)
	}
	const stmt = `
		UPDATE ` + MetaSchema + `.tenants
		SET metadata = COALESCE(NULLIF(metadata, 'null'::jsonb), '{}'::jsonb) || jsonb_build_object($1::text, $2::jsonb)
		WHERE id = $3
	`
	tag, err := q.Exec(ctx, stmt, key, string(encoded), id)
	if err != nil {
		return fmt.Errorf("set tenant metadata: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
