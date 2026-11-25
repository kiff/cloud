package handlers_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

type trialAPI struct {
	mu       sync.Mutex
	revoked  bool
	outcome  string
	events   []string
	proposal map[string]any
	keyReq   map[string]any
	// environment scopes the served card to one environment.
	environment string
	// holderKeyRoles are the roles of the key bound to the card's holder;
	// noHolderKey serves no such key. patchKey/patchReq record a grant.
	holderKeyRoles []string
	noHolderKey    bool
	// holderKeyEnv, when set, overrides the holder key's environment
	// (which otherwise matches the card's).
	holderKeyEnv *string
	patchKey     string
	patchReq     map[string]any
	// evidenceActor is the actor the card page asked the domain's
	// evidence for; evidenceFails makes that read fail.
	evidenceActor string
	evidenceFails bool
	// saturated serves a full page (50 rows) of the holder's decisions,
	// all but one on an action the card does not cover.
	saturated bool
	// at is when the served decisions were made, and the page's clock
	// (trialRig pins both). Read from the wall clock, the rows a minute
	// or two old fell on yesterday in the first minutes after UTC
	// midnight, and "2 decisions today" read "1 decision today".
	at time.Time
}

// noonYesterday is a fixed instant well inside one UTC day.
func noonYesterday() time.Time {
	return time.Now().UTC().Truncate(24 * time.Hour).Add(-12 * time.Hour)
}

func (a *trialAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case r.URL.Path == "/v1/me":
		writeJSONForTest(w, http.StatusOK, map[string]any{"subject": "alice", "tenant_id": "t1"})
	case r.URL.Path == "/v1/me/mandates":
		m := map[string]any{"id": "refund-agent-refunds", "subject": "refund-agent", "domain": "card-refund",
			"grants": []any{map[string]any{"action": "REFUND_ORDER"}}}
		if a.revoked {
			m["revoked_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
		}
		if a.environment != "" {
			m["environment"] = a.environment
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"mandates": []any{m}})
	case r.URL.Path == "/v1/me/domains/card-refund/evidence":
		a.evidenceActor = r.URL.Query().Get("actor")
		if a.evidenceFails {
			writeJSONForTest(w, http.StatusInternalServerError, map[string]any{"error": "boom"})
			return
		}
		now := a.at
		if now.IsZero() {
			now = time.Now().UTC()
		}
		if a.saturated {
			rows := []any{map[string]any{"proposal_id": "card-row", "entity_id": "kiff-test-9", "action_name": "REFUND_ORDER",
				"actor_id": "refund-agent", "outcome": "allowed", "created_at": now.Format(time.RFC3339), "receipt_status": "none"}}
			for i := 0; i < 49; i++ {
				rows = append(rows, map[string]any{"proposal_id": "other-" + strconv.Itoa(i), "entity_id": "o", "action_name": "ISSUE_CREDIT",
					"actor_id": "refund-agent", "outcome": "allowed", "principal_api_key_id": "key-bound", "actor_verified": true,
					"created_at": now.Add(-time.Duration(i) * time.Second).Format(time.RFC3339), "receipt_status": "none"})
			}
			writeJSONForTest(w, http.StatusOK, map[string]any{"decisions": map[string]any{"rows": rows}})
			return
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"decisions": map[string]any{"rows": []any{
			// A refusal that drew nothing, its receipt already signed.
			map[string]any{"proposal_id": "card-test-refused", "entity_id": "kiff-test-2", "action_name": "REFUND_ORDER",
				"actor_id": "refund-agent", "outcome": "blocked", "reasons": []string{"mandate_limit_reached"},
				"created_at": now.Format(time.RFC3339), "receipt_trace_id": "decision-card-test-refused", "receipt_status": "pending",
				"principal_api_key_id": "key-bound", "actor_verified": true},
			// An allowed one whose receipt the signing loop has not reached.
			map[string]any{"proposal_id": "card-test-allowed", "entity_id": "kiff-test-1", "action_name": "REFUND_ORDER",
				"actor_id": "refund-agent", "outcome": "allowed", "created_at": now.Add(-time.Minute).Format(time.RFC3339), "receipt_status": "none"},
			// The same agent on an action this card does not cover.
			map[string]any{"proposal_id": "other-action", "entity_id": "o-9", "action_name": "ISSUE_CREDIT",
				"actor_id": "refund-agent", "outcome": "allowed", "created_at": now.Add(-2 * time.Minute).Format(time.RFC3339), "receipt_status": "none"},
		}}})
	case r.URL.Path == "/v1/events/raw":
		b, _ := io.ReadAll(r.Body)
		a.events = append(a.events, r.URL.Query().Get("domain")+"|"+string(b))
		writeJSONForTest(w, http.StatusAccepted, map[string]any{"ok": true})
	case r.URL.Path == "/v1/keys" && r.Method == http.MethodGet:
		keys := []any{
			map[string]any{"id": "key-other", "label": "someone else", "agent_id": "other-agent", "roles": []string{"support"},
				"created_at": time.Now().Add(-48 * time.Hour).Format(time.RFC3339)},
		}
		if !a.noHolderKey {
			keys = append([]any{map[string]any{"id": "key-bound", "label": "Agent key for card refund-agent-refunds", "agent_id": "refund-agent",
				"roles": a.holderKeyRoles, "environment": a.holderKeyEnvironment(),
				"created_at": time.Now().Add(-48 * time.Hour).Format(time.RFC3339)}}, keys...)
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"keys": keys})
	case strings.HasPrefix(r.URL.Path, "/v1/keys/") && r.Method == http.MethodPatch:
		a.patchKey = strings.TrimPrefix(r.URL.Path, "/v1/keys/")
		_ = json.NewDecoder(r.Body).Decode(&a.patchReq)
		writeJSONForTest(w, http.StatusOK, map[string]any{"id": a.patchKey, "agent_id": "refund-agent", "roles": []string{"support"}})
	case r.URL.Path == "/v1/keys" && r.Method == http.MethodPost:
		_ = json.NewDecoder(r.Body).Decode(&a.keyReq)
		writeJSONForTest(w, http.StatusCreated, map[string]any{"id": "key-1", "label": a.keyReq["label"], "plaintext": "kiff_live_test_plaintext"})
	case r.URL.Path == "/v1/proposals/decide":
		_ = json.NewDecoder(r.Body).Decode(&a.proposal)
		out := map[string]any{"outcome": a.outcome}
		if a.outcome != "allowed" {
			out["reasons"] = []string{"mandate_limit_reached"}
		}
		if a.outcome == "owner_required" {
			out["reasons"] = []string{"mandate_owner_required"}
			out["exception"] = map[string]any{"id": "exc-7", "status": "held", "review_url": "/needs-you/exc-7"}
		}
		writeJSONForTest(w, http.StatusOK, out)
	default:
		writeJSONForTest(w, http.StatusNotFound, map[string]any{"error": "not found"})
	}
}

