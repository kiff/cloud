package access

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/access.sql
var accessSQL string

// ApplySchema creates the groups, members and grants tables if absent,
// and runs the one-time backfill. Idempotent. Run after the memberships
// and agents schemas.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "access/access", accessSQL); err != nil {
		return fmt.Errorf("apply access schema: %w", err)
	}
	return nil
}

// ClearArchivedGrants removes, once, the grants left on agents archived
// before archiving removed them (the API's AccessHandler.RemoveAgentGrants),
// each recorded in the trail by 'system' as an unshare is, and reports how
// many it removed: 0 once it has run. Run after ApplySchema.
func ClearArchivedGrants(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// One API instance clears them; another booting meanwhile waits, then
	// finds it done.
	if _, err := tx.Exec(ctx, `LOCK TABLE cloud_meta.access_archived_grants_cleared IN EXCLUSIVE MODE`); err != nil {
		return 0, fmt.Errorf("clear archived grants: %w", err)
	}
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cloud_meta.access_archived_grants_cleared)`).Scan(&done); err != nil {
		return 0, fmt.Errorf("clear archived grants: %w", err)
	}
	if done {
		return 0, nil
	}
	var removed int
	if err := tx.QueryRow(ctx, `
		WITH removed AS (
			DELETE FROM cloud_meta.access_grants g
			 USING cloud_meta.agents a
			 WHERE a.tenant_id = g.tenant_id AND a.id = g.agent_id AND a.archived_at IS NOT NULL
			RETURNING g.tenant_id, g.agent_id, g.group_id, g.subject
		), audited AS (
			INSERT INTO cloud_meta.membership_audit (id, tenant_id, actor, action, target, old_value)
			SELECT 'maud_' || replace(gen_random_uuid()::text, '-', ''), r.tenant_id, 'system', 'grant.removed', r.agent_id,
			       CASE WHEN r.group_id <> '' THEN 'group:' || r.group_id ELSE 'subject:' || r.subject END
			  FROM removed r
			RETURNING 1
		)
		SELECT count(*) FROM audited`).Scan(&removed); err != nil {
		return 0, fmt.Errorf("clear archived grants: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO cloud_meta.access_archived_grants_cleared (id) VALUES (TRUE) ON CONFLICT DO NOTHING`); err != nil {
		return 0, fmt.Errorf("clear archived grants: %w", err)
	}
	return removed, tx.Commit(ctx)
}

// PostgresStore is the production Store.
type PostgresStore struct{ pool *pgxpool.Pool }

// NewPostgresStore wraps pool; the caller applies the schema.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *PostgresStore) Groups(ctx context.Context, tenantID string) ([]Group, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id, id, name, everyone, created_at, created_by
		  FROM cloud_meta.access_groups WHERE tenant_id = $1 ORDER BY lower(name)`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Group, error) {
		var g Group
		err := r.Scan(&g.TenantID, &g.ID, &g.Name, &g.Everyone, &g.CreatedAt, &g.CreatedBy)
		return g, err
	})
}

func (s *PostgresStore) Group(ctx context.Context, tenantID, id string) (Group, error) {
	var g Group
	err := s.pool.QueryRow(ctx, `
		SELECT tenant_id, id, name, everyone, created_at, created_by
		  FROM cloud_meta.access_groups WHERE tenant_id = $1 AND id = $2`, tenantID, id).
		Scan(&g.TenantID, &g.ID, &g.Name, &g.Everyone, &g.CreatedAt, &g.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return Group{}, ErrNotFound
	}
	if err != nil {
		return Group{}, fmt.Errorf("read group: %w", err)
	}
	return g, nil
}

func (s *PostgresStore) CreateGroup(ctx context.Context, g Group) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cloud_meta.access_groups (tenant_id, id, name, everyone, created_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)`, g.TenantID, g.ID, g.Name, g.Everyone, g.CreatedAt.UTC(), g.CreatedBy)
	if uniqueViolation(err) {
		return ErrExists
	}
	if err != nil {
		return fmt.Errorf("create group: %w", err)
	}
	return nil
}

