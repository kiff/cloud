package proofs

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/kiff/cloud/api/internal/pgschema"
)

// Retention is how long runs and overrides are kept: the 45-day strip
// with room.
const Retention = 60 * 24 * time.Hour

var (
	// ErrDuplicate: a run with this run_id is already kept. Runs are
	// append-only, so a second send never replaces the first.
	ErrDuplicate = errors.New("proofs: run already kept")
	// ErrNotFound: no such run.
	ErrNotFound = errors.New("proofs: no such run")
)

// Store keeps runs and overrides. It is append-only: there is no update,
// and the only delete is Sweep, of rows older than Retention.
type Store interface {
	// Append keeps a run. ErrDuplicate if its run_id is kept already.
	Append(ctx context.Context, r Run) error
	// RunsSince returns every run that ended at or after since.
	RunsSince(ctx context.Context, since time.Time) ([]Run, error)
	// Run returns one run.
	Run(ctx context.Context, runID string) (Run, error)
	// AppendOverride keeps an override (the store stamps ID and CreatedAt when zero).
	AppendOverride(ctx context.Context, o Override) (Override, error)
	// Overrides returns every override kept.
	Overrides(ctx context.Context) ([]Override, error)
	// Sweep deletes runs received, and overrides made, before before:
	// KIFF's own clock, never the runner's.
	Sweep(ctx context.Context, before time.Time) (int, error)
}

// MemoryStore is a Store in memory, for tests and deployments without Postgres.
type MemoryStore struct {
	mu        sync.Mutex
	runs      map[string]Run
	overrides []Override
	now       func() time.Time
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{runs: map[string]Run{}, now: time.Now}
}

func (m *MemoryStore) Append(_ context.Context, r Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[r.RunID]; ok {
		return ErrDuplicate
	}
	if r.ReceivedAt.IsZero() {
		r.ReceivedAt = m.now()
	}
	r.Checks = append([]Check(nil), r.Checks...)
	r.Rules = append(json.RawMessage(nil), r.Rules...)
	m.runs[r.RunID] = r
	return nil
}

func (m *MemoryStore) RunsSince(_ context.Context, since time.Time) ([]Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Run
	for _, r := range m.runs {
		if !r.EndedAt.Before(since) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EndedAt.Before(out[j].EndedAt) })
	return out, nil
}

func (m *MemoryStore) Run(_ context.Context, runID string) (Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[runID]
	if !ok {
		return Run{}, ErrNotFound
	}
	return r, nil
}

func (m *MemoryStore) AppendOverride(_ context.Context, o Override) (Override, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if o.ID == "" {
		o.ID = "prov_" + ulid.Make().String()
	}
	if o.CreatedAt.IsZero() {
		o.CreatedAt = m.now()
	}
	m.overrides = append(m.overrides, o)
	return o, nil
}

func (m *MemoryStore) Overrides(_ context.Context) ([]Override, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Override(nil), m.overrides...), nil
}

func (m *MemoryStore) Sweep(_ context.Context, before time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, r := range m.runs {
		if r.ReceivedAt.Before(before) {
			delete(m.runs, id)
			n++
		}
	}
	kept := m.overrides[:0]
	for _, o := range m.overrides {
		if o.CreatedAt.Before(before) {
			n++
			continue
		}
		kept = append(kept, o)
	}
	m.overrides = kept
	return n, nil
}

//go:embed sql/proofs.sql
var proofsSQL string

// ApplySchema creates cloud_meta.proof_runs and cloud_meta.proof_overrides. Idempotent.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "proofs/proofs", proofsSQL); err != nil {
		return fmt.Errorf("apply proofs schema: %w", err)
	}
	return nil
}

// PostgresStore keeps runs and overrides in cloud_meta.
type PostgresStore struct{ pool *pgxpool.Pool }

// NewPostgresStore wraps the cloud pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

