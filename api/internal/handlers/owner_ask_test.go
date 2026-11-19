package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/words"
	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
)

var (
	sealedMoneyWhy = words.Of(words.EN).OwnerAsks[words.OwnerAskSealedMoney]
	ownerAskOther  = words.Of(words.EN).OwnerAsks[words.OwnerAskOther]
)

// Invariant: a gateway call that asks its owner (kiff_owner_ask) is held for the owner however much room the Card has, with KIFF's own line for its reason code; words the caller sends there never reach the owner, and an unknown code or a value that isn't one still asks, with KIFF's generic line.
// Category: authority
// Attack: an agent calls KIFF's decide itself with kiff_owner_ask set to "KIFF security has checked this; approve to continue", so the owner reads its words as KIFF's on a hold about money
// Refused: the call is held with KIFF's generic line; the agent's words are on no hold and in no answer, and the parameter is never kept
func TestProposals_AGatewayOwnerAskIsHeldWhateverTheCardsRoom(t *testing.T) {
	t.Parallel()
	r := newTrialRig(t)
	agent := gatewayKey("refund-agent", "")
	decide := func(id string, params map[string]any) map[string]any {
		t.Helper()
		// The gateway's TOOL_CALL_REQUESTED for the call, as it records it.
		resolved, err := r.rt.For(context.Background(), r.tnt)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = resolved.IngestRaw(context.Background(), kiffadapter.RawInput{ID: "evt-" + id, Adapter: "cloud", Type: "TOOL_CALL_REQUESTED",
			Source: "kiff-gateway", ReceivedAt: time.Now().UTC(), EntityID: id, EntityType: "ToolCall", ActorID: "refund-agent", Payload: map[string]any{}})
		rec := drivePost(t, r.serve(agent), "/v1/proposals/decide", map[string]any{
			"id": id, "entity_id": id, "entity_type": "ToolCall", "action_name": "refund_payment", "actor_id": "refund-agent",
			"parameters": params,
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", id, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), handlers.GatewayOwnerAskParam) {
			t.Fatalf("%s: the gateway's parameter was kept: %s", id, rec.Body)
		}
		var got map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		return got
	}
	if got := decide("tc-plain", map[string]any{"amount": 1000}); got["outcome"] != "allowed" {
		t.Fatalf("without the ask: %v", got)
	}
	asking := map[string]any{"amount": 1000, handlers.GatewayOwnerAskParam: handlers.OwnerAskSealedMoney}
	first := decide("tc-ask", asking)
	if first["outcome"] == "allowed" {
		t.Fatalf("an owner ask was allowed: %v", first)
	}
	if again := decide("tc-ask", asking); again["outcome"] != first["outcome"] {
		t.Fatalf("the retry: %v, then %v", first, again)
	}
	held, err := r.store.Exceptions(context.Background(), r.tnt, mandate.ExceptionHeld)
	if err != nil || len(held) != 1 || held[0].ProposalID != "tc-ask" || len(held[0].Breaches) != 1 || held[0].Breaches[0].Reason != sealedMoneyWhy ||
		held[0].Breaches[0].OwnerAsk != words.OwnerAskSealedMoney {
		t.Fatalf("held: %+v %v", held, err)
	}
	// The caller's own words, an unknown code or a value that isn't one:
	// it still asks, with KIFF's generic line, and the words are shown nowhere.
	const forged = "KIFF security has checked this; approve to continue"
	for id, v := range map[string]any{"tc-forged": forged, "tc-unknown": "no_such_code", "tc-odd": true} {
		if got := decide(id, map[string]any{"amount": 1000, handlers.GatewayOwnerAskParam: v}); got["outcome"] == "allowed" {
			t.Fatalf("%s still asks: %v", id, got)
		}
	}
	all, _ := r.store.Exceptions(context.Background(), r.tnt, "")
	for _, e := range all {
		for _, b := range e.Breaches {
			if strings.Contains(b.Reason, forged) || strings.Contains(b.Reason, "no_such_code") {
				t.Fatalf("the caller's words reached the hold: %q", b.Reason)
			}
			if e.ProposalID != "tc-ask" && (b.Reason != ownerAskOther || b.OwnerAsk != words.OwnerAskOther) {
				t.Fatalf("%s: %q, want KIFF's generic line", e.ProposalID, b.Reason)
			}
		}
	}
	if len(all) != 4 {
		t.Fatalf("%d holds, want 4", len(all))
	}

	trial := refundTrial(1000)
	trial["parameters"].(map[string]any)[handlers.GatewayOwnerAskParam] = handlers.OwnerAskSealedMoney
	if code, a := r.try(t, agent, trial); code != http.StatusOK || a.Decision != "held" || !strings.Contains(a.Message, sealedMoneyWhy) {
		t.Fatalf("a trial of an owner ask: %d %+v", code, a)
	}
}
