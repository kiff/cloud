package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/guard"
	"github.com/kiff/cloud/api/internal/proposals"
)

func coverageFor(t *testing.T, h *CoverageHandler, tenant, rawQuery string) coverageResponse {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(mux)

	path := "/v1/guard/coverage"
	if rawQuery != "" {
		path += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
		Subject: "key-1",
		Tenant:  tenant,
		Method:  auth.MethodAPIKey,
		APIKey:  true,
	}))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("coverage status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp coverageResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode coverage: %v", err)
	}
	return resp
}

func seedCoverageRuntime(t *testing.T, store guard.Store, tenant, env, workflow string, tools ...guard.ToolObservation) {
	t.Helper()
	if _, err := store.SaveObservation(context.Background(), tenant, guard.ObservationInput{
		Project:     "cookbook",
		Environment: env,
		AgentID:     "agent-1",
		Workflow:    workflow,
		Adapter:     "langgraph",
		Mode:        "enforce",
		Tools:       tools,
	}); err != nil {
		t.Fatalf("seed observation: %v", err)
	}
}

func seedControl(t *testing.T, store domain.ControlStore, tenant, env, mode string, actions ...string) {
	t.Helper()
	if err := store.Create(context.Background(), domain.Control{
		ID:          "ctl-" + env,
		TenantID:    tenant,
		Domain:      "billing",
		Entity:      "Invoice",
		Actions:     actions,
		Environment: env,
		Workflow:    "default",
		Status:      "active",
		Mode:        mode,
		ActivatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed control: %v", err)
	}
}

func covTool(name, action string, calls uint64) guard.ToolObservation {
	return guard.ToolObservation{
		Name:              name,
		Action:            action,
		EntityType:        "Invoice",
		EntityArg:         "invoice_id",
		ObservedCallCount: calls,
	}
}

func newCoverageHandler(t *testing.T, opts ...CoverageOption) (*CoverageHandler, guard.Store, domain.ControlStore) {
	t.Helper()
	guards := guard.NewMemoryStore()
	controls := domain.NewMemoryControlStore()
	h := NewCoverageHandler(guards, controls, opts...)
	if h == nil {
		t.Fatal("NewCoverageHandler returned nil with both stores wired")
	}
	return h, guards, controls
}

// The on-ramp case: a runtime connects, no control has been authored,
// and the tenant can already see the size of their ungoverned surface.
func TestCoverage_ReportsUngovernedSurfaceBeforeAnyControlExists(t *testing.T) {
	h, guards, _ := newCoverageHandler(t)
	seedCoverageRuntime(t, guards, "tenant-a", "prod", "default",
		covTool("pay_invoice", "PAY_INVOICE", 12),
		covTool("issue_refund", "ISSUE_REFUND", 4),
	)

	resp := coverageFor(t, h, "tenant-a", "")
	if got, want := resp.Summary.Tools, 2; got != want {
		t.Errorf("tools = %d, want %d", got, want)
	}
	if got, want := resp.Summary.Ungoverned, 2; got != want {
		t.Errorf("ungoverned = %d, want %d", got, want)
	}
	if got := resp.Summary.Governed; got != 0 {
		t.Errorf("governed = %d, want 0 before any control exists", got)
	}
}

func TestCoverage_ActiveControlMovesAToolIntoGoverned(t *testing.T) {
	h, guards, controls := newCoverageHandler(t)
	seedCoverageRuntime(t, guards, "tenant-a", "prod", "default",
		covTool("pay_invoice", "PAY_INVOICE", 12),
		covTool("issue_refund", "ISSUE_REFUND", 4),
	)
	seedControl(t, controls, "tenant-a", "prod", "enforce", "PAY_INVOICE")

	resp := coverageFor(t, h, "tenant-a", "")
	if got, want := resp.Summary.Governed, 1; got != want {
		t.Errorf("governed = %d, want %d", got, want)
	}
	if got, want := resp.Summary.Enforced, 1; got != want {
		t.Errorf("enforced = %d, want %d", got, want)
	}
	if got, want := resp.Summary.Ungoverned, 1; got != want {
		t.Errorf("ungoverned = %d, want %d", got, want)
	}
}

// Tenant isolation: another tenant's runtimes and controls must not
// appear in, or inflate, this tenant's report.
func TestCoverage_IsTenantScoped(t *testing.T) {
	h, guards, controls := newCoverageHandler(t)
	seedCoverageRuntime(t, guards, "tenant-a", "prod", "default", covTool("pay_invoice", "PAY_INVOICE", 1))
	seedCoverageRuntime(t, guards, "tenant-b", "prod", "default", covTool("delete_account", "DELETE_ACCOUNT", 9))
	seedControl(t, controls, "tenant-b", "prod", "enforce", "PAY_INVOICE")

	resp := coverageFor(t, h, "tenant-a", "")
	if got, want := resp.Summary.Tools, 1; got != want {
		t.Fatalf("tools = %d, want %d — tenant-b's runtime leaked", got, want)
	}
	if got := resp.Summary.Governed; got != 0 {
		t.Errorf("governed = %d, want 0 — tenant-b's control must not cover tenant-a", got)
	}
}

// Without a decisions reader the report must say so rather than
// presenting an unmeasured runtime as drift-free.
func TestCoverage_WithoutDecisionsReaderReportsDriftUnmeasured(t *testing.T) {
	h, guards, controls := newCoverageHandler(t)
	seedCoverageRuntime(t, guards, "tenant-a", "prod", "default", covTool("pay_invoice", "PAY_INVOICE", 12))
	seedControl(t, controls, "tenant-a", "prod", "enforce", "PAY_INVOICE")

	resp := coverageFor(t, h, "tenant-a", "")
	if resp.DriftMeasured {
		t.Error("drift_measured must be false with no decisions reader")
	}
	if resp.Summary.Undecided != nil {
		t.Errorf("summary undecided = %v, want nil", resp.Summary.Undecided)
	}
}

// The drift case the incident makes concrete: the tool ran more often
// than the gate was asked.
//
// Invariant: A tool that ran more often than the gate was consulted is
// reported as undecided calls. Bypass is measured against recorded
// decisions rather than assumed absent.
// Category: coverage
// Attack: Call a governed tool directly, bypassing the gate, and assume
// nobody can tell
// Refused: Observed call counts are compared against recorded decisions,
// and the difference is reported as undecided calls
func TestCoverage_ReportsCallsThatNeverReachedTheGate(t *testing.T) {
	reader := proposals.NewMemoryReader()
	for i := 0; i < 8; i++ {
		reader.Append("tenant-a", proposals.DecisionRow{
			ProposalID:  "p",
			ActionName:  "PAY_INVOICE",
			Environment: "prod",
			Outcome:     "allowed",
			CreatedAt:   time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
		})
	}

	h, guards, controls := newCoverageHandler(t, WithCoverageDecisions(reader))
	seedCoverageRuntime(t, guards, "tenant-a", "prod", "default", covTool("pay_invoice", "PAY_INVOICE", 12))
	seedControl(t, controls, "tenant-a", "prod", "enforce", "PAY_INVOICE")

	resp := coverageFor(t, h, "tenant-a", "")
	if !resp.DriftMeasured {
		t.Fatal("drift_measured must be true with a decisions reader")
	}
	if resp.Summary.Undecided == nil {
		t.Fatal("summary undecided is nil; want a measured value")
	}
	if got, want := *resp.Summary.Undecided, uint64(4); got != want {
		t.Errorf("undecided = %d, want %d (12 observed calls, 8 decisions)", got, want)
	}
}

// The caveats are a security claim's fine print. They must travel in
// the payload, not in documentation a dashboard will not read.
func TestCoverage_ResponseCarriesItsLimits(t *testing.T) {
	h, guards, _ := newCoverageHandler(t)
	seedCoverageRuntime(t, guards, "tenant-a", "prod", "default", covTool("pay_invoice", "PAY_INVOICE", 1))

	resp := coverageFor(t, h, "tenant-a", "")
	if len(resp.Limits) < 2 {
		t.Fatalf("limits = %v, want at least the instrumentation and workflow caveats", resp.Limits)
	}
	joined := strings.ToLower(strings.Join(resp.Limits, " "))
	if !strings.Contains(joined, "connected kiff-guard runtime") {
		t.Error("limits must state that only instrumented tools are counted")
	}
	if strings.Contains(joined, "restarts") {
		t.Error("the drift caveat must not appear when drift was not measured")
	}
}

func TestCoverage_DriftCaveatAppearsOnlyWhenDriftMeasured(t *testing.T) {
	h, guards, controls := newCoverageHandler(t, WithCoverageDecisions(proposals.NewMemoryReader()))
	seedCoverageRuntime(t, guards, "tenant-a", "prod", "default", covTool("pay_invoice", "PAY_INVOICE", 1))
	seedControl(t, controls, "tenant-a", "prod", "enforce", "PAY_INVOICE")

	resp := coverageFor(t, h, "tenant-a", "")
	joined := strings.ToLower(strings.Join(resp.Limits, " "))
	if !strings.Contains(joined, "restarts") {
		t.Errorf("limits = %v, want the observed-counter caveat when drift is measured", resp.Limits)
	}
}

// A connected runtime that has never pushed a catalog must not be
// counted as fully covered.
func TestCoverage_ConnectionWithoutObservationIsOmitted(t *testing.T) {
	h, guards, _ := newCoverageHandler(t)
	if _, err := guards.Touch(context.Background(), "tenant-a", guard.TouchInput{
		Project:     "cookbook",
		Environment: "prod",
		AgentID:     "agent-silent",
		Workflow:    "default",
		Adapter:     "langgraph",
	}); err != nil {
		t.Fatalf("touch: %v", err)
	}

	resp := coverageFor(t, h, "tenant-a", "")
	if len(resp.Runtimes) != 0 {
		t.Errorf("runtimes = %d, want 0 — a runtime with no catalog says nothing about coverage", len(resp.Runtimes))
	}
	if resp.Summary.Tools != 0 {
		t.Errorf("tools = %d, want 0", resp.Summary.Tools)
	}
}

func TestCoverage_ScopesToOneRuntimeByQuery(t *testing.T) {
	h, guards, _ := newCoverageHandler(t)
	seedCoverageRuntime(t, guards, "tenant-a", "prod", "default", covTool("pay_invoice", "PAY_INVOICE", 1))
	seedCoverageRuntime(t, guards, "tenant-a", "dev", "default", covTool("delete_account", "DELETE_ACCOUNT", 2))

	resp := coverageFor(t, h, "tenant-a", "environment=dev")
	if len(resp.Runtimes) != 1 {
		t.Fatalf("runtimes = %d, want 1", len(resp.Runtimes))
	}
	if got := resp.Runtimes[0].Runtime.Environment; got != "dev" {
		t.Errorf("environment = %q, want dev", got)
	}
}

// Unauthenticated callers get 401, never a report.
func TestCoverage_RequiresIdentity(t *testing.T) {
	h, _, _ := newCoverageHandler(t)
	mux := http.NewServeMux()
	h.Register(mux)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/guard/coverage", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// Without both stores the route must be absent rather than serving a
// report built from half its inputs.
func TestCoverage_NilWithoutRequiredStores(t *testing.T) {
	if NewCoverageHandler(nil, domain.NewMemoryControlStore()) != nil {
		t.Error("handler must be nil without a guard store")
	}
	if NewCoverageHandler(guard.NewMemoryStore(), nil) != nil {
		t.Error("handler must be nil without a control store")
	}
}
