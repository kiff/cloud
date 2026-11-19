package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

func agentKey(agent, env string) auth.Identity {
	return auth.Identity{Subject: "key-g", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true,
		Roles: []string{auth.RoleGatewayAgent}, AgentID: agent, Environment: env}
}

type agentCardBody struct {
	AgentID string `json:"agent_id"`
	Cards   []struct {
		ID     string `json:"id"`
		Grants []struct {
			Action       string `json:"action"`
			PerActionMax []struct {
				Quantity string `json:"quantity"`
				Max      int64  `json:"max"`
			} `json:"per_action_max"`
		} `json:"grants"`
		Limits []struct {
			Quantity  string `json:"quantity"`
			Limit     int64  `json:"limit"`
			Used      int64  `json:"used"`
			Remaining int64  `json:"remaining"`
			Status    string `json:"status"`
		} `json:"limits"`
		OnExceed string `json:"on_exceed"`
	} `json:"cards"`
}

// An agent's key reads the Cards that apply to its own gateway calls, with
// what each limit has used and has left, and nothing else.
func TestAgentReadsOnlyItsOwnGatewayCards(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	refunds := []mandate.Grant{{Action: "refund", PerAction: []mandate.PerActionLimit{
		{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}, Max: 5000},
	}}}
	daily := []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}, Limit: 20000, Window: mandate.WindowCalendarDay}}
	revokedAt := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	for _, m := range []mandate.Mandate{
		{ID: "mdt-gw", Subject: "claude", Domain: "gateway", Grants: refunds, Aggregates: daily},
		{ID: "mdt-any", Subject: "claude", Grants: []mandate.Grant{{Action: "search"}}},
		{ID: "mdt-billing", Subject: "claude", Domain: "billing", Grants: refunds},
		{ID: "mdt-other", Subject: "someone-else", Domain: "gateway", Grants: refunds},
		{ID: "mdt-prod", Subject: "claude", Domain: "gateway", Environment: "production", Grants: refunds},
		{ID: "mdt-revoked", Subject: "claude", Domain: "gateway", Grants: refunds, RevokedAt: &revokedAt},
	} {
		m.TenantID = "t1"
		if err := store.Issue(ctx, m, "tester"); err != nil {
			t.Fatalf("seed %s: %v", m.ID, err)
		}
	}
	for i, amt := range []int64{5000, 2000, 500} {
		if err := store.Record(ctx, mandate.Draw{MandateID: "mdt-gw", TenantID: "t1", ProposalID: string(rune('a' + i)),
			QuantityKey: "sum(amount_cents)", Amount: amt, DrawnAt: time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	rr := do(t, mux, agentKey("claude", ""), http.MethodGet, "/v1/me/card", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got agentCardBody
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.AgentID != "claude" {
		t.Errorf("agent_id = %q", got.AgentID)
	}
	ids := map[string]int{}
	for i, c := range got.Cards {
		ids[c.ID] = i
	}
	if len(got.Cards) != 2 {
		t.Fatalf("cards = %v, want mdt-gw and mdt-any only", ids)
	}
	i, ok := ids["mdt-gw"]
	if _, any := ids["mdt-any"]; !ok || !any {
		t.Fatalf("cards = %v, want mdt-gw and mdt-any", ids)
	}
	gw := got.Cards[i]
	if len(gw.Grants) != 1 || len(gw.Grants[0].PerActionMax) != 1 || gw.Grants[0].PerActionMax[0].Max != 5000 {
		t.Errorf("grants = %+v", gw.Grants)
	}
	if len(gw.Limits) != 1 || gw.Limits[0].Used != 7500 || gw.Limits[0].Remaining != 12500 || gw.Limits[0].Status != "ok" {
		t.Errorf("limits = %+v", gw.Limits)
	}

	// The same numbers the Card's statement shows.
	rr = do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/mdt-gw/statement", nil)
	var st struct {
		Limits []struct {
			Used      int64 `json:"used"`
			Remaining int64 `json:"remaining"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil || len(st.Limits) != 1 ||
		st.Limits[0].Used != gw.Limits[0].Used || st.Limits[0].Remaining != gw.Limits[0].Remaining {
		t.Errorf("statement %s, card %+v", rr.Body.String(), gw.Limits)
	}

	// The same key in production also sees the production Card.
	rr = do(t, mux, agentKey("claude", "production"), http.MethodGet, "/v1/me/card", nil)
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || len(got.Cards) != 3 {
		t.Errorf("production key cards = %s", rr.Body.String())
	}
}

// Setup's key reads the live Cards another agent of the account acts under,
// whatever their environment, so Coach can change a taf from what it holds
// (kiff/tafs#214). It still reads its own without naming an agent.
func TestSetupReadsAnotherAgentsCard(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	refunds := []mandate.Grant{{Action: "refund", PerAction: []mandate.PerActionLimit{
		{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}, Max: 5000},
	}}}
	for _, m := range []mandate.Mandate{
		{ID: "rae-gw", Subject: "rae", Domain: "gateway", Grants: refunds},
		{ID: "rae-prod", Subject: "rae", Domain: "gateway", Environment: "production", Grants: refunds},
		{ID: "rae-billing", Subject: "rae", Domain: "billing", Grants: refunds},
		{ID: "coach", Subject: "setup", Domain: "gateway", Grants: []mandate.Grant{{Action: "tafs_state"}}},
	} {
		m.TenantID = "t1"
		if err := store.Issue(ctx, m, "tester"); err != nil {
			t.Fatalf("seed %s: %v", m.ID, err)
		}
	}
	setup := agentKey("setup", "")
	setup.Roles = []string{auth.RoleSetupReader}
	read := func(path string) agentCardBody {
		t.Helper()
		rr := do(t, mux, setup, http.MethodGet, path, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d body=%s", path, rr.Code, rr.Body.String())
		}
		var got agentCardBody
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return got
	}
	ids := func(b agentCardBody) []string {
		var out []string
		for _, c := range b.Cards {
			out = append(out, c.ID)
		}
		slices.Sort(out)
		return out
	}
	if got := read("/v1/me/card?agent=rae"); got.AgentID != "rae" || !slices.Equal(ids(got), []string{"rae-gw", "rae-prod"}) {
		t.Errorf("rae's Card read by Setup: agent %q cards %v", got.AgentID, ids(got))
	}
	if got := read("/v1/me/card"); got.AgentID != "setup" || !slices.Equal(ids(got), []string{"coach"}) {
		t.Errorf("Setup's own Card: agent %q cards %v", got.AgentID, ids(got))
	}
	if got := read("/v1/me/card?agent=nobody"); len(got.Cards) != 0 {
		t.Errorf("an agent with no Card: %v", ids(got))
	}
}

// With the account's agents known, Setup naming an agent that isn't on the
// account gets 404, so a wrong id never reads as a taf that may do nothing.
// A taf with no Card in force still reads as an empty list.
func TestSetupNamingAnUnknownAgentIsTold(t *testing.T) {
	store := mandate.NewMemoryStore()
	reg := agents.NewMemoryStore()
	ctx := context.Background()
	if err := reg.Ensure(ctx, agents.Agent{TenantID: "t1", ID: "rae", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	h := NewMandatesHandler(store, nil, WithMandatesAgents(reg))
	mux := http.NewServeMux()
	h.Register(mux)
	setup := agentKey("setup", "")
	setup.Roles = []string{auth.RoleSetupReader}
	if rr := do(t, mux, setup, http.MethodGet, "/v1/me/card?agent=nobody", nil); rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "no_such_agent") {
		t.Fatalf("an unknown agent: %d %s", rr.Code, rr.Body.String())
	}
	rr := do(t, mux, setup, http.MethodGet, "/v1/me/card?agent=rae", nil)
	var got agentCardBody
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &got) != nil || got.AgentID != "rae" || len(got.Cards) != 0 {
		t.Fatalf("a taf with no Card: %d %s", rr.Code, rr.Body.String())
	}
}

// Invariant: only Setup's key reads another agent's Card; a taf's own key
// sees only the Cards it holds, whatever agent it names.
// Category: authority
// Attack: call GET /v1/me/card?agent=<another agent> with a gateway agent's key
// Refused: the handler requires the setup_reader role to name an agent
func TestOnlySetupReadsAnotherAgentsCard(t *testing.T) {
	mux, _ := mandatesServer(t, owner())
	rr := do(t, mux, agentKey("claude", ""), http.MethodGet, "/v1/me/card?agent=rae", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("a taf's key naming another agent: status = %d, want 403", rr.Code)
	}
	if rr := do(t, mux, agentKey("claude", ""), http.MethodGet, "/v1/me/card?agent=claude", nil); rr.Code != http.StatusOK {
		t.Errorf("naming itself: status = %d", rr.Code)
	}
}

// Invariant: the agent's Card read answers only a key bound to an agent;
// a person or an unbound key has no agent whose Card it could show.
// Category: authority
// Attack: call GET /v1/me/card with a key that is not bound to an agent
// Refused: the handler requires the identity's agent
func TestCardReadNeedsAnAgentKey(t *testing.T) {
	mux, _ := mandatesServer(t, owner())
	for _, id := range []auth.Identity{owner(), runtimeKey()} {
		if rr := do(t, mux, id, http.MethodGet, "/v1/me/card", nil); rr.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", id.Subject, rr.Code)
		}
	}
}

// Invariant: reading its Card gives an agent's key no way to change one.
// Category: authority
// Attack: an agent key that can read its Card tries to issue or revoke one
// Refused: issuing and revoking stay gated on the management roles
func TestAgentKeyStillCannotIssueOrRevoke(t *testing.T) {
	mux, _ := mandatesServer(t, owner())
	body := map[string]any{
		"id": "mdt-self", "subject": "claude", "domain": "gateway",
		"grants":     []map[string]any{{"action": "refund"}},
		"aggregates": []map[string]any{{"quantity": "count", "limit": 1000000, "window": "calendar_day"}},
	}
	if rr := do(t, mux, agentKey("claude", ""), http.MethodPost, "/v1/me/mandates", body); rr.Code != http.StatusForbidden {
		t.Errorf("issue status = %d, want 403", rr.Code)
	}
	if rr := do(t, mux, agentKey("claude", ""), http.MethodPost, "/v1/me/mandates/mdt-gw/revoke", nil); rr.Code != http.StatusForbidden {
		t.Errorf("revoke status = %d, want 403", rr.Code)
	}
}

// listingDomains lists fixed domains, or fails to.
type listingDomains struct {
	stubDomainSource
	list []domain.TenantDomain
	err  error
}

func (s listingDomains) List(context.Context, string) ([]domain.TenantDomain, error) {
	return s.list, s.err
}

// A Card scoped to the gateway domain by id is shown. When the domain
// list cannot be read the read fails rather than leaving that Card out,
// which would tell the agent its tools are not covered.
func TestCardScopedByDomainID(t *testing.T) {
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	if err := store.Issue(context.Background(), mandate.Mandate{ID: "mdt-id", TenantID: "t1", Subject: "claude",
		DomainID: "dom-gw", Grants: []mandate.Grant{{Action: "refund"}}}, "tester"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	read := func(src listingDomains) (int, agentCardBody) {
		h := NewMandatesHandler(store, src, WithMandatesClock(func() time.Time {
			return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
		}))
		mux := http.NewServeMux()
		h.Register(mux)
		rr := do(t, mux, agentKey("claude", ""), http.MethodGet, "/v1/me/card", nil)
		var got agentCardBody
		_ = json.Unmarshal(rr.Body.Bytes(), &got)
		return rr.Code, got
	}
	code, got := read(listingDomains{list: []domain.TenantDomain{{DomainID: "dom-gw", Slug: "gateway"}}})
	if code != http.StatusOK || len(got.Cards) != 1 || got.Cards[0].ID != "mdt-id" {
		t.Errorf("gateway domain: %d %+v", code, got)
	}
	code, got = read(listingDomains{list: []domain.TenantDomain{{DomainID: "dom-gw", Slug: "billing"}}})
	if code != http.StatusOK || len(got.Cards) != 0 {
		t.Errorf("other domain: %d %+v", code, got)
	}
	if code, _ = read(listingDomains{err: errors.New("db down")}); code != http.StatusInternalServerError {
		t.Errorf("domain list failed: %d, want 500", code)
	}
}

// refundCentsYAML is a gateway domain whose one action, refund, sums
// amount_cents in euro cents.
const refundCentsYAML = `domain: gateway
entity: ToolCall
events: [TOOL_CALL_REQUESTED]
states: [requested]
transitions:
  - on: TOOL_CALL_REQUESTED
    from: ""
    to: requested
actions:
  - name: refund
    allowed_states: [requested]
    required_parameters: [amount_cents]
    parameters:
      - name: amount_cents
        type: int
        min: 0
        unit: EUR
        scale: 2
    required_permissions: [tools.refund]
    risk: medium
    approval: never
    executor: cloud.proxy
permissions:
  roles:
    gateway_agent: [tools.refund]
`

// A summed limit carries the unit its parameter declares in the gateway
// domain, so a reader can show €200.00 rather than 20000. A count does not.
func TestAgentCardLimitsCarryTheAmountUnit(t *testing.T) {
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	if err := store.Issue(context.Background(), mandate.Mandate{ID: "mdt-gw", TenantID: "t1", Subject: "claude", Domain: "gateway",
		Grants: []mandate.Grant{{Action: "refund"}},
		Aggregates: []mandate.Aggregate{
			{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}, Limit: 20000, Window: mandate.WindowCalendarDay},
			{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 10, Window: mandate.WindowCalendarDay},
		}}, "tester"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := NewMandatesHandler(store, stubDomainSource{yaml: refundCentsYAML}, WithMandatesClock(func() time.Time {
		return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	}))
	mux := http.NewServeMux()
	h.Register(mux)
	rr := do(t, mux, agentKey("claude", ""), http.MethodGet, "/v1/me/card", nil)
	var got struct {
		Cards []struct {
			Limits []map[string]any `json:"limits"`
		} `json:"cards"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || rr.Code != http.StatusOK || len(got.Cards) != 1 {
		t.Fatalf("status %d, body %s", rr.Code, rr.Body.String())
	}
	byQuantity := map[string]map[string]any{}
	for _, l := range got.Cards[0].Limits {
		byQuantity[l["quantity"].(string)] = l
	}
	sum := byQuantity["sum(amount_cents)"]
	if sum["unit"] != "EUR" || sum["scale"] != float64(2) {
		t.Errorf("sum limit = %v, want unit EUR, scale 2", sum)
	}
	if _, ok := byQuantity["count"]["unit"]; ok {
		t.Errorf("count limit has a unit: %v", byQuantity["count"])
	}
}

// Units are resolved per Card, from the actions it grants that add to the
// total: separate Cards keep their own units although
// their tools reuse a parameter name, and a Card whose contributors
// disagree, or include one with no unit, shows its total bare.
func TestAgentCardUnitsAreResolvedPerCard(t *testing.T) {
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	daily := []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 5000, Window: mandate.WindowCalendarDay}}
	for id, actions := range map[string][]string{
		"eur":      {"refund"},
		"usd":      {"payout"},
		"mixed":    {"refund", "payout"},
		"unitless": {"credit"},
		"partial":  {"credit", "refund"},
	} {
		var g []mandate.Grant
		for _, a := range actions {
			g = append(g, mandate.Grant{Action: a})
		}
		if err := store.Issue(context.Background(), mandate.Mandate{ID: id, TenantID: "t1", Subject: "claude", Domain: "gateway",
			Grants: g, Aggregates: daily}, "tester"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	action := func(name, unit string) string {
		y := "  - name: " + name + "\n    allowed_states: [requested]\n    required_parameters: [amount]\n    parameters:\n      - name: amount\n        type: int\n        min: 0\n"
		if unit != "" {
			y += "        unit: " + unit + "\n        scale: 2\n"
		}
		return y + "    required_permissions: [tools." + name + "]\n    risk: medium\n    approval: never\n    executor: cloud.proxy\n"
	}
	yaml := "domain: gateway\nentity: ToolCall\nevents: [TOOL_CALL_REQUESTED]\nstates: [requested]\ntransitions:\n  - on: TOOL_CALL_REQUESTED\n    from: \"\"\n    to: requested\nactions:\n" +
		action("refund", "EUR") + action("payout", "USD") + action("credit", "") +
		"permissions:\n  roles:\n    gateway_agent: [tools.refund, tools.payout, tools.credit]\n"
	h := NewMandatesHandler(store, stubDomainSource{yaml: yaml}, WithMandatesClock(func() time.Time {
		return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	}))
	mux := http.NewServeMux()
	h.Register(mux)
	rr := do(t, mux, agentKey("claude", ""), http.MethodGet, "/v1/me/card", nil)
	var got struct {
		Cards []struct {
			ID     string           `json:"id"`
			Limits []map[string]any `json:"limits"`
		} `json:"cards"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || rr.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rr.Code, rr.Body.String())
	}
	units := map[string]any{}
	for _, c := range got.Cards {
		if len(c.Limits) != 1 {
			t.Fatalf("%s limits = %v", c.ID, c.Limits)
		}
		units[c.ID] = c.Limits[0]["unit"]
	}
	want := map[string]any{"eur": "EUR", "usd": "USD", "mixed": nil, "unitless": nil, "partial": nil}
	for id, w := range want {
		if units[id] != w {
			t.Errorf("Card %s unit = %v, want %v", id, units[id], w)
		}
	}
}

// An answered hold says how its call ended at the tool, so an
// approval the tool refused does not read as done. A hold still waiting
// has nothing to say yet.
func TestExceptionsCarryTheToolOutcome(t *testing.T) {
	store := mandate.NewMemoryStore()
	outcomes := &outcomeMap{m: map[string]tooloutcome.Outcome{
		"tc-used": {Status: tooloutcome.RefusedByTool, Message: "Refund amount (€25.00) is greater than unrefunded amount on charge (€20.00)"},
	}}
	h := NewMandatesHandler(store, nil, WithMandatesToolOutcomes(outcomes))
	mux := http.NewServeMux()
	h.Register(mux)
	ctx := context.Background()
	at := time.Date(2026, 10, 8, 9, 34, 0, 0, time.UTC)
	for _, e := range []mandate.Exception{
		{ID: "exc-used", ProposalID: "tc-used", Subject: "support-demo", Action: "refund_order", Status: mandate.ExceptionConsumed, ResolvedAt: &at, ConsumedAt: &at},
		{ID: "exc-waiting", ProposalID: "tc-waiting", Subject: "support-demo", Action: "refund_order", Status: mandate.ExceptionHeld},
	} {
		e.TenantID, e.CreatedAt = "t1", at
		if err := store.SaveException(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	type view struct {
		ID          string               `json:"id"`
		ToolOutcome *tooloutcome.Outcome `json:"tool_outcome"`
	}
	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/card-exceptions", nil)
	var list struct {
		Exceptions []view `json:"exceptions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v %s", err, rr.Body.String())
	}
	for _, v := range list.Exceptions {
		switch v.ID {
		case "exc-used":
			if v.ToolOutcome == nil || v.ToolOutcome.Status != tooloutcome.RefusedByTool {
				t.Errorf("used approval: %+v", v.ToolOutcome)
			}
		case "exc-waiting":
			if v.ToolOutcome != nil {
				t.Errorf("waiting hold carries an outcome: %+v", v.ToolOutcome)
			}
		}
	}
	if strings.Join(outcomes.asked, ",") != "tc-used" {
		t.Errorf("asked about %v, want only the answered hold", outcomes.asked)
	}
	rr = do(t, mux, owner(), http.MethodGet, "/v1/me/card-exceptions/exc-used", nil)
	var one view
	if err := json.Unmarshal(rr.Body.Bytes(), &one); err != nil || one.ToolOutcome == nil || one.ToolOutcome.Status != tooloutcome.RefusedByTool {
		t.Errorf("one: %v %s", err, rr.Body.String())
	}
}

type outcomeMap struct {
	m     map[string]tooloutcome.Outcome
	asked []string
}

func (f *outcomeMap) Outcomes(_ context.Context, _ string, ids []string) (map[string]tooloutcome.Outcome, error) {
	f.asked = append(f.asked, ids...)
	out := map[string]tooloutcome.Outcome{}
	for _, id := range ids {
		if o, ok := f.m[id]; ok {
			out[id] = o
		}
	}
	return out, nil
}

// An agent reads where its own held calls stand, by the exception
// ids the gateway recorded with them. Another agent's exception, and one
// that does not exist, are left out the same way; only an agent key reads.
func TestAgentReadsItsOwnHolds(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	future := time.Now().Add(time.Hour)
	for _, e := range []mandate.Exception{
		{ID: "exc-waiting", ProposalID: "p1", Subject: "claude", Action: "refund", Status: mandate.ExceptionHeld, ExpiresAt: &future},
		{ID: "exc-approved", ProposalID: "p2", Subject: "claude", Action: "refund", Status: mandate.ExceptionApproved, ResolvedAt: &past},
		{ID: "exc-lapsed", ProposalID: "p3", Subject: "claude", Action: "refund", Status: mandate.ExceptionHeld, ExpiresAt: &past},
		{ID: "exc-theirs", ProposalID: "p4", Subject: "someone-else", Action: "refund", Status: mandate.ExceptionHeld, ExpiresAt: &future},
	} {
		e.TenantID, e.CreatedAt = "t1", past
		if err := store.SaveException(ctx, e); err != nil {
			t.Fatalf("seed %s: %v", e.ID, err)
		}
	}
	rr := do(t, mux, agentKey("claude", ""), http.MethodGet,
		"/v1/me/holds?ids=exc-waiting,exc-approved,exc-lapsed,exc-theirs,exc-missing,exc-waiting", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Holds []struct {
			ExceptionID string `json:"exception_id"`
			Status      string `json:"status"`
		} `json:"holds"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	status := map[string]string{}
	for _, h := range got.Holds {
		status[h.ExceptionID] = h.Status
	}
	want := map[string]string{"exc-waiting": "held", "exc-approved": "held", "exc-lapsed": "expired"}
	if len(got.Holds) != len(want) {
		t.Fatalf("holds = %+v, want %v", got.Holds, want)
	}
	for id, s := range want {
		if status[id] != s {
			t.Errorf("%s = %q, want %q", id, status[id], s)
		}
	}
	if rr := do(t, mux, owner(), http.MethodGet, "/v1/me/holds?ids=exc-waiting", nil); rr.Code != http.StatusForbidden {
		t.Errorf("owner read: %d, want 403", rr.Code)
	}
}

// outcomeAndArgs is a gateway record that also has held calls' arguments.
type outcomeAndArgs struct {
	outcomeMap
	args map[string]json.RawMessage
	err  error
}

func (f *outcomeAndArgs) Arguments(_ context.Context, _ string, ids []string) (map[string]json.RawMessage, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]json.RawMessage{}
	for _, id := range ids {
		if a, ok := f.args[id]; ok {
			out[id] = a
		}
	}
	return out, nil
}

// A held gateway call shows the arguments it will be sent with,
// so the person answering sees "order 70349", not KIFF's id for the
// call, with every digit of a number. A call with no
// arguments says so; one whose record could not be read is marked
// unavailable, never shown as a call without arguments. A hold from code
// and an answered hold carry neither.
func TestHeldExceptionsCarryTheCallsArguments(t *testing.T) {
	at := time.Date(2026, 10, 8, 21, 27, 0, 0, time.UTC)
	gw := mandate.Request{Domain: "gateway"}
	seed := func(t *testing.T, store *mandate.MemoryStore) {
		for _, e := range []mandate.Exception{
			{ID: "exc-used", ProposalID: "tc-used", Action: "get_order", Request: gw, Status: mandate.ExceptionConsumed, ResolvedAt: &at, ConsumedAt: &at},
			{ID: "exc-waiting", ProposalID: "tc-waiting", Action: "get_order", Request: gw, Status: mandate.ExceptionHeld},
			{ID: "exc-noargs", ProposalID: "tc-noargs", Action: "ping", Request: gw, Status: mandate.ExceptionHeld},
			{ID: "exc-gone", ProposalID: "tc-gone", Action: "get_order", Request: gw, Status: mandate.ExceptionHeld},
			{ID: "exc-code", ProposalID: "prop-1", Action: "REFUND_ORDER", Request: mandate.Request{Domain: "card-refund"}, Status: mandate.ExceptionHeld},
		} {
			e.TenantID, e.CreatedAt, e.Subject = "t1", at, "onboard-agent"
			if err := store.SaveException(context.Background(), e); err != nil {
				t.Fatal(err)
			}
		}
	}
	type view struct {
		ID          string                     `json:"id"`
		Arguments   map[string]json.RawMessage `json:"arguments"`
		Unavailable bool                       `json:"arguments_unavailable"`
	}
	list := func(t *testing.T, rec *outcomeAndArgs) map[string]view {
		store := mandate.NewMemoryStore()
		seed(t, store)
		h := NewMandatesHandler(store, nil, WithMandatesToolOutcomes(rec))
		mux := http.NewServeMux()
		h.Register(mux)
		rr := do(t, mux, owner(), http.MethodGet, "/v1/me/card-exceptions", nil)
		var out struct {
			Exceptions []view `json:"exceptions"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v %s", err, rr.Body.String())
		}
		m := map[string]view{}
		for _, v := range out.Exceptions {
			m[v.ID] = v
		}
		// The detail reads the same way.
		rr = do(t, mux, owner(), http.MethodGet, "/v1/me/card-exceptions/exc-waiting", nil)
		var one view
		if err := json.Unmarshal(rr.Body.Bytes(), &one); err != nil {
			t.Fatal(err)
		}
		if a, b := string(one.Arguments["account"]), string(m["exc-waiting"].Arguments["account"]); a != b || one.Unavailable != m["exc-waiting"].Unavailable {
			t.Errorf("detail %q/%v differs from list %q/%v", a, one.Unavailable, b, m["exc-waiting"].Unavailable)
		}
		return m
	}

	got := list(t, &outcomeAndArgs{args: map[string]json.RawMessage{
		"tc-waiting": json.RawMessage(`{"order_id":"70349","account":9007199254740993}`),
		"tc-noargs":  json.RawMessage(`{}`),
		"tc-used":    json.RawMessage(`{"order_id":"70300"}`),
	}})
	if w := got["exc-waiting"]; string(w.Arguments["order_id"]) != `"70349"` || string(w.Arguments["account"]) != "9007199254740993" || w.Unavailable {
		t.Errorf("held call: %+v", w)
	}
	if n := got["exc-noargs"]; n.Arguments == nil || len(n.Arguments) != 0 || n.Unavailable {
		t.Errorf("no arguments: %+v", n)
	}
	if g := got["exc-gone"]; g.Arguments != nil || !g.Unavailable {
		t.Errorf("no record: %+v", g)
	}
	for _, id := range []string{"exc-used", "exc-code"} {
		if v := got[id]; v.Arguments != nil || v.Unavailable {
			t.Errorf("%s carries arguments: %+v", id, v)
		}
	}

	got = list(t, &outcomeAndArgs{err: errors.New("db down")})
	if w := got["exc-waiting"]; w.Arguments != nil || !w.Unavailable {
		t.Errorf("failed read: %+v", w)
	}
	if v := got["exc-code"]; v.Unavailable {
		t.Errorf("a hold from code is not a failed gateway read: %+v", v)
	}
}

// The owner's statement says what a summed limit means too, from the
// Card's own domain, so the dashboard shows €200.00 rather than 20000.
// Without a readable domain the statement still answers, with bare numbers.
func TestStatementLimitsCarryTheAmountUnit(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		wantUnit   bool
	}{
		{"domain read", refundCentsYAML, true},
		{"domain unreadable", "not a domain", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := mandate.NewMemoryStore()
			store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
			if err := store.Issue(context.Background(), mandate.Mandate{ID: "mdt-gw", TenantID: "t1", Subject: "claude", Domain: "gateway",
				Grants: []mandate.Grant{{Action: "refund"}},
				Aggregates: []mandate.Aggregate{
					{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}, Limit: 20000, Window: mandate.WindowCalendarDay},
					{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 10, Window: mandate.WindowCalendarDay},
				}}, "tester"); err != nil {
				t.Fatalf("seed: %v", err)
			}
			h := NewMandatesHandler(store, stubDomainSource{yaml: tc.yaml}, WithMandatesClock(func() time.Time {
				return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
			}))
			mux := http.NewServeMux()
			h.Register(mux)
			rr := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/mdt-gw/statement", nil)
			var got struct {
				Limits []map[string]any `json:"limits"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || rr.Code != http.StatusOK {
				t.Fatalf("status %d, body %s", rr.Code, rr.Body.String())
			}
			byQuantity := map[string]map[string]any{}
			for _, l := range got.Limits {
				byQuantity[l["quantity"].(string)] = l
			}
			sum := byQuantity["sum(amount_cents)"]
			if sum == nil {
				t.Fatalf("no sum limit in %v", got.Limits)
			}
			if tc.wantUnit && (sum["unit"] != "EUR" || sum["scale"] != float64(2)) {
				t.Errorf("sum limit = %v, want unit EUR, scale 2", sum)
			}
			if _, ok := sum["unit"]; !tc.wantUnit && ok {
				t.Errorf("sum limit has a unit without a readable domain: %v", sum)
			}
			if _, ok := byQuantity["count"]["unit"]; ok {
				t.Errorf("count limit has a unit: %v", byQuantity["count"])
			}
		})
	}
}

// The Cards list says what its amounts mean too: every summed
// limit and per-action limit carries its unit, so a phone listing Cards
// shows €200.00, not 20000. With ?include=use each Card also carries its
// limits' use in the current window, the same lines as its statement, so
// the list needs no statement call per Card. An agent key still lists
// only the Cards it holds.
func TestCardsListCarriesUnitsAndUse(t *testing.T) {
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	sum := mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}
	for _, m := range []mandate.Mandate{
		{ID: "mdt-gw", TenantID: "t1", Subject: "claude", Domain: "gateway",
			Grants: []mandate.Grant{{Action: "refund", PerAction: []mandate.PerActionLimit{{Quantity: sum, Max: 15000}}}},
			Aggregates: []mandate.Aggregate{
				{Quantity: sum, Limit: 20000, Window: mandate.WindowCalendarDay},
				{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 10, Window: mandate.WindowCalendarDay},
			}},
		{ID: "mdt-other", TenantID: "t1", Subject: "other-agent", Domain: "gateway",
			Grants:     []mandate.Grant{{Action: "refund"}},
			Aggregates: []mandate.Aggregate{{Quantity: sum, Limit: 500, Window: mandate.WindowCalendarDay}}},
	} {
		if err := store.Issue(context.Background(), m, "tester"); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	h := NewMandatesHandler(store, stubDomainSource{yaml: refundCentsYAML}, WithMandatesClock(func() time.Time {
		return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	}))
	mux := http.NewServeMux()
	h.Register(mux)
	type card struct {
		ID         string           `json:"id"`
		Aggregates []map[string]any `json:"aggregates"`
		Grants     []struct {
			PerActionMax []map[string]any `json:"per_action_max"`
		} `json:"grants"`
		Limits []map[string]any `json:"limits"`
	}
	list := func(path string, ident auth.Identity) []card {
		t.Helper()
		rr := do(t, mux, ident, http.MethodGet, path, nil)
		var got struct {
			Mandates []card `json:"mandates"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || rr.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", path, rr.Code, rr.Body.String())
		}
		return got.Mandates
	}
	byID := func(cs []card) map[string]card {
		out := map[string]card{}
		for _, c := range cs {
			out[c.ID] = c
		}
		return out
	}

	plain := byID(list("/v1/me/mandates", owner()))["mdt-gw"]
	for _, a := range plain.Aggregates {
		switch a["quantity"] {
		case "sum(amount_cents)":
			if a["unit"] != "EUR" || a["scale"] != float64(2) {
				t.Errorf("summed limit = %v, want unit EUR, scale 2", a)
			}
		case "count":
			if _, ok := a["unit"]; ok {
				t.Errorf("count limit has a unit: %v", a)
			}
		}
	}
	if len(plain.Grants) != 1 || len(plain.Grants[0].PerActionMax) != 1 ||
		plain.Grants[0].PerActionMax[0]["unit"] != "EUR" || plain.Grants[0].PerActionMax[0]["scale"] != float64(2) {
		t.Errorf("per-action limit lacks its unit: %+v", plain.Grants)
	}
	if plain.Limits != nil {
		t.Error("the list carries use without ?include=use")
	}

	withUse := byID(list("/v1/me/mandates?include=use", owner()))["mdt-gw"]
	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/mdt-gw/statement", nil)
	var stmt struct {
		Limits []map[string]any `json:"limits"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &stmt); err != nil {
		t.Fatal(err)
	}
	gotUse, _ := json.Marshal(withUse.Limits)
	wantUse, _ := json.Marshal(stmt.Limits)
	if len(withUse.Limits) != 2 || string(gotUse) != string(wantUse) {
		t.Errorf("list use = %s\nstatement = %s", gotUse, wantUse)
	}
	for _, l := range withUse.Limits {
		if l["quantity"] == "sum(amount_cents)" && (l["unit"] != "EUR" || l["remaining"] != float64(20000) || l["window_started"] == nil) {
			t.Errorf("summed use line = %v", l)
		}
	}

	if got := list("/v1/me/mandates?include=use", agentKey("claude", "")); len(got) != 1 || got[0].ID != "mdt-gw" {
		t.Errorf("agent key lists %+v, want only its own Card", got)
	}
}
