package pages

import (
	"sort"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/gatewayclient"
)

// AgentSummary is one agent with everything that hangs off its one
// name: the cards it holds, the keys bound to it, the kiff-guard
// runtimes registered under it, and its latest decision.
type AgentSummary struct {
	ID       string
	Recorded bool
	Sources  []string
	Cards    []apiclient.Mandate
	Keys     []apiclient.KeyListItem
	// Runtimes are most recently seen first.
	Runtimes []apiclient.GuardConnection
	// Last is the agent's most recent verified decision (made with a key
	// bound to it, the agent-as-unit design) among the tenant's recent
	// decisions, nil when none was read. A decision where another
	// credential merely claimed the agent's name is never its last
	// action.
	Last *apiclient.ProposalDecision
	// LastActive is the most recent verified decision made with a key
	// of the agent that is still active. It is the evidence for being
	// connected now; Last stays the record of what happened last, even
	// when its key has since been revoked.
	LastActive *apiclient.ProposalDecision
	// DecisionsUnknown: no verified decision was found, but the page of
	// the tenant's decisions read was full, so one may exist beyond it.
	// The absence is unknown, not a fact.
	DecisionsUnknown bool
	// DecisionsTruncated: the tenant page read was full.
	DecisionsTruncated bool
	// ActiveUnknown: no request from an active key of the agent was found,
	// but the page read was full or failed and the agent has an active
	// key, so such a request may exist beyond it. A revoked key's request
	// found in the page does not settle it.
	ActiveUnknown bool
	// DecisionsFailed: the tenant's recent decisions could not be read
	// at all, so nothing is known about this agent's decisions. Runtime
	// evidence still stands.
	DecisionsFailed bool
	// KeysFailed: the account's keys could not be read, so whether the
	// key behind Last is still active is unknown rather than false.
	KeysFailed bool
}

// LastKeyRevoked reports whether the key that made the agent's last
// verified request is no longer among its active keys. A revoked key
// cannot make the agent connected, however recent its last request; an
// earlier request from a key still active can. Unknown keys are not
// reported as revoked.
func (a AgentSummary) LastKeyRevoked() bool {
	if a.Last == nil || a.Last.PrincipalAPIKeyID == "" || a.KeysFailed {
		return false
	}
	return !hasKey(a.Keys, a.Last.PrincipalAPIKeyID)
}

// activeEvidence is the newest verified request from a key of the
// agent that is still active. When the keys could not be read, the
// newest verified request stands in: a missing key is unknown, not
// revoked.
func (a AgentSummary) activeEvidence() *apiclient.ProposalDecision {
	if a.KeysFailed {
		return a.Last
	}
	return a.LastActive
}

// HasActiveKeyRequest reports whether a key of the agent that is still
// active has made a verified request, however long ago.
func (a AgentSummary) HasActiveKeyRequest() bool {
	d := a.activeEvidence()
	return d != nil && d.PrincipalAPIKeyID != ""
}

// ConnectedNow is the "Connected" of Status: a verified request from an
// active key of the agent within the last 24 hours. The list, the
// page's badge and its Overview all use it, so they cannot disagree.
func (a AgentSummary) ConnectedNow(now time.Time) bool {
	return a.HasActiveKeyRequest() && now.Sub(a.activeEvidence().CreatedAt) < 24*time.Hour
}

