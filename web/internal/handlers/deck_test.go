package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDeckIndex asserts /deck lists all three decks with their
// audience labels and links.
func TestDeckIndex(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/deck", nil)
	rec := httptest.NewRecorder()
	Deck(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type: got %q, want text/html", ct)
	}
	// Decks are shareable artifacts, not search surfaces.
	if rec.Header().Get("X-Robots-Tag") != "noindex" {
		t.Errorf("deck index missing noindex header")
	}

	body := rec.Body.String()
	mustContain := []string{
		`class="deck-index"`,
		`href="/static/css/deck.css?v=`,
		`href="/deck/sales"`,
		`href="/deck/investors"`,
		`href="/deck/general"`,
		"Sales", "Investors", "General",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("deck index missing %q", want)
		}
	}
}

// TestDeckSlide_FirstSlide asserts a deck slug renders slide 1 with
// the shell, the meta stamp, and a next link but no prev link.
func TestDeckSlide_FirstSlide(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/deck/sales", nil)
	req.SetPathValue("slug", "sales")
	rec := httptest.NewRecorder()
	Deck(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()

	mustContain := []string{
		`class="deck"`,
		`class="deck-slide prose"`,
		// The PDF affordance is a plain link to the print-all view,
		// not a JS button, the deck is fully no-JS.
		`class="deck-download"`,
		`href="/deck/sales?print=1"`,
		`class="deck-footer"`,
		`rel="next"`,
		// The version/updated stamp must render so a forwarded link
		// always says how current it is.
		"v0.7",
		"updated 2026-09-28",
		// Slide counter.
		"1 / ",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("slide 1 missing %q", want)
		}
	}
	// The deck must not load any script: it is fully no-JS.
	if strings.Contains(body, "<script") {
		t.Errorf("deck slide should not load any script")
	}
	// No prev link on slide 1.
	if strings.Contains(body, `rel="prev"`) {
		t.Errorf("slide 1 should not render a prev link")
	}
}

// TestInvestorDeck_EvidenceAndHolderClaims pins two corrections from
// the a review: card issuance is an attributed revision while
// decisions carry signed receipts, and a new agent reuses the domain
// but gets its own card.
func TestInvestorDeck_EvidenceAndHolderClaims(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/deck/investors?print=1", nil)
	req.SetPathValue("slug", "investors")
	rec := httptest.NewRecorder()
	Deck(nil).ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{
		"reuses the same domain and receives its own card",
		// Balances are per card and holder: the lock refuses a second
		// run of the same agent, never another agent.
		"Each agent draws on its own card.",
		"Two runs of one agent spend at once",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("investor deck missing %q", want)
		}
	}
	for _, bad := range []string{"uses the same card", "signed record of who issued", "Two agents spend at the same moment"} {
		if strings.Contains(body, bad) {
			t.Errorf("investor deck still claims %q", bad)
		}
	}
}

func TestInvestorDeck_ThesisBeforePete(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/deck/investors?print=1", nil)
	req.SetPathValue("slug", "investors")
	rec := httptest.NewRecorder()
	Deck(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Authority for a fleet of agents.",
		"Pete builds agents for other companies.",
		"It cannot say how much that agent may spend today.",
		"v0.6",
		"updated 2026-09-28",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("investor opener missing %q", want)
		}
	}
	if strings.Index(body, "Authority for a fleet of agents.") > strings.Index(body, "Pete builds agents for other companies.") {
		t.Error("investor thesis should precede Pete's example")
	}
}

// TestDeckSlide_Clamping asserts an out-of-range ?slide=N clamps to
// the last slide rather than 404ing, a forwarded link with a stale
// index must still resolve.
func TestDeckSlide_Clamping(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/deck/sales?slide=999", nil)
	req.SetPathValue("slug", "sales")
	rec := httptest.NewRecorder()
	Deck(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	// On the last slide there is no next link.
	if strings.Contains(body, `rel="next"`) {
		t.Errorf("clamped last slide should not render a next link")
	}
	if !strings.Contains(body, `rel="prev"`) {
		t.Errorf("clamped last slide should render a prev link")
	}
}

// TestDeckPrint_AllSlides asserts ?print=1 renders the whole deck
// stacked on one no-JS page so the browser's print dialog can export
// it to PDF.
func TestDeckPrint_AllSlides(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/deck/sales?print=1", nil)
	req.SetPathValue("slug", "sales")
	rec := httptest.NewRecorder()
	Deck(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()

	mustContain := []string{
		`class="deck-print"`,
		`class="deck-print-slide prose"`,
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("print view missing %q", want)
		}
	}
	// The print view carries every slide: there must be at least as
	// many slide sections as the deck has slides. Sales has several;
	// assert more than one renders so it is clearly the stacked view.
	if got := strings.Count(body, `class="deck-print-slide prose"`); got < 2 {
		t.Errorf("print view rendered %d slides, want the full stack", got)
	}
	// The print view is no-JS like the rest of the deck, and carries
	// no slide-nav chrome.
	if strings.Contains(body, "<script") {
		t.Errorf("print view should not load any script")
	}
	if strings.Contains(body, `class="deck-nav"`) {
		t.Errorf("print view should not render slide nav chrome")
	}
}

// TestDeckSlide_BadSlideParam asserts a non-numeric ?slide value
// falls back to slide 1 instead of erroring.
func TestDeckSlide_BadSlideParam(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/deck/sales?slide=notanumber", nil)
	req.SetPathValue("slug", "sales")
	rec := httptest.NewRecorder()
	Deck(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "1 / ") {
		t.Errorf("bad slide param should fall back to slide 1")
	}
}

// TestDeck_UnknownSlug asserts an unknown deck 404s.
func TestDeck_UnknownSlug(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/deck/nope", nil)
	req.SetPathValue("slug", "nope")
	rec := httptest.NewRecorder()
	Deck(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug: got %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestDeck_PageVoice enforces the cloud-copy rule across
// every deck: property-led, not crypto-led. Implementation detail
// belongs in the RFCs, not the public pitch.
func TestDeck_PageVoice(t *testing.T) {
	t.Parallel()

	banned := []string{"Merkle", "Sepolia", "on-chain", "Base Sepolia"}
	for _, slug := range []string{"sales", "investors", "general"} {
		// Walk every slide of the deck.
		for n := 1; n <= 12; n++ {
			req := httptest.NewRequest(http.MethodGet, "/deck/"+slug+"?slide="+itoa(n), nil)
			req.SetPathValue("slug", slug)
			rec := httptest.NewRecorder()
			Deck(nil).ServeHTTP(rec, req)
			body := rec.Body.String()
			for _, tok := range banned {
				if strings.Contains(body, tok) {
					t.Errorf("deck %q slide %d contains banned cloud-copy token %q", slug, n, tok)
				}
			}
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
