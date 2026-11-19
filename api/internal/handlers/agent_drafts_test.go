package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/agentdrafts"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/permit"
)

type draftRig struct {
	t       *testing.T
	store   *mandate.MemoryStore
	drafts  *agentdrafts.MemoryStore
	keys    *auth.MemoryAPIKeyStore
	members *membership.MemoryStore
	mux     *http.ServeMux
	now     time.Time
}

func newDraftRig(t *testing.T, opts ...MandatesOption) *draftRig {
	t.Helper()
	return newDraftRigOn(t, stubDomainSource{yaml: refundCentsYAML}, opts...)
}

// newDraftRigOn is newDraftRig over src, the domains its Cards are placed in.
func newDraftRigOn(t *testing.T, src domain.Source, opts ...MandatesOption) *draftRig {
	t.Helper()
	r := &draftRig{t: t, store: mandate.NewMemoryStore(), drafts: agentdrafts.NewMemoryStore(), keys: auth.NewMemoryAPIKeyStore(),
		members: membership.NewMemoryStore(), now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	r.store.SetClock(func() time.Time { return r.now })
	svc := membership.NewService(r.members)
	if _, err := svc.EnsureFounder(context.Background(), "t1", "alice"); err != nil {
		t.Fatal(err)
	}
	h := NewMandatesHandler(r.store, src, append([]MandatesOption{
		WithMandatesClock(func() time.Time { return r.now }), WithMandatesMemberships(r.members),
		WithMandatesAgentDrafts(r.drafts, r.keys)}, opts...)...)
	r.mux = http.NewServeMux()
	h.Register(r.mux)
	h.RegisterAgentDraftClaim(r.mux)
	return r
}

func card(id, subject string) map[string]any {
	return map[string]any{"id": id, "subject": subject, "domain": "gateway",
		"grants":     []map[string]any{{"action": "refund", "per_action_max": []map[string]any{{"quantity": "sum(amount_cents)", "max": 15000}}}},
		"aggregates": []map[string]any{{"quantity": "sum(amount_cents)", "limit": 200000, "window": "calendar_day"}}}
}

func (r *draftRig) submit(id auth.Identity, body any) (int, map[string]any) {
	r.t.Helper()
	rr := do(r.t, r.mux, id, http.MethodPost, "/v1/me/agent-drafts", body)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

func (r *draftRig) claim(draftID, secret string) (int, map[string]any) {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-drafts/"+draftID+"/claim", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func admin() auth.Identity {
	return auth.Identity{Subject: "alice", Tenant: "t1", Method: auth.MethodSession, MemberRole: membership.RoleAdmin,
		Roles: membership.IdentityRoles(membership.RoleAdmin)}
}

func (r *draftRig) cardActive(id string) bool {
	ms, _ := r.store.List(context.Background(), "t1")
	for _, m := range ms {
		if m.ID == id {
			return m.Active(r.now)
		}
	}
	return false
}

// An agent's key submits a draft, which grants nothing: no Card, no key,
// and the agent cannot confirm or claim it. An owner confirms the exact
// version; the claim secret then collects the agent's key, once.
func TestAgentDraftFromAnAgentNeedsAnOwner(t *testing.T) {
	r := newDraftRig(t)
	code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "label": "Rae", "cards": []any{card("rae-refund", "rae")}})
	secret, _ := d["claim_secret"].(string)
	if code != http.StatusCreated || d["status"] != "draft" || !strings.HasPrefix(secret, "kiff_claim_") {
		t.Fatalf("submit: %d %v", code, d)
	}
	id, version := d["id"].(string), d["version"].(string)
	if r.cardActive("rae-refund") {
		t.Fatal("a draft issued a Card")
	}
	if code, _ := r.claim(id, secret); code != http.StatusConflict {
		t.Errorf("claim before confirmation: %d", code)
	}
	if rr := do(t, r.mux, agentKey("setup-agent", ""), http.MethodPost, "/v1/me/agent-drafts/"+id+"/confirm", map[string]string{"version": version}); rr.Code != http.StatusForbidden {
		t.Errorf("an agent confirmed a draft: %d", rr.Code)
	}
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+id+"/confirm", map[string]string{"version": "stale"}); rr.Code != http.StatusConflict {
		t.Errorf("a stale version confirmed: %d", rr.Code)
	}
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+id+"/confirm", map[string]string{"version": version}); rr.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", rr.Code, rr.Body.String())
	}
	if !r.cardActive("rae-refund") {
		t.Fatal("confirmation did not issue the Card")
	}
	if code, _ := r.claim(id, "kiff_claim_"+strings.Repeat("0", 64)); code != http.StatusNotFound {
		t.Errorf("a wrong secret: %d", code)
	}
	code, got := r.claim(id, secret)
	plaintext, _ := got["plaintext"].(string)
	if code != http.StatusOK || got["agent_id"] != "rae" || plaintext == "" {
		t.Fatalf("claim: %d %v", code, got)
	}
	parsed, err := auth.ParseAPIKey(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := r.keys.GetByHash(context.Background(), parsed.Hash)
	if err != nil || rec.AgentID != "rae" || len(rec.Roles) != 1 || rec.Roles[0] != auth.RoleGatewayAgent || rec.Tenant != "t1" {
		t.Fatalf("minted key = %+v, %v", rec, err)
	}
	if code, _ := r.claim(id, secret); code != http.StatusConflict {
		t.Errorf("a second claim: %d", code)
	}
}

