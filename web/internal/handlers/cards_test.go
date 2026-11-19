package handlers

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
)

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }
func renderCards(t *testing.T, v pages.CardsView) string {
	t.Helper()
	var b bytes.Buffer
	if err := pages.Cards(v).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return collapse(b.String())
}
func renderCardDetail(t *testing.T, v pages.CardDetailView) string {
	t.Helper()
	var b bytes.Buffer
	if err := pages.CardDetail(v).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return collapse(b.String())
}
func balance(ls ...apiclient.MandateLimit) pages.CardBalance { return pages.CardBalance{Limits: ls} }
func activeCard() apiclient.Mandate {
	until := time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC)
	return apiclient.Mandate{ID: "card-eu", Subject: "refund-agent-eu", Domain: "orders", Environment: "production", Active: true, ValidUntil: &until, Grants: []apiclient.MandateGrant{{Action: "ISSUE_REFUND", EntityStates: []string{"PAID"}}}, Aggregates: []apiclient.MandateAggregate{{Quantity: "sum(amount_cents)", Limit: 200000, Window: "calendar_day"}}}
}

func TestCardsIndexIsInventoryOnly(t *testing.T) {
	c := activeCard()
	body := renderCards(t, pages.CardsView{Cards: []apiclient.Mandate{c}, Status: "active", View: "gallery", Usage: map[string]pages.CardBalance{c.ID: balance(apiclient.MandateLimit{Quantity: "sum(amount_cents)", Limit: 200000, Used: 64000, Remaining: 136000, Window: "calendar_day", Status: "ok"})}})
	for _, want := range []string{"refund-agent-eu", "ISSUE_REFUND", "Open card", `href="/cards/card-eu"`, "Active", "Gallery", "List", "Revoked", "Archived"} {
		if !strings.Contains(body, collapse(want)) {
			t.Errorf("missing %q", want)
		}
	}
	for _, forbidden := range []string{"FR-10042", "Issued and changed", "Agent activity", "Change ceiling", "Revoke card"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("index contains detail content %q", forbidden)
		}
	}
}

