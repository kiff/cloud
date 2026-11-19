package handlers_test

import (
	"encoding/json"
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

// domainsFakeAPI serves the /v1/me + domain-apex read routes the
// dashboard domains surface consumes. hasDomain toggles the
// empty-vs-one-domain shape; observed toggles the observed-agents
// section between "observed" and "unavailable".
type domainsFakeAPI struct {
	hasDomain      bool
	observed       bool
	observedZero   bool
	detailFails    bool
	withApproval   bool
	verifyObserved bool
	manyDecisions  bool // serve a large decision total so the pager engages
	seriesData     bool // serve non-empty requests-over-time buckets for the chart
	// decisionReceipt makes the evidence receipts section return a
	// decision (pre-execution) receipt sample so the Activity
	// Receipts tab can be asserted to label it "pre-execution".
	decisionReceipt bool
	now             time.Time
	// lastModeFlip records the mode the posture-flip endpoint last
	// received, so a test can assert the web handler forwarded it.
	lastModeFlip string
	// secondDomain adds a second, enforcing domain to the list so the
	// multi-domain "All domains" portfolio path can be exercised.
	secondDomain bool
	// draftDomain, when set, is served by GET /v1/me/domain/draft as an
	// in-progress draft so the switcher/portfolio draft-entry paths can
	// be exercised. Empty ⇒ the draft endpoint 404s (no draft).
	draftDomain string
	// liveRuntime, when set, serves a guard connection whose workflow is
	// "duplicate-payment" seen just now, so the portfolio runtime column
	// reads "Live" for that domain.
	liveRuntime bool
}

func (f *domainsFakeAPI) start(t *testing.T) *httptest.Server {
	t.Helper()
	if f.now.IsZero() {
		f.now = time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC)
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"subject": "alice", "email": "alice@example.com",
			"tenant_id": "ten-test", "tenant_slug": "alice-team",
			"tenant_display_name": "Alice's Team", "api_key": true,
		})
	})

	mux.HandleFunc("GET /v1/me/domains", func(w http.ResponseWriter, _ *http.Request) {
		if !f.hasDomain {
			writeJSONForTest(w, http.StatusOK, map[string]any{"domains": []any{}})
			return
		}
		domains := []map[string]any{{
			"name": "duplicate-payment", "entity": "Invoice",
			"version": 1, "status": "active", "action_count": 1,
			"posture":      "observe",
			"activated_at": f.now.Add(-720 * time.Hour).Format(time.RFC3339),
		}}
		if f.secondDomain {
			domains = append(domains, map[string]any{
				"name": "account-access", "entity": "Account",
				"version": 1, "status": "active", "action_count": 3,
				"posture":      "enforce",
				"activated_at": f.now.Add(-240 * time.Hour).Format(time.RFC3339),
			})
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"domains": domains})
	})

	mux.HandleFunc("GET /v1/me/domains/{domain}", func(w http.ResponseWriter, r *http.Request) {
		if !f.hasDomain || r.PathValue("domain") != "duplicate-payment" {
			writeJSONForTest(w, http.StatusNotFound, map[string]any{"error": "domain not found"})
			return
		}
		if f.detailFails {
			writeJSONForTest(w, http.StatusInternalServerError, map[string]any{"error": "boom"})
			return
		}
		agents := map[string]any{"status": "unavailable", "agents": []any{}}
		if f.observed {
			ags := []map[string]any{{
				"id": "key-A", "kind": "api_key", "operations": 12,
				"last_seen": f.now.Add(-time.Hour).Format(time.RFC3339),
			}}
			if f.observedZero {
				ags = []map[string]any{}
			}
			agents = map[string]any{
				"status": "observed",
				"window": map[string]any{
					"from": f.now.Add(-720 * time.Hour).Format(time.RFC3339),
					"to":   f.now.Format(time.RFC3339),
				},
				"agents": ags,
			}
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"name": "duplicate-payment", "entity": "Invoice", "version": 1,
			"status":       "active",
			"posture":      "observe",
			"activated_at": f.now.Add(-720 * time.Hour).Format(time.RFC3339),
			"actions": []map[string]any{{
				"name": "PAY_INVOICE", "risk": "high", "approval": "required",
				"allowed_states": []string{"PENDING"}, "executor": "cloud.proxy",
			}},
			"lifecycle": map[string]any{
				"states":      []string{"PENDING", "PAID"},
				"events":      []string{"pay"},
				"transitions": []map[string]any{{"on": "pay", "from": "PENDING", "to": "PAID"}},
			},
			"agents":   agents,
			"evidence": map[string]any{"control_id": "ctrl-test-001"},
		})
	})

	mux.HandleFunc("GET /v1/me/domains/{domain}/evidence", func(w http.ResponseWriter, r *http.Request) {
		if !f.hasDomain || r.PathValue("domain") != "duplicate-payment" {
			writeJSONForTest(w, http.StatusNotFound, map[string]any{"error": "domain not found"})
			return
		}
		decisionRows := []any{}
		if f.verifyObserved {
			decisionRows = append(decisionRows, map[string]any{
				"proposal_id": "p-1", "entity_id": "invoice-7", "entity_type": "Invoice",
				"action_name": "PAY_INVOICE", "outcome": "allowed",
				"created_at":       f.now.Add(-30 * time.Minute).Format(time.RFC3339),
				"receipt_trace_id": "rcpt-1", "receipt_status": "signed",
			})
		}
		decisionsBlock := map[string]any{
			"total":     3,
			"counts":    map[string]int{"allowed": 1, "blocked": 1, "approval_required": 1},
			"truncated": false,
			"rows":      decisionRows,
		}
		if f.manyDecisions {
			// 45 allowed over a 20-row page => 3 pages, so the pager engages.
			decisionsBlock["total"] = 45
			decisionsBlock["counts"] = map[string]int{"allowed": 45}
			decisionsBlock["truncated"] = true
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"control": map[string]any{
				"id": "ctrl-test-001", "domain": "duplicate-payment",
				"entity": "Invoice", "action": "PAY_INVOICE", "status": "active",
				"mode": "enforce", "activated_at": f.now.Add(-720 * time.Hour).Format(time.RFC3339),
			},
			"window": map[string]any{
				"from": f.now.Add(-720 * time.Hour).Format(time.RFC3339),
				"to":   f.now.Format(time.RFC3339),
			},
			"decisions": decisionsBlock,
			"receipts":  f.receiptsBlock(),
			"alerts":    map[string]any{"status": "unavailable", "deliveries": []any{}},
			"approvals": map[string]any{"status": "unavailable"},
		})
	})

	mux.HandleFunc("GET /v1/me/domains/{domain}/activity/series", func(w http.ResponseWriter, r *http.Request) {
		if !f.hasDomain || r.PathValue("domain") != "duplicate-payment" {
			writeJSONForTest(w, http.StatusNotFound, map[string]any{"error": "domain not found"})
			return
		}
		buckets := []any{}
		if f.seriesData {
			buckets = []any{
				map[string]any{"start": f.now.Add(-2 * time.Minute).Format(time.RFC3339), "counts": map[string]int{"allowed": 2, "blocked": 1}, "total": 3},
				map[string]any{"start": f.now.Add(-1 * time.Minute).Format(time.RFC3339), "counts": map[string]int{"allowed": 1}, "total": 1},
			}
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"domain": "duplicate-payment", "control_id": "ctrl-test-001",
			"from":           f.now.Add(-time.Hour).Format(time.RFC3339),
			"to":             f.now.Format(time.RFC3339),
			"bucket_seconds": 60,
			"buckets":        buckets,
		})
	})

	// Tenant-wide usage carries a deliberately different value so the
	// glance test can prove the domain home reads the per-domain
	// slice, not this account-wide total.
	mux.HandleFunc("GET /v1/me/usage", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"tenant_id": "ten-test", "plan": "free_dev",
			"counters": map[string]uint64{
				"kiff.cloud.proposals.submitted": 99,
				"kiff.cloud.proposals.allowed":   99,
				"kiff.cloud.proposals.blocked":   99,
			},
		})
	})

	mux.HandleFunc("GET /v1/me/domains/{domain}/usage", func(w http.ResponseWriter, r *http.Request) {
		if !f.hasDomain || r.PathValue("domain") != "duplicate-payment" {
			writeJSONForTest(w, http.StatusOK, map[string]any{
				"domain": r.PathValue("domain"), "counters": map[string]uint64{},
			})
			return
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"domain": "duplicate-payment",
			"counters": map[string]uint64{
				"kiff.cloud.proposals.submitted": 3,
				"kiff.cloud.proposals.allowed":   2,
				"kiff.cloud.proposals.blocked":   1,
			},
		})
	})

	mux.HandleFunc("GET /v1/guard/connections", func(w http.ResponseWriter, _ *http.Request) {
		conns := []map[string]any{}
		if f.liveRuntime {
			conns = append(conns, map[string]any{
				"project": "cookbook", "environment": "prod", "agent_id": "pay-agent",
				"workflow": "duplicate-payment", "adapter": "openclaw",
				// Based on real wall-clock (the handler derives freshness
				// from time.Now(), not the fake's fixed clock).
				"last_seen_at": time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339),
				"seen_count":   12,
			})
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"connections": conns})
	})

	mux.HandleFunc("GET /v1/me/domain/draft", func(w http.ResponseWriter, _ *http.Request) {
		if f.draftDomain == "" {
			writeJSONForTest(w, http.StatusNotFound, map[string]any{"error": "no draft"})
			return
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"yaml":       "domain: " + f.draftDomain + "\nentity: Ticket\n",
			"updated_at": f.now.Unix(),
			"parsed": map[string]any{
				"domain": f.draftDomain, "entity": "Ticket",
			},
		})
	})

	mux.HandleFunc("PUT /v1/me/domains/{domain}/mode", func(w http.ResponseWriter, r *http.Request) {
		if !f.hasDomain || r.PathValue("domain") != "duplicate-payment" {
			writeJSONForTest(w, http.StatusNotFound, map[string]any{"error": "domain not found"})
			return
		}
		var body struct {
			Mode string `json:"mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Mode != "observe" && body.Mode != "enforce" {
			writeJSONForTest(w, http.StatusBadRequest, map[string]any{"error": "bad mode"})
			return
		}
		f.lastModeFlip = body.Mode
		writeJSONForTest(w, http.StatusOK, map[string]any{"domain": "duplicate-payment", "mode": body.Mode})
	})

	mux.HandleFunc("GET /v1/approvals", func(w http.ResponseWriter, _ *http.Request) {
		out := []map[string]any{}
		if f.withApproval {
			out = append(out, map[string]any{
				"id": "appr-d1", "entity_id": "invoice-7", "entity_type": "Invoice",
				"action_name": "PAY_INVOICE", "requested_by": "agent-x",
				"reason": "duplicate-payment guard", "status": "pending",
				"created_at": f.now.Add(-time.Hour).Format(time.RFC3339),
			})
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"approvals": out})
	})

	return srv
}

// receiptsBlock returns the evidence receipts section. Default is
// "unavailable" (the domain surface's usual empty shape); when
// decisionReceipt is set it returns a signed decision (pre-execution)
// receipt sample so the Activity Receipts tab can assert the
// "pre-execution" label rendered by the shared ControlRoomReceipts
// partial.
func (f *domainsFakeAPI) receiptsBlock() map[string]any {
	if !f.decisionReceipt {
		return map[string]any{"status": "unavailable", "samples": []any{}}
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
			"trace_id":    "decision-prop-allowed",
			"proposal_id": "prop-allowed",
			"entity_id":   "invoice-001",
			"action_name": "PAY_INVOICE",
			"issued_at":   f.now.Add(-time.Minute).Format(time.RFC3339),
			"signed":      true,
			"anchored":    false,
			"kind":        "decision",
		}},
	}
}

func newDomainsMux(t *testing.T, srv *httptest.Server, mgr *session.Manager) http.Handler {
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

func getDomains(t *testing.T, gated http.Handler, mgr *session.Manager, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200; body=%s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// A decision (pre-execution) receipt is labeled as such on the Activity
// Receipts tab, distinguishing it from an execution receipt. This
// coverage moved here from the Govern page when the receipts stream
// became Activity's responsibility (Govern/Activity SRP split).
func TestDashboardDomainEvidence_LabelsDecisionReceipt(t *testing.T) {
	t.Parallel()

	srv := (&domainsFakeAPI{hasDomain: true, decisionReceipt: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr,
		"/dashboard/domains/duplicate-payment/evidence?tab=receipts")

	if !strings.Contains(body, "pre-execution") {
		t.Fatalf("decision receipt should render a 'pre-execution' label on Activity Receipts\n---\n%s", body)
	}
}

// Activity renders the window selector and threads the selected preset
// through the tab + outcome-tile links so switching them keeps the window.
func TestDashboardDomainEvidence_WindowSelector(t *testing.T) {
	t.Parallel()

	srv := (&domainsFakeAPI{hasDomain: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr,
		"/dashboard/domains/duplicate-payment/evidence?tab=decisions&window=7d")

	for _, want := range []string{
		`class="cr-window"`,
		"7 days",
		"cr-window-option-active",
		// The selected preset is threaded into the tab + outcome-tile links
		// (not just the selector), so switching them keeps the window.
		"evidence?tab=receipts&amp;window=7d",
		"outcome=allowed&amp;tab=decisions&amp;window=7d",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Activity window selector/threading missing %q\n---\n%s", want, body)
		}
	}
	// The default 30-day window must NOT leak ?window= into the tab links
	// (they stay canonical); only the selector itself lists a 30d option.
	def := getDomains(t, newDomainsMux(t, srv, mgr), mgr,
		"/dashboard/domains/duplicate-payment/evidence?tab=decisions")
	if !strings.Contains(def, `href="/dashboard/domains/duplicate-payment/evidence?tab=receipts"`) {
		t.Fatalf("default window should leave tab links clean (no window param)\n---\n%s", def)
	}
}

// The Decisions tab renders the requests-over-time chart as a native
// inline SVG (the web-stack design, no JS) with per-outcome stacked bars
// and a legend when the series carries data.
func TestDashboardDomainEvidence_ActivityChart(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true, seriesData: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr,
		"/dashboard/domains/duplicate-payment/evidence?tab=decisions")

	for _, want := range []string{
		"Requests over time",
		`class="ac-chart"`,  // the SVG
		"ac-bar-allowed",    // a stacked segment
		"ac-bar-blocked",    // the other outcome present in the series
		`class="ac-legend"`, // the legend
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Activity chart missing %q\n---\n%s", want, body)
		}
	}
}

// With no decisions in the window the chart shows an honest empty state
// instead of an empty SVG.
func TestDashboardDomainEvidence_ActivityChartEmpty(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true, seriesData: false}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr,
		"/dashboard/domains/duplicate-payment/evidence?tab=decisions")

	if !strings.Contains(body, "Requests over time") {
		t.Fatalf("chart panel heading should render even when empty\n---\n%s", body)
	}
	if !strings.Contains(body, "No decisions in this window") {
		t.Fatalf("empty series should render the honest empty state\n---\n%s", body)
	}
	if strings.Contains(body, `class="ac-chart"`) {
		t.Fatalf("empty series should not render an SVG\n---\n%s", body)
	}
}

// The extended window selector exposes the short live-view presets
// (5m … 24h) alongside the long evidence windows, so the chart's span
// and resolution are selectable down to a 5-minute view.
func TestDashboardDomainEvidence_ShortWindowPresets(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true, seriesData: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr,
		"/dashboard/domains/duplicate-payment/evidence?tab=decisions&window=5m")

	for _, want := range []string{"5 min", "15 min", "1 hour", "24 hours"} {
		if !strings.Contains(body, want) {
			t.Fatalf("short window preset %q missing from selector\n---\n%s", want, body)
		}
	}
}

// The dashboard shell renders the three-way theme switcher and the
// pre-paint boot script (light | dark | system), so a user can change
// the theme and the choice is applied before paint.
func TestDashboard_RendersThemeSwitcher(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: false}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains")

	for _, want := range []string{
		`class="dash-theme"`,     // the switcher group
		`data-set-theme="light"`, // three options
		`data-set-theme="dark"`,
		`data-set-theme="system"`,
		"__kiffSetTheme",  // the boot script hook
		`data-theme-pref`, // preference stamped on <html>
	} {
		if !strings.Contains(body, want) {
			t.Errorf("theme switcher render missing %q", want)
		}
	}
}

func TestDashboardDomains_EmptyState(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: false}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains")

	for _, want := range []string{"Domains", "No domain yet", "Open the quickstart"} {
		if !strings.Contains(body, want) {
			t.Fatalf("empty domains page missing %q\n---\n%s", want, body)
		}
	}
}

func TestDashboardDomains_OneDomainShowsApexView(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true, observed: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	mux := newDomainsMux(t, srv, mgr)

	// Each tab carries its part of the domain; together they are the
	// whole of what the single page used to show at once.
	for path, wants := range map[string][]string{
		"/dashboard/domains": {
			"<h1>duplicate-payment</h1>",                    // domain name leads
			"What agents can do here",                       // cards per governed action
			"PAY_INVOICE",                                   // the action
			"/dashboard/domains/duplicate-payment/evidence", // evidence reached from the domain (D2)
			`href="/dashboard/domains/duplicate-payment?tab=contract"`,
		},
		"/dashboard/domains?tab=contract": {
			"Governance",              // the projection framing (slice 3)
			"read from your contract", // code is the record
			"Governed actions",        // actions section
			"allowed in: PENDING",     // allowed states rendered
			"Lifecycle",               // lifecycle section
			"On pay: PENDING → PAID",  // transition rendered
		},
		"/dashboard/domains?tab=settings":    {"governing since"},         // continuity seed (retention)
		"/dashboard/domains?tab=integration": {"key-A", "Agents seen in"}, // observed agents, honestly framed
	} {
		body := getDomains(t, mux, mgr, path)
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Fatalf("%s missing %q\n---\n%s", path, want, body)
			}
		}
	}
}

// The domain home renders the per-domain governance posture control:
// the current mode as a state chip, and a flip to the other mode posting
// to the domain's /mode endpoint.
func TestDashboardDomains_RendersPostureCard(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains?tab=settings")

	for _, want := range []string{
		"Governance posture", // the posture card
		"Observe · current",  // current mode (fake serves observe) as a chip
		"Switch to enforce",  // one-click flip to the other mode
		`action="/dashboard/domains/duplicate-payment/mode"`, // flip form target
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("posture card missing %q\n---\n%s", want, body)
		}
	}
}

// Posting a posture flip forwards the requested mode to the api and
// redirects back to the domain home.
func TestDashboardDomains_PostureFlipForwardsMode(t *testing.T) {
	t.Parallel()
	f := &domainsFakeAPI{hasDomain: true}
	srv := f.start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newDomainsMux(t, srv, mgr)

	req := httptest.NewRequest(http.MethodPost, "/dashboard/domains/duplicate-payment/mode",
		strings.NewReader("mode=enforce"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("flip status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/domains/duplicate-payment" {
		t.Errorf("redirect = %q, want /dashboard/domains/duplicate-payment", loc)
	}
	if f.lastModeFlip != "enforce" {
		t.Errorf("api received mode %q, want enforce", f.lastModeFlip)
	}
}

// A multi-domain tenant gets the "All domains" operational portfolio:
// one row per domain with its posture state, and a per-domain next
// action driven by whether it has recent activity.
func TestDashboardDomains_PortfolioRendersOperationalRollup(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true, secondDomain: true, withApproval: true, liveRuntime: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains")

	for _, want := range []string{
		"dash-portfolio", // the portfolio table
		`href="/dashboard/domains/duplicate-payment"`, // row link (has activity)
		`href="/dashboard/domains/account-access"`,    // row link (second domain)
		"Observe",              // duplicate-payment posture state
		"Enforcing",            // account-access posture state
		"Live",                 // duplicate-payment runtime freshness (workflow match)
		"Review 1 approval",    // per-domain approval (Invoice → duplicate-payment)
		"Connect a runtime",    // next action for the unexercised second domain
		"waiting for a person", // workspace approvals callout (total)
		// The callout deep-links to the domain holding the approvals, not
		// /dashboard/activity (which bounces back to this portfolio).
		`href="/dashboard/domains/duplicate-payment/evidence?tab=approvals"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("portfolio missing %q\n---\n%s", want, body)
		}
	}
	// A multi-domain view must not render a single domain's inline apex
	// detail (the verify section) — it's the portfolio, not one domain.
	if strings.Contains(body, "Actions against real traffic") {
		t.Error("portfolio view should not render a single-domain apex detail")
	}
}