// An owner's own draft is confirmed by submitting it; a draft can't start
// an agent that already holds Cards, or give a Card to anyone else.
func TestAgentDraftFromAnOwnerAndItsLimits(t *testing.T) {
	r := newDraftRig(t)
	code, d := r.submit(admin(), map[string]any{"agent_id": "rae", "label": "Rae", "cards": []any{card("rae-refund", "rae")}})
	if code != http.StatusCreated || d["status"] != "confirmed" || !r.cardActive("rae-refund") {
		t.Fatalf("owner submit: %d %v", code, d)
	}
	if code, _ := r.claim(d["id"].(string), d["claim_secret"].(string)); code != http.StatusOK {
		t.Fatalf("claim: %d", code)
	}
	// For an agent that holds Cards, a draft is a revision: it adds or
	// revises Cards, and gives no key (kiff/tafs#107).
	if code, rev := r.submit(admin(), map[string]any{"agent_id": "rae", "cards": []any{card("rae-two", "rae")}}); code != http.StatusCreated ||
		rev["status"] != "confirmed" || rev["claim_secret"] != nil || !r.cardActive("rae-two") {
		t.Errorf("a revision draft for an agent with Cards: %d %v", code, rev)
	}
	if code, _ := r.submit(admin(), map[string]any{"agent_id": "bo", "cards": []any{card("bo-refund", "someone-else")}}); code != http.StatusBadRequest {
		t.Errorf("a Card for another holder: %d", code)
	}
	if code, _ := r.submit(admin(), map[string]any{"agent_id": "bo", "cards": []any{}}); code != http.StatusBadRequest {
		t.Errorf("a draft with no Cards: %d", code)
	}
}

// A draft's tool that moves an amount is limited on that amount: a Card
// that only counts its calls is refused, whoever submits it (kiff/tafs#138:
// Setup put refund_order on a taf's Card with "20 calls a day" alone). A
// limit per call or over a total is enough.
func TestAgentDraftLimitsAnAmountNotOnlyCalls(t *testing.T) {
	r := newDraftRig(t)
	counted := map[string]any{"id": "rae-refund", "subject": "rae", "domain": "gateway",
		"grants":     []map[string]any{{"action": "refund"}},
		"aggregates": []map[string]any{{"quantity": "count", "limit": 20, "window": "calendar_day"}}}
	for _, who := range []auth.Identity{agentKey("setup-agent", ""), admin()} {
		code, out := r.submit(who, map[string]any{"agent_id": "rae", "label": "Rae", "cards": []any{counted}})
		if code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "refund(amount_cents)") {
			t.Fatalf("calls alone, by %s: %d %v", who.Subject, code, out)
		}
	}
	perCall := map[string]any{"id": "rae-per-call", "subject": "rae", "domain": "gateway",
		"grants":     []map[string]any{{"action": "refund", "per_action_max": []map[string]any{{"quantity": "sum(AMOUNT_CENTS)", "max": 5000}}}},
		"aggregates": []map[string]any{{"quantity": "count", "limit": 20, "window": "calendar_day"}}}
	total := map[string]any{"id": "rae-total", "subject": "rae", "domain": "gateway",
		"grants":     []map[string]any{{"action": "refund"}},
		"aggregates": []map[string]any{{"quantity": "sum(amount_cents)", "limit": 10000, "window": "calendar_day"}}}
	for _, c := range []map[string]any{perCall, total} {
		if code, out := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "label": "Rae", "cards": []any{c}}); code != http.StatusCreated {
			t.Errorf("%s: %d %v", c["id"], code, out)
		}
	}
}

