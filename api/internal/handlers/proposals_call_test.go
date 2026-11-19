package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/permit"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
)

// kiff/tafs#167: a decision records what its call moves and what it is
// on, so the owner's record and the signed receipt can say "€150.00 on
// payment …". The gateway's contract declares, per tool, its amount (with
// unit and scale) and its resource argument.
const callFactsDomain = `domain: gateway
entity: ToolCall
events: [TOOL_CALL_REQUESTED]
states: [requested]
transitions:
  - on: TOOL_CALL_REQUESTED
    from: ""
    to: requested
actions:
  - name: stripe_refund
    allowed_states: [requested]
    required_parameters: [amount, charge]
    parameters:
      - name: amount
        type: int
        min: 0
        unit: EUR
        scale: 2
      - name: charge
        type: string
        min_length: 1
        max_length: 256
    required_permissions: [tools.stripe_refund]
    risk: medium
    approval: never
    executor: cloud.proxy
  - name: count_things
    allowed_states: [requested]
    required_parameters: [n]
    parameters:
      - name: n
        type: int
        min: 0
    required_permissions: [tools.count_things]
    risk: low
    approval: never
    executor: cloud.proxy
permissions:
  roles:
    gateway_agent: [tools.stripe_refund, tools.count_things]
`

func callFactsSetup(t *testing.T, opts ...handlers.ProposalsOption) (http.Handler, *cloudruntime.TenantRuntime, string) {
	t.Helper()
	ctx := context.Background()
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-call-facts", Status: tenant.StatusActive}
	if err := tenantStore.Create(ctx, tnt); err != nil {
		t.Fatal(err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, []byte(callFactsDomain)); err != nil {
		t.Fatal(err)
	}
	rt := cloudruntime.New(cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry()))
	h := handlers.NewProposalsHandler(rt, opts...)
	inner := http.NewServeMux()
	h.Register(inner)
	return withFakeIdentity(inner, auth.Identity{Tenant: tnt.ID, Subject: "subj", Roles: []string{"gateway_agent"}}), rt, tnt.ID
}

func TestProposals_DecisionRecordsAmountAndWhatItIsOn(t *testing.T) {
	t.Parallel()
	srv, rt, tenantID := callFactsSetup(t)
	drivePost(t, srv, "/v1/proposals/decide", map[string]any{
		"id": "tc-refund", "entity_id": "tc-refund", "entity_type": "ToolCall", "action_name": "stripe_refund",
		"actor_id": "agent", "parameters": map[string]any{"amount": 15000, "charge": "pi_QbstR"},
	})
	rec := decisionRecordFor(t, rt, tenantID)
	if rec == nil {
		t.Fatal("no proposal_decision audit record")
	}
	amount, _ := rec.Data["amount"].(map[string]any)
	if amount["value"] != "150.00" || amount["unit"] != "EUR" {
		t.Errorf("amount = %v, want 150.00 EUR", rec.Data["amount"])
	}
	if rec.Data["on_arg"] != "charge" || rec.Data["on_hash"] != handlers.ResourceHashForTest("pi_QbstR") {
		t.Errorf("on = %v %v", rec.Data["on_arg"], rec.Data["on_hash"])
	}
	for k, v := range rec.Data {
		if v == "pi_QbstR" {
			t.Fatalf("the resource value is recorded under %s", k)
		}
	}
}

// An integer with no unit in the gateway's domain is still its amount,
// as for a draft's Cards; the action names nothing it is on.
func TestProposals_DecisionRecordsABareAmount(t *testing.T) {
	t.Parallel()
	srv, rt, tenantID := callFactsSetup(t)
	drivePost(t, srv, "/v1/proposals/decide", map[string]any{
		"id": "tc-count", "entity_id": "tc-count", "entity_type": "ToolCall", "action_name": "count_things",
		"actor_id": "agent", "parameters": map[string]any{"n": 7},
	})
	rec := decisionRecordFor(t, rt, tenantID)
	if rec == nil {
		t.Fatal("no proposal_decision audit record")
	}
	amount, _ := rec.Data["amount"].(map[string]any)
	if amount["value"] != "7" || amount["unit"] != nil {
		t.Errorf("amount = %v, want a bare 7", rec.Data["amount"])
	}
	if _, ok := rec.Data["on_hash"]; ok {
		t.Errorf("on_hash = %v, want none", rec.Data["on_hash"])
	}
}

