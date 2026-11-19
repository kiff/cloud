package handlers_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

func TestDashboardControls_NoTrafficShowsPassiveEmptyState(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC)
	srv := startControlsFakeAPI(t, now, "duplicate-payment")
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newDashboardControlsMux(t, srv, mgr)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/govern", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /dashboard/govern: status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Posture reflects the control's mode (enforce), not whether the window
	// happens to hold decisions — so an empty window doesn't flip the page
	// to "Ready to govern". The metric strip + window selector stay visible
	// (you can always widen the window), identity renders, and an empty
	// window shows a compact note rather than the full onboarding preview.
	for _, want := range []string{
		"Enforced before execution.", // mode-led H1, constant across windows
		"Govern",
		"duplicate-payment / PAY_INVOICE",
		"No decisions in this window", // compact empty-window note
		`class="cr-window"`,           // window selector stays visible
		`href="/cards"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("controls page missing %q\n---\n%s", want, body)
		}
	}
	// An empty window must NOT flip the posture to the cold-start state.
	if strings.Contains(body, "Ready to govern.") {
		t.Fatalf("empty window should not flip an enforcing control to 'Ready to govern'\n---\n%s", body)
	}
	// The demo proof-run is demoted off the value surface: no
	// synthetic-traffic buttons, no synthetic proof receipt cards.
	for _, absent := range []string{
		`action="/dashboard/govern/proof"`,
		`action="/dashboard/govern/proof/duplicate"`,
		"Run first attempt",
		"Run the two-step proof.",
		"Attempt 1 — allowed",
		"Operator snapshot",
	} {
		if strings.Contains(body, absent) {
			t.Fatalf("control room still renders demoted proof-run element %q\n---\n%s", absent, body)
		}
	}
}

func TestDashboardControls_RendersControlRoomDecisionEvidence(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC)
	api := &controlsFakeAPI{now: now, workflow: "duplicate-payment", decisionRows: true}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newDashboardControlsMux(t, srv, mgr)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/govern?control_id=ctrl-test-001", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /dashboard/govern: status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		// Govern owns the control: posture-led H1, the headline metric
		// strip (counts live once, here), the control's identity, and the
		// proof-pack export.
		"Enforced before execution.",
		"<span>Held</span><strong>1</strong>",
		"<span>Blocked</span><strong>1</strong>",
		"duplicate-payment / PAY_INVOICE", // control identity
		"Export proof pack",
		// And Govern hands off to Activity for the evidence stream rather
		// than re-rendering the same tables (SRP, the
		// authenticated-surface design/031).
		"View full activity",
		`href="/dashboard/domains/duplicate-payment/evidence"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Govern control surface missing %q\n---\n%s", want, body)
		}
	}
	if strings.Contains(body, "Run the two-step proof.") {
		t.Fatalf("real decision evidence should not lead with proof illustration\n---\n%s", body)
	}
	// The evidence *stream* is Activity's responsibility. Govern must not
	// render the decision rows, receipts, verifiability, alert deliveries,
	// or the approval queue — those are the tables Activity owns. This is
	// the SRP guard: the two surfaces must not duplicate the same tables.
	for _, banned := range []string{
		">Decisions<",                        // decisions section header
		"state_not_allowed",                  // a decision row's reason
		"trace-duplicate-1",                  // a receipt trace id
		"1 receipts · 1 signed · 0 anchored", // receipts aggregate
		"Computed across the 1 receipts shown",
		"signed, not anchored", // verifiability tier
		"Alert deliveries",     // alerts section
		// And the old redundant count encodings stay gone.
		"1 allowed, 1 blocked, 1 held.",
		"Decisions by outcome",
		"PAY_INVOICE enforce evidence.",
	} {
		if strings.Contains(body, banned) {
			t.Fatalf("Govern still renders evidence-stream table %q that now lives on Activity\n---\n%s", banned, body)
		}
	}
}

