package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

// holdersAPI stubs the Card list and the holders write.
type holdersAPI struct {
	mu       sync.Mutex
	holders  []string
	puts     [][]string
	expected [][]string
	reject   string
	// changed makes the holders write answer 412, as the API does when
	// another change landed after the list was read.
	changed bool
}

func (a *holdersAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case r.URL.Path == "/v1/me/mandates" && r.Method == http.MethodGet:
		writeJSONForTest(w, http.StatusOK, map[string]any{"mandates": []any{map[string]any{
			"id": "team", "subject": a.holders[0], "subjects": a.holders, "domain": "gateway", "active": true,
			"grants": []any{map[string]any{"action": "issue_refund"}}}}})
	case r.URL.Path == "/v1/me/mandates/team/holders" && r.Method == http.MethodPut:
		if a.reject != "" {
			writeJSONForTest(w, http.StatusConflict, map[string]any{"error": a.reject})
			return
		}
		var body struct {
			Subjects []string `json:"subjects"`
			Expected []string `json:"expected"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.expected = append(a.expected, body.Expected)
		if a.changed {
			writeJSONForTest(w, http.StatusPreconditionFailed, map[string]any{"error": "the card's holders changed"})
			return
		}
		a.puts = append(a.puts, body.Subjects)
		a.holders = body.Subjects
		writeJSONForTest(w, http.StatusOK, map[string]any{"id": "team", "subject": body.Subjects[0], "subjects": body.Subjects})
	case r.URL.Path == "/v1/me/domains/gateway/evidence":
		// Unfiltered, the domain's newest decisions are all another
		// agent's; filtered by actor, each holder has its own.
		actor := r.URL.Query().Get("actor")
		var rows []any
		switch actor {
		case "":
			for i := 0; i < 60; i++ {
				rows = append(rows, map[string]any{"proposal_id": "busy-" + strconv.Itoa(i), "actor_id": "busy-agent", "action_name": "issue_refund", "outcome": "allowed", "created_at": time.Now()})
			}
		default:
			rows = append(rows, map[string]any{"proposal_id": "p-" + actor, "actor_id": actor, "action_name": "issue_refund", "outcome": "allowed", "created_at": time.Now()})
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"decisions": map[string]any{"rows": rows}})
	default:
		writeJSONForTest(w, http.StatusNotFound, map[string]any{"error": "not found"})
	}
}

func holdersRig(t *testing.T, api *holdersAPI) (http.Handler, *http.Cookie) {
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

// the mandates design amendment: sharing adds the agent to the holders
// and stopping removes only that one; the whole list goes to the API,
// which decides.
func TestShareCard(t *testing.T) {
	t.Parallel()
	api := &holdersAPI{holders: []string{"refund-agent"}}
	h, c := holdersRig(t, api)
	share := func(action, agent string) string {
		return followFlashTo(t, h, c, postForm(h, c, "/cards/team/holders", url.Values{"action": {action}, "agent": {agent}}), "/cards/team")
	}
	if msg := share("add", "billing-agent"); !strings.Contains(msg, "billing-agent now shares this Card") {
		t.Fatalf("share: %s", msg)
	}
	if got := strings.Join(api.puts[0], ","); got != "refund-agent,billing-agent" {
		t.Fatalf("sent %s", got)
	}
	if got := strings.Join(api.expected[0], ","); got != "refund-agent" {
		t.Fatalf("expected sent %q, want the list the change was made from", got)
	}
	if msg := share("add", "billing-agent"); !strings.Contains(msg, "already holds") || len(api.puts) != 1 {
		t.Errorf("adding a holder twice: %s", msg)
	}
	if msg := share("remove", "refund-agent"); !strings.Contains(msg, "refund-agent no longer holds this Card") {
		t.Fatalf("stop sharing: %s", msg)
	}
	if got := strings.Join(api.puts[1], ","); got != "billing-agent" {
		t.Fatalf("sent %s", got)
	}
	if msg := share("remove", "billing-agent"); !strings.Contains(msg, "at least one holder") || len(api.puts) != 2 {
		t.Errorf("removing the last holder: %s", msg)
	}
	api.reject = "this card is revoked or outside its validity window; issue a new card instead"
	if msg := share("add", "ops-agent"); !strings.Contains(msg, "The holders were not changed: this card is revoked") {
		t.Errorf("API refusal: %s", msg)
	}
	if rec := postForm(h, c, "/cards/nope/holders", url.Values{"action": {"add"}, "agent": {"x"}}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown card: %d", rec.Code)
	}
}

// a review: a change built from a list another change has since
// replaced is refused by the API (412), and the owner is told, rather
// than one change silently undoing the other.
func TestShareCard_StaleListIsRefused(t *testing.T) {
	t.Parallel()
	api := &holdersAPI{holders: []string{"refund-agent", "billing-agent"}, changed: true}
	h, c := holdersRig(t, api)
	msg := followFlashTo(t, h, c, postForm(h, c, "/cards/team/holders", url.Values{"action": {"remove"}, "agent": {"billing-agent"}}), "/cards/team")
	if !strings.Contains(msg, "The holders changed since you opened this Card") || len(api.puts) != 0 {
		t.Fatalf("stale change: %s, writes %v", msg, api.puts)
	}
	if got := strings.Join(api.expected[0], ","); got != "refund-agent,billing-agent" {
		t.Fatalf("expected sent %q", got)
	}
}

// a review: a shared Card's Decisions tab reads each holder's decisions,
// so other agents' traffic in the domain cannot crowd them out.
func TestSharedCardDecisionsPerHolder(t *testing.T) {
	t.Parallel()
	api := &holdersAPI{holders: []string{"refund-agent", "billing-agent"}}
	h, c := holdersRig(t, api)
	body := send(h, c, http.MethodGet, "/cards/team/decisions").Body.String()
	for _, want := range []string{"p-refund-agent", "p-billing-agent"} {
		if !strings.Contains(body, want) {
			t.Errorf("decisions tab missing %s", want)
		}
	}
	if strings.Contains(body, "busy-") {
		t.Error("another agent's decision is on the Card")
	}
}
