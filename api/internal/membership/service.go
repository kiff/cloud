package membership

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Service is every membership change, each checked against the
// invariants and written with its audit row in one transaction.
type Service struct {
	Store Store
}

// NewService wraps a store.
func NewService(store Store) *Service { return &Service{Store: store} }

func audit(tx Tx, actor Actor, action, target, oldV, newV string) error {
	return tx.Audit(AuditEvent{
		Actor: actor.Subject, Action: action, Target: target, OldValue: oldV, NewValue: newV,
		RequestID: actor.RequestID, AuthMethod: actor.AuthMethod, ReauthAgeS: actor.ReauthAgeS,
	})
}

// counts returns the active admins and active approvers among ms.
func counts(ms []Member) (admins, approvers int) {
	for _, m := range ms {
		if !m.Active() {
			continue
		}
		switch m.Role {
		case RoleAdmin:
			admins++
		case RoleApprover:
			approvers++
		}
	}
	return admins, approvers
}

// guardLoss refuses a change that would take target's active role away
// from the account when target is its last active admin (M9) or, with
// separation of duties on, its last active approver.
func guardLoss(tx Tx, target Member, newRole Role, stillActive bool) error {
	if !target.Active() {
		return nil
	}
	ms, err := tx.Members()
	if err != nil {
		return err
	}
	admins, approvers := counts(ms)
	keepsRole := stillActive && newRole == target.Role
	if target.Role == RoleAdmin && !keepsRole && admins <= 1 {
		return ErrLastAdmin
	}
	if target.Role == RoleApprover && !keepsRole && approvers <= 1 {
		g, err := tx.Governance()
		if err != nil {
			return err
		}
		if g.SeparationOfDuties {
			return ErrLastApprover
		}
	}
	return nil
}

// EnsureFounder makes subject the founding admin of a tenant that has
// no memberships yet (first sign-in, or a tenant not yet backfilled),
// and returns the subject's membership. A tenant that already has
// members and not this subject answers ErrNotMember: a person reaches
// an account only through a membership (M1).
func (s *Service) EnsureFounder(ctx context.Context, tenantID, subject string) (Member, error) {
	if m, err := s.Store.Live(ctx, tenantID, subject); err == nil {
		return m, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Member{}, err
	}
	var out Member
	err := s.Store.Mutate(ctx, tenantID, func(tx Tx) error {
		if m, ok, err := tx.Member(subject); err != nil {
			return err
		} else if ok {
			out = m
			return nil
		}
		ms, err := tx.Members()
		if err != nil {
			return err
		}
		if len(ms) > 0 {
			return ErrNotMember
		}
		now := tx.Now()
		out = Member{ID: newID("mem_"), Subject: subject, Role: RoleAdmin, Status: StatusActive,
			CreatedAt: now, UpdatedAt: now, Version: 1}
		if err := tx.InsertMember(out); err != nil {
			return err
		}
		out.TenantID = tenantID
		return audit(tx, Actor{Subject: "system"}, AuditFounded, subject, "", string(RoleAdmin))
	})
	return out, err
}

// InviteRequest is what an admin asks for when inviting.
type InviteRequest struct {
	Email    string
	Role     Role
	Lifetime time.Duration // 0 = the role's default
}

// CreateInvite records an invitation and returns it with its token. The
// token is returned once and never stored (M6).
func (s *Service) CreateInvite(ctx context.Context, tenantID string, actor Actor, req InviteRequest) (Invite, string, error) {
	if _, ok := ParseRole(string(req.Role)); !ok {
		return Invite{}, "", ErrInvalidRole
	}
	email := strings.TrimSpace(req.Email)
	if !validEmail(email) {
		return Invite{}, "", ErrInvalidEmail
	}
	life, err := InviteLifetime(req.Role, req.Lifetime)
	if err != nil {
		return Invite{}, "", err
	}
	token, hash, err := NewInviteToken()
	if err != nil {
		return Invite{}, "", err
	}
	var inv Invite
	err = s.Store.Mutate(ctx, tenantID, func(tx Tx) error {
		now := tx.Now()
		inv = Invite{ID: newID("inv_"), TenantID: tenantID, Email: email, Role: req.Role,
			InvitedBy: actor.Subject, TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(life)}
		if err := tx.InsertInvite(inv); err != nil {
			return err
		}
		return audit(tx, actor, AuditInvited, inv.ID, "", string(req.Role))
	})
	if err != nil {
		return Invite{}, "", err
	}
	return inv, token, nil
}