// An in-progress draft appears as a first-class "Draft" row in the
// portfolio, linking to Build (its setup context) with a "Finish domain"
// next action — while the active domains keep their operating rows.
func TestDashboardDomains_PortfolioShowsDraftRow(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true, secondDomain: true, draftDomain: "support-ops"}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains")

	for _, want := range []string{
		"support-ops",             // the draft's name
		"Draft",                   // its state
		"Finish domain",           // its next action
		`href="/dashboard/build"`, // links to Build, not a domain home
		`href="/dashboard/domains/account-access"`, // active rows still present
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("portfolio draft row missing %q\n---\n%s", want, body)
		}
	}
}

// A draft whose domain is already active is an edit-in-progress, not a
// new domain — it must NOT be double-counted as a separate Draft row.
func TestDashboardDomains_PortfolioSkipsDraftOfActiveDomain(t *testing.T) {
	t.Parallel()
	// draft domain == an active domain (account-access).
	srv := (&domainsFakeAPI{hasDomain: true, secondDomain: true, draftDomain: "account-access"}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains")

	if strings.Contains(body, "Finish domain") {
		t.Error("a draft that shadows an active domain must not add a Draft row")
	}
}

func TestDashboardDomains_VerifyLoopUnexercised(t *testing.T) {
	t.Parallel()
	// A client-run (cloud.proxy) action with no observed decisions: the
	// loop is not yet closed, and the surface must say so honestly rather
	// than implying the side effect is wired.
	srv := (&domainsFakeAPI{hasDomain: true, verifyObserved: false}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains?tab=integration")

	for _, want := range []string{
		"Actions against real traffic",      // the verify section
		"Runs in your code",                 // cloud.proxy backing kind
		"Not yet connected",                 // unexercised client-run verdict
		"asked KIFF about this action",      // honest unobserved detail
		"not that the side effect happened", // the load-bearing boundary note
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("verify section (unexercised) missing %q\n---\n%s", want, body)
		}
	}
}

func TestDashboardDomains_VerifyLoopConnected(t *testing.T) {
	t.Parallel()
	// Same client-run action, now with an observed decision carrying a
	// receipt: the gate has been exercised, so the loop reads as closed.
	srv := (&domainsFakeAPI{hasDomain: true, verifyObserved: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains?tab=integration")

	for _, want := range []string{
		"Actions against real traffic",
		"Connected",         // exercised client-run verdict
		"receipts recorded", // receipt presence surfaced
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("verify section (connected) missing %q\n---\n%s", want, body)
		}
	}
	if strings.Contains(body, "Not yet connected") {
		t.Fatalf("connected loop must not render 'Not yet connected'\n---\n%s", body)
	}
}

func TestDashboardDomains_AgentsUnavailableIsHonest(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true, observed: false}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains?tab=integration")

	if !strings.Contains(body, "Agent activity couldn") {
		t.Fatalf("unavailable agents section should be honest about it\n---\n%s", body)
	}
	if strings.Contains(body, "key-A") {
		t.Fatalf("no agents should render when status is unavailable\n---\n%s", body)
	}
}

func TestDashboardDomains_DetailRouteUnknownSlugFallsBack(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	// A slug the tenant doesn't own → api 404 → ErrNoDomain → empty surface.
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains/other-domain")
	if !strings.Contains(body, "No domain yet") {
		t.Fatalf("unknown slug should fall back to the empty surface\n---\n%s", body)
	}
}

// observed status with an empty agent list is a distinct path: the
// window-basis copy still renders, with the "no agents yet" note.
func TestDashboardDomains_ObservedButZeroAgents(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true, observed: true, observedZero: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains?tab=integration")

	if !strings.Contains(body, "No agents have called this domain in this window") {
		t.Fatalf("observed-but-empty should render the no-agents-yet note\n---\n%s", body)
	}
	if strings.Contains(body, "Agent activity couldn") {
		t.Fatalf("observed-but-empty must not render the unavailable copy\n---\n%s", body)
	}
}

// When the list shows one domain but the inline detail load fails,
// the page degrades to the list card rather than erroring.
func TestDashboardDomains_DetailLoadFailureFallsBackToCard(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true, detailFails: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains")

	// Falls back to the list card (a link to the detail route), not a 500.
	if !strings.Contains(body, `href="/dashboard/domains/duplicate-payment"`) {
		t.Fatalf("detail-load failure should fall back to the list card\n---\n%s", body)
	}
	// And does not render the inline apex sections.
	if strings.Contains(body, "Governed actions") {
		t.Fatalf("inline detail should not render when the detail load failed\n---\n%s", body)
	}
}

// The domain evidence view renders the Control Room scoped to the
// domain and reached from it: the decision headline and the evidence
// sections, framed by the domain with a back link.
func TestDashboardDomainEvidence_RendersScopedView(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains/duplicate-payment/evidence")

	for _, want := range []string{
		"· Evidence",        // domain-first framing (eyebrow)
		"<h1>Activity</h1>", // clean H1, NOT the count sentence
		`<strong class="cr-tile-count">1</strong>`, // counts live in the tiles
		"authorized", // tile sublabel (Domain pattern)
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("domain evidence view missing %q\n---\n%s", want, body)
		}
	}
	// The counts must not be repeated as a headline sentence above the tiles.
	if strings.Contains(body, "1 allowed, 1 blocked, 1 held.") {
		t.Fatalf("Activity H1 still repeats the counts\n---\n%s", body)
	}
}

