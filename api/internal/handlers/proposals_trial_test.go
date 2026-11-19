package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"

	"github.com/kiff/cloud/api/internal/agentdrafts"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/mandate"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
)

// A trial its owner started (kiff/tafs#173), through the real decide path:
// refund-agent's live Card allows refunds of up to 5,000; the version being
// tried, drf_try, holds a trial Card for up to 9,000.

type sentTrialRig struct {
	serve   func(auth.Identity) http.Handler
	store   *mandate.MemoryStore
	drafts  *agentdrafts.MemoryStore
	rt      *cloudruntime.TenantRuntime
	tenants *tenant.MemoryStore
	tnt     string
}

func newSentTrialRig(t *testing.T, extra ...handlers.ProposalsOption) sentTrialRig {
	t.Helper()
	ctx := context.Background()
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-sent-trial", Status: tenant.StatusActive}
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
	issue := func(id, subject string, max int64) {
		if err := store.Issue(ctx, mandate.Mandate{
			ID: id, TenantID: tnt.ID, Subject: subject, Domain: "gateway", OnExceed: mandate.OnExceedOwner,
			Grants:     []mandate.Grant{{Action: "refund_payment", PerAction: []mandate.PerActionLimit{{Quantity: amount, Max: max}}}},
			Aggregates: []mandate.Aggregate{{Quantity: amount, Limit: 2 * max, Window: mandate.WindowCalendarDay}},
			ValidFrom:  time.Now().Add(-time.Hour),
		}, "owner"); err != nil {
			t.Fatal(err)
		}
	}
	issue("card-refunds", "refund-agent", 5000)
	issue("card-refunds~trial-t1", mandate.TrialSubject("refund-agent"), 9000)
	drafts := agentdrafts.NewMemoryStore()
	if err := drafts.Create(ctx, agentdrafts.D{ID: "drf_try", TenantID: tnt.ID, AgentID: "refund-agent", Version: "v1",
		Status: agentdrafts.Trying, Revision: true, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
		Trial: &agentdrafts.Trial{CardIDs: []string{"card-refunds~trial-t1"}, StartedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	h := handlers.NewProposalsHandler(rt, append([]handlers.ProposalsOption{handlers.WithProposalsMandates(mandate.NewAuthorizer(store, nil, nil)),
		handlers.WithProposalsAgentDrafts(drafts), handlers.WithProposalsTenantStore(tenantStore)}, extra...)...)
	mux := http.NewServeMux()
	h.Register(mux)
	return sentTrialRig{
		serve: func(id auth.Identity) http.Handler {
			id.Tenant = tnt.ID
			return withFakeIdentity(mux, id)
		},
		store: store, drafts: drafts, rt: rt, tenants: tenantStore, tnt: tnt.ID,
	}
}

type decided struct {
	Outcome string   `json:"outcome"`
	Reasons []string `json:"reasons"`
}

// call records the gateway call's TOOL_CALL_REQUESTED, then decides it.
func (r sentTrialRig) call(t *testing.T, id auth.Identity, call string, amount int, draft string) (int, decided) {
	t.Helper()
	resolved, err := r.rt.For(context.Background(), r.tnt)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = resolved.IngestRaw(context.Background(), kiffadapter.RawInput{ID: "evt-" + call, Adapter: "cloud", Type: "TOOL_CALL_REQUESTED",
		Source: "kiff-gateway", ReceivedAt: time.Now().UTC(), EntityID: call, EntityType: "ToolCall", ActorID: id.AgentID, Payload: map[string]any{}})
	body := map[string]any{"id": call, "entity_id": call, "entity_type": "ToolCall", "action_name": "refund_payment",
		"actor_id": id.AgentID, "parameters": map[string]any{"amount": amount}}
	if draft != "" {
		body["trial_draft"] = draft
	}
	rec := drivePost(t, r.serve(id), "/v1/proposals/decide", body)
	var d decided
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	return rec.Code, d
}

func (r sentTrialRig) used(t *testing.T, card string) int64 {
	t.Helper()
	n, err := r.store.Used(context.Background(), r.tnt, card, "sum(amount)", time.Now().Add(-24*time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Invariant: a call made under a trial its owner started is decided on the
// trial's Cards alone, drawing on them, and recorded under the trial; a call
// without it never sees them. Once the trial ends, its calls, held ones
// included, are refused.
// Category: authority
// Attack: an agent names a trial to get the version's higher limit for its live calls, names another agent's trial, or keeps a held trial call to be approved after the trial ended
// Refused: only the key of the trial's own agent can name it; live calls get the live Card; after the end, the trial's calls are refused with trial_ended
func TestDecideATrialsCallOnItsTrialCards(t *testing.T) {
	t.Parallel()
	r := newSentTrialRig(t)
	agent := gatewayKey("refund-agent", "")
	if code, d := r.call(t, agent, "tc-trial", 9000, "drf_try"); code != http.StatusOK || d.Outcome != "allowed" {
		t.Fatalf("9,000 in the trial: %d %+v", code, d)
	}
	if r.used(t, "card-refunds~trial-t1") != 9000 || r.used(t, "card-refunds") != 0 {
		t.Fatalf("drawn: trial %d, live %d", r.used(t, "card-refunds~trial-t1"), r.used(t, "card-refunds"))
	}
	resolved, _ := r.rt.For(context.Background(), r.tnt)
	records, _ := resolved.Audit.Query(context.Background(), kiffaudit.Filter{})
	var recorded bool
	for _, rec := range records {
		if rec.Kind == kiffaudit.Kind("proposal_decision") && rec.Data["proposal_id"] == "tc-trial" {
			recorded = rec.Data["trial_draft"] == "drf_try"
		}
	}
	if !recorded {
		t.Fatal("the trial's call isn't recorded under its trial")
	}
	// The same amount without the trial is the live Card's to decide.
	if _, d := r.call(t, agent, "tc-live", 9000, ""); d.Outcome != "owner_required" {
		t.Fatalf("9,000 live: %+v", d)
	}
	// Only the trial's own agent names it.
	rec := drivePost(t, r.serve(gatewayKey("", "")), "/v1/proposals/decide", map[string]any{"id": "tc-unbound", "entity_id": "tc-unbound",
		"entity_type": "ToolCall", "action_name": "refund_payment", "actor_id": "refund-agent", "parameters": map[string]any{"amount": 100},
		"trial_draft": "drf_try"})
	var unbound decided
	_ = json.Unmarshal(rec.Body.Bytes(), &unbound)
	if rec.Code != http.StatusBadRequest || len(unbound.Reasons) == 0 || unbound.Reasons[0] != "trial_needs_agent_key" {
		t.Fatalf("an unbound key names refund-agent's trial: %d %+v", rec.Code, unbound)
	}
	if _, d := r.call(t, gatewayKey("other-agent", ""), "tc-other", 100, "drf_try"); d.Outcome != "blocked" || d.Reasons[0] != "trial_draft_not_found" {
		t.Fatalf("another agent names the trial: %+v", d)
	}
	// Past the trial Card: held for the owner, on the trial Card.
	if _, d := r.call(t, agent, "tc-held", 9500, "drf_try"); d.Outcome != "owner_required" {
		t.Fatalf("9,500 in the trial: %+v", d)
	}
	if _, err := r.drafts.EndTrial(context.Background(), r.tnt, "drf_try", agentdrafts.EndedBack, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The held call asked again as it was, and a new one.
	for call, amount := range map[string]int{"tc-held": 9500, "tc-after": 100} {
		if _, d := r.call(t, agent, call, amount, "drf_try"); d.Outcome != "blocked" || d.Reasons[0] != "trial_ended" {
			t.Fatalf("%s after the trial ended: %+v", call, d)
		}
	}
}
