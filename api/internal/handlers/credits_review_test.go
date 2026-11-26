package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/meter"
)

// App Review's and the store test accounts (WithReviewAccounts): on no plan,
// Coach starts for them past the no-plan allowance, so a reviewer can always
// try it. Only Coach: a taf still needs a plan, and any other account with no
// plan is still refused once the allowance is spent.
func TestCoachStartsForAReviewAccountOnNoPlan(t *testing.T) {
	allowance := func(mux *http.ServeMux, r *creditsRig, who string) map[string]any {
		t.Helper()
		id := r.setup()
		if who == "taf" {
			id = r.taf("agt-refunds")
		}
		rr := serve(mux, id, http.MethodGet, "/v1/me/credits/allowance", "", nil)
		var out map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", who, rr.Code, rr.Body.String())
		}
		return out
	}
	review := newCreditsRigOn(t, map[string]any{})
	mux := http.NewServeMux()
	NewCreditsHandler(review.tenants, review.ledger, slog.Default()).WithClock(func() time.Time { return review.now }).
		WithReviewAccounts(" ", review.id).Register(mux)
	// Coach past the $0.54 no-plan allowance, long spent: allowed for a review account.
	review.ledger.noPlanSpent = meter.NoPlanSetupMicroUSD * 10
	if out := allowance(mux, review, "setup"); out["allowed"] != true || out["why"] != nil {
		t.Fatalf("Coach, a review account on no plan past the allowance: %v", out)
	}
	// A taf on no plan: refused, review account or not.
	if out := allowance(mux, review, "taf"); out["allowed"] != false || out["why"] != meter.WhyNoPlan {
		t.Fatalf("a taf, a review account on no plan: %v", out)
	}
	// Any other account on no plan: refused at the allowance, as before.
	other := newCreditsRigOn(t, map[string]any{})
	other.ledger.noPlanSpent = meter.NoPlanSetupMicroUSD
	otherMux := http.NewServeMux()
	NewCreditsHandler(other.tenants, other.ledger, slog.Default()).WithClock(func() time.Time { return other.now }).
		WithReviewAccounts(review.id).Register(otherMux)
	if out := allowance(otherMux, other, "setup"); out["allowed"] != false || out["why"] != meter.WhyNoPlan {
		t.Fatalf("Coach, another account on no plan past the allowance: %v", out)
	}
	// Plan & usage is unchanged for the review account: no plan, Solo offered, no packs.
	rr := serve(mux, review.admin(), http.MethodGet, "/v1/me/usage/credits", "", nil)
	var read struct {
		Balance *balanceView `json:"balance"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &read); err != nil || read.Balance == nil {
		t.Fatalf("balance: %d %s", rr.Code, rr.Body.String())
	}
	if read.Balance.Kind != meter.BalanceNone || read.Balance.CanBuy.Packs {
		t.Fatalf("the review account's balance: %+v", *read.Balance)
	}
}