// An unknown domain (api 404 → ErrNoDomain) renders the honest
// no-evidence-yet empty state, not a hard error.
func TestDashboardDomainEvidence_UnknownDomainEmptyState(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains/other-domain/evidence")

	if !strings.Contains(body, "No evidence yet") {
		t.Fatalf("unknown domain evidence should render the empty state\n---\n%s", body)
	}
}

// The evidence view carries the guarantee-legibility panel: the two
// kernel guarantees stated honestly. Authority must be
// "server-enforced" and the copy must NOT claim global unbypassability
// or that the client process inherits the kernel's compile-time
// boundary (/ kiff/kiff#26).
func TestDashboardDomainEvidence_GuaranteeLegibility(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	// The Activity page is now tabbed (viewport-first); the guarantee lives
	// on the "Guarantee & identity" tab for a populated domain (still one
	// click, still legible) — and on the empty-evidence path it renders
	// inline (see TestDashboardDomainEvidence_GuaranteeShownWhenEmpty).
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains/duplicate-payment/evidence?tab=about")

	for _, want := range []string{
		"How this is guaranteed",
		"Decided against KIFF",
		"Authority is enforced on the server",
		"server-side",
		"without relying on your own process",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("guarantee panel missing %q\n---\n%s", want, body)
		}
	}
	// Honesty constraint: no overclaim.
	for _, banned := range []string{"unbypassable", "cannot be bypassed", "impossible to bypass"} {
		if strings.Contains(strings.ToLower(body), banned) {
			t.Errorf("guarantee copy must not claim %q (honesty)\n---\n%s", banned, body)
		}
	}
}

