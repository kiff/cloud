package handlers_test

import (
	"encoding/json"
	"fmt"
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

// attentionRig serves the dashboard against a fake API whose attention
// routes are supplied by the test.
func attentionRig(t *testing.T, apiMux *http.ServeMux) (http.Handler, *http.Cookie) {
	t.Helper()
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)
	apiClient, err := apiclient.NewSessionAware(apiSrv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestSessionManager(t)
	dashH := handlers.NewDashboardHandler(apiClient, mgr, nil, nil)
	mux := http.NewServeMux()
	dashH.Register(mux)
	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{
		Subject: "alice", Bearer: "session-bearer", Email: "alice@example.com",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return handlers.RequireSession(mgr)(mux), issue.Result().Cookies()[0]
}

// An action on a work item returns to the Needs you page it came from;
// losing a claim race and an outage each come back with a flash that
// says which, never as another person's claim. A return address outside
// Needs you is ignored.
func TestAttentionAction_ReturnsToNeedsYou(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, back, wantLoc string
		apiCode             int
		wantFlash           bool
	}{
		{"claimed", "/needs-you?tab=blocked&page=2", "/needs-you?tab=blocked&page=2", http.StatusNoContent, false},
		{"lost race", "/needs-you", "/needs-you", http.StatusConflict, true},
		{"api down", "", "/needs-you", http.StatusServiceUnavailable, true},
		{"elsewhere", "https://example.com/needs-you", "/needs-you", http.StatusNoContent, false},
		{"scheme-relative", "//example.com", "/needs-you", http.StatusNoContent, false},
		{"another needs-you page", "/needs-you/exc-1", "/needs-you", http.StatusNoContent, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apiMux := http.NewServeMux()
			apiMux.HandleFunc("POST /v1/me/attention/{id}/{action}", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.apiCode)
			})
			h, cookie := attentionRig(t, apiMux)
			form := url.Values{"revision": {"3"}, "back": {tc.back}}
			req := httptest.NewRequest(http.MethodPost, "/dashboard/attention/approval:a1/claim", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != tc.wantLoc {
				t.Fatalf("got %d %q, want 303 %q", rec.Code, rec.Header().Get("Location"), tc.wantLoc)
			}
			flashed := false
			for _, c := range rec.Result().Cookies() {
				if c.Name == session.FlashCookieName && c.Value != "" {
					flashed = true
				}
			}
			if flashed != tc.wantFlash {
				t.Errorf("flash = %v, want %v", flashed, tc.wantFlash)
			}
		})
	}
}

