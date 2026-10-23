package mandate

import (
	"context"
	"errors"
	"sort"
	"time"
)

// The caller already owns the MemoryStore mutex during effectful answers.
type memoryApproverReader struct{ s *MemoryStore }

func (r memoryApproverReader) ApproverGrant(ctx context.Context, tenant, id string) (ApproverGrant, error) {
	return r.s.approverLocked(ctx, tenant, id)
}

// SetApproverPolicy wires live membership/governance checks for local stores.
// Without it, external approval fails closed.
func (s *MemoryStore) SetApproverPolicy(f func(context.Context, string, string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approverPolicy = f
}

func (s *MemoryStore) CreateApproverGrant(ctx context.Context, g ApproverGrant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.mandates[key(g.TenantID, g.CardID)]
	if !ok {
		return ErrApproverGrant
	}
	if err := validateApproverGrant(g, m, s.now()); err != nil {
		return err
	}
	if s.approverPolicy == nil {
		return ErrApproverGrant
	}
	if err := s.approverPolicy(ctx, g.TenantID, g.CreatedBy); err != nil {
		return err
	}
	if s.approvers == nil {
		s.approvers = map[string]ApproverGrant{}
	}
	if _, ok := s.approvers[key(g.TenantID, g.ID)]; ok {
		return ErrApproverGrant
	}
	s.approvers[key(g.TenantID, g.ID)] = g
	return nil
}
func (s *MemoryStore) ApproverGrant(ctx context.Context, t, id string) (ApproverGrant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.approverLocked(ctx, t, id)
}
func (s *MemoryStore) approverLocked(ctx context.Context, t, id string) (ApproverGrant, error) {
	g, ok := s.approvers[key(t, id)]
	if !ok || s.approverPolicy == nil {
		return g, ErrApproverGrant
	}
	if err := s.approverPolicy(ctx, g.TenantID, g.CreatedBy); err != nil {
		return g, err
	}
	return g, nil
}
func (s *MemoryStore) ApproverByHash(ctx context.Context, hash string) (ApproverGrant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.approvers {
		if g.KeyHash == hash {
			return s.approverLocked(ctx, g.TenantID, g.ID)
		}
	}
	return ApproverGrant{}, ErrApproverGrant
}
func (s *MemoryStore) RevokeApproverGrant(_ context.Context, t, id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.approvers[key(t, id)]
	if !ok {
		return ErrApproverGrant
	}
	if g.RevokedAt == nil {
		at := now.UTC()
		g.RevokedAt = &at
		s.approvers[key(t, id)] = g
	}
	return nil
}
func (s *MemoryStore) AnswerExternal(ctx context.Context, t, id, gid string, r ExternalReview, now time.Time) (Exception, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, err := s.approverLocked(ctx, t, gid)
	if err != nil {
		return Exception{}, err
	}
	for k, e := range s.exceptions {
		if e.TenantID == t && e.ID == id {
			changed, err := refreshExternalApproval(ctx, memoryApproverReader{s}, &e, now)
			if err != nil {
				return Exception{}, err
			}
			if err := applyExternalAnswer(g, s.mandates[key(t, g.CardID)], &e, r, now); err != nil {
				if changed && errors.Is(err, ErrExceptionExpired) {
					if s.failRecord != nil {
						return Exception{}, s.failRecord
					}
					s.exceptions[k] = e
				}
				return Exception{}, err
			}
			if s.failRecord != nil {
				return Exception{}, s.failRecord
			}
			s.exceptions[k] = e
			return e, nil
		}
	}
	return Exception{}, ErrExceptionNotFound
}

func (s *MemoryStore) ListApproverGrants(_ context.Context, tenant, card string) ([]ApproverGrant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []ApproverGrant{}
	for _, g := range s.approvers {
		if g.TenantID == tenant && g.CardID == card {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *MemoryStore) TouchApprover(_ context.Context, tenant, id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(tenant, id)
	g, ok := s.approvers[k]
	if !ok {
		return ErrApproverGrant
	}
	if g.Active(now) {
		at := now.UTC()
		g.LastSeenAt = &at
		s.approvers[k] = g
	}
	return nil
}
