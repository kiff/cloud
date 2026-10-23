package mandate

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *MemoryStore) WarningHistory(_ context.Context, tenant, id string, since, until time.Time) (WarningHistory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.failDraws != nil {
		return WarningHistory{}, s.failDraws
	}
	h := WarningHistory{}
	for _, d := range s.draws {
		if d.TenantID == tenant && d.MandateID == id && !d.DrawnAt.Before(since) && !d.DrawnAt.After(until) {
			h.Draws = append(h.Draws, d)
		}
	}
	for _, r := range s.releases {
		if r.TenantID == tenant && r.MandateID == id && !r.At.After(until) {
			h.Releases = append(h.Releases, r)
		}
	}
	for _, r := range s.revisions[key(tenant, id)] {
		if !r.At.After(until) {
			h.Revisions = append(h.Revisions, r)
		}
	}
	if len(h.Draws) > warningHistoryLimit || len(h.Releases) > warningHistoryLimit || len(h.Revisions) > 1000 {
		return WarningHistory{}, ErrWarningHistory
	}
	if len(h.Revisions) > 0 {
		h.IssuedAt = h.Revisions[0].At
	}
	return h, nil
}

func (s *PostgresStore) WarningHistory(ctx context.Context, tenant, id string, since, until time.Time) (WarningHistory, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return WarningHistory{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	h, err := readWarningHistory(ctx, tx, tenant, id, since, until)
	if err != nil {
		return h, err
	}
	return h, tx.Commit(ctx)
}

func readWarningHistory(ctx context.Context, q querier, tenant, id string, since, until time.Time) (WarningHistory, error) {
	h := WarningHistory{}
	rows, err := q.Query(ctx, `SELECT tenant_id,mandate_id,proposal_id,quantity_key,amount,drawn_at,charged_limits
	 FROM cloud_meta.mandate_draws WHERE tenant_id=$1 AND mandate_id=$2 AND drawn_at>=$3 AND drawn_at<=$4
	 ORDER BY drawn_at,proposal_id LIMIT 10001`, tenant, id, since, until)
	if err != nil {
		return h, err
	}
	for rows.Next() {
		var d Draw
		var data []byte
		if err = rows.Scan(&d.TenantID, &d.MandateID, &d.ProposalID, &d.QuantityKey, &d.Amount, &d.DrawnAt, &data); err != nil {
			break
		}
		if err = json.Unmarshal(data, &d.ChargedLimits); err != nil {
			break
		}
		h.Draws = append(h.Draws, d)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return h, err
	}
	if rowErr != nil {
		return h, rowErr
	}
	if len(h.Draws) > warningHistoryLimit {
		return h, ErrWarningHistory
	}
	rows, err = q.Query(ctx, `SELECT r.proposal_id,r.quantity_key,r.amount,r.recorded_at,r.charged_limits
	 FROM cloud_meta.mandate_effect_releases r JOIN cloud_meta.mandate_draws d
	 ON d.tenant_id=r.tenant_id AND d.mandate_id=r.mandate_id AND d.proposal_id=r.proposal_id AND d.quantity_key=r.quantity_key
	 WHERE r.tenant_id=$1 AND r.mandate_id=$2 AND d.drawn_at>=$3 AND d.drawn_at<=$4 AND r.recorded_at<=$4
	 LIMIT 10001`, tenant, id, since, until)
	if err != nil {
		return h, err
	}
	for rows.Next() {
		r := EffectRelease{TenantID: tenant, MandateID: id}
		var data []byte
		if err = rows.Scan(&r.ProposalID, &r.QuantityKey, &r.Amount, &r.At, &data); err != nil {
			break
		}
		if err = json.Unmarshal(data, &r.ChargedLimits); err != nil {
			break
		}
		h.Releases = append(h.Releases, r)
	}
	rowErr = rows.Err()
	rows.Close()
	if err != nil {
		return h, err
	}
	if rowErr != nil {
		return h, rowErr
	}
	if len(h.Releases) > warningHistoryLimit {
		return h, ErrWarningHistory
	}
	rows, err = q.Query(ctx, `SELECT changed_at,aggregates FROM cloud_meta.mandate_revisions WHERE tenant_id=$1 AND mandate_id=$2 AND changed_at<=$3 ORDER BY revision LIMIT 1001`, tenant, id, until)
	if err != nil {
		return h, err
	}
	for rows.Next() {
		var r Revision
		var data []byte
		if err = rows.Scan(&r.At, &data); err != nil {
			break
		}
		if err = json.Unmarshal(data, &r.Aggregates); err != nil {
			break
		}
		h.Revisions = append(h.Revisions, r)
	}
	rowErr = rows.Err()
	rows.Close()
	if err != nil {
		return h, err
	}
	if rowErr != nil {
		return h, rowErr
	}
	if len(h.Revisions) > 1000 {
		return h, ErrWarningHistory
	}
	if len(h.Revisions) > 0 {
		h.IssuedAt = h.Revisions[0].At
	}
	return h, nil
}
