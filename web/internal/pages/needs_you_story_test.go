package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
)

// Needs you opens on Waiting; a filter link from before the tabs opens
// Answered; an unknown tab is Waiting.
func TestNeedsYouTabs(t *testing.T) {
	for _, tc := range []struct {
		tab  string
		f    AnsweredFilter
		want string
	}{
		{"", AnsweredFilter{}, NeedsYouWaiting},
		{"bogus", AnsweredFilter{}, NeedsYouWaiting},
		{"warnings", AnsweredFilter{}, NeedsYouWarnings},
		{"answered", AnsweredFilter{}, NeedsYouAnswered},
		{"", AnsweredFilter{Answer: "rejected"}, NeedsYouAnswered},
		{"", AnsweredFilter{Agent: "luna-desk"}, NeedsYouAnswered},
	} {
		if got := NormalizeNeedsYouTab(tc.tab, tc.f); got != tc.want {
			t.Errorf("%q %+v: %q, want %q", tc.tab, tc.f, got, tc.want)
		}
	}
	if got := (AnsweredFilter{}).with("rejected", "luna-desk"); got != "/needs-you?agent=luna-desk&answer=rejected&tab=answered" {
		t.Errorf("filter link = %q", got)
	}
}

func renderNeedsYou(t *testing.T, ctx context.Context, v NeedsYouView) string {
	t.Helper()
	var b strings.Builder
	if err := NeedsYou(v).Render(ctx, &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// Warnings sit on their own tab as rows; Waiting says so in one line and
// never lists them. Add extra room is offered only to who can manage Cards.
func TestNeedsYouWarningsTab(t *testing.T) {
	at := time.Date(2026, 10, 13, 1, 57, 0, 0, time.UTC)
	warnings := OpenWarnings([]apiclient.AttentionItem{
		{ID: "w1", Kind: "card_warning", SourceID: "card-a", Reason: "This Card total is spent. 100% of this total is used today.", CreatedAt: at, Status: "open"},
		{ID: "w2", Kind: "card_warning", SourceID: "card-b", Reason: "Old warning", CreatedAt: at, Status: "resolved"},
	})
	if len(warnings) != 1 {
		t.Fatalf("open warnings: %+v", warnings)
	}
	v := NeedsYouView{Tab: NeedsYouWaiting, BudgetWarnings: warnings, WarningsOpen: len(warnings), WarningsUnread: len(warnings)}
	waiting := renderNeedsYou(t, context.Background(), v)
	if !strings.Contains(waiting, "Nothing is waiting for you.") || !strings.Contains(waiting, "1 Card is running low or used up") ||
		strings.Contains(waiting, "This Card total is spent") || strings.Contains(waiting, "All notices") {
		t.Fatalf("waiting: %s", waiting)
	}
	if !strings.Contains(waiting, `<span class="ac-tab-count is-warn">1</span>`) {
		t.Errorf("Warnings tab has no count")
	}
	v.Tab = NeedsYouWarnings
	page := renderNeedsYou(t, context.Background(), v)
	for _, want := range []string{"Used up", "card-a", "This Card total is spent", `href="/cards/card-a"`, `href="/cards/card-a/extra-room"`, "13 Oct 2026, 01:57 UTC"} {
		if !strings.Contains(page, want) {
			t.Errorf("warnings tab lacks %q", want)
		}
	}
	viewer := renderNeedsYou(t, components.WithCapabilities(context.Background(), []string{"cards.read"}), v)
	if strings.Contains(viewer, "/cards/card-a/extra-room") {
		t.Error("a viewer is offered extra room")
	}
	v.BudgetWarnings, v.WarningsOpen, v.BudgetWarningsError = nil, 0, "Budget warnings could not be read."
	if page := renderNeedsYou(t, context.Background(), v); !strings.Contains(page, "could not be read") || !strings.Contains(page, `<span class="ac-tab-count is-warn">?</span>`) {
		t.Errorf("unreadable warnings are not said")
	}
}

func answered(status string) apiclient.CardException {
	created := time.Date(2026, 10, 13, 2, 21, 0, 0, time.UTC)
	resolved := created.Add(3 * time.Minute)
	return apiclient.CardException{ID: "exc-1", ProposalID: "p-1", EntityID: "tc-74e3f4ca4e8", Subject: "luna-desk", Action: "shop_refund_order",
		Status: status, CreatedAt: created, ResolvedAt: &resolved, ResolvedBy: "you (ai@kiff.dev)", ResolvedRole: "admin",
		Breaches: []apiclient.CardBreach{
			{MandateID: "card-a", Outcome: "per_action_exceeded", Quantity: "sum(amount)", Limit: 5000, Draw: 24000, Unit: "EUR", Scale: 2},
			{MandateID: "card-a", Outcome: "limit_reached", Quantity: "sum(amount)", Limit: 30000, Used: 10500, Draw: 24000, Unit: "EUR", Scale: 2, Window: "calendar_day"},
		}}
}

func renderStory(t *testing.T, e apiclient.CardException) string {
	t.Helper()
	var b strings.Builder
	if err := answeredStory(e).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(b.String(), "&#39;", "'")
}

// An answered call reads as what happened, for every answer.
func TestAnsweredStory(t *testing.T) {
	rejected := answered("rejected")
	rejected.Refusal = &apiclient.Refusal{NextStep: "return_first", Note: "Ask for photos too."}
	page := renderStory(t, rejected)
	for _, want := range []string{
		"luna-desk asked to run shop_refund_order for 240.00 EUR. You rejected it and told the agent: Return first.",
		"Nothing was sent to the tool, and nothing counted against the Card.",
		"Bigger than one call may be", "The Card allows up to 50.00 EUR per call. This one was 240.00 EUR.",
		"More than the day had left", "Of the Card's 300.00 EUR a day, 195.00 EUR was left. This one needed 240.00 EUR.",
		"Rejected: Return first", "with the note: “Ask for photos too.”", "Nothing ran", "The tool was never called.",
		"Details for tracing", "tc-74e3f4ca4e8", `href="/cards/card-a"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("rejected story lacks %q", want)
		}
	}

	for status, wants := range map[string][]string{
		"approved": {"You approved it once. The agent hasn't collected it yet.", "Nothing has been sent yet."},
		"consumed": {"You approved it once, and the agent used the approval.", "The tool reported it done", "The agent collected it"},
		"expired":  {"Nobody answered before the hold ran out, so it was refused.", "The hold ran out", "Nothing ran"},
	} {
		e := answered(status)
		if status == "consumed" {
			used := e.ResolvedAt.Add(time.Minute)
			e.ConsumedAt = &used
			e.ToolOutcome = &apiclient.ToolOutcome{Status: "done", At: used}
		}
		page := renderStory(t, e)
		for _, want := range wants {
			if !strings.Contains(page, want) {
				t.Errorf("%s story lacks %q", status, want)
			}
		}
	}

	// a review: a withdrawn call shows an approval only when one was
	// recorded, at its own time, and the withdrawal at its own.
	withdrawn := answered("invalidated")
	withdrawn.ResolvedBy, withdrawn.ResolvedRole, withdrawn.Reason = "", "", "card_revoked"
	held := renderStory(t, withdrawn)
	if !strings.Contains(held, "It was withdrawn before anyone answered: the Card was revoked.") || !strings.Contains(held, "Withdrawn before anyone answered") ||
		strings.Contains(held, "Approved once") || strings.Contains(held, "approved it") || !strings.Contains(held, "nothing counted against the Card") {
		t.Errorf("held, then revoked: %s", held)
	}
	approvedAt := withdrawn.CreatedAt.Add(time.Minute)
	withdrawn.PriorApprovals = []apiclient.CardApprovalRecord{{By: "you (ai@kiff.dev)", At: &approvedAt}}
	if got := storyLead(withdrawn); !strings.Contains(got, "You approved it once, then it was withdrawn before the agent used it: the Card was revoked.") {
		t.Errorf("approved, then revoked: %q", got)
	}
	steps := storySteps(withdrawn)
	if len(steps) != 3 || steps[1].Title != "Approved once" || !steps[1].At.Equal(approvedAt) || steps[1].Detail != "Answered by you (ai@kiff.dev)" ||
		steps[2].Title != "Withdrawn before it was used" || !steps[2].At.Equal(*withdrawn.ResolvedAt) {
		t.Errorf("approved, then revoked steps: %+v", steps)
	}
	// An external approver's defer is not an approval.
	withdrawn.PriorApprovals = []apiclient.CardApprovalRecord{{At: &approvedAt, ExternalAnswer: &apiclient.ExternalAnswer{Principal: "svc", Decision: "defer", Reason: "Needs a human"}}}
	if got := storyLead(withdrawn); !strings.Contains(got, "withdrawn before anyone answered") {
		t.Errorf("deferred, then revoked: %q", got)
	}

	// a review: Change Card decides nothing until the agent asks again,
	// so without a recorded outcome nothing was checked or sent.
	changed := renderStory(t, answered("changed"))
	for _, want := range []string{"You changed the Card instead of approving it. If the agent asks again, the call is checked against the Card as it is then.",
		"Card changed instead", "No tool outcome is available here. A Card change alone doesn't say whether the agent asked again or the tool ran."} {
		if !strings.Contains(changed, want) {
			t.Errorf("changed story lacks %q", want)
		}
	}
	for _, never := range []string{"was checked again", "was checked against", "asked again,", "The tool reported", "Sent to the tool", "nothing has been sent", "Nothing was sent to the tool", "Nothing ran"} {
		if strings.Contains(changed, never) {
			t.Errorf("changed, not collected, claims %q", never)
		}
	}
	retried := answered("changed")
	retried.ToolOutcome = &apiclient.ToolOutcome{Status: "done", At: retried.ResolvedAt.Add(time.Minute)}
	if page := renderStory(t, retried); !strings.Contains(page, "The agent asked again, and the call was checked against the changed Card.") || !strings.Contains(page, "The tool reported") {
		t.Errorf("changed, then retried: %s", page)
	}
	refit := answered("changed")
	refit.ResolvedBy, refit.ResolvedRole, refit.Reason = "", "", "fits the card as it now stands"
	if got := storyLead(refit); !strings.Contains(got, "Before anyone answered, the agent asked again and the call fit the Card as it then stood.") {
		t.Errorf("fit on retry: %q", got)
	}
	// a review: a missing outcome may be a failed read, so neither story
	// says nothing was sent; a recorded outcome with no message still
	// says what it was.
	refitPage := renderStory(t, refit)
	if !strings.Contains(refitPage, "The call was allowed. No tool outcome is available here, so this page can't say whether the tool ran.") {
		t.Errorf("fit on retry, no outcome: %s", refitPage)
	}
	for _, never := range []string{"nothing has been sent", "Nothing was sent to the tool", "Nothing ran"} {
		if strings.Contains(refitPage, never) {
			t.Errorf("fit on retry, no outcome, claims %q", never)
		}
	}
	for status, want := range map[string]string{"unknown": "Outcome unknown.", "not_sent": "Not sent to the tool."} {
		blank := answered("changed")
		blank.ToolOutcome = &apiclient.ToolOutcome{Status: status, At: blank.ResolvedAt.Add(time.Minute)}
		if got := storyAfter(blank); got != want {
			t.Errorf("changed, %s with no message: %q, want %q", status, got, want)
		}
	}

	ext := answered("rejected")
	ext.ExternalAnswer = &apiclient.ExternalAnswer{Principal: "svc", Decision: "reject", Reason: "Not a damaged item", At: *ext.ResolvedAt}
	if page := renderStory(t, ext); !strings.Contains(page, "an external approver, rejected it") || !strings.Contains(page, "Not a damaged item") {
		t.Errorf("external story: %s", page)
	}
	// No one recorded: passive, never "You".
	anon := answered("rejected")
	anon.ResolvedBy = ""
	if got := storyLead(anon); !strings.Contains(got, "It was rejected.") {
		t.Errorf("unrecorded answerer: %q", got)
	}
	// A count limit reads in calls.
	count := apiclient.CardBreach{MandateID: "c", Outcome: "limit_reached", Quantity: "count", Limit: 2, Used: 2, Draw: 1, Window: "rolling_24h"}
	if r := storyReasons(apiclient.CardException{Breaches: []apiclient.CardBreach{count}}); r[0].Title != "More than the last 24 hours had left" ||
		r[0].Text != "Of the Card's 2 calls in any 24 hours, 0 calls were left. This one needed 1 call." {
		t.Errorf("count reason: %+v", r)
	}
}
