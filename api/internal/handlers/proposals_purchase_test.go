package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
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

// A purchase's card is money, but its case was the one decision: K4 doesn't
// ask the owner a second time for it. Any other money call still asks.
func TestAPurchasesCardIsNotAskedTwice(t *testing.T) {
	h := &ProposalsHandler{k4: map[string]bool{"t1": true}}
	money := withClass(context.Background(), callClass{class: "money"})
	var plain mandate.Request
	h.classInto(money, "t1", &plain)
	if plain.OwnerAsk == "" {
		t.Fatal("a money call isn't asked")
	}
	var card mandate.Request
	h.classInto(withPurchaseDecided(money), "t1", &card)
	if card.OwnerAsk != "" || card.Class != "money" {
		t.Fatalf("the purchase's card: %+v", card)
	}
}

type fakeHolds struct {
	row      purchase.Row
	decided  *purchase.Decision
	refused  bool
	decideTo error
}

func (f *fakeHolds) ByException(context.Context, string, string) (purchase.Row, error) {
	return f.row, nil
}
func (f *fakeHolds) Decide(_ context.Context, _, _ string, _ int, _ string, d purchase.Decision) error {
	f.decided = &d
	return f.decideTo
}
func (f *fakeHolds) Refuse(context.Context, string, string, int, string) error {
	f.refused = true
	return nil
}

// The owner's answer to a purchase hold decides its case: approve-once with
// the Card in force's day room, reject refuses it, and a room another
// purchase took approves nothing.
func TestTheOwnersAnswerDecidesThePurchase(t *testing.T) {
	ctx := context.Background()
	row := contractRow(t, purchase.StateHeld, time.Time{})
	store := mandate.NewMemoryStore()
	card := spendMandateForPurchase(row.Case.Card.ID)
	if err := store.Issue(ctx, card, "owner"); err != nil {
		t.Fatal(err)
	}
	answer := func(f *fakeHolds, a string) int {
		h := &MandatesHandler{store: store, purchaseHolds: f}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		if h.decidePurchase(rec, req, card.TenantID, "exc_1", a) {
			return rec.Code
		}
		return 0
	}
	f := &fakeHolds{row: row}
	if code := answer(f, "approve-once"); code != 0 || f.decided == nil || f.decided.By != purchase.ByOwner || f.decided.Limits.PerDay != 20000 {
		t.Fatalf("approve: %d %+v", code, f.decided)
	}
	f = &fakeHolds{row: row}
	if code := answer(f, "reject"); code != 0 || !f.refused || f.decided != nil {
		t.Fatalf("reject: %d", code)
	}
	f = &fakeHolds{row: row, decideTo: purchase.ErrRoomTaken}
	if code := answer(f, "approve-once"); code != http.StatusConflict {
		t.Fatalf("room taken: %d", code)
	}
	other := row
	other.Case.Card.ID = "card_gone"
	if code := answer(&fakeHolds{row: other}, "approve-once"); code != http.StatusConflict {
		t.Fatalf("no Card in force: %d", code)
	}
}

func spendMandateForPurchase(id string) mandate.Mandate {
	return mandate.Mandate{ID: id, TenantID: "t_acme", Subject: "agent://agt_supplies", Domain: "gateway", Environment: "prod",
		Grants: []mandate.Grant{{Action: "issue_card", Opens: []mandate.Opening{{Class: "money"}},
			Purchase: &mandate.PurchaseRule{Mode: mandate.PurchaseAskEachTime, Currency: "EUR", PerDay: 20000,
				Categories: []string{"stationery_office_supplies_printing_and_writing_paper"}}}},
		ValidFrom: time.Now().Add(-time.Hour), ValidUntil: time.Now().Add(time.Hour)}
}
