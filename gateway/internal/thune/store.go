package thune

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Execution states: KIFF's call.
const (
	ExecHeld    = "held"
	ExecAllowed = "allowed"
	ExecSending = "sending"
	ExecSent    = "sent"
	ExecNotSent = "not_sent"
	ExecUnknown = "unknown"
	ExecRefused = "refused"
	ExecExpired = "expired"
	ExecVoid    = "void"
)

// Financial states: the money, as the processor says it.
const (
	FinNone      = "none"
	FinPending   = RefundPending
	FinSucceeded = RefundSucceeded
	FinFailed    = RefundFailed
	FinCancelled = RefundCancelled
)

// Origins of a refund seen on a charge.
const (
	OriginKIFF    = "kiff"
	OriginOutside = "outside"
)

//go:embed sql/schema.sql
var schemaSQL string

// ApplySchema creates thune's tables, in the gateway schema the
// gateway's role owns (internal/dbrole): run invocation.ApplySchema first.
// Idempotent.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, schemaSQL)
	return err
}

// Store is thune's Postgres store.
type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewStore returns a store over pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool, now: time.Now} }

// SetClock replaces the clock (tests).
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Pool is the store's pool (tests read raw rows through it).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// ErrNotFound: no adjustment has that key.
var ErrNotFound = errors.New("thune: no such adjustment")

// ClaimResult says who holds a request.
type ClaimResult struct {
	// Won: this call holds the claim now.
	Won bool
	// Holder is the call that holds it (this one when Won).
	Holder string
	// HolderExecution and HolderFinancial are the holder's two statuses,
	// for the second taf's answer ("Refunds is already handling this").
	HolderExecution string
	HolderFinancial string
}

// Claim takes the claim on id for the call key, atomically: the first
// insert wins; a released claim is taken again by an update that matches
// only a released row; the holder claiming again keeps it.
func (s *Store) Claim(ctx context.Context, id Identity, key string) (ClaimResult, error) {
	if err := id.Valid(); err != nil {
		return ClaimResult{}, err
	}
	if id.Request == "" {
		return ClaimResult{}, fmt.Errorf("thune: a case with no request is never claimed")
	}
	var holder string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO gateway.thune_claims (tenant_id, account, object, kind, request, holder, taken_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (tenant_id, account, object, kind, request) DO UPDATE
		   SET holder = EXCLUDED.holder, taken_at = EXCLUDED.taken_at, released_at = NULL
		 WHERE gateway.thune_claims.released_at IS NOT NULL OR gateway.thune_claims.holder = EXCLUDED.holder
		RETURNING holder`, id.TenantID, id.Account, id.Object, id.Kind, id.Request, key, s.now()).Scan(&holder)
	if err == nil {
		return ClaimResult{Won: true, Holder: holder}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ClaimResult{}, err
	}
	r := ClaimResult{}
	err = s.pool.QueryRow(ctx, `
		SELECT c.holder, COALESCE(a.execution, ''), COALESCE(a.financial, '')
		  FROM gateway.thune_claims c LEFT JOIN gateway.thune_adjustments a ON a.key = c.holder
		 WHERE c.tenant_id=$1 AND c.account=$2 AND c.object=$3 AND c.kind=$4 AND c.request=$5`,
		id.TenantID, id.Account, id.Object, id.Kind, id.Request).Scan(&r.Holder, &r.HolderExecution, &r.HolderFinancial)
	return r, err
}

// Release frees key's claim on id, so another taf can take the request.
// Only a call that ended not_sent releases: money may have moved otherwise.
func (s *Store) Release(ctx context.Context, id Identity, key string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE gateway.thune_claims SET released_at = $7
		 WHERE tenant_id=$1 AND account=$2 AND object=$3 AND kind=$4 AND request=$5 AND holder=$6 AND released_at IS NULL`,
		id.TenantID, id.Account, id.Object, id.Kind, id.Request, key, s.now())
	return tag.RowsAffected() == 1, err
}

// LockCharge takes the charge's lock for holder until now+lease. A lock
// whose lease passed is taken over; the holder taking it again extends it.
func (s *Store) LockCharge(ctx context.Context, tenant, account, object, holder string, lease time.Duration) (bool, error) {
	now := s.now()
	var got string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO gateway.thune_charge_locks (tenant_id, account, object, holder, until) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (tenant_id, account, object) DO UPDATE SET holder = EXCLUDED.holder, until = EXCLUDED.until
		 WHERE gateway.thune_charge_locks.until <= $6 OR gateway.thune_charge_locks.holder = EXCLUDED.holder
		RETURNING holder`, tenant, account, object, holder, now.Add(lease), now).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// UnlockCharge frees holder's lock on the charge.
func (s *Store) UnlockCharge(ctx context.Context, tenant, account, object, holder string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM gateway.thune_charge_locks WHERE tenant_id=$1 AND account=$2 AND object=$3 AND holder=$4`,
		tenant, account, object, holder)
	return err
}