// What counts as an amount is what KIFF knows explicitly:
// a read's required integer (a limit) isn't one, and a call count is
// enough for it; a permit target's amount argument is one, required in its
// contract or not.
func TestAgentDraftKnowsAnAmountFromItsTarget(t *testing.T) {
	targets := permit.NewMemoryTargets()
	if err := targets.Replace(context.Background(), "t1", []permit.Target{{Domain: "shop", Action: "refund_order", AmountArg: "amount"}}, "alice", time.Now()); err != nil {
		t.Fatal(err)
	}
	r := newDraftRigOn(t, stubDomainSource{yaml: shopYAML}, WithMandatesPermitTargets(targets))
	counted := func(id, action string) map[string]any {
		return map[string]any{"id": id, "subject": "rae", "domain": "shop", "grants": []map[string]any{{"action": action}},
			"aggregates": []map[string]any{{"quantity": "count", "limit": 20, "window": "calendar_day"}}}
	}
	if code, out := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "label": "Rae", "cards": []any{counted("rae-list", "list_orders")}}); code != http.StatusCreated {
		t.Errorf("a read's required limit, counted: %d %v", code, out)
	}
	code, out := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "label": "Rae", "cards": []any{counted("rae-orders", "refund_order")}})
	if code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "refund_order(amount)") {
		t.Errorf("a target's optional amount, counted: %d %v", code, out)
	}
}

const shopYAML = `domain: shop
entity: Order
events: [ORDER_PLACED]
states: [placed]
transitions:
  - on: ORDER_PLACED
    from: ""
    to: placed
actions:
  - name: list_orders
    allowed_states: [placed]
    required_parameters: [limit]
    parameters:
      - name: limit
        type: int
        min: 1
    required_permissions: [orders.read]
    risk: low
    approval: never
    executor: cloud.proxy
  - name: refund_order
    allowed_states: [placed]
    parameters:
      - name: amount
        type: int
        min: 0
    required_permissions: [orders.refund]
    risk: medium
    approval: never
    executor: cloud.proxy
permissions:
  roles:
    shop_agent: [orders.read, orders.refund]
`

// Confirmations racing act once.
func TestAgentDraftConfirmsOnce(t *testing.T) {
	r := newDraftRig(t)
	_, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{card("rae-refund", "rae")}})
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+d["id"].(string)+"/confirm", map[string]string{"version": d["version"].(string)}).Code
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == http.StatusOK {
			ok++
		} else if c != http.StatusConflict {
			t.Errorf("confirm: %d", c)
		}
	}
	if ok != 1 {
		t.Fatalf("%d confirmations went through, want 1", ok)
	}
}

// An expired draft can't be confirmed or claimed; a discarded one takes
// its Cards with it.
func TestAgentDraftExpiresAndDiscards(t *testing.T) {
	r := newDraftRig(t)
	_, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{card("rae-refund", "rae")}})
	r.now = r.now.Add(25 * time.Hour)
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+d["id"].(string)+"/confirm", map[string]string{"version": d["version"].(string)}); rr.Code != http.StatusGone {
		t.Errorf("confirm after expiry: %d", rr.Code)
	}
	_, o := r.submit(admin(), map[string]any{"agent_id": "bo", "cards": []any{card("bo-refund", "bo")}})
	if !r.cardActive("bo-refund") {
		t.Fatal("not issued")
	}
	if rr := do(t, r.mux, admin(), http.MethodDelete, "/v1/me/agent-drafts/"+o["id"].(string), nil); rr.Code != http.StatusNoContent {
		t.Fatalf("discard: %d", rr.Code)
	}
	if r.cardActive("bo-refund") {
		t.Error("a discarded draft left its Card in force")
	}
	if code, _ := r.claim(o["id"].(string), o["claim_secret"].(string)); code != http.StatusConflict {
		t.Errorf("claim after discard: %d", code)
	}
	_, x := r.submit(admin(), map[string]any{"agent_id": "cy", "cards": []any{card("cy-refund", "cy")}})
	r.now = r.now.Add(25 * time.Hour)
	if code, _ := r.claim(x["id"].(string), x["claim_secret"].(string)); code != http.StatusGone {
		t.Errorf("claim after expiry: %d", code)
	}
}

