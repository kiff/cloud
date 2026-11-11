package gateway_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/gateway/internal/gateway"
)

func withWords(args map[string]any, about, reason any) map[string]any {
	args[gateway.AboutArg], args[gateway.ReasonArg] = about, reason
	return args
}

// Invariant: the taf's words about a call (kiff_about, kiff_reason) go to KIFF beside the proposal, never into its parameters, the tool's arguments or the call's identity: a retry that words them differently is the same call, never a second one.
// Category: authority
// Attack: reword the reason on each retry of a refund, without an operation id, so the gateway sees a new call and sends the refund twice
// Refused: the retry replays the first call; the tool is sent one refund and the words never reach it or the Card's parameters
func TestTheTafsWordsGoToKIFFAndAreNotPartOfTheCall(t *testing.T) {
	r := newRig(t)
	got := r.call(t, "agent-key", withWords(amount(500, ""), " Order 1042 ", "arrived broken"))
	key, _ := got.call["invocation"].(string)
	if key == "" || r.api.words[key] != (gateway.TafWords{About: "Order 1042", Reason: "arrived broken"}) {
		t.Fatalf("KIFF was told %+v (%+v)", r.api.words[key], got)
	}
	for k := range r.api.params[key] {
		if k == gateway.AboutArg || k == gateway.ReasonArg {
			t.Fatalf("the taf's words are a parameter: %v", r.api.params[key])
		}
	}
	*r.now = t0.Add(time.Minute)
	r.call(t, "agent-key", withWords(amount(500, ""), "Order 1042", "the customer says it arrived broken"))
	if r.tool.count() != 1 {
		t.Fatalf("a reworded retry was sent again: %d calls", r.tool.count())
	}
	if sent := string(r.tool.raw[0]); strings.Contains(sent, "kiff_") || strings.Contains(sent, "broken") {
		t.Fatalf("the tool was sent the taf's words: %s", sent)
	}
}

// A held call asked again under its operation id, worded differently, is
// still that call: never refused as another, and KIFF gets the words of
// this ask.
func TestAHeldCallAskedAgainCarriesItsNewWords(t *testing.T) {
	r := newRig(t)
	first := r.call(t, "agent-key", withWords(amount(5000, "op-held"), "Order 1042", "arrived broken"))
	key, _ := first.call["invocation"].(string)
	if first.call["state"] != "held" || r.api.words[key].Reason != "arrived broken" {
		t.Fatalf("first: %+v, words %+v", first, r.api.words[key])
	}
	again := r.call(t, "agent-key", withWords(amount(5000, "op-held"), "Order 1042", "arrived broken, photo attached"))
	if again.call["state"] != "held" || strings.Contains(again.text, "already used") || r.tool.count() != 0 {
		t.Fatalf("again: %+v", again)
	}
	if r.api.words[key].Reason != "arrived broken, photo attached" {
		t.Fatalf("KIFF has %+v", r.api.words[key])
	}
}

// Words that aren't text only ever show to the owner: they are dropped,
// and the call goes on, never refused over them.
func TestTheTafsWordsThatArentTextAreDropped(t *testing.T) {
	r := newRig(t)
	got := r.call(t, "agent-key", withWords(amount(500, "op-x"), 1042, map[string]any{"amount": 5}))
	key, _ := got.call["invocation"].(string)
	if got.isError || key == "" || r.tool.count() != 1 || r.api.words[key] != (gateway.TafWords{}) {
		t.Fatalf("%+v, sent %d, KIFF told %+v", got, r.tool.count(), r.api.words[key])
	}
	if sent := string(r.tool.raw[0]); strings.Contains(sent, "kiff_") {
		t.Fatalf("the tool was sent %s", sent)
	}
}

// Each tool that changes something lists kiff_about and kiff_reason, with
// their caps; a read lists neither.
func TestToolsThatChangeSomethingTakeTheTafsWords(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{refundTool, balanceTool}
	_, out := r.rpc(t, "agent-key", "tools/list", nil)
	props := map[string]map[string]any{}
	for _, tool := range out["result"].(map[string]any)["tools"].([]any) {
		m := tool.(map[string]any)
		props[m["name"].(string)] = m["inputSchema"].(map[string]any)["properties"].(map[string]any)
	}
	about, _ := props["issue_refund"][gateway.AboutArg].(map[string]any)
	reason, _ := props["issue_refund"][gateway.ReasonArg].(map[string]any)
	if about["maxLength"] != float64(gateway.MaxAboutLength) || reason["maxLength"] != float64(gateway.MaxReasonLength) ||
		!strings.Contains(about["description"].(string), "Never a person's name") {
		t.Fatalf("issue_refund lists %v and %v", about, reason)
	}
	if _, ok := props["get_balance"][gateway.AboutArg]; ok {
		t.Fatalf("a read lists %s: %v", gateway.AboutArg, props["get_balance"])
	}
}
