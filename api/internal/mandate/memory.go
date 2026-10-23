package mandate

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryStore is the in-process implementation used by tests and local
// dev. It enforces the same tenant scoping as Postgres so a test cannot
// pass against a looser store than production runs.
type MemoryStore struct {
	mu             sync.RWMutex
	expiryTold     map[string]bool // exceptions whose expiry the sweep told
	warningConfigs map[string][]WarningConfig
	mandates       map[string]Mandate    // keyed by tenant + id
	draws          map[string]Draw       // keyed by tenant + mandate + proposal
	revisions      map[string][]Revision // keyed by tenant + mandate, append-only
	claims         map[string]bool       // spent proposal ids, keyed by tenant + proposal
	exceptions     map[string]Exception  // keyed by tenant + proposal
	entries        []ExceptionEntry
	approvers      map[string]ApproverGrant
	approverPolicy func(context.Context, string, string) error
	releases       map[string]EffectRelease
	// trialMu guards trials, each trial run's simulated draws. It is its
	// own lock: a trial reads real usage through mu while holding it.
	trialMu    sync.Mutex
	trials     map[string][]TrialDraw
	failUsage  error
	failRecord error
	failDraws  error
	// now is the clock revisions are stamped with. Overridable so a
	// test can issue a card and record draws on the same timeline; a
	// revision stamped from the wall clock while draws are dated
	// otherwise would make every movement look older than the card.
	now func() time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		mandates:   map[string]Mandate{},
		draws:      map[string]Draw{},
		revisions:  map[string][]Revision{},
		claims:     map[string]bool{},
		exceptions: map[string]Exception{},
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// SetClock overrides the clock revisions are stamped with.
func (s *MemoryStore) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// FailDrawsWith makes Draws return err, so a caller's behaviour when
// the movement ledger is unreadable can be exercised without unplugging
// a database. The balance and the movements are separate reads and can
// fail independently.
func (s *MemoryStore) FailDrawsWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failDraws = err
}

// FailUsageWith makes Used return err, so a caller's fail-closed
// behaviour can be exercised without unplugging a database.
func (s *MemoryStore) FailUsageWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failUsage = err
}

func key(parts ...string) string { return strings.Join(parts, "\x00") }

func (s *MemoryStore) Issue(_ context.Context, m Mandate, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issueLocked(m, by)
	return nil
}

// IssueAll issues every Card in ms, all or none.
func (s *MemoryStore) IssueAll(_ context.Context, ms []Mandate, by string, expect map[string]int64, retire []string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(ms) == 0 {
		return nil
	}
	tenant := ms[0].TenantID
	for id, want := range expect {
		if int64(len(s.revisions[key(tenant, id)])) != want {
			return ErrCardRevised
		}
	}
	for _, id := range retire {
		if _, ok := s.mandates[key(tenant, id)]; !ok {
			return ErrNotFound
		}
	}
	for _, m := range ms {
		s.issueLocked(m, by)
	}
	for _, id := range retire {
		m := s.mandates[key(tenant, id)]
		if m.RevokedAt == nil || at.Before(*m.RevokedAt) {
			at := at
			m.RevokedAt = &at
			s.mandates[key(tenant, id)] = m
		}
		s.invalidateLocked(tenant, id, "card_revoked", at)
	}
	return nil
}

// issueLocked writes a mandate and its revision; s.mu must be held.
func (s *MemoryStore) issueLocked(m Mandate, by string) {
	m = resolvedMeasures(m)
	if old, ok := s.mandates[key(m.TenantID, m.ID)]; ok {
		m = keepRoom(m, old.Aggregates)
		if m.Location == "" {
			m.Location = old.Location
		}
	} else if m.Location == "" {
		m.Location = m.DefaultLocation
	}
	m.DefaultLocation = ""
	if m.OnExceed == OnExceedKeep {
		m.OnExceed = OnExceedOwner
		if cur, ok := s.mandates[key(m.TenantID, m.ID)]; ok {
			m.OnExceed = cur.OnExceed
		}
	}
	m.OnExceed = policyOf(m)
	if m.HoldExpiry == HoldExpiryKeep {
		m.HoldExpiry = 0
		if cur, ok := s.mandates[key(m.TenantID, m.ID)]; ok {
			m.HoldExpiry = cur.HoldExpiry
		}
	}
	var before []string
	if cur, ok := s.mandates[key(m.TenantID, m.ID)]; ok {
		before = cur.HolderList()
	}
	m = m.withHolders(keepHolders(m, before))
	s.mandates[key(m.TenantID, m.ID)] = m
	// Whatever was held or approved for an agent that no longer holds
	// the Card does not travel to the others.
	if gone := removedHolders(before, m.Holders); len(gone) > 0 {
		s.invalidateHoldersLocked(m.TenantID, m.ID, gone, "holder_changed", s.now())
	}

	// When the change was made, which is not ValidFrom: one is when the
	// authority begins, the other is when somebody decided it.
	k := key(m.TenantID, m.ID)
	s.revisions[k] = append(s.revisions[k], Revision{
		TenantID:   m.TenantID,
		MandateID:  m.ID,
		Number:     int64(len(s.revisions[k]) + 1),
		At:         s.now(),
		By:         by,
		Subject:    m.Subject,
		Holders:    m.HolderList(),
		OnExceed:   policyOf(m),
		Grants:     m.Grants,
		Aggregates: m.Aggregates,
		ValidUntil: m.ValidUntil,
	})
}

