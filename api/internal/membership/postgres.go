package membership

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/memberships.sql
var membershipsSQL string

// SchemaSQL returns the DDL, for integration tests.
func SchemaSQL() string { return membershipsSQL }

// ApplySchema runs the membership DDL. Call after tenant.ApplyCloudMeta.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, membershipsSQL); err != nil {
		return fmt.Errorf("apply memberships schema: %w", err)
	}
	return nil
}

// Backfill gives every subject_tenants row whose tenant has no
// membership yet an admin membership, every such tenant a governance
// row with separation of duties off, and each new membership a
// member.backfilled audit row. Idempotent:
// a tenant that has any membership is left alone. Returns how many
// memberships it created.
func Backfill(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
WITH todo AS (
    SELECT st.subject, st.tenant_id
      FROM cloud_meta.subject_tenants st
     WHERE NOT EXISTS (SELECT 1 FROM cloud_meta.tenant_memberships m WHERE m.tenant_id = st.tenant_id)
)
INSERT INTO cloud_meta.tenant_memberships (id, tenant_id, subject, role, status, invited_by)
SELECT 'mem_' || md5(random()::text || clock_timestamp()::text || todo.subject), todo.tenant_id, todo.subject, 'admin', 'active', NULL
  FROM todo
ON CONFLICT DO NOTHING
RETURNING tenant_id, subject`)
	if err != nil {
		return 0, fmt.Errorf("backfill memberships: %w", err)
	}
	type pair struct{ tenant, subject string }
	var made []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.tenant, &p.subject); err != nil {
			rows.Close()
			return 0, err
		}
		made = append(made, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, p := range made {
		if _, err := tx.Exec(ctx, `
INSERT INTO cloud_meta.tenant_governance (tenant_id) VALUES ($1) ON CONFLICT DO NOTHING`, p.tenant); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO cloud_meta.membership_audit (id, tenant_id, actor, action, target, new_value)
VALUES ($1, $2, 'system', $3, $4, 'admin')`, newID("maud_"), p.tenant, AuditBackfilled, p.subject); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(made), nil
}

// PostgresStore is the production Store over cloud_meta.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore wraps the cloud pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

const memberCols = `id, tenant_id, subject, role, status, COALESCE(invited_by, ''), created_at, updated_at, removed_at, version`

func scanMember(row pgx.Row) (Member, error) {
	var m Member
	var role, status string
	err := row.Scan(&m.ID, &m.TenantID, &m.Subject, &role, &status, &m.InvitedBy, &m.CreatedAt, &m.UpdatedAt, &m.RemovedAt, &m.Version)
	m.Role, m.Status = Role(role), Status(status)
	return m, err
}

