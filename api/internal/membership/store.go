package membership

import (
	"context"
	"time"
)

// Reader is what request resolution and the hold gate read. Every read
// goes to the store: there is no membership cache in v1 (M12).
type Reader interface {
	// Live returns the active or suspended membership for (tenant,
	// subject), or ErrNotFound.
	Live(ctx context.Context, tenantID, subject string) (Member, error)
	// Latest returns the live membership, else the most recently
	// removed one, or ErrNotFound when the subject never belonged.
	// Key checks use it to tell a removed minter from an operator.
	Latest(ctx context.Context, tenantID, subject string) (Member, error)
	// ForSubject returns every live membership a subject holds, across
	// tenants.
	ForSubject(ctx context.Context, subject string) ([]Member, error)
	// HasRemoved reports whether the subject has a removed membership in
	// any tenant: someone who joined through an invitation and was
	// removed, who must not be handed a fresh account on next sign-in.
	HasRemoved(ctx context.Context, subject string) (bool, error)
	// List returns a tenant's live memberships.
	List(ctx context.Context, tenantID string) ([]Member, error)
	// Governance returns the tenant's policy; separation of duties is
	// off when no row exists.
	Governance(ctx context.Context, tenantID string) (Governance, error)
	// Invites returns a tenant's invites, newest first.
	Invites(ctx context.Context, tenantID string) ([]Invite, error)
	// InviteByHash finds an invite by its token hash, or ErrNotFound.
	InviteByHash(ctx context.Context, hash [32]byte) (Invite, error)
	// Audit returns a tenant's membership trail, newest first.
	Audit(ctx context.Context, tenantID string, limit int) ([]AuditEvent, error)
	// HoldAnswers returns the recorded answerer of each named hold.
	HoldAnswers(ctx context.Context, tenantID string, holdIDs []string) (map[string]HoldAnswer, error)
}

// Store adds the transactional write path.
type Store interface {
	Reader
	// Mutate runs fn in one transaction that holds the tenant's
	// membership lock, so invariant checks (last admin, last approver)
	// and the change they guard cannot interleave with another change
	// to the same tenant. fn's writes, including its audit rows, commit
	// together or not at all (M8).
	Mutate(ctx context.Context, tenantID string, fn func(Tx) error) error
	// RecordHoldAnswer writes who answered a hold.
	RecordHoldAnswer(ctx context.Context, a HoldAnswer) error
}

// Tx is the view of one tenant inside Mutate.
type Tx interface {
	Members() ([]Member, error)
	Member(subject string) (Member, bool, error)
	Governance() (Governance, error)
	Invite(id string) (Invite, bool, error)
	InsertMember(m Member) error
	UpdateMember(m Member) error
	InsertInvite(i Invite) error
	UpdateInvite(i Invite) error
	SetGovernance(g Governance) error
	Audit(e AuditEvent) error
	Now() time.Time
}
