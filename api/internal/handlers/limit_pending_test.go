package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

// refusedOutcomes answers how each call ended at the tool, and trusted
// no-effect evidence only where the test gives it.
type refusedOutcomes struct {
	outcomes map[string]tooloutcome.Outcome
	evidence map[string]mandate.NoEffectEvidence
}

func (f *refusedOutcomes) Outcomes(_ context.Context, _ string, ids []string) (map[string]tooloutcome.Outcome, error) {
	out := map[string]tooloutcome.Outcome{}
	for _, id := range ids {
		if o, ok := f.outcomes[id]; ok {
			out[id] = o
		}
	}
	return out, nil
}

func (f *refusedOutcomes) NoEffect(_ context.Context, _ string, proposal string) (mandate.NoEffectEvidence, bool, error) {
	e, ok := f.evidence[proposal]
	return e, ok, nil
}

// The invariant: a call the tool refused, with only its error text, still
// counts toward a total. A vendor's words are never evidence that nothing
// moved. The total says so, as pending, until trusted evidence releases it.
func TestARefusedCallWithOnlyItsErrorTextStillCountsAndShowsAsPending(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 31, 14, 44, 0, 0, time.UTC)
	s := mandate.NewMemoryStore()
	s.SetClock(func() time.Time { return now })
	m := mandate.Mandate{TenantID: "t1", ID: "save-cancels", Subject: "agent-a", Grants: []mandate.Grant{{Action: "create_coupon"}}, Aggregates: []mandate.Aggregate{
		{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_off"}, Limit: 10000, Window: mandate.WindowCalendarDay},
		{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 50, Window: mandate.WindowCalendarDay},
	}}
	if err := s.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(s, nil, func() time.Time { return now })
	for _, op := range []string{"refused", "retried"} {
		if d, err := a.Authorize(ctx, "t1", op, mandate.Request{Subject: "agent-a", Action: "create_coupon", Parameters: map[string]int64{"amount_off": 4900}}); err != nil || !d.Allowed() {
			t.Fatal(op, d, err)
		}
	}
	f := &refusedOutcomes{
		outcomes: map[string]tooloutcome.Outcome{
			// The vendor's own words say nothing was made: still not evidence.
			"refused": {Status: tooloutcome.RefusedByTool, Message: "Invalid currency: no coupon was created", At: now.Add(time.Second)},
			"retried": {Status: tooloutcome.Done, At: now.Add(2 * time.Second)},
		},
		evidence: map[string]mandate.NoEffectEvidence{},
	}
	h := NewMandatesHandler(s, nil, WithMandatesToolOutcomes(f), WithMandatesClock(func() time.Time { return now.Add(time.Minute) }))
	mux := http.NewServeMux()
	h.Register(mux)
	limits := func() (sum, count map[string]any) {
		t.Helper()
		rr := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/save-cancels/statement", nil)
		var raw struct {
			Limits []map[string]any `json:"limits"`
		}
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &raw) != nil {
			t.Fatal(rr.Code, rr.Body.String())
		}
		for _, l := range raw.Limits {
			switch l["quantity"] {
			case "sum(amount_off)":
				sum = l
			case "count":
				count = l
			}
		}
		if sum == nil || count == nil {
			t.Fatal(raw.Limits)
		}
		return sum, count
	}

	sum, count := limits()
	if sum["used"] != float64(9800) || sum["remaining"] != float64(200) {
		t.Fatal("a refused call must still count", sum)
	}
	if sum["pending"] != float64(4900) || sum["pending_calls"] != float64(1) {
		t.Fatal("the refused call shows as pending", sum)
	}
	if text, _ := sum["pending_text"].(string); !strings.Contains(text, "until KIFF can confirm nothing moved") || strings.Contains(strings.ToLower(text), "no money moved") {
		t.Fatal(sum["pending_text"])
	}
	// A total of attempts counts a refused call for good: nothing pending.
	if _, ok := count["pending"]; ok || count["used"] != float64(2) {
		t.Fatal(count)
	}

	// The agent can't release it with the vendor's words: no trusted evidence.
	agent := runtimeKey()
	agent.AgentID = "agent-a"
	if rr := do(t, mux, agent, http.MethodPost, "/v1/me/card-effects/refused/reconcile", map[string]any{"isError": true, "effect": "none"}); rr.Code != http.StatusConflict {
		t.Fatal(rr.Code, rr.Body.String())
	}
	if sum, _ = limits(); sum["used"] != float64(9800) || sum["pending"] != float64(4900) {
		t.Fatal(sum)
	}

	// Trusted evidence releases it, and it is no longer pending.
	f.evidence["refused"] = mandate.NoEffectEvidence{TenantID: "t1", ProposalID: "refused", Subject: "agent-a", Action: "create_coupon", At: now.Add(time.Second), Source: "trusted-executor", EvidenceID: "terminal-1", Reason: "final refusal"}
	if rr := do(t, mux, agent, http.MethodPost, "/v1/me/card-effects/refused/reconcile", nil); rr.Code != http.StatusOK {
		t.Fatal(rr.Code, rr.Body.String())
	}
	sum, _ = limits()
	if sum["used"] != float64(4900) {
		t.Fatal(sum)
	}
	if _, ok := sum["pending"]; ok {
		t.Fatal("a released call isn't pending", sum)
	}
}

// A sum whose parameter declares no unit names no amount: its raw number
// is in minor units, and "4900 of it" would read as 100 times too much.
func TestPendingTextNamesNoAmountWithoutAUnit(t *testing.T) {
	if got, want := pendingText(4900, 1, false, "", 0, wordsIn(langEN)), "Part of it is from 1 call the tool refused. It counts until KIFF can confirm nothing moved."; got != want {
		t.Fatalf("no unit: %q, want %q", got, want)
	}
	if got := pendingText(4900, 2, false, "EUR", 2, wordsIn(langEN)); !strings.HasPrefix(got, "€49 of it is from 2 calls") {
		t.Fatalf("with a unit: %q", got)
	}
	if got := pendingText(0, 2, true, "", 0, wordsIn(langEN)); !strings.HasPrefix(got, "2 of them are calls the tool refused") {
		t.Fatalf("a count: %q", got)
	}
}