func TestIndividualCardSeparatesOverviewStatementAndChanges(t *testing.T) {
	c := activeCard()
	st := &apiclient.MandateStatement{Mandate: c, Limits: []apiclient.MandateLimit{{Quantity: "sum(amount_cents)", Limit: 200000, Used: 64000, Remaining: 136000, Window: "calendar_day", Status: "ok"}}, Draws: []apiclient.MandateDraw{{ProposalID: "FR-10042", Action: "ISSUE_REFUND", EntityID: "ord-42", Amount: 64000, DrawnAt: time.Date(2026, 9, 17, 10, 4, 0, 0, time.UTC)}}, Revisions: []apiclient.MandateRevision{{Revision: 1, ChangedAt: time.Date(2026, 9, 12, 9, 30, 0, 0, time.UTC), ChangedBy: "owner@example.com", Aggregates: []apiclient.MandateAggregate{{Quantity: "sum(amount_cents)", Limit: 200000}}}}, Note: "draws are recorded when an action is authorized, not when it executes"}
	base := pages.CardDetailView{Card: &c, Statement: st, Balance: balance(st.Limits...), Tab: "overview"}
	overview := renderCardDetail(t, base)
	for _, want := range []string{"Issue refund authority", "€2,000", "€1,360", "left of €2,000 per calendar day", `href="/cards/card-eu/settings"`, `href="/cards/card-eu/revoke"`} {
		if !strings.Contains(overview, collapse(want)) {
			t.Errorf("overview missing %q", want)
		}
	}
	// The terms are Settings rows, each opening its step.
	base.Tab = "settings"
	settings := renderCardDetail(t, base)
	for _, want := range []string{"Who holds it", "refund-agent-eu", "Past the limit", "Revoke this Card", "Its activity", "card=card-eu&amp;step=3"} {
		if !strings.Contains(settings, collapse(want)) {
			t.Errorf("settings missing %q", want)
		}
	}
	base.Tab = "overview"
	if strings.Contains(overview, "FR-10042") || strings.Contains(overview, "owner@example.com") {
		t.Error("overview contains statement or change rows")
	}
	base.Tab = "statement"
	statement := renderCardDetail(t, base)
	for _, want := range []string{"Statement", "FR-10042", "ISSUE_REFUND", "on ord-42", "not when it executes"} {
		if !strings.Contains(statement, collapse(want)) {
			t.Errorf("statement missing %q", want)
		}
	}
	if strings.Contains(statement, "owner@example.com") {
		t.Error("statement contains change history")
	}
	// What the owner approved once outside the card has its own tab: the
	// statement points there and lists none of it (2026-10-08, it sat
	// below the draws where it was missed).
	withApproved := base
	stApproved := *st
	stApproved.ApprovedOutside = &apiclient.ApprovedOutside{
		Entries: []apiclient.ApprovedOutsideEntry{
			{ExceptionID: "exc-1", ProposalID: "tc-outside", Action: "ISSUE_REFUND", Quantity: "count", Amount: 1},
			{ExceptionID: "exc-1", ProposalID: "tc-outside", Action: "ISSUE_REFUND", Quantity: "sum(amount_cents)", Amount: 250000},
		},
		Totals: map[string]int64{"count": 1, "sum(amount_cents)": 250000},
	}
	withApproved.Statement = &stApproved
	withApproved.Tab = "statement"
	statement = renderCardDetail(t, withApproved)
	if !strings.Contains(statement, collapse("1 action approved once outside this Card")) || strings.Contains(statement, "/needs-you/exc-1") {
		t.Error("statement should point to the Approved once tab and not list its rows")
	}
	// A failed read of what was approved once is said on the Statement
	// too, never a statement that looks complete.
	stUnread := *st
	stUnread.ApprovedOutsideError = "What was approved outside this card could not be read."
	unread := base
	unread.Statement = &stUnread
	unread.Tab = "statement"
	if body := renderCardDetail(t, unread); !strings.Contains(body, collapse("could not be read")) || !strings.Contains(body, collapse("can't show whether anything was")) {
		t.Error("statement should warn that approvals outside the card could not be read")
	}
	withApproved.Tab = "approved"
	withApproved.Answers = map[string]apiclient.CardException{"exc-1": {ID: "exc-1", Status: "consumed", ToolOutcome: &apiclient.ToolOutcome{Status: "refused_by_tool"}}}
	approved := renderCardDetail(t, withApproved)
	for _, want := range []string{"Approved once", "/needs-you/exc-1", "€2,500", "Refused by the tool", "1 action"} {
		if !strings.Contains(approved, collapse(want)) {
			t.Errorf("approved tab missing %q", want)
		}
	}
	if strings.Count(approved, "/needs-you/exc-1") != 1 {
		t.Error("an action approved once is one row, not one per quantity")
	}
	base.Tab = "changes"
	changes := renderCardDetail(t, base)
	for _, want := range []string{"Issued and changed", "owner@example.com", "€2,000"} {
		if !strings.Contains(changes, collapse(want)) {
			t.Errorf("changes missing %q", want)
		}
	}
	if strings.Contains(changes, "FR-10042") {
		t.Error("changes contains movements")
	}
}

func TestRevokedCardOffersArchiveAndPreservesHistoryLinks(t *testing.T) {
	c := activeCard()
	at := time.Now().Add(-time.Hour)
	c.RevokedAt = &at
	body := renderCardDetail(t, pages.CardDetailView{Card: &c, Tab: "settings", Balance: balance(apiclient.MandateLimit{Status: "ok"})})
	history := renderCardDetail(t, pages.CardDetailView{Card: &c, Tab: "statement", Balance: balance(apiclient.MandateLimit{Status: "ok"})})
	if !strings.Contains(body, "Archive this Card") || !strings.Contains(history, "/statement") || !strings.Contains(history, "/changes") {
		t.Error("revoked card must remain inspectable and archiveable")
	}
	if strings.Contains(body, "Revoke this Card") || strings.Contains(body, "step=") {
		t.Error("revoked card offered live-card actions")
	}
}

