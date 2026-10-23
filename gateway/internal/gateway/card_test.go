package gateway_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
)

var balanceTool = gateway.Tool{Name: "get_balance", Description: "Show the balance",
	UpstreamURL: "http://tool.test/mcp", UpstreamTool: "balance", ReadOnly: true}

func descriptions(t *testing.T, r *rig) map[string]string {
	t.Helper()
	status, out := r.rpc(t, "agent-key", "tools/list", nil)
	if status != http.StatusOK || out["result"] == nil {
		t.Fatalf("tools/list: %d %v", status, out)
	}
	got := map[string]string{}
	for _, tool := range out["result"].(map[string]any)["tools"].([]any) {
		m := tool.(map[string]any)
		got[m["name"].(string)], _ = m["description"].(string)
	}
	return got
}

// Each tool says what the agent's Card allows for it before the agent
// calls, and a tool no Card covers says its calls are refused.
func TestToolListShowsTheCard(t *testing.T) {
	r := newRig(t)
	orders := gateway.Tool{Name: "list_orders", Description: "List orders",
		UpstreamURL: "http://tool.test/mcp", UpstreamTool: "orders", ReadOnly: true}
	r.gw.Catalog = catalog{refundTool, balanceTool, orders}
	// A tool with no amount argument: only counts apply to it, never a sum.
	r.api.card.Cards = append([]kiffapi.Card{}, refundCard.Cards...)
	r.api.card.Cards[0].Grants = append(r.api.card.Cards[0].Grants, kiffapi.CardGrant{Action: "list_orders"})
	// The Card names the amount in another case; the decision still sums it.
	r.api.card.Cards[0].Grants[0].PerActionMax = []kiffapi.PerActionMax{{Quantity: "sum(Amount_EUR)", Max: 1000}}
	got := descriptions(t, r)
	want := map[string]string{
		"issue_refund": "Refund an order\n\nYour KIFF Card: Card mdt-refunds covers this tool: up to 1000 amount_eur per call; " +
			"a total of 5000 amount_eur today; a total of 20 calls in the last hour, shared by every tool this Card covers. " +
			"A per-action breach can be held for review. Approval still counts fully; a cumulative ceiling needs an explicit Card revision. What is used and left of each total: kiff_card.",
		"get_balance": "Show the balance\n\nYour KIFF Card: no Card of yours covers this tool, so calls to it are refused.",
		"list_orders": "List orders\n\nYour KIFF Card: Card mdt-refunds covers this tool: " +
			"a total of 20 calls in the last hour, shared by every tool this Card covers. A per-action breach can be held for review. Approval still counts fully; a cumulative ceiling needs an explicit Card revision. What is used and left of each total: kiff_card.",
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s:\n got %q\nwant %q", name, got[name], w)
		}
	}
	if !strings.Contains(got[gateway.CardTool], "changes nothing") {
		t.Errorf("kiff_card: %q", got[gateway.CardTool])
	}
}

// When the Card cannot be read the tools are still listed, as they are:
// the line is information, and every call is decided when it is made.
func TestToolListWithoutTheCard(t *testing.T) {
	r := newRig(t)
	r.api.cardFails = true
	got := descriptions(t, r)
	if len(got) != 3 || got["issue_refund"] != "Refund an order" { // and kiff_card, kiff_pending
		t.Fatalf("tools: %v", got)
	}
}

