package handlers

import (
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/content/invariants"
)

func TestInvariantsPage(t *testing.T) {
	rr := httptest.NewRecorder()
	Invariants().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/invariants", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()

	// Every published invariant must actually reach the page. A
	// guarantee that exists in the artifact but not in the HTML is
	// worse than one that was never claimed.
	all, err := invariants.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no invariants to render")
	}
	// The worked example is the part a reader without repository access
	// can actually use. If it stops rendering, the page is back to
	// citing lines nobody can open.
	withExample := 0
	for _, inv := range all {
		if inv.HasExample() {
			withExample++
			if !strings.Contains(collapseWS(body), collapseWS(inv.Attack)) {
				t.Errorf("page is missing the attack for %s", inv.Test)
			}
			if !strings.Contains(collapseWS(body), collapseWS(inv.Refused)) {
				t.Errorf("page is missing the refusal for %s", inv.Test)
			}
		}
	}
	if withExample != len(all) {
		t.Errorf("%d of %d invariants carry a worked example; every published guarantee "+
			"should be legible without opening the repository", withExample, len(all))
	}

	for _, inv := range all {
		if !strings.Contains(body, inv.Test) {
			t.Errorf("page is missing test name %q", inv.Test)
		}
		if !strings.Contains(body, inv.Location()) {
			t.Errorf("page is missing location %q for %s", inv.Location(), inv.Test)
		}
	}

	// The bound is the reason the page is credible. If it goes, the
	// page has become a badge.
	for _, want := range []string{
		// The bound is the reason the page is credible at all.
		"a system with no failing tests is not a system with no",
		"the conformance tests passed the entire",
		`href="/pricing"`,

		// The repository is private, so the citations cannot be
		// checked by a reader. The page has to say that itself
		// rather than let someone discover the gap — a citation
		// presented as proof, that nobody can open, is worse than
		// no citation.
		"The file and line are an index, not evidence",
		"name a line and we open that file in front of you on a call",
	} {
		if !strings.Contains(collapseWS(body), collapseWS(want)) {
			t.Errorf("page missing %q", want)
		}
	}
}

// collapseWS normalises whitespace and un-escapes HTML entities, so an
// assertion can be written as the statement actually reads rather than
// as templ encodes it — quotes in an attack like actor.roles = ["admin"]
// arrive as &#34;.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}