// A control mints from a whole domain, so it governs every declared
// action. Govern must show all of them (nav count + identity chips), not
// just the first — the old label showed only Actions[0] and hid the rest.
func TestDashboardControls_ShowsAllGovernedActions(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC)
	api := &controlsFakeAPI{now: now, workflow: "prod-guardian", decisionRows: true, multiAction: true}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newDashboardControlsMux(t, srv, mgr)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/govern?control_id=ctrl-test-001", nil)
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"4 governed actions", // honest count label (nav + identity)
		"RESTART_SERVICE",
		"SCALE_OUT",
		"ROLLBACK_DEPLOY",
		"DROP_DATABASE", // every action, not just the first
		// Per-action breakdown card (by_action): each action's own tally.
		"Per governed action",
		"Decisions in this window, by action.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Govern should show all governed actions; missing %q\n---\n%s", want, body)
		}
	}
}

// A remembered window (LastWindow) is restored when Govern is opened with
// no ?window= on the URL — so the window survives a hop to another tab and
// back instead of resetting to the 30-day default.
func TestDashboardControls_RestoresRememberedWindow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC)
	api := &controlsFakeAPI{now: now, workflow: "duplicate-payment", decisionRows: true}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueSessionWithLastWindow(t, mgr, "90d")
	gated := newDashboardControlsMux(t, srv, mgr)

	// No ?window= on the URL — the remembered 90d must be restored.
	req := httptest.NewRequest(http.MethodGet, "/dashboard/govern?control_id=ctrl-test-001", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `aria-current="true">90 days`) {
		t.Fatalf("remembered 90-day window should be restored/active without ?window=\n---\n%s", body)
	}
}

// Govern renders the evidence window selector (7 / 30 / 90 days) and marks
// the requested preset active, wired to the API's existing ?from=&?to=.
func TestDashboardControls_WindowSelector(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC)
	api := &controlsFakeAPI{now: now, workflow: "duplicate-payment", decisionRows: true}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newDashboardControlsMux(t, srv, mgr)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/govern?control_id=ctrl-test-001&window=7d", nil)
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`class="cr-window"`, // the selector
		"7 days",
		"30 days",
		"90 days",
		"cr-window-option-active", // the requested preset is marked active
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Govern window selector missing %q\n---\n%s", want, body)
		}
	}
}

// When scoped via ?domain=, Govern shows a visible, clearable chip so the
// narrowing (all domains → one) is legible and reversible — the silent
// Runtimes→Govern scope drop read as "things disappeared".
func TestDashboardControls_ScopedShowsClearableChip(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC)
	api := &controlsFakeAPI{now: now, workflow: "duplicate-payment", decisionRows: true}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newDashboardControlsMux(t, srv, mgr)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/govern?domain=duplicate-payment", nil)
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Scoped to",
		"duplicate-payment",
		"Show all domains",
		`href="/dashboard/govern"`, // the clear link drops the ?domain= scope
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("scoped Govern missing clearable scope chip element %q\n---\n%s", want, body)
		}
	}
}

func startControlsFakeAPI(t *testing.T, now time.Time, workflow string) *httptest.Server {
	t.Helper()
	api := &controlsFakeAPI{now: now, workflow: workflow}
	return api.start(t)
}

// The decision (pre-execution) receipt label is now asserted on the
// Activity Receipts tab, where the receipts stream lives after the
// Govern/Activity SRP split — see TestDashboardDomainEvidence_LabelsDecisionReceipt
// in dashboard_domains_test.go.

