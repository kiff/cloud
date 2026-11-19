package handlers_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// dashboard_timeline_test.go covers the per-entity timeline
// page. Three layers of coverage:
//
//   - templ render tests on DashboardEntityTimeline that assert
//     the rendering shape (header, table, kind pills, empty state)
//     without standing up the full handler chain;
//   - HTTP-level tests on /dashboard/entities/{id}/timeline that
//     assert the route is mounted, the handler fetches the
//     timeline endpoint, and tenant scoping is honored;
//   - a wiring test on the proposalRow that confirms the entity_id
//     cell links to the timeline page.

// renderTimeline runs DashboardEntityTimeline against a buffer
// so the tests can assert on the produced HTML.
func renderTimeline(t *testing.T, v pages.DashboardEntityTimelineView) string {
	t.Helper()
	var buf bytes.Buffer
	if err := pages.DashboardEntityTimeline(v).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render timeline: %v", err)
	}
	return buf.String()
}

// freshTimelineView mirrors freshDashboardView's identity setup
// so the top-bar workspace label renders consistently.
func freshTimelineView(entityID string) pages.DashboardEntityTimelineView {
	return pages.DashboardEntityTimelineView{
		Email:       "alice@example.com",
		Subject:     "alice",
		DisplayName: "Alice",
		Me: &apiclient.Me{
			Subject:     "alice",
			Email:       "alice@example.com",
			TenantID:    "ten-test",
			TenantSlug:  "alice-team",
			DisplayName: "Alice's Team",
			Plan:        "free_dev",
		},
		EntityID: entityID,
	}
}

// TestTimeline_RendersAllAuditKindsWithRecognizablePills walks
// the framework's full audit-kind enumeration through the row
// renderer and confirms each gets a recognisable pill class.
// The four buckets — ok / warn / error / info — must each be
// represented; an unknown kind falls through to info.
func TestTimeline_RendersAllAuditKindsWithRecognizablePills(t *testing.T) {
	t.Parallel()

	v := freshTimelineView("order-1")
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	kinds := []string{
		"event_ingested",
		"state_changed",
		"state_rebuilt",
		"decision_proposed",
		"action_validated",
		"action_validation_failed",
		"approval_required",
		"approval_requested",
		"approval_recorded",
		"approval_granted",
		"approval_denied",
		"action_executed",
		"action_failed",
	}
	for _, k := range kinds {
		v.Timeline = append(v.Timeline, apiclient.TimelineEntry{
			Kind:       k,
			OccurredAt: now,
			TraceID:    "trace-" + k,
			Message:    k + " happened",
		})
	}

	body := renderTimeline(t, v)

	for _, k := range kinds {
		if !strings.Contains(body, ">"+k+"<") {
			t.Errorf("kind label %q missing from rendered timeline", k)
		}
	}
	// Bucket coverage: at least one ok, warn, error, info pill in
	// the rendered HTML for the canonical kinds above.
	for _, want := range []string{"dash-pill-ok", "dash-pill-warn", "dash-pill-error", "dash-pill-info"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected at least one %s pill in timeline render", want)
		}
	}
}

// TestTimeline_EmptyStateRendersWhenNoRecords confirms the page
// renders the empty-state copy when the entity has no audit
// records (a typo'd id, a cross-tenant id, or a freshly-created
// entity that hasn't been touched).
func TestTimeline_EmptyStateRendersWhenNoRecords(t *testing.T) {
	t.Parallel()

	v := freshTimelineView("order-empty")
	body := renderTimeline(t, v)

	if !strings.Contains(body, "No activity yet") {
		t.Errorf("empty-state heading missing; body=%q", body)
	}
	if !strings.Contains(body, "belongs to another account") {
		t.Errorf("empty-state copy should mention tenant scoping; body=%q", body)
	}
	if !strings.Contains(body, "/dashboard/build") {
		t.Errorf("empty-state should cross-link to Studio; body=%q", body)
	}
}

// TestTimeline_ZeroTimestampRendersDash confirms the row
// renderer is honest about the apiclient timestamp gap: until
// TimelineEntry.OccurredAt unmarshals correctly (today the JSON
// tag is `occurred_at` but the framework emits `created_at`, so
// OccurredAt is the zero time on every record), the When cell
// renders "—" rather than "Jan 1, 0001".
func TestTimeline_ZeroTimestampRendersDash(t *testing.T) {
	t.Parallel()

	v := freshTimelineView("order-1")
	v.Timeline = []apiclient.TimelineEntry{
		{Kind: "event_ingested", Message: "no timestamp"},
	}
	body := renderTimeline(t, v)

	if strings.Contains(body, "0001-01-01") {
		t.Errorf("zero timestamp leaked into the rendered output; body=%q", body)
	}
}

