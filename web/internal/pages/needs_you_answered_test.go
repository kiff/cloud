package pages

import (
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// the governance-roles design: a statement line says who answered
// and in which capacity.
func TestAnsweredByLine(t *testing.T) {
	at, on, off := time.Now(), true, false
	for want, e := range map[string]apiclient.CardException{
		"Answered by ana@acme.test, an approver, with separation of duties on.":  {ResolvedAt: &at, ResolvedBy: "ana@acme.test", ResolvedRole: "approver", ResolvedUnderSoD: &on},
		"Answered by ed@acme.test, an editor, with separation of duties off.":    {ResolvedAt: &at, ResolvedBy: "ed@acme.test", ResolvedRole: "editor", ResolvedUnderSoD: &off},
		"Answered by key_01, with an API key, with separation of duties off.":    {ResolvedAt: &at, ResolvedBy: "key_01", ResolvedUnderSoD: &off},
		"Card changed by ed@acme.test, an editor, with separation of duties on.": {ResolvedAt: &at, ResolvedBy: "ed@acme.test", ResolvedRole: "editor", ResolvedUnderSoD: &on, Resolution: "change-card"},
		"Answered by owner.": {ResolvedAt: &at, ResolvedBy: "owner"},
		"":                   {ResolvedBy: "owner"},
	} {
		if got := answeredByLine(e); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