func collectMembers(rows pgx.Rows, err error) ([]Member, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func oneMember(row pgx.Row) (Member, error) {
	m, err := scanMember(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrNotFound
	}
	return m, err
}

const sqlLive = `SELECT ` + memberCols + ` FROM cloud_meta.tenant_memberships
 WHERE tenant_id = $1 AND subject = $2 AND status <> 'removed'`

func (s *PostgresStore) Live(ctx context.Context, tenantID, subject string) (Member, error) {
	return oneMember(s.pool.QueryRow(ctx, sqlLive, tenantID, subject))
}

func (s *PostgresStore) Latest(ctx context.Context, tenantID, subject string) (Member, error) {
	return oneMember(s.pool.QueryRow(ctx, `SELECT `+memberCols+` FROM cloud_meta.tenant_memberships
 WHERE tenant_id = $1 AND subject = $2
 ORDER BY (status <> 'removed') DESC, created_at DESC LIMIT 1`, tenantID, subject))
}

func (s *PostgresStore) ForSubject(ctx context.Context, subject string) ([]Member, error) {
	return collectMembers(s.pool.Query(ctx, `SELECT `+memberCols+` FROM cloud_meta.tenant_memberships
 WHERE subject = $1 AND status <> 'removed' ORDER BY created_at, id`, subject))
}

func (s *PostgresStore) HasRemoved(ctx context.Context, subject string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cloud_meta.tenant_memberships
 WHERE subject = $1 AND status = 'removed')`, subject).Scan(&ok)
	return ok, err
}

const sqlList = `SELECT ` + memberCols + ` FROM cloud_meta.tenant_memberships
 WHERE tenant_id = $1 AND status <> 'removed' ORDER BY created_at, id`

func (s *PostgresStore) List(ctx context.Context, tenantID string) ([]Member, error) {
	return collectMembers(s.pool.Query(ctx, sqlList, tenantID))
}

const sqlGovernance = `SELECT tenant_id, separation_of_duties, COALESCE(updated_by, ''), updated_at
 FROM cloud_meta.tenant_governance WHERE tenant_id = $1`

func scanGovernance(row pgx.Row, tenantID string) (Governance, error) {
	var g Governance
	err := row.Scan(&g.TenantID, &g.SeparationOfDuties, &g.UpdatedBy, &g.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Governance{TenantID: tenantID}, nil
	}
	return g, err
}

func (s *PostgresStore) Governance(ctx context.Context, tenantID string) (Governance, error) {
	return scanGovernance(s.pool.QueryRow(ctx, sqlGovernance, tenantID), tenantID)
}

const inviteCols = `id, tenant_id, email, role, invited_by, token_hash, created_at, expires_at, accepted_at, COALESCE(accepted_by, ''), revoked_at`

func scanInvite(row pgx.Row) (Invite, error) {
	var i Invite
	var role string
	var hash []byte
	err := row.Scan(&i.ID, &i.TenantID, &i.Email, &role, &i.InvitedBy, &hash, &i.CreatedAt, &i.ExpiresAt, &i.AcceptedAt, &i.AcceptedBy, &i.RevokedAt)
	i.Role = Role(role)
	copy(i.TokenHash[:], hash)
	return i, err
}

func (s *PostgresStore) Invites(ctx context.Context, tenantID string) ([]Invite, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+inviteCols+` FROM cloud_meta.tenant_invites
 WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT 200`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		i, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *PostgresStore) InviteByHash(ctx context.Context, hash [32]byte) (Invite, error) {
	i, err := scanInvite(s.pool.QueryRow(ctx, `SELECT `+inviteCols+` FROM cloud_meta.tenant_invites WHERE token_hash = $1`, hash[:]))
	if errors.Is(err, pgx.ErrNoRows) {
		return Invite{}, ErrNotFound
	}
	return i, err
}

func (s *PostgresStore) Audit(ctx context.Context, tenantID string, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
SELECT id, tenant_id, actor, action, COALESCE(target, ''), COALESCE(old_value, ''), COALESCE(new_value, ''),
       COALESCE(request_id, ''), COALESCE(auth_method, ''), reauth_age_s, at
  FROM cloud_meta.membership_audit WHERE tenant_id = $1 ORDER BY at DESC, id DESC LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Actor, &e.Action, &e.Target, &e.OldValue, &e.NewValue,
			&e.RequestID, &e.AuthMethod, &e.ReauthAgeS, &e.At); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *PostgresStore) HoldAnswers(ctx context.Context, tenantID string, holdIDs []string) (map[string]HoldAnswer, error) {
	out := map[string]HoldAnswer{}
	if len(holdIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
SELECT tenant_id, hold_id, answer, answerer, COALESCE(role, ''), separation_of_duties, at, device_proof
  FROM cloud_meta.hold_answers WHERE tenant_id = $1 AND hold_id = ANY($2)`, tenantID, holdIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a HoldAnswer
		var role string
		var device []byte
		if err := rows.Scan(&a.TenantID, &a.HoldID, &a.Answer, &a.Answerer, &role, &a.SeparationOfDuties, &a.At, &device); err != nil {
			return nil, err
		}
		if len(device) > 0 {
			a.Device = &DeviceProof{}
			if err := json.Unmarshal(device, a.Device); err != nil {
				return nil, fmt.Errorf("hold answer device proof: %w", err)
			}
		}
		a.Role = Role(role)
		out[a.HoldID] = a
	}
	return out, rows.Err()
}

