package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/notify"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

func renderNotifications(t *testing.T, v pages.DashboardView, sum notify.Summary) string {
	t.Helper()
	var buf bytes.Buffer
	ctx := notify.WithSummary(context.Background(), sum)
	if err := pages.DashboardNotifications(v).Render(ctx, &buf); err != nil {
		t.Fatalf("render notifications: %v", err)
	}
	return buf.String()
}

func notifSampleSummary(now time.Time) notify.Summary {
	return notify.Compose(
		[]apiclient.PendingApproval{
			{ID: "a1", ActionName: "refund_order", EntityID: "order_731", EntityType: "Order", RequestedBy: "refund-agent", CreatedAt: now.Add(-8 * time.Minute)},
		},
		[]apiclient.GuardConnection{
			{AgentID: "support-agent", Workflow: "close-ticket", Environment: "production", LastSeenAt: now.Add(-48 * time.Hour)},
		},
		nil,
		now,
	)
}

// The notifications page renders the feed (approval + idle-runtime) and
// the filter chips with their counts.
func TestNotificationsPage_RendersFeedAndFilters(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Me = &apiclient.Me{}
	body := renderNotifications(t, v, notifSampleSummary(time.Now().UTC()))

	for _, want := range []string{
		"Notifications",
		"refund_order is waiting for approval",
		"order_731 · requested by refund-agent",
		"close-ticket has gone quiet",
		"All · 2",
		"Approvals · 1",
		"Runtime health · 1",
		"dash-notif-row",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("notifications page missing %q", want)
		}
	}
}

// The ?filter= narrows the feed to one kind (the counts stay complete on
// the chips).
func TestNotificationsPage_FilterNarrowsFeed(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Me = &apiclient.Me{}
	v.NotificationsFilter = string(notify.KindRuntime)
	body := renderNotifications(t, v, notifSampleSummary(time.Now().UTC()))

	if !strings.Contains(body, "close-ticket has gone quiet") {
		t.Error("runtime filter should keep the idle-runtime item")
	}
	// The page feed narrows to exactly the runtime row. (The topbar bell
	// always shows the full recent feed, so scope this to the page's own
	// rows, not the whole document.)
	if n := strings.Count(body, "dash-notif-row"); n != 1 {
		t.Errorf("runtime filter should leave exactly 1 page row, got %d", n)
	}
	// Chip counts stay complete across kinds even under a filter.
	if !strings.Contains(body, "Approvals · 1") {
		t.Error("chip counts should stay complete under a filter")
	}
}

// The topbar bell shows the actionable badge (approvals only) and the
// items, and renders on the page shell.
func TestNotificationsPage_BellBadgeAndItems(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Me = &apiclient.Me{}
	body := renderNotifications(t, v, notifSampleSummary(time.Now().UTC()))

	for _, want := range []string{
		`class="dash-bell dash-autoclose"`,
		`class="dash-bell-badge"`, // one actionable approval → badge shows
		`>1</span>`,               // badge count = 1 (idle runtime not counted)
		"dash-bell-item",
		`href="/dashboard/notifications"`, // view-all link
	} {
		if !strings.Contains(body, want) {
			t.Errorf("bell missing %q\n---\n%s", want, body)
		}
	}
}

// A quiet workspace renders no badge and an all-caught-up empty state.
func TestNotificationsPage_EmptyState(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Me = &apiclient.Me{}
	body := renderNotifications(t, v, notify.Summary{})

	if strings.Contains(body, `class="dash-bell-badge"`) {
		t.Error("no actionable items → no badge")
	}
	if !strings.Contains(body, "Nothing here") && !strings.Contains(body, "caught up") {
		t.Error("empty feed should render an honest empty state")
	}
}

func renderNotifSettings(t *testing.T, v pages.DashboardView) string {
	t.Helper()
	var buf bytes.Buffer
	v.SettingsTab = "notifications"
	if err := pages.DashboardSettings(v).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render notification settings: %v", err)
	}
	return buf.String()
}

