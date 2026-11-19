package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/mandate"
)

// listedCards reads GET /v1/me/mandates as the admin, by Card id.
func (r *draftRig) listedCards() map[string]map[string]any {
	r.t.Helper()
	rr := do(r.t, r.mux, admin(), http.MethodGet, "/v1/me/mandates", nil)
	var out struct {
		Mandates []map[string]any `json:"mandates"`
	}
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &out) != nil {
		r.t.Fatalf("list mandates: %d %s", rr.Code, rr.Body.String())
	}
	byID := map[string]map[string]any{}
	for _, m := range out.Mandates {
		byID[m["id"].(string)] = m
	}
	return byID
}

// A Card says which draft issued its current terms, so a client can group
// an agent's Cards by draft; a Card issued directly says none, with no key.
func TestMandatesSayTheDraftThatIssuedThem(t *testing.T) {
	r := newDraftRig(t)
	code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "label": "Rae",
		"cards": []any{card("rae-refund", "rae"), card("rae-read", "rae")}})
	if code != http.StatusCreated {
		t.Fatalf("submit: %d %v", code, d)
	}
	first := d["id"].(string)
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+first+"/confirm", map[string]string{"version": d["version"].(string)}); rr.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/mandates", card("bo-refund", "bo")); rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("issue directly: %d %s", rr.Code, rr.Body.String())
	}
	cards := r.listedCards()
	for _, id := range []string{"rae-refund", "rae-read"} {
		if cards[id]["draft_id"] != first {
			t.Errorf("%s: draft_id %v, want %s", id, cards[id]["draft_id"], first)
		}
	}
	// Negative: a Card issued directly has no draft_id key, not "".
	if v, ok := cards["bo-refund"]["draft_id"]; ok {
		t.Errorf("a Card issued directly carries draft_id %q", v)
	}

	// A revision draft gives the Card it revises its own id; the Card it
	// leaves alone keeps the first draft's.
	code, rev := r.submit(admin(), map[string]any{"agent_id": "rae", "cards": []any{cardMax("rae-refund", "rae", 30000)}})
	if code != http.StatusCreated || rev["status"] != "confirmed" {
		t.Fatalf("revision draft: %d %v", code, rev)
	}
	second := rev["id"].(string)
	cards = r.listedCards()
	if cards["rae-refund"]["draft_id"] != second || cards["rae-read"]["draft_id"] != first {
		t.Errorf("after a revision: rae-refund %v (want %s), rae-read %v (want %s)",
			cards["rae-refund"]["draft_id"], second, cards["rae-read"]["draft_id"], first)
	}
	// The single Card read says the same.
	rr := do(t, r.mux, admin(), http.MethodGet, "/v1/me/mandates/rae-refund/statement", nil)
	var st struct {
		Mandate map[string]any `json:"mandate"`
	}
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &st) != nil || st.Mandate["draft_id"] != second {
		t.Errorf("statement: %d %s", rr.Code, rr.Body.String())
	}

	// Negative: reissued directly, its current terms came from no draft.
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/mandates", cardMax("rae-refund", "rae", 5000)); rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("reissue directly: %d %s", rr.Code, rr.Body.String())
	}
	if v, ok := r.listedCards()["rae-refund"]["draft_id"]; ok {
		t.Errorf("a Card reissued directly still carries draft_id %q", v)
	}
	rr = do(t, r.mux, admin(), http.MethodGet, "/v1/me/mandates/rae-refund/statement", nil)
	if strings.Contains(rr.Body.String(), `"draft_id"`) {
		t.Errorf("statement of a Card reissued directly: %s", rr.Body.String())
	}
}

// A trial's Cards are issued by the draft being tried: each carries its id,
// including the agent's Cards in force the trial copies.
func TestTrialCardsSayTheirDraft(t *testing.T) {
	r := newDraftRigOn(t, gatewayListed{stubDomainSource{yaml: refundCentsYAML}})
	r.startRae()
	id, version := r.raeV2()
	if code, d := r.tryDraft(id, version); code != http.StatusOK || d["status"] != "trying" {
		t.Fatalf("try: %d %v", code, d)
	}
	held := r.trialCardsOf("rae")
	if len(held) == 0 {
		t.Fatal("no trial Cards")
	}
	cards := r.listedCards()
	for _, m := range held {
		if m.DraftID != id {
			t.Errorf("trial Card %s: draft %q, want %s", m.ID, m.DraftID, id)
		}
		if c, ok := cards[m.ID]; !ok || c["draft_id"] != id {
			t.Errorf("listed trial Card %s: draft_id %v", m.ID, c["draft_id"])
		}
	}
	if !strings.Contains(held[0].ID, mandate.TrialSuffix) {
		t.Fatalf("not a trial Card: %s", held[0].ID)
	}
}
