package pages

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/gatewayclient"
)

func toolCardForTest() apiclient.Mandate {
	return apiclient.Mandate{ID: "refund-agent-issue-refund", Subject: "refund-agent", Domain: "gateway", Active: true,
		OnExceed: "owner", HoldExpirySeconds: 86400,
		Grants: []apiclient.MandateGrant{{Action: "issue_refund",
			PerActionMax: []apiclient.MandatePerAction{{Quantity: "sum(amount_eur)", Max: 500}}}},
		Aggregates: []apiclient.MandateAggregate{
			{Quantity: "sum(amount_eur)", Limit: 2000, Window: "rolling_24h"},
			{Quantity: "count", Limit: 20, Window: "rolling_24h"},
		}}
}

// Changing a tool Card starts from its current terms.
func TestToolCardFormFrom(t *testing.T) {
	got := ToolCardFormFrom(toolCardForTest())
	want := ToolCardForm{Agent: "refund-agent", PerCall: "500", Total: "2000", Calls: "20", Window: "rolling_24h", OnExceed: "owner", Hold: "24h"}
	if got != want {
		t.Fatalf("got %+v", got)
	}
}

// the cards-for-any-agent design: a tool Card's page names the tool,
// not a domain, and does not offer the SDK connect guide.
func TestToolCardPage(t *testing.T) {
	m := toolCardForTest()
	var b bytes.Buffer
	if err := CardDetail(CardDetailView{Card: &m, Tab: "settings"}).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	section := body[strings.Index(body, `<section class="dash`):]
	for _, want := range []string{">Tool<", `href="/agents/refund-agent/tools#gateway"`, `href="/tools/issue_refund/card?agent=refund-agent&amp;step=2&amp;one=1"`, "Past the limit"} {
		if !strings.Contains(section, want) {
			t.Errorf("tool Card page missing %q", want)
		}
	}
	for _, unwanted := range []string{"<dt>Domain</dt>", "Connect this card to your agent", "Change ceiling"} {
		if strings.Contains(section, unwanted) {
			t.Errorf("tool Card page shows %q", unwanted)
		}
	}
}

// A hold the form does not offer is kept, not reset to 10 minutes.
func TestToolCardKeepsACustomHold(t *testing.T) {
	m := toolCardForTest()
	m.HoldExpirySeconds = 7200
	f := ToolCardFormFrom(m)
	if f.Hold != "2h0m0s" {
		t.Fatalf("hold = %q", f.Hold)
	}
	var b bytes.Buffer
	if err := ToolCard(ToolCardView{Tool: gatewayclient.Connection{Name: "issue_refund", AmountArg: "amount_eur"}, Form: f}).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `value="2h0m0s" checked`) || strings.Contains(b.String(), `value="10m" checked`) {
		t.Fatalf("the form does not keep the Card's hold")
	}
}

// the cards-for-any-agent design: a personal account's Activity
// page does not name the domain; a company's does.
func TestActivityPageForAPersonalAccount(t *testing.T) {
	render := func(kind string) string {
		var b bytes.Buffer
		ctx := components.WithAccountKind(context.Background(), kind)
		if err := DashboardDomainEvidence(DashboardDomainEvidenceView{DomainName: "gateway"}).Render(ctx, &b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	// What the owner reads: the page's text and its title, not the
	// addresses links point to.
	personal := render("personal")
	text := regexp.MustCompile(`<[^>]*>`).ReplaceAllString(personal, " ")
	if m := regexp.MustCompile(`(?i).{0,40}\b(gateway|domains?)\b.{0,40}`).FindString(text); m != "" {
		t.Errorf("personal Activity names the domain: %q", m)
	}
	if company := render("company"); !strings.Contains(company, "gateway · Evidence") {
		t.Errorf("company Activity lost its domain heading")
	}
}

// A shared Card's page lists its holders with "Stop sharing", offers to
// share, and Needs you says Change card changes it for all of them.
func TestSharedCardPages(t *testing.T) {
	m := toolCardForTest()
	m.Subjects = []string{"refund-agent", "billing-agent"}
	var b bytes.Buffer
	if err := CardSheet(CardSheetView{Kind: "holder", Card: m, Agents: []string{"refund-agent", "billing-agent", "ops-agent"}}).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	// The holder sheet offers the account's agents that do not hold it.
	if !strings.Contains(b.String(), `type="radio" name="agent" value="ops-agent"`) || strings.Contains(b.String(), `type="radio" name="agent" value="billing-agent"`) {
		t.Error("the share field offers the wrong agents")
	}
	if err := CardDetail(CardDetailView{Card: &m, Tab: "overview"}).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"refund-agent, billing-agent</span> hold this Card today.", `value="move"`,
		"Stop sharing with billing-agent", "Share it", "refund-agent + 1 more"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("card page missing %q", want)
		}
	}
	e := apiclient.CardException{ID: "exc-1", Status: "held", Breaches: []apiclient.CardBreach{{MandateID: m.ID}}}
	note := sharedCardsNote(e, SharedCards([]apiclient.Mandate{m}))
	if note != "Changing the Card changes it for every agent that holds it: refund-agent-issue-refund is shared by 2 agents." {
		t.Errorf("note = %q", note)
	}
	if sharedCardsNote(e, SharedCards([]apiclient.Mandate{toolCardForTest()})) != "" {
		t.Error("a Card held by one agent is called shared")
	}
	summaries := AgentsFrom([]apiclient.Agent{{ID: "refund-agent"}, {ID: "billing-agent"}}, []apiclient.Mandate{m}, nil, nil, nil, false, false)
	for _, s := range summaries {
		if len(s.Cards) != 1 {
			t.Errorf("%s holds %d Cards, want the shared one", s.ID, len(s.Cards))
		}
	}
}
