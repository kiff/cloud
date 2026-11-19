package handlers

import (
	"context"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
)

// The walkthrough changes how the issue form looks, not what it asks
// the API for. What it posts must build exactly the request the stacked
// form built for the same answers.
func TestWalkthroughPostBuildsTheSameRequest(t *testing.T) {
	t.Parallel()
	before := url.Values{
		"subject": {"refund-agent"}, "actions": {"AUTO_REFUND", "MANUAL_REFUND"},
		"states.AUTO_REFUND": {"PAID"}, "quantity_kind": {"sum"}, "parameter": {"amount"},
		"limit": {"110000"}, "per_action_max": {"50000"}, "window": {"rolling_24h"},
		"on_exceed": {"refuse"}, "valid_until": {"2026-12-31"}, "measure": {"effects"},
	}
	after := url.Values{}
	for k, v := range before {
		after[k] = v
	}
	// The walkthrough picks the agent from a choice and always posts a
	// wait, which only matters when the action is held.
	after.Set("subject", "")
	after.Set("subject_choice", "refund-agent")
	after.Set("hold", "1h")

	want, err := buildIssueMandateRequest(mandateFormFromPost(before), "refunds")
	if err != nil {
		t.Fatal(err)
	}
	got, err := buildIssueMandateRequest(mandateFormFromPost(after), "refunds")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("walkthrough request differs:\n got %+v\nwant %+v", got, want)
	}
}

func TestAgentChoiceOrTypedID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		values url.Values
		want   string
	}{
		{"seen agent chosen", url.Values{"subject_choice": {"support-bot"}, "subject": {"leftover"}}, "support-bot"},
		{"another agent typed", url.Values{"subject_choice": {"other"}, "subject": {"new-agent"}}, "new-agent"},
		{"no choice posted", url.Values{"subject": {"typed-agent"}}, "typed-agent"},
	} {
		if got := mandateFormFromPost(tc.values).MandateSubj; got != tc.want {
			t.Errorf("%s: holder %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestHoldTravelsOnlyWithAsk(t *testing.T) {
	t.Parallel()
	base := pages.MandateFormValues{MandateSubj: "a", Actions: []string{"X"}, QuantityKind: "count", Limit: "1"}

	ask := base
	ask.OnExceed, ask.Hold = "owner", "24h"
	if req, err := buildIssueMandateRequest(ask, "d"); err != nil || req.HoldExpiry != "24h" {
		t.Errorf("ask: hold %q, %v", req.HoldExpiry, err)
	}
	stop := base
	stop.OnExceed, stop.Hold = "refuse", "24h"
	if req, err := buildIssueMandateRequest(stop, "d"); err != nil || req.HoldExpiry != "" {
		t.Errorf("stop: a refused action waits for nobody, got hold %q, %v", req.HoldExpiry, err)
	}
	keep := base
	keep.OnExceed = "owner"
	if req, err := buildIssueMandateRequest(keep, "d"); err != nil || req.HoldExpiry != "" {
		t.Errorf("no wait posted: the Card's own wait is kept, got %q, %v", req.HoldExpiry, err)
	}
	bad := base
	bad.OnExceed, bad.Hold = "owner", "30d"
	if _, err := buildIssueMandateRequest(bad, "d"); err == nil {
		t.Error("a wait longer than 7 days was accepted")
	}
}

func TestWalkthroughRendersOneDecisionPerStep(t *testing.T) {
	t.Parallel()
	body := collapse(renderMandateNew(t, pages.CardNewView{
		SelectedDomain: "refunds",
		Actions:        refundDomainActions(),
		Agents:         []apiclient.DomainAgent{{ID: "refund-agent", Operations: 3}},
	}))
	for i := 1; i <= 5; i++ {
		if !strings.Contains(body, `data-wk-step="`+string(rune('0'+i))+`"`) {
			t.Errorf("step %d missing", i)
		}
	}
	for _, field := range []string{
		`name="subject_choice"`, `name="subject"`, `name="actions"`, `name="quantity_kind"`,
		`name="limit"`, `name="per_action_max"`, `name="window"`, `name="on_exceed"`, `name="hold"`,
		`name="id"`, `name="valid_until"`, `name="environment"`, `name="measure"`, `name="states.AUTO_REFUND"`,
	} {
		if !strings.Contains(body, field) {
			t.Errorf("the walkthrough does not post %s", field)
		}
	}
	// One seen agent is chosen for a new Card; Ask is the default.
	if !strings.Contains(body, `value="refund-agent" checked`) {
		t.Error("the only seen agent is not chosen")
	}
	if !strings.Contains(body, `value="owner" checked`) {
		t.Error("Ask is not the default")
	}
	// The stacked form said an action past the Card is refused, even
	// when the default holds it. The walkthrough says what each route does.
	for _, wrong := range []string{"refused, not sent for approval", "is refused even though nothing is wrong"} {
		if strings.Contains(body, wrong) {
			t.Errorf("copy contradicts the default route: %q", wrong)
		}
	}
	if !strings.Contains(body, "Hold it for an approver") || !strings.Contains(body, "Refuse it") {
		t.Error("the two routes are not both offered")
	}
}

func TestChangingACardOpensOnTheReview(t *testing.T) {
	t.Parallel()
	body := renderMandateNew(t, pages.CardNewView{
		Editing: "refund-agent", SelectedDomain: "refunds", Actions: refundDomainActions(),
		Form: pages.MandateFormValues{ID: "refund-agent", MandateSubj: "someone-new", OnExceed: "refuse", Hold: "168h"},
	})
	if !strings.Contains(body, `data-step="5"`) {
		t.Error("a Card being changed does not open on the review")
	}
	if !strings.Contains(body, `value="refuse" checked`) {
		t.Error("the Card's route was not kept")
	}
	if !strings.Contains(body, `value="168h" checked`) {
		t.Error("the Card's wait was not kept")
	}
	// A holder KIFF has not seen is kept as a typed id.
	if !strings.Contains(body, `value="someone-new"`) {
		t.Error("an unseen holder was dropped")
	}
}

func TestHoldFromSecondsKeepsAnUnofferedWait(t *testing.T) {
	t.Parallel()
	for s, want := range map[int64]string{0: "10m", 600: "10m", 3600: "1h", 86400: "24h", 604800: "168h", 1800: "30m0s"} {
		if got := pages.HoldFromSeconds(s); got != want {
			t.Errorf("%d: %q, want %q", s, got, want)
		}
	}
}

func TestToolCardAgentChoiceOrTypedName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ choice, typed, want string }{
		{"refund-agent", "leftover", "refund-agent"},
		{"other", " new-agent ", "new-agent"},
		{"", "typed-agent", "typed-agent"},
	} {
		if got := toolCardAgent(tc.choice, tc.typed); got != tc.want {
			t.Errorf("choice %q typed %q: %q, want %q", tc.choice, tc.typed, got, tc.want)
		}
	}
}

// The quick Card is the same walkthrough: four steps posting the same
// fields to /cards/quick, with nothing that says an action is refused.
func TestQuickCardIsAWalkthrough(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	if err := pages.CardQuick(pages.CardQuickView{}).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	for _, want := range []string{`action="/cards/quick"`, `data-wk-step="4"`, `name="task"`, `name="subject"`, `name="limit"`, "css/walkthrough.css"} {
		if !strings.Contains(body, want) {
			t.Errorf("quick Card missing %s", want)
		}
	}
	if strings.Contains(body, `data-wk-step="5"`) {
		t.Error("quick Card has a fifth step")
	}
}
