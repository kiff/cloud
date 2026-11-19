package pages

import (
	"strings"
	"testing"
)

// A filter that survives the first click and drops on the second is
// worse than no filter: the reader believes they are still looking at
// one agent while the page has widened to every agent, with the way
// back to the card gone from the screen.
func TestEveryLinkKeepsTheActorScope(t *testing.T) {
	v := DashboardDomainEvidenceView{
		DomainName: "orders",
		Tab:        "decisions",
		Page:       2,
		Outcome:    "blocked",
		Window:     "7d",
		Actor:      "refund-agent-eu",
		Card:       "card-eu",
	}
	for name, href := range map[string]string{
		"outcome tile": evidenceDecisionsHref(v, "blocked", 1),
		"older page":   evidenceDecisionsHref(v, v.Outcome, v.Page+1),
		"newer page":   evidenceDecisionsHref(v, v.Outcome, v.Page-1),
		"receipts tab": evidenceTabHref(v, "receipts"),
		"about tab":    evidenceTabHref(v, "about"),
	} {
		if !strings.Contains(href, "actor=refund-agent-eu") {
			t.Errorf("%s dropped the actor filter: %s", name, href)
		}
		// The way back to the card has to survive too, or the reader
		// lands on a filtered page with no route to what filtered it.
		if !strings.Contains(href, "card=card-eu") {
			t.Errorf("%s dropped the card it came from: %s", name, href)
		}
		if !strings.Contains(href, "window=7d") {
			t.Errorf("%s dropped the window: %s", name, href)
		}
	}
}

// Unfiltered links stay clean: nothing is added when there is no scope
// to carry.
func TestLinksStayCleanWithoutAScope(t *testing.T) {
	v := DashboardDomainEvidenceView{DomainName: "orders", Tab: "decisions", Window: "30d"}
	for _, href := range []string{
		evidenceDecisionsHref(v, "", 1),
		evidenceTabHref(v, "receipts"),
	} {
		for _, unwanted := range []string{"actor=", "card=", "window="} {
			if strings.Contains(href, unwanted) {
				t.Errorf("href %q carries %q with nothing to carry", href, unwanted)
			}
		}
	}
}
