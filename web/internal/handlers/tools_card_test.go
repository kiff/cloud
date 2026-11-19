package handlers_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/gatewayclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

// cardAPI stubs the API calls a tool Card makes.
type cardAPI struct {
	mu     sync.Mutex
	issued []map[string]any
	cards  []map[string]any
	reject string
}

func (a *cardAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case r.URL.Path == "/v1/me/agents":
		writeJSONForTest(w, http.StatusOK, map[string]any{"agents": []any{
			map[string]any{"id": "refund-agent", "recorded": true},
			map[string]any{"id": "old-agent", "recorded": true, "archived_at": time.Now().Add(-time.Hour)},
		}})
	case r.URL.Path == "/v1/me/mandates" && r.Method == http.MethodGet:
		writeJSONForTest(w, http.StatusOK, map[string]any{"mandates": a.cards})
	case r.URL.Path == "/v1/me/mandates" && r.Method == http.MethodPost:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if a.reject != "" {
			writeJSONForTest(w, http.StatusBadRequest, map[string]any{"error": a.reject})
			return
		}
		a.issued = append(a.issued, body)
		writeJSONForTest(w, http.StatusCreated, map[string]any{"id": body["id"], "subject": body["subject"], "domain": body["domain"]})
	default:
		writeJSONForTest(w, http.StatusNotFound, map[string]any{"error": "not found"})
	}
}