func trialRig(t *testing.T, api *trialAPI) (http.Handler, *http.Cookie) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(srv.Close)
	client, err := apiclient.NewSessionAware(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestSessionManager(t)
	dash := handlers.NewDashboardHandler(client, mgr, handlers.StaticBearerSource(), nil)
	// The fixture's decisions and the page read one fixed day.
	api.at = noonYesterday()
	dash.SetNow(func() time.Time { return api.at })
	mux := http.NewServeMux()
	dash.Register(mux)
	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{Subject: "alice", Bearer: "b", Email: "a@example.com", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return handlers.RequireSession(mgr)(mux), issue.Result().Cookies()[0]
}

func send(h http.Handler, c *http.Cookie, method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCardTest_MakesOneRealDecisionAsTheCardsAgent(t *testing.T) {
	t.Parallel()
	api := &trialAPI{outcome: "allowed"}
	h, c := trialRig(t, api)
	rec := send(h, c, http.MethodPost, "/cards/refund-agent-refunds/test")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/cards/refund-agent-refunds/decisions" {
		t.Fatalf("got %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(api.events) != 1 || !strings.HasPrefix(api.events[0], "card-refund|") || !strings.Contains(api.events[0], `"type":"ORDER_PAID"`) {
		t.Fatalf("setup event = %v, want one ORDER_PAID on card-refund", api.events)
	}
	p := api.proposal
	if p["domain"] != "card-refund" || p["actor_id"] != "refund-agent" || p["action_name"] != "REFUND_ORDER" || p["entity_type"] != "Order" {
		t.Errorf("proposal = %v", p)
	}
	if params, _ := p["parameters"].(map[string]any); params["amount_cents"] != float64(100) {
		t.Errorf("test draw should be €1, got %v", p["parameters"])
	}
	if !strings.Contains(api.events[0], `"entity_id":"`+p["entity_id"].(string)+`"`) {
		t.Error("the decision must be about the entity the setup event created")
	}
}

// A test request outside the card comes home: it lands on the held
// action, where the owner answers it, not on a refusal.
func TestCardTest_OutsideTheCardGoesToNeedsYou(t *testing.T) {
	t.Parallel()
	h, c := trialRig(t, &trialAPI{outcome: "owner_required"})
	rec := send(h, c, http.MethodPost, "/cards/refund-agent-refunds/test")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/needs-you/exc-7" {
		t.Fatalf("status %d location %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestCardTest_RefusedAndRevoked(t *testing.T) {
	t.Parallel()
	refused := &trialAPI{outcome: "blocked"}
	h, c := trialRig(t, refused)
	if rec := send(h, c, http.MethodPost, "/cards/refund-agent-refunds/test"); rec.Code != http.StatusSeeOther {
		t.Fatalf("refused test status %d", rec.Code)
	}
	if refused.proposal == nil {
		t.Error("a refused test still asks KIFF")
	}

	revoked := &trialAPI{revoked: true, outcome: "allowed"}
	h, c = trialRig(t, revoked)
	send(h, c, http.MethodPost, "/cards/refund-agent-refunds/test")
	if len(revoked.events) != 0 || revoked.proposal != nil {
		t.Error("a revoked card must not be tested")
	}
	if rec := send(h, c, http.MethodPost, "/cards/no-such-card/test"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown card = %d, want 404", rec.Code)
	}
}

func TestSupportFormPrefill(t *testing.T) {
	t.Parallel()
	h, c := trialRig(t, &trialAPI{})
	rec := send(h, c, http.MethodGet, "/dashboard/support/new?category=question&title=Guided+setup+for+card+x&body=Please+help")
	body := rec.Body.String()
	if !strings.Contains(body, `value="Guided setup for card x"`) || !strings.Contains(body, "Please help</textarea>") {
		t.Errorf("support form should open prefilled; status %d", rec.Code)
	}
}

func TestCardAgentKey_CarriesTheJobsRole(t *testing.T) {
	t.Parallel()
	api := &trialAPI{}
	h, c := trialRig(t, api)
	rec := send(h, c, http.MethodPost, "/cards/refund-agent-refunds/key")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/cards/refund-agent-refunds" {
		t.Fatalf("got %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	roles, _ := api.keyReq["roles"].([]any)
	if len(roles) != 1 || roles[0] != "support" {
		t.Errorf("minted key roles = %v, want [support]", api.keyReq["roles"])
	}
	// Bound to the card's holder, or the decide path refuses the key on
	// an action that requires a card.
	if agent, _ := api.keyReq["agent_id"].(string); agent != "refund-agent" {
		t.Errorf("minted key agent_id = %v, want the card holder refund-agent", api.keyReq["agent_id"])
	}
	if label, _ := api.keyReq["label"].(string); !strings.Contains(label, "refund-agent-refunds") {
		t.Errorf("key label %q should name the card", label)
	}

	// A card with no environment mints a key free to act in any.
	if env, ok := api.keyReq["environment"]; ok {
		t.Errorf("an unscoped card's key must not be bound to an environment, got %v", env)
	}

	revoked := &trialAPI{revoked: true}
	h, c = trialRig(t, revoked)
	send(h, c, http.MethodPost, "/cards/refund-agent-refunds/key")
	if revoked.keyReq != nil {
		t.Error("no key may be minted for a revoked card")
	}
}

// A key created from an environment-scoped card is bound to that
// environment, so it cannot draw on another environment's card by
// naming that environment.
func TestCardAgentKey_BoundToTheCardsEnvironment(t *testing.T) {
	t.Parallel()
	api := &trialAPI{environment: "production"}
	h, c := trialRig(t, api)
	if rec := send(h, c, http.MethodPost, "/cards/refund-agent-refunds/key"); rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d", rec.Code)
	}
	if env, _ := api.keyReq["environment"].(string); env != "production" {
		t.Errorf("minted key environment = %v, want the card's production", api.keyReq["environment"])
	}
}

// The card lists what KIFF decided for its holder, refusals included,
// each with its receipt: the statement carries only draws, so a refused
// test request would otherwise appear nowhere on the card.
func TestCardDetail_ListsTheHoldersDecisionsWithReceipts(t *testing.T) {
	t.Parallel()
	api := &trialAPI{}
	h, c := trialRig(t, api)
	rec := send(h, c, http.MethodGet, "/cards/refund-agent-refunds/decisions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if api.evidenceActor != "refund-agent" {
		t.Errorf("evidence read for actor %q, want the card holder refund-agent", api.evidenceActor)
	}
	for _, want := range []string{
		"Recent decisions",
		"Refused", "mandate_limit_reached",
		`href="/dashboard/receipts/decision-card-test-refused"`,
		"Allowed", "kiff-test-1", "Receipt being signed",
		// Selected by holder and action, not card: the copy must not
		// claim every row fell under this card.
		"may predate this Card or fall under another one",
		// Connection status: the latest decision came through the key
		// bound to the holder, and only that key is listed as the holder's.
		"Connected.", "via key <code>Agent key for card refund-agent-refunds</code>", "(refused)", "decisions today",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("card page missing %q", want)
		}
	}
	if strings.Contains(body, "act under this card") {
		t.Error("the decisions list claims its rows fell under this card")
	}
	if strings.Contains(body, "other-action") {
		t.Error("a decision on an action the card does not cover is listed on the card")
	}

	if strings.Contains(body, "someone else") {
		t.Error("a key bound to another agent is listed as this holder's")
	}

	// The other tabs do not ask for decisions they will not show, and
	// the Overview no longer carries them.
	for _, tab := range []string{"", "/statement", "/changes"} {
		api.evidenceActor = ""
		page := send(h, c, http.MethodGet, "/cards/refund-agent-refunds"+tab).Body.String()
		if api.evidenceActor != "" {
			t.Errorf("tab %q read the holder's decisions", tab)
		}
		if strings.Contains(page, "Recent decisions") {
			t.Errorf("tab %q still lists recent decisions", tab)
		}
		if !strings.Contains(page, `href="/cards/refund-agent-refunds/decisions"`) {
			t.Errorf("tab %q has no link to the Decisions tab", tab)
		}
	}

	failing := &trialAPI{evidenceFails: true}
	h, c = trialRig(t, failing)
	body = send(h, c, http.MethodGet, "/cards/refund-agent-refunds/decisions").Body.String()
	if !strings.Contains(body, "recent decisions could not be read") || !strings.Contains(body, "Connection unknown") {
		t.Error("a failed read must say so rather than show an empty list or a false status")
	}
}

// A refused test request says it drew nothing and where its signed
// receipt is.
func TestCardTest_RefusalPointsToItsReceipt(t *testing.T) {
	t.Parallel()
	api := &trialAPI{outcome: "blocked"}
	h, c := trialRig(t, api)
	rec := send(h, c, http.MethodPost, "/cards/refund-agent-refunds/test")
	if loc := rec.Header().Get("Location"); loc != "/cards/refund-agent-refunds/decisions" {
		t.Fatalf("a decided test lands on %q, want the Decisions tab", loc)
	}
	// Follow the redirect carrying the flash cookie the test set.
	req := httptest.NewRequest(http.MethodGet, "/cards/refund-agent-refunds/decisions", nil)
	req.AddCookie(c)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name != c.Name {
			req.AddCookie(ck)
		}
	}
	follow := httptest.NewRecorder()
	h.ServeHTTP(follow, req)
	page := follow.Body.String()
	for _, want := range []string{"Nothing was drawn from the card", "listed below with its signed receipt"} {
		if !strings.Contains(page, want) {
			t.Errorf("refusal flash missing %q", want)
		}
	}
}

// end to end on both ways a key is minted from the dashboard: the
// redirect's cookies never carry the key in readable form, the page it
// lands on reveals it once, and a reload does not reveal it again.
func TestKeyReveal_NeverInACookieAndShownOnce(t *testing.T) {
	t.Parallel()
	const plaintext = "kiff_live_test_plaintext"
	for _, c := range []struct{ name, path, form string }{
		{"dashboard keys page", "/dashboard/keys", "label=agent"},
		{"card page", "/cards/refund-agent-refunds/key", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, sess := trialRig(t, &trialAPI{})
			req := httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(c.form))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(sess)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("mint: status %d", rec.Code)
			}
			var flash *http.Cookie
			for _, ck := range rec.Result().Cookies() {
				payload, _, _ := strings.Cut(ck.Value, ".")
				raw, _ := base64.RawURLEncoding.DecodeString(payload)
				if strings.Contains(ck.Value, plaintext) || strings.Contains(string(raw), plaintext) {
					t.Fatalf("cookie %s carries the key", ck.Name)
				}
				if ck.Name == session.FlashCookieName {
					flash = ck
				}
			}
			if flash == nil {
				t.Fatal("no flash cookie set")
			}

			show := func() string {
				req := httptest.NewRequest(http.MethodGet, rec.Header().Get("Location"), nil)
				req.AddCookie(sess)
				req.AddCookie(flash)
				page := httptest.NewRecorder()
				h.ServeHTTP(page, req)
				return page.Body.String()
			}
			if body := show(); !strings.Contains(body, plaintext) {
				t.Fatal("the landing page did not reveal the key")
			}
			// Replaying the same flash cookie (a reload, or a copy of it)
			// reveals nothing.
			if body := show(); strings.Contains(body, plaintext) {
				t.Fatal("the key was revealed a second time")
			}
		})
	}
}

