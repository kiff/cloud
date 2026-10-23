package mandate

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"
)

// The in-memory exception store: one mutex is the whole of its locking,
// which is correct for tests and a single process only.

func (s *MemoryStore) GetException(_ context.Context, tenantID, proposalID string) (Exception, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.exceptions[key(tenantID, proposalID)]
	return e, ok, nil
}

func (s *MemoryStore) SaveException(_ context.Context, e Exception) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exceptions[key(e.TenantID, e.ProposalID)] = e
	return nil
}

func (s *MemoryStore) Redeem(ctx context.Context, e Exception, draws []Draw, entries []ExceptionEntry, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failRecord != nil {
		return s.failRecord
	}
	if e.ExternalAnswer != nil && e.ExternalAnswer.Decision == "approve" {
		g, err := s.approverLocked(ctx, e.TenantID, e.ExternalAnswer.GrantID)
		if err != nil {
			return err
		}
		if !g.Active(at) {
			return ErrApproverGrant
		}
	}
	if s.claims[key(e.TenantID, e.ProposalID)] {
		return ErrProposalSpent
	}
	s.claims[key(e.TenantID, e.ProposalID)] = true
	for _, d := range draws {
		s.draws[key(d.TenantID, d.MandateID, d.ProposalID, d.QuantityKey)] = d
	}
	s.entries = append(s.entries, entries...)
	t := at.UTC()
	e.Status, e.ConsumedAt = ExceptionConsumed, &t
	s.exceptions[key(e.TenantID, e.ProposalID)] = e
	return nil
}

