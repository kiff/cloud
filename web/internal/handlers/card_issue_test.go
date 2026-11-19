package handlers

import (
	"bytes"
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
)

func renderMandateNew(t *testing.T, v pages.CardNewView) string {
	t.Helper()
	var buf bytes.Buffer
	if err := pages.CardNew(v).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render mandate form: %v", err)
	}
	return buf.String()
}

func refundDomainActions() []apiclient.DomainAction {
	return []apiclient.DomainAction{
		{
			Name:          "AUTO_REFUND",
			AllowedStates: []string{"PAID"},
			Risk:          "medium",
			Approval:      "never",
			Parameters: []apiclient.DomainParameter{
				{Name: "amount", Type: "int"},
				{Name: "reason", Type: "string"},
			},
		},
		{
			Name:          "ISSUE_CREDIT",
			AllowedStates: []string{"PAID"},
			Risk:          "medium",
			Parameters: []apiclient.DomainParameter{
				{Name: "amount", Type: "int"},
				{Name: "note", Type: "string"},
			},
		},
		{
			Name:          "MARK_PAID",
			AllowedStates: []string{"CREATED"},
			Risk:          "low",
			Parameters: []apiclient.DomainParameter{
				{Name: "payment_id", Type: "string"},
			},
		},
	}
}

// The form may only offer a quantity the domain declares as a number.
// Offering a string is a refusal the person discovers after filling in
// a ceiling; omitting a numeric one costs them typing.
func TestFormOffersOnlyNumericParameters(t *testing.T) {
	t.Parallel()
	v := pages.CardNewView{Actions: refundDomainActions()}

	got := map[string]pages.SummableParameter{}
	for _, p := range v.SummableParameters() {
		got[p.Name] = p
	}
	if _, ok := got["reason"]; ok {
		t.Error("a string parameter was offered as something to add up")
	}
	if _, ok := got["payment_id"]; ok {
		t.Error("a string parameter was offered as something to add up")
	}
	amount, ok := got["amount"]
	if !ok {
		t.Fatalf("the numeric parameter was not offered: %+v", v.SummableParameters())
	}
	// A mandate may sum a parameter only where every granted action
	// declares it. amount is on two of three actions, so ticking
	// MARK_PAID with it would be refused — the form says so before
	// the refusal rather than after.
	if amount.Universal() {
		t.Error("amount reported as declared on every action; MARK_PAID does not declare it")
	}
	if amount.DeclaredOn != 2 || amount.OfActions != 3 {
		t.Errorf("amount reach = %d of %d, want 2 of 3", amount.DeclaredOn, amount.OfActions)
	}
	if !strings.Contains(amount.Label(), "2 of 3") {
		t.Errorf("label does not show the reach: %q", amount.Label())
	}
}

// A domain with no numeric parameter can still be bounded by counting.
// The form has to say that is the only option and why, or it reads as
// broken.
func TestFormFallsBackToCountingWhenNothingIsSummable(t *testing.T) {
	t.Parallel()
	v := pages.CardNewView{
		Actions: []apiclient.DomainAction{{
			Name:       "DELETE_CUSTOMER",
			Parameters: []apiclient.DomainParameter{{Name: "customer_id", Type: "string"}},
		}},
	}
	if !v.CountingOnly() {
		t.Fatal("a domain with no numeric parameter should fall back to counting")
	}
	body := collapse(renderMandateNew(t, v))
	if !strings.Contains(body, "Counting how many times these actions run") {
		t.Error("the form does not say it is counting")
	}
	if !strings.Contains(body, "this domain declares none") {
		t.Error("the form does not say why summing is unavailable")
	}
	if strings.Contains(body, `name="parameter"`) {
		t.Error("a parameter picker was rendered with nothing to pick")
	}
}