// The person who confirmed must still be allowed to give keys when the key
// is claimed; otherwise no key is minted and the draft can be claimed
// later, once someone with authority confirms it again.
func TestAgentDraftClaimChecksTheConfirmerStillMayGiveKeys(t *testing.T) {
	r := newDraftRig(t)
	svc := membership.NewService(r.members)
	_, tok, err := svc.CreateInvite(context.Background(), "t1", membership.Actor{Subject: "alice"}, membership.InviteRequest{Email: "ed@x.io", Role: membership.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptInvite(context.Background(), tok, membership.Actor{Subject: "ed"}); err != nil {
		t.Fatal(err)
	}
	ed := admin()
	ed.Subject = "ed"
	_, d := r.submit(ed, map[string]any{"agent_id": "rae", "cards": []any{card("rae-refund", "rae")}})
	if _, err := svc.Remove(context.Background(), "t1", membership.Actor{Subject: "alice"}, "ed"); err != nil {
		t.Fatal(err)
	}
	if code, _ := r.claim(d["id"].(string), d["claim_secret"].(string)); code != http.StatusForbidden {
		t.Fatalf("claim after the confirmer left: %d", code)
	}
	if got, _ := r.drafts.Get(context.Background(), "t1", d["id"].(string)); got.Status != agentdrafts.Confirmed || got.KeyID != "" {
		t.Fatalf("draft after refused claim = %+v", got)
	}
	if keys, _ := r.keys.ListActive(context.Background(), "t1"); len(keys) != 0 {
		t.Errorf("a key was minted: %+v", keys)
	}
}

// faultyCards is a Card store whose issues and revokes can fail or wait.
type faultyCards struct {
	*mandate.MemoryStore
	mu          sync.Mutex
	issues      int
	failIssue   int           // the n-th issue fails
	holdIssue   int           // the n-th issue waits for release
	held        chan struct{} // closed when that issue is waiting
	release     chan struct{}
	revokeFails int // this many revokes fail first
	// beforeIssueAll runs as IssueAll starts, before the store's own check.
	beforeIssueAll func()
}

func (f *faultyCards) Issue(ctx context.Context, m mandate.Mandate, by string) error {
	f.mu.Lock()
	f.issues++
	n := f.issues
	f.mu.Unlock()
	if n == f.holdIssue {
		close(f.held)
		<-f.release
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if n == f.failIssue {
		return errors.New("disk full")
	}
	return f.MemoryStore.Issue(ctx, m, by)
}

// IssueAll is all or nothing, as the real stores are: every Card's fault
// applies before any is issued.
func (f *faultyCards) IssueAll(ctx context.Context, ms []mandate.Mandate, by string, expect map[string]int64, retire []string, at time.Time) error {
	return f.IssueAllCovering(ctx, ms, by, expect, retire, at, nil)
}

// IssueAllCovering takes the same faults: a confirmed draft whose Cards
// answer a currency ask issues through it.
func (f *faultyCards) IssueAllCovering(ctx context.Context, ms []mandate.Mandate, by string, expect map[string]int64, retire []string, at time.Time, covers []mandate.UnitCover) error {
	if f.beforeIssueAll != nil {
		f.beforeIssueAll()
	}
	for range ms {
		f.mu.Lock()
		f.issues++
		n := f.issues
		f.mu.Unlock()
		if n == f.holdIssue {
			close(f.held)
			<-f.release
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if n == f.failIssue {
			return errors.New("disk full")
		}
	}
	return f.MemoryStore.IssueAllCovering(ctx, ms, by, expect, retire, at, covers)
}

func (f *faultyCards) Revoke(ctx context.Context, tenantID, id string, at time.Time) error {
	f.mu.Lock()
	fail := f.revokeFails > 0
	if fail {
		f.revokeFails--
	}
	f.mu.Unlock()
	if fail {
		return errors.New("connection reset")
	}
	return f.MemoryStore.Revoke(ctx, tenantID, id, at)
}

// faultyRig is a draft rig over a faultyCards store.
func faultyRig(t *testing.T, f *faultyCards) (*draftRig, *faultyCards) {
	t.Helper()
	r := &draftRig{t: t, store: mandate.NewMemoryStore(), drafts: agentdrafts.NewMemoryStore(), keys: auth.NewMemoryAPIKeyStore(),
		members: membership.NewMemoryStore(), now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	r.store.SetClock(func() time.Time { return r.now })
	f.MemoryStore = r.store
	if f.holdIssue > 0 {
		f.held, f.release = make(chan struct{}), make(chan struct{})
	}
	h := NewMandatesHandler(f, stubDomainSource{yaml: refundCentsYAML}, WithMandatesClock(func() time.Time { return r.now }),
		WithMandatesAgentDrafts(r.drafts, r.keys))
	r.mux = http.NewServeMux()
	h.Register(r.mux)
	h.RegisterAgentDraftClaim(r.mux)
	return r, f
}

func (r *draftRig) anyCardActive() []string {
	ms, _ := r.store.List(context.Background(), "t1")
	var out []string
	for _, m := range ms {
		if m.Active(r.now) {
			out = append(out, m.ID)
		}
	}
	return out
}

func (r *draftRig) status(id string) agentdrafts.Status {
	d, _ := r.drafts.Get(context.Background(), "t1", id)
	return d.Status
}

func twoCards() map[string]any {
	return map[string]any{"agent_id": "rae", "cards": []any{card("rae-a", "rae"), card("rae-b", "rae")}}
}

// A Card that fails to issue undoes the ones before it and fails the
// draft: a partial confirmation leaves no authority behind.
func TestAgentDraftPartialFailureLeavesNothing(t *testing.T) {
	r, _ := faultyRig(t, &faultyCards{failIssue: 2})
	rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts", twoCards())
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "nothing it would grant was left in force") {
		t.Fatalf("partial failure: %d %s", rr.Code, rr.Body.String())
	}
	if active := r.anyCardActive(); len(active) != 0 {
		t.Errorf("Cards left in force: %v", active)
	}
}

// While its Cards are being issued, a draft can be neither claimed nor
// discarded: a claim mints no key, and a discard can't miss the Cards
// issued after it.
func TestAgentDraftConfirmingCantBeClaimedOrDiscarded(t *testing.T) {
	r, f := faultyRig(t, &faultyCards{holdIssue: 2})
	_, d := r.submit(agentKey("setup-agent", ""), twoCards())
	id := d["id"].(string)
	done := make(chan int)
	go func() {
		done <- do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+id+"/confirm", map[string]string{"version": d["version"].(string)}).Code
	}()
	<-f.held
	if got := r.status(id); got != agentdrafts.Confirming {
		t.Fatalf("status while issuing = %s", got)
	}
	if code, out := r.claim(id, d["claim_secret"].(string)); code != http.StatusConflict {
		t.Errorf("claim while issuing: %d %v", code, out)
	}
	if rr := do(t, r.mux, admin(), http.MethodDelete, "/v1/me/agent-drafts/"+id, nil); rr.Code != http.StatusConflict {
		t.Errorf("discard while issuing: %d", rr.Code)
	}
	close(f.release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("confirm: %d", code)
	}
	if keys, _ := r.keys.ListActive(context.Background(), "t1"); len(keys) != 0 {
		t.Errorf("a key was minted while issuing: %+v", keys)
	}
	if got := r.status(id); got != agentdrafts.Confirmed {
		t.Fatalf("status after issuing = %s", got)
	}
	// Now a discard takes every Card with it.
	if rr := do(t, r.mux, admin(), http.MethodDelete, "/v1/me/agent-drafts/"+id, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("discard: %d", rr.Code)
	}
	if active := r.anyCardActive(); len(active) != 0 {
		t.Errorf("a discarded draft left Cards in force: %v", active)
	}
}

// A caller that goes away mid-confirmation doesn't cut it short: it runs
// on its own context, and ends with every Card issued or none.
func TestAgentDraftConfirmationOutlivesItsCaller(t *testing.T) {
	r, f := faultyRig(t, &faultyCards{holdIssue: 2})
	_, d := r.submit(agentKey("setup-agent", ""), twoCards())
	id := d["id"].(string)
	ctx, cancel := context.WithCancel(auth.WithIdentity(context.Background(), admin()))
	req := httptest.NewRequest(http.MethodPost, "/v1/me/agent-drafts/"+id+"/confirm", strings.NewReader(`{"version":"`+d["version"].(string)+`"}`)).WithContext(ctx)
	done := make(chan int)
	go func() {
		rec := httptest.NewRecorder()
		r.mux.ServeHTTP(rec, req)
		done <- rec.Code
	}()
	<-f.held
	cancel()
	close(f.release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("confirm after the caller left: %d", code)
	}
	if got := r.status(id); got != agentdrafts.Confirmed {
		t.Fatalf("status = %s", got)
	}
	if active := r.anyCardActive(); len(active) != 2 {
		t.Errorf("active Cards = %v, want both", active)
	}
}

// A rollback whose revoke fails says so, leaves the draft unclaimable and
// mints no key; the sweep finishes it.
func TestAgentDraftRollbackThatFailsIsFinishedBySweep(t *testing.T) {
	r, f := faultyRig(t, &faultyCards{revokeFails: 1})
	_, d := r.submit(agentKey("setup-agent", ""), twoCards())
	id := d["id"].(string)
	ctx := context.Background()
	// A confirmation that stopped after its Cards were issued.
	if _, err := r.drafts.Confirm(ctx, "t1", id, d["version"].(string), "alice", "admin", []string{"rae-a", "rae-b"}, r.now); err != nil {
		t.Fatal(err)
	}
	if err := r.store.IssueAll(ctx, []mandate.Mandate{{ID: "rae-a", TenantID: "t1", Subject: "rae"}, {ID: "rae-b", TenantID: "t1", Subject: "rae"}}, "alice", nil, nil, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(agentdrafts.StuckAfter + time.Second)
	// The first sweep's revoke fails: it stays rolling back, and can't be claimed.
	if _, err := agentdrafts.Sweep(ctx, r.drafts, f, r.now); err == nil {
		t.Fatal("a failed revoke was not reported")
	}
	if got := r.status(id); got != agentdrafts.RollingBack {
		t.Fatalf("status = %s", got)
	}
	if code, _ := r.claim(id, d["claim_secret"].(string)); code != http.StatusConflict {
		t.Errorf("claim while rolling back: %d", code)
	}
	if keys, _ := r.keys.ListActive(ctx, "t1"); len(keys) != 0 {
		t.Errorf("a key was minted: %+v", keys)
	}
	if n, err := agentdrafts.Sweep(ctx, r.drafts, f, r.now); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if got := r.status(id); got != agentdrafts.Failed {
		t.Errorf("status after sweep = %s", got)
	}
	if active := r.anyCardActive(); len(active) != 0 {
		t.Errorf("Cards left in force: %v", active)
	}
}

// A discard whose revoke fails can be retried; the draft is never
// claimable in between.
func TestAgentDraftDiscardRetries(t *testing.T) {
	r, f := faultyRig(t, &faultyCards{})
	_, d := r.submit(admin(), twoCards())
	id := d["id"].(string)
	f.revokeFails = 1
	if rr := do(t, r.mux, admin(), http.MethodDelete, "/v1/me/agent-drafts/"+id, nil); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("discard with a failing revoke: %d", rr.Code)
	}
	if code, _ := r.claim(id, d["claim_secret"].(string)); code != http.StatusConflict {
		t.Errorf("claim while discarding: %d", code)
	}
	if rr := do(t, r.mux, admin(), http.MethodDelete, "/v1/me/agent-drafts/"+id, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("discard again: %d", rr.Code)
	}
	if got := r.status(id); got != agentdrafts.Discarded {
		t.Errorf("status = %s", got)
	}
	if active := r.anyCardActive(); len(active) != 0 {
		t.Errorf("Cards left in force: %v", active)
	}
}

// A confirmation the process never finished (it stopped mid-issue) is
// rolled back by the sweep once it is stuck, and its Cards revoked.
func TestAgentDraftSweepRollsBackAStuckConfirmation(t *testing.T) {
	r, f := faultyRig(t, &faultyCards{})
	_, d := r.submit(agentKey("setup-agent", ""), twoCards())
	id := d["id"].(string)
	ctx := context.Background()
	if _, err := r.drafts.Confirm(ctx, "t1", id, d["version"].(string), "alice", "admin", []string{"rae-a", "rae-b"}, r.now); err != nil {
		t.Fatal(err)
	}
	// It stopped after issuing the first Card.
	if err := r.store.Issue(ctx, mandate.Mandate{ID: "rae-a", TenantID: "t1", Subject: "rae"}, "alice"); err != nil {
		t.Fatal(err)
	}
	if n, _ := agentdrafts.Sweep(ctx, r.drafts, f, r.now.Add(time.Minute)); n != 0 || r.status(id) != agentdrafts.Confirming {
		t.Fatalf("a recent confirmation was swept: %d %s", n, r.status(id))
	}
	r.now = r.now.Add(agentdrafts.StuckAfter + time.Second)
	if n, err := agentdrafts.Sweep(ctx, r.drafts, f, r.now); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if got := r.status(id); got != agentdrafts.Failed {
		t.Errorf("status = %s", got)
	}
	if active := r.anyCardActive(); len(active) != 0 {
		t.Errorf("Cards left in force: %v", active)
	}
}

// kiff/agents#91: a draft for an agent that holds no tools (the setup agent)
// has no Cards, and says so. Confirming it issues nothing; its claim mints an
// agent-bound key without the gateway role, so it can call no gateway tool.
// An empty draft without no_tools, and no_tools with Cards, are refused.
func TestAgentDraftForAnAgentWithNoTools(t *testing.T) {
	r := newDraftRig(t)
	if code, _ := r.submit(admin(), map[string]any{"agent_id": "setup", "no_tools": true, "cards": []any{card("setup-refund", "setup")}}); code != http.StatusBadRequest {
		t.Errorf("no_tools with a Card: %d", code)
	}
	if code, _ := r.submit(admin(), map[string]any{"agent_id": "setup"}); code != http.StatusBadRequest {
		t.Errorf("a draft with no Cards and no no_tools: %d", code)
	}

	// From an agent's key it waits for the owner, as any draft does.
	code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "setup", "label": "Setup", "no_tools": true})
	if code != http.StatusCreated || d["status"] != "draft" || d["no_tools"] != true {
		t.Fatalf("submit: %d %v", code, d)
	}
	if cards, ok := d["cards"].([]any); !ok || len(cards) != 0 {
		t.Fatalf("cards = %v", d["cards"])
	}
	id, version, secret := d["id"].(string), d["version"].(string), d["claim_secret"].(string)
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+id+"/confirm", map[string]string{"version": version}); rr.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", rr.Code, rr.Body.String())
	}
	if held, _ := r.store.ForSubject(context.Background(), "t1", "setup"); len(held) != 0 {
		t.Fatalf("a no-tools draft issued Cards: %+v", held)
	}

	code, got := r.claim(id, secret)
	plaintext, _ := got["plaintext"].(string)
	if code != http.StatusOK || got["agent_id"] != "setup" || plaintext == "" {
		t.Fatalf("claim: %d %v", code, got)
	}
	parsed, err := auth.ParseAPIKey(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := r.keys.GetByHash(context.Background(), parsed.Hash)
	// Not the gateway's role: only setup_reader, which reaches read-only
	// tools on a Card of its own (kiff/tafs#138).
	if err != nil || rec.AgentID != "setup" || !slices.Equal(rec.Roles, []string{auth.RoleSetupReader}) || rec.Tenant != "t1" {
		t.Fatalf("minted key = %+v, %v", rec, err)
	}

	// A taf's draft still mints a gateway key.
	_, tafDraft := r.submit(admin(), map[string]any{"agent_id": "rae", "label": "Rae", "cards": []any{card("rae-refund", "rae")}})
	_, tafKey := r.claim(tafDraft["id"].(string), tafDraft["claim_secret"].(string))
	if roles, _ := tafKey["roles"].([]any); len(roles) != 1 || roles[0] != auth.RoleGatewayAgent {
		t.Fatalf("a taf's key roles = %v", tafKey["roles"])
	}
}