func TestUnreadableBalanceNeverLooksUnused(t *testing.T) {
	c := activeCard()
	body := renderCards(t, pages.CardsView{Cards: []apiclient.Mandate{c}, Status: "active", Usage: map[string]pages.CardBalance{c.ID: {Unread: true}}})
	if strings.Contains(body, "Used <strong>0") || !strings.Contains(body, "couldn’t be read") {
		t.Error("unknown balance was presented as unused")
	}
}

func TestFirstRunAndFilteredEmptyRemainDistinct(t *testing.T) {
	first := renderCards(t, pages.CardsView{Status: "active"})
	if !strings.Contains(first, "Nothing is issued automatically") {
		t.Error("first run lost authority warning")
	}
	filtered := renderCards(t, pages.CardsView{Status: "active", Holder: "refund-agent-eu"})
	if !strings.Contains(filtered, "refund-agent-eu holds no Card") || strings.Contains(filtered, "Nothing is issued automatically") {
		t.Error("filtered empty rendered as first run")
	}
}

func TestCardInventoryStates(t *testing.T) {
	c := activeCard()
	now := time.Now()
	if !cardMatchesInventory(c, "active", now) {
		t.Error("active card missing")
	}
	c.RevokedAt = &now
	if !cardMatchesInventory(c, "revoked", now) || cardMatchesInventory(c, "active", now) {
		t.Error("revoked filter wrong")
	}
	c.ArchivedAt = &now
	if !cardMatchesInventory(c, "archived", now) || cardMatchesInventory(c, "revoked", now) {
		t.Error("archived filter wrong")
	}
}

func TestCardsGalleryGroupsCardsByAgent(t *testing.T) {
	refund := activeCard()
	credit := activeCard()
	credit.ID, credit.Grants = "card-credit", []apiclient.MandateGrant{{Action: "ISSUE_STORE_CREDIT"}}
	other := activeCard()
	other.ID, other.Subject = "card-a", "billing-agent"
	body := renderCards(t, pages.CardsView{Cards: []apiclient.Mandate{refund, other, credit}, Status: "active", View: "gallery", Usage: map[string]pages.CardBalance{}})

	billing := strings.Index(body, `aria-label="Cards held by billing-agent"`)
	refunds := strings.Index(body, `aria-label="Cards held by refund-agent-eu"`)
	if billing < 0 || refunds < 0 || billing > refunds {
		t.Fatalf("want one group per agent in name order, got billing@%d refund@%d", billing, refunds)
	}
	if !strings.Contains(body[refunds:], `<span class="ac-agent-count">2 cards</span>`) {
		t.Error("the refund agent's group should count its two cards")
	}
	if !strings.Contains(body, `href="/cards/new?subject=refund-agent-eu">Give refund-agent-eu another card`) {
		t.Error("an agent's group should offer another card for that agent")
	}

	revoked := renderCards(t, pages.CardsView{Cards: []apiclient.Mandate{refund}, Status: "revoked", View: "gallery", Usage: map[string]pages.CardBalance{}})
	if strings.Contains(revoked, "another card") {
		t.Error("the revoked tab should not offer new cards")
	}
	filtered := renderCards(t, pages.CardsView{Cards: []apiclient.Mandate{refund}, Holder: "refund-agent-eu", Status: "active", View: "gallery", Usage: map[string]pages.CardBalance{}})
	if strings.Contains(filtered, "ac-agent-head") {
		t.Error("a page already filtered to one agent should not repeat it as a group heading")
	}
}