// Invariant: reading the Card is not a governed call. It is answered by
// the gateway: nothing is recorded, decided, drawn or sent to a tool.
// Category: accounting
// Attack: call kiff_card and expect it to count against a limit or reach a connected tool
// Refused: CallTool answers kiff_card before any invocation is begun
func TestCardToolReadsWithoutDeciding(t *testing.T) {
	r := newRig(t)
	// A rolling forecast remains read-only and never pretends the entire
	// budget resets. Both the text and structured MCP result carry it.
	at := time.Date(2026, 10, 10, 14, 20, 0, 0, time.UTC)
	r.api.card.Cards = append([]kiffapi.Card{}, refundCard.Cards...)
	r.api.card.Cards[0].Limits = append([]kiffapi.CardLimit{}, refundCard.Cards[0].Limits...)
	r.api.card.Cards[0].Limits[0].Window = "rolling_24h"
	r.api.card.Cards[0].Limits[0].Forecast = &kiffapi.CardForecast{State: "estimated", RunsOutAt: &at, ObservationSeconds: 3600}
	status, out := r.rpc(t, "agent-key", "tools/call", map[string]any{"name": gateway.CardTool})
	if status != http.StatusOK || out["result"] == nil {
		t.Fatalf("tools/call: %d %v", status, out)
	}
	res := out["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if res["isError"] == true || !strings.Contains(text, "agent coding-agent") ||
		!strings.Contains(text, "- issue_refund: Card mdt-refunds covers this tool: up to 1000 amount_eur per call") {
		t.Fatalf("result: %v", res)
	}
	sc, _ := res["structuredContent"].(map[string]any)
	if sc["agent_id"] != "coding-agent" {
		t.Errorf("structuredContent: %v", res["structuredContent"])
	}
	if !strings.Contains(text, "estimated to run out at 2026-10-10T14:20:00Z") || strings.Contains(text, "resets at") {
		t.Fatalf("forecast text: %s", text)
	}
	limits := sc["cards"].([]any)[0].(map[string]any)["limits"].([]any)
	forecast := limits[0].(map[string]any)["forecast"].(map[string]any)
	if forecast["runs_out_at"] != "2026-10-10T14:20:00Z" || forecast["resets_at"] != nil {
		t.Fatalf("forecast result: %v", forecast)
	}
	var n int
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM gateway.invocations`).Scan(&n); err != nil || n != 0 {
		t.Errorf("invocations: %d %v", n, err)
	}
	if len(r.api.ingested) != 0 || len(r.api.drawn) != 0 || len(r.tool.calls) != 0 {
		t.Errorf("ingested %v, drawn %v, forwarded %v", r.api.ingested, r.api.drawn, r.tool.calls)
	}

	r.api.cardFails = true
	_, out = r.rpc(t, "agent-key", "tools/call", map[string]any{"name": gateway.CardTool})
	if res := out["result"].(map[string]any); res["isError"] != true {
		t.Errorf("unreadable Card: %v", res)
	}
}

// Invariant: every spelling of kiff_card is the gateway's own tool. A tool
// connected under one before the name was reserved is neither listed nor
// reached.
// Category: authority
// Attack: call a connected tool named KIFF_Card that tools/list hides
// Refused: CallTool answers every reserved spelling as kiff_card before any invocation is begun
func TestReservedSpellingNeverReachesAConnectedTool(t *testing.T) {
	r := newRig(t)
	legacy := refundTool
	legacy.Name = "KIFF_Card"
	r.gw.Catalog = catalog{refundTool, legacy}
	if got := descriptions(t, r); len(got) != 3 || got["KIFF_Card"] != "" {
		t.Fatalf("tools: %v", got)
	}
	status, out := r.rpc(t, "agent-key", "tools/call", map[string]any{"name": "KIFF_Card", "arguments": map[string]any{"amount_eur": 10}})
	if status != http.StatusOK || out["result"] == nil {
		t.Fatalf("tools/call: %d %v", status, out)
	}
	res := out["result"].(map[string]any)
	if text := res["content"].([]any)[0].(map[string]any)["text"].(string); !strings.Contains(text, "You act as agent coding-agent") {
		t.Errorf("not answered as kiff_card: %v", res)
	}
	var n int
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM gateway.invocations`).Scan(&n); err != nil || n != 0 {
		t.Errorf("invocations: %d %v", n, err)
	}
	if len(r.api.ingested) != 0 || len(r.tool.calls) != 0 {
		t.Errorf("ingested %v, forwarded %v", r.api.ingested, r.tool.calls)
	}
}

// With the owner's amount unit, the Card line reads in it: 3800 cents of a
// EUR scale-2 amount is 38.00 EUR (#units).
func TestToolListShowsTheCardInTheAmountUnit(t *testing.T) {
	r := newRig(t)
	refund := refundTool
	refund.AmountUnit, refund.AmountScale = "EUR", 2
	r.gw.Catalog = catalog{refund}
	// apps/api resolved the Card's total to the same unit.
	card := refundCard.Cards[0]
	card.Limits = append([]kiffapi.CardLimit{}, card.Limits...)
	card.Limits[0].Unit, card.Limits[0].Scale = "EUR", 2
	r.api.card.Cards = []kiffapi.Card{card}
	got := descriptions(t, r)
	want := "Refund an order\n\nYour KIFF Card: Card mdt-refunds covers this tool: up to 10.00 EUR per call; " +
		"a total of 50.00 EUR today; a total of 20 calls in the last hour, shared by every tool this Card covers. " +
		"A per-action breach can be held for review. Approval still counts fully; a cumulative ceiling needs an explicit Card revision. What is used and left of each total: kiff_card."
	if got["issue_refund"] != want {
		t.Errorf("issue_refund:\n got %q\nwant %q", got["issue_refund"], want)
	}
}