// The guarantee panel is a property statement, so it renders even before
// any decisions exist (empty-evidence path).
func TestDashboardDomainEvidence_GuaranteeShownWhenEmpty(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	// other-domain → ErrNoDomain → empty evidence state, but Me is set.
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains/other-domain/evidence")
	if !strings.Contains(body, "How this is guaranteed") {
		t.Errorf("guarantee panel should render even on the empty-evidence path\n---\n%s", body)
	}
}

// The domain apex view exposes edit affordances into the Build lens
// TestDashboardDomains_SectionEditAffordances: after slice 5 (the
// authenticated-surface design/A1) the apex routes editing to the
// codebase. It carries one honest "Open Build" entry to the (now
// review-first) Build surface and the agents "Connect an agent" link —
// and no longer invites per-section in-app contract editing ("Edit
// actions" / "Edit lifecycle"), which would contradict the governance
// note's "your codebase is the record."
func TestDashboardDomains_SectionEditAffordances(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true, observed: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains?tab=settings")

	for _, want := range []string{
		"Open Build", // single honest entry to the Build surface
		`href="/dashboard/build"`,
		`class="dash-domain-detail"`, // grouped, consistent-spacing container
	} {
		if !strings.Contains(body, want) {
			t.Errorf("domain apex missing affordance / layout %q\n---\n%s", want, body)
		}
	}
	// The per-section in-app contract edit buttons are gone — editing the
	// contract is a codebase action now.
	for _, absent := range []string{"Edit actions", "Edit lifecycle", "Edit in Studio"} {
		if strings.Contains(body, absent) {
			t.Errorf("apex still renders retired per-section edit affordance %q", absent)
		}
	}
}

