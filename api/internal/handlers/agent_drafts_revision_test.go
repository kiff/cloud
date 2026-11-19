package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/agentdrafts"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
)

// Revision drafts (kiff/tafs#107): a draft for an agent that already holds
// Cards revises them under the same ids, or adds new ones.

// cardMax is card with its per-refund maximum.
func cardMax(id, subject string, max int) map[string]any {
	c := card(id, subject)
	c["grants"] = []map[string]any{{"action": "refund", "per_action_max": []map[string]any{{"quantity": "sum(amount_cents)", "max": max}}}}
	return c
}

func (r *draftRig) perActionMax(id string) int64 {
	r.t.Helper()
	ms, _ := r.store.List(context.Background(), "t1")
	for _, m := range ms {
		if m.ID == id {
			return m.Grants[0].PerAction[0].Max
		}
	}
	r.t.Fatalf("no Card %s", id)
	return 0
}

func (r *draftRig) used(id string) int64 {
	r.t.Helper()
	n, err := r.store.Used(context.Background(), "t1", id, "sum(amount_cents)", r.now.Add(-24*time.Hour), "")
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

// startRae issues rae's refund Card through a draft, and draws 100.00 on it.
func (r *draftRig) startRae() {
	r.t.Helper()
	if code, d := r.submit(admin(), map[string]any{"agent_id": "rae", "cards": []any{card("rae-refund", "rae")}}); code != http.StatusCreated || d["status"] != "confirmed" {
		r.t.Fatalf("start rae: %d %v", code, d)
	}
	m := r.cardTerms("rae-refund")
	a := mandate.NewAuthorizer(r.store, nil, func() time.Time { return r.now })
	d, err := a.Authorize(context.Background(), "t1", "p-1", mandate.Request{Subject: "rae", Domain: m.Domain, DomainID: m.DomainID,
		Environment: m.Environment, Action: "refund", Parameters: map[string]int64{"amount_cents": 10000}})
	if err != nil || !d.Allowed() || r.used("rae-refund") != 10000 {
		r.t.Fatalf("draw on rae's Card: %+v %v, used %d", d, err, r.used("rae-refund"))
	}
}

// Invariant: a revision draft changes an agent's Cards only when an owner
// confirms it, all of them or none, keeping each revised Card's usage so
// its totals don't start again, and it gives no key.
// Category: authority
// Attack: an agent drafts a revision of its own Card with a higher limit, hoping it applies before an owner confirms, resets the day's totals, or yields a second key
// Refused: nothing changes until an owner confirms; then the Card is revised under the same id with what it used kept, and no claim secret or key comes out of it
func TestAgentDraftRevisesTheAgentsCards(t *testing.T) {
	r := newDraftRig(t)
	r.startRae()
	code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "label": "Rae v2",
		"cards": []any{cardMax("rae-refund", "rae", 30000), card("rae-read", "rae")}})
	if code != http.StatusCreated || d["status"] != "draft" || d["claim_secret"] != nil || d["revision"] != true {
		t.Fatalf("submit a revision: %d %v", code, d)
	}
	// The owner is shown each revised Card as it is now and as it will be.
	cards, _ := d["cards"].([]any)
	first, _ := cards[0].(map[string]any)
	cur, _ := first["current"].(map[string]any)
	if first["revises"] != "rae-refund" || cur == nil {
		t.Fatalf("first Card in the view: %v", first)
	}
	if b, _ := json.Marshal(cur["grants"]); !json.Valid(b) || string(b) == "null" {
		t.Fatalf("current terms: %v", cur)
	}
	if second, _ := cards[1].(map[string]any); second["revises"] != nil {
		t.Fatalf("a new Card says it revises one: %v", second)
	}
	if r.perActionMax("rae-refund") != 15000 || r.cardActive("rae-read") {
		t.Fatal("a draft changed the Cards before an owner confirmed it")
	}
	id := d["id"].(string)
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+id+"/confirm", map[string]string{"version": d["version"].(string)}); rr.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", rr.Code, rr.Body.String())
	}
	if r.perActionMax("rae-refund") != 30000 || !r.cardActive("rae-read") || !r.cardActive("rae-refund") {
		t.Fatal("the confirmed revision isn't in force")
	}
	if r.used("rae-refund") != 10000 {
		t.Fatalf("the revision reset what the Card used: %d", r.used("rae-refund"))
	}
	if revs, _ := r.store.Revisions(context.Background(), "t1", "rae-refund"); len(revs) != 2 {
		t.Fatalf("revisions = %d, want 2", len(revs))
	}
	// No key comes out of it, and it can't be discarded: going back is
	// another revision.
	got, _ := r.drafts.Get(context.Background(), "t1", id)
	if _, err := r.drafts.Claim(context.Background(), id, got.ClaimHash, r.now); err == nil {
		t.Fatal("a revision draft was claimed")
	}
	if rr := do(t, r.mux, admin(), http.MethodDelete, "/v1/me/agent-drafts/"+id, nil); rr.Code != http.StatusConflict || !r.cardActive("rae-refund") {
		t.Fatalf("discard a confirmed revision: %d, rae-refund active %v", rr.Code, r.cardActive("rae-refund"))
	}
}