func (s *MemoryStore) Exceptions(_ context.Context, tenantID string, status ExceptionStatus) ([]Exception, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Exception{}
	for _, e := range s.exceptions {
		if e.TenantID == tenantID && (status == "" || e.Status == status) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *MemoryStore) Exception(_ context.Context, tenantID, id string) (Exception, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.exceptions {
		if e.TenantID == tenantID && e.ID == id {
			return e, nil
		}
	}
	return Exception{}, ErrExceptionNotFound
}

func (s *MemoryStore) ExceptionEntries(_ context.Context, tenantID, mandateID string, since time.Time) ([]ExceptionEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []ExceptionEntry{}
	for _, en := range s.entries {
		if en.TenantID == tenantID && en.MandateID == mandateID && !en.RecordedAt.Before(since) {
			out = append(out, en)
		}
	}
	return out, nil
}

func (s *MemoryStore) answer(ctx context.Context, tenantID, id string, now time.Time, fn func(*Exception) error) (Exception, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.exceptions {
		if e.TenantID != tenantID || e.ID != id {
			continue
		}
		changed, err := refreshExternalApproval(ctx, memoryApproverReader{s}, &e, now)
		if err != nil {
			return Exception{}, err
		}
		if err := fn(&e); err != nil {
			if changed && errors.Is(err, ErrExceptionExpired) {
				if s.failRecord != nil {
					return Exception{}, s.failRecord
				}
				s.exceptions[k] = e
			}
			return Exception{}, err
		}
		s.exceptions[k] = e
		return e, nil
	}
	return Exception{}, ErrExceptionNotFound
}

func (s *MemoryStore) ApproveOnce(ctx context.Context, tenantID, id, by string, now time.Time) (Exception, error) {
	return s.answer(ctx, tenantID, id, now, func(e *Exception) error {
		requireFreshConsent(e)
		if e.Expired(now) {
			return ErrExceptionExpired
		}
		if e.Status != ExceptionHeld {
			return ErrExceptionState
		}
		if !e.CanApproveOnce() {
			return ErrCumulativeApproval
		}
		e.AccountingVersion = CumulativeAccountingVersion
		resolve(e, ExceptionApproved, by, now)
		return nil
	})
}

func (s *MemoryStore) Reject(ctx context.Context, tenantID, id, by string, now time.Time, r Refusal) (Exception, error) {
	return s.answer(ctx, tenantID, id, now, func(e *Exception) error {
		if e.Expired(now) {
			return ErrExceptionExpired
		}
		if e.Status != ExceptionHeld && e.Status != ExceptionApproved {
			return ErrExceptionState
		}
		var cards []Mandate
		for _, mid := range e.MandateIDs() {
			if m, ok := s.mandates[key(tenantID, mid)]; ok {
				cards = append(cards, m)
			}
		}
		if err := refuseWith(e, r, cards); err != nil {
			return err
		}
		resolve(e, ExceptionRejected, by, now)
		return nil
	})
}

func (s *MemoryStore) ExpireHeld(_ context.Context, before time.Time, limit int) ([]Exception, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expiryTold == nil {
		s.expiryTold = map[string]bool{}
	}
	var out []Exception
	for k, e := range s.exceptions {
		if len(out) == limit {
			break
		}
		if e.ExpiresAt == nil || e.ExpiresAt.After(before) || s.expiryTold[k] {
			continue
		}
		if e.Status != ExceptionHeld && e.Status != ExceptionExpired {
			continue
		}
		if e.Status == ExceptionHeld {
			expire(&e)
			s.exceptions[k] = e
		}
		s.expiryTold[k] = true
		// An expiry older than the window is recorded, not announced late.
		if e.ExpiresAt.After(before.Add(-ExpiryTellWindow)) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *MemoryStore) Withdraw(ctx context.Context, tenantID, id, agent string, now time.Time) (Exception, error) {
	return s.answer(ctx, tenantID, id, now, func(e *Exception) error { return withdraw(e, agent, now) })
}

func (s *MemoryStore) ChangeCard(ctx context.Context, tenantID, id, by string, now time.Time) (Exception, error) {
	return s.ChangeCardRoom(ctx, tenantID, id, by, now, RoomThisWindow, time.Time{})
}
func (s *MemoryStore) ChangeCardRoom(ctx context.Context, tenantID, id, by string, now time.Time, duration RoomDuration, until time.Time) (Exception, error) {
	return s.answer(ctx, tenantID, id, now, func(e *Exception) error {
		if e.Expired(now) {
			return ErrExceptionExpired
		}
		if e.Status != ExceptionHeld {
			return ErrExceptionState
		}
		usage := limitUsage{Usage: UsageFunc(func(mandateID string, q Quantity, since time.Time) (int64, error) {
			return s.usedLocked(tenantID, mandateID, q.Key(), since, e.ProposalID), nil
		}), read: func(m Mandate, a Aggregate, at time.Time) (int64, error) {
			b, err := s.limitBalanceLocked(tenantID, m, a, at, e.ProposalID)
			return b.Net, err
		}}
		changes := []Mandate{}
		for _, mid := range e.MandateIDs() {
			m, ok := s.mandates[key(tenantID, mid)]
			if !ok || !m.Active(now) || !m.HeldBy(e.Subject) {
				return ErrExceptionState
			}
			changed, ok, err := roomForHold(m, *e, usage, by, now, duration, until)
			if err != nil {
				return err
			}
			if ok {
				changes = append(changes, changed)
			}
		}
		for _, m := range changes {
			s.issueLocked(m, by)
		}
		resolve(e, ExceptionChanged, by, now)
		return nil
	})
}

func (s *MemoryStore) invalidateLocked(tenantID, mandateID, reason string, at time.Time) {
	for k, e := range s.exceptions {
		if e.TenantID != tenantID || (e.Status != ExceptionHeld && e.Status != ExceptionApproved) {
			continue
		}
		if !breachedIDs(e.Breaches)[mandateID] {
			continue
		}
		t := at.UTC()
		preserveApproval(&e)
		e.Status, e.Reason, e.ResolvedBy, e.ResolvedAt = ExceptionInvalidated, reason, "", &t
		s.exceptions[k] = e
	}
}

// invalidateHoldersLocked is invalidateLocked for the exceptions of some
// agents only: those that stopped holding a shared Card. s.mu must be
// held.
func (s *MemoryStore) invalidateHoldersLocked(tenantID, mandateID string, agents []string, reason string, at time.Time) {
	for k, e := range s.exceptions {
		if e.TenantID != tenantID || (e.Status != ExceptionHeld && e.Status != ExceptionApproved) {
			continue
		}
		if !breachedIDs(e.Breaches)[mandateID] || !slices.Contains(agents, e.Subject) {
			continue
		}
		t := at.UTC()
		preserveApproval(&e)
		e.Status, e.Reason, e.ResolvedBy, e.ResolvedAt = ExceptionInvalidated, reason, "", &t
		s.exceptions[k] = e
	}
}