// A draft's amounts carry their unit and scale, as a Card's do, so
// the phone approving it can say €150.00, not 15000. Every view of the
// draft has them: the submit answer, a read, the waiting list, the confirm.
func TestAgentDraftAmountsCarryTheirUnits(t *testing.T) {
	r := newDraftRig(t)
	c := card("rae-refund", "rae")
	c["aggregates"] = append(c["aggregates"].([]map[string]any), map[string]any{"quantity": "count", "limit": 20, "window": "calendar_day"})
	code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "label": "Rae", "cards": []any{c}})
	if code != http.StatusCreated {
		t.Fatalf("submit: %d %v", code, d)
	}
	check := func(where string, v map[string]any) {
		t.Helper()
		cards, _ := v["cards"].([]any)
		if len(cards) != 1 {
			t.Fatalf("%s: cards = %v", where, v["cards"])
		}
		shown := cards[0].(map[string]any)
		per := shown["grants"].([]any)[0].(map[string]any)["per_action_max"].([]any)[0].(map[string]any)
		if per["unit"] != "EUR" || per["scale"] != float64(2) || per["max"] != float64(15000) {
			t.Errorf("%s: per call = %v, want 15000 EUR at scale 2", where, per)
		}
		aggs := shown["aggregates"].([]any)
		total, count := aggs[0].(map[string]any), aggs[1].(map[string]any)
		if total["unit"] != "EUR" || total["scale"] != float64(2) {
			t.Errorf("%s: daily total = %v, want EUR at scale 2", where, total)
		}
		if _, ok := count["unit"]; ok {
			t.Errorf("%s: a count got a unit: %v", where, count)
		}
	}
	check("submit", d)
	id := d["id"].(string)

	rr := do(t, r.mux, admin(), http.MethodGet, "/v1/me/agent-drafts/"+id, nil)
	var got map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	check("read", got)

	rr = do(t, r.mux, admin(), http.MethodGet, "/v1/me/agent-drafts", nil)
	var waiting struct{ Drafts []map[string]any }
	_ = json.Unmarshal(rr.Body.Bytes(), &waiting)
	if len(waiting.Drafts) != 1 {
		t.Fatalf("waiting: %s", rr.Body.String())
	}
	check("waiting", waiting.Drafts[0])

	rr = do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+id+"/confirm", map[string]string{"version": d["version"].(string)})
	var confirmed map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &confirmed)
	check("confirm", confirmed)
}

