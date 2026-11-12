package gateway

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/gateway/internal/invocation"
)

// One object, one holder. A write its profile marks with a claim (a kind of
// action: dispute_answer, reminder) is made by one agent at a time on one
// object, the call's resource. The first agent's call claims it; another
// agent's call of the same kind on the same object, while that claim
// counts, is refused before KIFF decides it: nothing is drawn or sent, and
// the agent is told which agent holds it and in what state. A claim counts
// while its holder's call is being decided, held for the owner or being
// sent, within ClaimLease (a held call until its hold expires); the same
// agent's own later calls go through. Refunds keep thune's per-request
// claims and per-charge lock (refund.go).

// ReasonObjectClaimed refuses a call another agent's call holds the object for.
const ReasonObjectClaimed = "object_claimed"

// ReasonClaimUnavailable fails a call whose claim couldn't be checked.
const ReasonClaimUnavailable = "claim_unavailable"

// ClaimLease bounds a claim whose holder's call stalled without ending.
const ClaimLease = 15 * time.Minute

// claimKind is a profile's claim: a short lowercase name.
var claimKind = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)

// claimObject claims the call's object for t's kind of action. held is
// true, with the answer, when another agent holds it, or when the claim
// couldn't be checked (fail closed); false when the call goes on.
func (a *agent) claimObject(ctx context.Context, t Tool, in invocation.Invocation, params map[string]any, from []invocation.State) (json.RawMessage, bool, error) {
	if t.Claim == "" || t.ReadOnly || t.ResourceArg == "" {
		return nil, false, nil
	}
	object, _ := params[t.ResourceArg].(string)
	if object == "" {
		return nil, false, nil
	}
	c, err := a.g.Store.ClaimObject(ctx, a.me.TenantID, t.Claim, object, a.me.AgentID, in.Key, ClaimLease)
	if err != nil {
		out, ferr := a.finish(ctx, in.Key, from, invocation.StateFailed, invocation.Update{
			Message: "KIFF couldn't check whether another agent is acting on " + object + ", so the call was not sent. Retry with a new " + OperationArg + ".",
			Reasons: []string{ReasonClaimUnavailable},
		}, err)
		return out, true, ferr
	}
	if c.Won {
		return nil, false, nil
	}
	out, err := a.finish(ctx, in.Key, from, invocation.StateRefused, invocation.Update{
		Message: "Not sent: another agent (" + c.Agent + ") is already acting on " + object + " for " + t.Claim + ": its call is " + claimState(c.State) +
			". Nothing was decided or sent. Leave it to that agent, or try again once its call has ended.",
		Reasons: []string{ReasonObjectClaimed},
	}, nil)
	return out, true, err
}

// claimState says where the holder's call is, in the agent's words.
func claimState(s invocation.State) string {
	switch s {
	case invocation.StateHeld:
		return "waiting for the owner's approval"
	case invocation.StateForwarding:
		return "being sent"
	}
	return "being decided"
}

// ClaimRelated is an operation's claim_related: the kind of action on a
// related object to show the agent, and where that object sits in the read
// its amount lookup makes ({kind: "refund", path: "charge"} on a dispute
// answer: the dispute's charge). Shown, never blocking: an agent answering
// a dispute is told of the refunds already made on its charge.
type ClaimRelated struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// RelatedRefund is the one related kind KIFF knows today: refunds, as thune
// keeps them.
const RelatedRefund = "refund"

// withRelated adds to out the refunds already made on the call's related
// object, when its profile names one and the amount lookup found it. A
// failed lookup adds nothing: the call's answer is never held up by it.
func (a *agent) withRelated(ctx context.Context, t Tool, out json.RawMessage) json.RawMessage {
	if t.ClaimRelated == nil || a.related == "" || a.g.Thune == nil {
		return out
	}
	made, err := a.g.Thune.RefundsMade(ctx, a.me.TenantID, a.related)
	if err != nil || len(made) == 0 {
		return out
	}
	var lines []string
	for _, m := range made {
		who := "outside KIFF"
		if m.Agent != "" {
			who = "by agent " + m.Agent
		}
		state := m.Status
		if m.InFlight {
			state = "sent, not yet listed by the processor"
		}
		amount := strconv.FormatInt(m.Amount, 10)
		if m.Currency != "" {
			amount += " " + m.Currency + " (minor units)"
		}
		lines = append(lines, amount+", "+who+", "+state)
	}
	return withNote(out, "Refunds already made on "+a.related+": "+strings.Join(lines, "; ")+
		". Take them into account before you answer: money already returned to the customer may change what to say.")
}
