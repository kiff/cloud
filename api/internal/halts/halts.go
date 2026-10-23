// Package halts is the owner's halt (gate B): one
// agent, or the whole account, stopped until it is resumed. While an agent
// or its account is halted, KIFF decides none of its calls: new ones, held
// ones and retries are refused (halted), so no new call reaches a vendor.
// Once resumed, what was asked before the halt stays refused: only new calls
// go through.
//
// KIFF holds the halt the moment it is stored. planque, which sends relay
// calls with permits KIFF issued before the halt, reads it from KIFF at
// every dispatch and fails closed; it confirms it can see each halt
// (PlanqueAck), and the halt reads as halted, not halting, only once it has.
package halts

import (
	"context"
	"errors"
	"time"
)

// Account names the whole account in place of an agent: no agent's id is
// empty.
const Account = ""

// Halt is one agent's halt, or the account's (AgentID Account).
type Halt struct {
	TenantID string
	AgentID  string
	// On: halted now. HaltedAt is when the latest halt began, kept after a
	// resume: what was asked before it never goes through.
	On       bool
	HaltedAt time.Time
	HaltedBy string
	// ResumedAt and ResumedBy are the latest resume's, if any.
	ResumedAt *time.Time
	ResumedBy string
	// PlanqueAck is when planque confirmed it sees this halt (HaltedAt);
	// nil until it has.
	PlanqueAck *time.Time
}

// Store keeps halts. Every method is tenant-scoped, and a halt that was
// never made reads as the zero Halt.
type Store interface {
	// Halt halts agent (or the account) at at. Halting what is halted keeps
	// the first halt's HaltedAt; began says whether this call began one.
	Halt(ctx context.Context, tenant, agent string, at time.Time, by string) (h Halt, began bool, err error)
	// Resume ends the halt, keeping its HaltedAt; ended says whether it
	// was on.
	Resume(ctx context.Context, tenant, agent string, at time.Time, by string) (h Halt, ended bool, err error)
	// Get is agent's halt (or the account's).
	Get(ctx context.Context, tenant, agent string) (Halt, error)
	// AckPlanque records that planque sees the halt that began at haltedAt;
	// a halt begun since keeps no older ack.
	AckPlanque(ctx context.Context, tenant, agent string, haltedAt, at time.Time) error
	// Log keeps a halt or a resume for the Record; Events lists the
	// tenant's, newest first, at most limit.
	Log(ctx context.Context, e Event) error
	Events(ctx context.Context, tenant string, limit int) ([]Event, error)
}

// The kinds of Event.
const (
	KindHalt   = "halt"
	KindResume = "resume"
)

// Event is one halt or resume, as the Record shows it: who, when, which
// agent (Account: all of them), and the receipt that signs it.
type Event struct {
	TenantID string    `json:"-"`
	ID       string    `json:"id"`
	AgentID  string    `json:"agent,omitempty"`
	Kind     string    `json:"kind"`
	At       time.Time `json:"at"`
	By       string    `json:"by,omitempty"`
	Receipt  string    `json:"receipt,omitempty"`
}

// ErrInvalidAgent is an agent id no halt can name.
var ErrInvalidAgent = errors.New("halts: an agent id is 1–200 characters")

func checkAgent(agent string) error {
	if len(agent) > 200 {
		return ErrInvalidAgent
	}
	return nil
}

// Effective is what holds for an agent: its own halt over its account's.
type Effective struct {
	Own, Account Halt
}

// On reports whether the agent is halted: it, or its account, is.
func (e Effective) On() bool { return e.Own.On || e.Account.On }

// At is when the latest of the two halts began: what was asked at or
// before it never goes through, even once resumed. Zero if never halted.
func (e Effective) At() time.Time {
	if e.Account.HaltedAt.After(e.Own.HaltedAt) {
		return e.Account.HaltedAt
	}
	return e.Own.HaltedAt
}

// Before reports whether something first asked at t comes from before the
// latest halt.
func (e Effective) Before(t time.Time) bool {
	at := e.At()
	return !at.IsZero() && !t.After(at)
}

// Confirmed reports whether every halt that is on has planque's ack.
func (e Effective) Confirmed() bool {
	for _, h := range []Halt{e.Own, e.Account} {
		if h.On && h.PlanqueAck == nil {
			return false
		}
	}
	return true
}

// For is agent's effective halt. For Account it is the account's alone.
func For(ctx context.Context, s Store, tenant, agent string) (Effective, error) {
	var e Effective
	var err error
	if agent != Account {
		if e.Own, err = s.Get(ctx, tenant, agent); err != nil {
			return Effective{}, err
		}
	}
	if e.Account, err = s.Get(ctx, tenant, Account); err != nil {
		return Effective{}, err
	}
	return e, nil
}
