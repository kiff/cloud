// Package thune is KIFF's money layer for refunds (thune step one): the
// case a refund is decided on, the claim that hands one request to one
// taf, the lock that runs one charge's refunds one at a time, the two
// statuses an adjustment carries, and the watch that follows the money to
// its end.
//
// It adds no second execution record. A refund stays one gateway call
// (internal/invocation), sent once through the connection's permit; thune
// keeps what the call is about: which request, which charge, the facts the
// processor gave, and what became of the money.
//
// Three amounts are kept apart, and never changed quietly:
//   - requested: the agent's figure, or "the remainder". Not trusted.
//   - facts: what the processor says, each read with the time it was read.
//   - authorized: what KIFF allows, the only one the Card counts and the
//     permit carries.
package thune

import (
	"fmt"
	"strings"
	"time"
)

// KindRefund is the one kind step one handles.
const KindRefund = "refund"

// Identity is one business adjustment: the processor account, the object
// (the charge), the kind and the request it answers. The claim is unique
// on it. Account comes from the processor, never from a connection's name,
// so two connections to one account are one account.
type Identity struct {
	TenantID string
	Account  string
	Object   string
	Kind     string
	// Request is the id of what asked for the refund in a connected app
	// (a ticket, a message, an order line), never the agent's words.
	Request string
}

// Valid reports whether every part is set but Request, which a case may
// lack (it is then held for the owner, never claimed).
func (id Identity) Valid() error {
	switch {
	case id.TenantID == "":
		return fmt.Errorf("thune: identity has no tenant")
	case id.Account == "":
		return fmt.Errorf("thune: identity has no processor account")
	case id.Object == "":
		return fmt.Errorf("thune: identity has no charge")
	case id.Kind != KindRefund:
		return fmt.Errorf("thune: kind %q is not handled", id.Kind)
	}
	return nil
}

// Refund statuses at the processor, as thune keeps them.
const (
	RefundPending   = "pending"
	RefundSucceeded = "succeeded"
	RefundFailed    = "failed"
	RefundCancelled = "cancelled"
)

// Refund is one refund the processor holds on a charge.
type Refund struct {
	ID     string `json:"id"`
	Amount int64  `json:"amount"`
	Status string `json:"status"`
	// Marker is the operation id KIFF wrote on it (metadata.kiff_op), the
	// gateway's invocation key; "" for a refund made outside KIFF.
	Marker string `json:"marker,omitempty"`
	// InFlight: not read from the processor. One of KIFF's own calls on
	// the charge is sending, or ended unknown, and the processor doesn't
	// list its refund yet; it counts as pending until it does, or until
	// the call is known not to have sent it. It has no ID.
	InFlight bool `json:"in_flight,omitempty"`
}

// counts reports whether the refund takes from the charge's remainder:
// pending and succeeded do, failed and cancelled gave the money back.
func (r Refund) counts() bool { return r.Status == RefundPending || r.Status == RefundSucceeded }

// Facts are what the processor said about a charge, read for one case.
type Facts struct {
	Account  string `json:"account"`
	Charge   string `json:"charge"`
	Currency string `json:"currency"`
	// Captured is the charge's captured amount, in minor units.
	Captured int64 `json:"captured"`
	// Disputed: a dispute on the charge is open.
	Disputed bool      `json:"disputed"`
	Refunds  []Refund  `json:"refunds"`
	ReadAt   time.Time `json:"read_at"`
}

// Refunded is what succeeded refunds took from the charge.
func (f Facts) Refunded() int64 { return f.sum(RefundSucceeded) }

// Pending is what pending refunds hold.
func (f Facts) Pending() int64 { return f.sum(RefundPending) }

func (f Facts) sum(status string) int64 {
	var n int64
	for _, r := range f.Refunds {
		if r.Status == status {
			n += r.Amount
		}
	}
	return n
}

// Remainder is what can still be refunded: captured less every refund
// that succeeded or is pending.
func (f Facts) Remainder() int64 { return f.Captured - f.Refunded() - f.Pending() }

// WithInFlight returns f with KIFF's in-flight refunds on the charge added
// as pending, each unless the processor already lists its marker.
func (f Facts) WithInFlight(inflight []Refund) Facts {
	for _, r := range inflight {
		if _, listed := f.Marked(r.Marker); listed {
			continue
		}
		r.ID, r.Status, r.InFlight = "", RefundPending, true
		f.Refunds = append(f.Refunds, r)
	}
	return f
}

