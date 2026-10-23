package membership

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore is the in-memory Store for tests and database-less runs.
// Mutate works on a copy and swaps it in only when fn succeeds, so a
// failed change leaves nothing behind, as the Postgres transaction does.
type MemoryStore struct {
	mu      sync.Mutex
	state   memState
	answers map[string]HoldAnswer // tenant|hold
	now     func() time.Time
}

type memState struct {
	members    []Member
	invites    []Invite
	governance map[string]Governance
	audit      []AuditEvent
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		state:   memState{governance: map[string]Governance{}},
		answers: map[string]HoldAnswer{},
		now:     func() time.Time { return time.Now().UTC() },
	}
}

// WithClock substitutes the clock (tests).
func (s *MemoryStore) WithClock(now func() time.Time) *MemoryStore {
	s.now = now
	return s
}

func (st memState) clone() memState {
	c := memState{
		members:    append([]Member(nil), st.members...),
		invites:    append([]Invite(nil), st.invites...),
		governance: make(map[string]Governance, len(st.governance)),
		audit:      append([]AuditEvent(nil), st.audit...),
	}
	for k, v := range st.governance {
		c.governance[k] = v
	}
	return c
}

func (st memState) live(tenantID, subject string) (Member, bool) {
	for _, m := range st.members {
		if m.TenantID == tenantID && m.Subject == subject && m.Status != StatusRemoved {
			return m, true
		}
	}
	return Member{}, false
}

func (s *MemoryStore) Live(_ context.Context, tenantID, subject string) (Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.state.live(tenantID, subject); ok {
		return m, nil
	}
	return Member{}, ErrNotFound
}

func (s *MemoryStore) Latest(_ context.Context, tenantID, subject string) (Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.state.live(tenantID, subject); ok {
		return m, nil
	}
	var latest *Member
	for i := range s.state.members {
		m := s.state.members[i]
		if m.TenantID == tenantID && m.Subject == subject {
			if latest == nil || m.CreatedAt.After(latest.CreatedAt) {
				latest = &m
			}
		}
	}
	if latest == nil {
		return Member{}, ErrNotFound
	}
	return *latest, nil
}

func (s *MemoryStore) ForSubject(_ context.Context, subject string) ([]Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Member
	for _, m := range s.state.members {
		if m.Subject == subject && m.Status != StatusRemoved {
			out = append(out, m)
		}
	}
	sortMembers(out)
	return out, nil
}

func (s *MemoryStore) HasRemoved(_ context.Context, subject string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.state.members {
		if m.Subject == subject && m.Status == StatusRemoved {
			return true, nil
		}
	}
	return false, nil
}

func (s *MemoryStore) List(_ context.Context, tenantID string) ([]Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.list(tenantID), nil
}

func (st memState) list(tenantID string) []Member {
	var out []Member
	for _, m := range st.members {
		if m.TenantID == tenantID && m.Status != StatusRemoved {
			out = append(out, m)
		}
	}
	sortMembers(out)
	return out
}

func sortMembers(ms []Member) {
	sort.SliceStable(ms, func(i, j int) bool {
		if !ms[i].CreatedAt.Equal(ms[j].CreatedAt) {
			return ms[i].CreatedAt.Before(ms[j].CreatedAt)
		}
		return ms[i].ID < ms[j].ID
	})
}

func (s *MemoryStore) Governance(_ context.Context, tenantID string) (Governance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g, ok := s.state.governance[tenantID]; ok {
		return g, nil
	}
	return Governance{TenantID: tenantID}, nil
}

func (s *MemoryStore) Invites(_ context.Context, tenantID string) ([]Invite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Invite
	for _, i := range s.state.invites {
		if i.TenantID == tenantID {
			out = append(out, i)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].CreatedAt.After(out[b].CreatedAt) })
	return out, nil
}

func (s *MemoryStore) InviteByHash(_ context.Context, hash [32]byte) (Invite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, i := range s.state.invites {
		if i.TokenHash == hash {
			return i, nil
		}
	}
	return Invite{}, ErrNotFound
}

func (s *MemoryStore) Audit(_ context.Context, tenantID string, limit int) ([]AuditEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AuditEvent
	for i := len(s.state.audit) - 1; i >= 0; i-- {
		if e := s.state.audit[i]; e.TenantID == tenantID {
			out = append(out, e)
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (s *MemoryStore) HoldAnswers(_ context.Context, tenantID string, holdIDs []string) (map[string]HoldAnswer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]HoldAnswer{}
	for _, id := range holdIDs {
		if a, ok := s.answers[tenantID+"|"+id]; ok {
			out[id] = a
		}
	}
	return out, nil
}

func (s *MemoryStore) RecordHoldAnswer(_ context.Context, a HoldAnswer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.At.IsZero() {
		a.At = s.now()
	}
	s.answers[a.TenantID+"|"+a.HoldID] = a
	return nil
}

func (s *MemoryStore) Mutate(_ context.Context, tenantID string, fn func(Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx := &memTx{tenant: tenantID, st: s.state.clone(), now: s.now()}
	if err := fn(tx); err != nil {
		return err
	}
	s.state = tx.st
	return nil
}

type memTx struct {
	tenant string
	st     memState
	now    time.Time
}

func (t *memTx) Now() time.Time { return t.now }

func (t *memTx) Members() ([]Member, error) { return t.st.list(t.tenant), nil }

func (t *memTx) Member(subject string) (Member, bool, error) {
	m, ok := t.st.live(t.tenant, subject)
	return m, ok, nil
}

func (t *memTx) Governance() (Governance, error) {
	if g, ok := t.st.governance[t.tenant]; ok {
		return g, nil
	}
	return Governance{TenantID: t.tenant}, nil
}

func (t *memTx) Invite(id string) (Invite, bool, error) {
	for _, i := range t.st.invites {
		if i.ID == id && i.TenantID == t.tenant {
			return i, true, nil
		}
	}
	return Invite{}, false, nil
}

func (t *memTx) InsertMember(m Member) error {
	m.TenantID = t.tenant
	if _, ok := t.st.live(t.tenant, m.Subject); ok && m.Status != StatusRemoved {
		return ErrAlreadyMember
	}
	if m.Version == 0 {
		m.Version = 1
	}
	t.st.members = append(t.st.members, m)
	return nil
}

func (t *memTx) UpdateMember(m Member) error {
	for i := range t.st.members {
		if t.st.members[i].ID == m.ID && t.st.members[i].TenantID == t.tenant {
			m.Version = t.st.members[i].Version + 1
			t.st.members[i] = m
			return nil
		}
	}
	return ErrNotFound
}

func (t *memTx) InsertInvite(i Invite) error {
	i.TenantID = t.tenant
	t.st.invites = append(t.st.invites, i)
	return nil
}

func (t *memTx) UpdateInvite(i Invite) error {
	for k := range t.st.invites {
		if t.st.invites[k].ID == i.ID && t.st.invites[k].TenantID == t.tenant {
			t.st.invites[k] = i
			return nil
		}
	}
	return ErrNotFound
}

func (t *memTx) SetGovernance(g Governance) error {
	g.TenantID = t.tenant
	t.st.governance[t.tenant] = g
	return nil
}

func (t *memTx) Audit(e AuditEvent) error {
	e.TenantID = t.tenant
	if e.ID == "" {
		e.ID = newID("maud_")
	}
	if e.At.IsZero() {
		e.At = t.now
	}
	t.st.audit = append(t.st.audit, e)
	return nil
}

var _ Store = (*MemoryStore)(nil)
