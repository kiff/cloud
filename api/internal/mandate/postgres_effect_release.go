package mandate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

func (s *PostgresStore) ReleaseEffects(ctx context.Context, e NoEffectEvidence) ([]EffectRelease, error) {
	if err := e.validate(); err != nil {
		return nil, err
	}
	// Draws are immutable and published atomically with the operation claim.
	draws, err := s.ProposalDraws(ctx, e.TenantID, e.ProposalID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	releases := []EffectRelease{}
	for _, d := range draws {
		r, ok, err := eligibleRelease(d, e)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		ids = append(ids, d.MandateID)
		releases = append(releases, r)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockCards(ctx, tx, e.TenantID, ids); err != nil {
		return nil, err
	}
	for i, r := range releases {
		terms, err := json.Marshal(r.ChargedLimits)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO cloud_meta.mandate_effect_releases
		(tenant_id,mandate_id,proposal_id,quantity_key,amount,recorded_at,source,evidence_id,reason,charged_limits)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, r.TenantID, r.MandateID, r.ProposalID, r.QuantityKey, r.Amount, r.At, r.Source, r.EvidenceID, r.Reason, terms)
		if err != nil {
			return nil, fmt.Errorf("record effect release: %w", err)
		}
		// Duplicate reports return the first evidence, rather than replacing it.
		var data []byte
		if err := tx.QueryRow(ctx, `SELECT amount,recorded_at,source,evidence_id,reason,charged_limits
		FROM cloud_meta.mandate_effect_releases WHERE tenant_id=$1 AND mandate_id=$2 AND proposal_id=$3 AND quantity_key=$4`,
			r.TenantID, r.MandateID, r.ProposalID, r.QuantityKey).Scan(&r.Amount, &r.At, &r.Source, &r.EvidenceID, &r.Reason, &data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &r.ChargedLimits); err != nil {
			return nil, err
		}
		releases[i] = r
	}
	return releases, tx.Commit(ctx)
}

func (s *PostgresStore) EffectReleases(ctx context.Context, tenant, id string) ([]EffectRelease, error) {
	rows, err := s.pool.Query(ctx, `SELECT proposal_id,quantity_key,amount,recorded_at,source,evidence_id,reason,charged_limits
	FROM cloud_meta.mandate_effect_releases WHERE tenant_id=$1 AND mandate_id=$2 ORDER BY recorded_at`, tenant, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EffectRelease{}
	for rows.Next() {
		r := EffectRelease{TenantID: tenant, MandateID: id}
		var data []byte
		if err := rows.Scan(&r.ProposalID, &r.QuantityKey, &r.Amount, &r.At, &r.Source, &r.EvidenceID, &r.Reason, &data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &r.ChargedLimits); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PostgresStore) LimitBalance(ctx context.Context, tenant string, m Mandate, a Aggregate, now time.Time, exclude string) (LimitBalance, error) {
	return readLimitBalance(ctx, s.pool, tenant, m, a, now, exclude)
}
func (t pgCardTx) LimitBalance(ctx context.Context, tenant string, m Mandate, a Aggregate, now time.Time, exclude string) (LimitBalance, error) {
	return readLimitBalance(ctx, t.tx, tenant, m, a, now, exclude)
}

func readLimitBalance(ctx context.Context, q querier, tenant string, m Mandate, a Aggregate, now time.Time, exclude string) (LimitBalance, error) {
	since, err := a.Window.Start(now, m.Loc())
	if err != nil {
		return LimitBalance{}, err
	}
	// Keep the sum in SQL: a busy Card must not load its whole ledger to decide.
	// Release terms are original effects only. An edit cannot transfer their
	// credit to another window, measure or calendar timebase.
	const stmt = `SELECT COALESCE(SUM(d.amount),0),COALESCE(SUM(CASE WHEN
	 r.recorded_at <= $6 AND r.amount=d.amount AND EXISTS (
	 SELECT 1 FROM jsonb_array_elements(r.charged_limits) t WHERE
	 t->>'Measure'=$7 AND t->>'Window'=$8 AND t->>'Location'=$9
	 AND (t->>'WindowEnd')::timestamptz>$6
	 AND ($8 <> 'calendar_day' OR (t->>'WindowStart')::timestamptz=$4)
	 ) THEN d.amount ELSE 0 END),0)
	 FROM cloud_meta.mandate_draws d LEFT JOIN cloud_meta.mandate_effect_releases r
	 ON (r.tenant_id,r.mandate_id,r.proposal_id,r.quantity_key)=(d.tenant_id,d.mandate_id,d.proposal_id,d.quantity_key)
	 WHERE d.tenant_id=$1 AND d.mandate_id=$2 AND d.quantity_key=$3
	 AND d.drawn_at >= $4 AND d.drawn_at <= $6 AND ($5='' OR d.proposal_id<>$5)`
	b := LimitBalance{}
	err = q.QueryRow(ctx, stmt, tenant, m.ID, a.Quantity.Key(), since.UTC(), exclude, now.UTC(), a.EffectiveMeasure(), a.Window, m.Loc().String()).Scan(&b.Gross, &b.Released)
	if err != nil {
		return b, fmt.Errorf("sum charged limits: %w", err)
	}
	if b.Gross < 0 || b.Released < 0 || b.Released > b.Gross {
		return b, ErrUsageUnavailable
	}
	b.Net = b.Gross - b.Released
	return b, nil
}