// One Card total shared by a EUR tool and a USD tool has no single unit, so
// apps/api leaves it bare and both tools show it bare, the same numbers;
// each tool's per-call maximum still reads in its own unit.
func TestCardTotalSharedByToolsOfDifferentUnitsStaysBare(t *testing.T) {
	r := newRig(t)
	refund := refundTool
	refund.AmountUnit, refund.AmountScale = "EUR", 2
	payout := refundTool
	payout.Name, payout.Description, payout.AmountUnit, payout.AmountScale = "issue_payout", "Pay out", "USD", 2
	r.gw.Catalog = catalog{refund, payout}
	card := refundCard.Cards[0]
	card.Grants = []kiffapi.CardGrant{
		{Action: "issue_refund", PerActionMax: []kiffapi.PerActionMax{{Quantity: "sum(amount_eur)", Max: 1000}}},
		{Action: "issue_payout", PerActionMax: []kiffapi.PerActionMax{{Quantity: "sum(amount_eur)", Max: 2000}}},
	}
	card.Limits = []kiffapi.CardLimit{{Quantity: "sum(amount_eur)", Limit: 5000, Window: "calendar_day", Used: 1200, Remaining: 3800, Status: "ok"}}
	r.api.card.Cards = []kiffapi.Card{card}
	got := descriptions(t, r)
	for name, perCall := range map[string]string{"issue_refund": "up to 10.00 EUR per call", "issue_payout": "up to 20.00 USD per call"} {
		want := perCall + "; a total of 5000 amount_eur today."
		if !strings.Contains(got[name], want) {
			t.Errorf("%s:\n got %q\nwant it to contain %q", name, got[name], want)
		}
	}
}

// The agent reads a lifetime total as one that never resets, not as
// "in this window (lifetime)".
func TestCardLifetimeTotalReadsAsNeverResetting(t *testing.T) {
	r := newRig(t)
	card := refundCard.Cards[0]
	card.Limits = []kiffapi.CardLimit{{Quantity: "sum(amount_eur)", Limit: 20000, Window: "lifetime", Used: 5000, Remaining: 15000, Status: "ok"}}
	r.api.card.Cards = []kiffapi.Card{card}
	want := "15000 of 20000 amount_eur left for the Card's whole life (it never resets)"
	if got := cardText(t, r); !strings.Contains(got, want) {
		t.Fatalf("got %q\nwant it to contain %q", got, want)
	}
	if got := descriptions(t, r)["issue_refund"]; !strings.Contains(got, "a total of 20000 amount_eur for the Card's whole life (it never resets)") {
		t.Fatalf("description: %q", got)
	}
}

// cardText is kiff_card's answer.
func cardText(t *testing.T, r *rig) string {
	t.Helper()
	status, out := r.rpc(t, "agent-key", "tools/call", map[string]any{"name": gateway.CardTool})
	if status != http.StatusOK || out["result"] == nil {
		t.Fatalf("kiff_card: %d %v", status, out)
	}
	return out["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
}

// The tool list starts the agent's prompt, which is cached between its turns
// and runs (kiff/tafs#138, item 58): what a Card has used and left moves with
// every call, so it isn't in the tools' descriptions, only in kiff_card. On the
// owner's taf every message read its whole prompt again (no cache read on its
// first turn), because a read's call count changed each tool's description.
func TestToolDescriptionsStayTheSameAsTheCardIsUsed(t *testing.T) {
	r := newRig(t)
	card := refundCard.Cards[0]
	card.Limits = []kiffapi.CardLimit{{Quantity: "sum(amount_eur)", Limit: 5000, Window: "calendar_day", Used: 1200, Remaining: 3800, Status: "ok"}}
	r.api.card.Cards = []kiffapi.Card{card}
	before := descriptions(t, r)
	if strings.Contains(before["issue_refund"], "3800") || !strings.Contains(before["issue_refund"], "a total of 5000 amount_eur today") {
		t.Fatalf("description: %q", before["issue_refund"])
	}
	if got := cardText(t, r); !strings.Contains(got, "3800 of 5000 amount_eur left today") {
		t.Fatalf("kiff_card: %q", got)
	}
	r.api.card.Cards[0].Limits[0].Used, r.api.card.Cards[0].Limits[0].Remaining = 4900, 100
	after := descriptions(t, r)
	for name, d := range before {
		if after[name] != d {
			t.Errorf("%s moved with usage:\n before %q\n after  %q", name, d, after[name])
		}
	}
	if got := cardText(t, r); !strings.Contains(got, "100 of 5000 amount_eur left today") {
		t.Fatalf("kiff_card after: %q", got)
	}
}
