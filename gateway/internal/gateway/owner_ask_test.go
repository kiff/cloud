package gateway_test

import (
	"context"
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

// Invariant: an operation whose reviewed profile says ask_each_call asks the owner on every call, whatever its custody: a permit-bound tool (relay) is told kiff_owner_ask with the code each_call, so no Card's room pays a website's checkout without a person.
// Category: authority
// Attack: pay a small checkout through a relay tool, well inside the agent's Card, so no person sees it
// Refused: the call's proposal carries kiff_owner_ask each_call, and KIFF holds it for the owner; the same tool without the flag asks nothing extra
func TestAnAskEachCallToolAsksItsOwnerEveryCallEvenWithAPermit(t *testing.T) {
	r := newRig(t)
	pay := refundTool
	pay.Mode, pay.Money, pay.AskEachCall = gateway.ModeRelay, true, true
	r.gw.Catalog = catalog{pay}
	got := r.call(t, "agent-key", amount(500, "op-each"))
	key, _ := got.call["invocation"].(string)
	if p := r.api.params[key]; key == "" || p[gateway.OwnerAskParam] != gateway.OwnerAskEachCall {
		t.Fatalf("an ask_each_call tool told KIFF %v (%+v)", p, got)
	}

	plain := newRig(t)
	relay := refundTool
	relay.Mode, relay.Money = gateway.ModeRelay, true
	plain.gw.Catalog = catalog{relay}
	got = plain.call(t, "agent-key", amount(500, "op-relay"))
	key, _ = got.call["invocation"].(string)
	if p := plain.api.params[key]; key == "" || p[gateway.OwnerAskParam] != nil {
		t.Fatalf("a relay money tool without the flag told KIFF %v", p)
	}
}

// The catalog carries ask_each_call from the tool's approved profile, so the
// call above asks: a catalog that dropped it would let a checkout through on
// a Card's room.
func TestTheCatalogCarriesAskEachCallFromTheProfile(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.pool.Exec(ctx, `DELETE FROM gateway.tools WHERE tenant_id = 'tenant-ask';
		DELETE FROM gateway.profiles WHERE tenant_id = 'tenant-ask';
		INSERT INTO gateway.profiles (tenant_id, revision, profile, profile_sha256, words_sha256, device_id, signed_at, created_by) VALUES
		('tenant-ask', 1, '{"operations":[{"tool":"pay","kind":"write","effect":"moves_money","ask_each_call":true},{"tool":"act","kind":"write","effect":"writes"}]}', 'p', 'w', 'd', now(), 'owner');
		INSERT INTO gateway.tools (tenant_id, name, input_schema, upstream_url, upstream_tool, profile_revision) VALUES
		('tenant-ask', 'pay', '{}', 'http://t/mcp', 'pay', 1), ('tenant-ask', 'act', '{}', 'http://t/mcp', 'act', 1)`); err != nil {
		t.Fatal(err)
	}
	tools, err := gateway.PostgresCatalog{Pool: r.pool}.Tools(ctx, "tenant-ask")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tl := range tools {
		got[tl.Name] = tl.AskEachCall
	}
	if len(got) != 2 || !got["pay"] || got["act"] {
		t.Fatalf("ask_each_call from the catalog: %v", got)
	}
}
