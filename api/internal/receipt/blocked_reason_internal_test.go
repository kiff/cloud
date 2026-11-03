package receipt

import (
	"testing"

	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
)

// A blocked receipt is for an action the framework refused for its state,
// by the record's typed reason: never by text another failure happens to
// contain, and never for a failure that says nothing.
func TestAStateBlockIsReadFromItsTypedReason(t *testing.T) {
	failed := func(data map[string]any) kiffaudit.Record {
		return kiffaudit.Record{Kind: kiffaudit.KindActionFailed, Message: "action validation failed", Data: data}
	}
	for _, c := range []struct {
		name string
		rec  kiffaudit.Record
		want bool
	}{
		{"typed state refusal", failed(map[string]any{"reason": "state_not_allowed", "error": `action state not allowed: "PAID"`}), true},
		{"another reason, the phrase in its text", failed(map[string]any{"reason": "permission_denied", "error": "executor said: action state not allowed"}), false},
		{"an older framework's text", failed(map[string]any{"error": `action state not allowed: "PAID"`}), true},
		{"no reason, no text", failed(map[string]any{}), false},
		{"another kind", kiffaudit.Record{Kind: kiffaudit.KindActionExecuted, Data: map[string]any{"reason": "state_not_allowed"}}, false},
	} {
		if got := isStateNotAllowedRecord(c.rec); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