// Adjustment is one refund call's record.
type Adjustment struct {
	Key        string
	Identity   Identity
	Requested  int64
	Remainder  bool
	Authorized int64
	Currency   string
	Facts      Facts
	Verdict    string
	Reasons    []string
	Execution  string
	Financial  string
	RefundID   string
	Watchable  bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

const adjCols = `key, tenant_id, account, object, kind, request, requested, remainder, authorized, currency, facts,
	verdict, reasons, execution, financial, refund_id, watchable, created_at, updated_at`

func scanAdj(row pgx.Row) (Adjustment, error) {
	var a Adjustment
	var facts []byte
	err := row.Scan(&a.Key, &a.Identity.TenantID, &a.Identity.Account, &a.Identity.Object, &a.Identity.Kind, &a.Identity.Request,
		&a.Requested, &a.Remainder, &a.Authorized, &a.Currency, &facts, &a.Verdict, &a.Reasons, &a.Execution, &a.Financial,
		&a.RefundID, &a.Watchable, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	err = json.Unmarshal(facts, &a.Facts)
	return a, err
}

// Record stores the case decided for call key. A repeat changes nothing
// and returns the first record. The execution status starts from the
// verdict: allowed, held or refused.
func (s *Store) Record(ctx context.Context, key string, c Case) (Adjustment, error) {
	exec := ExecAllowed
	switch c.Verdict {
	case VerdictAsk:
		exec = ExecHeld
	case VerdictRefuse:
		exec = ExecRefused
	}
	facts, err := json.Marshal(c.Facts)
	if err != nil {
		return Adjustment{}, err
	}
	reasons := c.Reasons
	if reasons == nil {
		reasons = []string{}
	}
	now := s.now()
	_, err = s.pool.Exec(ctx, `
		INSERT INTO gateway.thune_adjustments (key, tenant_id, account, object, kind, request, requested, remainder, authorized,
			currency, facts, facts_read_at, verdict, reasons, execution, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16)
		ON CONFLICT (key) DO NOTHING`,
		key, c.Identity.TenantID, c.Identity.Account, c.Identity.Object, c.Identity.Kind, c.Identity.Request,
		c.Requested, c.Remainder, c.Authorized, c.Facts.Currency, facts, c.Facts.ReadAt, c.Verdict, reasons, exec, now)
	if err != nil {
		return Adjustment{}, err
	}
	return s.Get(ctx, key)
}

// Get reads one adjustment.
func (s *Store) Get(ctx context.Context, key string) (Adjustment, error) {
	return scanAdj(s.pool.QueryRow(ctx, `SELECT `+adjCols+` FROM gateway.thune_adjustments WHERE key=$1`, key))
}

// InFlight are KIFF's refunds on a charge whose calls are sending, or
// ended unknown, with no refund found for them yet, each at its
// authorized amount and marked with its key; except's own call is left
// out. A case counts them as pending: an answer that was lost still took
// from the remainder until the processor or the owner says otherwise.
func (s *Store) InFlight(ctx context.Context, tenant, account, object, except string) ([]Refund, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, authorized FROM gateway.thune_adjustments
		WHERE tenant_id=$1 AND account=$2 AND object=$3 AND key <> $4 AND execution IN ('sending','unknown') AND financial='none'
		ORDER BY key`, tenant, account, object, except)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Refund
	for rows.Next() {
		var r Refund
		if err := rows.Scan(&r.Marker, &r.Amount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Refresh replaces a held adjustment's facts and authorized amount with a
// case read again, as long as it is still held or allowed: the re-read
// before sending.
func (s *Store) Refresh(ctx context.Context, key string, c Case) (bool, error) {
	facts, err := json.Marshal(c.Facts)
	if err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE gateway.thune_adjustments SET facts=$2, facts_read_at=$3, authorized=$4, updated_at=$5
		 WHERE key=$1 AND execution IN ('held','allowed')`, key, facts, c.Facts.ReadAt, c.Authorized, s.now())
	return tag.RowsAffected() == 1, err
}

// SetExecution moves key's execution status from one of from to to.
// Exactly one caller wins a move; a repeat changes nothing.
func (s *Store) SetExecution(ctx context.Context, key string, from []string, to string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var tenant string
	err = tx.QueryRow(ctx, `UPDATE gateway.thune_adjustments SET execution=$3, updated_at=$4 WHERE key=$1 AND execution = ANY($2)
		RETURNING tenant_id`, key, from, to, s.now()).Scan(&tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// A sent or unknown call is watched until its money ends.
	if to == ExecSent || to == ExecUnknown {
		if _, err := tx.Exec(ctx, `INSERT INTO gateway.thune_watch_work (key, tenant_id, available_at) VALUES ($1,$2,$3)
			ON CONFLICT (key) DO NOTHING`, key, tenant, s.now()); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

// financialMoves are the financial changes thune accepts: from none to
// any, from pending to an end. An end never changes.
var financialMoves = map[string][]string{
	FinPending:   {FinNone},
	FinSucceeded: {FinNone, FinPending},
	FinFailed:    {FinNone, FinPending, FinSucceeded},
	FinCancelled: {FinNone, FinPending},
}

// Change is one thing an observation changed.
type Change struct {
	Key string
	// Found: the call's refund was found by its marker, and an unknown
	// or still-sending call became sent.
	Found bool
	// FromFin and ToFin, when they differ, are the financial move made.
	FromFin, ToFin string
}

// Observation is what one read of a charge changed.
type Observation struct {
	Changes []Change
	// Outside are refunds first seen on the charge with no marker of
	// this account's calls: refunded outside KIFF.
	Outside []Refund
}

// Observe records what the processor said about a charge: every refund,
// whose it is, and each of this charge's adjustments' money. One
// transaction, under the charge's adjustments' row locks, so two watchers
// reading one charge change each thing once.
func (s *Store) Observe(ctx context.Context, tenant string, f Facts) (Observation, error) {
	var obs Observation
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return obs, err
	}
	defer tx.Rollback(ctx)
	now := s.now()
	rows, err := tx.Query(ctx, `SELECT key, execution, financial FROM gateway.thune_adjustments
		WHERE tenant_id=$1 AND account=$2 AND object=$3 ORDER BY key FOR UPDATE`, tenant, f.Account, f.Charge)
	if err != nil {
		return obs, err
	}
	type adj struct{ exec, fin string }
	ours := map[string]adj{}
	for rows.Next() {
		var k string
		var a adj
		if err := rows.Scan(&k, &a.exec, &a.fin); err != nil {
			rows.Close()
			return obs, err
		}
		ours[k] = a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return obs, err
	}
	for _, r := range f.Refunds {
		if r.ID == "" {
			continue
		}
		origin := OriginOutside
		if _, ok := ours[r.Marker]; ok && r.Marker != "" {
			origin = OriginKIFF
		}
		var inserted bool
		err := tx.QueryRow(ctx, `
			INSERT INTO gateway.thune_refunds (tenant_id, account, refund_id, object, amount, status, marker, origin, seen_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
			ON CONFLICT (tenant_id, account, refund_id) DO UPDATE SET status=EXCLUDED.status, updated_at=EXCLUDED.updated_at
			RETURNING (xmax = 0)`, tenant, f.Account, r.ID, f.Charge, r.Amount, r.Status, r.Marker, origin, now).Scan(&inserted)
		if err != nil {
			return obs, err
		}
		if origin == OriginOutside {
			if inserted {
				obs.Outside = append(obs.Outside, r)
			}
			continue
		}
		a := ours[r.Marker]
		c := Change{Key: r.Marker}
		if a.exec == ExecUnknown || a.exec == ExecSending || a.exec == ExecNotSent {
			// Its refund is at the processor: the call was sent, whether its
			// answer was lost (unknown), the gateway died before it heard
			// one (still sending), or the owner said it didn't happen (not
			// sent, on "Did this happen?"): the marker is the processor's
			// word. Watched until the money ends.
			if _, err := tx.Exec(ctx, `UPDATE gateway.thune_adjustments SET execution='sent', updated_at=$2 WHERE key=$1 AND execution IN ('unknown','sending','not_sent')`,
				r.Marker, now); err != nil {
				return obs, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO gateway.thune_watch_work (key, tenant_id, available_at) VALUES ($1,$2,$3)
				ON CONFLICT (key) DO NOTHING`, r.Marker, tenant, now); err != nil {
				return obs, err
			}
			c.Found = true
		}
		if slicesContains(financialMoves[r.Status], a.fin) {
			if _, err := tx.Exec(ctx, `UPDATE gateway.thune_adjustments SET financial=$2, refund_id=$3, updated_at=$4 WHERE key=$1 AND financial=$5`,
				r.Marker, r.Status, r.ID, now, a.fin); err != nil {
				return obs, err
			}
			c.FromFin, c.ToFin = a.fin, r.Status
		}
		if c.Found || c.FromFin != c.ToFin {
			obs.Changes = append(obs.Changes, c)
		}
	}
	return obs, tx.Commit(ctx)
}

func slicesContains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// RecordEvent keeps a processor event's id. It reports whether this is the
// first delivery; a redelivery is answered and changes nothing.
func (s *Store) RecordEvent(ctx context.Context, tenant, eventID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO gateway.thune_events (tenant_id, event_id, received_at) VALUES ($1,$2,$3)
		ON CONFLICT DO NOTHING`, tenant, eventID, s.now())
	return tag.RowsAffected() == 1, err
}

// Unwatchable marks every adjustment on account whose money hasn't ended
// as one thune can no longer watch (the connection was revoked), and
// returns their keys for the owner. Claims stay: money may have moved.
func (s *Store) Unwatchable(ctx context.Context, tenant, account string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `UPDATE gateway.thune_adjustments SET watchable=false, updated_at=$3
		WHERE tenant_id=$1 AND account=$2 AND watchable AND execution IN ('sent','unknown') AND financial IN ('none','pending')
		RETURNING key`, tenant, account, s.now())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Report is what tafs says about a charge, without anyone's help.
type Report struct {
	Account  string
	Charge   string
	Currency string
	Captured int64
	// Left is the remainder as last read.
	Left int64
	// KIFF are the refunds KIFF's calls made, found by their markers.
	KIFF []ReportRefund
	// Outside are refunds made outside KIFF.
	Outside []ReportRefund
	// RefundedTwice: the charge has more than one refund that counts,
	// and at least one is outside KIFF.
	RefundedTwice bool
}

// ReportRefund is one refund in a report.
type ReportRefund struct {
	RefundID string
	Key      string
	Amount   int64
	Status   string
}

// ChargeReport reports a charge as thune last saw it.
func (s *Store) ChargeReport(ctx context.Context, tenant, account, object string) (Report, error) {
	r := Report{Account: account, Charge: object}
	var facts []byte
	err := s.pool.QueryRow(ctx, `SELECT facts FROM gateway.thune_adjustments WHERE tenant_id=$1 AND account=$2 AND object=$3
		ORDER BY facts_read_at DESC LIMIT 1`, tenant, account, object).Scan(&facts)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return r, err
	}
	var f Facts
	if facts != nil {
		if err := json.Unmarshal(facts, &f); err != nil {
			return r, err
		}
	}
	r.Currency, r.Captured = f.Currency, f.Captured
	rows, err := s.pool.Query(ctx, `SELECT refund_id, marker, amount, status, origin FROM gateway.thune_refunds
		WHERE tenant_id=$1 AND account=$2 AND object=$3 ORDER BY seen_at, refund_id`, tenant, account, object)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	counting, outsideCounting := 0, 0
	var taken int64
	for rows.Next() {
		var x ReportRefund
		var origin string
		if err := rows.Scan(&x.RefundID, &x.Key, &x.Amount, &x.Status, &origin); err != nil {
			return r, err
		}
		if (Refund{Status: x.Status}).counts() {
			counting++
			taken += x.Amount
			if origin == OriginOutside {
				outsideCounting++
			}
		}
		if origin == OriginKIFF {
			r.KIFF = append(r.KIFF, x)
		} else {
			x.Key = ""
			r.Outside = append(r.Outside, x)
		}
	}
	if err := rows.Err(); err != nil {
		return r, err
	}
	r.Left = r.Captured - taken
	r.RefundedTwice = counting > 1 && outsideCounting > 0
	return r, nil
}

// AccountOf is the processor account KIFF decided a refund on charge in,
// for an event that names the charge only. known is false when KIFF never
// decided one there.
func (s *Store) AccountOf(ctx context.Context, tenant, charge string) (account string, known bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT account FROM gateway.thune_adjustments WHERE tenant_id=$1 AND object=$2
		ORDER BY created_at DESC LIMIT 1`, tenant, charge).Scan(&account)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return account, err == nil, err
}
