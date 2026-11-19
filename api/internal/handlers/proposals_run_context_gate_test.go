package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
)

// the trusted-run-context design: an action can declare that its answer depends on the
// trusted run context, and the gate applies that dependency before the
// mandate draw and only on the path that would otherwise allow.
//
// The fixture is local rather than the shared proposalYAML because it
// needs actions that declare run_context, and the shared one is an
// evidence fixture other tests pin.

const runContextYAML = `
domain: run-context-test
entity: Order
events:
  - ORDER_PLACED
  - ORDER_PAID
states:
  - CREATED
  - PAID
transitions:
  - on: ORDER_PLACED
    from: ""
    to: CREATED
  - on: ORDER_PAID
    from: CREATED
    to: PAID
actions:
  - name: PLAIN_REFUND
    allowed_states: [PAID]
    required_parameters: [amount, reason]
    risk: low
    approval: never
    executor: refund.auto_refund
  - name: HELD_ON_UNTRUSTED
    allowed_states: [PAID]
    required_parameters: [amount, reason]
    risk: low
    approval: never
    executor: refund.auto_refund
    run_context:
      on_untrusted_input: approval
  - name: BLOCKED_ON_UNTRUSTED
    allowed_states: [PAID]
    required_parameters: [amount, reason]
    risk: low
    approval: never
    executor: refund.auto_refund
    run_context:
      on_untrusted_input: block
permissions:
  roles:
    ops_agent:
      - orders.refund
`

// runContextSetup wires a tenant whose domain declares run-context
// dependencies, with the entity already in PAID so every action below
// would otherwise be allowed.
func runContextSetup(t *testing.T) (http.Handler, *cloudruntime.TenantRuntime, string) {
	t.Helper()
	ctx := context.Background()

	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-rc", Status: tenant.StatusActive}
	if err := tenantStore.Create(ctx, tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, []byte(runContextYAML)); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)
	h := handlers.NewProposalsHandler(rt)
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{
		Tenant: tnt.ID, Subject: "subj", Roles: []string{"ops_agent"},
	}))

	resolved, err := rt.For(ctx, tnt.ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, kind := range []string{"ORDER_PLACED", "ORDER_PAID"} {
		raw := kiffadapter.RawInput{
			ID: kind + "-rc", Adapter: "cloud", Type: kind, Source: "test",
			ReceivedAt: time.Now().UTC(), EntityID: "o-rc", EntityType: "Order",
			ActorID: "a", Metadata: kiffevent.Metadata{TraceID: "t-rc"},
			Payload: map[string]any{},
		}
		if _, err := resolved.IngestRaw(ctx, raw); err != nil {
			t.Fatalf("ingest %s: %v", kind, err)
		}
	}
	return wrapped, rt, tnt.ID
}

func rcBody(action string, runCtx map[string]any) map[string]any {
	body := map[string]any{
		"entity_id": "o-rc", "entity_type": "Order", "action_name": action,
		"actor_id":   "agent",
		"parameters": map[string]any{"amount": 100, "reason": "x"},
	}
	if runCtx != nil {
		body["run_context"] = runCtx
	}
	return body
}

// A tainted run holds an action that declares approval.
func TestProposals_RunContextGate_TaintedHoldsForApproval(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextSetup(t)

	rec := drivePost(t, wrapped, "/v1/proposals/decide",
		rcBody("HELD_ON_UNTRUSTED", map[string]any{"run_id": "r1", "untrusted_input": true}))
	assertOutcome(t, rec, http.StatusOK, "approval_required", []string{"untrusted_run_context"})
}

// A tainted run refuses an action that declares block.
func TestProposals_RunContextGate_TaintedBlocks(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextSetup(t)

	rec := drivePost(t, wrapped, "/v1/proposals/decide",
		rcBody("BLOCKED_ON_UNTRUSTED", map[string]any{"run_id": "r1", "untrusted_input": true}))
	assertOutcome(t, rec, http.StatusOK, "blocked", []string{"untrusted_run_context"})
}

