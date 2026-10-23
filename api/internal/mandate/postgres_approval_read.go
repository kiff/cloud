package mandate

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// A repeatable-read snapshot gives the view consistent consent, Cards and
// usage without waiting behind advisory locks held by live authorizations.
// Collection still uses the Authorizer's locked, transactional path.
func (s *PostgresStore) projectException(ctx context.Context, previous Exception, now time.Time) (ExceptionProjection, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ExceptionProjection{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	e, err := exceptionByID(ctx, tx, previous.TenantID, previous.ID)
	if err != nil {
		return ExceptionProjection{}, err
	}
	var ms []Mandate
	if e.Status == ExceptionApproved || e.Status == ExceptionHeld {
		ms, err = forSubject(ctx, tx, e.TenantID, e.Subject)
		if err != nil {
			return ExceptionProjection{}, err
		}
	}
	p, err := projectApproval(ctx, pgCardTx{tx: tx}, ms, e, now)
	if err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}
