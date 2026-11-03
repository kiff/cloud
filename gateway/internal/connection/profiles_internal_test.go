package connection

import (
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
)

// A profile's amount or unit read must be the lookup the connection
// actually makes: the same read, path and resource argument. One that
// names another read, another field or other arguments doesn't fit.
func TestAProfilesReadMustBeTheConnectionsLookup(t *testing.T) {
	l := &gateway.UnitLookup{Tool: "get_invoice", Argument: "id", Path: "grand_total"}
	for name, c := range map[string]struct {
		r    *profileRead
		want bool
	}{
		"the same read":          {&profileRead{Tool: "get_invoice", Args: map[string]string{"id": "$args.invoice"}, Path: "grand_total"}, true},
		"a shared argument":      {&profileRead{Tool: "get_invoice", Args: map[string]string{"id": "$args.invoice", "account": "$args.account"}, Path: "grand_total"}, true},
		"another read":           {&profileRead{Tool: "different_read", Args: map[string]string{"id": "$args.invoice"}, Path: "grand_total"}, false},
		"another field":          {&profileRead{Tool: "get_invoice", Args: map[string]string{"id": "$args.invoice"}, Path: "fee"}, false},
		"another resource":       {&profileRead{Tool: "get_invoice", Args: map[string]string{"id": "$args.customer"}, Path: "grand_total"}, false},
		"a fixed argument":       {&profileRead{Tool: "get_invoice", Args: map[string]string{"id": "$args.invoice", "status": "paid"}, Path: "grand_total"}, false},
		"no read in the profile": {nil, false},
	} {
		if got := c.r.sameAs(l, "invoice"); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
	if (&profileRead{Tool: "get_invoice", Args: map[string]string{"id": "$args.invoice"}, Path: "grand_total"}).sameAs(nil, "invoice") {
		t.Error("a read without a lookup on the connection fit")
	}
}
