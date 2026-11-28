package mandate_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/mandate"
)

func k4Req(class string, args map[string]string) mandate.Request {
	r := req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 100})
	r.K4, r.Class, r.ClassArgs = true, class, args
	return r
}

// K4: on an account K4 is on for, an irreversible, delete or money call is
// refused until the Card opens its category for the action; a read or a
// write is decided as before; an opening may name one argument's value.
// Category: authority
// Attack: a taf calls a money, delete or send tool its Card grants by name, though the owner never opened that category
// Refused: category_closed, before anything is counted, unless every covering Card opens that class for the action (for the call's value, when the opening names one)
func TestK4RefusesAGatedClassUntilTheCardOpensIt(t *testing.T) {
	m := spendMandate(500000)
	for _, class := range []string{"irreversible", "delete", "money"} {
		d, err := mandate.Evaluate([]mandate.Mandate{m}, k4Req(class, nil), used(0), base)
		if err != nil || d.Outcome != mandate.OutcomeCategoryClosed || d.MandateID != m.ID || !strings.Contains(d.Reason, class) {
			t.Errorf("%s, not opened: %+v %v", class, d, err)
		}
	}
	for _, class := range []string{"read", "write"} {
		if d, _ := mandate.Evaluate([]mandate.Mandate{m}, k4Req(class, nil), used(0), base); !d.Allowed() {
			t.Errorf("%s: %+v", class, d)
		}
	}
	// Off K4, a money call is decided as before.
	off := k4Req("money", nil)
	off.K4 = false
	if d, _ := mandate.Evaluate([]mandate.Mandate{m}, off, used(0), base); !d.Allowed() {
		t.Errorf("off K4: %+v", d)
	}
	// Opened for money: allowed, and still counted against the Card's limits.
	open := spendMandate(500000)
	open.Grants[0].Opens = []mandate.Opening{{Class: "money"}}
	if d, _ := mandate.Evaluate([]mandate.Mandate{open}, k4Req("money", nil), used(0), base); !d.Allowed() {
		t.Errorf("money opened: %+v", d)
	}
	if d, _ := mandate.Evaluate([]mandate.Mandate{open}, k4Req("money", nil), used(499950), base); d.Allowed() {
		t.Errorf("money opened, over the limit: %+v", d)
	}
	// Opening money doesn't open delete or irreversible.
	if d, _ := mandate.Evaluate([]mandate.Mandate{open}, k4Req("delete", nil), used(0), base); d.Outcome != mandate.OutcomeCategoryClosed {
		t.Errorf("delete with money opened: %+v", d)
	}
	// An opening for one value of one argument: that value only.
	fin := spendMandate(500000)
	fin.Grants[0].Opens = []mandate.Opening{{Class: "irreversible", Argument: "status", Value: "finalize"}}
	if d, _ := mandate.Evaluate([]mandate.Mandate{fin}, k4Req("irreversible", map[string]string{"status": "finalize"}), used(0), base); !d.Allowed() {
		t.Errorf("finalize opened: %+v", d)
	}
	for _, args := range []map[string]string{nil, {"status": "cancel"}, {"other": "finalize"}} {
		if d, _ := mandate.Evaluate([]mandate.Mandate{fin}, k4Req("irreversible", args), used(0), base); d.Outcome != mandate.OutcomeCategoryClosed {
			t.Errorf("finalize opened, call %v: %+v", args, d)
		}
	}
	// Two covering Cards: both must open it.
	other := spendMandate(500000)
	other.ID = "mdt-2"
	if d, _ := mandate.Evaluate([]mandate.Mandate{open, other}, k4Req("money", nil), used(0), base); d.Outcome != mandate.OutcomeCategoryClosed || d.MandateID != "mdt-2" {
		t.Errorf("one of two Cards opens money: %+v", d)
	}
}

// Opening a category is a widening, so a window tap can't do it; keeping or
// narrowing one isn't.
func TestOpeningACategoryWidensTheCard(t *testing.T) {
	cur := spendMandate(500000)
	next := spendMandate(500000)
	next.Grants[0].Opens = []mandate.Opening{{Class: "money"}}
	ws := mandate.Widens(&cur, next)
	if len(ws) != 1 || ws[0].Kind != "opens" || !strings.Contains(ws[0].Words, "money") {
		t.Fatalf("opening money: %+v", ws)
	}
	if ws := mandate.Widens(&next, next); len(ws) != 0 {
		t.Errorf("kept: %+v", ws)
	}
	if ws := mandate.Widens(&next, cur); len(ws) != 0 {
		t.Errorf("closed again: %+v", ws)
	}
	narrow := spendMandate(500000)
	narrow.Grants[0].Opens = []mandate.Opening{{Class: "money", Argument: "kind", Value: "refund"}}
	if ws := mandate.Widens(&next, narrow); len(ws) != 0 {
		t.Errorf("narrowed to one value: %+v", ws)
	}
	if ws := mandate.Widens(&narrow, next); len(ws) != 1 {
		t.Errorf("widened from one value to all: %+v", ws)
	}
}

// A Card opens only irreversible, delete or money, each once, with an
// argument and its value together.
func TestACardOpensOnlyAGatedClass(t *testing.T) {
	for name, os := range map[string][]mandate.Opening{
		"read":           {{Class: "read"}},
		"write":          {{Class: "write"}},
		"unknown":        {{Class: "spend"}},
		"argument alone": {{Class: "money", Argument: "kind"}},
		"value alone":    {{Class: "money", Value: "x"}},
		"twice":          {{Class: "money"}, {Class: "money"}},
		"control":        {{Class: "money", Argument: "k\n", Value: "v"}},
	} {
		if err := mandate.ValidateOpensForTest("REFUND_INVOICE", os); !errors.Is(err, mandate.ErrInvalidMandate) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := mandate.ValidateOpensForTest("REFUND_INVOICE", []mandate.Opening{{Class: "money"}, {Class: "irreversible", Argument: "status", Value: "finalize"}}); err != nil {
		t.Errorf("good openings: %v", err)
	}
}
