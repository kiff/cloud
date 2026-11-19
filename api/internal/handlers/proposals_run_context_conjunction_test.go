package handlers_test

import (
	"net/http"
	"testing"
)

// the trusted-run-context design, second condition. `on_untrusted_input` alone does not
// discriminate on an agent whose work begins with untrusted content:
// the prompt-injection lab measured 90 of 90 benign runs tainted, so
// the rule held every benign write. `on_untrusted_sensitive_read`
// fires on the conjunction instead — the exfiltration path forming,
// rather than its first link.
//
// The fixture reuses runContextSetup's domain, extended with actions
// that declare the conjunction.

func rcConjBody(action string, runCtx map[string]any) map[string]any {
	return rcBody(action, runCtx)
}

// The case the whole rule exists for: a tainted run that has NOT read
// anything sensitive proceeds. This is the benign triage run, and
// under `on_untrusted_input` it would have been held.
func TestRunContextConjunction_TaintedWithoutSensitiveReadPasses(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextConjSetup(t)

	rec := drivePost(t, wrapped, "/v1/proposals/decide",
		rcConjBody("COMMENT_CONJ", map[string]any{
			"run_id": "r1", "untrusted_input": true, "sensitive_read": false,
		}))
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)
}

// Both halves established: the path formed, and the action is held.
func TestRunContextConjunction_TaintedWithSensitiveReadHolds(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextConjSetup(t)

	rec := drivePost(t, wrapped, "/v1/proposals/decide",
		rcConjBody("COMMENT_CONJ", map[string]any{
			"run_id": "r1", "untrusted_input": true, "sensitive_read": true,
		}))
	assertOutcome(t, rec, http.StatusOK, "approval_required",
		[]string{"untrusted_sensitive_read"})
}

// A clean run passes whatever it read. The conjunction needs both.
func TestRunContextConjunction_UntaintedPasses(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextConjSetup(t)

	rec := drivePost(t, wrapped, "/v1/proposals/decide",
		rcConjBody("COMMENT_CONJ", map[string]any{
			"run_id": "r1", "untrusted_input": false, "sensitive_read": true,
		}))
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)
}

// The fail-open this rule could have shipped with, and the reason
// RunContext.SensitiveRead is a pointer.
//
// A guard that predates the second fact asserts only untrusted_input.
// If its silence decoded to `sensitive_read: false`, the conjunction
// would be false, and the gate would ALLOW precisely the action the
// rule exists to hold — a control that reads as present and is not.
// An unasserted fact is not a fact that did not happen.
func TestRunContextConjunction_UnassertedSensitiveReadFailsClosed(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextConjSetup(t)

	rec := drivePost(t, wrapped, "/v1/proposals/decide",
		rcConjBody("COMMENT_CONJ", map[string]any{
			"run_id": "r1", "untrusted_input": true, // no sensitive_read key
		}))
	assertOutcome(t, rec, http.StatusOK, "approval_required",
		[]string{"sensitive_read_unasserted"})
}

// No run context at all still fails closed, under the reason that
// says so, exactly as it does for the single-condition rule.
func TestRunContextConjunction_AbsentAssertionFailsClosed(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextConjSetup(t)

	rec := drivePost(t, wrapped, "/v1/proposals/decide", rcConjBody("COMMENT_CONJ", nil))
	assertOutcome(t, rec, http.StatusOK, "approval_required",
		[]string{"run_context_absent"})
}

// A block disposition refuses rather than holds, on the conjunction
// the same as on the broad condition.
func TestRunContextConjunction_BlockDispositionRefuses(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextConjSetup(t)

	rec := drivePost(t, wrapped, "/v1/proposals/decide",
		rcConjBody("COMMENT_CONJ_BLOCK", map[string]any{
			"run_id": "r1", "untrusted_input": true, "sensitive_read": true,
		}))
	assertOutcome(t, rec, http.StatusOK, "blocked",
		[]string{"untrusted_sensitive_read"})
}

// An action may declare both conditions. The specific one governs
// when it fires; the broad one still governs when it does not. The
// gate may only ever make an outcome stricter, so neither can soften
// the other.
func TestRunContextConjunction_BothConditionsEachGovern(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextConjSetup(t)

	// Tainted, nothing sensitive read: the conjunction does not fire,
	// the broad condition does, and it declares approval.
	rec := drivePost(t, wrapped, "/v1/proposals/decide",
		rcConjBody("COMMENT_BOTH", map[string]any{
			"run_id": "r1", "untrusted_input": true, "sensitive_read": false,
		}))
	assertOutcome(t, rec, http.StatusOK, "approval_required",
		[]string{"untrusted_run_context"})

	// Both halves: the conjunction fires and declares block.
	rec2 := drivePost(t, wrapped, "/v1/proposals/decide",
		rcConjBody("COMMENT_BOTH", map[string]any{
			"run_id": "r2", "untrusted_input": true, "sensitive_read": true,
		}))
	assertOutcome(t, rec2, http.StatusOK, "blocked",
		[]string{"untrusted_sensitive_read"})
}

// With both declared and NO assertion at all, the refusal cannot know
// which condition would have fired, so it applies the stricter of the
// two. An absent assertion establishes nothing, including which half
// of the rule is relevant.
func TestRunContextConjunction_AbsentWithBothTakesStricter(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := runContextConjSetup(t)

	rec := drivePost(t, wrapped, "/v1/proposals/decide", rcConjBody("COMMENT_BOTH", nil))
	assertOutcome(t, rec, http.StatusOK, "blocked", []string{"run_context_absent"})
}
