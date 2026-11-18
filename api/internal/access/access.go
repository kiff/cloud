// Package access is who may use, oversee or manage an agent: groups of
// an account's people, each with managers and members, and grants of an
// agent to a group or a person.
//
// A governance role says what someone may change in KIFF; what they may
// use comes from grants. Being on the account alone gives no use of any
// agent. KIFF answers the one question, "what may this subject do with
// agent X?", and the runtimes that run agents ask it (Decide).
package access

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/kiff/cloud/api/internal/membership"
)

// GroupRole is a person's role in a group.
type GroupRole string

const (
	// Manager oversees the work of the agents the group is granted, and
	// adds or removes the group's members among the account's people.
	Manager GroupRole = "manager"
	// Member uses the agents the group is granted.
	Member GroupRole = "member"
)

// ParseGroupRole returns the role named by s, or false.
func ParseGroupRole(s string) (GroupRole, bool) {
	switch GroupRole(s) {
	case Manager, Member:
		return GroupRole(s), true
	}
	return "", false
}

// Group is a set of the account's people. It lives inside one account:
// an organisational and permission boundary, not a tenant.
type Group struct {
	TenantID string
	ID       string
	Name     string
	// Everyone marks the group the backfill made from the account's
	// people when groups arrived, granted every taf then, so no one
	// lost a taf. Admins narrow it like any group.
	Everyone  bool
	CreatedAt time.Time
	CreatedBy string
}

// GroupMember is one person in a group.
type GroupMember struct {
	TenantID string
	Group    string
	Subject  string
	Role     GroupRole
	AddedAt  time.Time
	AddedBy  string
}

// Grant gives an agent's use to a group or to one person: exactly one of
// Group and Subject is set.
type Grant struct {
	TenantID  string
	Agent     string
	Group     string
	Subject   string
	CreatedAt time.Time
	CreatedBy string
}

// Answer is what a subject may do with an agent.
type Answer struct {
	// Use: start runs with it, and read their own sessions.
	Use bool `json:"use"`
	// Oversee: see everyone's work with it. A manager of a group the
	// agent is granted, or an account admin.
	Oversee bool `json:"oversee"`
	// Manage: change it (its Cards, versions, instructions, sharing):
	// cards.manage, as before groups.
	Manage bool `json:"manage"`
	// Groups are the subject's groups the agent is granted, by id.
	Groups []string `json:"groups"`
}

var (
	// ErrNotFound is a group, member or grant the account doesn't have.
	ErrNotFound = errors.New("access: not found")
	// ErrExists is a group name the account already uses.
	ErrExists = errors.New("access: a group of that name exists")
	// ErrInvalidName is a group name outside 1–64 characters, or with
	// control characters.
	ErrInvalidName = errors.New("access: a group's name is 1 to 64 characters, without control characters")
)

// NormalizeName trims a group's name and checks it.
func NormalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 64 {
		return "", ErrInvalidName
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", ErrInvalidName
		}
	}
	return name, nil
}

// Store keeps groups, their members and grants. Every method is
// tenant-scoped; a group, member or grant of another account is
// ErrNotFound.
type Store interface {
	// Groups returns the account's groups, by name.
	Groups(ctx context.Context, tenantID string) ([]Group, error)
	// Group returns one group, or ErrNotFound.
	Group(ctx context.Context, tenantID, id string) (Group, error)
	// CreateGroup records a new group; ErrExists for a name in use.
	CreateGroup(ctx context.Context, g Group) error
	// RenameGroup renames one; ErrExists for a name in use.
	RenameGroup(ctx context.Context, tenantID, id, name string) error
	// DeleteGroup removes a group, its members and its grants.
	DeleteGroup(ctx context.Context, tenantID, id string) error

	// Members returns a group's members, by subject.
	Members(ctx context.Context, tenantID, group string) ([]GroupMember, error)
	// SetMember adds a person to a group, or changes their role.
	SetMember(ctx context.Context, m GroupMember) error
	// RemoveMember takes a person out of a group; ErrNotFound when they
	// aren't in it.
	RemoveMember(ctx context.Context, tenantID, group, subject string) error
	// MembershipsOf returns the groups a person is in, with their role.
	MembershipsOf(ctx context.Context, tenantID, subject string) ([]GroupMember, error)

	// Grants returns an agent's grants. An empty agent returns every
	// grant of the account.
	Grants(ctx context.Context, tenantID, agent string) ([]Grant, error)
	// AddGrant records a grant; granting again changes nothing.
	AddGrant(ctx context.Context, g Grant) error
	// RemoveGrant removes one; ErrNotFound when it isn't there.
	RemoveGrant(ctx context.Context, g Grant) error

	// RecordRead keeps one read of another person's work: append only.
	RecordRead(ctx context.Context, r Read) error
	// Reads returns an account's reads, newest first, at most limit: of
	// one owner's work when owner is set, on one agent when agent is.
	Reads(ctx context.Context, tenantID, owner, agent string, limit int) ([]Read, error)

	// ForgetSubject takes a person who left the account out of every group,
	// and removes every grant to them, so coming back restores nothing.
	ForgetSubject(ctx context.Context, tenantID, subject string) error
	// WelcomeInvite adds the person who accepted an invitation that was
	// pending when the backfill ran to the account's Everyone group, as the
	// backfill would have, once. Any other invitation changes nothing.
	WelcomeInvite(ctx context.Context, tenantID, inviteID, subject string, at time.Time) error
}

