package pages

import (
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// One rule says whether an agent is connected: a verified request from
// a key of the agent that is still active, within the last 24 hours,
// unless a guard runtime seen within the hour says more. The list, the
// page's badge and its Overview all read it, so the list can no longer
// say "Not connected" while the Overview says "Connected".
func TestAgentConnectedNeedsARecentRequestFromAnActiveKey(t *testing.T) {
	now := time.Now()
	req := func(ago time.Duration, key string) apiclient.ProposalDecision {
		return apiclient.ProposalDecision{ActorID: "a", ActorVerified: true, PrincipalAPIKeyID: key, CreatedAt: now.Add(-ago)}
	}
	key := func(id string) apiclient.KeyListItem { return apiclient.KeyListItem{ID: id, AgentID: "a"} }
	live := []apiclient.GuardConnection{{AgentID: "a", Mode: "enforce", LastSeenAt: now.Add(-time.Minute)}}

	for _, tc := range []struct {
		name        string
		keys        []apiclient.KeyListItem
		keysFailed  bool
		truncated   bool // the page of decisions read was full
		runtimes    []apiclient.GuardConnection
		decisions   []apiclient.ProposalDecision // newest first
		connected   bool
		revoked     bool
		labelPrefix string
	}{
		{name: "recent, active key", keys: []apiclient.KeyListItem{key("k1")},
			decisions: []apiclient.ProposalDecision{req(time.Hour, "k1")}, connected: true, labelPrefix: "Connected"},
		{name: "recent, revoked key",
			decisions: []apiclient.ProposalDecision{req(time.Hour, "k1")}, revoked: true, labelPrefix: "Not connected"},
		{name: "recent, keys unread", keysFailed: true,
			decisions: []apiclient.ProposalDecision{req(time.Hour, "k1")}, connected: true, labelPrefix: "Connected"},
		{name: "a day old, active key", keys: []apiclient.KeyListItem{key("k1")},
			decisions: []apiclient.ProposalDecision{req(25*time.Hour, "k1")}, labelPrefix: "Last seen"},
		{name: "a day old, revoked key",
			decisions: []apiclient.ProposalDecision{req(25*time.Hour, "k1")}, revoked: true, labelPrefix: "Not connected"},
		// Two keys: the newest caller's key is revoked, the other is
		// still active and was used a minute before. Still connected.
		{name: "newest caller revoked, another key active and recent", keys: []apiclient.KeyListItem{key("kA")},
			decisions: []apiclient.ProposalDecision{req(time.Minute, "kB"), req(2*time.Minute, "kA")},
			connected: true, revoked: true, labelPrefix: "Connected"},
		// A guard seen within the hour leads, as before: an old revoked
		// key does not override it.
		{name: "live runtime, old revoked key", runtimes: live,
			decisions: []apiclient.ProposalDecision{req(time.Hour, "kOld")}, revoked: true, labelPrefix: "Enforcing"},
		// The page read was full: a revoked key's request is in it, the
		// active key's may be beyond it. Unknown, not disconnected.
		{name: "page full, revoked in it, active key may be beyond", keys: []apiclient.KeyListItem{key("kA")}, truncated: true,
			decisions: []apiclient.ProposalDecision{req(time.Minute, "kB")}, revoked: true, labelPrefix: "Not seen recently"},
		// No active key remains: that is definite, page full or not.
		{name: "page full, revoked in it, no active key", truncated: true,
			decisions: []apiclient.ProposalDecision{req(time.Minute, "kB")}, revoked: true, labelPrefix: "Not connected"},
		{name: "nothing", keys: []apiclient.KeyListItem{key("k1")}, labelPrefix: "Not connected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agents := AgentsFrom([]apiclient.Agent{{ID: "a"}}, nil, tc.keys, tc.runtimes, tc.decisions, tc.truncated, false)
			if len(agents) != 1 {
				t.Fatalf("got %d agents", len(agents))
			}
			a := agents[0]
			a.KeysFailed = tc.keysFailed
			if got := a.ConnectedNow(now); got != tc.connected {
				t.Errorf("ConnectedNow = %v, want %v", got, tc.connected)
			}
			if got := a.LastKeyRevoked(); got != tc.revoked {
				t.Errorf("LastKeyRevoked = %v, want %v", got, tc.revoked)
			}
			label, _ := a.Status(now)
			if !strings.HasPrefix(label, tc.labelPrefix) {
				t.Errorf("Status = %q, want it to start %q", label, tc.labelPrefix)
			}
			if len(tc.runtimes) == 0 && (label == "Connected") != tc.connected {
				t.Errorf("Status %q disagrees with ConnectedNow %v", label, tc.connected)
			}
		})
	}
}