// RevokeInvite withdraws a pending invite.
func (s *Service) RevokeInvite(ctx context.Context, tenantID string, actor Actor, inviteID string) (Invite, error) {
	var inv Invite
	err := s.Store.Mutate(ctx, tenantID, func(tx Tx) error {
		i, ok, err := tx.Invite(inviteID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		now := tx.Now()
		if i.StateAt(now) != InvitePending {
			return ErrInviteNotPending
		}
		i.RevokedAt = &now
		if err := tx.UpdateInvite(i); err != nil {
			return err
		}
		inv = i
		return audit(tx, actor, AuditInviteRevoked, i.ID, string(i.Role), "")
	})
	return inv, err
}

// PendingInvite returns the invite a token names, if it can still be
// accepted.
func (s *Service) PendingInvite(ctx context.Context, token string, now time.Time) (Invite, error) {
	inv, err := s.Store.InviteByHash(ctx, HashInviteToken(token))
	if err != nil {
		return Invite{}, err
	}
	if inv.StateAt(now) != InvitePending {
		return Invite{}, ErrInviteNotPending
	}
	return inv, nil
}

// AcceptInvite turns a pending invite into a new membership for the
// authenticated subject (M5). It never creates, changes or reactivates
// an existing live membership's role (M7): a live membership answers
// ErrAlreadyMember and nothing changes. A removed one does not block:
// the person gets a new row, so two tenures stay separate.
func (s *Service) AcceptInvite(ctx context.Context, token string, actor Actor) (Member, error) {
	inv, err := s.Store.InviteByHash(ctx, HashInviteToken(token))
	if err != nil {
		return Member{}, err
	}
	var out Member
	err = s.Store.Mutate(ctx, inv.TenantID, func(tx Tx) error {
		i, ok, err := tx.Invite(inv.ID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		now := tx.Now()
		if i.StateAt(now) != InvitePending {
			return ErrInviteNotPending
		}
		if _, live, err := tx.Member(actor.Subject); err != nil {
			return err
		} else if live {
			return ErrAlreadyMember
		}
		i.AcceptedAt, i.AcceptedBy = &now, actor.Subject
		if err := tx.UpdateInvite(i); err != nil {
			return err
		}
		out = Member{ID: newID("mem_"), TenantID: i.TenantID, Subject: actor.Subject, Role: i.Role,
			Status: StatusActive, InvitedBy: i.InvitedBy, CreatedAt: now, UpdatedAt: now, Version: 1}
		if err := tx.InsertMember(out); err != nil {
			return err
		}
		return audit(tx, actor, AuditInviteAccepted, actor.Subject, i.ID, string(i.Role))
	})
	return out, err
}

// ChangeRole sets target's role. No one changes their own role, except
// an admin stepping down while another active admin remains (M14). The
// last active admin keeps the role (M9), and with separation of duties
// on, so does the last active approver.
func (s *Service) ChangeRole(ctx context.Context, tenantID string, actor Actor, targetSubject string, role Role) (before, after Member, err error) {
	if _, ok := ParseRole(string(role)); !ok {
		return Member{}, Member{}, ErrInvalidRole
	}
	err = s.Store.Mutate(ctx, tenantID, func(tx Tx) error {
		m, ok, err := tx.Member(targetSubject)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		if m.Role == role {
			return ErrNoChange
		}
		if actor.Subject == targetSubject && (m.Role != RoleAdmin || role == RoleAdmin) {
			return ErrSelfRoleChange
		}
		if err := guardLoss(tx, m, role, true); err != nil {
			return err
		}
		before = m
		m.Role, m.UpdatedAt = role, tx.Now()
		if err := tx.UpdateMember(m); err != nil {
			return err
		}
		after = m
		return audit(tx, actor, AuditRoleChanged, targetSubject, string(before.Role), string(role))
	})
	return before, after, err
}

// Suspend takes target's authority away, reversibly.
func (s *Service) Suspend(ctx context.Context, tenantID string, actor Actor, targetSubject string) (Member, error) {
	return s.setStatus(ctx, tenantID, actor, targetSubject, StatusActive, StatusSuspended, AuditSuspended)
}

// Reactivate restores a suspended membership exactly as it was.
func (s *Service) Reactivate(ctx context.Context, tenantID string, actor Actor, targetSubject string) (Member, error) {
	return s.setStatus(ctx, tenantID, actor, targetSubject, StatusSuspended, StatusActive, AuditReactivated)
}

// Remove ends target's membership permanently. A removed person who
// comes back is invited again and gets a new membership.
func (s *Service) Remove(ctx context.Context, tenantID string, actor Actor, targetSubject string) (Member, error) {
	return s.setStatus(ctx, tenantID, actor, targetSubject, "", StatusRemoved, AuditRemoved)
}

func (s *Service) setStatus(ctx context.Context, tenantID string, actor Actor, targetSubject string, from, to Status, action string) (Member, error) {
	var out Member
	err := s.Store.Mutate(ctx, tenantID, func(tx Tx) error {
		m, ok, err := tx.Member(targetSubject)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		if from != "" && m.Status != from {
			return ErrInvalidTransition
		}
		if to != StatusActive {
			if err := guardLoss(tx, m, m.Role, false); err != nil {
				return err
			}
		}
		old := m.Status
		now := tx.Now()
		m.Status, m.UpdatedAt = to, now
		if to == StatusRemoved {
			m.RemovedAt = &now
		}
		if err := tx.UpdateMember(m); err != nil {
			return err
		}
		out = m
		return audit(tx, actor, action, targetSubject, string(old), string(to))
	})
	return out, err
}

// SetSeparationOfDuties turns the policy on or off. Turning it on needs
// an active approver to answer holds.
func (s *Service) SetSeparationOfDuties(ctx context.Context, tenantID string, actor Actor, on bool) (Governance, error) {
	var out Governance
	err := s.Store.Mutate(ctx, tenantID, func(tx Tx) error {
		g, err := tx.Governance()
		if err != nil {
			return err
		}
		if g.SeparationOfDuties == on {
			return ErrNoChange
		}
		if on {
			ms, err := tx.Members()
			if err != nil {
				return err
			}
			if _, approvers := counts(ms); approvers == 0 {
				return ErrNoApprover
			}
		}
		g.SeparationOfDuties, g.UpdatedBy, g.UpdatedAt = on, actor.Subject, tx.Now()
		if err := tx.SetGovernance(g); err != nil {
			return err
		}
		out = g
		action := AuditSoDDisabled
		if on {
			action = AuditSoDEnabled
		}
		return audit(tx, actor, action, "", strconv.FormatBool(!on), strconv.FormatBool(on))
	})
	return out, err
}

// RecordKeysRevoked writes the audit row for the credentials a removal
// revoked.
func (s *Service) RecordKeysRevoked(ctx context.Context, tenantID string, actor Actor, targetSubject string, keys, sessions int) error {
	return s.Store.Mutate(ctx, tenantID, func(tx Tx) error {
		return audit(tx, actor, AuditKeysRevoked, targetSubject, "",
			"keys="+strconv.Itoa(keys)+",developer_sessions="+strconv.Itoa(sessions))
	})
}

// Latest delegates to the store (auth.Memberships).
func (s *Service) Latest(ctx context.Context, tenantID, subject string) (Member, error) {
	return s.Store.Latest(ctx, tenantID, subject)
}

// HasRemoved delegates to the store (auth.Memberships).
func (s *Service) HasRemoved(ctx context.Context, subject string) (bool, error) {
	return s.Store.HasRemoved(ctx, subject)
}

// ForSubject delegates to the store (auth.Memberships).
func (s *Service) ForSubject(ctx context.Context, subject string) ([]Member, error) {
	return s.Store.ForSubject(ctx, subject)
}