// A Card revised since the draft was made isn't revised over: the owner
// would approve terms they weren't shown.
func TestAgentDraftRevisionOfAChangedCardIsRefused(t *testing.T) {
	r := newDraftRig(t)
	r.startRae()
	_, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{cardMax("rae-refund", "rae", 30000)}})
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/mandates", cardMax("rae-refund", "rae", 5000)); rr.Code != http.StatusOK && rr.Code != http.StatusCreated {
		t.Fatalf("the owner's own change: %d %s", rr.Code, rr.Body.String())
	}
	rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+d["id"].(string)+"/confirm", map[string]string{"version": d["version"].(string)})
	if rr.Code != http.StatusConflict || r.perActionMax("rae-refund") != 5000 {
		t.Fatalf("confirm over a changed Card: %d %s, max %d", rr.Code, rr.Body.String(), r.perActionMax("rae-refund"))
	}
	// It says why as a code too, so a client needn't match the text.
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body["reason"] != "card_revised" || body["error"] == "" {
		t.Fatalf("the refusal's body: %s", rr.Body.String())
	}
}

// A draft revises only Cards its agent holds alone: never another agent's.
func TestAgentDraftRevisesOnlyItsAgentsCards(t *testing.T) {
	r := newDraftRig(t)
	r.startRae()
	if code, _ := r.submit(admin(), map[string]any{"agent_id": "bo", "cards": []any{card("bo-refund", "bo")}}); code != http.StatusCreated {
		t.Fatal("start bo")
	}
	if code, _ := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{cardMax("bo-refund", "rae", 30000)}}); code != http.StatusConflict {
		t.Fatalf("a draft naming another agent's Card: %d", code)
	}
}

// A revision draft stuck confirming is settled from what stands: its
// Cards were issued all together or not at all. Issued, it is finished;
// not, it fails, and the agent's Cards are left exactly as they were.
func TestAgentDraftSweepSettlesAStuckRevision(t *testing.T) {
	r := newDraftRig(t)
	r.startRae()
	ctx := context.Background()
	stuck := func(max int, issue bool) string {
		_, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{cardMax("rae-refund", "rae", max)}})
		id := d["id"].(string)
		if _, err := r.drafts.Confirm(ctx, "t1", id, d["version"].(string), "alice", "admin", []string{"rae-refund"}, r.now); err != nil {
			t.Fatal(err)
		}
		if issue {
			m := r.cardTerms("rae-refund")
			m.Grants[0].PerAction[0].Max = int64(max)
			if err := r.store.IssueAll(ctx, []mandate.Mandate{m}, "alice", nil, nil, time.Time{}); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	notIssued := stuck(40000, false)
	r.now = r.now.Add(agentdrafts.StuckAfter + time.Second)
	if n, err := agentdrafts.Sweep(ctx, r.drafts, r.store, r.now); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if got := r.status(notIssued); got != agentdrafts.Failed || r.perActionMax("rae-refund") != 15000 || !r.cardActive("rae-refund") {
		t.Fatalf("not issued: status %s, max %d, active %v", got, r.perActionMax("rae-refund"), r.cardActive("rae-refund"))
	}
	issued := stuck(30000, true)
	r.now = r.now.Add(agentdrafts.StuckAfter + time.Second)
	if n, err := agentdrafts.Sweep(ctx, r.drafts, r.store, r.now); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if got := r.status(issued); got != agentdrafts.Confirmed || r.perActionMax("rae-refund") != 30000 || !r.cardActive("rae-refund") {
		t.Fatalf("issued: status %s, max %d", got, r.perActionMax("rae-refund"))
	}
}