// RecordAll writes every draw of one authorization under one lock, so a
// reader never observes half of an authorization.
func (s *MemoryStore) RecordAll(_ context.Context, draws []Draw) error {
	if len(draws) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failRecord != nil {
		return s.failRecord
	}
	// The claim: under the store's mutex, check-and-insert is atomic.
	if s.claims[key(draws[0].TenantID, draws[0].ProposalID)] {
		return ErrProposalSpent
	}
	for _, existing := range s.draws {
		if existing.TenantID == draws[0].TenantID && existing.ProposalID == draws[0].ProposalID {
			return ErrProposalSpent
		}
	}
	s.claims[key(draws[0].TenantID, draws[0].ProposalID)] = true
	for _, d := range draws {
		s.draws[key(d.TenantID, d.MandateID, d.ProposalID, d.QuantityKey)] = d
	}
	return nil
}

// FailRecordWith makes RecordAll fail, so a caller's all-or-nothing
// behaviour can be exercised without unplugging a database.
func (s *MemoryStore) FailRecordWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failRecord = err
}

// Move applies the same rule as PostgresStore.Move under the store's own
// mutex, which is the whole of this process's locking.
func (s *MemoryStore) Move(ctx context.Context, tenantID, mandateID, to, by string, now time.Time) (Mandate, error) {
	return s.SetHolders(ctx, tenantID, mandateID, []string{to}, nil, by, now)
}

// SetHolders applies the same rule as PostgresStore.SetHolders under the
// store's own mutex.
func (s *MemoryStore) SetHolders(_ context.Context, tenantID, mandateID string, holders, expected []string, by string, now time.Time) (Mandate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.mandates[key(tenantID, mandateID)]
	if !ok {
		return Mandate{}, ErrNotFound
	}
	if err := checkSetHolders(m, holders, expected, now); err != nil {
		return Mandate{}, err
	}
	m = m.withHolders(holders)
	s.issueLocked(m, by)
	return m, nil
}

func (s *MemoryStore) Revisions(_ context.Context, tenantID, mandateID string) ([]Revision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rs := s.revisions[key(tenantID, mandateID)]
	out := make([]Revision, len(rs))
	copy(out, rs)
	return out, nil
}

func (s *MemoryStore) Revoke(_ context.Context, tenantID, mandateID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.mandates[key(tenantID, mandateID)]
	if !ok {
		return ErrNotFound
	}
	// Revoking twice keeps the earlier instant: authority ended when it
	// first ended, and a retry must not quietly extend it.
	if m.RevokedAt == nil || at.Before(*m.RevokedAt) {
		at := at
		m.RevokedAt = &at
		s.mandates[key(tenantID, mandateID)] = m
	}
	s.invalidateLocked(tenantID, mandateID, "card_revoked", at)
	return nil
}

func (s *MemoryStore) Archive(_ context.Context, tenantID, mandateID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.mandates[key(tenantID, mandateID)]
	if !ok {
		return ErrNotFound
	}
	if m.RevokedAt == nil && (m.ValidUntil.IsZero() || at.Before(m.ValidUntil)) {
		return ErrActive
	}
	if m.ArchivedAt == nil || at.Before(*m.ArchivedAt) {
		at := at
		m.ArchivedAt = &at
		s.mandates[key(tenantID, mandateID)] = m
	}
	return nil
}

func (s *MemoryStore) ForSubject(_ context.Context, tenantID, subject string) ([]Mandate, error) {
	// Every Card the agent holds, shared or not.
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Mandate
	for _, m := range s.mandates {
		if m.TenantID != tenantID || !m.HeldBy(subject) {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *MemoryStore) List(_ context.Context, tenantID string) ([]Mandate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Mandate
	for _, m := range s.mandates {
		if m.TenantID == tenantID {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ValidFrom.After(out[j].ValidFrom) })
	return out, nil
}

func (s *MemoryStore) Used(_ context.Context, tenantID, mandateID, quantityKey string, since time.Time, excludeProposalID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.failUsage != nil {
		return 0, s.failUsage
	}
	return s.usedLocked(tenantID, mandateID, quantityKey, since, excludeProposalID), nil
}

// usedLocked sums draws; s.mu must be held.
func (s *MemoryStore) usedLocked(tenantID, mandateID, quantityKey string, since time.Time, excludeProposalID string) int64 {
	var total int64
	for _, d := range s.draws {
		if d.TenantID != tenantID || d.MandateID != mandateID || d.QuantityKey != quantityKey {
			continue
		}
		if d.DrawnAt.Before(since) {
			continue
		}
		if excludeProposalID != "" && d.ProposalID == excludeProposalID {
			continue
		}
		total += d.Amount
	}
	return total
}

func (s *MemoryStore) ProposalDraws(_ context.Context, tenantID, proposalID string) ([]Draw, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.failUsage != nil {
		return nil, s.failUsage
	}
	var out []Draw
	for _, d := range s.draws {
		if d.TenantID == tenantID && d.ProposalID == proposalID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (s *MemoryStore) Record(_ context.Context, d Draw) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Keyed on the proposal, so the same proposal recorded twice
	// consumes the limit once.
	k := key(d.TenantID, d.MandateID, d.ProposalID, d.QuantityKey)
	if _, exists := s.draws[k]; !exists {
		s.draws[k] = d
	}
	return nil
}

func (s *MemoryStore) Draws(_ context.Context, tenantID, mandateID string, since time.Time) ([]Draw, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.failDraws != nil {
		return nil, s.failDraws
	}
	var out []Draw
	for _, d := range s.draws {
		if d.TenantID != tenantID || d.MandateID != mandateID || d.DrawnAt.Before(since) {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DrawnAt.Before(out[j].DrawnAt) })
	return out, nil
}
