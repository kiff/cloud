// Package handoff keeps "Your step": a step a taf may never take itself
// (approve a bank transfer, sign a contract, submit a form only a person
// may), which the taf prepares and hands to its owner to finish.
//
// It works as a held call does, with the owner's answer "Done" or "Skip
// it" in place of "Approve" or "Refuse":
//   - the taf raises it with its own key, naming the calls that prepared
//     it (prepared_by, by the runtime's operation ids), each one its own and
//     each one sent: a hand-off never makes a refused call allowed, and
//     grants nothing. KIFF keeps the calls' proposal ids;
//   - only a person answers it, on a phone with its device key's
//     signature over HandoffAnswer's message, as for a hold;
//   - no answer within Wait closes it as no_answer, never as done;
//   - after "Done", the taf may name the call that read the vendor to see
//     it was done (Check, by its operation id): KIFF records that a read was made and how it
//     ended at the tool, never that the step is proven from the AI's word.
//
// Each hand-off gets a receipt when it's raised, and a settled receipt when
// it closes. The words (what, why, about) stay in this table, outside what
// a receipt hashes, so they can be erased and every receipt still verifies.
// The link the owner opens is kept as its host only.
package handoff

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed sql/handoffs.sql
var schemaSQL string

// Status is where a hand-off stands.
type Status string

const (
	// Waiting: raised, and the owner hasn't answered.
	Waiting Status = "waiting"
	// Done: the owner says they did it.
	Done Status = "done"
	// Skipped: the owner chose not to.
	Skipped Status = "skipped"
	// NoAnswer: nobody answered within Wait. Never read as done.
	NoAnswer Status = "no_answer"
)

// Answers are the two answers a person gives, as the API takes them.
var Answers = []Status{Done, Skipped}

// Limits.
const (
	MaxWhat  = 300
	MaxWhy   = 300
	MaxAbout = 200
	// MaxPreparedBy bounds the calls one hand-off names.
	MaxPreparedBy = 20
	// MaxWaitingPerAgent bounds an agent's hand-offs waiting at once, so a
	// taf in a loop can't fill its owner's list.
	MaxWaitingPerAgent = 20
	// MaxOperation bounds the operation id a run sends.
	MaxOperation = 128
)

// Wait is how long a hand-off waits for its owner: a week, as a card does.
const Wait = 7 * 24 * time.Hour

// CheckGrace is how long a "Done" waits for the taf's read of the vendor
// before its settled receipt is made without one.
const CheckGrace = time.Hour

// Version is the hand-off's version as the phone signs it: a hand-off never
// changes once raised, so it is 1 until its shape does.
const Version = 1

// Errors.
var (
	ErrNotFound   = errors.New("handoff: no such hand-off")
	ErrAnswered   = errors.New("handoff: already answered or closed")
	ErrTooMany    = errors.New("handoff: too many hand-offs waiting for this agent")
	ErrNotChecked = errors.New("handoff: only a hand-off answered done, not yet settled or checked, takes a check")
)

// Device is the phone's signed proof of an answer.
type Device struct {
	Name      string `json:"name,omitempty"`
	KeyID     string `json:"key_id"`
	PublicKey []byte `json:"public_key"`
	Message   string `json:"signed_message"`
	Signature string `json:"signature"`
}