// The fail-closed case, and the reason this slice exists. An action that
// declares a run-context dependency and receives NO assertion gets the
// same stricter outcome as a tainted run, under its own reason code.
// Absence is not evidence of a clean run.
func TestProposals_RunContextGate_AbsentAssertionFailsClosed(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextSetup(t)

	held := drivePost(t, wrapped, "/v1/proposals/decide", rcBody("HELD_ON_UNTRUSTED", nil))
	assertOutcome(t, held, http.StatusOK, "approval_required", []string{"run_context_absent"})

	blocked := drivePost(t, wrapped, "/v1/proposals/decide", rcBody("BLOCKED_ON_UNTRUSTED", nil))
	assertOutcome(t, blocked, http.StatusOK, "blocked", []string{"run_context_absent"})
}

// An explicit clean assertion is the only path through the gate. This is
// the other half of the three-state distinction: asserted-clean must be
// distinguishable from asserted-nothing, or the control is unusable.
func TestProposals_RunContextGate_AssertedCleanPasses(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextSetup(t)

	rec := drivePost(t, wrapped, "/v1/proposals/decide",
		rcBody("HELD_ON_UNTRUSTED", map[string]any{"run_id": "r1", "untrusted_input": false}))
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)
}

// An action that declares no dependency is untouched, tainted or not.
// The gate applies to actions that opted in, not to the tenant.
func TestProposals_RunContextGate_UndeclaredActionIsUnaffected(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextSetup(t)

	tainted := drivePost(t, wrapped, "/v1/proposals/decide",
		rcBody("PLAIN_REFUND", map[string]any{"run_id": "r1", "untrusted_input": true}))
	assertOutcome(t, tainted, http.StatusOK, "allowed", nil)

	absent := drivePost(t, wrapped, "/v1/proposals/decide", rcBody("PLAIN_REFUND", nil))
	assertOutcome(t, absent, http.StatusOK, "allowed", nil)
}

// The gate only makes an outcome stricter. A proposal the contract
// already refuses stays refused under its own reason, rather than being
// converted into a run-context hold — the gate is consulted solely on
// the path that would otherwise allow.
func TestProposals_RunContextGate_DoesNotSoftenAnExistingRefusal(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextSetup(t)

	// Unknown entity => no PAID state => blocked on state, in a tainted
	// run, on an action that declares a hold.
	rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
		"entity_id": "o-does-not-exist", "entity_type": "Order",
		"action_name": "HELD_ON_UNTRUSTED", "actor_id": "agent",
		"parameters":  map[string]any{"amount": 100, "reason": "x"},
		"run_context": map[string]any{"run_id": "r1", "untrusted_input": true},
	})
	if body := rec.Body.String(); rec.Code == http.StatusOK &&
		(strings.Contains(body, "untrusted_run_context") || strings.Contains(body, "approval_required")) {
		t.Errorf("a state-refused proposal was converted into a run-context hold: %s", body)
	}
}

