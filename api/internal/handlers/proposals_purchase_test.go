package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/purchase"
)

type oneCase struct {
	row purchase.Row
	err error
}

func (o oneCase) Latest(context.Context, string, string) (purchase.Row, error) { return o.row, o.err }

func contractRow(t *testing.T, state string, until time.Time) purchase.Row {
	t.Helper()
	raw, err := os.ReadFile("../purchase/testdata/purchase.json")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Case struct {
			Value  json.RawMessage `json:"value"`
			Digest string          `json:"digest"`
		} `json:"case"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	var c purchase.Case
	if err := json.Unmarshal(d.Case.Value, &c); err != nil {
		t.Fatal(err)
	}
	return purchase.Row{Case: c, Digest: d.Case.Digest, State: state, LeaseHolder: "exe_1", ValidUntil: until}
}

// issue_card's permit is signed only for the case KIFF approved and is
// executing, with its exact arguments; anything else, or no purchase store,
// is refused before anything is bound.
func TestIssueCardsPermitIsOnlyForTheExecutingCase(t *testing.T) {
	ctx := context.Background()
	until := time.Now().Add(10 * time.Minute)
	row := contractRow(t, purchase.StateExecuting, until)
	args, err := purchase.CardArgs(row, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h := &ProposalsHandler{purchases: oneCase{row: row}}
	if why := h.purchaseCardRefusal(ctx, "t_acme", args); why != "" {
		t.Fatalf("the executing case: %s", why)
	}
	for name, hh := range map[string]*ProposalsHandler{
		"no purchase store":       {},
		"no such case":            {purchases: oneCase{err: errors.New("none")}},
		"approved, not executing": {purchases: oneCase{row: contractRow(t, purchase.StateApproved, until)}},
		"ended":                   {purchases: oneCase{row: contractRow(t, purchase.StateExecuting, time.Now().Add(-time.Second))}},
	} {
		if why := hh.purchaseCardRefusal(ctx, "t_acme", args); why == "" {
			t.Errorf("%s: allowed", name)
		}
	}
	if why := h.purchaseCardRefusal(ctx, "t_acme", json.RawMessage(`{"case":{}}`)); why == "" {
		t.Error("no case id: allowed")
	}
}