func toolCardRig(t *testing.T, api *cardAPI) (http.Handler, *http.Cookie) {
	t.Helper()
	apiSrv := httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(apiSrv.Close)
	gwSrv := httptest.NewServer(http.HandlerFunc((&toolsGateway{}).serve))
	t.Cleanup(gwSrv.Close)
	client, err := apiclient.NewSessionAware(apiSrv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	gw, err := gatewayclient.New(gwSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestSessionManager(t)
	dash := handlers.NewDashboardHandler(client, mgr, handlers.StaticBearerSource(), nil)
	dash.SetGateway(gw)
	mux := http.NewServeMux()
	dash.Register(mux)
	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{Subject: "alice", Bearer: "b", Email: "a@example.com", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return handlers.RequireSession(mgr)(mux), issue.Result().Cookies()[0]
}

// the cards-for-any-agent design: the form speaks in the tool's
// terms and shows no domain or contract.
func TestToolCard_FormInTheToolsTerms(t *testing.T) {
	t.Parallel()
	h, c := toolCardRig(t, &cardAPI{})
	body := send(h, c, http.MethodGet, "/tools/issue_refund/card?agent=refund-agent").Body.String()
	for _, want := range []string{`Which agent may use <span class="wk-mono">issue_refund</span>?`, `Total <span class="wk-unit">amount_eur</span>`,
		`name="agent_choice" value="refund-agent" checked`, `value="10m" checked`, "Hold it for an approver"} {
		if !strings.Contains(body, want) {
			t.Errorf("form missing %q", want)
		}
	}
	for _, hidden := range []string{"old-agent", "gateway", "domain", "contract"} {
		if strings.Contains(strings.ToLower(pageMain(body)), hidden) {
			t.Errorf("form shows %q", hidden)
		}
	}
	if rec := send(h, c, http.MethodGet, "/tools/nope/card"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown tool = %d", rec.Code)
	}
}

// pageMain is the page's own content, without the shared chrome.
func pageMain(body string) string {
	if i := strings.Index(body, `<main class="wk-main">`); i >= 0 {
		if j := strings.Index(body[i:], "</main>"); j >= 0 {
			return body[i : i+j]
		}
	}
	if i := strings.Index(body, `<section class="dash`); i >= 0 {
		return body[i:]
	}
	return body
}

// The Card is issued on the gateway domain behind the scenes, with the
// tool as its action and its amount argument as the quantity.
func TestToolCard_IssuesOnTheGatewayDomain(t *testing.T) {
	t.Parallel()
	api := &cardAPI{}
	h, c := toolCardRig(t, api)
	form := url.Values{"agent": {"refund-agent"}, "per_call": {"500"}, "total": {"2,000"}, "calls": {"20"},
		"window": {"calendar_day"}, "on_exceed": {"owner"}, "hold": {"24h"}}
	rec := postForm(h, c, "/tools/issue_refund/card", form)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/cards/refund-agent-issue-refund" {
		t.Fatalf("issue = %d → %q\n%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	got, _ := json.Marshal(api.issued[0])
	want := `{"aggregates":[{"limit":2000,"quantity":"sum(amount_eur)","window":"calendar_day"},{"limit":20,"quantity":"count","window":"calendar_day"}],` +
		`"domain":"gateway","grants":[{"action":"issue_refund","entity_states":null,"per_action_max":[{"max":500,"quantity":"sum(amount_eur)"}]}],` +
		`"hold_expiry":"24h","id":"refund-agent-issue-refund","on_exceed":"owner","subject":"refund-agent"}`
	if string(got) != want {
		t.Fatalf("issued\n%s\nwant\n%s", got, want)
	}

	// Refuse: no hold expiry is sent.
	form.Set("on_exceed", "refuse")
	postForm(h, c, "/tools/issue_refund/card", form)
	if _, ok := api.issued[1]["hold_expiry"]; ok || api.issued[1]["on_exceed"] != "refuse" {
		t.Errorf("refuse card = %v", api.issued[1])
	}
}

func TestToolCard_ExplicitMeasuresRoundTrip(t *testing.T) {
	t.Parallel()
	api := &cardAPI{}
	h, c := toolCardRig(t, api)
	form := url.Values{"agent": {"refund-agent"}, "total": {"2000"}, "calls": {"20"}, "window": {"calendar_day"}, "on_exceed": {"owner"}, "hold": {"10m"}, "amount_measure": {"attempts"}, "count_measure": {"effects"}}
	response := postForm(h, c, "/tools/issue_refund/card", form)
	if response.Code != http.StatusSeeOther || len(api.issued) != 1 {
		t.Fatalf("override: %d %s", response.Code, response.Body.String())
	}
	aggregates := api.issued[0]["aggregates"].([]any)
	if aggregates[0].(map[string]any)["measure"] != "attempts" || aggregates[1].(map[string]any)["measure"] != "effects" {
		t.Fatal("measures changed", aggregates)
	}
	api.mu.Lock()
	api.cards = []map[string]any{api.issued[0]}
	api.mu.Unlock()
	body := send(h, c, http.MethodGet, "/tools/issue_refund/card?agent=refund-agent").Body.String()
	if !strings.Contains(body, `value="attempts" selected`) || !strings.Contains(body, `value="effects" selected`) {
		t.Fatal("edit lost overrides", body)
	}
}

func TestToolCard_RefusesWhatBindsNothing(t *testing.T) {
	t.Parallel()
	api := &cardAPI{}
	h, c := toolCardRig(t, api)
	base := url.Values{"agent": {"refund-agent"}, "window": {"calendar_day"}, "on_exceed": {"owner"}, "hold": {"10m"}}
	for name, c2 := range map[string]struct {
		set  map[string]string
		want string
	}{
		"no total":      {map[string]string{"per_call": "500"}, "A Card with no total limits nothing over time."},
		"no agent":      {map[string]string{"agent": "", "calls": "5"}, "Choose the agent"},
		"bad number":    {map[string]string{"calls": "-3"}, "has to be a whole number above zero"},
		"bad window":    {map[string]string{"calls": "5", "window": "forever"}, "Choose the period"},
		"bad hold":      {map[string]string{"calls": "5", "hold": "3s"}, "how long a held call waits"},
		"hold too long": {map[string]string{"calls": "5", "hold": "200h"}, "how long a held call waits"},
		"bad on_exceed": {map[string]string{"calls": "5", "on_exceed": "maybe"}, "outside the Card"},
	} {
		form := url.Values{}
		for k, v := range base {
			form[k] = v
		}
		for k, v := range c2.set {
			form.Set(k, v)
		}
		rec := postForm(h, c, "/tools/issue_refund/card", form)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c2.want) {
			t.Errorf("%s: %d, want %q", name, rec.Code, c2.want)
		}
	}
	if len(api.issued) != 0 {
		t.Fatalf("issued %v", api.issued)
	}
	api.reject = "action \"issue_refund\" is not declared in this domain"
	rec := postForm(h, c, "/tools/issue_refund/card", url.Values{"agent": {"refund-agent"}, "calls": {"5"}, "window": {"calendar_day"}, "on_exceed": {"refuse"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "The Card was not issued: action") {
		t.Fatalf("api refusal: %d", rec.Code)
	}
}

// One Card per agent and tool: issuing again changes it while in force;
// a revoked one, or a Card of that name on another domain, is never
// reissued as a tool Card.
func TestToolCard_IDs(t *testing.T) {
	t.Parallel()
	revoked := time.Now().Add(-time.Hour)
	refund := []any{map[string]any{"action": "issue_refund"}}
	api := &cardAPI{cards: []map[string]any{
		{"id": "refund-agent-issue-refund", "subject": "refund-agent", "domain": "gateway", "grants": refund, "revoked_at": revoked},
		{"id": "refund-agent-issue-refund-2", "subject": "refund-agent", "domain": "card-refund", "grants": refund},
		{"id": "refund-agent-issue-refund-3", "subject": "refund-agent", "domain": "gateway", "grants": refund},
	}}
	h, c := toolCardRig(t, api)
	form := url.Values{"agent": {"refund-agent"}, "calls": {"5"}, "window": {"calendar_day"}, "on_exceed": {"refuse"}}
	rec := followFlashTo(t, h, c, postForm(h, c, "/tools/issue_refund/card", form), "/cards/refund-agent-issue-refund-3")
	if !strings.Contains(rec, "refund-agent has new terms for issue_refund: 5 calls per day.") {
		t.Errorf("flash: %s", rec)
	}
}

// Card names are slugs, so different agents and tools can share one. A
// Card under the name that is another agent's, held by this agent only
// as a second holder, or granting more than this tool is never reissued:
// that would move it, drop its other holders, or drop its other grants.
func TestToolCard_NameCollisionNeverReissuesAnotherCard(t *testing.T) {
	t.Parallel()
	refund := []any{map[string]any{"action": "issue_refund"}}
	api := &cardAPI{cards: []map[string]any{
		{"id": "refund-agent-issue-refund", "subject": "refund_agent", "domain": "gateway", "grants": refund,
			"aggregates": []any{map[string]any{"quantity": "count", "limit": 7, "window": "rolling_1h"}}},
		{"id": "refund-agent-issue-refund-2", "subject": "refund-agent", "domain": "gateway",
			"grants": []any{map[string]any{"action": "issue_refund"}, map[string]any{"action": "quote"}}},
		{"id": "refund-agent-issue-refund-3", "subject": "billing-agent", "subjects": []string{"billing-agent", "refund-agent"},
			"domain": "gateway", "grants": refund},
	}}
	h, c := toolCardRig(t, api)
	form := url.Values{"agent": {"refund-agent"}, "calls": {"5"}, "window": {"calendar_day"}, "on_exceed": {"refuse"}}
	rec := followFlashTo(t, h, c, postForm(h, c, "/tools/issue_refund/card", form), "/cards/refund-agent-issue-refund-4")
	if !strings.Contains(rec, "refund-agent can now use issue_refund") {
		t.Errorf("flash: %s", rec)
	}
	if len(api.issued) != 1 || api.issued[0]["id"] != "refund-agent-issue-refund-4" || api.issued[0]["subject"] != "refund-agent" {
		t.Fatalf("issued %v", api.issued)
	}

	// Change limits must not start from another agent's terms either.
	page := send(h, c, http.MethodGet, "/tools/issue_refund/card?agent=refund-agent").Body.String()
	if !strings.Contains(page, `name="calls"`) || strings.Contains(page, `value="7"`) {
		t.Errorf("the form was prefilled from refund_agent's Card")
	}
}

// followFlashTo checks the redirect and returns its flash message.
func followFlashTo(t *testing.T, h http.Handler, c *http.Cookie, rec *httptest.ResponseRecorder, want string) string {
	t.Helper()
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
		t.Fatalf("redirect = %d → %q, want %q", rec.Code, rec.Header().Get("Location"), want)
	}
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == session.FlashCookieName {
			payload, _, _ := strings.Cut(ck.Value, ".")
			b, _ := base64.RawURLEncoding.DecodeString(payload)
			return string(b)
		}
	}
	return ""
}

// A Card issued from the card form without a name is named after its
// agent. Another agent's Card under that name ("refund_agent" and
// "refund-agent" share "limit-refund-agent") is never changed: the new
// Card takes the next free name.
func TestCardForm_UnnamedCardNeverTakesAnotherAgentsCard(t *testing.T) {
	t.Parallel()
	api := &cardAPI{cards: []map[string]any{
		{"id": "limit-refund-agent", "subject": "refund_agent", "domain": "refunds",
			"grants": []any{map[string]any{"action": "ISSUE_REFUND"}}},
	}}
	h, c := toolCardRig(t, api)
	form := url.Values{"subject": {"refund-agent"}, "domain": {"refunds"}, "actions": {"ISSUE_REFUND"},
		"quantity_kind": {"count"}, "limit": {"3"}, "window": {"calendar_day"}}
	rec := postForm(h, c, "/cards", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(api.issued) != 1 || api.issued[0]["id"] != "limit-refund-agent-2" || api.issued[0]["subject"] != "refund-agent" {
		t.Fatalf("issued %v; want a new limit-refund-agent-2 for refund-agent", api.issued)
	}

	// The same agent's own unnamed Card is still changed in place.
	api.cards = append(api.cards, map[string]any{"id": "limit-refund-agent-2", "subject": "refund-agent", "domain": "refunds"})
	api.issued = nil
	postForm(h, c, "/cards", form)
	if len(api.issued) != 1 || api.issued[0]["id"] != "limit-refund-agent-2" {
		t.Fatalf("issued %v; want refund-agent's own limit-refund-agent-2 changed", api.issued)
	}
}

// A tool Card's total can be counted over the Card's whole life. The form
// offers it, the Card is issued with window "lifetime", and the terms read
// "in total", never "per day".
func TestToolCard_LifetimeTotal(t *testing.T) {
	t.Parallel()
	api := &cardAPI{}
	h, c := toolCardRig(t, api)
	page := send(h, c, http.MethodGet, "/tools/issue_refund/card?agent=refund-agent").Body.String()
	if !strings.Contains(page, `value="lifetime"`) || !strings.Contains(page, "Whole life") {
		t.Fatalf("the form does not offer a whole-life total")
	}
	form := url.Values{"agent": {"refund-agent"}, "calls": {"5"}, "window": {"lifetime"}, "on_exceed": {"refuse"}}
	rec := followFlashTo(t, h, c, postForm(h, c, "/tools/issue_refund/card", form), "/cards/refund-agent-issue-refund")
	if !strings.Contains(rec, "5 calls in total") {
		t.Errorf("flash: %s", rec)
	}
	if len(api.issued) != 1 {
		t.Fatalf("issued %v", api.issued)
	}
	aggs, _ := api.issued[0]["aggregates"].([]any)
	if len(aggs) != 1 || aggs[0].(map[string]any)["window"] != "lifetime" {
		t.Fatalf("aggregates %v", api.issued[0]["aggregates"])
	}
}