func taskCard() apiclient.Mandate {
	return apiclient.Mandate{ID: "refund-agent-refunds", Subject: "refund-agent", Domain: "card-refund", Active: true,
		Grants:     []apiclient.MandateGrant{{Action: "REFUND_ORDER"}},
		Aggregates: []apiclient.MandateAggregate{{Quantity: "sum(amount_cents)", Limit: 100000, Window: "calendar_day"}}}
}

func TestCardConnectPanel(t *testing.T) {
	c := taskCard()
	body := renderCardDetail(t, pages.CardDetailView{Card: &c, Tab: "settings", Statement: &apiclient.MandateStatement{}})
	for _, want := range []string{
		`<details class="ac-connect" open>`, "Connect this Card to your agent", "With your coding agent", "Recommended",
		"Agent name, sent as actor_id: refund-agent", "Domain: card-refund",
		"https://api.kiff.dev/v1/proposals/decide", `&#34;actor_id&#34;: &#34;refund-agent&#34;`,
		"https://api.kiff.dev/v1/events/raw?domain=card-refund", `the &#34;support&#34; role`,
		`&#34;entity_id&#34;: &#34;&lt;the order id&gt;&#34;`, "Ask for guided setup", "/dashboard/support/new?", `action="/cards/refund-agent-refunds/test"`, "It draws €1",
		`action="/cards/refund-agent-refunds/key"`, "Create a key for this agent",
	} {
		if !strings.Contains(body, collapse(want)) {
			t.Errorf("connect panel missing %q", want)
		}
	}

	used := renderCardDetail(t, pages.CardDetailView{Card: &c, Tab: "settings", Statement: &apiclient.MandateStatement{Draws: []apiclient.MandateDraw{{Amount: 100}}}})
	if !strings.Contains(used, `<details class="ac-connect">`) {
		t.Error("a card already in use should keep the panel folded")
	}

	advanced := c
	advanced.Domain, advanced.Grants = "orders", []apiclient.MandateGrant{{Action: "ISSUE_REFUND"}}
	adv := renderCardDetail(t, pages.CardDetailView{Card: &advanced, Tab: "settings"})
	if !strings.Contains(adv, "With your coding agent") || strings.Contains(adv, "Send a test request") {
		t.Error("a card from the full form gets the prompt but no test request")
	}

	past := time.Now().Add(-time.Hour)
	revoked := c
	revoked.RevokedAt = &past
	if strings.Contains(renderCardDetail(t, pages.CardDetailView{Card: &revoked, Tab: "settings"}), "Connect this Card") {
		t.Error("a revoked card cannot be connected")
	}
}

func renderDomainPage(t *testing.T, v pages.DashboardDomainsView) string {
	t.Helper()
	var b bytes.Buffer
	if err := pages.DashboardDomains(v).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return collapse(b.String())
}