func TestMandateFormStateNamesEveryBranch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		v    pages.CardNewView
		want string
	}{
		{"unconfigured", pages.CardNewView{NotConfiguredMsg: "no api"}, "unconfigured"},
		{"load error", pages.CardNewView{LoadError: "boom"}, "error"},
		{"no domain", pages.CardNewView{}, "nodomain"},
		{"ready", pages.CardNewView{Actions: refundDomainActions()}, "ready"},
		// A failure to read the domain must not render as "you have no
		// actions", which reads as a reason to go make one.
		{"error beats no domain", pages.CardNewView{LoadError: "boom"}, "error"},
	} {
		if got := tc.v.State(); got != tc.want {
			t.Errorf("%s: State() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A refusal must come back with the fields still filled in. Someone
// reading "MARK_PAID does not declare amount" wants to untick one box,
// not retype six fields.
func TestRefusalKeepsWhatWasTyped(t *testing.T) {
	t.Parallel()
	v := pages.CardNewView{
		Actions:  refundDomainActions(),
		Rejected: `mandate: sum(amount) sums "amount", which action MARK_PAID does not declare`,
		Form: pages.MandateFormValues{
			MandateSubj:  "refund-agent",
			Actions:      []string{"AUTO_REFUND", "MARK_PAID"},
			QuantityKind: "sum",
			Parameter:    "amount",
			Limit:        "110000",
			Window:       "rolling_24h",
		},
	}
	body := renderMandateNew(t, v)

	if !strings.Contains(body, "MARK_PAID does not declare") {
		t.Error("the cloud's own explanation was not shown")
	}
	for _, want := range []string{
		`value="refund-agent"`,
		`value="110000"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("form lost %s", want)
		}
	}
	if !v.IsChecked("AUTO_REFUND") || !v.IsChecked("MARK_PAID") {
		t.Error("ticked actions were lost")
	}
	if v.IsChecked("ISSUE_CREDIT") {
		t.Error("an unticked action came back ticked")
	}
	if v.SelectedWindow() != "rolling_24h" {
		t.Errorf("SelectedWindow() = %q, want the submitted rolling_24h", v.SelectedWindow())
	}
}

// The window defaults to the calendar day: what a person means by "a
// day" when they say what an agent may spend in one.
func TestWindowDefaultsToTheCalendarDay(t *testing.T) {
	t.Parallel()
	v := pages.CardNewView{Actions: refundDomainActions()}
	if v.SelectedWindow() != "calendar_day" {
		t.Errorf("SelectedWindow() = %q, want calendar_day", v.SelectedWindow())
	}
}

func TestBuildIssueMandateRequest(t *testing.T) {
	t.Parallel()
	f := pages.MandateFormValues{
		MandateSubj:  "refund-agent",
		Actions:      []string{"AUTO_REFUND", "ISSUE_CREDIT"},
		QuantityKind: "sum",
		Parameter:    "amount",
		Limit:        "110000",
		Window:       "calendar_day",
		Environment:  "production",
	}
	req, err := buildIssueMandateRequest(f, "refund-control")
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if req.Subject != "refund-agent" || req.Domain != "refund-control" || req.Environment != "production" {
		t.Errorf("identity fields wrong: %+v", req)
	}
	if len(req.Grants) != 2 || req.Grants[0].Action != "AUTO_REFUND" {
		t.Errorf("grants wrong: %+v", req.Grants)
	}
	if len(req.Aggregates) != 1 {
		t.Fatalf("want one aggregate, got %+v", req.Aggregates)
	}
	a := req.Aggregates[0]
	if a.Quantity != "sum(amount)" || a.Limit != 110000 || a.Window != "calendar_day" {
		t.Errorf("aggregate wrong: %+v", a)
	}
	// A limit left unnamed is named from the subject, so issuing a
	// second unnamed one for the same agent raises the first rather
	// than quietly stacking a second ceiling nobody named.
	if req.ID != "limit-refund-agent" {
		t.Errorf("derived id = %q, want limit-refund-agent", req.ID)
	}
}

func TestBuildIssueMandateRequestRefusesWhatTheFormCanSee(t *testing.T) {
	t.Parallel()
	base := pages.MandateFormValues{
		MandateSubj:  "refund-agent",
		Actions:      []string{"AUTO_REFUND"},
		QuantityKind: "count",
		Limit:        "10",
	}
	for _, tc := range []struct {
		name   string
		mutate func(f *pages.MandateFormValues)
		want   string
	}{
		{"no subject", func(f *pages.MandateFormValues) { f.MandateSubj = "" }, "actor id"},
		{"no actions", func(f *pages.MandateFormValues) { f.Actions = nil }, "bound nothing"},
		{"ceiling of zero", func(f *pages.MandateFormValues) { f.Limit = "0" }, "revocation"},
		{"ceiling not a number", func(f *pages.MandateFormValues) { f.Limit = "lots" }, "whole number"},
		{"summing nothing", func(f *pages.MandateFormValues) {
			f.QuantityKind = "sum"
			f.Parameter = ""
		}, "parameter to add up"},
	} {
		f := base
		f.Actions = append([]string{}, base.Actions...)
		tc.mutate(&f)
		_, err := buildIssueMandateRequest(f, "refund-control")
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: message %q does not mention %q", tc.name, err.Error(), tc.want)
		}
	}
}

// A count does not name a parameter, even if one was left selected in
// the form from an earlier attempt.
func TestCountingIgnoresALeftoverParameter(t *testing.T) {
	t.Parallel()
	req, err := buildIssueMandateRequest(pages.MandateFormValues{
		MandateSubj:  "refund-agent",
		Actions:      []string{"AUTO_REFUND"},
		QuantityKind: "count",
		Parameter:    "amount",
		Limit:        "3",
	}, "refund-control")
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if req.Aggregates[0].Quantity != "count" {
		t.Errorf("quantity = %q, want count", req.Aggregates[0].Quantity)
	}
}

func TestMandateIDFromSubject(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"refund-agent":    "limit-refund-agent",
		"agent://refunds": "limit-agent-refunds",
		"Ops Bot 7":       "limit-ops-bot-7",
		"  spaced  out  ": "limit-spaced-out",
	} {
		if got := mandateIDFromSubject(in); got != want {
			t.Errorf("mandateIDFromSubject(%q) = %q, want %q", in, got, want)
		}
	}
}

// The form promises the card says what the agent may do and when it
// stops, so both have to reach the request. Before this, it said so and
// sent neither: every card was issued over every state the action
// allows, with no expiry.
func TestGrantsCarryTheStatesAndTheExpiry(t *testing.T) {
	t.Parallel()
	f := pages.MandateFormValues{
		MandateSubj:  "refund-agent",
		Actions:      []string{"AUTO_REFUND", "ISSUE_CREDIT"},
		QuantityKind: "count",
		Limit:        "5",
		States:       map[string][]string{"AUTO_REFUND": {"PAID"}},
		ValidUntil:   "2026-12-31",
	}
	req, err := buildIssueMandateRequest(f, "refund-control")
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if got := req.Grants[0].EntityStates; len(got) != 1 || got[0] != "PAID" {
		t.Errorf("AUTO_REFUND states = %v, want [PAID]", got)
	}
	// An action with nothing ticked keeps the contract's own states. A
	// card narrows; it never widens, so "none selected" is not "none
	// allowed".
	if got := req.Grants[1].EntityStates; len(got) != 0 {
		t.Errorf("ISSUE_CREDIT states = %v, want none", got)
	}
	if req.ValidUntil == nil {
		t.Fatal("expiry was dropped")
	}
	// The end of the chosen day, not its start: someone who types
	// today's date means the card is good for today.
	if want := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC); !req.ValidUntil.Equal(want) {
		t.Errorf("expiry = %v, want %v", req.ValidUntil, want)
	}
}

func TestCardWithoutAnExpiryHoldsUntilRevoked(t *testing.T) {
	t.Parallel()
	req, err := buildIssueMandateRequest(pages.MandateFormValues{
		MandateSubj: "refund-agent", Actions: []string{"AUTO_REFUND"},
		QuantityKind: "count", Limit: "5",
	}, "refund-control")
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if req.ValidUntil != nil {
		t.Errorf("a blank expiry became %v", req.ValidUntil)
	}
}

func TestAnUnreadableExpiryIsRefusedRatherThanIgnored(t *testing.T) {
	t.Parallel()
	_, err := buildIssueMandateRequest(pages.MandateFormValues{
		MandateSubj: "refund-agent", Actions: []string{"AUTO_REFUND"},
		QuantityKind: "count", Limit: "5", ValidUntil: "31/12/2026",
	}, "refund-control")
	if err == nil {
		t.Fatal("a date the form could not read was accepted, which issues a card that never expires")
	}
}

func TestStatesFromForm(t *testing.T) {
	t.Parallel()
	got := statesFromForm(url.Values{
		"actions":             {"AUTO_REFUND"},
		"states.AUTO_REFUND":  {"PAID", "PARTIALLY_REFUNDED"},
		"states.ISSUE_CREDIT": {},
		"subject":             {"refund-agent"},
	})
	if len(got) != 1 || len(got["AUTO_REFUND"]) != 2 {
		t.Fatalf("states = %+v", got)
	}
	// Only the prefixed keys, and only the ones carrying a value.
	if _, ok := got["ISSUE_CREDIT"]; ok {
		t.Error("an action with no state ticked must not appear, or it would read as a narrowing nobody asked for")
	}
}

// The form's optional per-action limit caps each granted action on the
// summed parameter, and is refused where it cannot mean anything.
func TestBuildIssueMandateRequestPerActionLimit(t *testing.T) {
	t.Parallel()
	f := pages.MandateFormValues{
		MandateSubj: "refund-agent", Actions: []string{"AUTO_REFUND", "ISSUE_CREDIT"},
		QuantityKind: "sum", Parameter: "amount", Limit: "3000000", PerActionMax: "50000",
	}
	req, err := buildIssueMandateRequest(f, "refund-control")
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	for _, g := range req.Grants {
		if len(g.PerActionMax) != 1 || g.PerActionMax[0].Quantity != "sum(amount)" || g.PerActionMax[0].Max != 50000 {
			t.Errorf("%s: per-action = %+v", g.Action, g.PerActionMax)
		}
	}

	none := f
	none.PerActionMax = ""
	if req, _ := buildIssueMandateRequest(none, "refund-control"); len(req.Grants[0].PerActionMax) != 0 {
		t.Error("an empty per-action field must send no per-action limit")
	}
	for name, mutate := range map[string]func(*pages.MandateFormValues){
		"counting actions": func(f *pages.MandateFormValues) { f.QuantityKind = "count" },
		"zero":             func(f *pages.MandateFormValues) { f.PerActionMax = "0" },
		"not a number":     func(f *pages.MandateFormValues) { f.PerActionMax = "lots" },
	} {
		bad := f
		mutate(&bad)
		if _, err := buildIssueMandateRequest(bad, "refund-control"); err == nil {
			t.Errorf("%s: want a refusal", name)
		}
	}
}

// The form's choice of what happens outside the Card reaches the API; an
// unknown value is refused rather than silently becoming a default.
func TestBuildIssueMandateRequestCarriesOnExceed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{{"", ""}, {"owner", "owner"}, {"refuse", "refuse"}} {
		f := pages.MandateFormValues{MandateSubj: "a", Actions: []string{"X"}, QuantityKind: "count", Limit: "1", OnExceed: tc.in}
		req, err := buildIssueMandateRequest(f, "d")
		if err != nil || req.OnExceed != tc.want {
			t.Errorf("%q: got %q, %v", tc.in, req.OnExceed, err)
		}
	}
	f := pages.MandateFormValues{MandateSubj: "a", Actions: []string{"X"}, QuantityKind: "count", Limit: "1", OnExceed: "keep"}
	if _, err := buildIssueMandateRequest(f, "d"); err == nil {
		t.Error("unknown policy accepted")
	}
}
