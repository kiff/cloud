package handlers

import (
	"testing"

	kiffoutcome "github.com/kiff/kiff/pkg/kiff/outcome"
)

// TestOutcomeVocabularyMatchesFramework guards the decide contract's
// stable wire strings against two kinds of drift:
//
//   - the cloud's four governed outcomes are bound to the framework's
//     outcome vocabulary, so a framework bump that renamed one would
//     surface here rather than silently changing the /v1/proposals/decide
//     response;
//   - the literal wire values are pinned, so neither the cloud nor a
//     framework bump can change what integrators actually receive
//     without this test failing.
func TestOutcomeVocabularyMatchesFramework(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		got  outcome
		fw   kiffoutcome.Outcome
		wire string
	}{
		{"allowed", outcomeAllowed, kiffoutcome.Allowed, "allowed"},
		{"approval_required", outcomeApprovalRequired, kiffoutcome.ApprovalRequired, "approval_required"},
		{"blocked", outcomeBlocked, kiffoutcome.Blocked, "blocked"},
		{"invalid", outcomeInvalid, kiffoutcome.Invalid, "invalid"},
	}
	for _, tc := range cases {
		if string(tc.got) != string(tc.fw) {
			t.Errorf("%s: cloud outcome %q != framework outcome %q", tc.name, tc.got, tc.fw)
		}
		if string(tc.got) != tc.wire {
			t.Errorf("%s: wire string drifted: got %q, want %q", tc.name, tc.got, tc.wire)
		}
	}

	// limit_exceeded is cloud-only and must NOT collide with any
	// framework outcome value.
	if string(outcomeLimitExceeded) != "limit_exceeded" {
		t.Errorf("limit_exceeded wire string drifted: got %q", outcomeLimitExceeded)
	}
	for _, fw := range []kiffoutcome.Outcome{
		kiffoutcome.Allowed, kiffoutcome.ApprovalRequired,
		kiffoutcome.Blocked, kiffoutcome.Invalid,
	} {
		if string(outcomeLimitExceeded) == string(fw) {
			t.Errorf("cloud-only limit_exceeded collides with framework outcome %q", fw)
		}
	}
}
