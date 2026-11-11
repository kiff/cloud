package mandate

import "context"

// TafWordsEraser forgets the taf's own words a hold keeps (Request.TafAbout
// and TafReason), leaving the hold and its decision as they were. The words
// are no part of the fingerprint or of any receipt, so erasing them changes
// nothing a hold was decided or verified on.
type TafWordsEraser interface {
	EraseTafWords(ctx context.Context, tenantID, proposalID string) (bool, error)
}

func (s *MemoryStore) EraseTafWords(_ context.Context, tenantID, proposalID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.exceptions[key(tenantID, proposalID)]
	if !ok || (e.Request.TafAbout == "" && e.Request.TafReason == "") {
		return false, nil
	}
	e.Request.TafAbout, e.Request.TafReason = "", ""
	s.exceptions[key(tenantID, proposalID)] = e
	return true, nil
}

func (s *PostgresStore) EraseTafWords(ctx context.Context, tenantID, proposalID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE cloud_meta.mandate_exceptions SET request = request - 'TafAbout' - 'TafReason'
		 WHERE tenant_id = $1 AND proposal_id = $2 AND (request ? 'TafAbout' OR request ? 'TafReason')`, tenantID, proposalID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}
