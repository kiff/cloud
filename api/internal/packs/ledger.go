package packs

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/packs.sql
var schemaSQL string

// ApplySchema creates the pack tables. Idempotent. Run after
// cloud_meta.tenants exists.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "packs/packs", schemaSQL); err != nil {
		return fmt.Errorf("apply packs schema: %w", err)
	}
	return nil
}

// Kind is how an account got a pack.
type Kind string

const (
	KindBought Kind = "bought"
	KindBonus  Kind = "bonus"
)

// Source is where a pack came from.
type Source string

const (
	SourceStripe Source = "stripe"
	SourceApple  Source = "apple"
	SourceGoogle Source = "google"
	SourceKIFF   Source = "kiff" // a bonus
)

// Grant is a pack to record.
type Grant struct {
	TenantID string
	// Pack is the pack's id in the table; "bonus" for a bonus.
	Pack  string
	Tasks int
	Kind  Kind
	// Source and SourceTxn name the store transaction; a pack is recorded
	// once per pair.
	Source    Source
	SourceTxn string
	// RequestID is the member's request the pack answers, if any.
	RequestID string
	// At is when it was bought; it lasts until Expiry(Kind, At).
	At time.Time
	// AnsweredBy is who bought it for a request, when known.
	AnsweredBy string
}

