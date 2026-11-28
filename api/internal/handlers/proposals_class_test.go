package handlers

import (
	"context"
	"testing"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/words"
	kiffproposal "github.com/kiff/kiff/pkg/kiff/proposal"
)

// K4: the gateway's class reaches a K4 account's decision, and only a
// gateway call's; it is taken out of the recorded parameters; a money call
// there always asks its owner; an account off the list is decided as before;
// a call with no class on a K4 account is a write, never a read.
func TestTheCallsClassIsReadForK4AccountsOnly(t *testing.T) {
	prop := kiffproposal.ActionProposal{ID: "p1", ActionName: "pay_q1", Parameters: map[string]any{
		"amount": int64(5), GatewayClassParam: "money", GatewayClassArgsParam: map[string]any{"status": "finalize", "n": 3},
	}}
	c, out := takeClass(domain.Spec{Domain: gatewayDomain}, prop)
	if c.class != "money" || c.args["status"] != "finalize" || len(c.args) != 1 {
		t.Fatalf("taken: %+v", c)
	}
	if _, ok := out.Parameters[GatewayClassParam]; ok || out.Parameters["amount"] != int64(5) || len(prop.Parameters) != 3 {
		t.Fatalf("parameters after: %v, before: %v", out.Parameters, prop.Parameters)
	}
	// Another domain's call: taken out, never read.
	if c, _ := takeClass(domain.Spec{Domain: "billing"}, prop); c.class != "" {
		t.Errorf("another domain's class was read: %+v", c)
	}

	h := &ProposalsHandler{}
	WithProposalsK4Tenants(" t_k4 ", "", "t_other")(h)
	ctx := withClass(context.Background(), c)
	var on mandate.Request
	h.classInto(ctx, "t_k4", &on)
	if !on.K4 || on.Class != "money" || on.ClassArgs["status"] != "finalize" || on.OwnerAsk != words.OwnerAskEachCall || on.OwnerAskWhy == "" {
		t.Errorf("a money call on a K4 account: %+v", on)
	}
	var off mandate.Request
	h.classInto(ctx, "t_plain", &off)
	if off.K4 || off.Class != "" || off.OwnerAsk != "" {
		t.Errorf("an account off the list: %+v", off)
	}
	var none mandate.Request
	h.classInto(context.Background(), "t_k4", &none)
	if !none.K4 || none.Class != "write" || none.OwnerAsk != "" {
		t.Errorf("no class sent: %+v", none)
	}
	// An irreversible call asks too; a delete is refused until opened, and
	// decided by the Card once it is.
	var fin, del mandate.Request
	h.classInto(withClass(context.Background(), callClass{class: "irreversible"}), "t_k4", &fin)
	h.classInto(withClass(context.Background(), callClass{class: "delete"}), "t_k4", &del)
	if fin.OwnerAsk != words.OwnerAskEachCall || del.OwnerAsk != "" {
		t.Errorf("irreversible: %+v, delete: %+v", fin, del)
	}
	// A money call keeps a stronger ask it already has.
	sealed := mandate.Request{OwnerAsk: words.OwnerAskSealedFill}
	h.classInto(ctx, "t_k4", &sealed)
	if sealed.OwnerAsk != words.OwnerAskSealedFill {
		t.Errorf("an earlier ask was replaced: %+v", sealed)
	}
	if (&ProposalsHandler{}).k4["t_k4"] {
		t.Error("no option names no account")
	}
}
