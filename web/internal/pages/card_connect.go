package pages

import (
	"bytes"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// cardInForce reports whether a card can still authorize anything.
func cardInForce(m apiclient.Mandate, now time.Time) bool {
	return m.RevokedAt == nil && m.ArchivedAt == nil && (m.ValidUntil == nil || now.Before(*m.ValidUntil))
}

// cardConnectOpen opens the connect panel for a card nothing has drawn
// on in the current window; a card already in use keeps it folded.
func cardConnectOpen(v CardDetailView) bool {
	return v.Statement == nil || len(v.Statement.Draws) == 0
}

// CardConnectPrompt is the instruction a customer pastes into their
// coding agent to connect this card. Everything specific to the card is
// filled in, because a wrong actor id, domain or unit is the quietest
// way for an integration to fail: the decision still comes back, just
// not for this card.
func CardConnectPrompt(m apiclient.Mandate, task CardTask, hasTask bool) string {
	var b strings.Builder
	w := func(s ...string) {
		for _, p := range s {
			b.WriteString(p)
		}
		b.WriteByte('\n')
	}
	actions := make([]string, 0, len(m.Grants))
	for _, g := range m.Grants {
		actions = append(actions, g.Action)
	}
	action := strings.Join(actions, ", ")
	if hasTask {
		action = task.Action
	}

	w("Connect my agent to its KIFF card, so the agent asks KIFF before it acts and only acts when KIFF allows it.")
	w()
	w("Card: ", m.ID)
	w("Agent name, sent as actor_id: ", m.Subject)
	w("Domain: ", m.Domain)
	if hasTask {
		w("Action: ", task.Action, " (", strings.ToLower(task.Label), ")")
	} else {
		w("Actions: ", action)
	}
	for _, a := range m.Aggregates {
		w("Limit: ", authorityValue(a.Limit, a.Quantity), " ", windowLabel(a.Window), eachItem(a.Per), limitUnitNote(a.Quantity))
	}
	w()
	w("Steps:")
	w("1. Find the place in this codebase where the agent performs this action. Explain it to me before changing anything.")
	params := map[string]any{"...": "the action's parameters"}
	entityType, entityHint := "<entity type>", "the entity's id"
	if hasTask {
		params = map[string]any{}
		for k := range task.TestParams {
			params[k] = "<" + k + ">"
		}
		entityType, entityHint = task.EntityType, task.EntityHint
	}
	body := map[string]any{
		"domain": m.Domain, "entity_id": "<" + entityHint + ">", "entity_type": entityType,
		"action_name": strings.Split(action, ", ")[0], "actor_id": m.Subject, "parameters": params,
	}
	w("2. Just before it runs, call POST https://api.kiff.dev/v1/proposals/decide with header \"Authorization: Bearer $KIFF_CLOUD_API_KEY\" and this JSON body:")
	w("   ", compactJSON(body))
	w("3. Run the action only when the response's \"outcome\" is \"allowed\". On anything else, do not run it and surface \"reasons\" and \"message\" (for example mandate_limit_reached means the card's limit would be exceeded).")
	w("4. KIFF decides against the entity's current state, so the system must also report that entity's events to POST https://api.kiff.dev/v1/events/raw?domain=", m.Domain, ". Check with me which events we already emit.")
	role := "a role this domain allows to run the action"
	keyHint := "; if I cannot get one, stop and tell me, and KIFF will issue one as part of guided setup"
	if hasTask {
		role = "the \"" + task.Role + "\" role"
		keyHint = " (I can create one with that role on the card's page in KIFF)"
	}
	w("5. Read the key from the environment variable KIFF_CLOUD_API_KEY and never hard-code it. Ask me for it. The key must hold ", role, ", or KIFF refuses the action on permissions before it checks the card", keyHint, ". It must also be the key issued for ", m.Subject, ": KIFF refuses a key issued for another agent (actor_not_key_holder), and a key not issued for any agent wherever a card is drawn on (key_not_bound_to_agent).")
	w("6. Add a test for the allowed and the refused path.")
	w()
	w("Reference: https://kiff.dev/docs/connect-an-agent#call-the-decision-api-directly and https://kiff.dev/llms.txt")
	return strings.TrimRight(b.String(), "\n")
}

func limitUnitNote(quantity string) string {
	if p, ok := strings.CutPrefix(quantity, "sum("); ok {
		return ", summed over the " + strings.TrimSuffix(p, ")") + " parameter (euro cents)"
	}
	return ", counted per authorized action"
}

func compactJSON(v map[string]any) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(plainJSON(k))
		b.WriteString(": ")
		b.WriteString(plainJSON(v[k]))
	}
	b.WriteByte('}')
	return b.String()
}

// plainJSON encodes v without HTML escaping: the prompt is read and
// pasted by people, and json.Marshal would turn "<order id>" into
// "\u003corder id\u003e".
func plainJSON(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimRight(buf.String(), "\n")
}

// guidedSetupHref opens a support request that already names the card.
func guidedSetupHref(m apiclient.Mandate) string {
	q := url.Values{}
	q.Set("category", "question")
	q.Set("title", "Guided setup for card "+m.ID)
	q.Set("body", "I'd like help connecting the card "+m.ID+" (agent "+m.Subject+", domain "+m.Domain+") to our agent.\n\nOur agent runs on: \nThe action happens in: ")
	return "/dashboard/support/new?" + q.Encode()
}