// A full page of the holder's decisions, dominated by an action the
// card does not cover: the one card-action row read is a floor for
// today, and finding no request from the agent's key on the card is
// "not seen recently", never "not connected".
func TestCardDecisions_SaturatedPageIsNotAFact(t *testing.T) {
	t.Parallel()
	h, c := trialRig(t, &trialAPI{saturated: true})
	body := send(h, c, http.MethodGet, "/cards/refund-agent-refunds/decisions").Body.String()
	for _, want := range []string{"Not seen recently.", "at least 1 decision today"} {
		if !strings.Contains(body, want) {
			t.Errorf("saturated page missing %q", want)
		}
	}
	for _, wrong := range []string{"Not connected yet.", "; 1 decision today"} {
		if strings.Contains(body, wrong) {
			t.Errorf("saturated page states %q as a fact", wrong)
		}
	}
}

// The card page offers the job's role to the key its agent already acts
// with, instead of minting a second key.
func TestCardOverview_OffersTheRoleToTheAgentsKey(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		api     *trialAPI
		want    []string
		mustNot []string
	}{
		{"key without the role", &trialAPI{},
			[]string{`action="/cards/refund-agent-refunds/key/role"`, "Give Agent key for card refund-agent-refunds the support role"},
			[]string{`action="/cards/refund-agent-refunds/key"`}},
		{"key with the role", &trialAPI{holderKeyRoles: []string{"support"}},
			[]string{"already has its key"},
			[]string{`action="/cards/refund-agent-refunds/key"`, `/key/role"`}},
		{"no key yet", &trialAPI{noHolderKey: true},
			[]string{`action="/cards/refund-agent-refunds/key"`, "Create a key for this agent"},
			[]string{`/key/role"`}},
		{"environment-scoped card and key", &trialAPI{environment: "production"},
			[]string{`action="/cards/refund-agent-refunds/key/role"`},
			nil},
		{"key in another environment", &trialAPI{environment: "production", holderKeyEnv: new(string)},
			[]string{`action="/cards/refund-agent-refunds/key"`},
			[]string{`/key/role"`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, cookie := trialRig(t, c.api)
			body := send(h, cookie, http.MethodGet, "/cards/refund-agent-refunds/settings").Body.String()
			for _, w := range c.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %q", w)
				}
			}
			for _, w := range c.mustNot {
				if strings.Contains(body, w) {
					t.Errorf("must not show %q", w)
				}
			}
		})
	}
}