// TestDashboardDomains_GlanceUsesPerDomainUsage pins the review fix:
// the domain home's activity glance must read the per-domain usage
// slice (GET /v1/me/domains/{domain}/usage), not the tenant-wide
// /v1/me/usage. The fake serves 3/2/1 per-domain and 99
// account-wide, so the glance showing 3 (and never 99) proves the
// counts are scoped to the domain under whose header they render.
func TestDashboardDomains_GlanceUsesPerDomainUsage(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr, "/dashboard/domains")

	// Per-domain counts render in the glance.
	for _, want := range []string{
		`<span>Submitted</span><strong>3</strong>`,
		`<span>Allowed</span><strong>2</strong>`,
		`<span>Blocked</span><strong>1</strong>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("glance missing per-domain metric %q", want)
		}
	}
	// The tenant-wide totals must never surface under the domain header.
	if strings.Contains(body, `<strong>99</strong>`) {
		t.Error("glance rendered tenant-wide usage (99) instead of the per-domain slice")
	}
}

// TestDashboardDomainEvidence_RendersPendingApprovalQueue pins the
// authenticated-surface design fold: the actionable approval queue —
// grant/deny forms and the request detail — renders inside the
// domain's Activity (evidence) view, sourced from the tenant's
// pending approvals.
func TestDashboardDomainEvidence_RendersPendingApprovalQueue(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true, withApproval: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	// The actionable approval queue lives on the Activity "Approvals" tab
	// (the tab carries a count badge on every view so waiting work is
	// visible without hunting).
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr,
		"/dashboard/domains/duplicate-payment/evidence?tab=approvals")

	for _, want := range []string{
		"1 pending approval",
		"PAY_INVOICE",
		"invoice-7",
		"agent-x",
		"duplicate-payment guard",
		`/dashboard/approvals/appr-d1/grant`,
		`/dashboard/approvals/appr-d1/deny`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("domain Activity view missing approval-queue %q", want)
		}
	}
}

// TestDashboardDomainEvidence_OutcomeTilesFilter pins the clickable
// outcome summary: the count tiles are themselves the decisions filter
// (a link per outcome into ?tab=decisions&outcome=), and the tile
// matching the active ?outcome= is marked. This is the single home for
// the counts — no separate chip bar, no duplicate per-panel count row.
func TestDashboardDomainEvidence_OutcomeTilesFilter(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr,
		"/dashboard/domains/duplicate-payment/evidence?tab=decisions&outcome=blocked")

	for _, want := range []string{
		`class="cr-tiles"`, // the tile group
		`href="/dashboard/domains/duplicate-payment/evidence?outcome=allowed&amp;tab=decisions"`, // Allowed tile is a filter link
		`cr-tile-active`,      // the active (Blocked) tile
		`aria-current="true"`, // …marked current
	} {
		if !strings.Contains(body, want) {
			t.Errorf("outcome tiles filter missing %q\n---\n%s", want, body)
		}
	}
	// The duplicate count surfaces are gone: no separate chip bar, and the
	// decisions panel no longer repeats its own "Decisions by outcome"
	// count row headline (the tiles are the counts).
	if strings.Contains(body, `class="cr-filterbar"`) {
		t.Error("stale chip bar still present; the tiles are the filter now")
	}
}

// TestDashboardDomainEvidence_DecisionsPager pins the slice-2 pagination:
// with more decisions than one page, page 1 offers an "Older" next link,
// and page 2 offers both directions with the right page label.
func TestDashboardDomainEvidence_DecisionsPager(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true, manyDecisions: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	mux := newDomainsMux(t, srv, mgr)

	page1 := getDomains(t, mux, mgr, "/dashboard/domains/duplicate-payment/evidence?tab=decisions")
	if !strings.Contains(page1, `class="cr-pager"`) {
		t.Errorf("page 1 missing pager")
	}
	if !strings.Contains(page1, "Page 1 of 3") {
		t.Errorf("page 1 missing 'Page 1 of 3' label")
	}
	if !strings.Contains(page1, `evidence?page=2&amp;tab=decisions`) {
		t.Errorf("page 1 missing Older→ link to page 2")
	}

	page2 := getDomains(t, mux, mgr, "/dashboard/domains/duplicate-payment/evidence?tab=decisions&page=2")
	if !strings.Contains(page2, "Page 2 of 3") {
		t.Errorf("page 2 missing 'Page 2 of 3' label")
	}
	if !strings.Contains(page2, `evidence?tab=decisions`) { // ←Newer back to page 1 (page omitted)
		t.Errorf("page 2 missing Newer← link to page 1")
	}
	if !strings.Contains(page2, `evidence?page=3&amp;tab=decisions`) {
		t.Errorf("page 2 missing Older→ link to page 3")
	}
}

// TestDashboardDomainEvidence_NoApprovalsHidesQueue confirms the
// actionable queue is absent when there are no pending approvals (the
// read-only approval evidence section still renders below).
func TestDashboardDomainEvidence_NoApprovalsHidesQueue(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true, withApproval: false}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getDomains(t, newDomainsMux(t, srv, mgr), mgr,
		"/dashboard/domains/duplicate-payment/evidence")

	if strings.Contains(body, "pending approval") {
		t.Error("domain Activity view rendered the actionable queue with no pending approvals")
	}
}
