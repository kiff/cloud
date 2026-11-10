package gateway_test

import (
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
)

// Invariant: a tool whose profile says it moves or records money, on a connection where KIFF holds the key, asks the owner on every call: KIFF is told kiff_owner_ask with the code sealed_money, for real calls and trials, so no Card's room lets one through without a person.
// Category: authority
// Attack: refund a small amount through a sealed money tool, well inside the agent's Card, so no person sees it
// Refused: the call's proposal carries kiff_owner_ask, and KIFF holds it for the owner; a tool that moves no money, or one a permit binds, asks nothing extra
func TestASealedMoneyToolAsksItsOwnerEveryCall(t *testing.T) {
	r := newRig(t)
	sealed := refundTool
	sealed.Mode, sealed.Money = gateway.ModeSealed, true
	r.gw.Catalog = catalog{sealed}
	got := r.call(t, "agent-key", amount(500, "op-ask"))
	key, _ := got.call["invocation"].(string)
	if p := r.api.params[key]; key == "" || p[gateway.OwnerAskParam] != gateway.OwnerAskSealedMoney || p["amount_eur"] != int64(500) {
		t.Fatalf("a sealed money call told KIFF %v (%+v)", p, got)
	}

	r.trialRun = "run-1"
	r.trialCall(t, "agent-key", "live", "issue_refund", amount(500, ""))
	if len(r.api.trials) != 1 || r.api.trials[0].Parameters[gateway.OwnerAskParam] != gateway.OwnerAskSealedMoney {
		t.Fatalf("a trial of it: %+v", r.api.trials)
	}

	// Moving no money, it asks nothing extra.
	plain := newRig(t)
	notMoney := refundTool
	notMoney.Mode = gateway.ModeSealed
	plain.gw.Catalog = catalog{notMoney}
	got = plain.call(t, "agent-key", amount(500, "op-plain"))
	key, _ = got.call["invocation"].(string)
	if p := plain.api.params[key]; key == "" || p[gateway.OwnerAskParam] != nil {
		t.Fatalf("a sealed call moving no money told KIFF %v", p)
	}
}
