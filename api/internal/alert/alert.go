// Package alert delivers outbound notifications when KIFF returns a
// consequential decision. It is the notification half of the
// approval loop: when decide answers approval_required or blocks a
// money-moving action, a human should be told without polling the
// dashboard.
//
// Three pieces:
//
//   - Target: a per-tenant (optionally per-action) webhook the tenant
//     registers, with the set of outcomes it wants delivered.
//   - Store: persistence for targets (memory + Postgres).
//   - Dispatcher: a Notifier that, given a decision Event, fans out
//     best-effort to the matching targets on a detached context — it
//     never blocks or alters the decide response path.
//
// Privacy: an Event deliberately carries only the decision essentials
// (control, action, entity, outcome, reason, approval handle + review
// URL). It never carries the request parameters, so the
// redaction policy holds by construction — there is nothing sensitive
// in the payload to redact.
package alert

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"slices"
	"strconv"
	"sync"
	"time"
)

// NewSigningSecret returns a fresh signing secret for a target.
func NewSigningSecret() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return "whsec_" + base64.RawURLEncoding.EncodeToString(b[:])
}

// SignatureHeader is the KIFF-Signature value for body sent at t:
// "t=<unix seconds>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>". A
// receiver recomputes it with its secret, compares in constant time, and
// refuses a t too far from its own clock.
func SignatureHeader(secret string, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// Approval is the actionable handle echoed into an alert when the
// outcome is approval_required, so the human can go straight to the
// review surface.
type Approval struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	ReviewURL string `json:"review_url,omitempty"`
}

