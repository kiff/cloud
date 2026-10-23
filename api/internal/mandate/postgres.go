package mandate

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/mandates.sql
var mandatesSQL string

//go:embed sql/warnings.sql
var warningsSQL string

// SchemaSQL is the DDL for mandates and their draw ledger.
func SchemaSQL() string { return mandatesSQL + "\n" + warningsSQL }

// ApplySchema creates the tables if they are absent. Idempotent.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, SchemaSQL()); err != nil {
		return fmt.Errorf("apply mandates schema: %w", err)
	}
	return nil
}

// PostgresStore is the production Store.
//
// Every query carries tenant_id in its predicate rather than filtering
// in Go, so one tenant's mandates and usage are unreachable from
// another's request by construction.
type PostgresStore struct {
	pool *pgxpool.Pool
	// afterIssueLock runs once Issue holds the Card's lock. Tests use
	// it to hold that lock while a concurrent write queues behind it.
	afterIssueLock func()
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	if pool == nil {
		return nil
	}
	return &PostgresStore{pool: pool}
}

// querier is what a *pgxpool.Pool and a pgx.Tx have in common, so the
// same statements run on the pool or inside a Card-locked transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// lockCards takes the transaction-scoped advisory lock for each Card, in
// a stable order, on tx's own connection.
// Held until tx ends: if the connection is lost the locks go with it,
// and so does every uncommitted write made under them. A lock on a
// separate session could be lost while the write still committed.
func lockCards(ctx context.Context, tx pgx.Tx, tenantID string, mandateIDs []string) error {
	keys := make([]string, 0, len(mandateIDs))
	for _, id := range mandateIDs {
		keys = append(keys, CardLockKey(tenantID, id))
	}
	for _, id := range sortedCardLockIDs(keys) {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", id); err != nil {
			return fmt.Errorf("lock card: %w", err)
		}
	}
	return nil
}