// The gateway tells KIFF the currency its read found. A call in
// another currency than its Card counts, or one the gateway couldn't
// check, is recorded as blocked, before any Card is checked, with its
// amount in the currency it was in; the gateway's word isn't kept as a
// parameter.
func TestProposals_ACallInAnotherCurrencyIsBlocked(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		unit, reason, value string
	}{
		{"usd", "amount_unit_mismatch", "USD"},
		{handlers.UnitUnverified, "amount_unit_unverified", "EUR"},
	} {
		srv, rt, tenantID := callFactsSetup(t)
		rec := drivePost(t, srv, "/v1/proposals/decide", map[string]any{
			"id": "tc-" + tc.unit, "entity_id": "tc-" + tc.unit, "entity_type": "ToolCall", "action_name": "stripe_refund", "actor_id": "agent",
			"parameters": map[string]any{"amount": 30000, "charge": "ch_1", handlers.GatewayUnitParam: tc.unit},
		})
		assertOutcome(t, rec, http.StatusOK, "blocked", []string{tc.reason})
		d := decisionRecordFor(t, rt, tenantID)
		if d == nil {
			t.Fatal("no decision recorded")
		}
		amount, _ := d.Data["amount"].(map[string]any)
		if amount["value"] != "300.00" || amount["unit"] != tc.value || d.Data["on_arg"] != "charge" {
			t.Errorf("%s: recorded %v", tc.unit, d.Data)
		}
	}
	// The same currency is decided as before.
	srv, _, _ := callFactsSetup(t)
	rec := drivePost(t, srv, "/v1/proposals/decide", map[string]any{
		"id": "tc-eur", "entity_id": "tc-eur", "entity_type": "ToolCall", "action_name": "stripe_refund", "actor_id": "agent",
		"parameters": map[string]any{"amount": 30000, "charge": "ch_1", handlers.GatewayUnitParam: "eur"},
	})
	if strings.Contains(rec.Body.String(), "amount_unit") {
		t.Fatalf("EUR: %s", rec.Body)
	}
}

// A call whose amount the gateway reads from its resource and
// couldn't is recorded as blocked, so the owner sees it in the record, and
// never counted. The gateway's word isn't kept as a parameter, and false
// from it is no word at all.
func TestProposals_ACallWhoseAmountCantBeReadIsBlocked(t *testing.T) {
	t.Parallel()
	srv, rt, tenantID := callFactsSetup(t)
	rec := drivePost(t, srv, "/v1/proposals/decide", map[string]any{
		"id": "tc-unread", "entity_id": "tc-unread", "entity_type": "ToolCall", "action_name": "stripe_refund", "actor_id": "agent",
		"parameters": map[string]any{"charge": "ch_1", handlers.GatewayAmountUnverifiedParam: true},
	})
	assertOutcome(t, rec, http.StatusOK, "blocked", []string{"amount_unverified"})
	d := decisionRecordFor(t, rt, tenantID)
	if d == nil {
		t.Fatal("no decision recorded")
	}
	if d.Data["on_arg"] != "charge" || d.Data["amount"] != nil {
		t.Errorf("recorded %v", d.Data)
	}
	// A relay tool's action has a permit target, so a call decided without a
	// permit request is refused permit_required, but the record-only refusal
	// still reaches its blocked decision, with no permit and nothing counted
	// (codex-assistant's review of).
	targets := &anyTargets{permit.Target{Domain: "gateway", Action: "stripe_refund", Tool: "refund", Audience: "https://planque.example/v1/relay",
		AmountArg: "amount", ResourceArg: "charge", AmountLookedUp: true}}
	srv, rt, tenantID = callFactsSetup(t, handlers.WithProposalsPermitTargets(targets))
	rec = drivePost(t, srv, "/v1/proposals/decide", map[string]any{
		"id": "tc-relay", "entity_id": "tc-relay", "entity_type": "ToolCall", "action_name": "stripe_refund", "actor_id": "agent",
		"parameters": map[string]any{"charge": "ch_1", handlers.GatewayAmountUnverifiedParam: true},
	})
	assertOutcome(t, rec, http.StatusOK, "blocked", []string{"amount_unverified"})
	if strings.Contains(rec.Body.String(), `"permit":"`) || decisionRecordFor(t, rt, tenantID) == nil {
		t.Fatalf("relay: %s", rec.Body)
	}
	// Without the gateway's word, the same call still needs its permit.
	rec = drivePost(t, srv, "/v1/proposals/decide", map[string]any{
		"id": "tc-relay-2", "entity_id": "tc-relay-2", "entity_type": "ToolCall", "action_name": "stripe_refund", "actor_id": "agent",
		"parameters": map[string]any{"amount": 30000, "charge": "ch_1"},
	})
	if !strings.Contains(rec.Body.String(), "permit_required") {
		t.Fatalf("relay without the word: %s", rec.Body)
	}
	srv, _, _ = callFactsSetup(t)
	rec = drivePost(t, srv, "/v1/proposals/decide", map[string]any{
		"id": "tc-read", "entity_id": "tc-read", "entity_type": "ToolCall", "action_name": "stripe_refund", "actor_id": "agent",
		"parameters": map[string]any{"amount": 30000, "charge": "ch_1", handlers.GatewayAmountUnverifiedParam: false},
	})
	if strings.Contains(rec.Body.String(), "amount_unverified") || strings.Contains(rec.Body.String(), handlers.GatewayAmountUnverifiedParam) {
		t.Fatalf("false: %s", rec.Body)
	}
}

