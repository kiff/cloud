package pages

import (
	"context"
	"sort"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
)

// agentTab is one tab on an agent's page.
type agentTab struct{ ID, Label, Suffix string }

// agentTabs are the agent page's tabs, one topic each.
var agentTabs = []agentTab{
	{"overview", "Overview", ""},
	{"cards", "Cards", "/cards"},
	{"tools", "Tools", "/tools"},
	{"keys", "Keys", "/keys"},
}

// keyIsGateway reports whether the key holds gateway_agent, the one role
// the KIFF MCP gateway accepts.
func keyIsGateway(k apiclient.KeyListItem) bool {
	for _, r := range k.Roles {
		if r == "gateway_agent" {
			return true
		}
	}
	return false
}

// keyKindLabel says what a key is for, in the words the page uses.
func keyKindLabel(k apiclient.KeyListItem) string {
	if keyIsGateway(k) {
		return "Gateway (MCP tools)"
	}
	return "Decision API (kiff-guard)"
}

// keysByUse orders keys most recently used first, never-used last, then
// newest first: the order that tells look-alike keys apart.
func keysByUse(keys []apiclient.KeyListItem) []apiclient.KeyListItem {
	out := append([]apiclient.KeyListItem(nil), keys...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].LastUsedAt, out[j].LastUsedAt
		switch {
		case a != nil && b != nil && *a != *b:
			return *a > *b
		case (a == nil) != (b == nil):
			return a != nil
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out
}

// keyLastUsed is a key's last use, or "Never used".
func keyLastUsed(k apiclient.KeyListItem) string {
	if k.LastUsedAt == nil {
		return "Never used"
	}
	return keyUsedLabel(*k.LastUsedAt)
}

// agentStep is one step of getting an agent working.
type agentStep struct {
	Title, Detail, Href, Action string
	Done                        bool
}

// agentSteps are the three things an agent needs before KIFF governs it:
// a key, a Card, and a first call that reached KIFF.
func agentSteps(ctx context.Context, v AgentDetailView, connected bool, inForce int) []agentStep {
	base := agentHref(v.Agent.ID)
	key := agentStep{"Give it a key", "The agent signs its calls with it.", base + "/keys", "Open Keys", len(v.Agent.Keys) > 0}
	if gatewayFirst(ctx, v) {
		key = agentStep{"Give it a key", "Connecting it to the KIFF gateway creates its key and the configuration for Claude Code or Codex.", base + "/tools#gateway", "Connect", len(v.Agent.Keys) > 0}
	}
	return []agentStep{
		key,
		{"Give it a Card", "What it may do, and how much. Without one, actions that need a Card are refused.", base + "/cards", "Open Cards", inForce > 0},
		{"Make a first call", "Through kiff-guard, or through the KIFF gateway for MCP tools.", base + "/tools", "Open Tools", connected},
	}
}

// gatewayFirst reports whether the agent is best connected through the
// KIFF gateway: it holds a tool Card, or the account is personal, where
// agents reach tools rather than call KIFF from code. Only when this
// deployment has a gateway.
func gatewayFirst(ctx context.Context, v AgentDetailView) bool {
	if v.GatewayURL == "" {
		return false
	}
	if components.IsPersonal(ctx) {
		return true
	}
	for _, m := range v.Agent.Cards {
		if IsToolCard(m) {
			return true
		}
	}
	return false
}

// cardAggregates names a Card's running limits ("€1,000 per day"), or
// "" when it has none.
func cardAggregates(m apiclient.Mandate) string {
	var out []string
	for _, a := range m.Aggregates {
		out = append(out, strings.TrimSpace(authorityValue(a.Limit, a.Quantity)+" "+windowLabel(a.Window)+eachItem(a.Per)))
	}
	return strings.Join(out, "; ")
}