// The settings page renders the real rules with their status + a toggle
// reflecting the tenant's prefs (runtime muted here).
func TestNotificationSettings_RendersRulesFromPrefs(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Me = &apiclient.Me{}
	v.NotificationPrefs = map[string]bool{"approval": true, "runtime_idle": false}
	body := renderNotifSettings(t, v)

	// The rules are Settings › Notifications, and the sidebar no longer
	// lists Notifications (the bell is the feed).
	if strings.Contains(body, `href="/dashboard/notifications"><span`) || strings.Contains(body, `dash-nav-item" href="/dashboard/notifications"`) {
		t.Error("the sidebar still lists Notifications")
	}
	for _, want := range []string{
		`href="/dashboard/settings?tab=notifications" aria-current="page"`,
		"Approval required",
		// a review: muting hides the notice, not the work, and both
		// places the work waits are named.
		"the work itself still waits on <a href=\"/needs-you\">Needs you</a>", "domain approvals in Waiting, blocked calls in Blocked",
		"Runtime went quiet",
		"dash-rule-row",
		`action="/dashboard/notifications/settings"`,
		`name="rule" value="runtime_idle"`,
		// Held calls' emails, on unless the account turns them off.
		"Email about held calls",
		`name="rule" value="held_email"`,
		"Turn on",  // runtime muted → offer to turn on
		"Turn off", // approval enabled → offer to turn off
	} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page missing %q", want)
		}
	}
}

// Toggling a rule merges onto current prefs, PUTs, and redirects back
// with a flash; the other rule is preserved.
func TestNotificationSettings_TogglePersistsAndRedirects(t *testing.T) {
	t.Parallel()
	var putBody map[string]bool
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /v1/me/notification-prefs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{"prefs": map[string]bool{"approval": true, "runtime_idle": true}})
	})
	apiMux.HandleFunc("PUT /v1/me/notification-prefs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prefs map[string]bool `json:"prefs"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		putBody = body.Prefs
		writeJSONForTest(w, http.StatusOK, map[string]any{"prefs": body.Prefs})
	})
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)

	apiClient, err := apiclient.NewSessionAware(apiSrv.URL, 0)
	if err != nil {
		t.Fatalf("NewSessionAware: %v", err)
	}
	mgr := newTestSessionManager(t)
	dashMux := http.NewServeMux()
	handlers.NewDashboardHandler(apiClient, mgr, nil, nil).Register(dashMux)
	protected := handlers.RequireSession(mgr)(dashMux)

	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{
		Subject: "alice", Bearer: "session-bearer", Email: "alice@example.com",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	form := "rule=runtime_idle&enabled=false"
	req := httptest.NewRequest(http.MethodPost, "/dashboard/notifications/settings", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issue.Result().Cookies()[0])
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body=%q)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/settings?tab=notifications" {
		t.Fatalf("Location = %q, want Settings › Notifications", loc)
	}
	// The old page sends you to Settings › Notifications.
	old := httptest.NewRequest(http.MethodGet, "/dashboard/notifications/settings", nil)
	old.AddCookie(issue.Result().Cookies()[0])
	oldRec := httptest.NewRecorder()
	protected.ServeHTTP(oldRec, old)
	if oldRec.Code != http.StatusSeeOther || oldRec.Header().Get("Location") != "/dashboard/settings?tab=notifications" {
		t.Errorf("old settings page = %d %q", oldRec.Code, oldRec.Header().Get("Location"))
	}
	if putBody["runtime_idle"] != false {
		t.Errorf("runtime_idle should be persisted false, got %+v", putBody)
	}
	if putBody["approval"] != true {
		t.Errorf("approval should be preserved true, got %+v", putBody)
	}

	// The held-call email is a rule like the others.
	req = httptest.NewRequest(http.MethodPost, "/dashboard/notifications/settings", strings.NewReader("rule=held_email&enabled=false"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issue.Result().Cookies()[0])
	rec = httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || putBody["held_email"] != false || putBody["approval"] != true {
		t.Errorf("held_email toggle = %d, put %+v", rec.Code, putBody)
	}
}

// WithNotifications composes the summary onto the context for GET
// requests, from the approvals + connections the API returns.
func TestWithNotifications_EnrichesGet(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /v1/approvals", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"approvals": []map[string]any{
				{"id": "a1", "action_name": "refund_order", "entity_id": "order_1", "entity_type": "Order", "requested_by": "refund-agent", "created_at": now.Add(-5 * time.Minute).Format(time.RFC3339)},
			},
		})
	})
	apiMux.HandleFunc("GET /v1/guard/connections", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"connections": []map[string]any{
				{"agent_id": "idle-agent", "last_seen_at": now.Add(-72 * time.Hour).Format(time.RFC3339)},
			},
		})
	})
	apiMux.HandleFunc("GET /v1/me/notification-prefs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{"prefs": map[string]bool{"approval": true, "runtime_idle": true}})
	})
	apiMux.HandleFunc("GET /v1/me/domains", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{"domains": []any{}})
	})
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)

	apiClient, err := apiclient.NewSessionAware(apiSrv.URL, 0)
	if err != nil {
		t.Fatalf("NewSessionAware: %v", err)
	}
	mgr := newTestSessionManager(t)
	dashH := handlers.NewDashboardHandler(apiClient, mgr, nil, nil)

	var gotCount, gotItems int
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sum := notify.SummaryFrom(r.Context())
		gotCount = sum.Count
		gotItems = len(sum.Items)
		w.WriteHeader(http.StatusOK)
	})
	gated := handlers.RequireSession(mgr)(dashH.WithChrome(probe))

	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{
		Subject: "alice", Bearer: "session-bearer", Email: "alice@example.com",
		ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/dashboard/notifications", nil)
	req.AddCookie(issue.Result().Cookies()[0])
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotCount != 1 {
		t.Errorf("badge count = %d, want 1 (one approval; idle runtime not counted)", gotCount)
	}
	if gotItems != 2 {
		t.Errorf("items = %d, want 2 (approval + idle runtime)", gotItems)
	}
}