// AgentsFrom joins the agents list with its cards, keys, runtimes and
// the tenant's recent decisions (newest first; truncated when the page
// read was full; failed when it could not be read, in which case
// decisions is empty and every agent's decision evidence is unknown).
// Archived agents and archived cards are left out. Ordered by latest
// activity, then name.
func AgentsFrom(list []apiclient.Agent, cards []apiclient.Mandate, keys []apiclient.KeyListItem, runtimes []apiclient.GuardConnection, decisions []apiclient.ProposalDecision, truncated, failed bool) []AgentSummary {
	byID := map[string]*AgentSummary{}
	var order []string
	for _, a := range list {
		if a.ArchivedAt != nil {
			continue
		}
		byID[a.ID] = &AgentSummary{ID: a.ID, Recorded: a.Recorded, Sources: a.Sources}
		order = append(order, a.ID)
	}
	for _, m := range cards {
		if m.ArchivedAt != nil {
			continue
		}
		// A shared Card is each of its holders' (the mandates design
		// amendment).
		for _, holder := range m.Holders() {
			if s, ok := byID[holder]; ok {
				s.Cards = append(s.Cards, m)
			}
		}
	}
	for _, k := range keys {
		if s, ok := byID[k.AgentID]; ok {
			s.Keys = append(s.Keys, k)
		}
	}
	for _, r := range runtimes {
		if s, ok := byID[r.AgentID]; ok {
			s.Runtimes = append(s.Runtimes, r)
		}
	}
	for i := range decisions {
		if !decisions[i].ActorVerified {
			continue
		}
		s, ok := byID[decisions[i].ActorID]
		if !ok {
			continue
		}
		d := decisions[i]
		if s.Last == nil {
			s.Last = &d
		}
		if s.LastActive == nil && d.PrincipalAPIKeyID != "" && hasKey(s.Keys, d.PrincipalAPIKeyID) {
			s.LastActive = &d
		}
	}
	out := make([]AgentSummary, 0, len(order))
	for _, id := range order {
		s := byID[id]
		sort.SliceStable(s.Runtimes, func(i, j int) bool { return s.Runtimes[i].LastSeenAt.After(s.Runtimes[j].LastSeenAt) })
		s.DecisionsTruncated = truncated
		s.DecisionsFailed = failed
		s.DecisionsUnknown = (truncated || failed) && s.Last == nil
		s.ActiveUnknown = (truncated || failed) && s.LastActive == nil && len(s.Keys) > 0
		out = append(out, *s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := out[i].LastActivity(), out[j].LastActivity()
		if !ai.Equal(aj) {
			return ai.After(aj)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func hasKey(keys []apiclient.KeyListItem, id string) bool {
	for _, k := range keys {
		if k.ID == id {
			return true
		}
	}
	return false
}

// LastActivity is the later of the agent's last guard heartbeat and its
// last decision; zero when neither is known.
func (a AgentSummary) LastActivity() time.Time {
	var t time.Time
	if len(a.Runtimes) > 0 {
		t = a.Runtimes[0].LastSeenAt
	}
	if a.Last != nil && a.Last.CreatedAt.After(t) {
		t = a.Last.CreatedAt
	}
	return t
}

// Status is the agent's connection state in a word or two, and a tone
// for it ("ok", "warn", "off"):
//
//   - Enforcing / Observing: a guard runtime seen within the hour, by
//     its mode. Observing means the guard never asks KIFF, so the
//     agent's cards cannot decide yet.
//   - Connected: no live runtime, but its latest verified decision (its
//     own bound key, still active) is recent: a direct integration.
//   - Last seen …: a runtime, or an active key, that has gone quiet.
//   - Activity unknown: no live runtime, and the decisions could not be
//     read at all.
//   - Not seen recently: nothing verified from an active key in the
//     decisions read, but the page read was full, so that is not a fact.
//   - Not connected: nothing yet.
func (a AgentSummary) Status(now time.Time) (label, tone string) {
	if len(a.Runtimes) > 0 && now.Sub(a.Runtimes[0].LastSeenAt) < time.Hour {
		for _, r := range a.Runtimes {
			if r.Mode != "observe" && now.Sub(r.LastSeenAt) < time.Hour {
				return "Enforcing", "ok"
			}
		}
		return "Observing", "warn"
	}
	if a.ConnectedNow(now) {
		return "Connected", "ok"
	}
	if a.DecisionsFailed {
		return "Activity unknown", "off"
	}
	if len(a.Runtimes) > 0 || a.HasActiveKeyRequest() {
		// Seen means by its guard or an active key: a request from a
		// revoked key is history, not a sign of the agent.
		var t time.Time
		if len(a.Runtimes) > 0 {
			t = a.Runtimes[0].LastSeenAt
		}
		if d := a.activeEvidence(); d != nil && d.CreatedAt.After(t) {
			t = d.CreatedAt
		}
		return "Last seen " + timeAgo(t), "off"
	}
	if a.DecisionsUnknown || a.ActiveUnknown {
		return "Not seen recently", "off"
	}
	return "Not connected", "off"
}

// ActiveCards counts the agent's cards in force.
func (a AgentSummary) ActiveCards(now time.Time) int {
	n := 0
	for _, m := range a.Cards {
		if cardInForce(m, now) {
			n++
		}
	}
	return n
}

// Connection is the agent's connection status in the card page's terms,
// so the agent page and the card page read the same facts the same way.
func (a AgentSummary) Connection(decisions []apiclient.ProposalDecision, now time.Time) CardConnection {
	return NewCardConnection(a.ID, decisions, a.DecisionsTruncated || a.DecisionsFailed, a.Keys, false, a.Runtimes, false, now)
}

// AgentsView is the Agents list page.
type AgentsView struct {
	Email, Subject, DisplayName string
	Me                          *apiclient.Me
	Flash                       *DashboardFlash
	Agents                      []AgentSummary
	LoadError                   string
	// Partial names the sources that could not be read, so a missing
	// card or runtime reads as unknown rather than absent.
	Partial []string
}

// AgentDetailView is one agent's page.
type AgentDetailView struct {
	Email, Subject, DisplayName string
	Me                          *apiclient.Me
	Flash                       *DashboardFlash
	Agent                       AgentSummary
	// Decisions are the agent's recent decisions among the tenant's
	// recent ones, newest first.
	Decisions []apiclient.ProposalDecision
	Tab       string // "overview" | "keys" | "tools" | "cards"
	// Revoke is the key awaiting confirmation on the Keys tab ("" for none).
	Revoke string
	// Tools are the account's connected tools, read for the Tools tab;
	// ToolsError says why they could not be.
	Tools      []gatewayclient.Connection
	ToolsError string
	LoadError  string
	Partial    []string
	// GatewayURL is the KIFF MCP gateway endpoint; empty when this
	// deployment has no gateway, and the gateway section is hidden.
	GatewayURL string
	// ConnectLinks are the agent's connect links, read for the Tools tab;
	// ConnectLinksError says why they could not be.
	ConnectLinks      []apiclient.ConnectLink
	ConnectLinksError string
}

// AgentNewView is the Add agent form.
type AgentNewView struct {
	Email, Subject, DisplayName string
	Me                          *apiclient.Me
	Flash                       *DashboardFlash
	Name                        string
	Error                       string
}
