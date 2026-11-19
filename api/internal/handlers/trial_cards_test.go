package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/agentdrafts"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/proposals"
)

// Trying a version (kiff/tafs#173): its Cards are issued as trial Cards,
// apart from the agent's own, and its trial ends by going back, by its
// runtime, or by installing it.

// raeV2 submits rae's version 2: refunds of up to 300.00 each, on its Card
// and on a new one. It returns the draft's id and version.
func (r *draftRig) raeV2() (string, string) {
	r.t.Helper()
	code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "label": "Rae v2",
		"cards": []any{cardMax("rae-refund", "rae", 30000), cardMax("rae-read", "rae", 30000)}})
	if code != http.StatusCreated || d["status"] != "draft" {
		r.t.Fatalf("submit rae v2: %d %v", code, d)
	}
	return d["id"].(string), d["version"].(string)
}

func (r *draftRig) tryDraft(id, version string) (int, map[string]any) {
	r.t.Helper()
	rr := do(r.t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+id+"/confirm", map[string]any{"version": version, "as": "trial"})
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

// authorize decides one refund of cents for subject, as decide does.
func (r *draftRig) authorize(subject, proposal string, cents int64) mandate.Decision {
	r.t.Helper()
	m := r.cardTerms("rae-refund")
	a := mandate.NewAuthorizer(r.store, nil, func() time.Time { return r.now })
	d, err := a.Authorize(context.Background(), "t1", proposal, mandate.Request{Subject: subject, Domain: m.Domain, DomainID: m.DomainID,
		Environment: m.Environment, Action: "refund", Parameters: map[string]int64{"amount_cents": cents}})
	if err != nil {
		r.t.Fatal(err)
	}
	return d
}

func (r *draftRig) trialCardsOf(agent string) []mandate.Mandate {
	r.t.Helper()
	ms, err := r.store.ForSubject(context.Background(), "t1", mandate.TrialSubject(agent))
	if err != nil {
		r.t.Fatal(err)
	}
	var active []mandate.Mandate
	for _, m := range ms {
		if m.Active(r.now) {
			active = append(active, m)
		}
	}
	return active
}

// Invariant: a version being tried is decided on its own trial Cards, and
// the live version on the agent's Cards, each with its own counters. Trying
// it changes nothing on the live Cards, and no call made for the agent sees
// a trial Card.
// Category: authority
// Attack: start a trial of a version with a higher limit, then make the agent's ordinary calls, hoping they get the trial's limit, or the trial's spending counts against (or resets) the live Card
// Refused: the agent's calls are decided on its live Card as before; the trial's calls only on the trial Cards, from zero, until they lapse in 30 days
func TestTrialCardsAreTheVersionsAndApartFromTheLive(t *testing.T) {
	// The domains listed, so the agent's Card read places each Card.
	r := newDraftRigOn(t, gatewayListed{stubDomainSource{yaml: refundCentsYAML}})
	r.startRae()
	id, version := r.raeV2()
	code, d := r.tryDraft(id, version)
	trial, _ := d["trial"].(map[string]any)
	cards, _ := trial["cards"].([]any)
	if code != http.StatusOK || d["status"] != "trying" || len(cards) != 2 {
		t.Fatalf("try rae v2: %d %v", code, d)
	}
	for _, c := range cards {
		if !strings.Contains(c.(string), mandate.TrialSuffix+"-") {
			t.Fatalf("a trial Card's id: %v", c)
		}
	}
	// The live Cards are as they were.
	if r.perActionMax("rae-refund") != 15000 || r.cardActive("rae-read") || r.used("rae-refund") != 10000 {
		t.Fatal("trying a version changed the live Cards")
	}
	held := r.trialCardsOf("rae")
	if len(held) != 2 {
		t.Fatalf("trial Cards held: %+v", held)
	}
	for _, m := range held {
		if m.Subject != "rae~trial" || !m.ValidUntil.Equal(r.now.Add(agentdrafts.TrialTTL)) {
			t.Fatalf("trial Card %s: held by %s until %v", m.ID, m.Subject, m.ValidUntil)
		}
	}
	// 200.00 is past the live Card's 150.00 a refund, and within the trial's
	// 300.00; the trial's counters start from zero.
	if live := r.authorize("rae", "p-live", 20000); live.Allowed() {
		t.Fatalf("a live call got the trial's limit: %+v", live)
	}
	if tried := r.authorize(mandate.TrialSubject("rae"), "p-trial", 20000); !tried.Allowed() {
		t.Fatalf("a trial call on the trial's Card: %+v", tried)
	}
	if r.used("rae-refund") != 10000 {
		t.Fatalf("the trial drew on the live Card: %d", r.used("rae-refund"))
	}
	// The agent's Card read shows the trial's Cards only with the draft.
	rr := do(t, r.mux, agentKey("rae", ""), http.MethodGet, "/v1/me/card?trial="+id, nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), mandate.TrialSuffix) || strings.Contains(rr.Body.String(), `"id":"rae-refund"`) {
		t.Fatalf("the agent's Card during its trial: %d %s", rr.Code, rr.Body.String())
	}
	rr = do(t, r.mux, agentKey("rae", ""), http.MethodGet, "/v1/me/card", nil)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), mandate.TrialSuffix) {
		t.Fatalf("the agent's own Card shows a trial Card: %s", rr.Body.String())
	}
}