// An agent's key reads its own agent's drafts, and no one else's.
func TestAgentKeyReadsOnlyItsOwnDrafts(t *testing.T) {
	r := newDraftRig(t)
	r.startRae()
	_, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{cardMax("rae-refund", "rae", 30000)}})
	path := "/v1/me/agent-drafts/" + d["id"].(string)
	if rr := do(t, r.mux, agentKey("rae", ""), http.MethodGet, path, nil); rr.Code != http.StatusOK {
		t.Fatalf("rae reads its draft: %d", rr.Code)
	}
	if rr := do(t, r.mux, agentKey("bo", ""), http.MethodGet, path, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("bo reads rae's draft: %d", rr.Code)
	}
}

func (r *draftRig) cardTerms(id string) mandate.Mandate {
	r.t.Helper()
	ms, _ := r.store.List(context.Background(), "t1")
	for _, m := range ms {
		if m.ID == id {
			return m
		}
	}
	r.t.Fatalf("no Card %s", id)
	return mandate.Mandate{}
}

// A Card revised between confirm's own check and the issue (another
// confirmation, the owner's edit) is caught under the Cards' locks: the
// confirmation fails with nothing issued, and the other revision stands.
func TestAgentDraftRevisionLosesARaceWithoutOverwriting(t *testing.T) {
	r, f := faultyRig(t, &faultyCards{})
	r.startRae()
	_, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{cardMax("rae-refund", "rae", 30000), card("rae-read", "rae")}})
	f.beforeIssueAll = func() {
		m := r.cardTerms("rae-refund")
		m.Grants[0].PerAction[0].Max = 5000
		if err := r.store.Issue(context.Background(), m, "bob"); err != nil {
			t.Error(err)
		}
	}
	rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+d["id"].(string)+"/confirm", map[string]string{"version": d["version"].(string)})
	if rr.Code != http.StatusConflict {
		t.Fatalf("confirm that lost the race: %d %s", rr.Code, rr.Body.String())
	}
	if r.perActionMax("rae-refund") != 5000 || r.cardActive("rae-read") || !r.cardActive("rae-refund") {
		t.Fatalf("after the lost race: max %d, rae-read active %v", r.perActionMax("rae-refund"), r.cardActive("rae-read"))
	}
	if got := r.status(d["id"].(string)); got != agentdrafts.Failed {
		t.Fatalf("draft status = %s", got)
	}
}

// Going back (kiff/tafs#107): a restore draft brings back a Card's terms as
// they were at an earlier revision, read from KIFF's own history. It only
// waits, even submitted by someone who may confirm, and once confirmed the
// Card is revised again under its id, its usage kept.
func TestAgentDraftRestoresAnEarlierRevision(t *testing.T) {
	r := newDraftRig(t)
	r.startRae()
	if code, d := r.submit(admin(), map[string]any{"agent_id": "rae", "cards": []any{cardMax("rae-refund", "rae", 30000)}}); code != http.StatusCreated || d["status"] != "confirmed" {
		t.Fatalf("revise to 300.00: %d %v", code, d)
	}
	rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/restore", map[string]any{"agent_id": "rae", "label": "Back to v1",
		"revisions": []any{map[string]any{"id": "rae-refund", "revision": 1}}})
	var d map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &d)
	if rr.Code != http.StatusCreated || d["status"] != "draft" || d["claim_secret"] != nil {
		t.Fatalf("restore: %d %v", rr.Code, d)
	}
	if r.perActionMax("rae-refund") != 30000 {
		t.Fatal("a restore changed the Card before an owner confirmed it")
	}
	cards, _ := d["cards"].([]any)
	first, _ := cards[0].(map[string]any)
	if first["revises"] != "rae-refund" || first["current"] == nil {
		t.Fatalf("the restore's Card in the view: %v", first)
	}
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+d["id"].(string)+"/confirm", map[string]string{"version": d["version"].(string)}); rr.Code != http.StatusOK {
		t.Fatalf("confirm the restore: %d %s", rr.Code, rr.Body.String())
	}
	if r.perActionMax("rae-refund") != 15000 || r.used("rae-refund") != 10000 {
		t.Fatalf("after the restore: max %d, used %d", r.perActionMax("rae-refund"), r.used("rae-refund"))
	}
	if revs, _ := r.store.Revisions(context.Background(), "t1", "rae-refund"); len(revs) != 3 {
		t.Fatalf("revisions = %d, want 3", len(revs))
	}
	for _, c := range []map[string]any{
		{"agent_id": "rae", "revisions": []any{map[string]any{"id": "rae-refund", "revision": 9}}},
		{"agent_id": "rae", "revisions": []any{map[string]any{"id": "rae-nothing", "revision": 1}}},
	} {
		if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/restore", c); rr.Code != http.StatusNotFound {
			t.Errorf("%v: %d %s", c, rr.Code, rr.Body.String())
		}
	}
}

