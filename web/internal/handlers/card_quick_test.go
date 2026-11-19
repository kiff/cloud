package handlers_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

type quickAPI struct {
	mu         sync.Mutex
	domains    []string
	createCode int
	created    []string
	issued     []map[string]any
}

func (a *quickAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/me/domains":
		out := []map[string]any{}
		for _, d := range a.domains {
			out = append(out, map[string]any{"name": d})
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"domains": out})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/me/domains":
		a.created = append(a.created, r.URL.Query().Get("template"))
		code := a.createCode
		if code == 0 {
			code = http.StatusCreated
		}
		writeJSONForTest(w, code, map[string]any{"domain": r.URL.Query().Get("template")})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/me/mandates":
		writeJSONForTest(w, http.StatusOK, map[string]any{"mandates": []any{}})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/me/mandates":
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		a.issued = append(a.issued, body)
		writeJSONForTest(w, http.StatusOK, map[string]any{"id": body["id"], "subject": body["subject"]})
	default:
		writeJSONForTest(w, http.StatusNotFound, map[string]any{"error": "not found"})
	}
}

func quickRig(t *testing.T, api *quickAPI) (http.Handler, *http.Cookie) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(srv.Close)
	client, err := apiclient.NewSessionAware(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestSessionManager(t)
	dash := handlers.NewDashboardHandler(client, mgr, handlers.StaticBearerSource(), nil)
	mux := http.NewServeMux()
	dash.Register(mux)
	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{Subject: "alice", Bearer: "b", Email: "a@example.com", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return handlers.RequireSession(mgr)(mux), issue.Result().Cookies()[0]
}

func postQuick(h http.Handler, c *http.Cookie, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/cards/quick", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTaskCard_CreatesTheDomainFromItsTemplateAndIssues(t *testing.T) {
	t.Parallel()
	api := &quickAPI{domains: []string{"orders"}}
	h, c := quickRig(t, api)
	rec := postQuick(h, c, url.Values{"task": {"refunds"}, "subject": {"refund-agent"}, "limit": {"1,000"}})

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/cards/refund-agent-refunds" {
		t.Fatalf("got %d → %q, want 303 → /cards/refund-agent-refunds; body %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if len(api.created) != 1 || api.created[0] != "card-refund" {
		t.Errorf("template creates = %v, want [card-refund]", api.created)
	}
	if len(api.issued) != 1 {
		t.Fatalf("issued %d cards, want 1", len(api.issued))
	}
	got, _ := json.Marshal(api.issued[0])
	for _, want := range []string{`"id":"refund-agent-refunds"`, `"subject":"refund-agent"`, `"domain":"card-refund"`, `"action":"REFUND_ORDER"`, `"quantity":"sum(amount_cents)"`, `"limit":100000`, `"window":"calendar_day"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("issued card %s missing %s", got, want)
		}
	}
}

func TestTaskCard_ReusesAnExistingDomain(t *testing.T) {
	t.Parallel()
	api := &quickAPI{domains: []string{"subscription-plan-change"}}
	h, c := quickRig(t, api)
	rec := postQuick(h, c, url.Values{"task": {"plan-changes"}, "subject": {"billing-agent"}, "limit": {"40"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d; body %s", rec.Code, rec.Body.String())
	}
	if len(api.created) != 0 {
		t.Errorf("created %v for a domain that already exists", api.created)
	}
	got, _ := json.Marshal(api.issued)
	if !strings.Contains(string(got), `"quantity":"count"`) || !strings.Contains(string(got), `"limit":40`) {
		t.Errorf("plan changes should be counted: %s", got)
	}
}

func TestTaskCard_RefusalsKeepTheForm(t *testing.T) {
	t.Parallel()
	api := &quickAPI{createCode: http.StatusForbidden}
	h, c := quickRig(t, api)
	rec := postQuick(h, c, url.Values{"task": {"store-credit"}, "subject": {"retention-agent"}, "limit": {"500"}})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "limit on domains is reached") || !strings.Contains(body, `value="retention-agent"`) {
		t.Errorf("domain cap should re-render the form with the agent kept; got %d", rec.Code)
	}
	if len(api.issued) != 0 {
		t.Error("no card may be issued when its rules could not be set up")
	}

	rec = postQuick(h, c, url.Values{"task": {"refunds"}, "subject": {"refund-agent"}, "limit": {"lots"}})
	if !strings.Contains(rec.Body.String(), "amount in euros above zero") {
		t.Error("a bad amount should be refused on the form")
	}
}

func TestCardNew_StartsFromTheJob(t *testing.T) {
	t.Parallel()
	h, c := quickRig(t, &quickAPI{domains: []string{"orders"}})
	get := func(target string) string {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Body.String()
	}
	quick := get("/cards/new?subject=refund-agent")
	for _, want := range []string{`action="/cards/quick"`, "Refund orders", "Issue store credit", "Change subscription plans", `value="refund-agent"`, `href="/cards/new?advanced=1&amp;subject=refund-agent"`} {
		if !strings.Contains(quick, want) {
			t.Errorf("simple form missing %q", want)
		}
	}
	if advanced := get("/cards/new?advanced=1"); strings.Contains(advanced, `action="/cards/quick"`) {
		t.Error("?advanced=1 should open the full form")
	}
}
