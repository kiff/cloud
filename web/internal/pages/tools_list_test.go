package pages

import (
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/gatewayclient"
)

func toolCard(id, tool string) apiclient.Mandate {
	return apiclient.Mandate{ID: id, Domain: "gateway", Grants: []apiclient.MandateGrant{{Action: tool}}}
}

// Tools by server, by name, each with what a Card limits and how many
// Cards name it; filters count every tool and narrow the groups.
func TestGroupTools(t *testing.T) {
	now := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	stripe := "https://mcp.stripe.com"
	shop := "https://shop.example.com/mcp"
	tools := []gatewayclient.Connection{
		{Name: "stripe_write", URL: stripe, Mode: "sealed", Description: "Write data via any Stripe API operation. 1. Use stripe_api_search first."},
		{Name: "shop_refund_order", URL: shop, Mode: "verify", AmountArg: "amount_cents", AmountUnit: "EUR", AmountScale: 2},
		{Name: "stripe_read", URL: stripe, Mode: "sealed", ReadOnly: true},
		{Name: "shop_get_order", URL: shop, Mode: "sealed", ReadOnly: true},
	}
	revoked := toolCard("old", "shop_refund_order")
	revoked.RevokedAt = &now
	cards := []apiclient.Mandate{toolCard("c1", "stripe_write"), toolCard("c2", "stripe_write"), toolCard("c3", "stripe_read"), revoked}

	groups, chips := GroupTools(tools, cards, true, ToolsFilter{}, now)
	if len(groups) != 2 || groups[0].Host != "mcp.stripe.com" || groups[1].Host != "shop.example.com" {
		t.Fatalf("groups: %+v", groups)
	}
	if g := groups[0]; g.Tools[0].Tool.Name != "stripe_read" || g.Tools[1].Cards != 2 || g.Custody != "KIFF holds the key, encrypted" || g.NoCard != 0 {
		t.Errorf("stripe group: %+v", g)
	}
	// A revoked Card does not cover the tool; mixed custody is said once.
	if g := groups[1]; g.Custody != "Keys differ: see each tool" || g.NoCard != 2 || toolGroupLine(g, ToolsFilter{}) != "2 tools · Keys differ: see each tool · 2 with no Card" {
		t.Errorf("shop group: %+v / %q", g, toolGroupLine(g, ToolsFilter{}))
	}
	want := map[string]int{"All": 4, "Change things": 2, "Read only": 2, "No Card yet": 2}
	for _, c := range chips {
		if c.Count != want[c.Label] {
			t.Errorf("chip %s = %d, want %d", c.Label, c.Count, want[c.Label])
		}
	}
	if got := toolLimit(tools[1]); got != "Amount · EUR cents" {
		t.Errorf("amount limit = %q", got)
	}
	if toolLimit(tools[0]) != "Calls" || toolLimit(tools[2]) != "Read only" {
		t.Error("limits for calls / read only")
	}
	if got := toolOneLine(tools[0]); got != "Write data via any Stripe API operation." {
		t.Errorf("one line = %q", got)
	}

	f := NormalizeToolsFilter(" refund ", "nocard")
	groups, chips = GroupTools(tools, cards, true, f, now)
	if len(groups) != 1 || len(groups[0].Tools) != 1 || groups[0].Tools[0].Tool.Name != "shop_refund_order" ||
		toolGroupLine(groups[0], f) != "1 of 2 tools · Keys differ: see each tool · 2 with no Card" {
		t.Errorf("filtered: %+v", groups)
	}
	if chips[1].Href != "/tools?q=refund&show=changes" || !chips[3].Current {
		t.Errorf("chips keep the search: %+v", chips)
	}

	// a review: a Card covers every tool any of its grants names, once
	// each; it counts only while in force and in the gateway domain.
	later := now.Add(time.Hour)
	both := apiclient.Mandate{ID: "both", Domain: "gateway", Grants: []apiclient.MandateGrant{{Action: "shop_cancel_order"}, {Action: "SHOP_ISSUE_STORE_CREDIT"}, {Action: "shop_cancel_order"}}}
	future := toolCard("future", "shop_cancel_order")
	future.ValidFrom = &later
	elsewhere := toolCard("elsewhere", "shop_cancel_order")
	elsewhere.Domain = "card-refund"
	anyDomain := toolCard("any", "shop_get_order")
	anyDomain.Domain = ""
	multi := []gatewayclient.Connection{{Name: "shop_cancel_order", URL: shop}, {Name: "shop_issue_store_credit", URL: shop}, {Name: "shop_get_order", URL: shop, ReadOnly: true}}
	groups, chips = GroupTools(multi, []apiclient.Mandate{both, future, elsewhere, anyDomain}, true, ToolsFilter{}, now)
	got := map[string]int{}
	for _, r := range groups[0].Tools {
		got[r.Tool.Name] = r.Cards
	}
	if got["shop_cancel_order"] != 1 || got["shop_issue_store_credit"] != 1 || got["shop_get_order"] != 1 {
		t.Errorf("covering counts = %v, want 1 each (multi-grant once, future and other domain not)", got)
	}
	for _, c := range chips {
		if c.Label == "No Card yet" && c.Count != 0 {
			t.Errorf("No Card yet = %d, want 0", c.Count)
		}
	}

	// Without the Cards, nothing is called uncovered.
	groups, chips = GroupTools(tools, nil, false, ToolsFilter{}, now)
	if len(chips) != 3 || groups[1].NoCard != 0 || toolCardsLabel(groups[1].Tools[0]) != "Cards ?" {
		t.Errorf("cards unknown: %+v %+v", chips, groups[1])
	}
	if NormalizeToolsFilter("", "bogus").Show != ToolsShowAll {
		t.Error("unknown show is not all")
	}
}