// Going back to a version that didn't hold a Card a later one added
// (kiff/tafs#107): the restore retires it. The draft names it with its
// terms now, its version commits to it, and confirming revokes it in the
// same transaction the restored Card is issued. A retired Card that moved
// on refuses the whole confirmation, as a revised one does.
func TestAgentDraftRestoreRetiresACardTheVersionDidntHold(t *testing.T) {
	r := newDraftRig(t)
	r.startRae()
	if code, d := r.submit(admin(), map[string]any{"agent_id": "rae", "cards": []any{cardMax("rae-refund", "rae", 30000), card("rae-read", "rae")}}); code != http.StatusCreated || d["status"] != "confirmed" {
		t.Fatalf("version 2 adds rae-read: %d %v", code, d)
	}
	restore := func() map[string]any {
		rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/restore", map[string]any{"agent_id": "rae", "label": "Back to v1",
			"revisions": []any{map[string]any{"id": "rae-refund", "revision": 1}}})
		var d map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &d)
		if rr.Code != http.StatusCreated || d["status"] != "draft" {
			t.Fatalf("restore: %d %v", rr.Code, d)
		}
		return d
	}
	d := restore()
	retires, _ := d["retires"].([]any)
	if len(retires) != 1 {
		t.Fatalf("the restore retires %v; want rae-read", d["retires"])
	}
	rt, _ := retires[0].(map[string]any)
	if rt["id"] != "rae-read" || rt["revision"] != float64(1) || rt["current"] == nil {
		t.Fatalf("the retired Card in the view: %v", rt)
	}
	if !r.cardActive("rae-read") {
		t.Fatal("a restore retired a Card before an owner confirmed it")
	}

	// rae-read moves on meanwhile: the confirmation is refused, nothing changes.
	if code, _ := r.submit(admin(), map[string]any{"agent_id": "rae", "cards": []any{cardMax("rae-read", "rae", 100)}}); code != http.StatusCreated {
		t.Fatal("revise rae-read")
	}
	rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+d["id"].(string)+"/confirm", map[string]string{"version": d["version"].(string)})
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "card_revised") || !r.cardActive("rae-read") || r.perActionMax("rae-refund") != 30000 {
		t.Fatalf("confirm over a moved retired Card: %d %s", rr.Code, rr.Body.String())
	}

	// Drafted again, it still retires rae-read; confirmed, the version's Cards are back.
	d = restore()
	rr = do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+d["id"].(string)+"/redraft", nil)
	var again map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &again)
	if rr.Code != http.StatusCreated || len(again["retires"].([]any)) != 1 || again["version"] != d["version"] {
		t.Fatalf("redraft of a restore: %d %v", rr.Code, again)
	}
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+again["id"].(string)+"/confirm", map[string]string{"version": again["version"].(string)}); rr.Code != http.StatusOK {
		t.Fatalf("confirm the restore: %d %s", rr.Code, rr.Body.String())
	}
	if r.cardActive("rae-read") || !r.cardActive("rae-refund") || r.perActionMax("rae-refund") != 15000 {
		t.Fatalf("after the restore: rae-read active %v, rae-refund max %d", r.cardActive("rae-read"), r.perActionMax("rae-refund"))
	}
}