func (s *PostgresStore) RenameGroup(ctx context.Context, tenantID, id, name string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE cloud_meta.access_groups SET name = $3 WHERE tenant_id = $1 AND id = $2`, tenantID, id, name)
	if uniqueViolation(err) {
		return ErrExists
	}
	if err != nil {
		return fmt.Errorf("rename group: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) DeleteGroup(ctx context.Context, tenantID, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a committed tx's rollback is a no-op
	if _, err := tx.Exec(ctx, `DELETE FROM cloud_meta.access_grants WHERE tenant_id = $1 AND group_id = $2`, tenantID, id); err != nil {
		return fmt.Errorf("delete group's grants: %w", err)
	}
	// Its members go with it (ON DELETE CASCADE).
	tag, err := tx.Exec(ctx, `DELETE FROM cloud_meta.access_groups WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

func scanMember(r pgx.CollectableRow) (GroupMember, error) {
	var m GroupMember
	var role string
	err := r.Scan(&m.TenantID, &m.Group, &m.Subject, &role, &m.AddedAt, &m.AddedBy)
	m.Role = GroupRole(role)
	return m, err
}

func (s *PostgresStore) Members(ctx context.Context, tenantID, group string) ([]GroupMember, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id, group_id, subject, role, added_at, added_by
		  FROM cloud_meta.access_group_members WHERE tenant_id = $1 AND group_id = $2 ORDER BY subject`, tenantID, group)
	if err != nil {
		return nil, fmt.Errorf("list group members: %w", err)
	}
	return pgx.CollectRows(rows, scanMember)
}

func (s *PostgresStore) SetMember(ctx context.Context, m GroupMember) error {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO cloud_meta.access_group_members (tenant_id, group_id, subject, role, added_at, added_by)
		SELECT $1, $2, $3, $4, $5, $6
		 WHERE EXISTS (SELECT 1 FROM cloud_meta.access_groups WHERE tenant_id = $1 AND id = $2)
		ON CONFLICT (tenant_id, group_id, subject) DO UPDATE SET role = EXCLUDED.role`,
		m.TenantID, m.Group, m.Subject, string(m.Role), m.AddedAt.UTC(), m.AddedBy)
	if err != nil {
		return fmt.Errorf("set group member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) RemoveMember(ctx context.Context, tenantID, group, subject string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM cloud_meta.access_group_members WHERE tenant_id = $1 AND group_id = $2 AND subject = $3`, tenantID, group, subject)
	if err != nil {
		return fmt.Errorf("remove group member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) MembershipsOf(ctx context.Context, tenantID, subject string) ([]GroupMember, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id, group_id, subject, role, added_at, added_by
		  FROM cloud_meta.access_group_members WHERE tenant_id = $1 AND subject = $2 ORDER BY group_id`, tenantID, subject)
	if err != nil {
		return nil, fmt.Errorf("list a person's groups: %w", err)
	}
	return pgx.CollectRows(rows, scanMember)
}

func (s *PostgresStore) Grants(ctx context.Context, tenantID, agent string) ([]Grant, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id, agent_id, group_id, subject, created_at, created_by
		  FROM cloud_meta.access_grants
		 WHERE tenant_id = $1 AND ($2 = '' OR agent_id = $2)
		 ORDER BY agent_id, group_id, subject`, tenantID, agent)
	if err != nil {
		return nil, fmt.Errorf("list grants: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Grant, error) {
		var g Grant
		err := r.Scan(&g.TenantID, &g.Agent, &g.Group, &g.Subject, &g.CreatedAt, &g.CreatedBy)
		return g, err
	})
}

func (s *PostgresStore) AddGrant(ctx context.Context, g Grant) error {
	if (g.Group == "") == (g.Subject == "") {
		return errors.New("access: a grant names a group or a person")
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO cloud_meta.access_grants (tenant_id, agent_id, group_id, subject, created_at, created_by)
		SELECT $1, $2, $3, $4, $5, $6
		 WHERE $3 = '' OR EXISTS (SELECT 1 FROM cloud_meta.access_groups WHERE tenant_id = $1 AND id = $3)
		ON CONFLICT DO NOTHING`, g.TenantID, g.Agent, g.Group, g.Subject, g.CreatedAt.UTC(), g.CreatedBy)
	if err != nil {
		return fmt.Errorf("add grant: %w", err)
	}
	if tag.RowsAffected() == 0 && g.Group != "" {
		// Either granted already, or no such group.
		if _, err := s.Group(ctx, g.TenantID, g.Group); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) ForgetSubject(ctx context.Context, tenantID, subject string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("forget subject: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a committed tx's rollback is a no-op
	if _, err := tx.Exec(ctx, `DELETE FROM cloud_meta.access_group_members WHERE tenant_id = $1 AND subject = $2`, tenantID, subject); err != nil {
		return fmt.Errorf("forget subject's groups: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM cloud_meta.access_grants WHERE tenant_id = $1 AND subject = $2`, tenantID, subject); err != nil {
		return fmt.Errorf("forget subject's grants: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) WelcomeInvite(ctx context.Context, tenantID, inviteID, subject string, at time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("welcome invite: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a committed tx's rollback is a no-op
	tag, err := tx.Exec(ctx, `DELETE FROM cloud_meta.access_backfill_invites WHERE tenant_id = $1 AND invite_id = $2`, tenantID, inviteID)
	if err != nil {
		return fmt.Errorf("welcome invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil // not pending at the backfill
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO cloud_meta.access_group_members (tenant_id, group_id, subject, role, added_at, added_by)
		SELECT $1, id, $2, 'member', $3, 'backfill'
		  FROM cloud_meta.access_groups WHERE tenant_id = $1 AND everyone
		ON CONFLICT DO NOTHING`, tenantID, subject, at.UTC()); err != nil {
		return fmt.Errorf("welcome invite: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) RemoveGrant(ctx context.Context, g Grant) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM cloud_meta.access_grants
		 WHERE tenant_id = $1 AND agent_id = $2 AND group_id = $3 AND subject = $4`, g.TenantID, g.Agent, g.Group, g.Subject)
	if err != nil {
		return fmt.Errorf("remove grant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) RecordRead(ctx context.Context, r Read) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cloud_meta.access_reads (tenant_id, id, reader, owner, agent_id, session_id, purpose, note, at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		r.TenantID, r.ID, r.Reader, r.Owner, r.Agent, r.Session, r.Purpose, r.Note, r.At)
	if err != nil {
		return fmt.Errorf("record read: %w", err)
	}
	return nil
}

func (s *PostgresStore) Reads(ctx context.Context, tenantID, owner, agent string, limit int) ([]Read, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id, id, reader, owner, agent_id, session_id, purpose, note, at
		  FROM cloud_meta.access_reads
		 WHERE tenant_id = $1 AND ($2 = '' OR owner = $2) AND ($3 = '' OR agent_id = $3)
		 ORDER BY at DESC, id DESC LIMIT $4`, tenantID, owner, agent, readLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list reads: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Read, error) {
		var r Read
		err := row.Scan(&r.TenantID, &r.ID, &r.Reader, &r.Owner, &r.Agent, &r.Session, &r.Purpose, &r.Note, &r.At)
		return r, err
	})
}

// readLimit bounds a listing of reads: 200 unless asked for fewer, at
// most 500.
func readLimit(limit int) int {
	if limit <= 0 || limit > 500 {
		return 200
	}
	return limit
}
