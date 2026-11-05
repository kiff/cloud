package gateway

import (
	"testing"

	"github.com/kiff/cloud/gateway/internal/invocation"
)

// The outcome line's class and status come from the record's codes, never
// its words.
func TestOutcomeClassAndStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		in    invocation.Invocation
		class string
	}{
		"done":               {invocation.Invocation{State: invocation.StateForwarded}, ClassDone},
		"vendor error":       {invocation.Invocation{State: invocation.StateForwarded, ToolError: "Stripe answered 404: No such invoice"}, ClassVendorError},
		"connection refusal": {invocation.Invocation{State: invocation.StateForwarded, ToolError: "Refused by planque", ConnectionRefusal: "halted"}, ClassConnectionRefusal},
		"refused":            {invocation.Invocation{State: invocation.StateRefused, Reasons: []string{"run_grant"}}, ClassRefused},
		"failed":             {invocation.Invocation{State: invocation.StateFailed}, ClassFailed},
		"unknown":            {invocation.Invocation{State: invocation.StateUnknown}, ClassUnknown},
	} {
		if got := outcomeClass(tc.in); got != tc.class {
			t.Errorf("%s: class %q, want %q", name, got, tc.class)
		}
	}
	for text, want := range map[string]string{
		"Stripe answered 404: No such invoice: in_1UYSg1":       "404",
		"planque answered 403: Google Sheets refused the write": "403",
		"The connector refused the operation.":                  "",
		"answered 999 is no status":                             "",
		"call 404 went through; see https://x.test/404":         "",
	} {
		if got := statusOf(text); got != want {
			t.Errorf("statusOf(%q) = %q, want %q", text, got, want)
		}
	}
}