// anyTargets answers every tenant with its one target.
type anyTargets struct{ t permit.Target }

func (a *anyTargets) Get(_ context.Context, _, dom, action string) (permit.Target, bool, error) {
	return a.t, dom == a.t.Domain && action == a.t.Action, nil
}
func (a *anyTargets) Replace(context.Context, string, []permit.Target, string, time.Time) error {
	return nil
}
func (a *anyTargets) List(context.Context, string) ([]permit.Target, error) { return nil, nil }

// kiff/tafs#464 item 11: a call refused because its Card has no limit in its
// unit opens one ask for the owner, a card_unit exception, and alerts once
// as a hold does. Each refused repeat is still decided and recorded, and
// counts on the ask without alerting again; a call in the Card's own unit
// leaves it open.
func TestProposals_ACallInAnotherCurrencyAsksTheOwnerOnce(t *testing.T) {
	t.Parallel()
	asks := mandate.NewMemoryStore()
	al := &recordingAlerter{}
	srv, rt, tenantID := callFactsSetup(t, handlers.WithProposalsCardUnitAsks(asks), handlers.WithProposalsAlerter(al))
	decide := func(id, unit string) {
		t.Helper()
		rec := drivePost(t, srv, "/v1/proposals/decide", map[string]any{
			"id": id, "entity_id": id, "entity_type": "ToolCall", "action_name": "stripe_refund", "actor_id": "agent",
			"parameters": map[string]any{"amount": 30000, "charge": "ch_1", handlers.GatewayUnitParam: unit},
		})
		if unit != "eur" {
			assertOutcome(t, rec, http.StatusOK, "blocked", []string{"amount_unit_mismatch"})
		}
	}
	for _, id := range []string{"tc-u1", "tc-u2", "tc-u3"} {
		decide(id, "usd")
	}
	held, err := asks.Exceptions(context.Background(), tenantID, mandate.ExceptionHeld)
	if err != nil || len(held) != 1 {
		t.Fatalf("asks: %d %v", len(held), err)
	}
	e := held[0]
	if e.KindOf() != mandate.KindCardUnit || e.Count != 3 || e.Action != "stripe_refund" || e.Subject != "agent" ||
		e.Breaches[0].Unit != "USD" || e.Breaches[0].CardUnit != "EUR" || e.ProposalID != "tc-u1" {
		t.Fatalf("ask: %+v", e)
	}
	// Every refusal keeps its decision.
	if n := decisionCount(t, rt, tenantID); n != 3 {
		t.Fatalf("%d decisions recorded, want 3", n)
	}
	// One owner_required alert, naming the ask, among the blocked ones.
	asked := 0
	for _, ev := range al.all() {
		if ev.Outcome == "owner_required" {
			asked++
			if ev.Hold == nil || ev.Hold.ID != e.ID || ev.ProposalID != "tc-u1" {
				t.Fatalf("the ask's alert: %+v", ev)
			}
		}
	}
	if asked != 1 {
		t.Fatalf("%d owner_required alerts, want 1", asked)
	}
	// a follow-up: the same call retried, still in USD, gets its
	// recorded refusal for the unit (before, the gateway's unit parameter
	// made every retry read as another request: idempotency_key_reuse). A
	// retry is the same call: the ask stays held, isn't counted again or
	// alerted again, and no second decision is recorded.
	decide("tc-u1", "usd")
	if got, _ := asks.Exception(context.Background(), tenantID, e.ID); got.Status != mandate.ExceptionHeld || got.Count != 3 || got.KindOf() != mandate.KindCardUnit {
		t.Fatalf("after a retry: %s count %d", got.Status, got.Count)
	}
	if n := decisionCount(t, rt, tenantID); n != 3 {
		t.Fatalf("%d decisions after a retry, want 3", n)
	}
	retried := 0
	for _, ev := range al.all() {
		if ev.Outcome == "owner_required" {
			retried++
		}
	}
	if retried != 1 {
		t.Fatalf("%d owner_required alerts after a retry, want 1", retried)
	}
	// A call in the Card's own unit says nothing about USD: the ask stays.
	// It closes when a USD call is no longer refused (the store's test).
	decide("tc-u4", "eur")
	if got, _ := asks.Exception(context.Background(), tenantID, e.ID); got.Status != mandate.ExceptionHeld {
		t.Fatalf("after a EUR call: %s", got.Status)
	}
}

