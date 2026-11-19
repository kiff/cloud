package pages

import (
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// The page showed `["mandate_owner_rejected"]` as the reason
// (screenshot). A reader sees a sentence instead, and an unknown code
// as words.
func TestAttentionWhyIsASentence(t *testing.T) {
	item := apiclient.AttentionItem{Kind: "blocked", ActorID: "support-demo", Reason: `["mandate_limit_reached"]`}
	if got := attentionWhy(item); len(got) != 1 || got[0] != "The Card's limit for this window is used up." {
		t.Fatalf("why: %v", got)
	}
	if href := attentionCardsHref(item); href != "/cards?holder=support-demo" {
		t.Fatalf("cards href: %q", href)
	}
	if got := attentionWhy(apiclient.AttentionItem{Kind: "blocked", Reason: `["state_not_allowed"]`}); got[0] != "State not allowed." {
		t.Fatalf("unknown code: %v", got)
	}
	if got := attentionWhy(apiclient.AttentionItem{Kind: "blocked"}); got[0] != "The decision recorded no reason." {
		t.Fatalf("no reason: %v", got)
	}
	if href := attentionCardsHref(apiclient.AttentionItem{Kind: "blocked", ActorID: "a", Reason: `["state_not_allowed"]`}); href != "" {
		t.Fatalf("non-card refusal leads to cards: %q", href)
	}
}

func TestAnsweredFilter(t *testing.T) {
	es := []apiclient.CardException{
		{ID: "h", Status: "held", Subject: "a"},
		{ID: "r", Status: "rejected", Subject: "a"},
		{ID: "c", Status: "consumed", Subject: "b", ToolOutcome: &apiclient.ToolOutcome{Status: "refused_by_tool"}},
		{ID: "d", Status: "consumed", Subject: "b", ToolOutcome: &apiclient.ToolOutcome{Status: "done"}},
	}
	ids := func(f AnsweredFilter) (out string) {
		for _, e := range FilterAnswered(es, f) {
			out += e.ID
		}
		return out
	}
	if got := ids(NormalizeAnsweredFilter("", "")); got != "rcd" {
		t.Errorf("all: %q", got)
	}
	if got := ids(NormalizeAnsweredFilter("approved", "")); got != "cd" {
		t.Errorf("approved: %q", got)
	}
	if got := ids(NormalizeAnsweredFilter("tool-problem", "")); got != "c" {
		t.Errorf("tool problem: %q", got)
	}
	if got := ids(NormalizeAnsweredFilter("rejected", "b")); got != "" {
		t.Errorf("rejected by b: %q", got)
	}
	if f := NormalizeAnsweredFilter("bogus", "b"); f.Answer != "" || f.Query() != "?agent=b" {
		t.Errorf("bogus answer kept: %+v %q", f, f.Query())
	}
	if got := AnsweredAgents(es); len(got) != 2 || got[0] != "a" {
		t.Errorf("agents: %v", got)
	}
	if exceptionAnswerLabel(es[2]) != "Approved once" || answeredOutcomeLabel(es[2]) != "Refused by the tool" {
		t.Errorf("labels: %q / %q", exceptionAnswerLabel(es[2]), answeredOutcomeLabel(es[2]))
	}
}