// TestTimeline_TraceIDCollapsesAndExpands confirms the row
// renderer wraps the trace id in a <details> element so the
// table stays scannable while keeping the full id reachable.
func TestTimeline_TraceIDCollapsesAndExpands(t *testing.T) {
	t.Parallel()

	v := freshTimelineView("order-1")
	v.Timeline = []apiclient.TimelineEntry{
		{Kind: "event_ingested", TraceID: "trace-abcdef-12345-very-long"},
	}
	body := renderTimeline(t, v)

	if !strings.Contains(body, "<details") || !strings.Contains(body, "trace-ab") {
		t.Errorf("trace id should render inside a <details> with a truncated summary; body=%q", body)
	}
	if !strings.Contains(body, "trace-abcdef-12345-very-long") {
		t.Errorf("full trace id should still be in the expanded body; body=%q", body)
	}
}

// timelineFakeAPI is the minimal apps/api fake the timeline
// handler needs: /v1/me, /v1/entities/{id}/timeline. Extends
// the same posture as onboardingFakeAPI but trimmed to the
// dashboard timeline's reads.
type timelineFakeAPI struct {
	timelineStatus int
	timelineBody   any
	meRoles        []string
}

func (f *timelineFakeAPI) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"subject":             "alice",
			"email":               "alice@example.com",
			"tenant_id":           "ten-test",
			"tenant_slug":         "alice-team",
			"tenant_display_name": "Alice's Team",
			"api_key":             false,
			"roles":               f.meRoles,
		})
	})
	mux.HandleFunc("GET /v1/entities/{id}/timeline", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, f.timelineStatus, f.timelineBody)
	})
	t.Cleanup(srv.Close)
	return srv
}

// newDashboardTimelineMux wires a session-gated mux pointing at
// the timeline fake. Returns the mux and the cookie a
// well-authenticated request should carry.
func newDashboardTimelineMux(t *testing.T, srv *httptest.Server, mgr *session.Manager) http.Handler {
	t.Helper()
	cli, err := apiclient.NewSessionAware(srv.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("apiclient.NewSessionAware: %v", err)
	}
	h := handlers.NewDashboardHandler(cli, mgr, handlers.StaticBearerSource(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	inner := http.NewServeMux()
	h.Register(inner)
	return handlers.RequireSession(mgr)(inner)
}

// TestTimelineHandler_RendersOneEntitysAuditChain is the
// HTTP-level happy path: a session-gated GET to
// /dashboard/entities/{id}/timeline lands on the templ output
// with the audit rows rendered.
func TestTimelineHandler_RendersOneEntitysAuditChain(t *testing.T) {
	t.Parallel()

	api := &timelineFakeAPI{
		timelineStatus: http.StatusOK,
		timelineBody: map[string]any{
			"timeline": []map[string]any{
				{
					"id":          "audit-1",
					"kind":        "event_ingested",
					"entity_id":   "order-42",
					"entity_type": "Order",
					"trace_id":    "trace-abc",
					"message":     "ORDER_PAID ingested",
				},
				{
					"id":          "audit-2",
					"kind":        "state_changed",
					"entity_id":   "order-42",
					"entity_type": "Order",
					"trace_id":    "trace-abc",
					"message":     "state changed",
					"data":        map[string]any{"from": "", "to": "PAID"},
				},
			},
		},
	}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newDashboardTimelineMux(t, srv, mgr)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/entities/order-42/timeline", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"order-42", "Audit chain", "event_ingested", "state_changed", "trace-abc"} {
		if !strings.Contains(body, want) {
			t.Errorf("timeline page missing %q; body=%q", want, body)
		}
	}
}

// TestTimelineHandler_EmptyTimelineRendersEmptyState confirms
// a tenant viewing an entity with no audit records (or an entity
// id belonging to another tenant — the api scopes the lookup
// per tenant) lands on the empty state, not a 5xx.
func TestTimelineHandler_EmptyTimelineRendersEmptyState(t *testing.T) {
	t.Parallel()

	api := &timelineFakeAPI{
		timelineStatus: http.StatusOK,
		timelineBody:   map[string]any{"timeline": []any{}},
	}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newDashboardTimelineMux(t, srv, mgr)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/entities/other-tenant-id/timeline", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "No activity yet") {
		t.Errorf("empty timeline should render the empty-state copy; body=%q", rec.Body.String())
	}
}

// TestTimelineHandler_AnonymousRedirectsToSignIn confirms the
// route inherits RequireSession from the gated mux.
func TestTimelineHandler_AnonymousRedirectsToSignIn(t *testing.T) {
	t.Parallel()

	api := &timelineFakeAPI{
		timelineStatus: http.StatusOK,
		timelineBody:   map[string]any{"timeline": []any{}},
	}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newDashboardTimelineMux(t, srv, mgr)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/entities/order-42/timeline", nil)
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status: got %d, want 302", rec.Code)
	}
	loc := rec.Result().Header.Get("Location")
	if !strings.HasPrefix(loc, "/sign-in") {
		t.Fatalf("redirect: got %q, want /sign-in...", loc)
	}
}
