package mandate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// policyOf is what is stored for a Card's policy: an unset policy is
// refuse, the original behaviour.
func policyOf(m Mandate) string {
	if m.OnExceed == OnExceedOwner {
		return OnExceedOwner
	}
	return OnExceedRefuse
}

const exceptionColumns = `
		SELECT tenant_id, id, proposal_id, subject, action, entity_id, fingerprint,
		       request, breaches, status, reason, created_at, resolved_by, resolved_at, consumed_at, expires_at,
		       accounting_version, legacy_approval, covering_cards, prior_approvals, external_answer, refusal
		  FROM cloud_meta.mandate_exceptions`

func scanExceptions(rows pgx.Rows) ([]Exception, error) {
	defer rows.Close()
	var out []Exception
	for rows.Next() {
		var e Exception
		var req, breaches []byte
		var legacy, prior, external, refusal []byte
		if err := rows.Scan(&e.TenantID, &e.ID, &e.ProposalID, &e.Subject, &e.Action, &e.EntityID, &e.Fingerprint,
			&req, &breaches, &e.Status, &e.Reason, &e.CreatedAt, &e.ResolvedBy, &e.ResolvedAt, &e.ConsumedAt, &e.ExpiresAt,
			&e.AccountingVersion, &legacy, &e.Covering, &prior, &external, &refusal); err != nil {
			return nil, fmt.Errorf("scan exception: %w", err)
		}
		if err := json.Unmarshal(req, &e.Request); err != nil {
			return nil, fmt.Errorf("decode exception request: %w", err)
		}
		if err := json.Unmarshal(breaches, &e.Breaches); err != nil {
			return nil, fmt.Errorf("decode exception breaches: %w", err)
		}
		if len(legacy) > 0 {
			if err := json.Unmarshal(legacy, &e.LegacyApproval); err != nil {
				return nil, fmt.Errorf("decode legacy approval: %w", err)
			}
		}
		if err := json.Unmarshal(prior, &e.PriorApprovals); err != nil {
			return nil, fmt.Errorf("decode prior approvals: %w", err)
		}
		if len(external) > 0 {
			if err := json.Unmarshal(external, &e.ExternalAnswer); err != nil {
				return nil, err
			}
		}
		if len(refusal) > 0 {
			if err := json.Unmarshal(refusal, &e.Refusal); err != nil {
				return nil, fmt.Errorf("decode refusal: %w", err)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func getException(ctx context.Context, q querier, tenantID, proposalID string) (Exception, bool, error) {
	rows, err := q.Query(ctx, exceptionColumns+` WHERE tenant_id = $1 AND proposal_id = $2`, tenantID, proposalID)
	if err != nil {
		return Exception{}, false, fmt.Errorf("load exception: %w", err)
	}
	es, err := scanExceptions(rows)
	if err != nil || len(es) == 0 {
		return Exception{}, false, err
	}
	return es[0], true, nil
}

func exceptionByID(ctx context.Context, q querier, tenantID, id string) (Exception, error) {
	return loadException(ctx, q, exceptionColumns+` WHERE tenant_id = $1 AND id = $2`, tenantID, id)
}

// exceptionForUpdate is exceptionByID that also locks the row until the
// transaction ends, so the expiry sweep (SKIP LOCKED) leaves a call being
// answered to its answer, and an answer that waits behind the sweep reads
// what the sweep wrote.
func exceptionForUpdate(ctx context.Context, tx pgx.Tx, tenantID, id string) (Exception, error) {
	return loadException(ctx, tx, exceptionColumns+` WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id)
}

func loadException(ctx context.Context, q querier, query, tenantID, id string) (Exception, error) {
	rows, err := q.Query(ctx, query, tenantID, id)
	if err != nil {
		return Exception{}, fmt.Errorf("load exception: %w", err)
	}
	es, err := scanExceptions(rows)
	if err != nil {
		return Exception{}, err
	}
	if len(es) == 0 {
		return Exception{}, ErrExceptionNotFound
	}
	return es[0], nil
}

func saveException(ctx context.Context, q querier, e Exception) error {
	req, err := json.Marshal(e.Request)
	if err != nil {
		return fmt.Errorf("marshal exception request: %w", err)
	}
	breaches, err := json.Marshal(e.Breaches)
	if err != nil {
		return fmt.Errorf("marshal exception breaches: %w", err)
	}
	legacy, err := json.Marshal(e.LegacyApproval)
	if err != nil {
		return fmt.Errorf("marshal legacy approval: %w", err)
	}
	history := e.PriorApprovals
	if history == nil {
		history = []ApprovalRecord{}
	}
	prior, err := json.Marshal(history)
	if err != nil {
		return fmt.Errorf("marshal prior approvals: %w", err)
	}
	external, err := json.Marshal(e.ExternalAnswer)
	if err != nil {
		return err
	}
	refusal, err := json.Marshal(e.Refusal)
	if err != nil {
		return fmt.Errorf("marshal refusal: %w", err)
	}
	const stmt = `
		INSERT INTO cloud_meta.mandate_exceptions
			(tenant_id, id, proposal_id, subject, action, entity_id, fingerprint,
			 request, breaches, status, reason, created_at, resolved_by, resolved_at, consumed_at, expires_at,
			 accounting_version, legacy_approval, covering_cards, prior_approvals, external_answer, refusal)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
		ON CONFLICT (tenant_id, id) DO UPDATE SET
			fingerprint = EXCLUDED.fingerprint, request = EXCLUDED.request,
			breaches = EXCLUDED.breaches, status = EXCLUDED.status, reason = EXCLUDED.reason,
			created_at = EXCLUDED.created_at, resolved_by = EXCLUDED.resolved_by,
			resolved_at = EXCLUDED.resolved_at, consumed_at = EXCLUDED.consumed_at,
			expires_at = EXCLUDED.expires_at, accounting_version = EXCLUDED.accounting_version,
			legacy_approval = EXCLUDED.legacy_approval, covering_cards = EXCLUDED.covering_cards,
			prior_approvals = EXCLUDED.prior_approvals, external_answer = EXCLUDED.external_answer,
			refusal = EXCLUDED.refusal`
	if _, err := q.Exec(ctx, stmt, e.TenantID, e.ID, e.ProposalID, e.Subject, e.Action, e.EntityID, e.Fingerprint,
		req, breaches, string(e.Status), e.Reason, e.CreatedAt.UTC(), e.ResolvedBy, e.ResolvedAt, e.ConsumedAt, e.ExpiresAt,
		e.AccountingVersion, legacy, nonNilStrings(e.Covering), prior, external, refusal); err != nil {
		return fmt.Errorf("save exception: %w", err)
	}
	return nil
}

func nonNilStrings(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// redeem claims the proposal id, writes draws and entries and marks the
// exception consumed, on q (a transaction the caller commits).
func redeem(ctx context.Context, q querier, e Exception, draws []Draw, entries []ExceptionEntry, at time.Time) error {
	tag, err := q.Exec(ctx, `
		INSERT INTO cloud_meta.mandate_proposal_claims (tenant_id, proposal_id, claimed_at, accounting_version)
		VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, e.TenantID, e.ProposalID, at.UTC(), CumulativeAccountingVersion)
	if err != nil {
		return fmt.Errorf("redeem: claim proposal: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProposalSpent
	}
	for _, d := range draws {
		if err := writeDraw(ctx, q, d); err != nil {
			return fmt.Errorf("redeem: draw on %s: %w", d.MandateID, err)
		}
	}
	for _, en := range entries {
		if _, err := q.Exec(ctx, `
			INSERT INTO cloud_meta.mandate_exception_entries
				(tenant_id, exception_id, mandate_id, proposal_id, quantity_key, amount, recorded_at, action, entity_id, subject, accounting_version)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			en.TenantID, en.ExceptionID, en.MandateID, en.ProposalID, en.QuantityKey, en.Amount, en.RecordedAt.UTC(),
			en.Action, en.EntityID, en.Subject, en.AccountingVersion); err != nil {
			return fmt.Errorf("redeem: entry on %s: %w", en.MandateID, err)
		}
	}
	e.Status = ExceptionConsumed
	t := at.UTC()
	e.ConsumedAt = &t
	return saveException(ctx, q, e)
}

// invalidateFor withdraws every open exception naming a Card: held ones
// stop waiting, approved-but-unused ones stop authorizing. Called inside
// the transaction that revokes or moves the Card, under its lock.
func invalidateFor(ctx context.Context, q querier, tenantID, mandateID, reason string, at time.Time) error {
	_, err := q.Exec(ctx, `
		UPDATE cloud_meta.mandate_exceptions
		   SET prior_approvals = CASE WHEN status = 'approved' AND resolved_at IS NOT NULL
               THEN prior_approvals || jsonb_build_array(jsonb_build_object(
                 'By', resolved_by, 'At', resolved_at, 'Reason', reason,
                 'AccountingVersion', accounting_version, 'Breaches', breaches,
                 'external_answer', external_answer))
               WHEN external_answer->>'decision' = 'defer'
               THEN prior_approvals || jsonb_build_array(jsonb_build_object(
                 'By', external_answer->>'principal', 'At', external_answer->'at',
                 'Reason', external_answer->>'reason',
                 'AccountingVersion', accounting_version, 'Breaches', breaches,
                 'external_answer', external_answer))
               ELSE prior_approvals END,
               external_answer = CASE WHEN (status = 'approved' AND resolved_at IS NOT NULL)
                 OR external_answer->>'decision' = 'defer'
                 THEN NULL ELSE external_answer END,
               status = 'invalidated', reason = $3, resolved_by = '', resolved_at = $4
		 WHERE tenant_id = $1 AND status IN ('held', 'approved')
		   AND EXISTS (SELECT 1 FROM jsonb_array_elements(breaches) b WHERE b->>'MandateID' = $2)`,
		tenantID, mandateID, reason, at.UTC())
	if err != nil {
		return fmt.Errorf("invalidate exceptions for %s: %w", mandateID, err)
	}
	return nil
}

// invalidateHoldersFor is invalidateFor for the exceptions of some agents
// only: those that stopped holding a shared Card.
func invalidateHoldersFor(ctx context.Context, q querier, tenantID, mandateID string, agents []string, reason string, at time.Time) error {
	_, err := q.Exec(ctx, `
		UPDATE cloud_meta.mandate_exceptions
		   SET prior_approvals = CASE WHEN status = 'approved' AND resolved_at IS NOT NULL
               THEN prior_approvals || jsonb_build_array(jsonb_build_object(
                 'By', resolved_by, 'At', resolved_at, 'Reason', reason,
                 'AccountingVersion', accounting_version, 'Breaches', breaches,
                 'external_answer', external_answer))
               WHEN external_answer->>'decision' = 'defer'
               THEN prior_approvals || jsonb_build_array(jsonb_build_object(
                 'By', external_answer->>'principal', 'At', external_answer->'at',
                 'Reason', external_answer->>'reason',
                 'AccountingVersion', accounting_version, 'Breaches', breaches,
                 'external_answer', external_answer))
               ELSE prior_approvals END,
               external_answer = CASE WHEN (status = 'approved' AND resolved_at IS NOT NULL)
                 OR external_answer->>'decision' = 'defer'
                 THEN NULL ELSE external_answer END,
               status = 'invalidated', reason = $3, resolved_by = '', resolved_at = $4
		 WHERE tenant_id = $1 AND status IN ('held', 'approved')
		   AND subject = ANY($5)
		   AND EXISTS (SELECT 1 FROM jsonb_array_elements(breaches) b WHERE b->>'MandateID' = $2)`,
		tenantID, mandateID, reason, at.UTC(), agents)
	if err != nil {
		return fmt.Errorf("invalidate exceptions for %s: %w", mandateID, err)
	}
	return nil
}

func (t pgCardTx) GetException(ctx context.Context, tenantID, proposalID string) (Exception, bool, error) {
	return getException(ctx, t.tx, tenantID, proposalID)
}

func (t pgCardTx) SaveException(ctx context.Context, e Exception) error {
	return saveException(ctx, t.tx, e)
}

func (t pgCardTx) Redeem(ctx context.Context, e Exception, draws []Draw, entries []ExceptionEntry, at time.Time) error {
	return redeem(ctx, t.tx, e, draws, entries, at)
}

// The Store-level forms are used only by callers without a Card
// transaction; the authorizer reaches these through WithCards.

func (s *PostgresStore) GetException(ctx context.Context, tenantID, proposalID string) (Exception, bool, error) {
	return getException(ctx, s.pool, tenantID, proposalID)
}

func (s *PostgresStore) SaveException(ctx context.Context, e Exception) error {
	return saveException(ctx, s.pool, e)
}

func (s *PostgresStore) Redeem(ctx context.Context, e Exception, draws []Draw, entries []ExceptionEntry, at time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("redeem: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := redeem(ctx, tx, e, draws, entries, at); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) Exceptions(ctx context.Context, tenantID string, status ExceptionStatus) ([]Exception, error) {
	q := exceptionColumns + ` WHERE tenant_id = $1 AND ($2 = '' OR status = $2) ORDER BY created_at DESC, id`
	rows, err := s.pool.Query(ctx, q, tenantID, string(status))
	if err != nil {
		return nil, fmt.Errorf("list exceptions: %w", err)
	}
	es, err := scanExceptions(rows)
	if es == nil {
		es = []Exception{}
	}
	return es, err
}

func (s *PostgresStore) Exception(ctx context.Context, tenantID, id string) (Exception, error) {
	return exceptionByID(ctx, s.pool, tenantID, id)
}

func (s *PostgresStore) ExceptionEntries(ctx context.Context, tenantID, mandateID string, since time.Time) ([]ExceptionEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id, exception_id, mandate_id, proposal_id, quantity_key, amount, recorded_at, action, entity_id, subject, accounting_version
		  FROM cloud_meta.mandate_exception_entries
		 WHERE tenant_id = $1 AND mandate_id = $2 AND recorded_at >= $3
		 ORDER BY recorded_at, exception_id`, tenantID, mandateID, since.UTC())
	if err != nil {
		return nil, fmt.Errorf("exception entries: %w", err)
	}
	defer rows.Close()
	out := []ExceptionEntry{}
	for rows.Next() {
		var en ExceptionEntry
		if err := rows.Scan(&en.TenantID, &en.ExceptionID, &en.MandateID, &en.ProposalID, &en.QuantityKey,
			&en.Amount, &en.RecordedAt, &en.Action, &en.EntityID, &en.Subject, &en.AccountingVersion); err != nil {
			return nil, fmt.Errorf("exception entries: scan: %w", err)
		}
		out = append(out, en)
	}
	return out, rows.Err()
}

// answer runs one owner answer in a transaction holding the locks of
// every Card the exception names. The exception is read before the locks
// (to know which Cards) and again under them, so an answer and a
// redemption, revocation or move of the same Cards serialize.
func (s *PostgresStore) answer(ctx context.Context, tenantID, id string, now time.Time, fn func(pgx.Tx, *Exception) error) (Exception, error) {
	first, err := exceptionByID(ctx, s.pool, tenantID, id)
	if err != nil {
		return Exception{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Exception{}, fmt.Errorf("answer exception: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ids := first.MandateIDs()
	sort.Strings(ids)
	if err := lockCards(ctx, tx, tenantID, ids); err != nil {
		return Exception{}, err
	}
	e, err := exceptionForUpdate(ctx, tx, tenantID, id)
	if err != nil {
		return Exception{}, err
	}
	reader := pgCardTx{tx: tx}
	if err := lockExternalApprovalPolicy(ctx, reader, e); err != nil {
		return Exception{}, err
	}
	changed, err := refreshExternalApproval(ctx, reader, &e, now)
	if err != nil {
		return Exception{}, err
	}
	if err := fn(tx, &e); err != nil {
		// Expiry is checked before any answer/Card writes. Preserve the
		// withdrawn approval's history even though the new answer is refused.
		if changed && errors.Is(err, ErrExceptionExpired) {
			if writeErr := saveException(ctx, tx, e); writeErr != nil {
				return Exception{}, writeErr
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return Exception{}, commitErr
			}
		}
		return Exception{}, err
	}
	if err := saveException(ctx, tx, e); err != nil {
		return Exception{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Exception{}, fmt.Errorf("answer exception: commit: %w", err)
	}
	return e, nil
}

func (s *PostgresStore) ApproveOnce(ctx context.Context, tenantID, id, by string, now time.Time) (Exception, error) {
	return s.answer(ctx, tenantID, id, now, func(_ pgx.Tx, e *Exception) error {
		requireFreshConsent(e)
		if e.Expired(now) {
			return ErrExceptionExpired
		}
		if e.Status != ExceptionHeld {
			return ErrExceptionState
		}
		if !e.CanApproveOnce() {
			return ErrCumulativeApproval
		}
		e.AccountingVersion = CumulativeAccountingVersion
		resolve(e, ExceptionApproved, by, now)
		return nil
	})
}

func (s *PostgresStore) Reject(ctx context.Context, tenantID, id, by string, now time.Time, r Refusal) (Exception, error) {
	return s.answer(ctx, tenantID, id, now, func(tx pgx.Tx, e *Exception) error {
		if e.Expired(now) {
			return ErrExceptionExpired
		}
		if e.Status != ExceptionHeld && e.Status != ExceptionApproved {
			return ErrExceptionState
		}
		cards, err := forSubject(ctx, tx, tenantID, e.Subject)
		if err != nil {
			return err
		}
		if err := refuseWith(e, r, cards); err != nil {
			return err
		}
		resolve(e, ExceptionRejected, by, now)
		return nil
	})
}

func (s *PostgresStore) ExpireHeld(ctx context.Context, before time.Time, limit int) ([]Exception, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("expire holds: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Expiries older than ExpiryTellWindow (holds nobody retried, from
	// before the sweep existed) are recorded quietly: expired, and marked
	// told so they leave the sweep, but not returned, so nothing is
	// announced late. Same fields as expire().
	window := before.Add(-ExpiryTellWindow).UTC()
	if _, err := tx.Exec(ctx, `
		UPDATE cloud_meta.mandate_exceptions
		   SET status = CASE WHEN status = 'held' THEN 'expired' ELSE status END,
		       reason = CASE WHEN status = 'held' THEN 'not answered before the hold expired' ELSE reason END,
		       resolved_by = CASE WHEN status = 'held' THEN '' ELSE resolved_by END,
		       resolved_at = CASE WHEN status = 'held' THEN expires_at ELSE resolved_at END,
		       expiry_told_at = $2
		 WHERE expires_at <= $1
		   AND (status = 'held' OR (status = 'expired' AND expiry_told_at IS NULL))`, window, before.UTC()); err != nil {
		return nil, fmt.Errorf("expire old holds: %w", err)
	}
	// SKIP LOCKED: a call being answered right now (answer() locks its
	// row) is left to its answer.
	rows, err := tx.Query(ctx, exceptionColumns+`
		 WHERE expires_at <= $1 AND expires_at > $3
		   AND (status = 'held' OR (status = 'expired' AND expiry_told_at IS NULL))
		 ORDER BY expires_at
		 LIMIT $2
		 FOR UPDATE SKIP LOCKED`, before.UTC(), limit, window)
	if err != nil {
		return nil, fmt.Errorf("expire holds: %w", err)
	}
	es, err := scanExceptions(rows)
	if err != nil {
		return nil, err
	}
	for i := range es {
		if es[i].Status == ExceptionHeld {
			expire(&es[i])
			if err := saveException(ctx, tx, es[i]); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE cloud_meta.mandate_exceptions SET expiry_told_at = $3
			 WHERE tenant_id = $1 AND id = $2`, es[i].TenantID, es[i].ID, before.UTC()); err != nil {
			return nil, fmt.Errorf("expire holds: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("expire holds: commit: %w", err)
	}
	return es, nil
}

func (s *PostgresStore) Withdraw(ctx context.Context, tenantID, id, agent string, now time.Time) (Exception, error) {
	return s.answer(ctx, tenantID, id, now, func(_ pgx.Tx, e *Exception) error { return withdraw(e, agent, now) })
}

func (s *PostgresStore) ChangeCard(ctx context.Context, tenantID, id, by string, now time.Time) (Exception, error) {
	return s.ChangeCardRoom(ctx, tenantID, id, by, now, RoomThisWindow, time.Time{})
}
func (s *PostgresStore) ChangeCardRoom(ctx context.Context, tenantID, id, by string, now time.Time, duration RoomDuration, until time.Time) (Exception, error) {
	return s.answer(ctx, tenantID, id, now, func(tx pgx.Tx, e *Exception) error {
		if e.Expired(now) {
			return ErrExceptionExpired
		}
		if e.Status != ExceptionHeld {
			return ErrExceptionState
		}
		cards, err := forSubject(ctx, tx, tenantID, e.Subject)
		if err != nil {
			return err
		}
		usage := usageFor(ctx, pgCardTx{tx: tx}, tenantID, e.ProposalID)
		named := breachedIDs(e.Breaches)
		for _, m := range cards {
			if !named[m.ID] {
				continue
			}
			if !m.Active(now) || !m.HeldBy(e.Subject) {
				return ErrExceptionState
			}
			changed, ok, err := roomForHold(m, *e, usage, by, now, duration, until)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if err := updateTerms(ctx, tx, changed, by); err != nil {
				return err
			}
		}
		resolve(e, ExceptionChanged, by, now)
		return nil
	})
}

// updateTerms writes a Card's new grants and aggregates and records the
// revision, inside the caller's transaction (which holds its lock).
func updateTerms(ctx context.Context, tx pgx.Tx, m Mandate, by string) error {
	m = resolvedMeasures(m)
	grants, err := json.Marshal(m.Grants)
	if err != nil {
		return err
	}
	aggs, err := json.Marshal(m.Aggregates)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE cloud_meta.mandates SET grants = $3, aggregates = $4
		 WHERE tenant_id = $1 AND id = $2`, m.TenantID, m.ID, grants, aggs); err != nil {
		return fmt.Errorf("change card %s: %w", m.ID, err)
	}
	return insertRevision(ctx, tx, m, by)
}

func resolve(e *Exception, status ExceptionStatus, by string, now time.Time) {
	if e.ExternalAnswer != nil && e.ExternalAnswer.Decision == "defer" || status != ExceptionApproved && status != ExceptionConsumed {
		preserveApproval(e)
	}
	t := now.UTC()
	e.Status, e.ResolvedBy, e.ResolvedAt = status, by, &t
}
