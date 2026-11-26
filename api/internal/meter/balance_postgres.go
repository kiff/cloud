package meter

import (
	"context"
	"fmt"
	"time"
)

// SetupSpent is what Setup's (Coach's) runs of tenantID that ended in
// [from, to) cost, in millionths of a US dollar: what its fair use is
// measured against.
func (p *Postgres) SetupSpent(ctx context.Context, tenantID string, from, to time.Time) (int64, error) {
	var spent int64
	if err := p.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(cost_micro_usd), 0)::bigint FROM cloud_meta.billable_runs
		 WHERE tenant_id = $1 AND setup AND ended_at >= $2 AND ended_at < $3`,
		tenantID, from.UTC(), to.UTC()).Scan(&spent); err != nil {
		return 0, fmt.Errorf("meter: setup's spend: %w", err)
	}
	return spent, nil
}

// NoPlanSetupSpent is what Setup's runs of tenantID cost while it had no
// plan, those that ended at since or later (zero: all of them): what the
// no-plan allowance is measured against, once per account, or since an
// operator's reset started it again (tenant.NoPlanAllowanceFrom).
func (p *Postgres) NoPlanSetupSpent(ctx context.Context, tenantID string, since time.Time) (int64, error) {
	var spent int64
	if err := p.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(cost_micro_usd), 0)::bigint FROM cloud_meta.billable_runs
		 WHERE tenant_id = $1 AND setup AND no_plan AND ended_at >= $2`, tenantID, since.UTC()).Scan(&spent); err != nil {
		return 0, fmt.Errorf("meter: setup's spend with no plan: %w", err)
	}
	return spent, nil
}

// PendingRunTenants lists the accounts with counted runs whose credits
// aren't yet taken from their balance.
func (p *Postgres) PendingRunTenants(ctx context.Context) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT DISTINCT tenant_id FROM cloud_meta.billable_runs WHERE counted AND settled_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("meter: pending runs: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("meter: pending runs: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// UnsettledRuns lists tenantID's counted runs that ended in [from, to)
// whose credits aren't yet taken from its balance, oldest first.
func (p *Postgres) UnsettledRuns(ctx context.Context, tenantID string, from, to time.Time) ([]UnsettledRun, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT run_id, ended_at, credits FROM cloud_meta.billable_runs
		 WHERE tenant_id = $1 AND counted AND settled_at IS NULL AND ended_at >= $2 AND ended_at < $3
		 ORDER BY ended_at, run_id`, tenantID, from.UTC(), to.UTC())
	if err != nil {
		return nil, fmt.Errorf("meter: unsettled runs: %w", err)
	}
	defer rows.Close()
	var out []UnsettledRun
	for rows.Next() {
		var u UnsettledRun
		if err := rows.Scan(&u.Run, &u.Ended, &u.Credits); err != nil {
			return nil, fmt.Errorf("meter: unsettled runs: %w", err)
		}
		u.Ended = u.Ended.UTC()
		out = append(out, u)
	}
	return out, rows.Err()
}

// SettleRunsBefore settles every counted run of tenantID that ended
// before t, taking nothing: a plan whose credits come from no pack, or a
// period too old to draw for.
func (p *Postgres) SettleRunsBefore(ctx context.Context, tenantID string, t time.Time) error {
	if _, err := p.pool.Exec(ctx, `
		UPDATE cloud_meta.billable_runs SET settled_at = NOW()
		 WHERE tenant_id = $1 AND counted AND settled_at IS NULL AND ended_at < $2`, tenantID, t.UTC()); err != nil {
		return fmt.Errorf("meter: settle runs: %w", err)
	}
	return nil
}

// SettleRuns settles tenantID's named runs that are still unsettled: what
// they used is accounted for (within what's included, or billed).
func (p *Postgres) SettleRuns(ctx context.Context, tenantID string, runs []string) error {
	if len(runs) == 0 {
		return nil
	}
	if _, err := p.pool.Exec(ctx, `
		UPDATE cloud_meta.billable_runs SET settled_at = NOW()
		 WHERE tenant_id = $1 AND run_id = ANY($2::text[]) AND settled_at IS NULL`, tenantID, runs); err != nil {
		return fmt.Errorf("meter: settle runs: %w", err)
	}
	return nil
}