func TestCardKeyRole_GrantsTheJobsRoleToTheExistingKey(t *testing.T) {
	t.Parallel()
	api := &trialAPI{}
	h, c := trialRig(t, api)
	rec := send(h, c, http.MethodPost, "/cards/refund-agent-refunds/key/role")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/cards/refund-agent-refunds" {
		t.Fatalf("got %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	roles, _ := api.patchReq["add_roles"].([]any)
	if api.patchKey != "key-bound" || len(roles) != 1 || roles[0] != "support" {
		t.Errorf("grant = PATCH %q %v, want key-bound add_roles [support]", api.patchKey, api.patchReq)
	}
	if api.keyReq != nil {
		t.Error("granting a role must not mint a key")
	}

	for name, a := range map[string]*trialAPI{
		"already has it": {holderKeyRoles: []string{"support"}},
		"no key":         {noHolderKey: true},
		"revoked card":   {revoked: true},
	} {
		h, c := trialRig(t, a)
		send(h, c, http.MethodPost, "/cards/refund-agent-refunds/key/role")
		if a.patchKey != "" {
			t.Errorf("%s: granted a role to %q", name, a.patchKey)
		}
	}
}

func (a *trialAPI) holderKeyEnvironment() string {
	if a.holderKeyEnv != nil {
		return *a.holderKeyEnv
	}
	return a.environment
}
