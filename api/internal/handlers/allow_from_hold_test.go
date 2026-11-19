package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// "Allow this from now on" (kiff/tafs/docs/allow-from-now-on.md): KIFF drafts
// the narrowest change for a held call, never confirms it itself, keeps the
// held call on the draft and on the revision its confirm writes, and replays
// the held calls against the draft.
// Category: authority
// Attack: an offer that widens past the held call, confirms itself, changes another Card, or is asked by someone who doesn't manage Cards
// Refused: one change per breach to the call's own figure, a draft that waits for the owner's confirm, cards.manage only
func TestAllowFromHoldDraftsTheNarrowestChangeAndReplays(t *testing.T) {
	t.Parallel()
	r := newDraftRig(t)
	r.startRae() // €150 a refund, €2,000 a day, €100 used
	held := func(proposal string, cents int64) string {
		t.Helper()
		d := r.authorize("rae", proposal, cents)
		if d.Outcome != mandate.OutcomeOwnerRequired || d.ExceptionID == "" {
			t.Fatalf("%s not held: %+v", proposal, d)
		}
		return d.ExceptionID
	}
	held("p-a", 20000)
	held("p-b", 50000)
	ex := held("p-big", 32000)
	post := func(path string, body any) (int, map[string]any) {
		t.Helper()
		rr := do(t, r.mux, admin(), http.MethodPost, path, body)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}

	// The offer: the per-refund limit up to the call's €320, a draft that waits.
	code, out := post("/v1/me/card-exceptions/"+ex+"/allow-draft", map[string]any{})
	if code != http.StatusCreated || out["draft"] == nil || out["confirm_key"] != "critical" {
		t.Fatalf("allow-draft: %d %v", code, out)
	}
	changes, _ := out["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("changes: %v", out)
	}
	c := changes[0].(map[string]any)
	if c["mandate_id"] != "rae-refund" || c["kind"] != "per_action" || c["before"] != float64(15000) || c["after"] != float64(32000) || c["rounded"] != false {
		t.Fatalf("the change: %v", c)
	}
	if round, _ := out["round"].(map[string]any); round["after"] != float64(35000) {
		t.Fatalf("round: %v", out["round"])
	}
	draftID := out["draft"].(string)
	d, err := r.drafts.Get(context.Background(), "t1", draftID)
	if err != nil || d.FromHold != ex || d.Status != "draft" || !d.Revision {
		t.Fatalf("the draft: %+v %v", d, err)
	}
	if r.perActionMax("rae-refund") != 15000 {
		t.Fatal("making the offer changed the Card")
	}
	var bodies []mandateBody
	if err := json.Unmarshal(d.Cards, &bodies); err != nil || len(bodies) != 1 {
		t.Fatalf("the draft's Cards: %s", d.Cards)
	}
	inForce := r.cardTerms("rae-refund")
	drafted, err := bodies[0].draftMandate("t1")
	if err != nil {
		t.Fatal(err)
	}
	drafted = mandate.AsIssuedOver(drafted, &inForce)
	if drafted.Grants[0].PerAction[0].Max != 32000 || drafted.Aggregates[0].Limit != inForce.Aggregates[0].Limit ||
		drafted.OnExceed != inForce.OnExceed || drafted.Subject != inForce.Subject || len(drafted.Grants) != len(inForce.Grants) {
		t.Fatalf("the draft's Card isn't the Card in force with one change: %+v vs %+v", drafted, inForce)
	}

	// Rounded: one step up, said.
	if code, out := post("/v1/me/card-exceptions/"+ex+"/allow-draft", map[string]any{"round": true}); code != http.StatusCreated ||
		out["changes"].([]any)[0].(map[string]any)["after"] != float64(35000) || out["round"] != nil {
		t.Fatalf("rounded: %d %v", code, out)
	}

	// The replay: €200 and €320 would have gone on their own, €500 still waits.
	code, out = post("/v1/me/agent-drafts/"+draftID+"/replay", map[string]any{"days": 30})
	would, _ := out["would_allow"].(map[string]any)
	if code != http.StatusOK || would["calls"] != float64(2) || out["days"] != float64(30) {
		t.Fatalf("replay: %d %v", code, out)
	}
	if code, _ := post("/v1/me/agent-drafts/"+draftID+"/replay", map[string]any{"days": 400}); code != http.StatusBadRequest {
		t.Fatalf("a replay of 400 days: %d", code)
	}

	// Confirmed, the revision says it came from the held call.
	code, out = post("/v1/me/agent-drafts/"+draftID+"/confirm", map[string]any{"version": d.Version})
	if code != http.StatusOK || r.perActionMax("rae-refund") != 32000 {
		t.Fatalf("confirm: %d %v", code, out)
	}
	revs, err := r.store.Revisions(context.Background(), "t1", "rae-refund")
	if err != nil || len(revs) < 2 || revs[len(revs)-1].FromHold != ex || revs[0].FromHold != "" {
		t.Fatalf("revisions: %+v %v", revs, err)
	}

	// The confirmed hold's limit moved: nothing narrow is left to offer.
	if code, out := post("/v1/me/card-exceptions/"+ex+"/allow-draft", map[string]any{}); code != http.StatusOK || out["none"] != "no_change" {
		t.Fatalf("a hold whose limit moved: %d %v", code, out)
	}
	// A call that no longer waits gets no offer.
	other0 := held("p-d", 60000)
	if _, err := r.store.Reject(context.Background(), "t1", other0, "alice", r.now, mandate.Refusal{}); err == nil {
		if code, _ := post("/v1/me/card-exceptions/"+other0+"/allow-draft", map[string]any{}); code != http.StatusConflict {
			t.Fatalf("an answered hold: %d", code)
		}
	} else {
		t.Fatal(err)
	}
	// Someone who doesn't manage Cards gets none.
	other := held("p-c", 90000)
	viewer := person("vic", membership.RoleViewer)
	if rr := do(t, r.mux, viewer, http.MethodPost, "/v1/me/card-exceptions/"+other+"/allow-draft", map[string]any{}); rr.Code != http.StatusForbidden {
		t.Fatalf("a viewer's ask: %d %s", rr.Code, rr.Body.String())
	}
}

// The apps show "Allow this from now on" only when /v1/me names holds.allow:
// to whoever manages the account's Cards, and never to a viewer.
func TestHoldsAllowIsACapabilityOfWhoeverManagesCards(t *testing.T) {
	t.Parallel()
	has := func(caps []string) bool {
		for _, c := range caps {
			if c == capHoldsAllow {
				return true
			}
		}
		return false
	}
	if !has(capabilitiesOf(person("alice", membership.RoleAdmin))) {
		t.Fatal("an admin isn't offered holds.allow")
	}
	if has(capabilitiesOf(person("vic", membership.RoleViewer))) {
		t.Fatal("a viewer is offered holds.allow")
	}
}