// decisionCount is how many proposal decisions the tenant recorded.
func decisionCount(t *testing.T, rt *cloudruntime.TenantRuntime, tenantID string) int {
	t.Helper()
	resolved, err := rt.For(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	records, err := resolved.Audit.Query(context.Background(), kiffaudit.Filter{Kind: kiffaudit.Kind("proposal_decision")})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	return len(records)
}

// A gateway call carries its unit; the decision is recorded without it. A
// retry of the same call (the same id, the same unit) gets its recorded
// decision, never idempotency_key_reuse, and records no second one.
func TestProposals_AGatewayRetryWithItsUnitGetsItsRecordedDecision(t *testing.T) {
	t.Parallel()
	srv, rt, tenantID := callFactsSetup(t)
	call := func(id, unit string) *httptest.ResponseRecorder {
		t.Helper()
		return drivePost(t, srv, "/v1/proposals/decide", map[string]any{
			"id": id, "entity_id": id, "entity_type": "ToolCall", "action_name": "stripe_refund", "actor_id": "agent",
			"parameters": map[string]any{"amount": 3000, "charge": "ch_1", handlers.GatewayUnitParam: unit},
		})
	}
	outcome := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		var got map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		o, _ := got["outcome"].(string)
		return o
	}
	for _, c := range []struct{ id, unit string }{{"tc-r1", "eur"}, {"tc-r2", "usd"}} {
		first := outcome(call(c.id, c.unit))
		if again := outcome(call(c.id, c.unit)); again != first || again == "invalid" {
			t.Fatalf("%s in %s: %s, then %s", c.id, c.unit, first, again)
		}
	}
	if n := decisionCount(t, rt, tenantID); n != 2 {
		t.Fatalf("%d decisions recorded, want 2: a retry records none", n)
	}
}

// A held call decided again once the owner answers is the same proposal:
// the decision store refusing its second record is no failure, and no
// warning. Any other store error still is.
func TestAProposalAlreadyRecordedIsNoFailure(t *testing.T) {
	dup := fmt.Errorf("insert decision: %w", &pgconn.PgError{Code: "23505", ConstraintName: "kiff_decisions_pkey"})
	if !handlers.AlreadyRecordedForTest(dup) {
		t.Fatal("a duplicate proposal record read as a failure")
	}
	for _, err := range []error{
		fmt.Errorf("insert decision: %w", &pgconn.PgError{Code: "23505", ConstraintName: "another_key"}),
		fmt.Errorf("insert decision: %w", &pgconn.PgError{Code: "57014"}),
		errors.New("duplicate key value violates unique constraint \"kiff_decisions_pkey\""),
	} {
		if handlers.AlreadyRecordedForTest(err) {
			t.Fatalf("%v read as already recorded", err)
		}
	}
}