// Hold is the held call an alert is about: its id, where it is
// answered, and when it stops waiting. A receiver, such as a phone app's
// backend, reads anything more through the API with its own credential.
type Hold struct {
	ID        string     `json:"id"`
	Status    string     `json:"status"`
	ReviewURL string     `json:"review_url,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// MoneyOut says whether the held call moves money when approved: true
	// when its operation's owner-approved profile says moves_money, false
	// when the profile says it does anything else. Absent when KIFF can't
	// tell (no profile, or no operation by that name), so a receiver that
	// lets a call wait only on false never makes a money call wait on a
	// guess. A fact about the operation, never the call's parameters.
	MoneyOut *bool `json:"money_out,omitempty"`
	// CanWait says whether the held call may wait to be read with others,
	// rather than alerting at once: true when its operation's profile says
	// it's a read or it drafts, writes or records money; false when it
	// moves money, sends something outside the company (an email, a message
	// to a customer) or does anything else. Absent when KIFF can't tell, as
	// MoneyOut, so a receiver lets a call wait only on true.
	CanWait *bool `json:"can_wait,omitempty"`
}

// HoldAnswered is the outcome of the event sent when a held call is
// answered. A target receives it only when it lists it in its outcomes:
// targets made before it existed keep the payloads they had.
const HoldAnswered = "hold_answered"

// PackRequested and PackRequestAnswered are sent when a member asks the
// account's admins for a pack of tafs tasks, and when the request is
// declined or withdrawn. The event names the request (EntityID) and the
// pack (Reasons); a receiver reads the rest through the API. Like
// HoldAnswered, a target receives them only when it lists them.
const (
	PackRequested       = "pack_requested"
	PackRequestAnswered = "pack_request_answered"
)

// CreditsWarning is sent to the account's alert targets when its tafs
// have used 80% of what the period has, and again when they've used all
// of it and wait, once each a period: the share (Reasons[0], "80" or
// "100") and why they wait (Reasons[1], at 100). A receiver tells the
// people who manage billing, and reads the rest through the API. Like
// HoldAnswered, a target receives it only when it lists it.
const CreditsWarning = "credits_warning"

// ConnectionSignedOut is sent when a vendor ends the sign-in planque holds
// for a taf's connection (its refresh refused, or a token expired with no
// refresh): the owner signs in again in tafs. Like HoldAnswered, a target
// receives it only when it lists it.
const ConnectionSignedOut = "connection_signed_out"

// RefundNotice is sent when a refund KIFF sent failed or was cancelled at
// the processor (its Card's room came back), when a charge KIFF refunded
// was also refunded outside KIFF, or when a refund the owner said didn't
// happen is at the processor after all (its room was taken again; thune
// step one): the kind (Reasons[0], "refund_failed", "refunded_twice" or
// "refund_after_no"), the agent that made the call
// (Reasons[1]) and the call (EntityID). A receiver reads the rest through
// the API. Like HoldAnswered, a target receives it only when it lists it.
const RefundNotice = "refund_notice"

// HandoffAnswered is sent when a step a taf handed to its owner ("Your
// step") closes: answered done or skipped, or expired with no answer. The
// event names the agent (EntityID) and carries the hand-off (Handoff), so
// the agent's runtime continues the run that waits on it. Like
// HoldAnswered, a target receives it only when it lists it.
const HandoffAnswered = "handoff_answered"

// Handoff is the hand-off an alert is about: its id, the agent that raised
// it, and how it stands (done, skipped or no_answer). No words: a receiver
// reads the rest through the API with its own credential.
type Handoff struct {
	ID     string `json:"id"`
	Agent  string `json:"agent"`
	Status string `json:"status"`
}

// PhoneOutcomesAdded are the outcomes added since phones first registered
// their targets: at start, KIFF gives them to targets an earlier app
// registered at an app backend, since a phone registers its target once.
var PhoneOutcomesAdded = []string{PackRequested, PackRequestAnswered, ConnectionSignedOut, CreditsWarning, RefundNotice, HandoffAnswered}

// optIn are the outcomes a target receives only when it lists them.
var optIn = []string{HoldAnswered, PackRequested, PackRequestAnswered, ConnectionSignedOut, CreditsWarning, RefundNotice, HandoffAnswered}

// Connection is the connection an alert is about, on connection_signed_out:
// planque's grant, the agent it serves, the vendor by its name for people,
// why (for the receiver's records, never for people), and when. No tokens,
// accounts or tools.
type Connection struct {
	Grant       string    `json:"grant"`
	Agent       string    `json:"agent"`
	Vendor      string    `json:"vendor"`
	Reason      string    `json:"reason"`
	SignedOutAt time.Time `json:"signed_out_at"`
}

// Event is the decision essentials an alert carries. No request
// parameters by design (see package doc).
type Event struct {
	TenantID   string
	ProposalID string
	Domain     string
	Action     string
	EntityID   string
	EntityType string
	Outcome    string
	Reasons    []string
	Approval   *Approval
	// Hold is the held call, on owner_required and hold_answered.
	Hold *Hold
	// Notify names the people to tell about a held call, by sign-in
	// subject, when KIFF knows who asked for it: those who may answer it,
	// never the person who asked. Nil leaves it to the receiver, as
	// before; an empty list tells no one.
	Notify *[]string
	// Request is a member's request for more tasks, on pack_requested and
	// pack_request_answered: who asked, so the answer reaches them.
	Request *PackRequest
	// Connection is the connection a vendor signed out, on
	// connection_signed_out.
	Connection *Connection
	// Handoff is the hand-off that closed, on handoff_answered.
	Handoff   *Handoff
	DecidedAt time.Time
	// EventID identifies the event across targets and retries, so a
	// receiver can drop a duplicate. Set by the dispatcher when empty.
	EventID string
}

// PackRequest is a member's request for a pack of tasks, as an alert
// carries it: the request, its pack and status, the member who asked (their
// sign-in subject, so an app tells them alone), and an admin's note when one
// declined it.
type PackRequest struct {
	ID string `json:"id"`
	// Kind is what it asks for: a pack ("pack"), or a raise of the spend
	// limit ("raise"), which names no pack.
	Kind    string `json:"kind,omitempty"`
	Pack    string `json:"pack"`
	Status  string `json:"status"`
	AskedBy string `json:"asked_by"`
	Note    string `json:"note,omitempty"`
}

// Target is a registered outbound alert destination.
//
// Action scoping: empty Action means "all actions for this tenant";
// a set Action narrows the target to one control. Outcomes scoping:
// empty Outcomes means "all consequential outcomes"; a set list
// narrows to those outcomes (e.g. just "approval_required").
type Target struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Action    string    `json:"action,omitempty"`
	URL       string    `json:"url"`
	Outcomes  []string  `json:"outcomes,omitempty"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	// Secret signs every delivery to this target (KIFF-Signature).
	// It is shown once, when the target is made or its secret rotated,
	// and never listed. Empty for a target made before signing, which
	// is sent unsigned until its secret is rotated.
	Secret string `json:"-"`
	// DisabledReason and DisabledAt say why and when KIFF switched the
	// target off: its deliveries kept failing for about a day.
	// Empty while it is on, or when its owner switched it off.
	DisabledReason string     `json:"disabled_reason,omitempty"`
	DisabledAt     *time.Time `json:"disabled_at,omitempty"`
}

