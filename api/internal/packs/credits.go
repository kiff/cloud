package packs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CreditGrant is a pack of credits to record.
type CreditGrant struct {
	TenantID string
	// Pack is the pack's id in the table; "bonus" for a bonus.
	Pack    string
	Credits int64
	Kind    Kind
	// Source and SourceTxn name the store transaction, or KIFF's grant for
	// a bonus; a pack is recorded once per pair.
	Source    Source
	SourceTxn string
	// RequestID is the member's request the pack answers, if any, and
	// AnsweredBy who bought it for them, when known.
	RequestID  string
	AnsweredBy string
	// At is when it was bought. A pack of credits never expires.
	At time.Time
}

// CreditPack is a recorded pack of credits.
type CreditPack struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"-"`
	Pack        string     `json:"pack"`
	Credits     int64      `json:"credits"`
	CreditsLeft int64      `json:"credits_left"`
	BoughtAt    time.Time  `json:"bought_at"`
	Kind        Kind       `json:"kind"`
	Source      Source     `json:"source"`
	SourceTxn   string     `json:"-"`
	RequestID   string     `json:"request_id,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	// TakenBack is what a take-back removed: the credits the pack had left
	// when RevokeCredits ran (0 when it was already taken back). The
	// credits it had are then spent and stay spent, as with any consumable
	// refunded after use.
	TakenBack int64 `json:"-"`
}

// CreditLedger is the record of an account's packs of credits.
type CreditLedger interface {
	// RecordCredits records g once per source transaction: a repeat
	// returns the pack already recorded, with created false. When g
	// answers an open request, the request is approved with it.
	RecordCredits(ctx context.Context, g CreditGrant) (p CreditPack, created bool, err error)
	// RevokeCredits takes back what a refunded or revoked pack has left,
	// and says how much in TakenBack; what was spent stays spent. False when no pack has that transaction yet: the take-back is then
	// kept, and the pack is recorded already taken back when it arrives.
	RevokeCredits(ctx context.Context, source Source, txn string, at time.Time) (CreditPack, bool, error)
	// Credits are the pack credits drawn for runs that ended in [from, to),
	// and those left on the account's packs now.
	Credits(ctx context.Context, tenantID string, from, to time.Time) (drawn, left int64, err error)
	// CreditPacks lists the account's packs with credits left, oldest
	// first: the order they're drawn in.
	CreditPacks(ctx context.Context, tenantID string) ([]CreditPack, error)
}

// BoughtRead says which packs an account has bought. *Postgres and
// *Memory are each one.
type BoughtRead interface {
	// BoughtPacks are the ids of the packs of credits the account has
	// ever had recorded as bought, refunded ones too: a pack sold once
	// per account isn't offered again after a refund, or buying it,
	// spending it and refunding it would bring it back.
	BoughtPacks(ctx context.Context, tenantID string) (map[string]bool, error)
}

var (
	_ BoughtRead = (*Postgres)(nil)
	_ BoughtRead = (*Memory)(nil)
)