func TestDomainOverviewLeadsWithCardsAndAttention(t *testing.T) {
	detail := &apiclient.DomainDetail{Name: "card-refund", Entity: "Order", Posture: "enforce",
		Actions: []apiclient.DomainAction{{Name: "REFUND_ORDER", RequiresAuthority: true}, {Name: "ISSUE_CREDIT"}}}
	card := taskCard()
	card.Domain = "card-refund"
	base := pages.DashboardDomainsView{Me: &apiclient.Me{}, Selected: detail, Tab: "overview"}

	withCard := base
	withCard.DomainCards = []apiclient.Mandate{card}
	withCard.CardUsage = map[string]pages.CardBalance{card.ID: balance(apiclient.MandateLimit{Quantity: "sum(amount_cents)", Limit: 100000, Used: 100000, Remaining: 0, Window: "calendar_day", Status: "ok"})}
	body := renderDomainPage(t, withCard)
	for _, want := range []string{
		`<h1>card-refund</h1>`, "Issue a card", `href="/cards/new?task=refunds"`,
		`class="is-current" href="/dashboard/domains/card-refund"`, `href="/dashboard/domains/card-refund?tab=settings"`,
		"What agents can do here", "REFUND_ORDER", "requires a card", "ISSUE_CREDIT", "no card needed",
		"<code>refund-agent</code>", "€0 of €1,000 left per calendar day",
		"Needs attention", "refund-agent has no room left on refund-agent-refunds",
	} {
		if !strings.Contains(body, collapse(want)) {
			t.Errorf("overview missing %q", want)
		}
	}
	for _, gone := range []string{"Governance posture", "Actions against real traffic", "Delete this domain", "Lifecycle"} {
		if strings.Contains(body, gone) {
			t.Errorf("overview should leave %q to its tab", gone)
		}
	}

	noCard := renderDomainPage(t, base)
	if !strings.Contains(noCard, "REFUND_ORDER needs a Card and no agent holds one") {
		t.Error("an action that requires a card no agent holds must be flagged")
	}
	if strings.Contains(noCard, "ISSUE_CREDIT requires a card") {
		t.Error("an action that needs no card must not be flagged for lacking one")
	}

	observe := base
	observe.Selected = &apiclient.DomainDetail{Name: "card-refund", Posture: "observe"}
	if !strings.Contains(renderDomainPage(t, observe), "This domain is in observe mode.") {
		t.Error("observe posture must be warned about on the overview")
	}

	contract := base
	contract.Tab = "contract"
	contract.Selected = &apiclient.DomainDetail{Name: "card-refund", Lifecycle: apiclient.DomainLifecycle{Transitions: []apiclient.DomainTransition{{From: "", On: "ORDER_PAID", To: "PAID"}}}}
	if !strings.Contains(renderDomainPage(t, contract), "On ORDER_PAID: new → PAID") {
		t.Error("the first transition should read as from new, not an empty state")
	}
}

// A Card shows what is waiting for its owner and what was approved
// outside it, so an exception is never invisible from the Card.
func TestCardDetailShowsHeldAndPolicy(t *testing.T) {
	c := activeCard()
	c.OnExceed = "owner"
	held := []apiclient.CardException{{ID: "exc-1", Subject: "refund-agent-eu", Action: "ISSUE_REFUND", Status: "held",
		Breaches: []apiclient.CardBreach{{MandateID: "card-eu", Outcome: "per_action_exceeded", Quantity: "count", Limit: 1, Draw: 2}}}}
	got := renderCardDetail(t, pages.CardDetailView{Card: &c, Tab: "overview", Held: held})
	for _, want := range []string{"Needs you", "/needs-you/exc-1"} {
		if !strings.Contains(got, want) {
			t.Errorf("detail lacks %q", want)
		}
	}
	got = renderCardDetail(t, pages.CardDetailView{Card: &c, Tab: "overview", HeldError: "could not load held"})
	if !strings.Contains(got, "could not load held") {
		t.Error("held read failure hidden")
	}
}

// A gallery Card is how the owner tells Cards apart, so what it says is
// readable to a screen reader; only the seam is decoration.
func TestGalleryCardIsReadable(t *testing.T) {
	c := activeCard()
	body := renderCards(t, pages.CardsView{Cards: []apiclient.Mandate{c}, Status: "active", View: "gallery", Usage: map[string]pages.CardBalance{}})
	i := strings.Index(body, `class="k-card"`)
	if i < 0 {
		t.Fatal("no Card in the gallery")
	}
	card := body[i:]
	card = card[:strings.Index(card, "k-lines")+600]
	if strings.Contains(body[i-40:i], "aria-hidden") {
		t.Error("the Card container is hidden from assistive technology")
	}
	for _, want := range []string{`aria-hidden="true" focusable="false"`, "refund-agent-eu", "Past its limit:", "Active"} {
		if !strings.Contains(card, want) {
			t.Errorf("the gallery Card lacks %q", want)
		}
	}
}