// A run-context hold has to be something a human can actually grant.
// The response carries a real approval handle and both reason codes:
// which gate produced the hold, and that approval is what clears it.
// Without the handle this outcome would be a dead end wearing a hold's
// label — the caller told to get approval with nothing to get.
func TestProposals_RunContextGate_HoldIsGrantable(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextSetup(t)

	rec := drivePost(t, wrapped, "/v1/proposals/decide",
		rcBody("HELD_ON_UNTRUSTED", map[string]any{"run_id": "r1", "untrusted_input": true}))
	assertOutcome(t, rec, http.StatusOK, "approval_required",
		[]string{"untrusted_run_context", "approval_required"})

	var body struct {
		Approval *struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			ReviewURL string `json:"review_url"`
		} `json:"approval"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Approval == nil {
		t.Fatal("a run-context hold returned no approval handle: nothing for a human to grant")
	}
	if body.Approval.ID == "" {
		t.Error("approval handle carries no id")
	}
	if body.Approval.Status != "pending" {
		t.Errorf("approval status = %q, want pending", body.Approval.Status)
	}
}

// The absent-assertion hold is grantable too, under its own reason. An
// integrator who is not wiring run context yet still has a path that
// does not require a code change to unblock a single action.
func TestProposals_RunContextGate_AbsentHoldIsGrantable(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextSetup(t)

	rec := drivePost(t, wrapped, "/v1/proposals/decide", rcBody("HELD_ON_UNTRUSTED", nil))
	assertOutcome(t, rec, http.StatusOK, "approval_required",
		[]string{"run_context_absent", "approval_required"})

	if !strings.Contains(rec.Body.String(), `"approval"`) {
		t.Errorf("absent-assertion hold returned no approval handle: %s", rec.Body.String())
	}
}

// specFailsAccessor resolves a runtime happily but cannot produce a
// Spec. Before the spec was resolved beside the runtime, this shape let
// an action that declares a run-context dependency through ungoverned,
// because the gate could not tell whether a rule existed and defaulted
// to "none".
type specFailsAccessor struct {
	inner *cloudruntime.TenantRuntime
}

func (a specFailsAccessor) For(ctx context.Context, tenantID string) (*kiffruntime.Runtime, error) {
	return a.inner.For(ctx, tenantID)
}

func (a specFailsAccessor) ForDomain(ctx context.Context, tenantID, domainID string) (*kiffruntime.Runtime, error) {
	return a.inner.ForDomain(ctx, tenantID, domainID)
}

func (a specFailsAccessor) DomainIDFor(ctx context.Context, tenantID, domainID string) (string, error) {
	return a.inner.DomainIDFor(ctx, tenantID, domainID)
}

func (a specFailsAccessor) SpecForDomain(context.Context, string, string) (domain.Spec, error) {
	return domain.Spec{}, errors.New("spec store unreachable")
}

// A proposal whose spec cannot be read is refused, not allowed. The
// runtime and the spec come from one resolution, so "we could not read
// the rules" is the same answer as "we could not read the runtime" —
// never "there were no rules".
func TestProposals_RunContextGate_UnreadableSpecRefusesRatherThanAllows(t *testing.T) {
	t.Parallel()
	_, rt, tenantID := runContextSetup(t)

	h := handlers.NewProposalsHandler(specFailsAccessor{inner: rt})
	inner := http.NewServeMux()
	h.Register(inner)
	mux := http.NewServeMux()
	mux.Handle("/", withFakeIdentity(inner, auth.Identity{
		Tenant: tenantID, Subject: "subj", Roles: []string{"ops_agent"},
	}))

	// The declaring action, in a run asserted clean: without the fix this
	// sailed through as allowed.
	rec := drivePost(t, mux, "/v1/proposals/decide",
		rcBody("HELD_ON_UNTRUSTED", map[string]any{"run_id": "r1", "untrusted_input": false}))
	if body := rec.Body.String(); strings.Contains(body, `"outcome":"allowed"`) {
		t.Fatalf("an action declaring a run-context dependency was allowed while its rules were unreadable: %s", body)
	}
	// 502, not 400: the caller's request was fine, our side could not
	// answer it. The status tells them to retry rather than to fix their
	// payload.
	assertOutcome(t, rec, http.StatusBadGateway, "invalid", []string{"tenant_runtime_unavailable"})
}

// ---- a review: run context must be part of the request identity ----
//
// The fingerprint feeds both the idempotency dedup and the approval id.
// While run context sat outside it, a caller could take a clean answer
// and replay it under a tainted context: maybeDedupe matched on the
// fingerprint and returned the recorded `allowed` before the gate ran.

// A retry that flips the asserted context from clean to tainted must not
// receive the earlier `allowed`.
func TestProposals_RunContextGate_CleanAnswerIsNotReplayedUnderTaint(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextSetup(t)

	body := rcBody("HELD_ON_UNTRUSTED", map[string]any{"run_id": "r1", "untrusted_input": false})
	body["id"] = "stable-key-clean-then-tainted" // stable id => idempotent
	first := drivePost(t, wrapped, "/v1/proposals/decide", body)
	assertOutcome(t, first, http.StatusOK, "allowed", nil)

	body["run_context"] = map[string]any{"run_id": "r1", "untrusted_input": true}
	second := drivePost(t, wrapped, "/v1/proposals/decide", body)
	if s := second.Body.String(); strings.Contains(s, `"outcome":"allowed"`) {
		t.Fatalf("a clean run's allowed answer was replayed for a tainted run: %s", s)
	}
}

// Same, from no assertion at all to a tainted one.
func TestProposals_RunContextGate_AbsentAnswerIsNotReplayedUnderTaint(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextSetup(t)

	body := rcBody("PLAIN_REFUND", nil) // no rule: this one really is allowed
	body["id"] = "stable-key-absent-then-tainted"
	first := drivePost(t, wrapped, "/v1/proposals/decide", body)
	assertOutcome(t, first, http.StatusOK, "allowed", nil)

	body["run_context"] = map[string]any{"run_id": "r1", "untrusted_input": true}
	second := drivePost(t, wrapped, "/v1/proposals/decide", body)
	if s := second.Body.String(); strings.Contains(s, `"deduplicated":true`) {
		t.Fatalf("an absent-context decision was deduped against a tainted-context request: %s", s)
	}
}

// A retry with the SAME run context still dedupes: binding run context
// into the identity must not break idempotency for genuine retries.
func TestProposals_RunContextGate_SameContextStillDedupes(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextSetup(t)

	body := rcBody("PLAIN_REFUND", map[string]any{"run_id": "r1", "untrusted_input": true})
	body["id"] = "stable-key-same-context"
	first := drivePost(t, wrapped, "/v1/proposals/decide", body)
	assertOutcome(t, first, http.StatusOK, "allowed", nil)

	second := drivePost(t, wrapped, "/v1/proposals/decide", body)
	assertOutcome(t, second, http.StatusOK, "allowed", nil)
	if s := second.Body.String(); !strings.Contains(s, `"deduplicated":true`) {
		t.Errorf("an identical retry in the same run should dedupe: %s", s)
	}
}

// An approval granted for one run must not clear the same action in
// another. The approval id carries the fingerprint, so binding run
// context into the fingerprint is what makes the hold per-run.
func TestProposals_RunContextGate_ApprovalIDIsPerRun(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextSetup(t)

	// A STABLE proposal id, so the approval id's base is identical across
	// both calls and the only thing that can differ is the fingerprint.
	// With a generated id the two ids differ anyway and the test would
	// pass without run context being bound in at all.
	idOf := func(runID string) string {
		body := rcBody("HELD_ON_UNTRUSTED", map[string]any{"run_id": runID, "untrusted_input": true})
		body["id"] = "stable-key-per-run-approval"
		rec := drivePost(t, wrapped, "/v1/proposals/decide", body)
		var decoded struct {
			Approval *struct {
				ID string `json:"id"`
			} `json:"approval"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil || decoded.Approval == nil {
			t.Fatalf("no approval handle for run %s: %s", runID, rec.Body.String())
		}
		return decoded.Approval.ID
	}

	if a, b := idOf("run-a"), idOf("run-b"); a == b {
		t.Errorf("two runs produced the same approval id (%s): a grant in one run would clear the other", a)
	}
}