// A Card that names no domain has nothing to read units from: its numbers
// stay as they were sent.
func TestAgentDraftWithoutADomainStaysBare(t *testing.T) {
	r := newDraftRig(t)
	c := card("rae-refund", "rae")
	delete(c, "domain")
	code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{c}})
	if code != http.StatusCreated {
		t.Skipf("a Card without a domain isn't a valid draft here: %d %v", code, d)
	}
	shown := d["cards"].([]any)[0].(map[string]any)
	if _, ok := shown["aggregates"].([]any)[0].(map[string]any)["unit"]; ok {
		t.Errorf("a unit without a domain: %v", shown)
	}
}

// countingDomains counts reads of a domain's contracts.
type countingDomains struct {
	stubDomainSource
	reads *int
}

func (s countingDomains) LoadByDomain(ctx context.Context, tenantID, domainID string) (domain.TenantDomain, error) {
	*s.reads++
	return s.stubDomainSource.LoadByDomain(ctx, tenantID, domainID)
}

// The waiting list reads each domain's units once, however many drafts it
// shows.
func TestAgentDraftWaitingListReadsEachDomainOnce(t *testing.T) {
	reads := 0
	r := newDraftRig(t)
	h := NewMandatesHandler(r.store, countingDomains{stubDomainSource{yaml: refundCentsYAML}, &reads},
		WithMandatesClock(func() time.Time { return r.now }), WithMandatesMemberships(r.members), WithMandatesAgentDrafts(r.drafts, r.keys))
	r.mux = http.NewServeMux()
	h.Register(r.mux)
	for _, agent := range []string{"rae", "bo", "cy"} {
		if code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": agent, "cards": []any{card(agent+"-refund", agent)}}); code != http.StatusCreated {
			t.Fatalf("submit %s: %d %v", agent, code, d)
		}
	}
	reads = 0
	rr := do(t, r.mux, admin(), http.MethodGet, "/v1/me/agent-drafts", nil)
	var waiting struct{ Drafts []map[string]any }
	_ = json.Unmarshal(rr.Body.Bytes(), &waiting)
	if len(waiting.Drafts) != 3 {
		t.Fatalf("waiting: %s", rr.Body.String())
	}
	if reads != 1 {
		t.Errorf("the list read the gateway domain %d times, want once", reads)
	}
}