// Entry is a recorded pack.
type Entry struct {
	ID        string     `json:"id"`
	TenantID  string     `json:"-"`
	Pack      string     `json:"pack"`
	Tasks     int        `json:"tasks"`
	TasksLeft int        `json:"tasks_left"`
	BoughtAt  time.Time  `json:"bought_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	Kind      Kind       `json:"kind"`
	Source    Source     `json:"source"`
	SourceTxn string     `json:"-"`
	RequestID string     `json:"request_id,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// KeepTakeBack is how long a take-back that came before its pack is kept.
// A payment completes long before; a refund of anything else (a plan's
// invoice) never matches a pack, and goes after this.
const KeepTakeBack = 30 * 24 * time.Hour

// ErrNoTasks is a draw with no pack left to draw from.
var ErrNoTasks = errors.New("packs: no pack tasks left")

// Ledger is the record of an account's packs.
type Ledger interface {
	// Record records g once per source transaction: a repeat returns the
	// pack already recorded, with created false. When g answers an open
	// request, the request is approved with it.
	Record(ctx context.Context, g Grant) (e Entry, created bool, err error)
	// Revoke takes a refunded or revoked pack's tasks back. False when no
	// pack has that transaction yet: the take-back is then kept, and the
	// pack is recorded already taken back when its payment arrives, with
	// no tasks and answering no request.
	Revoke(ctx context.Context, source Source, txn string, at time.Time) (Entry, bool, error)
	// Allowance is the pack tasks the account has in the period that
	// started at from, at at: those drawn for tasks done from from to at,
	// and those left on packs valid at at. With the plan's included tasks,
	// it is what the account may count this period.
	Allowance(ctx context.Context, tenantID string, from, at time.Time) (drawn, left int, err error)
	// Draw draws one counted task, the call key at at, from the oldest
	// pack valid at at that has tasks left; ErrNoTasks when none has.
	// Drawing a key again is a no-op.
	Draw(ctx context.Context, tenantID, key string, at time.Time) error
	// Valid lists the account's packs with tasks left at at, oldest first.
	Valid(ctx context.Context, tenantID string, at time.Time) ([]Entry, error)
}

func newID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// Postgres keeps the ledger in cloud_meta.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres keeps the ledger through pool; the caller applies the schema.
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

const entryCols = `id, tenant_id, pack, tasks, tasks_left, bought_at, expires_at, kind, source, source_txn, COALESCE(request_id, ''), revoked_at`

func scanEntry(row pgx.Row) (Entry, error) {
	var e Entry
	err := row.Scan(&e.ID, &e.TenantID, &e.Pack, &e.Tasks, &e.TasksLeft, &e.BoughtAt, &e.ExpiresAt, &e.Kind, &e.Source, &e.SourceTxn, &e.RequestID, &e.RevokedAt)
	return e, err
}

// Record implements Ledger.
func (p *Postgres) Record(ctx context.Context, g Grant) (Entry, bool, error) {
	if g.TenantID == "" || g.Pack == "" || g.Tasks <= 0 || g.SourceTxn == "" {
		return Entry{}, false, fmt.Errorf("packs: record: a pack needs its account, pack, tasks and transaction")
	}
	if g.Kind == "" {
		g.Kind = KindBought
	}
	at := g.At.UTC()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Entry{}, false, fmt.Errorf("packs: record: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockTxn(ctx, tx, g.Source, g.SourceTxn); err != nil {
		return Entry{}, false, fmt.Errorf("packs: record: %w", err)
	}
	var req *string
	if g.RequestID != "" {
		req = &g.RequestID
	}
	e, err := scanEntry(tx.QueryRow(ctx, `
		INSERT INTO cloud_meta.task_packs (id, tenant_id, pack, tasks, tasks_left, bought_at, expires_at, kind, source, source_txn, request_id)
		VALUES ($1, $2, $3, $4, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (source, source_txn) DO NOTHING
		RETURNING `+entryCols,
		newID("pk_"), g.TenantID, g.Pack, g.Tasks, at, Expiry(g.Kind, at), string(g.Kind), string(g.Source), g.SourceTxn, req))
	if errors.Is(err, pgx.ErrNoRows) {
		e, err = scanEntry(tx.QueryRow(ctx, `SELECT `+entryCols+` FROM cloud_meta.task_packs WHERE source = $1 AND source_txn = $2`,
			string(g.Source), g.SourceTxn))
		if err != nil {
			return Entry{}, false, fmt.Errorf("packs: record: %w", err)
		}
		return e, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("packs: record: %w", err)
	}
	// Taken back before it was recorded (a refund or a lost dispute that
	// came first): recorded taken back, answering no request.
	var back time.Time
	err = tx.QueryRow(ctx, `DELETE FROM cloud_meta.task_pack_takebacks WHERE source = $1 AND source_txn = $2 RETURNING taken_at`,
		string(g.Source), g.SourceTxn).Scan(&back)
	switch {
	case err == nil:
		if e, err = scanEntry(tx.QueryRow(ctx, `UPDATE cloud_meta.task_packs SET tasks_left = 0, revoked_at = $2, request_id = NULL
			WHERE id = $1 RETURNING `+entryCols, e.ID, back)); err != nil {
			return Entry{}, false, fmt.Errorf("packs: record: take back: %w", err)
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return Entry{}, false, fmt.Errorf("packs: record: take back: %w", err)
	case g.RequestID != "":
		// The first answer counts: a request already answered stays as it is.
		if _, err := tx.Exec(ctx, `UPDATE cloud_meta.task_pack_requests
			SET status = 'approved', answered_by = NULLIF($3, ''), answered_at = $4, pack_id = $5
			WHERE tenant_id = $1 AND id = $2 AND status = 'open' AND kind = 'pack' AND expires_at > $4`,
			g.TenantID, g.RequestID, g.AnsweredBy, at, e.ID); err != nil {
			return Entry{}, false, fmt.Errorf("packs: record: answer request: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Entry{}, false, fmt.Errorf("packs: record: %w", err)
	}
	return e, true, nil
}

// Revoke implements Ledger.
func (p *Postgres) Revoke(ctx context.Context, source Source, txn string, at time.Time) (Entry, bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Entry{}, false, fmt.Errorf("packs: revoke: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockTxn(ctx, tx, source, txn); err != nil {
		return Entry{}, false, fmt.Errorf("packs: revoke: %w", err)
	}
	e, err := scanEntry(tx.QueryRow(ctx, `
		UPDATE cloud_meta.task_packs SET tasks_left = 0, revoked_at = COALESCE(revoked_at, $3)
		WHERE source = $1 AND source_txn = $2
		RETURNING `+entryCols, string(source), txn, at.UTC()))
	found := err == nil
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Not recorded yet: kept for when it is. Take-backs are a few a
		// month, so those past KeepTakeBack are pruned here, without a
		// sweeper; a failed prune leaves them for the next one.
		if _, err := tx.Exec(ctx, `INSERT INTO cloud_meta.task_pack_takebacks (source, source_txn, taken_at) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, string(source), txn, at.UTC()); err != nil {
			return Entry{}, false, fmt.Errorf("packs: revoke: keep: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM cloud_meta.task_pack_takebacks WHERE taken_at < $1`, at.Add(-KeepTakeBack).UTC()); err != nil {
			return Entry{}, false, fmt.Errorf("packs: revoke: prune: %w", err)
		}
		e = Entry{}
	case err != nil:
		return Entry{}, false, fmt.Errorf("packs: revoke: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Entry{}, false, fmt.Errorf("packs: revoke: %w", err)
	}
	return e, found, nil
}

// lockTxn serializes recording and taking back one store transaction, so a
// take-back that comes while its payment is being recorded is never lost.
func lockTxn(ctx context.Context, tx pgx.Tx, source Source, txn string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, string(source)+":"+txn)
	return err
}

// Allowance implements Ledger.
func (p *Postgres) Allowance(ctx context.Context, tenantID string, from, at time.Time) (int, int, error) {
	var drawn, left int
	err := p.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM cloud_meta.task_pack_draws WHERE tenant_id = $1 AND call_at >= $2 AND call_at <= $3),
		       (SELECT COALESCE(sum(tasks_left), 0) FROM cloud_meta.task_packs
		         WHERE tenant_id = $1 AND revoked_at IS NULL AND bought_at <= $3 AND expires_at > $3)`,
		tenantID, from.UTC(), at.UTC()).Scan(&drawn, &left)
	if err != nil {
		return 0, 0, fmt.Errorf("packs: allowance: %w", err)
	}
	return drawn, left, nil
}

// Draw implements Ledger. One transaction: the pack's row is locked, so
// two draws never take its last task twice, and a key is drawn once.
func (p *Postgres) Draw(ctx context.Context, tenantID, key string, at time.Time) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("packs: draw: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cloud_meta.task_pack_draws WHERE tenant_id = $1 AND call_key = $2)`,
		tenantID, key).Scan(&done); err != nil {
		return fmt.Errorf("packs: draw: %w", err)
	}
	if done {
		return nil
	}
	var id string
	err = tx.QueryRow(ctx, `
		SELECT id FROM cloud_meta.task_packs
		 WHERE tenant_id = $1 AND revoked_at IS NULL AND tasks_left > 0 AND bought_at <= $2 AND expires_at > $2
		 ORDER BY bought_at, id LIMIT 1 FOR UPDATE`, tenantID, at.UTC()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoTasks
	}
	if err != nil {
		return fmt.Errorf("packs: draw: %w", err)
	}
	// The draw first: of two draws of one key at once, only the one that
	// records it takes a task.
	tag, err := tx.Exec(ctx, `INSERT INTO cloud_meta.task_pack_draws (tenant_id, call_key, pack_id, call_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, tenantID, key, id, at.UTC())
	if err != nil {
		return fmt.Errorf("packs: draw: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE cloud_meta.task_packs SET tasks_left = tasks_left - 1 WHERE id = $1`, id); err != nil {
		return fmt.Errorf("packs: draw: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("packs: draw: %w", err)
	}
	return nil
}

// Valid implements Ledger.
func (p *Postgres) Valid(ctx context.Context, tenantID string, at time.Time) ([]Entry, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+entryCols+` FROM cloud_meta.task_packs
		WHERE tenant_id = $1 AND revoked_at IS NULL AND tasks_left > 0 AND bought_at <= $2 AND expires_at > $2
		ORDER BY bought_at, id`, tenantID, at.UTC())
	if err != nil {
		return nil, fmt.Errorf("packs: valid: %w", err)
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("packs: valid: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