// Marked returns the refund carrying marker, if the processor has one.
func (f Facts) Marked(marker string) (Refund, bool) {
	if marker == "" {
		return Refund{}, false
	}
	for _, r := range f.Refunds {
		if r.Marker == marker && !r.InFlight {
			return r, true
		}
	}
	return Refund{}, false
}

// Ask is what the agent asked for.
type Ask struct {
	// Amount is the requested figure in minor units; ignored when
	// Remainder is set.
	Amount    int64
	Remainder bool
	// Currency, when the agent gave one, must be the charge's.
	Currency string
}

// Verdicts a case reaches before the Card weighs it.
const (
	// VerdictAllow: the case holds; the Card decides on Authorized.
	VerdictAllow = "allow"
	// VerdictAsk: held for the owner whatever the Card says, with Reasons.
	// The Card's limits still apply on top.
	VerdictAsk = "ask"
	// VerdictRefuse: not sent, with Reasons. Nothing is counted.
	VerdictRefuse = "refuse"
)

// Reasons a case is held or refused for. They are stable codes; the
// words are tafs' and KIFF's screens'.
const (
	ReasonNoRequest     = "refund_no_request"
	ReasonSecondRefund  = "refund_second_on_charge"
	ReasonDisputeOpen   = "refund_dispute_open"
	ReasonNothingLeft   = "refund_nothing_left"
	ReasonOverRemainder = "refund_over_remainder"
	ReasonCurrency      = "refund_currency_mismatch"
	ReasonBadAmount     = "refund_bad_amount"
	ReasonFactsStale    = "refund_facts_stale"
	ReasonClaimed       = "refund_already_claimed"
	ReasonChargeBusy    = "refund_charge_busy"
	ReasonFactsChanged  = "refund_facts_changed"
)

// Case is what a refund is decided on.
type Case struct {
	Identity  Identity
	Requested int64
	Remainder bool
	Facts     Facts
	// Authorized is the exact amount KIFF allows: the requested figure,
	// or the remainder read from the processor. 0 when refused.
	Authorized int64
	Verdict    string
	Reasons    []string
}

// MaxFactsAge is how old the facts a decision uses may be.
const MaxFactsAge = 2 * time.Minute

// Build builds the case for ask on the charge facts describe, at now.
// It never changes the requested amount: it fits the facts, or the case is
// held or refused. "The remainder" becomes the figure read.
func Build(id Identity, ask Ask, facts Facts, now time.Time) Case {
	c := Case{Identity: id, Requested: ask.Amount, Remainder: ask.Remainder, Facts: facts, Verdict: VerdictAllow}
	refuse := func(r string) Case {
		c.Verdict, c.Reasons, c.Authorized = VerdictRefuse, []string{r}, 0
		return c
	}
	if facts.ReadAt.IsZero() || now.Sub(facts.ReadAt) > MaxFactsAge || facts.ReadAt.After(now.Add(time.Minute)) {
		return refuse(ReasonFactsStale)
	}
	if ask.Currency != "" && !strings.EqualFold(ask.Currency, facts.Currency) {
		return refuse(ReasonCurrency)
	}
	if facts.Disputed {
		return refuse(ReasonDisputeOpen)
	}
	left := facts.Remainder()
	if left <= 0 {
		return refuse(ReasonNothingLeft)
	}
	switch {
	case ask.Remainder:
		c.Authorized = left
	case ask.Amount <= 0:
		return refuse(ReasonBadAmount)
	case ask.Amount > left:
		return refuse(ReasonOverRemainder)
	default:
		c.Authorized = ask.Amount
	}
	if id.Request == "" {
		c.Verdict = VerdictAsk
		c.Reasons = append(c.Reasons, ReasonNoRequest)
	}
	for _, r := range facts.Refunds {
		if r.counts() {
			// A second refund on a charge, from anyone, is the owner's,
			// shown beside the first.
			c.Verdict = VerdictAsk
			c.Reasons = append(c.Reasons, ReasonSecondRefund)
			break
		}
	}
	return c
}

// Changed reports whether facts read again before sending differ from the
// facts a case was decided on in a way that voids its approval: the
// remainder, the dispute state, the currency or the charge.
func Changed(was, now Facts) bool {
	return was.Charge != now.Charge || was.Account != now.Account ||
		!strings.EqualFold(was.Currency, now.Currency) ||
		was.Disputed != now.Disputed || was.Remainder() != now.Remainder()
}
