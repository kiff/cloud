package coverage

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/guard"
)

func conn(env, workflow string) guard.Connection {
	return guard.Connection{
		TenantID:    "t1",
		Project:     "cookbook",
		Environment: env,
		AgentID:     "agent-1",
		Workflow:    workflow,
		Adapter:     "langgraph",
		Mode:        "enforce",
	}
}

func obs(tools ...guard.ToolObservation) guard.Observation {
	return guard.Observation{TenantID: "t1", Tools: tools}
}

func tool(name, action string, calls uint64) guard.ToolObservation {
	return guard.ToolObservation{
		Name:              name,
		Action:            action,
		EntityType:        "Invoice",
		EntityArg:         "invoice_id",
		ObservedCallCount: calls,
	}
}

func activeControl(id, env string, mode string, actions ...string) domain.Control {
	return domain.Control{
		ID:          id,
		TenantID:    "t1",
		Domain:      "billing",
		Actions:     actions,
		Environment: env,
		Workflow:    "default",
		Status:      "active",
		Mode:        mode,
		ActivatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
}

func byTool(t *testing.T, rt Runtime, name string) Tool {
	t.Helper()
	for _, tl := range rt.Tools {
		if tl.Tool == name {
			return tl
		}
	}
	t.Fatalf("tool %q not in report", name)
	return Tool{}
}

// The headline number: capability the tenant has not governed.
func TestUngovernedToolsAreCounted(t *testing.T) {
	rt := ForRuntime(
		conn("prod", "default"),
		obs(
			tool("pay_invoice", "PAY_INVOICE", 12),
			tool("issue_refund", "ISSUE_REFUND", 3),
			tool("read_invoice", "READ_INVOICE", 40),
		),
		[]domain.Control{activeControl("ctl-1", "prod", "enforce", "PAY_INVOICE")},
		nil,
	)

	if got, want := rt.Summary.Tools, 3; got != want {
		t.Errorf("tools = %d, want %d", got, want)
	}
	if got, want := rt.Summary.Governed, 1; got != want {
		t.Errorf("governed = %d, want %d", got, want)
	}
	if got, want := rt.Summary.Ungoverned, 2; got != want {
		t.Errorf("ungoverned = %d, want %d", got, want)
	}
	if !byTool(t, rt, "pay_invoice").Governed {
		t.Error("pay_invoice is named by an active control; want governed")
	}
	if byTool(t, rt, "issue_refund").Governed {
		t.Error("issue_refund is governed by no control; want ungoverned")
	}
}

// A control in another environment must not make a tool look covered.
func TestControlInAnotherEnvironmentDoesNotGovern(t *testing.T) {
	rt := ForRuntime(
		conn("prod", "default"),
		obs(tool("pay_invoice", "PAY_INVOICE", 5)),
		[]domain.Control{activeControl("ctl-dev", "dev", "enforce", "PAY_INVOICE")},
		nil,
	)
	if byTool(t, rt, "pay_invoice").Governed {
		t.Error("a dev control must not govern a prod runtime's tool")
	}
	if got, want := rt.Summary.Ungoverned, 1; got != want {
		t.Errorf("ungoverned = %d, want %d", got, want)
	}
}

func TestSupersededControlDoesNotGovern(t *testing.T) {
	superseded := activeControl("ctl-old", "prod", "enforce", "PAY_INVOICE")
	superseded.Status = "superseded"

	rt := ForRuntime(
		conn("prod", "default"),
		obs(tool("pay_invoice", "PAY_INVOICE", 5)),
		[]domain.Control{superseded},
		nil,
	)
	if byTool(t, rt, "pay_invoice").Governed {
		t.Error("a superseded control must not count as coverage")
	}
}

// An observe-mode control records but does not withhold, so the tool is
// governed for evidence and not enforced.
func TestObserveModeCountsAsGovernedButNotEnforced(t *testing.T) {
	rt := ForRuntime(
		conn("prod", "default"),
		obs(tool("pay_invoice", "PAY_INVOICE", 5)),
		[]domain.Control{activeControl("ctl-1", "prod", "observe", "PAY_INVOICE")},
		nil,
	)
	if got, want := rt.Summary.Governed, 1; got != want {
		t.Errorf("governed = %d, want %d", got, want)
	}
	if got, want := rt.Summary.Enforced, 0; got != want {
		t.Errorf("enforced = %d, want %d — observe records, it does not withhold", got, want)
	}
	if got, want := byTool(t, rt, "pay_invoice").Mode, "observe"; got != want {
		t.Errorf("mode = %q, want %q", got, want)
	}
}

// A control whose mode is unset inherits the tenant posture, which this
// package does not resolve. It must not be reported as enforced.
func TestUnsetControlModeIsNotReportedAsEnforced(t *testing.T) {
	rt := ForRuntime(
		conn("prod", "default"),
		obs(tool("pay_invoice", "PAY_INVOICE", 5)),
		[]domain.Control{activeControl("ctl-1", "prod", "", "PAY_INVOICE")},
		nil,
	)
	if got, want := rt.Summary.Governed, 1; got != want {
		t.Errorf("governed = %d, want %d", got, want)
	}
	if got := rt.Summary.Enforced; got != 0 {
		t.Errorf("enforced = %d, want 0 — an unset mode inherits the tenant posture", got)
	}
}

// The most recently activated control is the one a decision would be
// stamped with, so it is the one coverage attributes the tool to.
func TestLatestActivationWinsWhenTwoControlsNameTheAction(t *testing.T) {
	older := activeControl("ctl-old", "prod", "observe", "PAY_INVOICE")
	newer := activeControl("ctl-new", "prod", "enforce", "PAY_INVOICE")
	newer.ActivatedAt = older.ActivatedAt.Add(24 * time.Hour)

	rt := ForRuntime(
		conn("prod", "default"),
		obs(tool("pay_invoice", "PAY_INVOICE", 5)),
		[]domain.Control{newer, older}, // deliberately out of order
		nil,
	)
	got := byTool(t, rt, "pay_invoice")
	if got.ControlID != "ctl-new" {
		t.Errorf("control_id = %q, want ctl-new", got.ControlID)
	}
	if got.Mode != "enforce" {
		t.Errorf("mode = %q, want enforce", got.Mode)
	}
}

// Drift: the tool ran more often than the gate was consulted.
func TestDriftReportsCallsThatNeverReachedTheGate(t *testing.T) {
	counts := func(env, action string) (int, bool) {
		if env == "prod" && action == "PAY_INVOICE" {
			return 8, true
		}
		return 0, true
	}

	rt := ForRuntime(
		conn("prod", "default"),
		obs(tool("pay_invoice", "PAY_INVOICE", 12)),
		[]domain.Control{activeControl("ctl-1", "prod", "enforce", "PAY_INVOICE")},
		counts,
	)

	got := byTool(t, rt, "pay_invoice")
	if got.Decisions == nil || *got.Decisions != 8 {
		t.Fatalf("decisions = %v, want 8", got.Decisions)
	}
	if got.Undecided == nil || *got.Undecided != 4 {
		t.Fatalf("undecided = %v, want 4 (12 calls, 8 decisions)", got.Undecided)
	}
	if rt.Summary.Undecided == nil || *rt.Summary.Undecided != 4 {
		t.Errorf("summary undecided = %v, want 4", rt.Summary.Undecided)
	}
}

// More decisions than observed calls is normal after a runtime restart
// resets the counter. It must not surface as negative drift.
func TestMoreDecisionsThanObservedCallsFloorsAtZero(t *testing.T) {
	counts := func(string, string) (int, bool) { return 50, true }

	rt := ForRuntime(
		conn("prod", "default"),
		obs(tool("pay_invoice", "PAY_INVOICE", 2)),
		[]domain.Control{activeControl("ctl-1", "prod", "enforce", "PAY_INVOICE")},
		counts,
	)
	got := byTool(t, rt, "pay_invoice")
	if got.Undecided == nil || *got.Undecided != 0 {
		t.Errorf("undecided = %v, want 0", got.Undecided)
	}
}

// "Not measured" and "measured zero" must be distinguishable, or a
// dashboard cannot tell a clean runtime from an unmeasured one.
func TestUnmeasuredDriftIsNilNotZero(t *testing.T) {
	rt := ForRuntime(
		conn("prod", "default"),
		obs(tool("pay_invoice", "PAY_INVOICE", 12)),
		[]domain.Control{activeControl("ctl-1", "prod", "enforce", "PAY_INVOICE")},
		nil,
	)
	got := byTool(t, rt, "pay_invoice")
	if got.Decisions != nil || got.Undecided != nil {
		t.Errorf("drift fields = (%v, %v), want both nil when counts is nil", got.Decisions, got.Undecided)
	}
	if rt.Summary.Undecided != nil {
		t.Errorf("summary undecided = %v, want nil", rt.Summary.Undecided)
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, absent := range []string{"decisions", "undecided"} {
		if containsKey(string(b), absent) {
			t.Errorf("unmeasured tool serialized %q; it must be omitted: %s", absent, b)
		}
	}
}

// Drift is only meaningful where a control exists, so an ungoverned
// tool carries no decision count even when counts are supplied.
func TestUngovernedToolsCarryNoDriftFields(t *testing.T) {
	counts := func(string, string) (int, bool) { return 99, true }

	rt := ForRuntime(
		conn("prod", "default"),
		obs(tool("issue_refund", "ISSUE_REFUND", 4)),
		nil,
		counts,
	)
	got := byTool(t, rt, "issue_refund")
	if got.Decisions != nil || got.Undecided != nil {
		t.Error("an ungoverned tool has no decisions to compare against")
	}
}

func TestBuildRanksRuntimesByUngovernedHighRisk(t *testing.T) {
	clean := RuntimeInput{
		Connection:  conn("prod", "clean"),
		Observation: obs(tool("read_invoice", "READ_INVOICE", 3)),
	}
	risky := RuntimeInput{
		Connection:  conn("prod", "risky"),
		Observation: obs(tool("pay_invoice", "PAY_INVOICE", 3), tool("delete_account", "DELETE_ACCOUNT", 1)),
	}

	rep := Build([]RuntimeInput{clean, risky}, nil, nil)
	if len(rep.Runtimes) != 2 {
		t.Fatalf("runtimes = %d, want 2", len(rep.Runtimes))
	}
	if rep.Runtimes[0].Runtime.Workflow != "risky" {
		t.Errorf("first runtime = %q, want the one with ungoverned high-risk tools",
			rep.Runtimes[0].Runtime.Workflow)
	}
	if got, want := rep.Summary.Tools, 3; got != want {
		t.Errorf("tenant tools = %d, want %d", got, want)
	}
	if got, want := rep.Summary.Ungoverned, 3; got != want {
		t.Errorf("tenant ungoverned = %d, want %d", got, want)
	}
	if rep.DriftMeasured {
		t.Error("drift_measured must be false when no counts were supplied")
	}
}

func TestBuildReportsDriftMeasuredWhenCountsSupplied(t *testing.T) {
	rep := Build(
		[]RuntimeInput{{Connection: conn("prod", "default"), Observation: obs(tool("pay_invoice", "PAY_INVOICE", 2))}},
		[]domain.Control{activeControl("ctl-1", "prod", "enforce", "PAY_INVOICE")},
		func(string, string) (int, bool) { return 1, true },
	)
	if !rep.DriftMeasured {
		t.Error("drift_measured must be true when counts were supplied")
	}
}

// An empty environment on either side resolves to the same default, so
// a default-environment tenant is not reported as entirely ungoverned.
func TestEmptyEnvironmentsResolveToTheSameDefault(t *testing.T) {
	c := activeControl("ctl-1", "", "enforce", "PAY_INVOICE")
	rt := ForRuntime(
		conn("", "default"),
		obs(tool("pay_invoice", "PAY_INVOICE", 1)),
		[]domain.Control{c},
		nil,
	)
	if !byTool(t, rt, "pay_invoice").Governed {
		t.Error("empty environments must both resolve to the default and match")
	}
}

func containsKey(s, key string) bool {
	return len(s) > 0 && (indexOf(s, `"`+key+`":`) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