// WithCards runs fn in one transaction that holds every listed Card's
// lock, and commits what fn wrote only if fn succeeds. The authorizer
// re-reads the Cards, evaluates and records its draws through the
// CardTx it is given, all on this transaction.
func (s *PostgresStore) WithCards(ctx context.Context, tenantID string, mandateIDs []string, fn func(CardTx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("card transaction: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockCards(ctx, tx, tenantID, mandateIDs); err != nil {
		return err
	}
	if err := fn(pgCardTx{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("card transaction: commit: %w", err)
	}
	return nil
}

// pgCardTx is the store as seen from inside a Card-locked transaction.
type pgCardTx struct{ tx pgx.Tx }

func (t pgCardTx) ForSubject(ctx context.Context, tenantID, subject string) ([]Mandate, error) {
	return forSubject(ctx, t.tx, tenantID, subject)
}

func (t pgCardTx) Used(ctx context.Context, tenantID, mandateID, quantityKey string, since time.Time, excludeProposalID string) (int64, error) {
	return used(ctx, t.tx, tenantID, mandateID, quantityKey, since, excludeProposalID)
}

func (t pgCardTx) RecordAll(ctx context.Context, draws []Draw) error {
	return writeDraws(ctx, t.tx, draws)
}

// issueStmt upserts a Card. revoked_at is deliberately absent from the
// UPDATE list: re-issuing a mandate id must not silently un-revoke
// authority that was withdrawn.
const issueStmt = `
	INSERT INTO cloud_meta.mandates
		(id, tenant_id, subject, domain, environment, grants, aggregates,
		 valid_from, valid_until, revoked_at, location, domain_id, on_exceed, hold_expiry_seconds)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	ON CONFLICT (tenant_id, id) DO UPDATE SET
		subject = EXCLUDED.subject,
		on_exceed = EXCLUDED.on_exceed,
		hold_expiry_seconds = EXCLUDED.hold_expiry_seconds,
		domain = EXCLUDED.domain,
		domain_id = EXCLUDED.domain_id,
		environment = EXCLUDED.environment,
		grants = EXCLUDED.grants,
		aggregates = EXCLUDED.aggregates,
		valid_from = EXCLUDED.valid_from,
		valid_until = EXCLUDED.valid_until,
		location = EXCLUDED.location`

func (s *PostgresStore) Issue(ctx context.Context, m Mandate, by string) error {
	// The revision is written in the same transaction as the upsert.
	// The upsert destroys the previous ceiling, so a revision written
	// afterwards on a best-effort basis would leave exactly the gap
	// this closes whenever the second write failed.
	return s.issueAll(ctx, []Mandate{m}, by, nil, nil, time.Time{})
}

// IssueAll issues every Card in ms, each with its revision, in one
// transaction holding all their locks: all of them, or none (kiff/tafs#107,
// a version's Cards revised together). They share one tenant.
func (s *PostgresStore) IssueAll(ctx context.Context, ms []Mandate, by string, expect map[string]int64, retire []string, at time.Time) error {
	return s.issueAll(ctx, ms, by, expect, retire, at)
}

func (s *PostgresStore) issueAll(ctx context.Context, ms []Mandate, by string, expect map[string]int64, retire []string, at time.Time) error {
	if len(ms) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("issue mandate: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Reissuing can change the holder and the terms, so it serializes
	// with the draws against these Cards under the same locks.
	ids := make([]string, len(ms))
	for i, m := range ms {
		if m.TenantID != ms[0].TenantID {
			return errors.New("issue mandate: Cards issued together share one tenant")
		}
		ids[i] = m.ID
	}
	if err := lockCards(ctx, tx, ms[0].TenantID, append(slices.Clone(ids), retire...)); err != nil {
		return fmt.Errorf("issue mandate: %w", err)
	}
	// Read under the locks: nothing revises these Cards between this check
	// and the commit.
	for id, want := range expect {
		var latest int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(revision), 0) FROM cloud_meta.mandate_revisions WHERE tenant_id = $1 AND mandate_id = $2`,
			ms[0].TenantID, id).Scan(&latest); err != nil {
			return fmt.Errorf("issue mandate: read revision: %w", err)
		}
		if latest != want {
			return ErrCardRevised
		}
	}
	for _, m := range ms {
		if err := s.issueTx(ctx, tx, m, by); err != nil {
			return err
		}
	}
	// Retired with them, as Revoke does: the earlier instant kept, and the
	// Card's open exceptions withdrawn.
	for _, id := range retire {
		tag, err := tx.Exec(ctx, `UPDATE cloud_meta.mandates SET revoked_at = LEAST(COALESCE(revoked_at, $3), $3) WHERE tenant_id = $1 AND id = $2`,
			ms[0].TenantID, id, at.UTC())
		if err != nil {
			return fmt.Errorf("issue mandate: retire %s: %w", id, err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if err := invalidateFor(ctx, tx, ms[0].TenantID, id, "card_revoked", at); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("issue mandate: commit: %w", err)
	}
	return nil
}

// issueTx upserts one Card and records its revision on tx, which holds
// the Card's lock.
func (s *PostgresStore) issueTx(ctx context.Context, tx pgx.Tx, m Mandate, by string) error {
	m = resolvedMeasures(m)
	grants, err := json.Marshal(m.Grants)
	if err != nil {
		return fmt.Errorf("marshal grants: %w", err)
	}
	aggs, err := json.Marshal(m.Aggregates)
	if err != nil {
		return fmt.Errorf("marshal aggregates: %w", err)
	}
	var previous []byte
	var previousLocation string
	previousErr := tx.QueryRow(ctx, `SELECT aggregates, location FROM cloud_meta.mandates WHERE tenant_id=$1 AND id=$2`, m.TenantID, m.ID).Scan(&previous, &previousLocation)
	if previousErr != nil && !errors.Is(previousErr, pgx.ErrNoRows) {
		return previousErr
	}
	if m.Location == "" {
		if previousErr == nil {
			m.Location = previousLocation
		} else {
			m.Location = m.DefaultLocation
		}
	}
	m.DefaultLocation = ""
	if len(previous) > 0 {
		var old []Aggregate
		if err := json.Unmarshal(previous, &old); err != nil {
			return err
		}
		m = keepRoom(m, old)
		aggs, err = json.Marshal(m.Aggregates)
		if err != nil {
			return err
		}
	}
	if s.afterIssueLock != nil {
		s.afterIssueLock()
	}
	// An unnamed policy is the Card's current one, read under its lock.
	if m.OnExceed == OnExceedKeep {
		var current string
		err := tx.QueryRow(ctx, `SELECT on_exceed FROM cloud_meta.mandates WHERE tenant_id = $1 AND id = $2`,
			m.TenantID, m.ID).Scan(&current)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			m.OnExceed = OnExceedOwner
		case err != nil:
			return fmt.Errorf("issue mandate: read policy: %w", err)
		default:
			m.OnExceed = current
		}
	}
	// An unnamed hold expiry is the Card's current one, read under its
	// lock; a new Card gets the default (stored as 0).
	if m.HoldExpiry == HoldExpiryKeep {
		var secs int64
		err := tx.QueryRow(ctx, `SELECT hold_expiry_seconds FROM cloud_meta.mandates WHERE tenant_id = $1 AND id = $2`,
			m.TenantID, m.ID).Scan(&secs)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			m.HoldExpiry = 0
		case err != nil:
			return fmt.Errorf("issue mandate: read hold expiry: %w", err)
		default:
			m.HoldExpiry = time.Duration(secs) * time.Second
		}
	}

	before, err := holdersOf(ctx, tx, m.TenantID, m.ID)
	if err != nil {
		return fmt.Errorf("issue mandate: %w", err)
	}
	m = m.withHolders(keepHolders(m, before))
	if _, err := tx.Exec(ctx, issueStmt,
		m.ID, m.TenantID, m.Subject, m.Domain, m.Environment, grants, aggs,
		nullTime(m.ValidFrom), nullTime(m.ValidUntil), m.RevokedAt, m.Location, m.DomainID, policyOf(m),
		int64(m.HoldExpiry/time.Second),
	); err != nil {
		return fmt.Errorf("issue mandate: %w", err)
	}
	if err := writeHolders(ctx, tx, m.TenantID, m.ID, m.Holders); err != nil {
		return fmt.Errorf("issue mandate: %w", err)
	}
	if gone := removedHolders(before, m.Holders); len(gone) > 0 {
		if err := invalidateHoldersFor(ctx, tx, m.TenantID, m.ID, gone, "holder_changed", time.Now()); err != nil {
			return err
		}
	}
	return insertRevision(ctx, tx, m, by)
}

// insertRevision records the Card as it stands after a change, with who
// made it and who holds it, as the next revision number.
func insertRevision(ctx context.Context, q querier, m Mandate, by string) error {
	grants, err := json.Marshal(m.Grants)
	if err != nil {
		return fmt.Errorf("marshal grants: %w", err)
	}
	aggs, err := json.Marshal(m.Aggregates)
	if err != nil {
		return fmt.Errorf("marshal aggregates: %w", err)
	}
	const stmt = `
		INSERT INTO cloud_meta.mandate_revisions
			(tenant_id, mandate_id, revision, changed_at, changed_by,
			 grants, aggregates, valid_until, subject, on_exceed, holders)
		SELECT $1, $2,
		       COALESCE(MAX(revision), 0) + 1,
		       NOW(), $3, $4, $5, $6, $7, $8, $9
		  FROM cloud_meta.mandate_revisions
		 WHERE tenant_id = $1 AND mandate_id = $2`
	if _, err := q.Exec(ctx, stmt, m.TenantID, m.ID, by, grants, aggs, nullTime(m.ValidUntil), m.Subject, policyOf(m), m.HolderList()); err != nil {
		return fmt.Errorf("record mandate revision: %w", err)
	}
	return nil
}

// Revisions reads a mandate's issuance history, oldest first.
func (s *PostgresStore) Revisions(ctx context.Context, tenantID, mandateID string) ([]Revision, error) {
	const stmt = `
		SELECT revision, changed_at, changed_by, grants, aggregates, valid_until,
		       COALESCE(subject, ''), COALESCE(on_exceed, ''), COALESCE(holders, '{}')
		  FROM cloud_meta.mandate_revisions
		 WHERE tenant_id = $1 AND mandate_id = $2
		 ORDER BY revision ASC`
	rows, err := s.pool.Query(ctx, stmt, tenantID, mandateID)
	if err != nil {
		return nil, fmt.Errorf("list mandate revisions: %w", err)
	}
	defer rows.Close()

	out := []Revision{}
	for rows.Next() {
		r := Revision{TenantID: tenantID, MandateID: mandateID}
		var grants, aggs []byte
		var until *time.Time
		if err := rows.Scan(&r.Number, &r.At, &r.By, &grants, &aggs, &until, &r.Subject, &r.OnExceed, &r.Holders); err != nil {
			return nil, fmt.Errorf("scan mandate revision: %w", err)
		}
		if err := json.Unmarshal(grants, &r.Grants); err != nil {
			return nil, fmt.Errorf("decode revision grants: %w", err)
		}
		if err := json.Unmarshal(aggs, &r.Aggregates); err != nil {
			return nil, fmt.Errorf("decode revision aggregates: %w", err)
		}
		if until != nil {
			r.ValidUntil = *until
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PostgresStore) Revoke(ctx context.Context, tenantID, mandateID string, at time.Time) error {
	// LEAST keeps the earlier instant when revoked twice: authority
	// ended when it first ended, and a retry must not extend it.
	const stmt = `
		UPDATE cloud_meta.mandates
		   SET revoked_at = LEAST(COALESCE(revoked_at, $3), $3)
		 WHERE tenant_id = $1 AND id = $2`
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("revoke mandate: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockCards(ctx, tx, tenantID, []string{mandateID}); err != nil {
		return fmt.Errorf("revoke mandate: %w", err)
	}
	tag, err := tx.Exec(ctx, stmt, tenantID, mandateID, at.UTC())
	if err != nil {
		return fmt.Errorf("revoke mandate: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	// A revoked Card's open exceptions are withdrawn with it: nothing
	// held waits on it, and an approval not yet used stops authorizing.
	if err := invalidateFor(ctx, tx, tenantID, mandateID, "card_revoked", at); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("revoke mandate: commit: %w", err)
	}
	return nil
}

// Move gives a Card to another holder: SetHolders with that one agent.
func (s *PostgresStore) Move(ctx context.Context, tenantID, mandateID, to, by string, now time.Time) (Mandate, error) {
	return s.SetHolders(ctx, tenantID, mandateID, []string{to}, nil, by, now)
}

// SetHolders replaces a Card's holders: the same id, terms, usage and
// statement, with a revision that names them. The Card is read and
// written in one transaction holding its lock, so it cannot change
// between the check and the write, and no draw by a removed holder can
// commit after it. Exceptions for the agents removed are invalidated;
// the others' stay.
func (s *PostgresStore) SetHolders(ctx context.Context, tenantID, mandateID string, holders, expected []string, by string, now time.Time) (Mandate, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Mandate{}, fmt.Errorf("set holders: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockCards(ctx, tx, tenantID, []string{mandateID}); err != nil {
		return Mandate{}, fmt.Errorf("set holders: %w", err)
	}
	rows, err := tx.Query(ctx, mandateColumns+` FROM cloud_meta.mandates WHERE tenant_id = $1 AND id = $2`, tenantID, mandateID)
	if err != nil {
		return Mandate{}, fmt.Errorf("set holders: load: %w", err)
	}
	ms, err := scanMandates(rows)
	if err != nil {
		return Mandate{}, fmt.Errorf("set holders: %w", err)
	}
	if len(ms) == 0 {
		return Mandate{}, ErrNotFound
	}
	m := ms[0]
	if err := checkSetHolders(m, holders, expected, now); err != nil {
		return Mandate{}, err
	}
	before := m.HolderList()
	m = m.withHolders(holders)
	if _, err := tx.Exec(ctx, `UPDATE cloud_meta.mandates SET subject = $3 WHERE tenant_id = $1 AND id = $2`, tenantID, mandateID, m.Subject); err != nil {
		return Mandate{}, fmt.Errorf("set holders: %w", err)
	}
	if err := writeHolders(ctx, tx, tenantID, mandateID, m.Holders); err != nil {
		return Mandate{}, fmt.Errorf("set holders: %w", err)
	}
	if err := insertRevision(ctx, tx, m, by); err != nil {
		return Mandate{}, err
	}
	// What was held for, or approved to, an agent that no longer holds
	// the Card does not travel to the others.
	if gone := removedHolders(before, m.Holders); len(gone) > 0 {
		if err := invalidateHoldersFor(ctx, tx, tenantID, mandateID, gone, "holder_changed", now); err != nil {
			return Mandate{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Mandate{}, fmt.Errorf("set holders: commit: %w", err)
	}
	return m, nil
}

// holdersOf reads a Card's holders as stored, first one first; nil for a
// Card not yet written.
func holdersOf(ctx context.Context, q querier, tenantID, mandateID string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT subject FROM cloud_meta.mandate_holders
		WHERE tenant_id = $1 AND mandate_id = $2 ORDER BY position`, tenantID, mandateID)
	if err != nil {
		return nil, fmt.Errorf("read holders: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("read holders: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// writeHolders replaces a Card's holder rows with holders, in order.
func writeHolders(ctx context.Context, q querier, tenantID, mandateID string, holders []string) error {
	if _, err := q.Exec(ctx, `DELETE FROM cloud_meta.mandate_holders WHERE tenant_id = $1 AND mandate_id = $2`, tenantID, mandateID); err != nil {
		return fmt.Errorf("write holders: %w", err)
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO cloud_meta.mandate_holders (tenant_id, mandate_id, subject, position)
		SELECT $1, $2, h, (ord - 1)::int FROM unnest($3::text[]) WITH ORDINALITY AS t(h, ord)`,
		tenantID, mandateID, holders); err != nil {
		return fmt.Errorf("write holders: %w", err)
	}
	return nil
}

func (s *PostgresStore) Archive(ctx context.Context, tenantID, mandateID string, at time.Time) error {
	const stmt = `
		UPDATE cloud_meta.mandates
		   SET archived_at = COALESCE(archived_at, $3)
		 WHERE tenant_id = $1 AND id = $2
		   AND (revoked_at IS NOT NULL OR valid_until <= $3)`
	tag, err := s.pool.Exec(ctx, stmt, tenantID, mandateID, at.UTC())
	if err != nil {
		return fmt.Errorf("archive mandate: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cloud_meta.mandates WHERE tenant_id=$1 AND id=$2)`, tenantID, mandateID).Scan(&exists); err != nil {
		return fmt.Errorf("archive mandate: check: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	return ErrActive
}

// mandateColumns selects a mandate row in the order scanMandates reads.
const mandateColumns = `
		SELECT id, tenant_id, subject, domain, environment, grants, aggregates,
		       valid_from, valid_until, revoked_at, archived_at, location, domain_id, on_exceed, hold_expiry_seconds,
		       ARRAY(SELECT h.subject FROM cloud_meta.mandate_holders h
		              WHERE h.tenant_id = mandates.tenant_id AND h.mandate_id = mandates.id
		              ORDER BY h.position)`

func (s *PostgresStore) ForSubject(ctx context.Context, tenantID, subject string) ([]Mandate, error) {
	return forSubject(ctx, s.pool, tenantID, subject)
}

func forSubject(ctx context.Context, q querier, tenantID, subject string) ([]Mandate, error) {
	// Every Card the agent holds, shared or not.
	rows, err := q.Query(ctx, mandateColumns+`
		  FROM cloud_meta.mandates
		 WHERE tenant_id = $1 AND (subject = $2 OR EXISTS (
		       SELECT 1 FROM cloud_meta.mandate_holders h
		        WHERE h.tenant_id = mandates.tenant_id AND h.mandate_id = mandates.id AND h.subject = $2))
		 ORDER BY id`, tenantID, subject)
	if err != nil {
		return nil, fmt.Errorf("load mandates for subject: %w", err)
	}
	return scanMandates(rows)
}

func (s *PostgresStore) List(ctx context.Context, tenantID string) ([]Mandate, error) {
	rows, err := s.pool.Query(ctx, mandateColumns+`
		  FROM cloud_meta.mandates
		 WHERE tenant_id = $1
		 ORDER BY issued_at DESC, id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list mandates: %w", err)
	}
	return scanMandates(rows)
}

func (s *PostgresStore) Used(ctx context.Context, tenantID, mandateID, quantityKey string, since time.Time, excludeProposalID string) (int64, error) {
	return used(ctx, s.pool, tenantID, mandateID, quantityKey, since, excludeProposalID)
}

func used(ctx context.Context, q querier, tenantID, mandateID, quantityKey string, since time.Time, excludeProposalID string) (int64, error) {
	const stmt = `
		SELECT COALESCE(SUM(amount), 0)
		  FROM cloud_meta.mandate_draws
		 WHERE tenant_id = $1 AND mandate_id = $2 AND quantity_key = $3
		   AND drawn_at >= $4
		   AND ($5 = '' OR proposal_id <> $5)`
	var total int64
	if err := q.QueryRow(ctx, stmt, tenantID, mandateID, quantityKey, since.UTC(), excludeProposalID).Scan(&total); err != nil {
		// Never degrade to zero. A zero reads as "plenty of room left",
		// and the caller is required to refuse on an error instead.
		return 0, fmt.Errorf("sum draws: %w", err)
	}
	return total, nil
}

func (s *PostgresStore) ProposalDraws(ctx context.Context, tenantID, proposalID string) ([]Draw, error) {
	const stmt = `
		SELECT mandate_id, proposal_id, quantity_key, amount, drawn_at,
		       COALESCE(action, ''), COALESCE(entity_id, ''), subject,
		       measure, accounting_version, charged_window, window_start, charged_limits
		  FROM cloud_meta.mandate_draws
		 WHERE tenant_id = $1 AND proposal_id = $2`
	rows, err := s.pool.Query(ctx, stmt, tenantID, proposalID)
	if err != nil {
		return nil, fmt.Errorf("proposal draws: %w", err)
	}
	defer rows.Close()
	var out []Draw
	for rows.Next() {
		d := Draw{TenantID: tenantID}
		var start *time.Time
		var terms []byte
		if err := rows.Scan(&d.MandateID, &d.ProposalID, &d.QuantityKey, &d.Amount, &d.DrawnAt, &d.Action, &d.EntityID,
			&d.Subject, &d.Measure, &d.AccountingVersion, &d.Window, &start, &terms); err != nil {
			return nil, fmt.Errorf("proposal draws: scan: %w", err)
		}
		if start != nil {
			d.WindowStart = *start
		}
		if err := json.Unmarshal(terms, &d.ChargedLimits); err != nil {
			return nil, fmt.Errorf("proposal draw terms: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("proposal draws: %w", err)
	}
	return out, nil
}

// drawUpsertSQL is shared by Record and RecordAll. A committed draw is
// immutable. Retrying its insert cannot change its amount or accounting
// terms; authorizations use the atomic batch rather than partial writes.
const drawUpsertSQL = `
		INSERT INTO cloud_meta.mandate_draws
			(tenant_id, mandate_id, proposal_id, quantity_key, amount, drawn_at,
			 action, entity_id, subject, measure, accounting_version, charged_window, window_start, charged_limits)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (tenant_id, mandate_id, proposal_id, quantity_key) DO NOTHING`

func (s *PostgresStore) Record(ctx context.Context, d Draw) error {
	return writeDraw(ctx, s.pool, d)
}

func writeDraw(ctx context.Context, q querier, d Draw) error {
	terms, err := json.Marshal(d.ChargedLimits)
	if err != nil {
		return fmt.Errorf("encode draw terms: %w", err)
	}
	if _, err := q.Exec(ctx, drawUpsertSQL,
		d.TenantID, d.MandateID, d.ProposalID, d.QuantityKey, d.Amount, d.DrawnAt.UTC(),
		d.Action, d.EntityID, d.Subject, d.Measure, d.AccountingVersion, d.Window, nullTime(d.WindowStart), terms); err != nil {
		return fmt.Errorf("record draw: %w", err)
	}
	return nil
}

// RecordAll writes every draw of one authorization in a single
// transaction, so the ledger never holds half of it.
func (s *PostgresStore) RecordAll(ctx context.Context, draws []Draw) error {
	if len(draws) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("record draws: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := writeDraws(ctx, tx, draws); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("record draws: commit: %w", err)
	}
	return nil
}

// writeDraws claims the proposal id and writes the draws on q, which is
// a transaction the caller commits.
func writeDraws(ctx context.Context, q querier, draws []Draw) error {
	if len(draws) == 0 {
		return nil
	}
	// Claim the proposal id first. A concurrent authorization under the
	// same id either inserted the claim already (0 rows here) or blocks
	// on its uncommitted row until it commits, then sees it: both ways
	// only one transaction writes draws.
	tag, err := q.Exec(ctx, `
		INSERT INTO cloud_meta.mandate_proposal_claims (tenant_id, proposal_id, claimed_at, accounting_version)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, draws[0].TenantID, draws[0].ProposalID, draws[0].DrawnAt.UTC(), CumulativeAccountingVersion)
	if err != nil {
		return fmt.Errorf("record draws: claim proposal: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProposalSpent
	}
	for _, d := range draws {
		if err := writeDraw(ctx, q, d); err != nil {
			return fmt.Errorf("record draw on mandate %s: %w", d.MandateID, err)
		}
	}
	return nil
}

func (s *PostgresStore) Draws(ctx context.Context, tenantID, mandateID string, since time.Time) ([]Draw, error) {
	const stmt = `
		SELECT tenant_id, mandate_id, proposal_id, quantity_key, amount, drawn_at,
		       action, entity_id, subject, measure, accounting_version, charged_window, window_start, charged_limits
		  FROM cloud_meta.mandate_draws
		 WHERE tenant_id = $1 AND mandate_id = $2 AND drawn_at >= $3
		 ORDER BY drawn_at, proposal_id`
	rows, err := s.pool.Query(ctx, stmt, tenantID, mandateID, since.UTC())
	if err != nil {
		return nil, fmt.Errorf("load draws: %w", err)
	}
	defer rows.Close()

	out := []Draw{}
	for rows.Next() {
		var d Draw
		var start *time.Time
		var terms []byte
		if err := rows.Scan(&d.TenantID, &d.MandateID, &d.ProposalID, &d.QuantityKey,
			&d.Amount, &d.DrawnAt, &d.Action, &d.EntityID, &d.Subject, &d.Measure, &d.AccountingVersion, &d.Window, &start, &terms); err != nil {
			return nil, fmt.Errorf("scan draw: %w", err)
		}
		if start != nil {
			d.WindowStart = *start
		}
		if err := json.Unmarshal(terms, &d.ChargedLimits); err != nil {
			return nil, fmt.Errorf("draw terms: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func scanMandates(rows pgx.Rows) ([]Mandate, error) {
	defer rows.Close()
	out := []Mandate{}
	for rows.Next() {
		var (
			m                                            Mandate
			grants, aggs                                 []byte
			validFrom, validUntil, revokedAt, archivedAt *time.Time
			holdSecs                                     int64
		)
		if err := rows.Scan(&m.ID, &m.TenantID, &m.Subject, &m.Domain, &m.Environment,
			&grants, &aggs, &validFrom, &validUntil, &revokedAt, &archivedAt, &m.Location, &m.DomainID, &m.OnExceed, &holdSecs, &m.Holders); err != nil {
			return nil, fmt.Errorf("scan mandate: %w", err)
		}
		if err := json.Unmarshal(grants, &m.Grants); err != nil {
			return nil, fmt.Errorf("decode grants for %s: %w", m.ID, err)
		}
		if err := json.Unmarshal(aggs, &m.Aggregates); err != nil {
			return nil, fmt.Errorf("decode aggregates for %s: %w", m.ID, err)
		}
		if validFrom != nil {
			m.ValidFrom = *validFrom
		}
		if validUntil != nil {
			m.ValidUntil = *validUntil
		}
		m.RevokedAt = revokedAt
		m.ArchivedAt = archivedAt
		m.HoldExpiry = time.Duration(holdSecs) * time.Second
		if len(m.Holders) == 0 {
			m.Holders = nil // Subject alone
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// compile-time proof both implementations satisfy the same contract, so
// a test passing against memory cannot be passing against a looser
// store than production runs.
var (
	_ Store = (*PostgresStore)(nil)
	_ Store = (*MemoryStore)(nil)
)
