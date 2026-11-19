package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// heldFor makes a held action of agent://a that asker's run asked for.
func (f *holdFixture) heldFor(asker string) string {
	f.t.Helper()
	f.n++
	d, err := f.auth.Authorize(context.Background(), "t1", "p-"+string(rune('a'+f.n)), mandate.Request{Subject: "agent://a",
		Action: "REFUND_INVOICE", Parameters: map[string]int64{"amount": 500}, AskedBy: asker})
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		f.t.Fatalf("hold: %+v %v", d, err)
	}
	return d.ExceptionID
}

// The person whose run asked for a held call answers it with separation
// of duties off, as an account of one must; with it on, someone else
// approves it. Refusing grants nothing, so they may always refuse.
func TestTheAskerAnswersTheirRunsHoldUnlessDutiesAreSeparated(t *testing.T) {
	f := newHoldFixture(t)
	if code, body := f.answer(person("alice", membership.RoleAdmin), f.heldFor("alice")); code != http.StatusOK {
		t.Fatalf("the only admin approving their own run's hold, SoD off: %d %v", code, body)
	}
	reject := func(id auth.Identity, exc string) (int, map[string]any) {
		rr := do(t, f.mux, id, http.MethodPost, "/v1/me/card-exceptions/"+exc+"/reject", nil)
		var body map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		return rr.Code, body
	}
	if code, body := reject(person("carol", membership.RoleApprover), f.heldFor("carol")); code != http.StatusOK {
		t.Fatalf("refusing their own run's hold, SoD off: %d %v", code, body)
	}

	f.sod(true)
	exc := f.heldFor("carol")
	if code, body := f.answer(person("carol", membership.RoleApprover), exc); code != http.StatusForbidden || body["error"] != "self_review" {
		t.Fatalf("approving their own run's hold, SoD on: %d %v", code, body)
	}
	if code, body := reject(person("carol", membership.RoleApprover), exc); code != http.StatusOK {
		t.Fatalf("refusing their own run's hold, SoD on: %d %v", code, body)
	}
	if code, body := f.answer(person("ann", membership.RoleApprover), f.heldFor("carol")); code != http.StatusOK {
		t.Fatalf("another approver, SoD on: %d %v", code, body)
	}
}
