package access

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
)

// MemoryStore is the in-memory Store, for tests and a server without
// Postgres. Concurrent-safe.
type MemoryStore struct {
	mu      sync.Mutex
	groups  map[[2]string]Group       // tenant, id
	members map[[3]string]GroupMember // tenant, group, subject
	grants  map[[4]string]Grant       // tenant, agent, group, subject
	// Invites pending when the backfill ran, by tenant and invite id.
	Invites map[[2]string]bool
	reads   []Read
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{groups: map[[2]string]Group{}, members: map[[3]string]GroupMember{}, grants: map[[4]string]Grant{},
		Invites: map[[2]string]bool{}}
}

func (s *MemoryStore) ForgetSubject(_ context.Context, tenantID, subject string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.members {
		if k[0] == tenantID && k[2] == subject {
			delete(s.members, k)
		}
	}
	for k := range s.grants {
		if k[0] == tenantID && k[3] == subject {
			delete(s.grants, k)
		}
	}
	return nil
}

func (s *MemoryStore) WelcomeInvite(_ context.Context, tenantID, inviteID, subject string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.Invites[[2]string{tenantID, inviteID}] {
		return nil
	}
	delete(s.Invites, [2]string{tenantID, inviteID})
	for k, g := range s.groups {
		if k[0] == tenantID && g.Everyone {
			if _, ok := s.members[[3]string{tenantID, g.ID, subject}]; !ok {
				s.members[[3]string{tenantID, g.ID, subject}] = GroupMember{TenantID: tenantID, Group: g.ID, Subject: subject, Role: Member, AddedAt: at, AddedBy: "backfill"}
			}
		}
	}
	return nil
}

func (s *MemoryStore) Groups(_ context.Context, tenantID string) ([]Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Group
	for k, g := range s.groups {
		if k[0] == tenantID {
			out = append(out, g)
		}
	}
	slices.SortFunc(out, func(a, b Group) int { return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out, nil
}

func (s *MemoryStore) Group(_ context.Context, tenantID, id string) (Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[[2]string{tenantID, id}]
	if !ok {
		return Group{}, ErrNotFound
	}
	return g, nil
}

// nameTaken reports whether another group of the account has name.
func (s *MemoryStore) nameTaken(tenantID, id, name string) bool {
	for k, g := range s.groups {
		if k[0] == tenantID && k[1] != id && strings.EqualFold(g.Name, name) {
			return true
		}
	}
	return false
}

func (s *MemoryStore) CreateGroup(_ context.Context, g Group) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.groups[[2]string{g.TenantID, g.ID}]; ok || s.nameTaken(g.TenantID, g.ID, g.Name) {
		return ErrExists
	}
	s.groups[[2]string{g.TenantID, g.ID}] = g
	return nil
}

func (s *MemoryStore) RenameGroup(_ context.Context, tenantID, id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[[2]string{tenantID, id}]
	if !ok {
		return ErrNotFound
	}
	if s.nameTaken(tenantID, id, name) {
		return ErrExists
	}
	g.Name = name
	s.groups[[2]string{tenantID, id}] = g
	return nil
}

func (s *MemoryStore) DeleteGroup(_ context.Context, tenantID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.groups[[2]string{tenantID, id}]; !ok {
		return ErrNotFound
	}
	delete(s.groups, [2]string{tenantID, id})
	for k := range s.members {
		if k[0] == tenantID && k[1] == id {
			delete(s.members, k)
		}
	}
	for k := range s.grants {
		if k[0] == tenantID && k[2] == id {
			delete(s.grants, k)
		}
	}
	return nil
}

func (s *MemoryStore) membersWhere(keep func([3]string) bool) []GroupMember {
	var out []GroupMember
	for k, m := range s.members {
		if keep(k) {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b GroupMember) int {
		return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Subject, b.Subject))
	})
	return out
}

func (s *MemoryStore) Members(_ context.Context, tenantID, group string) ([]GroupMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.membersWhere(func(k [3]string) bool { return k[0] == tenantID && k[1] == group }), nil
}

func (s *MemoryStore) SetMember(_ context.Context, m GroupMember) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.groups[[2]string{m.TenantID, m.Group}]; !ok {
		return ErrNotFound
	}
	k := [3]string{m.TenantID, m.Group, m.Subject}
	if was, ok := s.members[k]; ok {
		was.Role = m.Role
		m = was
	}
	s.members[k] = m
	return nil
}

func (s *MemoryStore) RemoveMember(_ context.Context, tenantID, group, subject string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := [3]string{tenantID, group, subject}
	if _, ok := s.members[k]; !ok {
		return ErrNotFound
	}
	delete(s.members, k)
	return nil
}

func (s *MemoryStore) MembershipsOf(_ context.Context, tenantID, subject string) ([]GroupMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.membersWhere(func(k [3]string) bool { return k[0] == tenantID && k[2] == subject }), nil
}

func (s *MemoryStore) Grants(_ context.Context, tenantID, agent string) ([]Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Grant
	for k, g := range s.grants {
		if k[0] == tenantID && (agent == "" || k[1] == agent) {
			out = append(out, g)
		}
	}
	slices.SortFunc(out, func(a, b Grant) int {
		return cmp.Or(cmp.Compare(a.Agent, b.Agent), cmp.Compare(a.Group, b.Group), cmp.Compare(a.Subject, b.Subject))
	})
	return out, nil
}

func (s *MemoryStore) AddGrant(_ context.Context, g Grant) error {
	if (g.Group == "") == (g.Subject == "") {
		return errors.New("access: a grant names a group or a person")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if g.Group != "" {
		if _, ok := s.groups[[2]string{g.TenantID, g.Group}]; !ok {
			return ErrNotFound
		}
	}
	k := [4]string{g.TenantID, g.Agent, g.Group, g.Subject}
	if _, ok := s.grants[k]; !ok {
		s.grants[k] = g
	}
	return nil
}

func (s *MemoryStore) RemoveGrant(_ context.Context, g Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := [4]string{g.TenantID, g.Agent, g.Group, g.Subject}
	if _, ok := s.grants[k]; !ok {
		return ErrNotFound
	}
	delete(s.grants, k)
	return nil
}

func (s *MemoryStore) RecordRead(_ context.Context, r Read) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, r)
	return nil
}

func (s *MemoryStore) Reads(_ context.Context, tenantID, owner, agent string, limit int) ([]Read, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Read{}
	for i := len(s.reads) - 1; i >= 0 && len(out) < readLimit(limit); i-- {
		r := s.reads[i]
		if r.TenantID == tenantID && (owner == "" || r.Owner == owner) && (agent == "" || r.Agent == agent) {
			out = append(out, r)
		}
	}
	return out, nil
}
