package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// consentAPI fakes the API's OAuth routes, recording what consent sent.
type consentAPI struct {
	mu           sync.Mutex
	canConsent   bool
	linkAgent    string // the request came through a link naming this agent
	wrongAccount bool
	previous     string
	conflict     bool   // consent answers 409 (a new agent name in use)
	cardsFail    bool   // the Cards list answers 500
	agents       string // GET /v1/me/agents body; empty means support and billing
	sent         map[string]string
}

func (f *consentAPI) client(t *testing.T) *apiclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body map[string]string
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/oauth/requests/kr_1":
			_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "kr_1", "client_name": "Claude Code",
				"redirect_host": "127.0.0.1:53682", "previous_agent_id": f.previous, "can_consent": f.canConsent,
				"agent_id": f.linkAgent, "wrong_account": f.wrongAccount})
		case "GET /v1/me/mandates":
			if f.cardsFail {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			// support holds one gateway Card in force; its revoked,
			// archived and other-domain Cards are not shown. billing holds
			// only a Card in another domain, so none for tool calls.
			_, _ = w.Write([]byte(`{"mandates":[
				{"id":"support-card","subject":"support","domain":"Gateway","active":true,"on_exceed":"owner","grants":[{"action":"get_order"},{"action":"draft_support_reply"}]},
				{"id":"support-guard-card","subject":"support","domain":"support","active":true,"grants":[{"action":"refund"}]},
				{"id":"billing-guard-card","subject":"billing","domain":"billing","active":true,"grants":[{"action":"change_plan"}]},
				{"id":"prod-card","subject":"ops","environment":"production","active":true,"grants":[{"action":"restart"}]},
				{"id":"empty-card","subject":"idle","active":true,"grants":[]},
				{"id":"old-card","subject":"support","active":false,"revoked_at":"2026-10-01T00:00:00Z","grants":[{"action":"refund"}]},
				{"id":"archived-card","subject":"support","active":true,"archived_at":"2026-10-01T00:00:00Z","grants":[{"action":"refund"}]}]}`))
		case "GET /v1/me/agents":
			if f.agents != "" {
				_, _ = w.Write([]byte(f.agents))
				return
			}
			_, _ = w.Write([]byte(`{"agents":[{"id":"support","sources":["card"]},{"id":"billing","sources":["key"]}]}`))
		case "POST /v1/me/agents":
			t.Error("the consent page must not create agents itself; the API does")
		case "POST /v1/oauth/consent":
			if !f.canConsent {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if f.conflict {
				w.WriteHeader(http.StatusConflict)
				return
			}
			f.sent = body
			_, _ = w.Write([]byte(`{"redirect_to":"http://127.0.0.1:53682/callback?code=c1&state=s"}`))
		case "POST /v1/oauth/deny":
			_, _ = w.Write([]byte(`{"redirect_to":"http://127.0.0.1:53682/callback?error=access_denied"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := apiclient.NewSessionAware(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func consentMux(t *testing.T, f *consentAPI) *http.ServeMux {
	mux := http.NewServeMux()
	NewOAuthConsentHandler(f.client(t), nil, nil, nil).Register(mux)
	return mux
}

func consentDo(mux *http.ServeMux, method, target string, form url.Values) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req = req.WithContext(withSession(req.Context(), deviceTestSession()))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func mustContain(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// Through a connect link the page only asks whether to allow the agent.
func TestOAuthConsent_LinkIsApproveOnly(t *testing.T) {
	t.Parallel()
	f := &consentAPI{canConsent: true, linkAgent: "support"}
	mux := consentMux(t, f)
	body := consentDo(mux, "GET", "/oauth/consent?request=kr_1", nil).Body.String()
	mustContain(t, body, "Claude Code wants to act as", "support", "can only ask", "the app on this computer", ">Allow<", ">Decline<")
	if strings.Contains(body, `name="choice"`) || strings.Contains(body, "consent-agents") {
		t.Error("an agent choice is shown for a link")
	}
	rec := consentDo(mux, "POST", "/oauth/consent", url.Values{"request": {"kr_1"}, "action": {"approve"}})
	if rec.Code != http.StatusSeeOther || f.sent["agent_id"] != "" || f.sent["new_agent"] != "" {
		t.Fatalf("allow: %d sent=%v", rec.Code, f.sent)
	}
}

func TestOAuthConsent_LinkOfAnotherAccount(t *testing.T) {
	t.Parallel()
	body := consentDo(consentMux(t, &consentAPI{wrongAccount: true}), "GET", "/oauth/consent?request=kr_1", nil).Body.String()
	mustContain(t, body, "This link belongs to another account", ">Decline<")
	if strings.Contains(body, ">Allow<") || strings.Contains(body, ">Connect<") {
		t.Error("another account's link offered to connect")
	}
}

// On the generic URL the default is a new agent for this app; each existing
// agent is a choice of its own, preselected and listed first when the app
// connected as it before.
func TestOAuthConsent_GenericDefaultsToANewAgent(t *testing.T) {
	t.Parallel()
	f := &consentAPI{canConsent: true}
	mux := consentMux(t, f)
	body := consentDo(mux, "GET", "/oauth/consent?request=kr_1", nil).Body.String()
	mustContain(t, body, "Claude Code wants to connect to KIFF", `value="new" checked`, `name="new_name" value="Claude Code"`,
		`name="choice" value="agent:support"`, `name="choice" value="agent:billing"`, "Or one of your agents", "connect link")
	if strings.Contains(body, `value="agent:support" checked`) || strings.Contains(body, "consent-agents") {
		t.Error("an existing agent is preselected with no previous connection, or the old search box is back")
	}
	rec := consentDo(mux, "POST", "/oauth/consent", url.Values{"request": {"kr_1"}, "action": {"approve"}, "choice": {"new"}, "new_name": {"Claude desk"}})
	if rec.Code != http.StatusSeeOther || f.sent["new_agent"] != "Claude desk" || f.sent["agent_id"] != "" {
		t.Fatalf("new agent: %d sent=%v", rec.Code, f.sent)
	}
	rec = consentDo(mux, "POST", "/oauth/consent", url.Values{"request": {"kr_1"}, "action": {"approve"}, "choice": {"agent:billing"}})
	if rec.Code != http.StatusSeeOther || f.sent["agent_id"] != "billing" || f.sent["new_agent"] != "" {
		t.Fatalf("existing agent: %d sent=%v", rec.Code, f.sent)
	}
	// A page rendered before agents became choices still posts "existing".
	rec = consentDo(mux, "POST", "/oauth/consent", url.Values{"request": {"kr_1"}, "action": {"approve"}, "choice": {"existing"}, "existing": {"support"}})
	if rec.Code != http.StatusSeeOther || f.sent["agent_id"] != "support" {
		t.Fatalf("legacy existing: %d sent=%v", rec.Code, f.sent)
	}
	if rec := consentDo(mux, "POST", "/oauth/consent", url.Values{"request": {"kr_1"}, "action": {"approve"}, "choice": {"agent:"}}); !strings.Contains(rec.Body.String(), "Pick the agent to connect.") {
		t.Fatalf("empty agent choice: %d %s", rec.Code, rec.Body.String())
	}

	f2 := &consentAPI{canConsent: true, previous: "billing"}
	body = consentDo(consentMux(t, f2), "GET", "/oauth/consent?request=kr_1", nil).Body.String()
	mustContain(t, body, `value="agent:billing" checked`, "connected before")
	if strings.Contains(body, `value="new" checked`) {
		t.Error("a new agent is preselected although the app connected as billing before")
	}
	if i, j := strings.Index(body, `value="agent:billing"`), strings.Index(body, `value="agent:support"`); i < 0 || j < i {
		t.Error("the previous agent is not listed first")
	}
	// A previous agent that is no longer listed (archived) leaves "a new
	// agent" preselected, so the form always has a choice.
	body = consentDo(consentMux(t, &consentAPI{canConsent: true, previous: "gone"}), "GET", "/oauth/consent?request=kr_1", nil).Body.String()
	mustContain(t, body, `value="new" checked`)
}

func TestOAuthConsent_NewAgentNameInUse(t *testing.T) {
	t.Parallel()
	mux := consentMux(t, &consentAPI{canConsent: true, conflict: true})
	rec := consentDo(mux, "POST", "/oauth/consent", url.Values{"request": {"kr_1"}, "action": {"approve"}, "choice": {"new"}, "new_name": {"support"}})
	if rec.Code == http.StatusSeeOther || !strings.Contains(rec.Body.String(), "already has an agent named support") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestOAuthConsent_NonEditorCanOnlyDecline(t *testing.T) {
	t.Parallel()
	f := &consentAPI{}
	mux := consentMux(t, f)
	body := consentDo(mux, "GET", "/oauth/consent?request=kr_1", nil).Body.String()
	if !strings.Contains(body, "Only an admin or editor") || strings.Contains(body, `value="approve"`) {
		t.Fatal("a non-editor was offered to connect")
	}
	rec := consentDo(mux, "POST", "/oauth/consent", url.Values{"request": {"kr_1"}, "action": {"deny"}})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "error=access_denied") {
		t.Fatalf("deny: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestOAuthConsent_RedirectsToSignIn(t *testing.T) {
	t.Parallel()
	mux := consentMux(t, &consentAPI{})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/oauth/consent?request=kr_1", nil))
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusFound || !strings.HasPrefix(loc, "/sign-in?next=") || !strings.Contains(loc, "kr_1") {
		t.Fatalf("%d %q", rec.Code, loc)
	}
	next, _ := url.Parse(loc)
	if !isSafeNext(next.Query().Get("next")) {
		t.Fatalf("next is not accepted by sign-in: %q", next.Query().Get("next"))
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{"127.0.0.1:53682": true, "localhost:9": true, "[::1]:8080": true,
		"claude.ai": false, "evil.127.0.0.1.example:443": false} {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v", host, got)
		}
	}
}

// The Card that would apply is shown before the person allows: for a link's
// agent, and for each agent on the generic URL.
func TestOAuthConsent_ShowsTheCards(t *testing.T) {
	t.Parallel()
	card := []string{`<span class="consent-card-name">support-card</span>`,
		`<span class="consent-chip">get_order</span>`, `<span class="consent-chip">draft_support_reply</span>`,
		"Over the Card: held for an approver"}
	body := consentDo(consentMux(t, &consentAPI{canConsent: true, linkAgent: "support"}), "GET", "/oauth/consent?request=kr_1", nil).Body.String()
	mustContain(t, body, append([]string{"support's Card", "will act as this agent, and only within this Card."}, card...)...)
	for _, hidden := range []string{"old-card", "archived-card", "support-guard-card"} {
		if strings.Contains(body, hidden) {
			t.Errorf("%s is shown: revoked, archived or for another domain", hidden)
		}
	}
	// billing's only Card is for another domain, so it never applies to a
	// call through the gateway.
	body = consentDo(consentMux(t, &consentAPI{canConsent: true, linkAgent: "billing"}), "GET", "/oauth/consent?request=kr_1", nil).Body.String()
	mustContain(t, body, "billing holds no Card for tool calls: its calls are refused until one is issued.")
	if strings.Contains(body, "billing-guard-card") {
		t.Error("a Card for another domain is shown")
	}

	body = consentDo(consentMux(t, &consentAPI{canConsent: true, previous: "support"}), "GET", "/oauth/consent?request=kr_1", nil).Body.String()
	mustContain(t, body, append([]string{"Or one of your agents", `value="agent:support" checked`, `<span class="consent-agent-label">billing</span>`,
		"No Card for tool calls: its calls are refused until one is issued.", `<span class="consent-badge">connected before</span>`}, card...)...)
}

// When the Cards cannot be read, the page says so rather than claiming an
// agent holds none.
func TestOAuthConsent_CardsUnknown(t *testing.T) {
	t.Parallel()
	body := consentDo(consentMux(t, &consentAPI{canConsent: true, linkAgent: "support", cardsFail: true}), "GET", "/oauth/consent?request=kr_1", nil).Body.String()
	mustContain(t, body, "could not load the Cards", ">Allow<")
	if strings.Contains(body, "holds no Card") {
		t.Error("claimed no Card when the Cards could not be read")
	}
}

// A Card scoped to an environment says so, a Card with no grants says it
// allows no actions, and a long agent list keeps the first agents visible
// with the rest behind a labelled "Show N more agents".
func TestOAuthConsent_CardDetailsAndLongLists(t *testing.T) {
	t.Parallel()
	body := consentDo(consentMux(t, &consentAPI{canConsent: true, linkAgent: "ops"}), "GET", "/oauth/consent?request=kr_1", nil).Body.String()
	mustContain(t, body, `<span class="consent-card-env">only in production</span>`)
	body = consentDo(consentMux(t, &consentAPI{canConsent: true, linkAgent: "idle"}), "GET", "/oauth/consent?request=kr_1", nil).Body.String()
	mustContain(t, body, `<span class="consent-chip is-empty">no actions</span>`)

	var ids []string
	for i := 1; i <= 14; i++ {
		ids = append(ids, fmt.Sprintf(`{"id":"agent-%02d"}`, i))
	}
	ids = append(ids, `{"id":"refunds-eu"}`)
	f := &consentAPI{canConsent: true, previous: "agent-12", agents: `{"agents":[` + strings.Join(ids, ",") + `]}`}
	mux := consentMux(t, f)
	label := func(a string) string { return `<span class="consent-agent-label">` + a + `</span>` }

	// Page 1: six agents, the previous one first and preselected, search
	// and page links shown because there are more than six.
	body = consentDo(mux, "GET", "/oauth/consent?request=kr_1", nil).Body.String()
	mustContain(t, body, `value="agent:agent-12" checked`, label("agent-05"), `name="q"`, `form="consent-agent-search"`,
		`id="consent-agent-search"`, `aria-current="page">1<`, "page=3")
	if strings.Contains(body, label("agent-06")) || strings.Contains(body, "<details") {
		t.Error("page 1 shows more than six agents, or hides agents behind a disclosure")
	}
	if i, j := strings.Index(body, label("agent-12")), strings.Index(body, label("agent-01")); i < 0 || j < i {
		t.Error("the previous agent is not first")
	}

	// Page 3 has the last three; the previous agent is not there, so "a new
	// agent" is preselected.
	body = consentDo(mux, "GET", "/oauth/consent?request=kr_1&page=3", nil).Body.String()
	mustContain(t, body, label("agent-14"), label("refunds-eu"), `value="new" checked`, `aria-current="page">3<`)
	// A page past the end shows the last page.
	body = consentDo(mux, "GET", "/oauth/consent?request=kr_1&page=99", nil).Body.String()
	mustContain(t, body, `aria-current="page">3<`)

	// Search narrows by name, case-insensitively, and keeps the request.
	body = consentDo(mux, "GET", "/oauth/consent?request=kr_1&q=REFUND", nil).Body.String()
	mustContain(t, body, label("refunds-eu"), `value="REFUND"`, "Clear")
	if strings.Contains(body, label("agent-01")) || strings.Contains(body, "consent-pager") {
		t.Error("search did not narrow the agents")
	}
	body = consentDo(mux, "GET", "/oauth/consent?request=kr_1&q=nobody", nil).Body.String()
	mustContain(t, body, "No agent matches")

	// Six agents or fewer: no search, no pages.
	body = consentDo(consentMux(t, &consentAPI{canConsent: true}), "GET", "/oauth/consent?request=kr_1", nil).Body.String()
	if strings.Contains(body, `name="q"`) || strings.Contains(body, "consent-pager") {
		t.Error("search or pages shown for a short list")
	}
}

// The page wears KIFF Cloud's own chrome: the dashboard's theme tokens and
// fonts, the logo, and section separators.
func TestOAuthConsent_KIFFChrome(t *testing.T) {
	t.Parallel()
	body := consentDo(consentMux(t, &consentAPI{canConsent: true}), "GET", "/oauth/consent?request=kr_1", nil).Body.String()
	mustContain(t, body, `class="consent-shell"`, "kiff-logo-transparent-512.png", "css/app-shell.css", "css/fonts.css",
		`class="consent-sep"`, "Or one of your agents", "is-wide")
}

// a review: a post of the generic form with no choice is refused before
// the API, and a long search is cut on a character boundary.
func TestOAuthConsent_NoChoiceAndLongSearch(t *testing.T) {
	t.Parallel()
	f := &consentAPI{canConsent: true}
	mux := consentMux(t, f)
	rec := consentDo(mux, "POST", "/oauth/consent", url.Values{"request": {"kr_1"}, "action": {"approve"}, "choosing": {"1"}})
	if !strings.Contains(rec.Body.String(), "Choose who to connect as") || f.sent != nil {
		t.Fatalf("no choice: %d sent=%v", rec.Code, f.sent)
	}
	// Through a connect link there is no choice to make.
	f2 := &consentAPI{canConsent: true, linkAgent: "support"}
	if rec := consentDo(consentMux(t, f2), "POST", "/oauth/consent", url.Values{"request": {"kr_1"}, "action": {"approve"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("link allow: %d", rec.Code)
	}
	body := consentDo(mux, "GET", "/oauth/consent?request=kr_1", nil).Body.String()
	mustContain(t, body, `name="choosing" value="1"`)

	long := strings.Repeat("é", 120) // 2 bytes each
	var ids []string
	for i := 1; i <= 8; i++ {
		ids = append(ids, fmt.Sprintf(`{"id":"agent-%d"}`, i))
	}
	f3 := &consentAPI{canConsent: true, agents: `{"agents":[` + strings.Join(ids, ",") + `]}`}
	body = consentDo(consentMux(t, f3), "GET", "/oauth/consent?request=kr_1&q="+url.QueryEscape(long), nil).Body.String()
	mustContain(t, body, `value="`+strings.Repeat("é", 100)+`"`)
	if strings.Contains(body, strings.Repeat("é", 101)) || strings.Contains(body, "\ufffd") || strings.Contains(body, "\xc3\"") {
		t.Error("the search was not cut at 100 characters")
	}
}
