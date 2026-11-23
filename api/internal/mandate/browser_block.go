package mandate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// A held browser call's block is planque's description of it, signed by
// planque (handlers/browser_block.go checks the signature before it's kept):
// the site, the element's words and the page's excerpt the owner reads on
// the hold, and for a sealed fill the fields the phone's release signs. KIFF
// keeps it once, while the call is held, and serves it with the hold.

// BrowserBlocks keeps a held browser call's signed block.
type BrowserBlocks interface {
	// KeepBrowserBlock keeps block (planque's signed answer, as it came) on
	// the held call proposalID, once: ErrExceptionState when the call isn't
	// held or already has one, ErrExceptionNotFound when there's no such hold.
	KeepBrowserBlock(ctx context.Context, tenantID, proposalID string, block json.RawMessage) error
	// BrowserBlock is the block kept on the hold for proposalID, or nil.
	BrowserBlock(ctx context.Context, tenantID, proposalID string) (json.RawMessage, error)
}

func (s *MemoryStore) KeepBrowserBlock(_ context.Context, tenantID, proposalID string, block json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(tenantID, proposalID)
	e, ok := s.exceptions[k]
	if !ok {
		return ErrExceptionNotFound
	}
	if _, kept := s.browserBlocks[k]; kept || e.Status != ExceptionHeld {
		return ErrExceptionState
	}
	if s.browserBlocks == nil {
		s.browserBlocks = map[string]json.RawMessage{}
	}
	s.browserBlocks[k] = append(json.RawMessage(nil), block...)
	return nil
}

func (s *MemoryStore) BrowserBlock(_ context.Context, tenantID, proposalID string) (json.RawMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.browserBlocks[key(tenantID, proposalID)], nil
}

func (s *PostgresStore) KeepBrowserBlock(ctx context.Context, tenantID, proposalID string, block json.RawMessage) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE cloud_meta.mandate_exceptions SET browser_block = $3
		 WHERE tenant_id = $1 AND proposal_id = $2 AND status = 'held' AND browser_block IS NULL`, tenantID, proposalID, []byte(block))
	if err != nil {
		return fmt.Errorf("keep browser block: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var status string
		err := s.pool.QueryRow(ctx, `SELECT status FROM cloud_meta.mandate_exceptions WHERE tenant_id = $1 AND proposal_id = $2`, tenantID, proposalID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrExceptionNotFound
		}
		if err != nil {
			return fmt.Errorf("keep browser block: %w", err)
		}
		return ErrExceptionState
	}
	return nil
}

func (s *PostgresStore) BrowserBlock(ctx context.Context, tenantID, proposalID string) (json.RawMessage, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT browser_block FROM cloud_meta.mandate_exceptions WHERE tenant_id = $1 AND proposal_id = $2`, tenantID, proposalID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read browser block: %w", err)
	}
	return raw, nil
}