// Decide is what subject may do with agent, from their membership on
// the account (nil when they have none, or it isn't active), the groups
// they're in, and the agent's grants.
//
//   - use: a grant to the subject, or to a group they're in;
//   - oversee: a manager of a group the agent is granted, or an account
//     admin (who oversees every agent);
//   - manage: their role holds cards.manage.
//
// Someone not active on the account may do nothing.
func Decide(m *membership.Member, groups []GroupMember, grants []Grant) Answer {
	a := Answer{Groups: []string{}}
	if m == nil || m.Status != membership.StatusActive {
		return a
	}
	in := make(map[string]GroupRole, len(groups))
	for _, g := range groups {
		in[g.Group] = g.Role
	}
	for _, g := range grants {
		switch {
		case g.Subject != "" && g.Subject == m.Subject:
			a.Use = true
		case g.Group != "":
			if role, ok := in[g.Group]; ok {
				a.Use = true
				a.Groups = append(a.Groups, g.Group)
				if role == Manager {
					a.Oversee = true
				}
			}
		}
	}
	if m.Role == membership.RoleAdmin {
		a.Oversee = true
	}
	a.Manage = membership.Can(m.Role, membership.CapCardsManage)
	return a
}

// MayRead is whether reader may read owner's work with an agent, from
// the reader's membership (nil when inactive), the groups each is in, and
// the agent's grants. An account admin reads any member's work. Anyone
// else manages a group the agent is granted to that the owner is in: a
// manager oversees their own group's work, never the rest of the agent's.
func MayRead(reader *membership.Member, readerGroups, ownerGroups []GroupMember, grants []Grant) bool {
	if reader == nil || reader.Status != membership.StatusActive {
		return false
	}
	if reader.Role == membership.RoleAdmin {
		return true
	}
	granted := map[string]bool{}
	for _, g := range grants {
		if g.Group != "" {
			granted[g.Group] = true
		}
	}
	theirs := map[string]bool{}
	for _, g := range ownerGroups {
		theirs[g.Group] = true
	}
	for _, g := range readerGroups {
		if g.Role == Manager && granted[g.Group] && theirs[g.Group] {
			return true
		}
	}
	return false
}

// ForSetup is the answer for the account's Setup (Coach), the guided
// assistant for setting up and fixing its tafs: it is for the account's
// admins and editors by default, so an active admin
// or editor may use it whether or not it was shared with them. Others use
// it only when it's shared, as any agent. What Coach changes in a taf
// still needs manage, and an editor's is only on the tafs they made
// (ForTaf).
func ForSetup(a Answer, m *membership.Member) Answer {
	if m != nil && m.Status == membership.StatusActive && (m.Role == membership.RoleAdmin || m.Role == membership.RoleEditor) {
		a.Use = true
	}
	return a
}

// ForTaf narrows a person's answer for a taf: an
// editor manages only the tafs they made (madeBy, the agent's creator), in
// Coach and in the app alike. Use stays as it is: they still chat with a
// taf shared with them, and answer its holds. Agents that aren't tafs
// (isTaf false) keep the role's manage.
func ForTaf(a Answer, m *membership.Member, isTaf bool, madeBy string) Answer {
	if isTaf && m != nil && m.Role == membership.RoleEditor && madeBy != m.Subject {
		a.Manage = false
	}
	return a
}

// GrantCreator gives a new agent's use to the person who made it, when no
// one has it yet: a taf someone starts is theirs until they share it. An
// agent with grants keeps them as they are.
func GrantCreator(ctx context.Context, s Store, tenantID, agent, subject string, now time.Time) error {
	if s == nil || subject == "" {
		return nil
	}
	grants, err := s.Grants(ctx, tenantID, agent)
	if err != nil || len(grants) > 0 {
		return err
	}
	return s.AddGrant(ctx, Grant{TenantID: tenantID, Agent: agent, Subject: subject, CreatedAt: now, CreatedBy: subject})
}

// Purposes are why someone reads another person's work, as they choose
// it before KIFF lets them: follow the work up, fix a problem, or look
// into a concern.
var Purposes = []string{"follow_up", "fix", "concern"}

// Read is one read of another person's work with an agent: who read
// whose, which session (empty for the list of their sessions), why, and
// when. Kept as the account's audit is; never edited.
type Read struct {
	TenantID string
	ID       string
	Reader   string
	Owner    string
	Agent    string
	Session  string
	Purpose  string
	Note     string
	At       time.Time
}