// The human reviewing a hold has to see why the gate held it. The
// approval's reason is what the dashboard renders, and leaving it as the
// model's own explanation lets a reviewer clear an untrusted-run hold
// without ever being told that is what it is.
func TestProposals_RunContextGate_ApprovalReasonCarriesTheTrustedCause(t *testing.T) {
	t.Parallel()
	wrapped, rt, tenantID := runContextSetup(t)

	body := rcBody("HELD_ON_UNTRUSTED", map[string]any{"run_id": "run-xyz", "untrusted_input": true})
	body["reasoning_summary"] = "routine cleanup, nothing unusual"
	drivePost(t, wrapped, "/v1/proposals/decide", body)

	resolved, err := rt.For(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	approvals, err := resolved.Approvals.List(context.Background(), "o-rc")
	if err != nil || len(approvals) == 0 {
		t.Fatalf("no approval record created: %v", err)
	}
	reason := approvals[0].Reason
	for _, want := range []string{"untrusted_run_context", "run-xyz"} {
		if !strings.Contains(reason, want) {
			t.Errorf("approval reason is missing %q, so a reviewer cannot see why it was held: %q", want, reason)
		}
	}
	if !strings.HasPrefix(reason, "KIFF held this:") {
		t.Errorf("the trusted cause must lead, so a truncating surface keeps it: %q", reason)
	}
}
