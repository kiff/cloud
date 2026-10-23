package mandate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// ApproverGrant is immutable except for withdrawal. A credential grants
// no management, execution or budget-extension capability.
type ApproverGrant struct {
	ID               string     `json:"id"`
	TenantID         string     `json:"tenant_id"`
	Name             string     `json:"name"`
	Principal        string     `json:"principal"`
	CardID           string     `json:"card_id"`
	Worker           string     `json:"worker"`
	Action           string     `json:"action"`
	Parameter        string     `json:"parameter"`
	Max              int64      `json:"max"`
	CreatedBy        string     `json:"created_by"`
	CreatedAt        time.Time  `json:"created_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	LastSeenAt       *time.Time `json:"last_seen_at,omitempty"`
	ConnectionStatus string     `json:"connection_status,omitempty"`
	KeyHash          string     `json:"-"`
}

func (g ApproverGrant) Active(now time.Time) bool {
	return g.RevokedAt == nil && now.Before(g.ExpiresAt)
}

// ExternalAnswer is saved in the same row/transaction as the answer.
// The model's judgment and reason are evidence, never an authority claim.
type ExternalAnswer struct {
	GrantID     string    `json:"grant_id"`
	Principal   string    `json:"principal"`
	Name        string    `json:"name,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Decision    string    `json:"decision"`
	ReasonCode  string    `json:"reason_code"`
	ReviewToken string    `json:"review_token"`
	Model       string    `json:"model,omitempty"`
	At          time.Time `json:"at"`
}

type ExternalReview struct {
	State  string
	Card   string
	Answer ExternalAnswer
}

var ErrApproverGrant = errors.New("external approver has no current authority for this request")
var ErrStaleReview = errors.New("review changed; read the held request again")

type ApproverStore interface {
	CreateApproverGrant(context.Context, ApproverGrant) error
	ListApproverGrants(context.Context, string, string) ([]ApproverGrant, error)
	TouchApprover(context.Context, string, string, time.Time) error
	ApproverGrant(context.Context, string, string) (ApproverGrant, error)
	ApproverByHash(context.Context, string) (ApproverGrant, error)
	RevokeApproverGrant(context.Context, string, string, time.Time) error
	AnswerExternal(context.Context, string, string, string, ExternalReview, time.Time) (Exception, error)
}

func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ReviewState excludes usage (which is rechecked on collection), and
// includes the exact held revision, request, breaches and original deadline.
func ReviewState(e Exception) string {
	return digest(struct {
		ID, Tenant, Proposal string
		Request              Request
		Fingerprint          string
		Breaches             []Breach
		Created              time.Time
		Expires              *time.Time
		Version              int
	}{e.ID, e.TenantID, e.ProposalID, e.Request, e.Fingerprint, e.Breaches, e.CreatedAt, e.ExpiresAt, e.AccountingVersion})
}
func ReviewCard(m Mandate) string { return digest(m) }

func (g ApproverGrant) ValidFor(m Mandate, now time.Time) bool {
	return validateApproverGrant(g, m, now) == nil
}

func validateApproverGrant(g ApproverGrant, m Mandate, now time.Time) error {
	if g.ID == "" || g.KeyHash == "" || g.CreatedBy == "" || g.Principal == "" || g.Principal == g.Worker ||
		g.Name == "" || len(g.Name) > 80 || g.Max <= 0 || !g.Active(now) || g.ExpiresAt.Sub(now) > 90*24*time.Hour ||
		g.TenantID != m.TenantID || g.CardID != m.ID || !m.Active(now) || !m.HeldBy(g.Worker) || !m.ComesHome() {
		return ErrApproverGrant
	}
	for _, grant := range m.Grants {
		if strings.EqualFold(grant.Action, g.Action) {
			for _, limit := range grant.PerAction {
				if limit.Quantity.Kind == KindSum && strings.EqualFold(limit.Quantity.Parameter, g.Parameter) {
					return nil
				}
			}
		}
	}
	return ErrApproverGrant
}

func ApproverCovers(g ApproverGrant, e Exception, m Mandate, now time.Time) bool {
	if validateApproverGrant(g, m, now) != nil {
		return false
	}
	if !g.Active(now) || !m.Active(now) || !m.ComesHome() || !m.HeldBy(g.Worker) || e.Subject != g.Worker ||
		g.Principal == e.Subject || !strings.EqualFold(g.Action, e.Action) {
		return false
	}
	var n int64
	var ok bool
	for name, value := range e.Request.Parameters {
		if strings.EqualFold(name, g.Parameter) {
			if ok {
				return false
			}
			n, ok = value, true
		}
	}
	if !ok || n < 0 || n > g.Max || len(e.Breaches) == 0 {
		return false
	}
	for _, b := range e.Breaches {
		if b.MandateID != g.CardID || b.Quantity.Kind != KindSum || !strings.EqualFold(b.Quantity.Parameter, g.Parameter) {
			return false
		}
	}
	return true
}

