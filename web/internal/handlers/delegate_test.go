package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func delegateDo(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	Delegate().ServeHTTP(rec, req)
	return rec
}

func TestDelegate_Page(t *testing.T) {
	t.Parallel()
	rec := delegateDo(t, httptest.NewRequest(http.MethodGet, "/delegate", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<title>Delegate to an agent with a Card | KIFF</title>",
		`<meta name="description" content="Write what you are willing to delegate`,
		`property="og:title" content="Delegate to an agent with a Card | KIFF"`,
		"Skills belong to agents.",
		// the sentence, taken apart, with its grammar labels
		`<span class="dg-part dg-r-action" tabindex="0"><mark>resolve refunds</mark> <small>action</small>`,
		`<mark>up to €500 each</mark> <small>per action</small>`,
		// the match suggests an agent; the owner approves
		`name="step" value="a:support">Approve: give the Card to Support agent</button>`,
		// both halves of the gallery
		">Business</h3>", ">Personal</h3>", `id="card-flight-rescue"`, `id="card-car-negotiator"`,
		"/static/js/htmx.min.js",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	for _, bad := range []string{"prototype", "concept lab", "noindex", "hypothesis"} {
		if strings.Contains(strings.ToLower(body), bad) {
			t.Errorf("page should read as a product, found %q", bad)
		}
	}
}

// Letting the agent run on its own: no approval per action, every new
// row marked.
func TestDelegate_AutoRun(t *testing.T) {
	t.Parallel()
	form := url.Values{"card": {"eu-refunds"}, "log": {"a:support"}, "step": {"auto"}}
	req := httptest.NewRequest(http.MethodPost, "/delegate/step", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	body := delegateDo(t, req).Body.String()
	if n := strings.Count(body, "is-new"); n != 4 {
		t.Errorf("new rows marked = %d, want 4", n)
	}
	if !strings.Contains(body, "with nobody approving them") || !strings.Contains(body, "€29,050") {
		t.Errorf("auto-run fragment wrong: %.300s", body)
	}
}

// The five-screen walkthrough is now three lines on the page, with no
// fragment endpoint behind it: the page scrolls like any other.
func TestDelegate_HowRules(t *testing.T) {
	t.Parallel()
	body := delegateDo(t, httptest.NewRequest(http.MethodGet, "/delegate", nil)).Body.String()
	for _, want := range []string{
		`id="how"`,
		"The per-action limit catches one large mistake; the total catches many small ones.",
		"Only the Card authorizes it, and only you change the Card.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, "/delegate/how") || strings.Contains(body, `id="dg-how"`) {
		t.Error("the paged walkthrough should be gone")
	}
	if rec := delegateDo(t, httptest.NewRequest(http.MethodGet, "/delegate/how?n=2", nil)); rec.Code != http.StatusNotFound {
		t.Errorf("/delegate/how status %d, want 404", rec.Code)
	}
}

func postStep(t *testing.T, card, log, step string) string {
	t.Helper()
	form := url.Values{"card": {card}, "log": {log}, "step": {step}}
	req := httptest.NewRequest(http.MethodPost, "/delegate/step", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := delegateDo(t, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("want a fragment, got %d", rec.Code)
	}
	return rec.Body.String()
}

// Outside the Card, the work comes home: a Needs you panel with the three
// choices, and the limit it is outside of marked on the Card.
func TestDelegate_OutsideComesHome(t *testing.T) {
	t.Parallel()
	body := postStep(t, "eu-refunds", "a:support,r,r", "r")
	for _, want := range []string{
		`id="dg-live" class="dg-live dg-last-held"`,
		"Needs you",
		"Your Card allows up to €500 per action.",
		"€1,000 outside your delegation.",
		"Decision source: AI model",
		"This is outside what you delegated. What do you want to do?",
		`value="ok">Approve once</button>`,
		`value="change">Change Card: up to €1,500 per action</button>`,
		`value="no">Reject</button>`,
		`dg-term dg-r-peraction dg-hit`,
		`class="dg-row dg-row-held is-new"`,
		`name="log" value="a:support,r,r,r"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("fragment missing %q", want)
		}
	}
	for _, bad := range []string{"refused", "Refused", "violation"} {
		if strings.Contains(body, bad) {
			t.Errorf("exception should not read as a failure; found %q", bad)
		}
	}
}

func TestDelegate_OwnerAnswers(t *testing.T) {
	t.Parallel()
	once := postStep(t, "eu-refunds", "a:support,r,r,r", "ok")
	if !strings.Contains(once, "Done, this once. The Card is unchanged") || !strings.Contains(once, "approved once") || !strings.Contains(once, "≤ €500") {
		t.Errorf("approve once: %.300s", once)
	}
	changed := postStep(t, "eu-refunds", "a:support,r,r,r", "change")
	for _, want := range []string{"≤ €1,500", "dg-grew", "rev 3", "Changed the Card: per action €500 → €1,500", "done after change", "€27,950"} {
		if !strings.Contains(changed, want) {
			t.Errorf("change card missing %q", want)
		}
	}
	rejected := postStep(t, "eu-refunds", "a:support,r,r,r", "no")
	if !strings.Contains(rejected, "Rejected. Nothing happened") || strings.Contains(rejected, "Needs you") {
		t.Errorf("reject: %.300s", rejected)
	}
}

func TestDelegate_MoveKeepsBalance(t *testing.T) {
	t.Parallel()
	form := url.Values{"card": {"eu-refunds"}, "log": {"a:support,r"}, "step": {"m:backoffice"}}
	req := httptest.NewRequest(http.MethodPost, "/delegate/step", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	body := delegateDo(t, req).Body.String()
	for _, want := range []string{"held by Back-office agent", "€29,800", "Card moved from Support agent to Back-office agent"} {
		if !strings.Contains(body, want) {
			t.Errorf("fragment missing %q", want)
		}
	}
}

// Without JavaScript the form still works: post, redirect, render.
func TestDelegate_NoJSRedirects(t *testing.T) {
	t.Parallel()
	form := url.Values{"card": {"flight-rescue"}, "log": {""}, "step": {"a:concierge"}}
	req := httptest.NewRequest(http.MethodPost, "/delegate/step", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := delegateDo(t, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/delegate?card=flight-rescue&log=a%3Aconcierge#try" {
		t.Fatalf("status %d location %q", rec.Code, rec.Header().Get("Location"))
	}
}

// A forged step is dropped by the replay rather than trusted.
func TestDelegate_ForgedStepIgnored(t *testing.T) {
	t.Parallel()
	form := url.Values{"card": {"eu-refunds"}, "log": {""}, "step": {"a:writer"}}
	req := httptest.NewRequest(http.MethodPost, "/delegate/step", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	body := delegateDo(t, req).Body.String()
	if !strings.Contains(body, `name="log" value=""`) || !strings.Contains(body, "not given to an agent") {
		t.Fatalf("forged assignment was applied: %.300s", body)
	}
}

// The phone bar opens the tab where the change is and offers the one
// next action, so a demo is a few taps with no scrolling.
func TestDelegate_PhoneBar(t *testing.T) {
	t.Parallel()
	start := delegateDo(t, httptest.NewRequest(http.MethodGet, "/delegate", nil)).Body.String()
	for _, want := range []string{`id="dg-tab-queue" checked`, `name="step" value="a:support">Approve: give it to Support agent</button>`} {
		if !strings.Contains(start, want) {
			t.Errorf("start missing %q", want)
		}
	}
	ran := postStep(t, "eu-refunds", "a:support", "auto")
	for _, want := range []string{`id="dg-tab-queue" checked`, `<b class="dg-badge">1</b>`, `<label class="dg-btn dg-btn-primary" for="dg-tab-queue">Needs you: decide</label>`, `class="dg-tabbar-news"`} {
		if !strings.Contains(ran, want) {
			t.Errorf("after auto-run missing %q", want)
		}
	}
	changed := postStep(t, "eu-refunds", "a:support,auto", "change")
	if !strings.Contains(changed, `id="dg-tab-card" checked`) || strings.Contains(changed, "dg-badge") {
		t.Errorf("after change: should open the Card tab with an empty queue")
	}
}

// a review.
func TestDelegate_ReviewFixes(t *testing.T) {
	t.Parallel()
	// Change Card names and changes every limit the action exceeds.
	held := postStep(t, "home-away", "a:concierge", "auto")
	if !strings.Contains(held, `value="change">Change Card: up to €2,400 per action and €2,700 this month</button>`) {
		t.Errorf("change label should cover both limits")
	}
	changed := postStep(t, "home-away", "a:concierge,auto", "change")
	if strings.Contains(changed, "€-") || !strings.Contains(changed, "€5") ||
		strings.Count(changed, "dg-grew") != 2 {
		t.Errorf("change must keep the Card non-negative and mark both lines")
	}
	// Owner controls stay reachable on phones: on the Card itself.
	working := postStep(t, "eu-refunds", "a:support", "r")
	if !strings.Contains(working, `class="dg-card-actions"`) ||
		!strings.Contains(working, `name="step" value="x">Revoke</button>`) ||
		!strings.Contains(working, `value="m:backoffice">Move to Back-office agent</button>`) {
		t.Errorf("owner controls missing from the Card")
	}
	// The page says what is sample, inside the live frame so phones see
	// it too; everything it shows works in KIFF Cloud, so it claims no gap.
	for _, body := range []string{working, delegateDo(t, httptest.NewRequest(http.MethodGet, "/delegate", nil)).Body.String()} {
		if !strings.Contains(body, `class="dg-disclosure"`) || !strings.Contains(body, "Sample agents and actions.") ||
			strings.Contains(body, "not in KIFF Cloud yet") || strings.Contains(body, "Moving a Card between agents") {
			t.Errorf("disclosure missing")
		}
	}
}
