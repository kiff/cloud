package operatoralerts

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Querier is the subset of *pgxpool.Pool / pgx.Tx the tx-aware status
// update uses, so an alert ack/resolve can run in the SAME
// transaction as its operator-audit insert.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SetStatusQ transitions an alert on the given querier (pool or tx).
// Same SQL as PostgresStore.SetStatus.
func SetStatusQ(ctx context.Context, q Querier, id, status, by, note string) (Alert, error) {
	row := q.QueryRow(ctx, sqlSetAlertStatus, id, status, by, note)
	a, err := scanAlert(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Alert{}, ErrNotFound
	}
	return a, err
}