// Matches reports whether this target should receive an alert for the
// given action + outcome. A disabled target never matches.
func (t Target) Matches(action, outcome string) bool {
	if !t.Enabled {
		return false
	}
	if t.Action != "" && t.Action != action {
		return false
	}
	if slices.Contains(optIn, outcome) {
		return slices.Contains(t.Outcomes, outcome)
	}
	if len(t.Outcomes) == 0 {
		return true
	}
	for _, o := range t.Outcomes {
		if o == outcome {
			return true
		}
	}
	return false
}

// Store persists alert targets, tenant-scoped at the call boundary.
type Store interface {
	ListTargets(ctx context.Context, tenantID string) ([]Target, error)
	CreateTarget(ctx context.Context, t Target) error
	// ReplaceTarget creates t and removes the account's other targets at
	// t.URL in one step: of concurrent replacements at one URL,
	// exactly one target is left, the last to finish.
	ReplaceTarget(ctx context.Context, t Target) error
	DeleteTarget(ctx context.Context, tenantID, id string) error
	// RotateSecret replaces a target's signing secret; false when the
	// account has no such target.
	RotateSecret(ctx context.Context, tenantID, id, secret string) (bool, error)
	// SetEnabled switches a target on or off; off with a reason when KIFF
	// switched it off. False when the account has no such target.
	SetEnabled(ctx context.Context, tenantID, id string, enabled bool, reason string, at time.Time) (bool, error)
}

// Notifier is the contract the proposal gate depends on. Notify must
// return immediately and must never block the caller — the
// Dispatcher implementation schedules delivery on its own context.
type Notifier interface {
	Notify(ev Event)
}

// MemoryStore is the in-process Store for tests and no-Postgres runs.
type MemoryStore struct {
	mu      sync.RWMutex
	targets map[string][]Target // tenantID -> targets
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{targets: map[string][]Target{}}
}

// ListTargets implements Store.
func (m *MemoryStore) ListTargets(_ context.Context, tenantID string) ([]Target, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Target, 0, len(m.targets[tenantID]))
	out = append(out, m.targets[tenantID]...)
	return out, nil
}

// CreateTarget implements Store.
func (m *MemoryStore) CreateTarget(_ context.Context, t Target) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.targets[t.TenantID] = append(m.targets[t.TenantID], t)
	return nil
}

// ReplaceTarget implements Store.
func (m *MemoryStore) ReplaceTarget(_ context.Context, t Target) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.targets[t.TenantID] = append(slices.DeleteFunc(m.targets[t.TenantID], func(o Target) bool { return o.URL == t.URL }), t)
	return nil
}

// SetEnabled implements Store.
func (m *MemoryStore) SetEnabled(_ context.Context, tenantID, id string, enabled bool, reason string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, t := range m.targets[tenantID] {
		if t.ID != id {
			continue
		}
		t.Enabled, t.DisabledReason, t.DisabledAt = enabled, "", nil
		if !enabled && reason != "" {
			when := at
			t.DisabledReason, t.DisabledAt = reason, &when
		}
		m.targets[tenantID][i] = t
		return true, nil
	}
	return false, nil
}

// RotateSecret implements Store.
func (m *MemoryStore) RotateSecret(_ context.Context, tenantID, id, secret string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, t := range m.targets[tenantID] {
		if t.ID == id {
			m.targets[tenantID][i].Secret = secret
			return true, nil
		}
	}
	return false, nil
}

// DeleteTarget implements Store.
func (m *MemoryStore) DeleteTarget(_ context.Context, tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.targets[tenantID]
	out := cur[:0:0]
	for _, t := range cur {
		if t.ID != id {
			out = append(out, t)
		}
	}
	m.targets[tenantID] = out
	return nil
}

// Refunds hands the refund watch's notices (tooloutcome.RefundWatch) to
// Notifier as RefundNotice events: the kind and the agent in Reasons, the
// call as the entity.
type Refunds struct{ Notifier Notifier }

// NotifyRefund sends one refund notice.
func (r Refunds) NotifyRefund(tenant, call, tool, agent, kind string, at time.Time) {
	r.Notifier.Notify(Event{TenantID: tenant, ProposalID: call, Domain: "gateway", Action: tool,
		EntityID: call, EntityType: "ToolCall", Outcome: RefundNotice, Reasons: []string{kind, agent}, DecidedAt: at})
}
