package mandate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// A sealed fill's release is the phone's tafs-chrome/release/v1 for one held
// call that fills a sign-in saved in the owner's Chrome (handlers/
// sealed_fill.go). KIFF keeps it from the approve-once that carried it until
// the gateway sends that call, and hands it over once: the gateway sends it to
// the tool beside the call, never inside its arguments, and planque puts it on
// the Chrome's step. It is never listed, logged or kept past its own exp: a
// release opens one fill, within 60 s of the owner's tap, and the extension
// takes its nonce once.

// SealedReleases keeps a held sealed fill's release between the owner's
// approve-once and the call's sending.
type SealedReleases interface {
	// KeepSealedRelease keeps release on hold id, which must be approved and
	// not yet used (ErrExceptionState otherwise).
	KeepSealedRelease(ctx context.Context, tenantID, id string, release json.RawMessage) error
	// TakeSealedRelease hands over the release kept for the held call
	// proposalID and forgets it, in one step: nil when none is kept, or
	// when its exp is past now.
	TakeSealedRelease(ctx context.Context, tenantID, proposalID string, now time.Time) (json.RawMessage, error)
}

// releaseExp is a release's own exp, in unix ms; 0 when it can't be read.
func releaseExp(raw json.RawMessage) int64 {
	var r struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return 0
	}
	return r.Exp
}

// usable is raw if it's a release still ahead of now, else nil.
func usable(raw json.RawMessage, now time.Time) json.RawMessage {
	if len(raw) == 0 || releaseExp(raw) <= now.UnixMilli() {
		return nil
	}
	return raw
}

func (s *MemoryStore) KeepSealedRelease(_ context.Context, tenantID, id string, release json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.exceptions {
		if e.TenantID != tenantID || e.ID != id {
			continue
		}
		if e.Status != ExceptionApproved {
			return ErrExceptionState
		}
		if s.sealedReleases == nil {
			s.sealedReleases = map[string]json.RawMessage{}
		}
		s.sealedReleases[k] = append(json.RawMessage(nil), release...)
		return nil
	}
	return ErrExceptionNotFound
}

func (s *MemoryStore) TakeSealedRelease(_ context.Context, tenantID, proposalID string, now time.Time) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(tenantID, proposalID)
	raw, ok := s.sealedReleases[k]
	if !ok {
		return nil, nil
	}
	delete(s.sealedReleases, k)
	return usable(raw, now), nil
}

func (s *PostgresStore) KeepSealedRelease(ctx context.Context, tenantID, id string, release json.RawMessage) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE cloud_meta.mandate_exceptions SET sealed_release = $3
		 WHERE tenant_id = $1 AND id = $2 AND status = 'approved'`, tenantID, id, []byte(release))
	if err != nil {
		return fmt.Errorf("keep sealed release: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var status string
		err := s.pool.QueryRow(ctx, `SELECT status FROM cloud_meta.mandate_exceptions WHERE tenant_id = $1 AND id = $2`, tenantID, id).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrExceptionNotFound
		}
		if err != nil {
			return fmt.Errorf("keep sealed release: %w", err)
		}
		return ErrExceptionState
	}
	return nil
}

func (s *PostgresStore) TakeSealedRelease(ctx context.Context, tenantID, proposalID string, now time.Time) (json.RawMessage, error) {
	// The old value, read and cleared in one statement: two sends of the
	// same call can't both take it.
	var raw []byte
	err := s.pool.QueryRow(ctx, `
		UPDATE cloud_meta.mandate_exceptions e SET sealed_release = NULL
		  FROM (SELECT tenant_id, id, sealed_release FROM cloud_meta.mandate_exceptions
		         WHERE tenant_id = $1 AND proposal_id = $2 AND sealed_release IS NOT NULL
		         FOR UPDATE) old
		 WHERE e.tenant_id = old.tenant_id AND e.id = old.id
		RETURNING old.sealed_release`, tenantID, proposalID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("take sealed release: %w", err)
	}
	return usable(raw, now), nil
}

// dropStaleSealedReleases forgets every release whose exp is past before,
// in the hold sweep: one the gateway never took (the run didn't continue)
// opens nothing after its exp, and isn't kept either.
func dropStaleSealedReleases(ctx context.Context, tx pgx.Tx, before time.Time) error {
	if _, err := tx.Exec(ctx, `
		UPDATE cloud_meta.mandate_exceptions SET sealed_release = NULL
		 WHERE sealed_release IS NOT NULL
		   AND COALESCE((sealed_release->>'exp')::BIGINT, 0) <= $1`, before.UnixMilli()); err != nil {
		return fmt.Errorf("drop stale sealed releases: %w", err)
	}
	return nil
}