const runColumns = `run_id, schema_version, app, job, tenant_id, run_trigger, thread, started_at, ended_at,
	outcome, checks, limits_held, p50_ms, p95_ms, runner_version, events_key, entry, rules, counted, received_at,
	gh_run_id, gh_run_attempt`

func (p *PostgresStore) Append(ctx context.Context, r Run) error {
	checks, err := json.Marshal(r.Checks)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(ctx, `INSERT INTO cloud_meta.proof_runs (`+runColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18::json,$19,COALESCE($20, NOW()),$21,$22)`,
		r.RunID, r.Schema, r.App, r.Job, r.Tenant, r.Trigger, r.Thread, r.StartedAt.UTC(), r.EndedAt.UTC(),
		r.Outcome, checks, r.LimitsHeld, r.P50Ms, r.P95Ms, r.RunnerVersion, r.EventsKey, r.Entry, rulesText(r.Rules),
		r.Counted, nullTime(r.ReceivedAt), r.GitHubRunID, r.GitHubRunAttempt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrDuplicate
	}
	return err
}

// rulesText is the rules as given, or {} when there are none.
func rulesText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	var checks []byte
	var rules string
	err := row.Scan(&r.RunID, &r.Schema, &r.App, &r.Job, &r.Tenant, &r.Trigger, &r.Thread, &r.StartedAt, &r.EndedAt,
		&r.Outcome, &checks, &r.LimitsHeld, &r.P50Ms, &r.P95Ms, &r.RunnerVersion, &r.EventsKey, &r.Entry, &rules,
		&r.Counted, &r.ReceivedAt, &r.GitHubRunID, &r.GitHubRunAttempt)
	if err != nil {
		return Run{}, err
	}
	r.Rules = json.RawMessage(rules)
	if err := json.Unmarshal(checks, &r.Checks); err != nil {
		return Run{}, fmt.Errorf("proofs: checks of %s: %w", r.RunID, err)
	}
	return r, nil
}

func (p *PostgresStore) RunsSince(ctx context.Context, since time.Time) ([]Run, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+runColumns+` FROM cloud_meta.proof_runs WHERE ended_at >= $1 ORDER BY ended_at`, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *PostgresStore) Run(ctx context.Context, runID string) (Run, error) {
	r, err := scanRun(p.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM cloud_meta.proof_runs WHERE run_id = $1`, runID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	return r, err
}

func (p *PostgresStore) AppendOverride(ctx context.Context, o Override) (Override, error) {
	if o.ID == "" {
		o.ID = "prov_" + ulid.Make().String()
	}
	var until *time.Time
	if o.Until != nil {
		u := o.Until.UTC()
		until = &u
	}
	err := p.pool.QueryRow(ctx, `INSERT INTO cloud_meta.proof_overrides (id, app, action, operator, reason, owner_approval, until_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,COALESCE($8, NOW())) RETURNING created_at`,
		o.ID, o.App, o.Action, o.Operator, o.Reason, o.OwnerApproval, until, nullTime(o.CreatedAt)).Scan(&o.CreatedAt)
	return o, err
}

func (p *PostgresStore) Overrides(ctx context.Context) ([]Override, error) {
	rows, err := p.pool.Query(ctx, `SELECT id, app, action, operator, reason, owner_approval, until_at, created_at
		FROM cloud_meta.proof_overrides ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Override
	for rows.Next() {
		var o Override
		if err := rows.Scan(&o.ID, &o.App, &o.Action, &o.Operator, &o.Reason, &o.OwnerApproval, &o.Until, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (p *PostgresStore) Sweep(ctx context.Context, before time.Time) (int, error) {
	a, err := p.pool.Exec(ctx, `DELETE FROM cloud_meta.proof_runs WHERE received_at < $1`, before.UTC())
	if err != nil {
		return 0, err
	}
	b, err := p.pool.Exec(ctx, `DELETE FROM cloud_meta.proof_overrides WHERE created_at < $1`, before.UTC())
	if err != nil {
		return int(a.RowsAffected()), err
	}
	return int(a.RowsAffected() + b.RowsAffected()), nil
}