// Clear all acknowledges exactly the items and revisions the bell rendered.
// An item that arrived after the render is not in the form, so it is never
// sent as seen.
func TestClearNotifications_SendsOnlyRenderedRevisions(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got map[string]int64
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("POST /v1/me/attention/seen", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Items map[string]int64 `json:"items"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got = body.Items
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	h, cookie := attentionRig(t, apiMux)

	form := url.Values{"seen:approval:a1": {"2"}, "seen:decision:p9": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/notifications/clear", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	want := map[string]int64{"approval:a1": 2, "decision:p9": 1}
	if len(got) != len(want) || got["approval:a1"] != 2 || got["decision:p9"] != 1 {
		t.Fatalf("acknowledged %v, want exactly %v", got, want)
	}
}

// If the acknowledgement cannot be saved, the bell is not cleared: the user
// is told to retry rather than silently losing unseen work.
func TestClearNotifications_AckFailureDoesNotClear(t *testing.T) {
	t.Parallel()
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("POST /v1/me/attention/seen", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	h, cookie := attentionRig(t, apiMux)
	req := httptest.NewRequest(http.MethodPost, "/dashboard/notifications/clear", strings.NewReader("seen:approval:a1=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookie.Name {
			t.Fatal("session re-issued (bell cleared) although the acknowledgement failed")
		}
	}
}

// Needs you took in Needs attention. The old page redirects there; a
// domain approval waits in Waiting beside the Card holds, a blocked call
// is on Blocked with Open the call, and neither tab shows Card warnings.
func TestNeedsYouTakesInNeedsAttention(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 13, 1, 57, 0, 0, time.UTC)
	warning := apiclient.AttentionItem{ID: "card-warning:w1", Kind: "card_warning", SourceID: "card-a", Action: "card_budget", Reason: "This Card total is spent.", Status: "open", CreatedAt: at, Unread: true}
	approval := apiclient.AttentionItem{ID: "approval:a1", Kind: "approval", SourceID: "a1", ActorID: "guard-bot", DomainID: "card-refund", Action: "REFUND_ORDER", EntityID: "order-1043", Status: "open", CreatedAt: at, Revision: 2}
	blocked := apiclient.AttentionItem{ID: "decision:p-9", Kind: "blocked", SourceID: "p-9", ActorID: "support-agent", Action: "CLOSE_TICKET", EntityID: "t-1", Reason: "blocked by policy", Status: "open", CreatedAt: at, Revision: 4, Assignee: "alice"}
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /v1/me/attention", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("kind") {
		case "card_warning":
			writeJSONForTest(w, 200, apiclient.AttentionFeed{Items: []apiclient.AttentionItem{warning}, Open: 1, Unread: 1})
		case "approval":
			writeJSONForTest(w, 200, apiclient.AttentionFeed{Items: []apiclient.AttentionItem{approval}, Open: 1})
		case "blocked":
			writeJSONForTest(w, 200, apiclient.AttentionFeed{Items: []apiclient.AttentionItem{blocked}, Open: 1})
		default:
			writeJSONForTest(w, 200, apiclient.AttentionFeed{Items: []apiclient.AttentionItem{warning, approval, blocked}, Open: 3})
		}
	})
	apiMux.HandleFunc("GET /v1/me/card-exceptions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, 200, map[string]any{"exceptions": []any{}})
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
	if rec := get("/dashboard/attention"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/needs-you" {
		t.Fatalf("old queue = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	waiting := get("/needs-you").Body.String()
	for _, want := range []string{`id="approval:a1"`, "Domain approval", "REFUND_ORDER", `action="/dashboard/approvals/a1/grant"`, `action="/dashboard/approvals/a1/deny"`,
		`action="/dashboard/attention/approval:a1/claim"`, `name="back" value="/needs-you"`, "1 call is waiting for an answer.", `<span class="ac-tab-count is-warn">1</span>`} {
		if !strings.Contains(waiting, want) {
			t.Errorf("Waiting lacks %q", want)
		}
	}
	if strings.Contains(waiting, `id="decision:p-9"`) || strings.Contains(waiting, warning.Reason) {
		t.Error("Waiting lists blocked calls or Card warnings")
	}
	blockedTab := get("/needs-you?tab=blocked").Body.String()
	for _, want := range []string{`id="decision:p-9"`, `href="/activity/p-9"`, `action="/dashboard/attention/decision:p-9/resolve"`,
		`action="/dashboard/attention/decision:p-9/release"`, `name="back" value="/needs-you?tab=blocked"`, "Nothing ran."} {
		if !strings.Contains(blockedTab, want) {
			t.Errorf("Blocked lacks %q", want)
		}
	}
	if strings.Contains(blockedTab, `id="approval:a1"`) || strings.Contains(blockedTab, warning.Reason) {
		t.Error("Blocked lists approvals or Card warnings")
	}
}

// a review: Warnings is the only list of Card warnings, so the ones
// past the API's first 100 stay reachable and the count is all of
// them, here and on the attention queue's pointer.
func TestNeedsYouWarningsPagesPastTheFirst100(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 13, 1, 57, 0, 0, time.UTC)
	const open = 150
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /v1/me/attention", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("kind") != "card_warning" {
			writeJSONForTest(w, 200, apiclient.AttentionFeed{})
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		feed := apiclient.AttentionFeed{Open: open, Unread: open}
		for i := page * 100; i < min(open, page*100+100); i++ {
			feed.Items = append(feed.Items, apiclient.AttentionItem{ID: fmt.Sprintf("card-warning:w%d", i+1), Kind: "card_warning",
				SourceID: fmt.Sprintf("card-%d", i+1), Reason: "This Card total is spent.", Status: "open", CreatedAt: at})
		}
		feed.Truncated = open > page*100+len(feed.Items)
		writeJSONForTest(w, 200, feed)
	})
	apiMux.HandleFunc("GET /v1/me/card-exceptions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, 200, map[string]any{"exceptions": []any{}})
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
	first := get("/needs-you?tab=warnings")
	for _, want := range []string{`<span class="ac-tab-count is-warn">150</span>`, "1–100 of 150 open warnings", `href="/needs-you?tab=warnings&amp;page=1"`, `href="/cards/card-100"`} {
		if !strings.Contains(first, want) {
			t.Errorf("first page lacks %q", want)
		}
	}
	if strings.Contains(first, `href="/cards/card-101"`) || strings.Contains(first, "Previous page") {
		t.Error("first page shows the second")
	}
	second := get("/needs-you?tab=warnings&page=1")
	for _, want := range []string{"101–150 of 150 open warnings", `href="/cards/card-101"`, `href="/cards/card-150"`, `href="/needs-you?tab=warnings"`, "Previous page", `data-live-src="/needs-you?tab=warnings&amp;page=1"`} {
		if !strings.Contains(second, want) {
			t.Errorf("second page lacks %q", want)
		}
	}
	if strings.Contains(second, "Next page") {
		t.Error("the last page offers a next one")
	}
	if waiting := get("/needs-you"); !strings.Contains(waiting, "150 Cards are running low or used up") {
		t.Error("Waiting counts only the first page")
	}
}

// Opening Warnings marks what it shows as seen, so the badge and the
// line on Waiting count only unseen ones; Dismiss claims then resolves
// the warning, and a conflict is said, not hidden.
func TestNeedsYouWarningsSeenAndDismiss(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 13, 1, 57, 0, 0, time.UTC)
	var mu sync.Mutex
	var seen []map[string]int64
	var acts []string
	conflict := false
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /v1/me/attention", func(w http.ResponseWriter, r *http.Request) {
		if kind := r.URL.Query().Get("kind"); kind != "" && kind != "card_warning" {
			writeJSONForTest(w, 200, apiclient.AttentionFeed{})
			return
		}
		mu.Lock()
		defer mu.Unlock()
		feed := apiclient.AttentionFeed{Open: 2, Unread: 2, Items: []apiclient.AttentionItem{
			{ID: "card-warning:w1", Kind: "card_warning", SourceID: "card-a", Reason: "This Card total is spent.", Status: "open", CreatedAt: at, Revision: 3, Unread: true},
			{ID: "card-warning:w2", Kind: "card_warning", SourceID: "card-b", Reason: "Running low.", Status: "open", CreatedAt: at, Revision: 1, Unread: true},
		}}
		if len(seen) > 0 {
			feed.Unread, feed.Items[0].Unread, feed.Items[1].Unread = 0, false, false
		}
		writeJSONForTest(w, 200, feed)
	})
	apiMux.HandleFunc("POST /v1/me/attention/seen", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Items map[string]int64 }
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seen = append(seen, body.Items)
		mu.Unlock()
		w.WriteHeader(204)
	})
	apiMux.HandleFunc("POST /v1/me/attention/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Revision int64 }
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		acts = append(acts, r.PathValue("id")+" "+r.PathValue("action")+" "+strconv.FormatInt(body.Revision, 10))
		if conflict {
			writeJSONForTest(w, 409, map[string]string{"error": "conflict"})
			return
		}
		w.WriteHeader(204)
	})
	apiMux.HandleFunc("GET /v1/me/card-exceptions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, 200, map[string]any{"exceptions": []any{}})
	})
	apiMux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, 200, map[string]any{"subject": "alice", "tenant_id": "t1", "account_kind": "tools"})
	})
	h, cookie := attentionRig(t, apiMux)
	do := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		var req *http.Request
		if form != nil {
			req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			req = httptest.NewRequest(method, path, nil)
		}
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Waiting doesn't mark anything seen and counts the unseen one.
	waiting := do(http.MethodGet, "/needs-you", nil).Body.String()
	if !strings.Contains(waiting, `<span class="ac-tab-count is-warn">2</span>`) || len(seen) != 0 {
		t.Fatalf("waiting: seen=%v", seen)
	}
	page := do(http.MethodGet, "/needs-you?tab=warnings", nil).Body.String()
	if len(seen) != 1 || len(seen[0]) != 2 || seen[0]["card-warning:w1"] != 3 || seen[0]["card-warning:w2"] != 1 {
		t.Fatalf("seen = %v, want both shown warnings at their revisions", seen)
	}
	if !strings.Contains(page, `class="att-new">New</span>`) || strings.Contains(page, `<span class="ac-tab-count is-warn">`) {
		t.Error("the warning shown says New this once, and the badge drops what was just seen")
	}
	if !strings.Contains(page, `action="/needs-you/warnings/card-warning:w1/dismiss"`) || !strings.Contains(page, `name="revision" value="3"`) {
		t.Error("no Dismiss form at the warning's revision")
	}
	// Seen, they stop asking: no badge, no line on Waiting, still listed.
	if waiting := do(http.MethodGet, "/needs-you", nil).Body.String(); strings.Contains(waiting, "running low or used up") || strings.Contains(waiting, `<span class="ac-tab-count is-warn">`) {
		t.Error("Waiting still counts seen warnings")
	}
	if again := do(http.MethodGet, "/needs-you?tab=warnings", nil).Body.String(); !strings.Contains(again, "card-a") || strings.Contains(again, `class="att-new"`) || len(seen) != 1 {
		t.Error("seen warnings leave the list, or say New again")
	}

	rec := do(http.MethodPost, "/needs-you/warnings/card-warning:w1/dismiss", url.Values{"revision": {"3"}, "page": {"1"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/needs-you?tab=warnings&page=1" {
		t.Fatalf("dismiss = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if strings.Join(acts, "|") != "card-warning:w1 claim 3|card-warning:w1 resolve 4" {
		t.Errorf("acts = %v, want claim at 3 then resolve at 4", acts)
	}
	conflict, acts = true, nil
	if rec := do(http.MethodPost, "/needs-you/warnings/card-warning:w1/dismiss", url.Values{"revision": {"3"}}); rec.Code != http.StatusSeeOther || len(acts) != 1 {
		t.Errorf("conflict: %d %v", rec.Code, acts)
	}
	if rec := do(http.MethodPost, "/needs-you/warnings/decision:d1/dismiss", url.Values{"revision": {"1"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("a non-warning item can be dismissed here: %d", rec.Code)
	}
}

// a review. The fake follows the attention store's rules (claim only when
// unassigned, resolve only by the assignee, both at the current
// revision). A claim that commits but whose answer is lost leaves the
// warning held by this person, and Dismiss then resolves it directly; a
// warning someone else holds offers no Dismiss; a page that shows only
// its load error marks nothing seen.
func TestNeedsYouDismissFollowsAssignment(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 13, 1, 57, 0, 0, time.UTC)
	type item struct {
		rev      int64
		assignee string
		resolved bool
	}
	var mu sync.Mutex
	items := map[string]*item{"card-warning:w1": {rev: 3}, "card-warning:w2": {rev: 5, assignee: "bob"}}
	loseClaimAnswer, holdsDown := true, false
	seenCalls := 0
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /v1/me/attention", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		feed := apiclient.AttentionFeed{}
		for _, id := range []string{"card-warning:w1", "card-warning:w2"} {
			it := items[id]
			if it.resolved {
				continue
			}
			feed.Items = append(feed.Items, apiclient.AttentionItem{ID: id, Kind: "card_warning", SourceID: "card-" + id[len(id)-2:], Reason: "Running low.",
				Status: "open", CreatedAt: at, Revision: it.rev, Assignee: it.assignee, Unread: true})
			feed.Open++
			feed.Unread++
		}
		writeJSONForTest(w, 200, feed)
	})
	apiMux.HandleFunc("POST /v1/me/attention/seen", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		seenCalls++
		mu.Unlock()
		w.WriteHeader(204)
	})
	apiMux.HandleFunc("POST /v1/me/attention/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Revision int64 }
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		it := items[r.PathValue("id")]
		ok := it != nil && !it.resolved && it.rev == body.Revision
		switch r.PathValue("action") {
		case "claim":
			ok = ok && it.assignee == ""
			if ok {
				it.assignee, it.rev = "alice", it.rev+1
				if loseClaimAnswer {
					loseClaimAnswer = false
					writeJSONForTest(w, 503, map[string]string{"error": "attention unavailable"})
					return
				}
			}
		case "resolve":
			ok = ok && it.assignee == "alice"
			if ok {
				it.resolved, it.rev = true, it.rev+1
			}
		default:
			ok = false
		}
		if !ok {
			writeJSONForTest(w, 409, map[string]string{"error": "conflict"})
			return
		}
		w.WriteHeader(204)
	})
	apiMux.HandleFunc("GET /v1/me/card-exceptions", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		down := holdsDown
		mu.Unlock()
		if down {
			writeJSONForTest(w, 503, map[string]string{"error": "unavailable"})
			return
		}
		writeJSONForTest(w, 200, map[string]any{"exceptions": []any{}})
	})
	apiMux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, 200, map[string]any{"subject": "alice", "tenant_id": "t1", "account_kind": "tools"})
	})
	h, cookie := attentionRig(t, apiMux)
	state := func(id string) item {
		mu.Lock()
		defer mu.Unlock()
		return *items[id]
	}
	setHoldsDown := func(v bool) {
		mu.Lock()
		holdsDown = v
		mu.Unlock()
	}
	seen := func() int {
		mu.Lock()
		defer mu.Unlock()
		return seenCalls
	}
	do := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		var req *http.Request
		if form != nil {
			req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			req = httptest.NewRequest(method, path, nil)
		}
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Holds unreadable: the Warnings tab still shows its warnings, says
	// the holds are missing, and marks seen only what it shows.
	setHoldsDown(true)
	if page := do(http.MethodGet, "/needs-you?tab=warnings", nil).Body.String(); !strings.Contains(page, "card-w1") || !strings.Contains(page, "could not be loaded") || seen() != 1 {
		t.Fatalf("warnings with holds unreadable: shown %v, seen calls %d", strings.Contains(page, "card-w1"), seen())
	}
	setHoldsDown(false)

	page := do(http.MethodGet, "/needs-you?tab=warnings", nil).Body.String()
	if !strings.Contains(page, "bob is handling this one.") || strings.Contains(page, "card-warning:w2/dismiss") {
		t.Error("a warning someone else holds offers Dismiss")
	}
	// The claim commits but its answer is lost: an error, and the warning
	// is now held by this person at revision 4.
	do(http.MethodPost, "/needs-you/warnings/card-warning:w1/dismiss", url.Values{"revision": {"3"}})
	if it := state("card-warning:w1"); it.assignee != "alice" || it.resolved || it.rev != 4 {
		t.Fatalf("after a lost claim answer: %+v", it)
	}
	page = do(http.MethodGet, "/needs-you?tab=warnings", nil).Body.String()
	if !strings.Contains(page, `name="held" value="mine"`) || !strings.Contains(page, `name="revision" value="4"`) {
		t.Fatal("the warning this person holds offers no way to finish dismissing it")
	}
	if rec := do(http.MethodPost, "/needs-you/warnings/card-warning:w1/dismiss", url.Values{"revision": {"4"}, "held": {"mine"}}); rec.Code != http.StatusSeeOther || !state("card-warning:w1").resolved {
		t.Errorf("dismissing a warning this person holds: %d %+v", rec.Code, state("card-warning:w1"))
	}
}

// a review: past 150 domain approvals every one is a page away on
// Waiting, each with its own Approve and Deny that return to that page; a
// failed Card-hold read hides nothing else; an incomplete projection is
// said, never an empty "nothing here".
func TestNeedsYouApprovalsPagesHoldsDownAndIncomplete(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 13, 1, 57, 0, 0, time.UTC)
	const approvals = 160
	var mu sync.Mutex
	holdsDown, pending, failed := false, 0, 0
	var granted []string
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /v1/me/attention", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		feed := apiclient.AttentionFeed{Pending: pending, Failed: failed}
		switch r.URL.Query().Get("kind") {
		case "approval":
			feed.Open = approvals
			for i := page * 100; i < min(approvals, page*100+100); i++ {
				feed.Items = append(feed.Items, apiclient.AttentionItem{ID: fmt.Sprintf("approval:a%d", i+1), Kind: "approval", SourceID: fmt.Sprintf("a%d", i+1),
					Action: "REFUND_ORDER", Status: "open", CreatedAt: at, Revision: 1})
			}
			feed.Truncated = approvals > page*100+len(feed.Items)
		case "blocked":
			if pending == 0 && failed == 0 {
				feed.Open = 1
				feed.Items = []apiclient.AttentionItem{{ID: "decision:p-9", Kind: "blocked", SourceID: "p-9", Action: "CLOSE_TICKET", Status: "open", CreatedAt: at, Revision: 1}}
			}
		}
		writeJSONForTest(w, 200, feed)
	})
	apiMux.HandleFunc("POST /v1/approvals/{id}/grant", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		granted = append(granted, r.PathValue("id"))
		mu.Unlock()
		writeJSONForTest(w, 200, map[string]any{})
	})
	apiMux.HandleFunc("GET /v1/me/card-exceptions", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		down := holdsDown
		mu.Unlock()
		if down {
			writeJSONForTest(w, 503, map[string]string{"error": "unavailable"})
			return
		}
		writeJSONForTest(w, 200, map[string]any{"exceptions": []any{}})
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

	first := get("/needs-you")
	for _, want := range []string{"1–100 of 160 domain approvals", `action="/dashboard/approvals/a1/grant"`, `action="/dashboard/approvals/a100/deny"`, `href="/needs-you?apage=1"`} {
		if !strings.Contains(first, want) {
			t.Errorf("approvals page 1 lacks %q", want)
		}
	}
	second := get("/needs-you?apage=1")
	for _, want := range []string{"101–160 of 160 domain approvals", `action="/dashboard/approvals/a101/grant"`, `action="/dashboard/approvals/a160/grant"`,
		`name="back" value="/needs-you?apage=1"`, `href="/needs-you"`, `data-live-src="/needs-you?apage=1"`} {
		if !strings.Contains(second, want) {
			t.Errorf("approvals page 2 lacks %q", want)
		}
	}
	if strings.Contains(second, `approvals/a100/grant"`) || strings.Contains(second, "More approvals") {
		t.Error("the last approvals page repeats the first or offers more")
	}
	// Approve from page 2 returns to page 2.
	req := httptest.NewRequest(http.MethodPost, "/dashboard/approvals/a150/grant", strings.NewReader(url.Values{"back": {"/needs-you?apage=1"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/needs-you?apage=1" || len(granted) != 1 || granted[0] != "a150" {
		t.Errorf("grant = %d %q %v", rec.Code, rec.Header().Get("Location"), granted)
	}

	// Card holds down: approvals and blocked calls still show.
	mu.Lock()
	holdsDown = true
	mu.Unlock()
	if page := get("/needs-you"); !strings.Contains(page, "could not be loaded") || !strings.Contains(page, `action="/dashboard/approvals/a1/grant"`) {
		t.Error("a Card-hold failure hides domain approvals")
	}
	if page := get("/needs-you?tab=blocked"); !strings.Contains(page, "could not be loaded") || !strings.Contains(page, `id="decision:p-9"`) {
		t.Error("a Card-hold failure hides blocked calls")
	}
	// a review: the answers come from the same failed read, so Answered
	// says they are unavailable, never that nothing was answered.
	if page := get("/needs-you?tab=answered"); strings.Contains(page, "Nothing answered yet.") || !strings.Contains(page, "Your answers could not be loaded") {
		t.Error("a Card-hold failure reads as an empty answer history")
	}
	mu.Lock()
	holdsDown, pending = false, 2
	mu.Unlock()

	// Projection pending, then failed: say so; never "Nothing is blocked".
	for _, state := range []string{"pending", "failed"} {
		if state == "failed" {
			mu.Lock()
			pending, failed = 0, 1
			mu.Unlock()
		}
		blocked := get("/needs-you?tab=blocked")
		if !strings.Contains(blocked, "still being processed") || strings.Contains(blocked, "Nothing is blocked.") {
			t.Errorf("%s projection: blocked tab claims it is empty", state)
		}
		if waiting := get("/needs-you"); !strings.Contains(waiting, "still being processed") {
			t.Errorf("%s projection: Waiting does not say approvals may be missing", state)
		}
	}
}