// receiptsSection mirrors the control-scope receipts block: a
// signed receipt tied to the allowed decision, scope=sample.
func (f *controlsFakeAPI) receiptsSection() map[string]any {
	if !f.decisionRows {
		return map[string]any{"status": "unavailable", "samples": []any{}}
	}
	traceID := "trace-duplicate-1"
	kind := "execution"
	if f.decisionReceipt {
		traceID = "decision-prop-allowed"
		kind = "decision"
	}
	return map[string]any{
		"status":         "complete",
		"count":          1,
		"signed_count":   1,
		"anchored_count": 0,
		"truncated":      false,
		"verifiability": map[string]any{
			"signed_algorithm": "secp256k1-keccak256",
			"signed_count":     1,
			"anchored_count":   0,
			"anchored":         "none",
			"note":             "Receipts are signed and locally verifiable; anchoring is not enabled for this window.",
			"scope":            "sample",
		},
		"samples": []map[string]any{{
			"trace_id":    traceID,
			"proposal_id": "prop-allowed",
			"entity_id":   "invoice-001",
			"action_name": "PAY_INVOICE",
			"issued_at":   f.now.Add(-time.Minute).Format(time.RFC3339),
			"signed":      true,
			"anchored":    false,
			"kind":        kind,
		}},
	}
}

// alertsSection mirrors the alert-deliveries block: one blocked
// delivery tied to the blocked decision.
func (f *controlsFakeAPI) alertsSection() map[string]any {
	if !f.decisionRows {
		return map[string]any{"status": "unavailable", "deliveries": []any{}}
	}
	return map[string]any{
		"status": "complete",
		"deliveries": []map[string]any{{
			"proposal_id":  "prop-blocked",
			"target_id":    "ops-webhook",
			"target_type":  "webhook",
			"status":       "blocked",
			"attempted_at": f.now.Format(time.RFC3339),
		}},
	}
}

func (f *controlsFakeAPI) approvalsStatus() string {
	return "unavailable"
}

type controlsFakeAPI struct {
	now      time.Time
	workflow string

	decisionRows bool
	// decisionReceipt makes the receipts section return a decision
	// (pre-execution) receipt sample instead of an execution one.
	decisionReceipt bool
	// multiAction makes the control govern several actions (a control mints
	// from a whole domain), so the nav + identity card can be asserted to
	// show every action, not just the first.
	multiAction bool
}

// actionSet is the actions this fake's control governs: one by default, a
// representative multi-action domain when multiAction is set.
func (f *controlsFakeAPI) actionSet() []string {
	if f.multiAction {
		return []string{"RESTART_SERVICE", "SCALE_OUT", "ROLLBACK_DEPLOY", "DROP_DATABASE"}
	}
	return []string{"PAY_INVOICE"}
}

// byActionSection is the per-action window-complete breakdown the real API
// composes for a multi-action control; nil (omitted) for a single action.
func (f *controlsFakeAPI) byActionSection() []map[string]any {
	if !f.multiAction {
		return nil
	}
	return []map[string]any{
		{"action": "RESTART_SERVICE", "total": 3, "counts": map[string]int{"allowed": 2, "blocked": 1, "approval_required": 0}},
		{"action": "SCALE_OUT", "total": 1, "counts": map[string]int{"allowed": 1}},
		{"action": "ROLLBACK_DEPLOY", "total": 1, "counts": map[string]int{"allowed": 1}},
		{"action": "DROP_DATABASE", "total": 2, "counts": map[string]int{"blocked": 2}},
	}
}

