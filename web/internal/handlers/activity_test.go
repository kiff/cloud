package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// Activity lists every call with what KIFF decided, the owner's
// answer, the tool and the receipt; a held call opens its story on
// Needs you, any other its own page; filters and pages are links.
func TestActivityPage(t *testing.T) {
	t.Parallel()
	at := time.Now().UTC().Add(-time.Hour)
	call := func(pid, actor, action, outcome string, held bool) apiclient.ActivityCall {
		return apiclient.ActivityCall{ProposalDecision: apiclient.ProposalDecision{ProposalID: pid, ActorID: actor, ActionName: action, Outcome: outcome, CreatedAt: at}, Held: held}
	}
	ok := call("p-ok", "luna-desk", "shop_get_order", "allowed", false)
	ok.ReceiptTraceID = "decision-p-ok"
	ok.ToolOutcome = &apiclient.ToolOutcome{Status: "done", At: at}
	rejected := call("p-rej", "luna-desk", "shop_refund_order", "owner_required", true)
	var mu sync.Mutex
	var asked []string
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /v1/me/activity", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.RawQuery)
		mu.Unlock()
		page := apiclient.ActivityPage{Counts: apiclient.ActivityCounts{All: 120, Allowed: 60, Refused: 40, Held: 20}, Shown: 120}
		switch r.URL.Query().Get("proposal") {
		case "p-ok":
			page.Calls = []apiclient.ActivityCall{ok}
		case "p-rej":
			page.Calls = []apiclient.ActivityCall{rejected}
		case "":
			page.Calls = []apiclient.ActivityCall{ok, rejected}
		}
		writeJSONForTest(w, 200, page)
	})
	apiMux.HandleFunc("GET /v1/me/card-exceptions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, 200, map[string]any{"exceptions": []apiclient.CardException{
			{ID: "exc-1", ProposalID: "p-rej", Subject: "luna-desk", Action: "shop_refund_order", Status: "rejected", CreatedAt: at, Refusal: &apiclient.Refusal{NextStep: "return_first"}},
		}})
	})
	apiMux.HandleFunc("GET /v1/me/mandates", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, 200, map[string]any{"mandates": []apiclient.Mandate{{ID: "c1", Subject: "luna-desk", Domain: "gateway"}}})
	})
	apiMux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, 200, map[string]any{"subject": "alice", "tenant_id": "t1", "account_kind": "tools"})
	})
	h, cookie := attentionRig(t, apiMux)
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	body := get("/activity?show=held&agent=luna-desk&page=1").Body.String()
	for _, want := range []string{
		`href="/activity/p-ok"`, `href="/needs-you/exc-1"`, "Rejected: Return first", "The tool reported it done", ">Signed<", "Being signed",
		`<span class="tl-chip-count">120</span>`, `<span class="tl-chip-count">20</span>`, `<option value="luna-desk" selected>`,
		`href="/activity?agent=luna-desk&amp;show=held"`,            // Newer, page 0
		`href="/activity?agent=luna-desk&amp;page=2&amp;show=held"`, // Older
		`class="dash-nav-item dash-nav-active" href="/activity"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("activity lacks %q", want)
		}
	}
	mu.Lock()
	first := asked[0]
	mu.Unlock()
	if first != "actor=luna-desk&limit=50&offset=50&show=held" {
		t.Errorf("asked the API %q", first)
	}

	call1 := get("/activity/p-ok").Body.String()
	for _, want := range []string{"luna-desk asked to run shop_get_order", "It fit the Cards that cover it, so KIFF allowed it.", "Receipt signed", `href="/dashboard/receipts/decision-p-ok"`} {
		if !strings.Contains(call1, want) {
			t.Errorf("call page lacks %q", want)
		}
	}
	if rec := get("/activity/p-rej"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/needs-you/exc-1" {
		t.Errorf("a held call = %d %q, want its story on Needs you", rec.Code, rec.Header().Get("Location"))
	}
	if rec := get("/activity/nope"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "No call with that id") {
		t.Errorf("unknown call = %d", rec.Code)
	}
}

// a review: an account with calls and no Cards still picks its
// agents; Older goes on past page 200; a call waiting for a domain
// approval, or for an owner whose answers can't be read, reads as
// waiting with where to answer, never as refused.
func TestActivityPendingAgentsAndDeepPages(t *testing.T) {
	t.Parallel()
	at := time.Now().UTC().Add(-time.Hour)
	call := func(pid, outcome string) apiclient.ActivityCall {
		return apiclient.ActivityCall{ProposalDecision: apiclient.ProposalDecision{ProposalID: pid, ActorID: "guard-bot", ActionName: "REFUND_ORDER", Outcome: outcome, CreatedAt: at}, Held: true}
	}
	var mu sync.Mutex
	var asked []string
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /v1/me/activity", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.RawQuery)
		mu.Unlock()
		page := apiclient.ActivityPage{Counts: apiclient.ActivityCounts{All: 20000, Held: 20000}, Shown: 20000, Agents: []string{"guard-bot"}}
		switch r.URL.Query().Get("proposal") {
		case "p-appr":
			page.Calls = []apiclient.ActivityCall{call("p-appr", "approval_required")}
		case "p-own":
			page.Calls = []apiclient.ActivityCall{call("p-own", "owner_required")}
		case "p-room":
			page.Calls = []apiclient.ActivityCall{call("p-room", "approved_no_room")}
		default:
			page.Calls = []apiclient.ActivityCall{call("p-appr", "approval_required")}
		}
		writeJSONForTest(w, 200, page)
	})
	apiMux.HandleFunc("GET /v1/me/card-exceptions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, 503, map[string]string{"error": "unavailable"})
	})
	apiMux.HandleFunc("GET /v1/me/mandates", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, 200, map[string]any{"mandates": []apiclient.Mandate{}})
	})
	apiMux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, 200, map[string]any{"subject": "alice", "tenant_id": "t1", "account_kind": "tools"})
	})
	h, cookie := attentionRig(t, apiMux)
	get := func(path string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}

	list := get("/activity?page=200")
	for _, want := range []string{`<option value="guard-bot">guard-bot</option>`, `href="/activity?page=201"`, "Waiting for approval"} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q", want)
		}
	}
	if strings.Contains(list, ">Refused<") {
		t.Error("a pending approval reads as refused")
	}
	get("/activity?page=201")
	mu.Lock()
	last := asked[len(asked)-1]
	mu.Unlock()
	if last != "limit=50&offset=10050" {
		t.Errorf("page 201 asked %q, want offset 10050", last)
	}

	appr := get("/activity/p-appr")
	if !strings.Contains(appr, "held it for an approver") || !strings.Contains(appr, "Answer it on Needs you") || strings.Contains(appr, "refused") {
		t.Error("a domain approval's page does not say it waits, or where to answer it")
	}
	own := get("/activity/p-own")
	if !strings.Contains(own, "A Card held it for you") || !strings.Contains(own, "could not be read") || !strings.Contains(own, `href="/needs-you"`) || strings.Contains(own, "refused") {
		t.Error("an owner's hold with unreadable answers reads as refused, or hides that its answer is unknown")
	}
	// a review: approved but without room, with the held calls
	// unreadable, is not asked for another answer.
	room := get("/activity/p-room")
	for _, want := range []string{"Approved, waiting for room", "It was approved, but the Card has no room for it yet", "could not be read", `href="/needs-you"`} {
		if !strings.Contains(room, want) {
			t.Errorf("approved, no room lacks %q", want)
		}
	}
	if strings.Contains(room, "until you answer") || strings.Contains(room, "Waiting for you") || strings.Contains(room, "refused") {
		t.Error("an approval kept for lack of room asks for another answer, or reads as refused")
	}
}