// A restore with nothing to retire still retires what is issued before it
// is confirmed: a Card added meanwhile refuses the
// confirmation, rather than staying in force after going back.
func TestAgentDraftRestoreCatchesACardAddedBeforeConfirm(t *testing.T) {
	r := newDraftRig(t)
	r.startRae()
	rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/restore", map[string]any{"agent_id": "rae", "label": "Back to v1",
		"revisions": []any{map[string]any{"id": "rae-refund", "revision": 1}}})
	var d map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &d)
	if rr.Code != http.StatusCreated || d["retires"] != nil {
		t.Fatalf("restore with nothing else held: %d %v", rr.Code, d)
	}
	// Only rae-read, so rae-refund's revision, which the restore checks, stays.
	if code, _ := r.submit(admin(), map[string]any{"agent_id": "rae", "cards": []any{card("rae-read", "rae")}}); code != http.StatusCreated {
		t.Fatal("add rae-read")
	}
	rr = do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+d["id"].(string)+"/confirm", map[string]string{"version": d["version"].(string)})
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "card_revised") || !r.cardActive("rae-read") {
		t.Fatalf("confirm after a Card was added: %d %s", rr.Code, rr.Body.String())
	}
}

// During a trial against a draft, the agent reads the Cards that draft
// would leave (kiff/tafs#107), as its calls are decided on them: a revised
// Card's terms, a Card it adds, and none it retires. Another agent's draft,
// or one not waiting, is refused.
func TestAgentCardDuringATrialIsTheDrafts(t *testing.T) {
	// The domains listed, so a Card is placed in the gateway's, as in KIFF.
	r := newDraftRigOn(t, gatewayListed{stubDomainSource{yaml: refundCentsYAML}})
	r.startRae()
	read := func(trial string) (int, agentCardBody) {
		path := "/v1/me/card"
		if trial != "" {
			path += "?trial=" + trial
		}
		rr := do(t, r.mux, agentKey("rae", ""), http.MethodGet, path, nil)
		var body agentCardBody
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		return rr.Code, body
	}
	maxOf := func(b agentCardBody, id string) int64 {
		for _, c := range b.Cards {
			if c.ID == id && len(c.Grants) > 0 && len(c.Grants[0].PerActionMax) > 0 {
				return c.Grants[0].PerActionMax[0].Max
			}
		}
		return -1
	}
	_, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{cardMax("rae-refund", "rae", 30000), card("rae-read", "rae")}})
	code, live := read("")
	if code != http.StatusOK || len(live.Cards) != 1 || maxOf(live, "rae-refund") != 15000 {
		t.Fatalf("the live Card: %d %+v", code, live)
	}
	code, trial := read(d["id"].(string))
	if code != http.StatusOK || len(trial.Cards) != 2 || maxOf(trial, "rae-refund") != 30000 || maxOf(trial, "rae-read") != 15000 {
		t.Fatalf("the Card during a trial of the draft: %d %+v", code, trial)
	}
	if code, _ := read("live"); code != http.StatusOK {
		t.Fatalf("trial=live: %d", code)
	}
	if rr := do(t, r.mux, agentKey("bo", ""), http.MethodGet, "/v1/me/card?trial="+d["id"].(string), nil); rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "trial_draft_other_agent") {
		t.Fatalf("another agent's draft: %d %s", rr.Code, rr.Body.String())
	}

	// A restore retires rae-read: a trial of going back reads without it.
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+d["id"].(string)+"/confirm", map[string]string{"version": d["version"].(string)}); rr.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", rr.Code, rr.Body.String())
	}
	rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/restore", map[string]any{"agent_id": "rae",
		"revisions": []any{map[string]any{"id": "rae-refund", "revision": 1}}})
	var back map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &back)
	if rr.Code != http.StatusCreated {
		t.Fatalf("restore: %d %s", rr.Code, rr.Body.String())
	}
	code, trial = read(back["id"].(string))
	if code != http.StatusOK || len(trial.Cards) != 1 || maxOf(trial, "rae-refund") != 15000 || maxOf(trial, "rae-read") != -1 {
		t.Fatalf("the Card during a trial of going back: %d %+v", code, trial)
	}
	if code, _ := read(d["id"].(string)); code != http.StatusConflict {
		t.Fatalf("a confirmed draft, as a trial: %d", code)
	}
}