func (f *controlsFakeAPI) start(t *testing.T) *httptest.Server {
	t.Helper()
	if f.now.IsZero() {
		f.now = time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC)
	}
	if f.workflow == "" {
		f.workflow = "duplicate-payment"
	}
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
			"subject":                "alice",
			"email":                  "alice@example.com",
			"tenant_id":              "ten-test",
			"tenant_slug":            "alice-team",
			"tenant_display_name":    "Alice's Team",
			"api_key":                true,
			"roles":                  []string{"tenant_owner", "duplicate-payment.pay_invoice"},
			"plan":                   "free_dev",
			"monthly_proposal_limit": 1000,
		})
	})
	mux.HandleFunc("GET /v1/me/usage", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"tenant_id": "ten-test",
			"from":      f.now.AddDate(0, 0, -2).Format(time.RFC3339),
			"to":        f.now.AddDate(0, 1, -2).Format(time.RFC3339),
			"counters": map[string]uint64{
				"kiff.cloud.proposals.submitted": 7,
			},
		})
	})
	mux.HandleFunc("GET /v1/me/controls", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"controls": []map[string]any{{
				"id":           "ctrl-test-001",
				"domain":       "duplicate-payment",
				"entity":       "Invoice",
				"actions":      f.actionSet(),
				"environment":  "aws",
				"workflow":     f.workflow,
				"activated_at": f.now.Add(-time.Hour).Format(time.RFC3339),
				"activated_by": "alice",
				"status":       "active",
			}},
		})
	})
	mux.HandleFunc("GET /v1/me/controls/ctrl-test-001/decisions", func(w http.ResponseWriter, _ *http.Request) {
		rows := []map[string]any{}
		counts := map[string]int{
			"allowed":           0,
			"blocked":           0,
			"approval_required": 0,
			"limit_exceeded":    0,
			"invalid":           0,
		}
		total := 0
		if f.decisionRows {
			total = 3
			counts["allowed"] = 1
			counts["blocked"] = 1
			counts["approval_required"] = 1
			rows = []map[string]any{
				{
					"proposal_id":      "prop-allowed",
					"entity_id":        "invoice-001",
					"entity_type":      "Invoice",
					"action_name":      "PAY_INVOICE",
					"control_id":       "ctrl-test-001",
					"environment":      "aws",
					"actor_id":         "ap-agent",
					"outcome":          "allowed",
					"created_at":       f.now.Add(-time.Minute).Format(time.RFC3339),
					"receipt_trace_id": "trace-duplicate-1",
					"receipt_status":   "signed",
				},
				{
					"proposal_id":    "prop-blocked",
					"entity_id":      "invoice-001",
					"entity_type":    "Invoice",
					"action_name":    "PAY_INVOICE",
					"control_id":     "ctrl-test-001",
					"environment":    "aws",
					"actor_id":       "ap-agent",
					"outcome":        "blocked",
					"reasons":        []string{"state_not_allowed"},
					"created_at":     f.now.Format(time.RFC3339),
					"receipt_status": "none",
				},
				{
					"proposal_id":    "prop-approval",
					"entity_id":      "invoice-002",
					"entity_type":    "Invoice",
					"action_name":    "PAY_INVOICE",
					"control_id":     "ctrl-test-001",
					"environment":    "aws",
					"actor_id":       "ap-agent",
					"outcome":        "approval_required",
					"reasons":        []string{"approval_required"},
					"created_at":     f.now.Add(-2 * time.Minute).Format(time.RFC3339),
					"receipt_status": "none",
				},
			}
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"control": map[string]any{
				"id":           "ctrl-test-001",
				"domain":       "duplicate-payment",
				"entity":       "Invoice",
				"action":       f.actionSet()[0],
				"actions":      f.actionSet(),
				"environment":  "aws",
				"workflow":     f.workflow,
				"status":       "active",
				"mode":         "enforce",
				"activated_at": f.now.Add(-time.Hour).Format(time.RFC3339),
				"activated_by": "alice",
			},
			"window": map[string]any{
				"from": f.now.AddDate(0, 0, -30).Format(time.RFC3339),
				"to":   f.now.Format(time.RFC3339),
			},
			"decisions": map[string]any{
				"total":     total,
				"counts":    counts,
				"truncated": false,
				"rows":      rows,
			},
			"by_action": f.byActionSection(),
			"receipts":  f.receiptsSection(),
			"alerts":    f.alertsSection(),
			"approvals": map[string]any{"status": f.approvalsStatus()},
		})
	})
	t.Cleanup(srv.Close)
	return srv
}

func newDashboardControlsMux(t *testing.T, srv *httptest.Server, mgr *session.Manager) http.Handler {
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
