package pages

import (
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// The Changes table shows a per-action change between two rows that
// share a ceiling, and "none" for a revision without one.
func TestRevisionPerAction(t *testing.T) {
	t.Parallel()
	capped := func(max int64) apiclient.MandateRevision {
		return apiclient.MandateRevision{Grants: []apiclient.MandateGrant{{Action: "AUTO_REFUND",
			PerActionMax: []apiclient.MandatePerAction{{Quantity: "sum(amount)", Max: max}}}}}
	}
	for _, tc := range []struct {
		rev  apiclient.MandateRevision
		want string
	}{
		{capped(50000), "AUTO_REFUND ≤ 50000 of sum(amount)"},
		{capped(10000), "AUTO_REFUND ≤ 10000 of sum(amount)"},
		{apiclient.MandateRevision{Grants: []apiclient.MandateGrant{{Action: "AUTO_REFUND"}}}, "none"},
		{apiclient.MandateRevision{}, "none"},
	} {
		if got := revisionPerAction(tc.rev); got != tc.want {
			t.Errorf("revisionPerAction = %q, want %q", got, tc.want)
		}
	}
}
