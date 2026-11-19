package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/kiff/cloud/api/internal/mandate"
)

// A currency ask closes when a Card that gives its agent an action in that
// unit is signed: issued by the owner, or an agent's draft once confirmed,
// in the same transaction, naming the action. A draft not yet confirmed
// closes nothing, and another agent's or another unit's ask stays
// (kiff/tafs#464 item 11).
func TestASignedCardInTheUnitClosesTheCurrencyAsk(t *testing.T) {
	src := unitDomains{yaml: map[string]string{"gateway": unitDomainYAML("gateway", "USD", 2)}}
	ask := func(r *draftRig, proposal, subject, unit string) mandate.Exception {
		e, _, err := r.store.AskCardUnit(context.Background(), mandate.CardUnitAsk{TenantID: "t1", ProposalID: proposal, Subject: subject,
			Action: "refund_eur", EntityID: "pi_1", Unit: unit, CardUnit: "EUR"}, r.now)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	status := func(r *draftRig, e mandate.Exception) mandate.Exception {
		got, err := r.store.Exception(context.Background(), "t1", e.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	t.Run("the owner issues it", func(t *testing.T) {
		r := newDraftRigOn(t, src)
		usd, gbp, theirs := ask(r, "p-1", "chargebacks", "USD"), ask(r, "p-2", "chargebacks", "GBP"), ask(r, "p-3", "refunds", "USD")
		if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/mandates", card("chargebacks-usd", "chargebacks")); rr.Code != http.StatusCreated {
			t.Fatalf("issue: %d %s", rr.Code, rr.Body)
		}
		if got := status(r, usd); got.Status != mandate.ExceptionChanged || got.Reason != "a signed Card gives it refund in USD now" || got.ResolvedBy != "alice" {
			t.Fatalf("the USD ask: %+v", got)
		}
		for _, e := range []mandate.Exception{gbp, theirs} {
			if got := status(r, e); got.Status != mandate.ExceptionHeld {
				t.Fatalf("%s's %s ask closed", e.Subject, e.Breaches[0].Unit)
			}
		}
	})

	t.Run("a draft closes it only once confirmed", func(t *testing.T) {
		r := newDraftRigOn(t, src)
		usd := ask(r, "p-4", "chargebacks", "USD")
		code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "chargebacks", "cards": []any{card("chargebacks-usd", "chargebacks")}})
		if code != http.StatusCreated && code != http.StatusOK {
			t.Fatalf("submit: %d %v", code, d)
		}
		if got := status(r, usd); got.Status != mandate.ExceptionHeld {
			t.Fatalf("a draft not yet confirmed closed the ask: %s", got.Status)
		}
		if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+d["id"].(string)+"/confirm", map[string]string{"version": d["version"].(string)}); rr.Code != http.StatusOK {
			t.Fatalf("confirm: %d %s", rr.Code, rr.Body)
		}
		if got := status(r, usd); got.Status != mandate.ExceptionChanged || got.Reason != "a signed Card gives it refund in USD now" {
			t.Fatalf("confirmed: %+v", got)
		}
	})
}
