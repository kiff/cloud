package pages

import (
	"fmt"
	"sort"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// CardConnection answers "is my agent connected, and is it using this
// card?" from what KIFF already records. Nothing new is stored.
//
// An agent reaches a card through kiff-guard: the guard registers its
// runtime and heartbeats (POST /v1/guard/connect, agent_id, adapter,
// mode), and on each mapped tool call asks KIFF to decide with actor_id
// set to that same agent id — the name a card's holder is. So:
//
//   - Runtimes are the guard runtimes registered under the holder's
//     name: whether the agent is running and talking to KIFF at all.
//   - In observe mode the guard records tool calls without asking KIFF,
//     so a live runtime can still leave the card unused.
//   - The last request comes from the holder's decisions made with an
//     agent key; test requests from the card page (the owner's session)
//     are not a connection. A custom integration that calls decide over
//     plain HTTP shows up here without a runtime.
type CardConnection struct {
	// Runtimes are the guard runtimes registered under the holder's
	// agent id, most recently seen first. RuntimesError marks them
	// unknown rather than absent.
	Runtimes      []apiclient.GuardConnection
	RuntimesError bool

	// Keys are the active API keys bound to the card's holder.
	Keys []apiclient.KeyListItem
	// KeysError is set when the keys could not be read; Keys is then
	// unknown rather than empty.
	KeysError bool

	// Connected is true once an agent key has sent a request that
	// reached one of the card's actions.
	Connected bool
	// LastAt, LastKeyID, LastKeyLabel and LastOutcome describe the most
	// recent such request.
	LastAt       time.Time
	LastKeyID    string
	LastKeyLabel string
	LastOutcome  string

	// Today counts decisions for the holder on the card's actions since
	// the start of the UTC day, test requests included. AtLeast marks it
	// as a floor: the page of decisions read was full.
	Today   int
	AtLeast bool

	// Truncated: the page of the holder's decisions read was full, so a
	// request from its key may exist beyond it. When nothing verified
	// was found, the honest answer is "not seen recently", not "not
	// connected".
	Truncated bool
}

// NewCardConnection derives the connection status. decisions are the
// holder's recent decisions on the card's actions, newest first, filtered
// from a bounded page; truncated reports that page was full. keys are
// the tenant's active keys, or nil with keysErr when they could not be
// read.
//
// Only a verified decision (a key bound to the holder made it, the
// agent-as-unit design) is a connection. An unbound key naming the
// holder, or the owner's test request, is a claim, not the agent.
func NewCardConnection(holder string, decisions []apiclient.ProposalDecision, truncated bool, keys []apiclient.KeyListItem, keysErr bool, runtimes []apiclient.GuardConnection, runtimesErr bool, now time.Time) CardConnection {
	c := CardConnection{KeysError: keysErr, RuntimesError: runtimesErr, Truncated: truncated, AtLeast: truncated}
	for _, r := range runtimes {
		if r.AgentID == holder {
			c.Runtimes = append(c.Runtimes, r)
		}
	}
	sort.SliceStable(c.Runtimes, func(i, j int) bool { return c.Runtimes[i].LastSeenAt.After(c.Runtimes[j].LastSeenAt) })
	labels := map[string]string{}
	for _, k := range keys {
		labels[k.ID] = k.Label
		if k.AgentID != "" && k.AgentID == holder {
			c.Keys = append(c.Keys, k)
		}
	}
	dayStart := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	for _, d := range decisions {
		if !d.CreatedAt.Before(dayStart) {
			c.Today++
		}
		if !c.Connected && d.ActorVerified && d.PrincipalAPIKeyID != "" {
			c.Connected = true
			c.LastAt = d.CreatedAt
			c.LastKeyID = d.PrincipalAPIKeyID
			c.LastKeyLabel = labels[d.PrincipalAPIKeyID]
			c.LastOutcome = d.Outcome
		}
	}
	return c
}

// Live reports whether a guard runtime for the holder was seen within
// the hour: the same "live" the Runtimes page uses.
func (c CardConnection) Live(now time.Time) bool {
	return len(c.Runtimes) > 0 && now.Sub(c.Runtimes[0].LastSeenAt) < time.Hour
}

// ObserveOnly reports whether every runtime for the holder runs in
// observe mode, where the guard records tool calls but never asks KIFF,
// so this card is not consulted.
func (c CardConnection) ObserveOnly() bool {
	if len(c.Runtimes) == 0 {
		return false
	}
	for _, r := range c.Runtimes {
		if r.Mode != "observe" {
			return false
		}
	}
	return true
}

// RuntimeSummary describes the holder's most recently seen guard
// runtime: "kiff-guard agno 1.1.0 in enforce mode, production, last seen
// 2 minutes ago."
func (c CardConnection) RuntimeSummary() string {
	if len(c.Runtimes) == 0 {
		return ""
	}
	r := c.Runtimes[0]
	out := "kiff-guard " + r.Adapter
	if r.SDKVersion != "" {
		out += " " + r.SDKVersion
	}
	out += " in " + r.Mode + " mode"
	if r.Environment != "" {
		out += ", " + r.Environment
	}
	out += ", last seen " + timeAgo(r.LastSeenAt) + "."
	if len(c.Runtimes) > 1 {
		out += fmt.Sprintf(" %d runtimes in all.", len(c.Runtimes))
	}
	return out
}

// TodayLabel is "3 decisions today", "50+ decisions today" or "no
// decisions today".
func (c CardConnection) TodayLabel() string {
	switch {
	case c.Today == 0 && c.AtLeast:
		return "no decisions today among those read"
	case c.Today == 0:
		return "no decisions today"
	case c.AtLeast:
		return fmt.Sprintf("at least %d %s today", c.Today, plural(c.Today, "decision", "decisions"))
	}
	return fmt.Sprintf("%d %s today", c.Today, plural(c.Today, "decision", "decisions"))
}

// LastKeyName names the key the last request came through: its label
// when the key is still listed, otherwise its id.
func (c CardConnection) LastKeyName() string {
	if c.LastKeyLabel != "" {
		return c.LastKeyLabel
	}
	return c.LastKeyID
}
