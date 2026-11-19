package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/agentdrafts"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/mandate"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
)

// The account's gateway domain as the gateway applies it (gateway.Contract):
// one ToolCall state, one action per tool, each requiring a Card.
const trialGatewayDomain = `domain: gateway
entity: ToolCall
events: [TOOL_CALL_REQUESTED]
states: [requested]
transitions:
  - on: TOOL_CALL_REQUESTED
    from: ""
    to: requested
actions:
  - name: refund_payment
    allowed_states: [requested]
    required_parameters: [amount]
    parameters:
      - name: amount
        type: int
        min: 0
    required_permissions: [tools.refund_payment]
    risk: medium
    approval: never
    executor: cloud.proxy
    requires_authority: true
permissions:
  roles:
    gateway_agent: [tools.refund_payment]
`

type trialRig struct {
	serve  func(auth.Identity) http.Handler
	store  *mandate.MemoryStore
	drafts *agentdrafts.MemoryStore
	tnt    string
	rt     *cloudruntime.TenantRuntime
}

// newTrialRig is an account with the gateway domain and one Card for
// refund-agent: refunds of up to 5,000 each and 10,000 a day, and work
// outside it comes home to its owner.
func newTrialRig(t *testing.T) trialRig {
	t.Helper()
	ctx := context.Background()
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-trial", Status: tenant.StatusActive}
	if err := tenantStore.Create(ctx, tnt); err != nil {
		t.Fatal(err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, []byte(trialGatewayDomain)); err != nil {
		t.Fatal(err)
	}
	rt := cloudruntime.New(cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry()))
	amount := mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}
	store := mandate.NewMemoryStore()
	if err := store.Issue(ctx, mandate.Mandate{
		ID: "card-refunds", TenantID: tnt.ID, Subject: "refund-agent", Domain: "gateway", OnExceed: mandate.OnExceedOwner,
		Grants:     []mandate.Grant{{Action: "refund_payment", PerAction: []mandate.PerActionLimit{{Quantity: amount, Max: 5000}}}},
		Aggregates: []mandate.Aggregate{{Quantity: amount, Limit: 10000, Window: mandate.WindowCalendarDay}},
		ValidFrom:  time.Now().Add(-time.Hour),
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	drafts := agentdrafts.NewMemoryStore()
	h := handlers.NewProposalsHandler(rt, handlers.WithProposalsMandates(mandate.NewAuthorizer(store, nil, nil)),
		handlers.WithProposalsAgentDrafts(drafts))
	mux := http.NewServeMux()
	h.Register(mux)
	return trialRig{
		serve: func(id auth.Identity) http.Handler {
			id.Tenant = tnt.ID
			return withFakeIdentity(mux, id)
		},
		store:  store,
		drafts: drafts,
		tnt:    tnt.ID,
		rt:     rt,
	}
}

// gatewayKey is a gateway key bound to agent ("": bound to none).
func gatewayKey(agent, env string) auth.Identity {
	return auth.Identity{Subject: "key-g", Method: auth.MethodAPIKey, APIKey: true,
		Roles: []string{auth.RoleGatewayAgent}, AgentID: agent, Environment: env}
}

type trialAnswer struct {
	Decision string   `json:"decision"`
	Reasons  []string `json:"reasons"`
	Message  string   `json:"message"`
}

func (r trialRig) try(t *testing.T, id auth.Identity, body map[string]any) (int, trialAnswer) {
	t.Helper()
	rec := drivePost(t, r.serve(id), "/v1/gateway/trial", body)
	var a trialAnswer
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	return rec.Code, a
}

func refundTrial(amount int) map[string]any {
	return map[string]any{"action_name": "refund_payment", "parameters": map[string]any{"amount": amount}, "draft": "live", "run": "run-1"}
}

// Invariant: a trial's writes through the gateway get the decisions the
// same queue of real calls would get from the agent's Cards, each counting
// the trial's earlier allowed writes, and KIFF records nothing real for
// them: no draw, no held call, nothing for the owner to answer.
// Category: authority
// Attack: an agent key sends trial calls inside and outside its Card, to use up the Card's total or fill the owner's queue without ever calling the tool
// Refused: each trial call is answered allowed, held or refused as the same real queue would be, and the Card's draws and held calls stay empty
func TestGatewayTrialDecidesAndRecordsNothing(t *testing.T) {
	t.Parallel()
	r := newTrialRig(t)
	agent := gatewayKey("refund-agent", "")
	// 10,000 a day, 5,000 a call: three refunds of 4,000 → allowed,
	// allowed, held, as a live queue would answer them.
	for i, want := range []string{"allowed", "allowed", "held"} {
		if code, a := r.try(t, agent, refundTrial(4000)); code != http.StatusOK || a.Decision != want {
			t.Fatalf("refund %d in the trial: %d %+v, want %s", i+1, code, a, want)
		}
	}
	// Another trial run starts from the Card's real usage.
	other := refundTrial(9000)
	other["run"] = "run-2"
	if code, a := r.try(t, agent, other); code != http.StatusOK || a.Decision != "held" || len(a.Reasons) != 1 || a.Reasons[0] != "mandate_owner_required" {
		t.Fatalf("outside a Card that comes home: %d %+v", code, a)
	}
	fresh := refundTrial(4000)
	fresh["run"] = "run-3"
	if _, a := r.try(t, agent, fresh); a.Decision != "allowed" {
		t.Fatalf("a fresh trial run: %+v", a)
	}
	draws, err := r.store.Draws(context.Background(), r.tnt, "card-refunds", time.Now().Add(-24*time.Hour))
	if err != nil || len(draws) != 0 {
		t.Fatalf("a trial drew on the Card: %+v, %v", draws, err)
	}
	if es, _ := r.store.Exceptions(context.Background(), r.tnt, ""); len(es) != 0 {
		t.Fatalf("a trial held a call for the owner: %+v", es)
	}
	// The contract still applies: a negative amount is refused before any Card.
	if _, a := r.try(t, agent, refundTrial(-1)); a.Decision != "refused" {
		t.Fatalf("negative amount: %+v", a)
	}
}

func TestGatewayTrialRefusals(t *testing.T) {
	t.Parallel()
	r := newTrialRig(t)
	agent := gatewayKey("refund-agent", "")
	if _, a := r.try(t, gatewayKey("other-agent", ""), refundTrial(100)); a.Decision != "refused" || a.Reasons[0] != "no_mandate_issued" {
		t.Errorf("an agent without a Card: %+v", a)
	}
	if _, a := r.try(t, agent, map[string]any{"action_name": "delete_customer", "parameters": map[string]any{}, "draft": "live", "run": "run-1"}); a.Decision != "refused" || a.Reasons[0] != "unknown_action" {
		t.Errorf("a tool that isn't connected: %+v", a)
	}
	body := refundTrial(100)
	body["draft"] = "something"
	if code, _ := r.try(t, agent, body); code != http.StatusBadRequest {
		t.Errorf("a draft that is neither: %d", code)
	}
	// Without its run, a trial's earlier writes can't be counted.
	noRun := refundTrial(100)
	delete(noRun, "run")
	if _, a := r.try(t, agent, noRun); a.Decision != "refused" || a.Reasons[0] != "trial_run_missing" {
		t.Errorf("a trial call without its run: %+v", a)
	}
}

// Only a key bound to an agent asks: the trial is decided for that agent.
func TestGatewayTrialNeedsAnAgentKey(t *testing.T) {
	t.Parallel()
	r := newTrialRig(t)
	unbound := gatewayKey("", "")
	if code, _ := r.try(t, unbound, refundTrial(100)); code != http.StatusForbidden {
		t.Errorf("unbound key: %d", code)
	}
	if code, _ := r.try(t, auth.Identity{Subject: "alice", Roles: []string{"tenant_owner"}}, refundTrial(100)); code != http.StatusForbidden {
		t.Errorf("a person: %d", code)
	}
}

// revision is a draft for refund-agent that revises its Card: refunds of up
// to 9,000 each and 20,000 a day.
func (r trialRig) revision(t *testing.T, id, agent string, status agentdrafts.Status, expires time.Time) {
	t.Helper()
	cards := `[{"id":"card-refunds","subject":"` + agent + `","domain":"gateway","on_exceed":"owner",` +
		`"grants":[{"action":"refund_payment","per_action_max":[{"quantity":"sum(amount)","max":9000}]}],` +
		`"aggregates":[{"quantity":"sum(amount)","limit":20000,"window":"calendar_day"}]}]`
	if err := r.drafts.Create(context.Background(), agentdrafts.D{ID: id, TenantID: r.tnt, AgentID: agent, Cards: json.RawMessage(cards),
		Version: "v1", Status: status, CreatedAt: time.Now(), ExpiresAt: expires, Revision: true,
		Revises: []agentdrafts.Revised{{ID: "card-refunds", Revision: 1}}}); err != nil {
		t.Fatal(err)
	}
}

// kiff/tafs#107, part C: a trial of a version that brings a Card revision is
// decided on the Cards that revision would leave, with what they used so
// far, and recorded nowhere real. The draft must be the agent's own, still
// waiting for its owner.
func TestGatewayTrialAgainstADraftsCards(t *testing.T) {
	t.Parallel()
	r := newTrialRig(t)
	agent := gatewayKey("refund-agent", "")
	r.revision(t, "drf_rev", "refund-agent", agentdrafts.Draft, time.Now().Add(time.Hour))
	live := refundTrial(9000)
	if _, a := r.try(t, agent, live); a.Decision != "held" {
		t.Fatalf("9,000 on the live Card: %+v", a)
	}
	withDraft := refundTrial(9000)
	withDraft["draft"], withDraft["run"] = "drf_rev", "run-2"
	if _, a := r.try(t, agent, withDraft); a.Decision != "allowed" {
		t.Fatalf("9,000 on the revised Card: %+v", a)
	}
	if draws, _ := r.store.Draws(context.Background(), r.tnt, "card-refunds", time.Now().Add(-24*time.Hour)); len(draws) != 0 {
		t.Fatalf("a trial drew: %+v", draws)
	}
	r.revision(t, "drf_other", "other-agent", agentdrafts.Draft, time.Now().Add(time.Hour))
	r.revision(t, "drf_old", "refund-agent", agentdrafts.Draft, time.Now().Add(-time.Minute))
	r.revision(t, "drf_done", "refund-agent", agentdrafts.Confirmed, time.Now().Add(time.Hour))
	for draft, reason := range map[string]string{"drf_other": "trial_draft_other_agent", "drf_old": "trial_draft_expired",
		"drf_done": "trial_draft_not_waiting", "drf_none": "trial_draft_not_found"} {
		body := refundTrial(100)
		body["draft"] = draft
		if _, a := r.try(t, agent, body); a.Decision != "refused" || len(a.Reasons) == 0 || a.Reasons[0] != reason {
			t.Errorf("%s: %+v, want %s", draft, a, reason)
		}
	}
}

// A trial past a draft's Card decides as that Card says (kiff/tafs#138): held
// for the owner, or refused, as confirming the draft would leave it. Before,
// the draft's policy wasn't read, and every Card was decided as refuse.
func TestGatewayTrialPastADraftsCardFollowsItsPolicy(t *testing.T) {
	t.Parallel()
	r := newTrialRig(t)
	agent := gatewayKey("refund-agent", "")
	for id, policy := range map[string]string{"drf_owner": `"on_exceed":"owner",`, "drf_refuse": `"on_exceed":"refuse",`, "drf_keep": ``} {
		cards := `[{"id":"card-refunds","subject":"refund-agent","domain":"gateway",` + policy +
			`"grants":[{"action":"refund_payment","per_action_max":[{"quantity":"sum(amount)","max":9000}]}],` +
			`"aggregates":[{"quantity":"sum(amount)","limit":20000,"window":"calendar_day"}]}]`
		if err := r.drafts.Create(context.Background(), agentdrafts.D{ID: id, TenantID: r.tnt, AgentID: "refund-agent", Cards: json.RawMessage(cards),
			Version: "v1", Status: agentdrafts.Draft, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), Revision: true,
			Revises: []agentdrafts.Revised{{ID: "card-refunds", Revision: 1}}}); err != nil {
			t.Fatal(err)
		}
	}
	// The live Card holds for the owner, so a draft that keeps its policy does too.
	for draft, want := range map[string]string{"drf_owner": "held", "drf_refuse": "refused", "drf_keep": "held"} {
		body := refundTrial(9500)
		body["draft"], body["run"] = draft, "run-"+draft
		if _, a := r.try(t, agent, body); a.Decision != want {
			t.Errorf("9,500 past %s's Card: %+v, want %s", draft, a, want)
		}
	}
}
