package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
)

func TestSplitExceptionsOrdersWaitingOldestFirst(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	r1, r2 := t0.Add(5*time.Hour), t0.Add(9*time.Hour)
	es := []apiclient.CardException{
		{ID: "new", Status: "held", CreatedAt: t0.Add(2 * time.Hour)},
		{ID: "a", Status: "rejected", CreatedAt: t0, ResolvedAt: &r1},
		{ID: "old", Status: "held", CreatedAt: t0},
		{ID: "b", Status: "consumed", CreatedAt: t0, ResolvedAt: &r2},
	}
	held, answered := SplitExceptions(es, 1)
	if len(held) != 2 || held[0].ID != "old" || held[1].ID != "new" {
		t.Fatalf("held %+v", held)
	}
	if len(answered) != 1 || answered[0].ID != "b" {
		t.Fatalf("answered %+v", answered)
	}
}

func TestHeldForCardMatchesAnyBreach(t *testing.T) {
	es := []apiclient.CardException{
		{ID: "1", Status: "held", Breaches: []apiclient.CardBreach{{MandateID: "a"}, {MandateID: "b"}}},
		{ID: "2", Status: "approved", Breaches: []apiclient.CardBreach{{MandateID: "b"}}},
		{ID: "3", Status: "held", Breaches: []apiclient.CardBreach{{MandateID: "c"}}},
	}
	got := HeldForCard(es, "b")
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("got %+v", got)
	}
}

func TestBreachWording(t *testing.T) {
	per := apiclient.CardBreach{MandateID: "card-refunds", Outcome: "per_action_exceeded", Quantity: "sum(amount_cents)", Limit: 50000, Draw: 150000}
	total := apiclient.CardBreach{MandateID: "card-day", Outcome: "limit_reached", Quantity: "count", Limit: 10, Used: 10, Draw: 1}
	if per.Over() != 100000 || total.Over() != 1 {
		t.Fatalf("over %d %d", per.Over(), total.Over())
	}
	if got := breachAllows(total); got != "10 in its window, 0 left" {
		t.Errorf("total allows %q", got)
	}
	e := apiclient.CardException{Subject: "refund-agent", Action: "REFUND_ORDER", Breaches: []apiclient.CardBreach{per, total}}
	if got := ExceptionSummary(e); got != "refund-agent wants to REFUND_ORDER "+exceptionAmount(e)+", outside card-refunds, card-day" {
		t.Errorf("summary %q", got)
	}
	if outsideCardsLine(1) == outsideCardsLine(2) {
		t.Error("plural")
	}
}

// the cards-for-any-agent design: an expired hold reads as expired
// with its reason, and a held one says when it stops waiting.
func TestExpiredHoldWording(t *testing.T) {
	exp := apiclient.CardException{Status: "expired", Reason: "not answered before the hold expired"}
	if exceptionStatusLabel(exp) != "Expired" || !strings.HasPrefix(exceptionReasonLabel(exp.Reason), "Expired: nobody answered") {
		t.Fatalf("expired: %q / %q", exceptionStatusLabel(exp), exceptionReasonLabel(exp.Reason))
	}
	at := time.Date(2026, 10, 1, 12, 10, 0, 0, time.UTC)
	held := apiclient.CardException{ID: "exc-1", Status: "held", ExpiresAt: &at}
	if answerByLabel(held, time.UTC) != "1 Oct 12:10 UTC" || answerByLabel(apiclient.CardException{Status: "held"}, time.UTC) != "" {
		t.Fatalf("answer by: %q", answerByLabel(held, time.UTC))
	}
	var b strings.Builder
	if err := needsYouItem(held, false, nil).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "Answer by 1 Oct 12:10 UTC.") {
		t.Fatalf("held item lacks its deadline: %s", b.String())
	}
	// In the account's time zone, so it doesn't read as past.
	b.Reset()
	if err := needsYouItem(held, true, nil).Render(components.WithTimeZone(context.Background(), "Europe/Paris"), &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "Answer by 1 Oct 14:10 CEST.") || strings.Contains(b.String(), " UTC") {
		t.Fatalf("deadline not in the account's zone: %s", b.String())
	}
}