// The chrome middleware surfaces an in-progress draft as a first-class
// "Draft" item in the sidebar domain selector — linking to Build, not a
// domain home that doesn't exist yet — alongside any active domains.
func TestWithChrome_SurfacesInProgressDraftInSelector(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /v1/approvals", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{"approvals": []any{}})
	})
	apiMux.HandleFunc("GET /v1/guard/connections", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{"connections": []any{}})
	})
	apiMux.HandleFunc("GET /v1/me/notification-prefs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{"prefs": map[string]bool{}})
	})
	apiMux.HandleFunc("GET /v1/me/domains", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"domains": []map[string]any{
				{"name": "payments", "status": "active", "posture": "enforce"},
			},
		})
	})
	apiMux.HandleFunc("GET /v1/me/domain/draft", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"yaml":   "domain: support-ops\nentity: Ticket\n",
			"parsed": map[string]any{"domain": "support-ops", "entity": "Ticket"},
		})
	})
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)

	apiClient, err := apiclient.NewSessionAware(apiSrv.URL, 0)
	if err != nil {
		t.Fatalf("NewSessionAware: %v", err)
	}
	mgr := newTestSessionManager(t)
	dashH := handlers.NewDashboardHandler(apiClient, mgr, nil, nil)

	var draftFound, activeFound bool
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nav := components.DomainNavFrom(r.Context())
		for _, it := range nav.Items {
			if it.Name == "support-ops" && it.Draft {
				draftFound = true
			}
			if it.Name == "payments" && !it.Draft {
				activeFound = true
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	gated := handlers.RequireSession(mgr)(dashH.WithChrome(probe))

	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{
		Subject: "alice", Bearer: "session-bearer", Email: "alice@example.com",
		ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/dashboard/build", nil)
	req.AddCookie(issue.Result().Cookies()[0])
	gated.ServeHTTP(httptest.NewRecorder(), req)

	if !activeFound {
		t.Error("selector should list the active domain")
	}
	if !draftFound {
		t.Error("selector should surface the in-progress draft as a Draft item")
	}
}

type countingBearerSource struct{ n atomic.Int32 }

func (c *countingBearerSource) BearerFor(context.Context, session.Session) (string, error) {
	return "minted-" + strconv.Itoa(int(c.n.Add(1))), nil
}

// A card page behind the chrome middleware mints one bearer, not one for
// the chrome and another for the page: each mint is a Clerk round trip.
func TestWithChrome_PageReusesTheRequestBearer(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	seen := map[string]bool{}
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Header.Get("Authorization")] = true
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/me/mandates":
			writeJSONForTest(w, http.StatusOK, map[string]any{"mandates": []map[string]any{{"id": "card-1", "subject": "agent-1"}}})
		case "/v1/me/mandates/card-1/statement":
			writeJSONForTest(w, http.StatusOK, map[string]any{"limits": []any{}})
		default:
			writeJSONForTest(w, http.StatusNotFound, map[string]any{"error": "not found"})
		}
	})
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)
	apiClient, err := apiclient.NewSessionAware(apiSrv.URL, 0)
	if err != nil {
		t.Fatalf("NewSessionAware: %v", err)
	}
	mgr := newTestSessionManager(t)
	bearers := &countingBearerSource{}
	dashH := handlers.NewDashboardHandler(apiClient, mgr, bearers, nil)
	mux := http.NewServeMux()
	dashH.Register(mux)
	gated := handlers.RequireSession(mgr)(dashH.WithChrome(mux))

	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{
		Subject: "alice", Bearer: "stored-bearer", SessionID: "sess_1", Email: "alice@example.com",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/cards/card-1", nil)
	req.AddCookie(issue.Result().Cookies()[0])
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := bearers.n.Load(); got != 1 {
		t.Errorf("bearer minted %d times for one page, want 1", got)
	}
	if len(seen) != 1 || !seen["Bearer minted-1"] {
		t.Errorf("API saw bearers %v, want only the one minted for this request", seen)
	}
}