// fakeTitles is the gateway's tool titles, by tool name.
type fakeTitles map[string]string

func (f fakeTitles) ToolTitles(context.Context, string) (map[string]string, error) { return f, nil }

// A draft's grants and the Card it issues say their tool's title, so the
// owner reads "Refund a payment", not the tool's KIFF name.
func TestAgentDraftAndItsCardSayTheToolsTitle(t *testing.T) {
	r := newDraftRig(t, WithMandatesToolTitles(fakeTitles{"refund": "Refund a payment"}))
	code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "label": "Rae", "cards": []any{card("rae-refund", "rae")}})
	if code != http.StatusCreated {
		t.Fatalf("submit: %d %v", code, d)
	}
	grant := d["cards"].([]any)[0].(map[string]any)["grants"].([]any)[0].(map[string]any)
	if grant["title"] != "Refund a payment" || grant["action"] != "refund" {
		t.Fatalf("the draft's grant: %v", grant)
	}
	rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/"+d["id"].(string)+"/confirm", map[string]string{"version": d["version"].(string)})
	if rr.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", rr.Code, rr.Body.String())
	}
	rr = do(t, r.mux, admin(), http.MethodGet, "/v1/me/mandates", nil)
	var listed struct{ Mandates []map[string]any }
	_ = json.Unmarshal(rr.Body.Bytes(), &listed)
	for _, m := range listed.Mandates {
		if m["id"] != "rae-refund" {
			continue
		}
		g := m["grants"].([]any)[0].(map[string]any)
		if g["title"] != "Refund a payment" {
			t.Fatalf("the Card's grant: %v", g)
		}
		return
	}
	t.Fatalf("the Card isn't listed: %s", rr.Body.String())
}