// A held call past a Card's total says why there is no Approve once,
// naming the total; other missing buttons are not explained so.
func TestPastTotalHint(t *testing.T) {
	cards := []apiclient.Mandate{{ID: "card-a", Aggregates: []apiclient.MandateAggregate{
		{Quantity: "count", Limit: 2, Window: "calendar_day"},
		{Quantity: "sum(amount_cents)", Limit: 30000, Window: "calendar_day"},
		{Quantity: "sum(amount_cents)", Limit: 100000, Window: "lifetime"},
	}}}
	held := func(bs ...apiclient.CardBreach) apiclient.CardException {
		e := apiclient.CardException{Status: "held", Breaches: bs}
		FillBreachWindows(&e, cards)
		return e
	}
	daily := held(apiclient.CardBreach{MandateID: "card-a", Outcome: "limit_reached", Quantity: "count", Limit: 2})
	if got := pastTotalHint(daily); !strings.HasPrefix(got, "This call is past the Card's daily total. Approve once covers only a per-call limit or a resource") {
		t.Errorf("daily: %q", got)
	}
	life := held(apiclient.CardBreach{MandateID: "card-a", Outcome: "limit_reached", Quantity: "sum(amount_cents)", Limit: 100000})
	if got := pastTotalHint(life); !strings.HasPrefix(got, "This call is past the Card's whole-life total.") {
		t.Errorf("matched by limit: %q", got)
	}
	// Two totals on one quantity, and the limit matches neither (extra
	// room changed it): the window isn't guessed.
	unknown := held(apiclient.CardBreach{MandateID: "card-a", Outcome: "limit_reached", Quantity: "sum(amount_cents)", Limit: 31000})
	if got := pastTotalHint(unknown); !strings.HasPrefix(got, "This call is past one of the Card's totals.") {
		t.Errorf("unknown window: %q", got)
	}
	both := held(apiclient.CardBreach{MandateID: "card-a", Outcome: "limit_reached", Quantity: "count", Limit: 2},
		apiclient.CardBreach{MandateID: "card-a", Outcome: "limit_reached", Quantity: "sum(amount_cents)", Limit: 100000})
	if got := pastTotalHint(both); !strings.Contains(got, "the Card's daily total and its whole-life total") || !strings.Contains(got, "those totals") {
		t.Errorf("both: %q", got)
	}
	for name, e := range map[string]apiclient.CardException{
		"per-call only":    held(apiclient.CardBreach{MandateID: "card-a", Outcome: "per_action_exceeded"}),
		"room unreadable":  func() apiclient.CardException { e := daily; e.RoomUnavailable = true; return e }(),
		"approved no room": func() apiclient.CardException { e := daily; e.Status = "approved_no_room"; return e }(),
	} {
		if got := pastTotalHint(e); got != "" {
			t.Errorf("%s: %q", name, got)
		}
	}
	var b strings.Builder
	if err := needsYouItem(daily, true, nil).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "past the Card&#39;s daily total") && !strings.Contains(b.String(), "past the Card's daily total") {
		t.Fatalf("hint not shown: %s", b.String())
	}
}

// A call on a resource outside the Card reads as such, not as an amount
// over a limit.
func TestBreachLineNamesTheResource(t *testing.T) {
	b := apiclient.CardBreach{MandateID: "card-a", Outcome: "resource_not_on_card", Resource: "order_id", Value: "ord_9"}
	if got := breachLine(b); got != "order_id ord_9 is not one this Card applies to" {
		t.Errorf("breachLine = %q", got)
	}
	if got := breachAllows(b); got != "this action only for the order_id values it lists" {
		t.Errorf("breachAllows = %q", got)
	}
	b.Value = ""
	if got := breachLine(b); !strings.HasPrefix(got, "No order_id named") {
		t.Errorf("breachLine with no value = %q", got)
	}
	e := apiclient.CardException{Status: "held", Breaches: []apiclient.CardBreach{b}}
	if !e.MayApproveOnce() {
		t.Error("a resource breach can be approved once")
	}
}

func TestCardResourcesLabel(t *testing.T) {
	m := apiclient.Mandate{Grants: []apiclient.MandateGrant{{Action: "refund_order",
		Resources: &apiclient.MandateResources{Parameter: "order_id", Values: []string{"ord_1", "ord_2"}}}, {Action: "get_order"}}}
	if got := cardResourcesLabel(m); got != "order_id ord_1, ord_2" {
		t.Errorf("label = %q", got)
	}
	if cardResourcesLabel(apiclient.Mandate{Grants: []apiclient.MandateGrant{{Action: "x"}}}) != "" {
		t.Error("an unscoped Card has no label")
	}
}

func TestCardNextStepsLabel(t *testing.T) {
	m := apiclient.Mandate{Grants: []apiclient.MandateGrant{
		{Action: "refund_order", NextSteps: []string{"return_first", "proof_first"}},
		{Action: "issue_credit", NextSteps: []string{"return_first"}},
	}}
	if got := cardNextStepsLabel(m); got != "Return first, Proof first" {
		t.Fatalf("label = %q", got)
	}
	if cardNextStepsLabel(apiclient.Mandate{Grants: []apiclient.MandateGrant{{Action: "x"}}}) != "" {
		t.Fatal("a Card without next steps has no label")
	}
}
