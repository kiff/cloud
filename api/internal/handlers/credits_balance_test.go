package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
)

func (r *creditsRig) balance(who auth.Identity, path string) balanceView {
	r.t.Helper()
	rr := serve(r.mux, who, http.MethodGet, path, "", nil)
	if rr.Code != http.StatusOK {
		r.t.Fatalf("GET %s: %d %s", path, rr.Code, rr.Body.String())
	}
	var out struct {
		Balance *balanceView `json:"balance"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out.Balance == nil {
		r.t.Fatalf("no balance: %v %s", err, rr.Body.String())
	}
	return *out.Balance
}

func (r *creditsRig) member() auth.Identity {
	return auth.Identity{Subject: "bob", Tenant: r.id, MemberRole: membership.RoleEditor, Roles: membership.IdentityRoles(membership.RoleEditor)}
}

func meterOf(v balanceView) int {
	if v.MeterPct == nil {
		return -1
	}
	return *v.MeterPct
}

// The credits read carries the balance now, whichever period it reads:
// Solo's included credits and packs, with what may be bought; Company's
// included credits and spend limit, with the raise for whoever manages
// billing; no limit on an agreement or internal; and no plan waits.
func TestTheBalanceInTheCreditsRead(t *testing.T) {
	month := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	t.Run("solo", func(t *testing.T) {
		r := newCreditsRig(t)
		r.ledger.totals = meter.RunTotals{Credits: meter.SoloIncludedCredits + 30, Over: 10, Free: map[string]int{}}
		if _, _, err := r.packs.RecordCredits(context.Background(), packs.CreditGrant{TenantID: r.id, Pack: "c-small", Credits: 100,
			Source: packs.SourceApple, SourceTxn: "t1", At: month.AddDate(0, -1, 0)}); err != nil {
			t.Fatal(err)
		}
		r.packs.DrawCredits(r.id, 20, month.AddDate(0, 0, 3))
		// The included credits and 20 drawn, of the included credits and
		// the pack's 100: past 80%. The 10 past them weren't charged.
		v := r.balance(r.admin(), "/v1/me/usage/credits?period=previous")
		if v.Kind != meter.BalanceSolo || v.State != meter.StateWarning || v.Included != meter.SoloIncludedCredits || v.PacksLeft != 80 || v.Over != 10 ||
			*v.Left != 80 || meterOf(v) != (meter.SoloIncludedCredits+20)*100/(meter.SoloIncludedCredits+100) || v.BilledBy != "apple" {
			t.Fatalf("solo: %+v", v)
		}
		if !v.Period.Start.Equal(month) {
			t.Fatalf("the balance is now, whatever period is read: %+v", v.Period)
		}
		// Packs of credits are offered to whoever manages billing, and the
		// raise is Company's.
		if !v.CanBuy.Packs || v.CanBuy.Solo || v.CanRaise {
			t.Fatalf("an admin on Solo: %+v %v", v.CanBuy, v.CanRaise)
		}
		if v := r.balance(r.member(), "/v1/me/usage/credits"); v.CanBuy.Packs || v.CanRaise {
			t.Fatalf("a member on Solo: %+v %v", v.CanBuy, v.CanRaise)
		}
		// All of it used: the tafs wait.
		r.ledger.totals.Credits, r.ledger.totals.Over = meter.SoloIncludedCredits+100, 0
		if v := r.balance(r.admin(), "/v1/me/usage/credits"); v.State != meter.StateWaiting || v.Why != meter.WhyUsedUp {
			t.Fatalf("used up: %+v", v)
		}
	})
	t.Run("company", func(t *testing.T) {
		r := newCreditsRigOn(t, map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeySeats: float64(4), tenant.MetadataKeyBillingSource: "stripe"})
		included := int64(4 * meter.CompanyCreditsPerSeat)
		r.ledger.totals = meter.RunTotals{Credits: included + meter.SpendLimitStepCents*80/100/meter.CentsPerCredit, Free: map[string]int{}}
		v := r.balance(r.admin(), "/v1/me/usage/credits")
		if v.Kind != meter.BalanceCompany || v.State != meter.StateWarning || v.Included != included || v.LimitCents != meter.SpendLimitStepCents ||
			meterOf(v) != 80 || !v.CanRaise || v.RaiseToCents != 2*meter.SpendLimitStepCents || v.CanBuy != (buyView{}) || v.Seats != 4 {
			t.Fatalf("company at 80%%: %+v", v)
		}
		if v := r.balance(r.member(), "/v1/me/usage/credits"); v.CanRaise || v.RaiseToCents != 0 {
			t.Fatalf("a member may not raise it: %+v", v)
		}
		// At the ceiling, no raise is offered from the app.
		if err := r.tenants.SetMetadata(context.Background(), r.id, tenant.MetadataKeySpendLimitCents, float64(maxLimitCents)); err != nil {
			t.Fatal(err)
		}
		if v := r.balance(r.admin(), "/v1/me/usage/credits"); !v.CanRaise || v.RaiseToCents != 0 || v.LimitCents != maxLimitCents {
			t.Fatalf("at the ceiling: %+v", v)
		}
	})
	for name, plan := range map[string]string{"agreement": "company", "internal": "internal"} {
		t.Run(name, func(t *testing.T) {
			r := newCreditsRigOn(t, map[string]any{tenant.MetadataKeyPlan: plan})
			r.ledger.totals = meter.RunTotals{Credits: 1_000_000, Free: map[string]int{}}
			if v := r.balance(r.admin(), "/v1/me/usage/credits"); v.Kind != meter.BalanceUnbounded || v.State != meter.StateOK || v.MeterPct != nil || v.Left != nil {
				t.Fatalf("%s: %+v", name, v)
			}
		})
	}
	t.Run("no plan", func(t *testing.T) {
		r := newCreditsRigOn(t, map[string]any{tenant.MetadataKeyPlan: "trial", tenant.MetadataKeyTrialEndsAt: "2026-11-20T00:00:00Z"})
		v := r.balance(r.admin(), "/v1/me/usage/credits")
		if v.Kind != meter.BalanceNone || v.State != meter.StateWaiting || v.Why != meter.WhyNoPlan || !v.CanBuy.Solo || v.CanBuy.Packs {
			t.Fatalf("no plan: %+v", v)
		}
		if v := r.balance(r.member(), "/v1/me/usage/credits"); v.CanBuy.Solo {
			t.Fatalf("a member is offered Solo: %+v", v)
		}
	})
}

func (r *creditsRig) allowance(who auth.Identity) (int, map[string]any) {
	r.t.Helper()
	rr := serve(r.mux, who, http.MethodGet, "/v1/me/credits/allowance", "", nil)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

// A taf's runtime asks before each run with the taf's key: allowed while the
// balance lasts, refused with why once it waits. Setup's key gets Coach's
// rules: within its fair use it doesn't wait while a plan runs, and with no
// plan it has a small allowance of its own. Only an agent's key asks, for a
// taf of the account or Setup.
func TestTheAllowance(t *testing.T) {
	r := newCreditsRig(t)
	if code, out := r.allowance(r.taf("agt-refunds")); code != http.StatusOK || out["allowed"] != true || out["why"] != nil {
		t.Fatalf("a taf, balance left: %d %v", code, out)
	}
	r.ledger.totals = meter.RunTotals{Credits: meter.SoloIncludedCredits, Free: map[string]int{}}
	if code, out := r.allowance(r.taf("agt-refunds")); code != http.StatusOK || out["allowed"] != false || out["why"] != meter.WhyUsedUp {
		t.Fatalf("a taf, used up: %d %v", code, out)
	}
	if _, out := r.allowance(r.setup()); out["allowed"] != true {
		t.Fatalf("Coach in its fair use, used up: %v", out)
	}
	r.ledger.setupSpent = meter.SetupAllowanceMicroUSD
	if _, out := r.allowance(r.setup()); out["allowed"] != false || out["why"] != meter.WhyUsedUp {
		t.Fatalf("Coach past its fair use, used up: %v", out)
	}
	for name, who := range map[string]auth.Identity{
		"a person":            r.admin(),
		"another agent's key": r.taf("agt-billing-bot"),
		"a key with no agent": {Subject: "k", Tenant: r.id, APIKey: true},
	} {
		if code, _ := r.allowance(who); code != http.StatusForbidden {
			t.Errorf("%s: %d", name, code)
		}
	}

	none := newCreditsRigOn(t, map[string]any{})
	if _, out := none.allowance(none.taf("agt-refunds")); out["allowed"] != false || out["why"] != meter.WhyNoPlan {
		t.Fatalf("a taf, no plan: %v", out)
	}
	none.ledger.noPlanSpent = meter.NoPlanSetupMicroUSD - 1
	if _, out := none.allowance(none.setup()); out["allowed"] != true {
		t.Fatalf("Coach, no plan, in its allowance: %v", out)
	}
	none.ledger.noPlanSpent = meter.NoPlanSetupMicroUSD
	if _, out := none.allowance(none.setup()); out["allowed"] != false || out["why"] != meter.WhyNoPlan {
		t.Fatalf("Coach, no plan, past it: %v", out)
	}
	// With no plan, Coach's runs are free, whatever its fair use says.
	none.ledger.spent = meter.SetupAllowanceMicroUSD
	if rr := serve(none.mux, none.setup(), http.MethodPut, "/v1/me/runs/c-np/usage", aRun, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("Coach's run: %d %s", rr.Code, rr.Body.String())
	}
	if e := none.ledger.kept["c-np"]; !e.NoPlan || e.Counted || e.Free != meter.FreeSetup {
		t.Fatalf("Coach's run with no plan: %+v", e)
	}
}

// Someone who manages billing raises Company's spend limit one step, from
// the limit they saw: a stale one is refused, and so is a raise past what
// the phone may take it to. It answers every open request for a raise, and
// tells each person who asked.
func TestRaisingTheSpendLimit(t *testing.T) {
	r := newCreditsRigOn(t, map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeySeats: float64(2)})
	ctx := context.Background()
	for _, who := range []string{"bob", "carol"} {
		if _, err := r.packs.Ask(ctx, packs.Request{TenantID: r.id, AskedBy: who, Kind: packs.AskRaise, CreatedAt: r.now.Add(-time.Hour), ExpiresAt: r.now.AddDate(0, 0, 9)}); err != nil {
			t.Fatal(err)
		}
	}
	step := int64(meter.SpendLimitStepCents)
	raise := func(who auth.Identity, from int64) (int, map[string]any) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"raise": true, "from_cents": from})
		rr := serve(r.mux, who, http.MethodPut, "/v1/me/usage/limit", string(body), nil)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}
	if code, out := raise(r.admin(), step); code != http.StatusOK || num(out["limit_cents"]) != int(2*step) {
		t.Fatalf("a raise: %d %v", code, out)
	}
	if v := r.balance(r.admin(), "/v1/me/usage/credits"); v.LimitCents != 2*step || v.RaiseToCents != 3*step {
		t.Fatalf("the limit stays: %+v", v)
	}
	// A second tap from the same screen raises nothing more.
	if code, out := raise(r.admin(), step); code != http.StatusConflict || out["error"] != "limit_changed" || num(out["limit_cents"]) != int(2*step) {
		t.Fatalf("a stale raise: %d %v", code, out)
	}
	// Both requests are answered, and both people told.
	open, _ := r.packs.List(ctx, r.id, "", true, r.now.AddDate(0, 0, -1), r.now)
	if len(open) != 0 {
		t.Fatalf("still open: %+v", open)
	}
	var told []string
	for _, ev := range r.alerts.events {
		if ev.Outcome == alert.PackRequestAnswered && ev.Request != nil && ev.Request.Status == packs.RequestApproved && ev.Request.Kind == packs.AskRaise {
			told = append(told, ev.Request.AskedBy)
		}
	}
	if len(told) != 2 {
		t.Fatalf("told %v", told)
	}
	for name, c := range map[string]struct {
		who  auth.Identity
		body string
		want int
	}{
		"a member":       {r.member(), `{"raise": true, "from_cents": 10000}`, http.StatusForbidden},
		"an agent's key": {r.taf("agt-refunds"), `{"raise": true, "from_cents": 10000}`, http.StatusForbidden},
		"no raise":       {r.admin(), `{"from_cents": 10000}`, http.StatusBadRequest},
		"no limit named": {r.admin(), `{"raise": true}`, http.StatusBadRequest},
	} {
		if rr := serve(r.mux, c.who, http.MethodPut, "/v1/me/usage/limit", c.body, nil); rr.Code != c.want {
			t.Errorf("%s: %d %s", name, rr.Code, rr.Body.String())
		}
	}
	// Up to the ceiling, and no further.
	for from := 2 * step; from < maxLimitCents; from += step {
		if code, out := raise(r.admin(), from); code != http.StatusOK {
			t.Fatalf("from %d: %d %v", from, code, out)
		}
	}
	if code, out := raise(r.admin(), maxLimitCents); code != http.StatusConflict || out["error"] != "limit_ceiling" {
		t.Fatalf("past the ceiling: %d %v", code, out)
	}
	// Solo has no spend limit.
	solo := newCreditsRig(t)
	if rr := serve(solo.mux, solo.admin(), http.MethodPut, "/v1/me/usage/limit", `{"raise": true, "from_cents": 5000}`, nil); rr.Code != http.StatusConflict ||
		!strings.Contains(rr.Body.String(), "no_spend_limit") {
		t.Fatalf("on Solo: %d %s", rr.Code, rr.Body.String())
	}
}