func applyExternalAnswer(g ApproverGrant, m Mandate, e *Exception, r ExternalReview, now time.Time) error {
	if !ApproverCovers(g, *e, m, now) {
		return ErrApproverGrant
	}
	a := r.Answer
	if a.GrantID != g.ID || a.Principal != g.Principal || a.ReviewToken == "" || len(a.ReasonCode) == 0 || len(a.ReasonCode) > 120 || len(a.Model) > 120 || len(a.Reason) > 1000 {
		return ErrApproverGrant
	}
	if e.ExternalAnswer != nil && e.ExternalAnswer.GrantID == g.ID && e.ExternalAnswer.ReviewToken == a.ReviewToken {
		old := *e.ExternalAnswer
		if old.Decision == a.Decision && old.ReasonCode == a.ReasonCode && old.Model == a.Model && old.Reason == a.Reason {
			return nil
		}
		return ErrExceptionState
	}
	if e.Expired(now) {
		return ErrExceptionExpired
	}
	if e.Status != ExceptionHeld {
		return ErrExceptionState
	}
	if ReviewState(*e) != r.State || ReviewCard(m) != r.Card {
		return ErrStaleReview
	}
	switch a.Decision {
	case "approve":
		if !e.CanApproveOnce() {
			return ErrCumulativeApproval
		}
		resolve(e, ExceptionApproved, g.Principal, now)
		e.AccountingVersion = CumulativeAccountingVersion
	case "reject":
		// A reason code the Card offers as a next step reaches the agent
		// as one; any other code stays evidence only.
		if slices.Contains(NextStepsFor(*e, []Mandate{m}), a.ReasonCode) {
			e.Refusal = &Refusal{NextStep: a.ReasonCode}
		}
		resolve(e, ExceptionRejected, g.Principal, now)
	case "defer": // Leaves human review and the original deadline intact.
		preserveApproval(e) // Retain an earlier defer when a new review replaces it.
	default:
		return ErrApproverGrant
	}
	a.Name = g.Name
	a.At = now.UTC()
	e.ExternalAnswer = &a
	return nil
}

// A redeemed decision remains historical. Unconsumed approvals must still
// have a live grant; stores check this under the same Card lock as redemption.
type externalGrantReader interface {
	ApproverGrant(context.Context, string, string) (ApproverGrant, error)
}

func externalApprovalActive(ctx context.Context, reader any, e Exception, now time.Time) (bool, error) {
	if e.ExternalAnswer == nil || e.ExternalAnswer.Decision != "approve" {
		return true, nil
	}
	s, ok := reader.(externalGrantReader)
	if !ok {
		return false, ErrApproverGrant
	}
	g, err := s.ApproverGrant(ctx, e.TenantID, e.ExternalAnswer.GrantID)
	if errors.Is(err, ErrApproverGrant) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return g.Active(now), nil
}

type externalPolicyLocker interface {
	LockApproverPolicy(context.Context, string) error
}

// Effectful PostgreSQL paths acquire this after Card locks and hold it
// through commit. Read projections deliberately use only the reader.
func lockExternalApprovalPolicy(ctx context.Context, reader any, e Exception) error {
	if e.Status != ExceptionApproved || e.ExternalAnswer == nil || e.ExternalAnswer.Decision != "approve" {
		return nil
	}
	if locker, ok := reader.(externalPolicyLocker); ok {
		return locker.LockApproverPolicy(ctx, e.TenantID)
	}
	return nil
}

func refreshExternalApproval(ctx context.Context, reader any, e *Exception, now time.Time) (bool, error) {
	if e.Status != ExceptionApproved || e.ExternalAnswer == nil {
		return false, nil
	}
	active, err := externalApprovalActive(ctx, reader, *e, now)
	if err != nil || active {
		return false, err
	}
	withdrawExternalApproval(e)
	// Approved rows do not expire. Once approval is withdrawn, the
	// original unanswered-hold deadline applies again.
	if e.Expired(now) {
		expire(e)
	}
	return true, nil
}

func withdrawExternalApproval(e *Exception) {
	preserveApproval(e)
	e.Status, e.ResolvedBy, e.ResolvedAt = ExceptionHeld, "", nil
	e.ExternalAnswer = nil
	e.Reason = "external approval withdrawn; fresh review required"
}
