// Package owneremail resolves a tenant's owner email at the moment it
// is needed, from the systems of record — never persisting it. The
// mapping is: tenant id → owner subject
// (cloud_meta.subject_tenants) → email (Clerk Backend API). Clerk
// remains the single source of truth for customer email; KIFF does not
// duplicate it.
//
// The resolver is reusable: the operator console detail view and the
// suspend/unsuspend notifier both go through it.
package owneremail

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kiff/cloud/api/internal/membership"
)

// SubjectLookup maps a tenant id to its owner subject. Satisfied by
// tenant.PostgresSubjectStore / MemorySubjectStore (SubjectForTenant).
type SubjectLookup interface {
	SubjectForTenant(ctx context.Context, tenantID string) (string, error)
}

// EmailLookup maps an auth subject to an email. The production
// implementation is Clerk-backed (see clerk.go); tests inject a fake.
type EmailLookup interface {
	EmailForSubject(ctx context.Context, subject string) (string, error)
}

// ErrNoEmail is returned when no email can be resolved for a tenant —
// no subject mapping, Clerk lookup failed, or the user has no email.
// Callers treat this as "skip the email cleanly," never as fatal.
var ErrNoEmail = errors.New("owneremail: no email resolvable for tenant")

// Resolver composes a SubjectLookup and an EmailLookup.
type Resolver struct {
	subjects SubjectLookup
	emails   EmailLookup
	members  Members
}

// NewResolver wires the two lookups. Both are required.
func NewResolver(subjects SubjectLookup, emails EmailLookup) *Resolver {
	return &Resolver{subjects: subjects, emails: emails}
}

// Email resolves the owner email for tenantID. Returns ErrNoEmail
// (wrapped with cause) when the email cannot be resolved; the cause is
// preserved for logging but callers should branch on ErrNoEmail.
func (r *Resolver) Email(ctx context.Context, tenantID string) (string, error) {
	if r == nil || r.subjects == nil || r.emails == nil {
		return "", ErrNoEmail
	}
	subject, err := r.subjects.SubjectForTenant(ctx, tenantID)
	if err != nil {
		return "", fmt.Errorf("%w: subject lookup: %w", ErrNoEmail, err)
	}
	email, err := r.emails.EmailForSubject(ctx, subject)
	if err != nil {
		return "", fmt.Errorf("%w: email lookup: %w", ErrNoEmail, err)
	}
	if strings.TrimSpace(email) == "" {
		return "", ErrNoEmail
	}
	return strings.TrimSpace(email), nil
}

// Members is the membership read HoldRecipients needs.
type Members interface {
	List(ctx context.Context, tenantID string) ([]membership.Member, error)
	Governance(ctx context.Context, tenantID string) (membership.Governance, error)
}

// WithMembers makes held-action email go to the people who may answer:
// every active approver, and admins and
// editors only while separation of duties is off.
func (r *Resolver) WithMembers(m Members) *Resolver {
	if r != nil {
		r.members = m
	}
	return r
}

// HoldRecipients returns the addresses a held action is emailed to.
// Without memberships it is the owner's address (Email). A member whose
// address cannot be resolved is skipped; ErrNoEmail when nobody is left.
// Recipient is someone a held call's email goes to: their address, and
// their subject, so the email can be in their language ("" when the
// account's one address is no member's).
type Recipient struct{ Email, Subject string }

// HoldRecipientsWithSubjects is HoldRecipients with each one's subject.
func (r *Resolver) HoldRecipientsWithSubjects(ctx context.Context, tenantID string) ([]Recipient, error) {
	emails, subjects, err := r.holdRecipients(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]Recipient, len(emails))
	for i := range emails {
		out[i] = Recipient{Email: emails[i], Subject: subjects[i]}
	}
	return out, nil
}

func (r *Resolver) HoldRecipients(ctx context.Context, tenantID string) ([]string, error) {
	emails, _, err := r.holdRecipients(ctx, tenantID)
	return emails, err
}

// owner is the account's one address and its owner's subject.
func (r *Resolver) owner(ctx context.Context, tenantID string) ([]string, []string, error) {
	e, err := r.Email(ctx, tenantID)
	if err != nil {
		return nil, nil, err
	}
	subject := ""
	if r.subjects != nil {
		subject, _ = r.subjects.SubjectForTenant(ctx, tenantID)
	}
	return []string{e}, []string{subject}, nil
}

func (r *Resolver) holdRecipients(ctx context.Context, tenantID string) ([]string, []string, error) {
	if r == nil || r.members == nil {
		return r.owner(ctx, tenantID)
	}
	if r.emails == nil {
		return nil, nil, ErrNoEmail
	}
	ms, err := r.members.List(ctx, tenantID)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: members: %w", ErrNoEmail, err)
	}
	gov, err := r.members.Governance(ctx, tenantID)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: governance: %w", ErrNoEmail, err)
	}
	if len(ms) == 0 {
		return r.owner(ctx, tenantID)
	}
	var out, subjects []string
	seen := map[string]bool{}
	for _, m := range ms {
		if !m.Active() || !membership.AnswersHolds(m.Role, gov.SeparationOfDuties) {
			continue
		}
		e, err := r.emails.EmailForSubject(ctx, m.Subject)
		e = strings.TrimSpace(e)
		if err != nil || e == "" || seen[strings.ToLower(e)] {
			continue
		}
		seen[strings.ToLower(e)] = true
		out = append(out, e)
		subjects = append(subjects, m.Subject)
	}
	if len(out) == 0 {
		return nil, nil, ErrNoEmail
	}
	return out, subjects, nil
}
