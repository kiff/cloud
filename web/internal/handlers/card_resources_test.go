package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// The dashboard cannot show a resource scope yet, so no edit may drop
// one. Dropping it would widen the Card to every resource.

func TestEditFormPreservesAResourceScope(t *testing.T) {
	scope := &apiclient.MandateResources{Parameter: "order_id", Values: []string{"ord_1"}}
	m := apiclient.Mandate{Grants: []apiclient.MandateGrant{{Action: "refund_order", Resources: scope}},
		Aggregates: []apiclient.MandateAggregate{{Quantity: "count", Limit: 5, Window: "calendar_day"}}}
	simple, preserved := grantsForForm(m)
	if simple != "" || preserved == "" {
		t.Fatalf("a scoped grant must be preserved and reissued exactly: simple %q preserved %q", simple, preserved)
	}
	var back []apiclient.MandateGrant
	if err := json.Unmarshal([]byte(preserved), &back); err != nil || back[0].Resources == nil || back[0].Resources.Values[0] != "ord_1" {
		t.Fatalf("preserved grants lost the scope: %s %v", preserved, err)
	}
}

func TestToolCardReissueKeepsTheResourceScope(t *testing.T) {
	scope := &apiclient.MandateResources{Parameter: "order_id", Values: []string{"ord_1"}}
	cards := []apiclient.Mandate{{ID: "card-x", Grants: []apiclient.MandateGrant{{Action: "refund_order", Resources: scope}}}}
	req := apiclient.IssueMandateRequest{ID: "card-x", Grants: []apiclient.MandateGrant{{Action: "refund_order"}}}
	keepCardTerms(&req, cards)
	if req.Grants[0].Resources != scope {
		t.Fatalf("the reissue dropped the scope: %+v", req.Grants[0])
	}
	other := apiclient.IssueMandateRequest{ID: "card-new", Grants: []apiclient.MandateGrant{{Action: "refund_order"}}}
	keepCardTerms(&other, cards)
	if other.Grants[0].Resources != nil {
		t.Fatal("a new Card took another Card's scope")
	}
}

// Next steps are set through the API; no dashboard edit may drop them,
// or the owner's "Return first" answer would stop being accepted.
func TestEditFormPreservesNextSteps(t *testing.T) {
	m := apiclient.Mandate{Grants: []apiclient.MandateGrant{{Action: "refund_order", NextSteps: []string{"return_first"}}},
		Aggregates: []apiclient.MandateAggregate{{Quantity: "count", Limit: 5, Window: "calendar_day"}}}
	simple, preserved := grantsForForm(m)
	if simple != "" || preserved == "" {
		t.Fatalf("a grant with next steps must be preserved and reissued exactly: simple %q preserved %q", simple, preserved)
	}
	var back []apiclient.MandateGrant
	if err := json.Unmarshal([]byte(preserved), &back); err != nil || len(back[0].NextSteps) != 1 || back[0].NextSteps[0] != "return_first" {
		t.Fatalf("preserved grants lost the next steps: %s %v", preserved, err)
	}
}

func TestToolCardReissueKeepsNextSteps(t *testing.T) {
	cards := []apiclient.Mandate{{ID: "card-x", Grants: []apiclient.MandateGrant{{Action: "refund_order", NextSteps: []string{"return_first", "proof_first"}}}}}
	req := apiclient.IssueMandateRequest{ID: "card-x", Grants: []apiclient.MandateGrant{{Action: "refund_order"}}}
	keepCardTerms(&req, cards)
	if len(req.Grants[0].NextSteps) != 2 {
		t.Fatalf("the reissue dropped the next steps: %+v", req.Grants[0])
	}
}

// The client reads and writes next_steps, so a Card read and issued back
// through it keeps them.
func TestMandateGrantNextStepsRoundTrip(t *testing.T) {
	var g apiclient.MandateGrant
	if err := json.Unmarshal([]byte(`{"action":"refund_order","next_steps":["return_first"]}`), &g); err != nil || len(g.NextSteps) != 1 {
		t.Fatalf("decode: %+v %v", g, err)
	}
	b, _ := json.Marshal(g)
	if !strings.Contains(string(b), `"next_steps":["return_first"]`) {
		t.Fatalf("encode: %s", b)
	}
}