// Handoff is one step handed to the owner.
type Handoff struct {
	TenantID string `json:"-"`
	ID       string `json:"id"`
	Agent    string `json:"agent"`
	// Operation is the run's operation id for the call that raised it:
	// asking again with it answers the same hand-off, after a crash too.
	Operation  string    `json:"-"`
	What       string    `json:"what"`
	Why        string    `json:"why"`
	LinkHost   string    `json:"link_host,omitempty"`
	About      string    `json:"about,omitempty"`
	PreparedBy []string  `json:"prepared_by"`
	Version    int       `json:"version"`
	Status     Status    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	AnsweredBy string    `json:"-"`
	// AnsweredByID is the sign-in id of who answered, when known.
	AnsweredByID string     `json:"-"`
	AnsweredAt   *time.Time `json:"answered_at,omitempty"`
	Device       *Device    `json:"-"`
	// CheckedBy is the call that read the vendor after "Done".
	CheckedBy string     `json:"checked_by,omitempty"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	// RaisedReceipt is set once the raised receipt is saved, SettledAt once
	// the settled one is.
	RaisedReceipt bool       `json:"-"`
	SettledAt     *time.Time `json:"-"`
	// AlreadyWaiting is set on what Raise returns when the agent asked
	// again for a step already waiting (the same what and about) under
	// another operation: a later run that found the same work. Not kept.
	AlreadyWaiting bool `json:"-"`
}

// Open reports whether it still waits for the owner as of now.
func (h Handoff) Open(now time.Time) bool { return h.Status == Waiting && now.Before(h.ExpiresAt) }

// WordsHash is the hash a receipt carries in place of the words: sha256
// over what, a newline, why, a newline and about.
func (h Handoff) WordsHash() string {
	sum := sha256.Sum256([]byte(h.What + "\n" + h.Why + "\n" + h.About))
	return hex.EncodeToString(sum[:])
}

// Closed reports whether the hand-off is closed and owes its settled
// receipt as of now: skipped or unanswered at once, done once its check
// came or CheckGrace has passed.
func (h Handoff) Closed(now time.Time) bool {
	switch h.Status {
	case Skipped, NoAnswer:
		return true
	case Done:
		return h.CheckedAt != nil || (h.AnsweredAt != nil && !now.Before(h.AnsweredAt.Add(CheckGrace)))
	}
	return false
}

// Request is what a taf sends to raise one.
type Request struct {
	What string `json:"what"`
	Why  string `json:"why"`
	Link string `json:"link,omitempty"`
	// About is the case, in the taf's words, as its calls' kiff_about.
	About string `json:"about,omitempty"`
	// PreparedBy are the operation ids (kiff_operation_id) of the calls
	// that prepared the step; KIFF resolves each to the gateway's call.
	PreparedBy []string `json:"prepared_by,omitempty"`
	Operation  string   `json:"operation"`
}

// Validate checks a request and returns the hand-off it raises for agent,
// with the link cut to its host. The caller checks prepared_by against the
// gateway's record (CheckPrepared).
func (r Request) Validate(tenantID, agent string, now time.Time) (Handoff, error) {
	what, why, about := strings.TrimSpace(r.What), strings.TrimSpace(r.Why), strings.TrimSpace(r.About)
	switch {
	case what == "" || utf8.RuneCountInString(what) > MaxWhat:
		return Handoff{}, fmt.Errorf("what is required, at most %d characters", MaxWhat)
	case why == "" || utf8.RuneCountInString(why) > MaxWhy:
		return Handoff{}, fmt.Errorf("why is required, at most %d characters", MaxWhy)
	case utf8.RuneCountInString(about) > MaxAbout:
		return Handoff{}, fmt.Errorf("about is at most %d characters", MaxAbout)
	case r.Operation == "" || len(r.Operation) > MaxOperation:
		return Handoff{}, fmt.Errorf("operation is required, at most %d characters", MaxOperation)
	case len(r.PreparedBy) > MaxPreparedBy:
		return Handoff{}, fmt.Errorf("prepared_by names at most %d calls", MaxPreparedBy)
	}
	for _, s := range []string{what, why, about} {
		if strings.ContainsFunc(s, func(c rune) bool { return c < 0x20 && c != '\n' }) {
			return Handoff{}, errors.New("what, why and about are plain text")
		}
	}
	host := ""
	if strings.TrimSpace(r.Link) != "" {
		var err error
		if host, err = LinkHost(r.Link); err != nil {
			return Handoff{}, err
		}
	}
	seen := map[string]bool{}
	prepared := []string{}
	for _, p := range r.PreparedBy {
		p = strings.TrimSpace(p)
		if p == "" || len(p) > 128 {
			return Handoff{}, errors.New("prepared_by holds call ids")
		}
		if !seen[p] {
			seen[p] = true
			prepared = append(prepared, p)
		}
	}
	return Handoff{TenantID: tenantID, Agent: agent, Operation: r.Operation, What: what, Why: why, LinkHost: host,
		About: about, PreparedBy: prepared, Version: Version, Status: Waiting,
		CreatedAt: now.UTC(), ExpiresAt: now.Add(Wait).UTC()}, nil
}

// LinkHost is a link's host, the only part KIFF keeps: an https URL's
// hostname, lower case, without its port, user, path, query or fragment.
func LinkHost(link string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return "", errors.New("link must be an https URL")
	}
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return "", errors.New("link must name a vendor's host, not an address")
	}
	return host, nil
}

// NewID returns a fresh hand-off id.
func NewID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "ho_" + hex.EncodeToString(b)
}

// Call is what the gateway recorded on one call, as a hand-off checks it.
type Call struct {
	// Proposal is the call's id in KIFF (its decision's, and the gateway's
	// key); Operation the runtime's kiff_operation_id for it.
	Proposal  string
	Operation string
	Agent     string
	// State is the gateway's: forwarding and forwarded calls were sent.
	State string
	// ToolOutcome is "done" or "error" when the tool's answer was recorded.
	ToolOutcome string
	CreatedAt   time.Time
}

// Sent reports whether the gateway sent the call: allowed, by the Card or
// by the owner's approval.
func (c Call) Sent() bool { return c.State == "forwarding" || c.State == "forwarded" }

// Owns reports whether agent made the call, as itself or in its trial.
func Owns(callAgent, agent string, trialSubject func(string) string) bool {
	return agent != "" && (callAgent == agent || (trialSubject != nil && callAgent == trialSubject(agent)))
}

// CheckPrepared checks that each of the calls a hand-off names, by
// operation id, is agent's own and was sent, and returns their proposal
// ids, in order. It names the first that isn't.
func CheckPrepared(calls map[string]Call, prepared []string, agent string, trialSubject func(string) string) ([]string, error) {
	out := make([]string, 0, len(prepared))
	for _, op := range prepared {
		c, ok := calls[op]
		if !ok || !Owns(c.Agent, agent, trialSubject) {
			return nil, fmt.Errorf("prepared_by: %s is not one of this agent's calls", op)
		}
		if !c.Sent() {
			return nil, fmt.Errorf("prepared_by: %s was not sent (a hand-off never stands for a refused or held call)", op)
		}
		out = append(out, c.Proposal)
	}
	return out, nil
}

// Store keeps hand-offs.
type Store interface {
	// Raise keeps h, or returns the hand-off its agent already raised with
	// the same operation (created false), or one of its agent's still
	// waiting for the same what and the same case, about, which must be
	// set (created false, AlreadyWaiting): a later run that found the same
	// work hands nothing over twice. Without a case, the same words may be
	// a new batch, so they raise a new step.
	// ErrTooMany when the agent has MaxWaitingPerAgent open.
	Raise(ctx context.Context, h Handoff) (Handoff, bool, error)
	Get(ctx context.Context, tenantID, id string) (Handoff, error)
	// Open lists the account's hand-offs that wait as of now, oldest first.
	Open(ctx context.Context, tenantID string, now time.Time, limit int) ([]Handoff, error)
	// Answer records a person's answer while it waits: ErrAnswered (with
	// the hand-off as it stands) once answered, closed or past its expiry.
	Answer(ctx context.Context, tenantID, id string, answer Status, by, byID string, device *Device, at time.Time) (Handoff, error)
	// Check records the call that read the vendor after "Done".
	Check(ctx context.Context, tenantID, id, proposal string, at time.Time) (Handoff, error)
	// Expire closes, across accounts, what waited past its expiry as of
	// now, and returns them.
	Expire(ctx context.Context, now time.Time, limit int) ([]Handoff, error)
	// Unreceipted lists, across accounts, hand-offs that owe a receipt as
	// of now: the raised one, or the settled one once Closed.
	Unreceipted(ctx context.Context, now time.Time, limit int) ([]Handoff, error)
	// Receipted marks the raised receipt saved, and the settled one when
	// settled is set.
	Receipted(ctx context.Context, tenantID, id string, settled *time.Time) error
	// Calls reads the gateway's record of the account's calls by proposal id.
	Calls(ctx context.Context, tenantID string, ids []string) (map[string]Call, error)
	// Operations reads the gateway's record of the calls agents made with
	// these operation ids, keyed by operation id.
	Operations(ctx context.Context, tenantID string, agents, ops []string) (map[string]Call, error)
}