// A Card a draft re-issues with exactly its terms says unchanged
// (kiff/tafs#138); a moved cap, or a change no line shows (what happens past
// the Card), doesn't.
func TestAgentDraftSaysWhenACardIsUnchanged(t *testing.T) {
	r := newDraftRig(t)
	r.startRae()
	unchanged := func(c map[string]any) any {
		t.Helper()
		code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{c, card("rae-read", "rae")}})
		if code != http.StatusCreated {
			t.Fatalf("submit: %d %v", code, d)
		}
		cards, _ := d["cards"].([]any)
		first, _ := cards[0].(map[string]any)
		if second, _ := cards[1].(map[string]any); second["unchanged"] != nil {
			t.Fatalf("a new Card read as unchanged: %v", second)
		}
		return first["unchanged"]
	}
	if got := unchanged(card("rae-refund", "rae")); got != true {
		t.Fatalf("re-issued as it is: unchanged = %v", got)
	}
	if got := unchanged(cardMax("rae-refund", "rae", 30000)); got != nil {
		t.Fatalf("a moved cap: unchanged = %v", got)
	}
	refused := card("rae-refund", "rae")
	refused["on_exceed"] = "refuse"
	if got := unchanged(refused); got != nil {
		t.Fatalf("held → refused, which no line shows: unchanged = %v", got)
	}
	// Neither shows on a line either: when the daily total resets, and when it starts.
	moved := card("rae-refund", "rae")
	moved["location"] = "America/New_York"
	if got := unchanged(moved); got != nil {
		t.Fatalf("a moved time zone: unchanged = %v", got)
	}
	later := card("rae-refund", "rae")
	later["valid_from"] = r.now.Add(48 * time.Hour).Format(time.RFC3339)
	if got := unchanged(later); got != nil {
		t.Fatalf("a later start: unchanged = %v", got)
	}
}

// A version's draft that went stale (a Card it revises moved on) is
// submitted again as a new draft, checked against the Cards now, and waits.
func TestAgentDraftRedraftsAStaleRevision(t *testing.T) {
	r := newDraftRig(t)
	r.startRae()
	_, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{cardMax("rae-refund", "rae", 30000)}})
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/mandates", cardMax("rae-refund", "rae", 5000)); rr.Code != http.StatusOK && rr.Code != http.StatusCreated {
		t.Fatalf("the owner's own change: %d", rr.Code)
	}
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+d["id"].(string)+"/confirm", map[string]string{"version": d["version"].(string)}); rr.Code != http.StatusConflict {
		t.Fatalf("a stale draft confirmed: %d", rr.Code)
	}
	rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+d["id"].(string)+"/redraft", nil)
	var again map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &again)
	if rr.Code != http.StatusCreated || again["status"] != "draft" || again["id"] == d["id"] {
		t.Fatalf("redraft: %d %v", rr.Code, again)
	}
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+again["id"].(string)+"/confirm", map[string]string{"version": again["version"].(string)}); rr.Code != http.StatusOK {
		t.Fatalf("confirm the redraft: %d %s", rr.Code, rr.Body.String())
	}
	if r.perActionMax("rae-refund") != 30000 {
		t.Fatalf("after the redraft: max %d", r.perActionMax("rae-refund"))
	}
	// Only a revision draft is drafted again: not one that starts an agent.
	_, first := r.submit(admin(), map[string]any{"agent_id": "bo", "cards": []any{card("bo-refund", "bo")}})
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+first["id"].(string)+"/redraft", nil); rr.Code != http.StatusConflict {
		t.Fatalf("redraft of a new agent's draft: %d", rr.Code)
	}
}

// gatewayListed lists its one domain as the gateway's.
type gatewayListed struct{ stubDomainSource }

func (s gatewayListed) List(ctx context.Context, tenantID string) ([]domain.TenantDomain, error) {
	td, _ := s.Load(ctx, tenantID)
	td.DomainID, td.Slug = "gateway", "gateway"
	return []domain.TenantDomain{td}, nil
}