// Only a new version of an agent with Cards is tried, as the owner was
// shown it; a new agent's draft is confirmed.
func TestTrialOnlyOfARevisionAsShown(t *testing.T) {
	r := newDraftRig(t)
	code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{card("rae-refund", "rae")}})
	if code != http.StatusCreated {
		t.Fatalf("submit: %d %v", code, d)
	}
	if code, out := r.tryDraft(d["id"].(string), d["version"].(string)); code != http.StatusConflict {
		t.Fatalf("trying a new agent's draft: %d %v", code, out)
	}
	r2 := newDraftRig(t)
	r2.startRae()
	id, _ := r2.raeV2()
	if code, out := r2.tryDraft(id, "not-the-version"); code != http.StatusConflict || r2.status(id) != agentdrafts.Draft {
		t.Fatalf("trying another version than shown: %d %v", code, out)
	}
	rr := do(t, r2.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+id+"/confirm", map[string]any{"version": "v", "as": "live"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("an unknown as: %d", rr.Code)
	}
}

// Invariant: a trial ends for good: going back, its runtime's sweep, or a
// new trial revoke its Cards, and only the agent's own key or someone who
// manages Cards can end it.
// Category: authority
// Attack: keep a trial's higher limits after going back, start a second trial to hold two sets of trial Cards, or end another agent's trial with this agent's key
// Refused: one trial per agent; an ended trial's Cards are revoked; another agent's key is told there is no such draft
func TestTrialEndsAndRevokesItsCards(t *testing.T) {
	r := newDraftRig(t)
	r.startRae()
	first, v1 := r.raeV2()
	if code, d := r.tryDraft(first, v1); code != http.StatusOK {
		t.Fatalf("try: %d %v", code, d)
	}
	// A second trial ends the first: one per agent.
	code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "label": "Rae v3",
		"cards": []any{cardMax("rae-refund", "rae", 25000)}})
	if code != http.StatusCreated {
		t.Fatalf("submit v3: %d %v", code, d)
	}
	second := d["id"].(string)
	if code, d := r.tryDraft(second, d["version"].(string)); code != http.StatusOK || d["status"] != "trying" {
		t.Fatalf("try v3: %d %v", code, d)
	}
	was, _ := r.drafts.Get(context.Background(), "t1", first)
	if was.Status != agentdrafts.Draft || was.Trial == nil || was.Trial.EndedBy != agentdrafts.EndedBack {
		t.Fatalf("the first trial: %+v", was)
	}
	if n := len(r.trialCardsOf("rae")); n != 1 {
		t.Fatalf("trial Cards in force: %d, want v3's one", n)
	}
	// Another agent's key can't see or end it.
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		if rr := do(t, r.mux, agentKey("bob", ""), method, "/v1/me/agent-drafts/"+second+"/trial", nil); rr.Code != http.StatusNotFound {
			t.Fatalf("%s by another agent: %d", method, rr.Code)
		}
	}
	// The agent's own key ends it (its runtime's sweep): expired.
	rr := do(t, r.mux, agentKey("rae", ""), http.MethodDelete, "/v1/me/agent-drafts/"+second+"/trial", nil)
	var ended map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &ended)
	if rr.Code != http.StatusOK || ended["status"] != "ended" || ended["ended_by"] != agentdrafts.EndedExpired {
		t.Fatalf("end by the agent's key: %d %v", rr.Code, ended)
	}
	if n := len(r.trialCardsOf("rae")); n != 0 || r.status(second) != agentdrafts.Draft {
		t.Fatalf("after the end: %d trial Cards, draft %s", n, r.status(second))
	}
	if tried := r.authorize(mandate.TrialSubject("rae"), "p-after", 100); tried.Allowed() {
		t.Fatalf("a trial call after its end: %+v", tried)
	}
	// Ending it again says how it ended.
	if rr := do(t, r.mux, admin(), http.MethodDelete, "/v1/me/agent-drafts/"+second+"/trial", nil); rr.Code != http.StatusConflict ||
		!strings.Contains(rr.Body.String(), `"ended_by":"expired"`) {
		t.Fatalf("end again: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, r.mux, admin(), http.MethodGet, "/v1/me/agent-drafts/"+second+"/trial", nil); rr.Code != http.StatusOK ||
		!strings.Contains(rr.Body.String(), `"status":"ended"`) {
		t.Fatalf("read the ended trial: %d %s", rr.Code, rr.Body.String())
	}
}

// Going back from the phone is recorded as back; a lapsed trial, as
// expired.
func TestTrialGoBackAndLapse(t *testing.T) {
	r := newDraftRig(t)
	r.startRae()
	id, v := r.raeV2()
	r.tryDraft(id, v)
	r.now = r.now.Add(agentdrafts.TrialTTL + time.Minute)
	if len(r.trialCardsOf("rae")) != 0 {
		t.Fatal("a trial's Cards outlived it")
	}
	rr := do(t, r.mux, admin(), http.MethodGet, "/v1/me/agent-drafts/"+id+"/trial", nil)
	if !strings.Contains(rr.Body.String(), `"ended_by":"expired"`) {
		t.Fatalf("a lapsed trial: %s", rr.Body.String())
	}
	r2 := newDraftRig(t)
	r2.startRae()
	id, v = r2.raeV2()
	r2.tryDraft(id, v)
	rr = do(t, r2.mux, admin(), http.MethodDelete, "/v1/me/agent-drafts/"+id+"/trial", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"ended_by":"back"`) {
		t.Fatalf("go back: %d %s", rr.Code, rr.Body.String())
	}
	if d, _ := r2.drafts.Get(context.Background(), "t1", id); !d.ExpiresAt.Equal(r2.now.Add(agentdrafts.DefaultTTL)) {
		t.Fatalf("after going back the draft waits as a fresh one would: until %v", d.ExpiresAt)
	}
}

// Invariant: installing a version being tried issues its Cards live and
// revokes its trial Cards, together, and the trial records that it was
// installed.
// Category: authority
// Attack: install a tried version and keep its trial Cards in force beside the live ones, doubling its room
// Refused: the trial Cards are revoked in the same issue as the live revision
func TestTrialInstallEndsIt(t *testing.T) {
	r := newDraftRig(t)
	r.startRae()
	id, v := r.raeV2()
	r.tryDraft(id, v)
	rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+id+"/confirm", map[string]any{"version": v})
	var d map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &d)
	trial, _ := d["trial"].(map[string]any)
	if rr.Code != http.StatusOK || d["status"] != "confirmed" || trial["ended_by"] != agentdrafts.EndedInstall {
		t.Fatalf("install: %d %v", rr.Code, d)
	}
	if r.perActionMax("rae-refund") != 30000 || !r.cardActive("rae-read") || r.used("rae-refund") != 10000 {
		t.Fatal("the version's Cards aren't live as the owner confirmed them")
	}
	if n := len(r.trialCardsOf("rae")); n != 0 {
		t.Fatalf("%d trial Cards left after install", n)
	}
}

// The trial's counts are its calls in the owner's record, from when it
// started: the agent's runtime keeps them, and can't disagree with KIFF.
func TestTrialCountsAreTheRecords(t *testing.T) {
	calls := proposals.NewMemoryReader()
	r := newDraftRig(t, WithMandatesCalls(calls))
	r.startRae()
	id, v := r.raeV2()
	r.tryDraft(id, v)
	at := r.now.Add(time.Minute)
	for i, outcome := range []string{"allowed", "owner_required", "allowed", "blocked"} {
		p := "p" + string(rune('a'+i))
		calls.Append("t1", proposals.DecisionRow{ProposalID: p, ActorID: "rae", Outcome: outcome, TrialDraft: id, CreatedAt: at})
	}
	calls.Append("t1", proposals.DecisionRow{ProposalID: "pb", ActorID: "rae", Outcome: "allowed", TrialDraft: id, CreatedAt: at.Add(time.Minute)})
	calls.Append("t1", proposals.DecisionRow{ProposalID: "live", ActorID: "rae", Outcome: "allowed", CreatedAt: at})
	calls.Append("t1", proposals.DecisionRow{ProposalID: "before", ActorID: "rae", Outcome: "allowed", TrialDraft: id, CreatedAt: r.now.Add(-time.Hour)})
	rr := do(t, r.mux, agentKey("rae", ""), http.MethodGet, "/v1/me/agent-drafts/"+id+"/trial", nil)
	var got struct {
		Status                        string
		Calls, Allowed, Held, Refused int
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if rr.Code != http.StatusOK || got.Status != "trying" || got.Calls != 4 || got.Allowed != 2 || got.Held != 1 || got.Refused != 1 {
		t.Fatalf("trial counts: %d %s", rr.Code, rr.Body.String())
	}
}

// An agent reaches its own trial's held calls, as its own.
func TestAgentsOwnIncludesItsTrial(t *testing.T) {
	if !agentsOwn("rae", "rae") || !agentsOwn("rae~trial", "rae") || agentsOwn("bob~trial", "rae") || agentsOwn("rae~trial", "") {
		t.Fatal("agentsOwn")
	}
}
