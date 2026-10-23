package mandate

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Membership/governance writers lock this row FOR UPDATE. Take a shared
// lock after Card locks, before checking policy, and retain it through
// commit. External calls can run concurrently across Cards; withdrawal
// cannot commit between their policy check and their answer/redemption.
func (t pgCardTx) LockApproverPolicy(ctx context.Context, tenant string) error {
	if _, err := t.tx.Exec(ctx, `INSERT INTO cloud_meta.tenant_governance(tenant_id) VALUES($1) ON CONFLICT DO NOTHING`, tenant); err != nil {
		return err
	}
	var id string
	return t.tx.QueryRow(ctx, `SELECT tenant_id FROM cloud_meta.tenant_governance WHERE tenant_id=$1 FOR SHARE`, tenant).Scan(&id)
}

func approverRow(ctx context.Context, q querier, where string, args ...any) (ApproverGrant, error) {
	rows, err := q.Query(ctx, `SELECT grant_record,key_hash,revoked_at,last_seen_at FROM cloud_meta.approver_grants WHERE `+where, args...)
	if err != nil {
		return ApproverGrant{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if rows.Err() != nil {
			return ApproverGrant{}, rows.Err()
		}
		return ApproverGrant{}, ErrApproverGrant
	}
	var b []byte
	var hash string
	var revoked, seen *time.Time
	if err := rows.Scan(&b, &hash, &revoked, &seen); err != nil {
		return ApproverGrant{}, err
	}
	var g ApproverGrant
	if err := json.Unmarshal(b, &g); err != nil {
		return g, err
	}
	g.KeyHash, g.RevokedAt, g.LastSeenAt = hash, revoked, seen
	return g, nil
}

// The minter must still be an active admin. The existing strict human-only
// separation-of-duties policy applies to this narrow credential as well.
func approverPolicy(ctx context.Context, q querier, g ApproverGrant) error {
	rows, err := q.Query(ctx, `SELECT role,status FROM cloud_meta.tenant_memberships WHERE tenant_id=$1 AND subject=$2 AND status<>'removed'`, g.TenantID, g.CreatedBy)
	if err != nil {
		return err
	}
	var role, status string
	if rows.Next() {
		err = rows.Scan(&role, &status)
	}
	rows.Close()
	if err != nil {
		return err
	}
	if role != "admin" || status != "active" {
		return ErrApproverGrant
	}
	rows, err = q.Query(ctx, `SELECT separation_of_duties FROM cloud_meta.tenant_governance WHERE tenant_id=$1`, g.TenantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var sod bool
	if rows.Next() {
		if err = rows.Scan(&sod); err != nil {
			return err
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if sod {
		return ErrApproverGrant
	}
	return nil
}

func (s *PostgresStore) ApproverGrant(ctx context.Context, t, id string) (ApproverGrant, error) {
	g, err := approverRow(ctx, s.pool, "tenant_id=$1 AND id=$2", t, id)
	if err == nil {
		err = approverPolicy(ctx, s.pool, g)
	}
	return g, err
}
func (t pgCardTx) ApproverGrant(ctx context.Context, tenant, id string) (ApproverGrant, error) {
	g, err := approverRow(ctx, t.tx, "tenant_id=$1 AND id=$2", tenant, id)
	if err == nil {
		err = approverPolicy(ctx, t.tx, g)
	}
	return g, err
}
func (s *PostgresStore) ApproverByHash(ctx context.Context, hash string) (ApproverGrant, error) {
	g, err := approverRow(ctx, s.pool, "key_hash=$1", hash)
	if err == nil {
		err = approverPolicy(ctx, s.pool, g)
	}
	return g, err
}
func (s *PostgresStore) CreateApproverGrant(ctx context.Context, g ApproverGrant) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockCards(ctx, tx, g.TenantID, []string{g.CardID}); err != nil {
		return err
	}
	if err = (pgCardTx{tx: tx}).LockApproverPolicy(ctx, g.TenantID); err != nil {
		return err
	}
	ms, err := forSubject(ctx, tx, g.TenantID, g.Worker)
	if err != nil {
		return err
	}
	var m Mandate
	for _, card := range ms {
		if card.ID == g.CardID {
			m = card
		}
	}
	if err = validateApproverGrant(g, m, time.Now().UTC()); err != nil {
		return err
	}
	if err = approverPolicy(ctx, tx, g); err != nil {
		return err
	}
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO cloud_meta.approver_grants(tenant_id,id,card_id,key_hash,grant_record) VALUES($1,$2,$3,$4,$5)`, g.TenantID, g.ID, g.CardID, g.KeyHash, b)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *PostgresStore) RevokeApproverGrant(ctx context.Context, t, id string, now time.Time) error {
	// Revocation and collection serialize on the same Card lock. An already
	// redeemed decision is history; an uncollected approval loses authority.
	g, err := approverRow(ctx, s.pool, "tenant_id=$1 AND id=$2", t, id)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockCards(ctx, tx, t, []string{g.CardID}); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE cloud_meta.approver_grants SET revoked_at=COALESCE(revoked_at,$3) WHERE tenant_id=$1 AND id=$2`, t, id, now.UTC())
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *PostgresStore) AnswerExternal(ctx context.Context, t, id, gid string, r ExternalReview, now time.Time) (Exception, error) {
	g, err := approverRow(ctx, s.pool, "tenant_id=$1 AND id=$2", t, gid)
	if err != nil {
		return Exception{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Exception{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockCards(ctx, tx, t, []string{g.CardID}); err != nil {
		return Exception{}, err
	}
	if err = (pgCardTx{tx: tx}).LockApproverPolicy(ctx, t); err != nil {
		return Exception{}, err
	}
	g, err = approverRow(ctx, tx, "tenant_id=$1 AND id=$2", t, gid)
	if err != nil {
		return Exception{}, err
	}
	if err = approverPolicy(ctx, tx, g); err != nil {
		return Exception{}, err
	}
	ms, err := forSubject(ctx, tx, t, g.Worker)
	if err != nil {
		return Exception{}, err
	}
	var m Mandate
	for _, card := range ms {
		if card.ID == g.CardID {
			m = card
		}
	}
	rows, err := tx.Query(ctx, exceptionColumns+` WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, t, id)
	if err != nil {
		return Exception{}, err
	}
	es, err := scanExceptions(rows)
	if err != nil {
		return Exception{}, err
	}
	if len(es) != 1 {
		return Exception{}, ErrExceptionNotFound
	}
	e := es[0]
	changed, err := refreshExternalApproval(ctx, pgCardTx{tx: tx}, &e, now)
	if err != nil {
		return Exception{}, err
	}
	if err = applyExternalAnswer(g, m, &e, r, now); err != nil {
		if changed && errors.Is(err, ErrExceptionExpired) {
			if writeErr := saveException(ctx, tx, e); writeErr != nil {
				return Exception{}, writeErr
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return Exception{}, commitErr
			}
		}
		return Exception{}, err
	}
	if err = saveException(ctx, tx, e); err != nil {
		return Exception{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Exception{}, err
	}
	return e, nil
}

var _ ApproverStore = (*PostgresStore)(nil)

// Listing is management-only. It includes inactive grants so an admin can
// understand and revoke a connection even after its minter loses authority.
func (s *PostgresStore) ListApproverGrants(ctx context.Context, tenant, card string) ([]ApproverGrant, error) {
	rows, err := s.pool.Query(ctx, `SELECT grant_record,key_hash,revoked_at,last_seen_at FROM cloud_meta.approver_grants WHERE tenant_id=$1 AND card_id=$2 ORDER BY id`, tenant, card)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ApproverGrant{}
	for rows.Next() {
		var b []byte
		var hash string
		var revoked, seen *time.Time
		if err := rows.Scan(&b, &hash, &revoked, &seen); err != nil {
			return nil, err
		}
		var g ApproverGrant
		if err := json.Unmarshal(b, &g); err != nil {
			return nil, err
		}
		g.KeyHash, g.RevokedAt, g.LastSeenAt = hash, revoked, seen
		out = append(out, g)
	}
	return out, rows.Err()
}
func (s *PostgresStore) TouchApprover(ctx context.Context, tenant, id string, now time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE cloud_meta.approver_grants SET last_seen_at=$3 WHERE tenant_id=$1 AND id=$2 AND revoked_at IS NULL AND (last_seen_at IS NULL OR last_seen_at < $3::timestamptz - interval '30 seconds')`, tenant, id, now.UTC())
	return err
}