// BoughtPacks implements BoughtRead.
func (p *Postgres) BoughtPacks(ctx context.Context, tenantID string) (map[string]bool, error) {
	rows, err := p.pool.Query(ctx, `SELECT DISTINCT pack FROM cloud_meta.credit_packs WHERE tenant_id = $1 AND kind = $2`, tenantID, KindBought)
	if err != nil {
		return nil, fmt.Errorf("packs: bought packs: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("packs: bought packs: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// Settled is how a run's credits were taken from the balance.
type Settled struct {
	// Included came from the period's included credits, Drawn from the
	// packs, and Over was past both: Solo's free overshoot.
	Included, Drawn, Over int64
	// Before: the run was settled already, and nothing changed.
	Before bool
}

// ErrNoRun is a run to settle that isn't kept.
var ErrNoRun = errors.New("packs: no such run")

const creditCols = `id, tenant_id, pack, credits, credits_left, bought_at, kind, source, source_txn, COALESCE(request_id, ''), revoked_at`

func scanCreditPack(row pgx.Row) (CreditPack, error) {
	var c CreditPack
	err := row.Scan(&c.ID, &c.TenantID, &c.Pack, &c.Credits, &c.CreditsLeft, &c.BoughtAt, &c.Kind, &c.Source, &c.SourceTxn, &c.RequestID, &c.RevokedAt)
	c.BoughtAt = c.BoughtAt.UTC()
	return c, err
}

// RecordCredits implements CreditLedger.
func (p *Postgres) RecordCredits(ctx context.Context, g CreditGrant) (CreditPack, bool, error) {
	if g.TenantID == "" || g.Pack == "" || g.Credits <= 0 || g.SourceTxn == "" {
		return CreditPack{}, false, fmt.Errorf("packs: record credits: a pack needs its account, pack, credits and transaction")
	}
	if g.Kind == "" {
		g.Kind = KindBought
	}
	at := g.At.UTC()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return CreditPack{}, false, fmt.Errorf("packs: record credits: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockTxn(ctx, tx, g.Source, g.SourceTxn); err != nil {
		return CreditPack{}, false, fmt.Errorf("packs: record credits: %w", err)
	}
	var req *string
	if g.RequestID != "" {
		req = &g.RequestID
	}
	c, err := scanCreditPack(tx.QueryRow(ctx, `
		INSERT INTO cloud_meta.credit_packs (id, tenant_id, pack, credits, credits_left, bought_at, kind, source, source_txn, request_id)
		VALUES ($1, $2, $3, $4, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (source, source_txn) DO NOTHING
		RETURNING `+creditCols,
		newID("cp_"), g.TenantID, g.Pack, g.Credits, at, string(g.Kind), string(g.Source), g.SourceTxn, req))
	if errors.Is(err, pgx.ErrNoRows) {
		if c, err = scanCreditPack(tx.QueryRow(ctx, `SELECT `+creditCols+` FROM cloud_meta.credit_packs WHERE source = $1 AND source_txn = $2`,
			string(g.Source), g.SourceTxn)); err != nil {
			return CreditPack{}, false, fmt.Errorf("packs: record credits: %w", err)
		}
		return c, false, nil
	}
	if err != nil {
		return CreditPack{}, false, fmt.Errorf("packs: record credits: %w", err)
	}
	// Taken back before it was recorded: recorded taken back, answering
	// no request.
	var back time.Time
	err = tx.QueryRow(ctx, `DELETE FROM cloud_meta.task_pack_takebacks WHERE source = $1 AND source_txn = $2 RETURNING taken_at`,
		string(g.Source), g.SourceTxn).Scan(&back)
	switch {
	case err == nil:
		if c, err = scanCreditPack(tx.QueryRow(ctx, `UPDATE cloud_meta.credit_packs SET credits_left = 0, revoked_at = $2, request_id = NULL
			WHERE id = $1 RETURNING `+creditCols, c.ID, back)); err != nil {
			return CreditPack{}, false, fmt.Errorf("packs: record credits: take back: %w", err)
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return CreditPack{}, false, fmt.Errorf("packs: record credits: take back: %w", err)
	case g.RequestID != "":
		// The first answer counts: a request already answered stays as it is.
		if _, err := tx.Exec(ctx, `UPDATE cloud_meta.task_pack_requests
			SET status = 'approved', answered_by = NULLIF($3, ''), answered_at = $4, pack_id = $5
			WHERE tenant_id = $1 AND id = $2 AND status = 'open' AND kind = 'pack' AND expires_at > $4`,
			g.TenantID, g.RequestID, g.AnsweredBy, at, c.ID); err != nil {
			return CreditPack{}, false, fmt.Errorf("packs: record credits: answer request: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return CreditPack{}, false, fmt.Errorf("packs: record credits: %w", err)
	}
	return c, true, nil
}

// RevokeCredits implements CreditLedger.
func (p *Postgres) RevokeCredits(ctx context.Context, source Source, txn string, at time.Time) (CreditPack, bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return CreditPack{}, false, fmt.Errorf("packs: revoke credits: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockTxn(ctx, tx, source, txn); err != nil {
		return CreditPack{}, false, fmt.Errorf("packs: revoke credits: %w", err)
	}
	var left int64
	err = tx.QueryRow(ctx, `SELECT credits_left FROM cloud_meta.credit_packs WHERE source = $1 AND source_txn = $2 FOR UPDATE`,
		string(source), txn).Scan(&left)
	var c CreditPack
	if err == nil {
		c, err = scanCreditPack(tx.QueryRow(ctx, `
			UPDATE cloud_meta.credit_packs SET credits_left = 0, revoked_at = COALESCE(revoked_at, $3)
			WHERE source = $1 AND source_txn = $2
			RETURNING `+creditCols, string(source), txn, at.UTC()))
		c.TakenBack = left
	}
	found := err == nil
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := tx.Exec(ctx, `INSERT INTO cloud_meta.task_pack_takebacks (source, source_txn, taken_at) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, string(source), txn, at.UTC()); err != nil {
			return CreditPack{}, false, fmt.Errorf("packs: revoke credits: keep: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM cloud_meta.task_pack_takebacks WHERE taken_at < $1`, at.Add(-KeepTakeBack).UTC()); err != nil {
			return CreditPack{}, false, fmt.Errorf("packs: revoke credits: prune: %w", err)
		}
		c = CreditPack{}
	case err != nil:
		return CreditPack{}, false, fmt.Errorf("packs: revoke credits: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CreditPack{}, false, fmt.Errorf("packs: revoke credits: %w", err)
	}
	return c, found, nil
}

// Credits implements CreditLedger.
func (p *Postgres) Credits(ctx context.Context, tenantID string, from, to time.Time) (int64, int64, error) {
	var drawn, left int64
	if err := p.pool.QueryRow(ctx, `
		SELECT (SELECT COALESCE(SUM(credits), 0)::bigint FROM cloud_meta.credit_pack_draws WHERE tenant_id = $1 AND at >= $2 AND at < $3),
		       (SELECT COALESCE(SUM(credits_left), 0)::bigint FROM cloud_meta.credit_packs WHERE tenant_id = $1 AND revoked_at IS NULL)`,
		tenantID, from.UTC(), to.UTC()).Scan(&drawn, &left); err != nil {
		return 0, 0, fmt.Errorf("packs: credits: %w", err)
	}
	return drawn, left, nil
}

// CreditPacks implements CreditLedger.
func (p *Postgres) CreditPacks(ctx context.Context, tenantID string) ([]CreditPack, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+creditCols+` FROM cloud_meta.credit_packs
		WHERE tenant_id = $1 AND revoked_at IS NULL AND credits_left > 0 ORDER BY bought_at, id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("packs: credit packs: %w", err)
	}
	defer rows.Close()
	out := []CreditPack{}
	for rows.Next() {
		c, err := scanCreditPack(rows)
		if err != nil {
			return nil, fmt.Errorf("packs: credit packs: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SettleRun takes a counted run's credits from the balance, once: from
// the included credits left in the period [from, to) it ended in, then
// from the packs bought by the time it ended, oldest first. What's past
// both is Solo's free overshoot, kept on the run (over_credits), so a pack
// bought later never pays for it. An account's runs settle one at a time,
// under its lock, so the included credits and the packs are read as they
// are; a run settled before changes nothing. A free run is settled taking
// nothing.
func (p *Postgres) SettleRun(ctx context.Context, tenantID, run string, from, to time.Time, included int64) (Settled, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Settled{}, fmt.Errorf("packs: settle %s: %w", run, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('credits:settle:' || $1))`, tenantID); err != nil {
		return Settled{}, fmt.Errorf("packs: settle %s: %w", run, err)
	}
	var (
		counted bool
		credits int64
		ended   time.Time
		settled *time.Time
	)
	err = tx.QueryRow(ctx, `SELECT counted, credits, ended_at, settled_at FROM cloud_meta.billable_runs
		WHERE tenant_id = $1 AND run_id = $2`, tenantID, run).Scan(&counted, &credits, &ended, &settled)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settled{}, ErrNoRun
	}
	if err != nil {
		return Settled{}, fmt.Errorf("packs: settle %s: %w", run, err)
	}
	if settled != nil {
		return Settled{Before: true}, nil
	}
	var s Settled
	if counted && credits > 0 {
		// What the period's settled runs took from the included credits:
		// their credits, less what they drew and what was past the balance.
		var took, over, drawn int64
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(credits), 0)::bigint, COALESCE(SUM(over_credits), 0)::bigint FROM cloud_meta.billable_runs
			 WHERE tenant_id = $1 AND counted AND settled_at IS NOT NULL AND ended_at >= $2 AND ended_at < $3`,
			tenantID, from.UTC(), to.UTC()).Scan(&took, &over); err != nil {
			return Settled{}, fmt.Errorf("packs: settle %s: period: %w", run, err)
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(credits), 0)::bigint FROM cloud_meta.credit_pack_draws
			WHERE tenant_id = $1 AND at >= $2 AND at < $3`, tenantID, from.UTC(), to.UTC()).Scan(&drawn); err != nil {
			return Settled{}, fmt.Errorf("packs: settle %s: draws: %w", run, err)
		}
		s.Included = min(credits, max(included-(took-drawn-over), 0))
		rest := credits - s.Included
		for rest > 0 {
			var (
				id   string
				left int64
			)
			err := tx.QueryRow(ctx, `
				SELECT id, credits_left FROM cloud_meta.credit_packs
				 WHERE tenant_id = $1 AND revoked_at IS NULL AND credits_left > 0 AND bought_at <= $2
				 ORDER BY bought_at, id LIMIT 1 FOR UPDATE`, tenantID, ended.UTC()).Scan(&id, &left)
			if errors.Is(err, pgx.ErrNoRows) {
				break
			}
			if err != nil {
				return Settled{}, fmt.Errorf("packs: settle %s: pack: %w", run, err)
			}
			take := min(rest, left)
			if _, err := tx.Exec(ctx, `INSERT INTO cloud_meta.credit_pack_draws (tenant_id, run_id, pack_id, credits, at) VALUES ($1, $2, $3, $4, $5)`,
				tenantID, run, id, take, ended.UTC()); err != nil {
				return Settled{}, fmt.Errorf("packs: settle %s: draw: %w", run, err)
			}
			if _, err := tx.Exec(ctx, `UPDATE cloud_meta.credit_packs SET credits_left = credits_left - $2 WHERE id = $1`, id, take); err != nil {
				return Settled{}, fmt.Errorf("packs: settle %s: draw: %w", run, err)
			}
			s.Drawn, rest = s.Drawn+take, rest-take
		}
		s.Over = rest
	}
	if _, err := tx.Exec(ctx, `UPDATE cloud_meta.billable_runs SET settled_at = NOW(), over_credits = $3
		WHERE tenant_id = $1 AND run_id = $2`, tenantID, run, s.Over); err != nil {
		return Settled{}, fmt.Errorf("packs: settle %s: %w", run, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Settled{}, fmt.Errorf("packs: settle %s: %w", run, err)
	}
	return s, nil
}

// StoreSources are the stores whose packs Reset store billing sets aside.
var StoreSources = []Source{SourceApple, SourceGoogle}

// StorePacksSetAside is what Reset store billing set aside on an account:
// the packs it bought through the App Store or Google Play that still had
// credits, and those credits.
type StorePacksSetAside struct {
	Packs   int   `json:"packs"`
	Credits int64 `json:"credits"`
}

// RowQuerier is a pool or a transaction.
type RowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SetAsideStorePacksQ sets aside, on q (Reset store billing's transaction),
// tenantID's packs bought through the App Store or Google Play that still
// have credits: they're kept, with no credits left and revoked at at, as a
// refunded pack is, so they're no longer drawn or shown. A store's later
// word on one (a refund, the app sending its transaction again) finds it
// recorded and doesn't bring it back. Bonuses and Stripe's packs stay.
func SetAsideStorePacksQ(ctx context.Context, q RowQuerier, tenantID string, at time.Time) (StorePacksSetAside, error) {
	sources := make([]string, len(StoreSources))
	for i, s := range StoreSources {
		sources[i] = string(s)
	}
	var out StorePacksSetAside
	if err := q.QueryRow(ctx, `
		WITH aside AS (
			UPDATE cloud_meta.credit_packs p SET credits_left = 0, revoked_at = $3
			  FROM (SELECT id, credits_left FROM cloud_meta.credit_packs
			         WHERE tenant_id = $1 AND source = ANY($2::text[]) AND revoked_at IS NULL AND credits_left > 0
			         FOR UPDATE) old
			 WHERE p.id = old.id
			RETURNING old.credits_left)
		SELECT count(*), COALESCE(SUM(credits_left), 0)::bigint FROM aside`,
		tenantID, sources, at.UTC()).Scan(&out.Packs, &out.Credits); err != nil {
		return StorePacksSetAside{}, fmt.Errorf("packs: set aside store packs: %w", err)
	}
	return out, nil
}