func (s *PostgresStore) RecordHoldAnswer(ctx context.Context, a HoldAnswer) error {
	if a.At.IsZero() {
		a.At = time.Now().UTC()
	}
	var role any
	if a.Role != "" {
		role = string(a.Role)
	}
	var device any
	if a.Device != nil {
		b, err := json.Marshal(a.Device)
		if err != nil {
			return err
		}
		device = b
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO cloud_meta.hold_answers (tenant_id, hold_id, answer, answerer, role, separation_of_duties, at, device_proof)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (tenant_id, hold_id) DO NOTHING`,
		a.TenantID, a.HoldID, a.Answer, a.Answerer, role, a.SeparationOfDuties, a.At, device)
	return err
}

// Mutate takes the tenant's governance row FOR UPDATE, which serializes
// every membership change to the tenant, then runs fn in the same
// transaction.
func (s *PostgresStore) Mutate(ctx context.Context, tenantID string, fn func(Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO cloud_meta.tenant_governance (tenant_id) VALUES ($1) ON CONFLICT DO NOTHING`, tenantID); err != nil {
		return fmt.Errorf("membership lock: %w", err)
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT NOW() FROM cloud_meta.tenant_governance WHERE tenant_id = $1 FOR UPDATE`, tenantID).Scan(&now); err != nil {
		return fmt.Errorf("membership lock: %w", err)
	}
	if err := fn(&pgTx{tx: tx, ctx: ctx, tenant: tenantID, now: now.UTC()}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type pgTx struct {
	tx     pgx.Tx
	ctx    context.Context
	tenant string
	now    time.Time
}

func (t *pgTx) Now() time.Time { return t.now }

func (t *pgTx) Members() ([]Member, error) {
	return collectMembers(t.tx.Query(t.ctx, sqlList, t.tenant))
}

func (t *pgTx) Member(subject string) (Member, bool, error) {
	m, err := oneMember(t.tx.QueryRow(t.ctx, sqlLive, t.tenant, subject))
	if errors.Is(err, ErrNotFound) {
		return Member{}, false, nil
	}
	return m, err == nil, err
}

func (t *pgTx) Governance() (Governance, error) {
	return scanGovernance(t.tx.QueryRow(t.ctx, sqlGovernance, t.tenant), t.tenant)
}

func (t *pgTx) Invite(id string) (Invite, bool, error) {
	i, err := scanInvite(t.tx.QueryRow(t.ctx, `SELECT `+inviteCols+` FROM cloud_meta.tenant_invites
 WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, id, t.tenant))
	if errors.Is(err, pgx.ErrNoRows) {
		return Invite{}, false, nil
	}
	return i, err == nil, err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (t *pgTx) InsertMember(m Member) error {
	_, err := t.tx.Exec(t.ctx, `
INSERT INTO cloud_meta.tenant_memberships (id, tenant_id, subject, role, status, invited_by, created_at, updated_at, removed_at, version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1)`,
		m.ID, t.tenant, m.Subject, string(m.Role), string(m.Status), nullable(m.InvitedBy), m.CreatedAt, m.UpdatedAt, m.RemovedAt)
	if isUniqueViolation(err) {
		return ErrAlreadyMember
	}
	return err
}

func (t *pgTx) UpdateMember(m Member) error {
	tag, err := t.tx.Exec(t.ctx, `
UPDATE cloud_meta.tenant_memberships
   SET role = $3, status = $4, updated_at = $5, removed_at = $6, version = version + 1
 WHERE id = $1 AND tenant_id = $2`, m.ID, t.tenant, string(m.Role), string(m.Status), m.UpdatedAt, m.RemovedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (t *pgTx) InsertInvite(i Invite) error {
	_, err := t.tx.Exec(t.ctx, `
INSERT INTO cloud_meta.tenant_invites (id, tenant_id, email, role, invited_by, token_hash, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		i.ID, t.tenant, i.Email, string(i.Role), i.InvitedBy, i.TokenHash[:], i.CreatedAt, i.ExpiresAt)
	return err
}

func (t *pgTx) UpdateInvite(i Invite) error {
	tag, err := t.tx.Exec(t.ctx, `
UPDATE cloud_meta.tenant_invites SET accepted_at = $3, accepted_by = $4, revoked_at = $5
 WHERE id = $1 AND tenant_id = $2`, i.ID, t.tenant, i.AcceptedAt, nullable(i.AcceptedBy), i.RevokedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (t *pgTx) SetGovernance(g Governance) error {
	_, err := t.tx.Exec(t.ctx, `
INSERT INTO cloud_meta.tenant_governance (tenant_id, separation_of_duties, updated_by, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (tenant_id) DO UPDATE SET separation_of_duties = EXCLUDED.separation_of_duties,
    updated_by = EXCLUDED.updated_by, updated_at = EXCLUDED.updated_at`,
		t.tenant, g.SeparationOfDuties, nullable(g.UpdatedBy), g.UpdatedAt)
	return err
}

func (t *pgTx) Audit(e AuditEvent) error {
	if e.ID == "" {
		e.ID = newID("maud_")
	}
	if e.At.IsZero() {
		e.At = t.now
	}
	_, err := t.tx.Exec(t.ctx, `
INSERT INTO cloud_meta.membership_audit (id, tenant_id, actor, action, target, old_value, new_value, request_id, auth_method, reauth_age_s, at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		e.ID, t.tenant, e.Actor, e.Action, nullable(e.Target), nullable(e.OldValue), nullable(e.NewValue),
		nullable(e.RequestID), nullable(e.AuthMethod), e.ReauthAgeS, e.At)
	return err
}

var _ Store = (*PostgresStore)(nil)

func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}
