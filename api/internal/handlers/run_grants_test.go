package handlers_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/permit"
	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
)

type alerts struct {
	mu  sync.Mutex
	got []alert.Event
}

func (a *alerts) Notify(ev alert.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, ev)
}

// grantRig is the decide path with run grants: refund-agent's Card allows
// refunds up to 5,000, and Refunds is shared with Ana (an approver); Bo
// is an approver too, and Ada the admin.
type grantRig struct {
	sentTrialRig
	key     permit.Key
	grants  *access.MemoryStore
	alerts  *alerts
	rg      *handlers.RunGrants
	mux     *http.ServeMux
	members *membership.MemoryStore
	asked   *askedBy
}

// askedBy keeps who asked for each call, as the meter would.
type askedBy struct{ got map[string]string }

func (a *askedBy) AskedBy(_ context.Context, _, key, subject string) error {
	a.got[key] = subject
	return nil
}

func newGrantRig(t *testing.T, extra ...handlers.ProposalsOption) grantRig {
	t.Helper()
	ctx := context.Background()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	key := permit.Key{ID: "k1", Priv: priv}
	members := membership.NewMemoryStore()
	grants := access.NewMemoryStore()
	agentStore := agents.NewMemoryStore()
	ah := handlers.NewAccessHandler(grants, members, agentStore, nil)
	rg := &handlers.RunGrants{Key: key, Published: []permit.PublicKey{key.Public()}, Iss: "https://api.kiff.dev", Access: ah, Now: time.Now}
	al := &alerts{}
	asked := &askedBy{got: map[string]string{}}
	r := newSentTrialRig(t, append([]handlers.ProposalsOption{handlers.WithProposalsRunGrants(rg), handlers.WithProposalsAlerter(al),
		handlers.WithProposalsCallRequesters(asked)}, extra...)...)
	if err := members.Mutate(ctx, r.tnt, func(tx membership.Tx) error {
		for s, role := range map[string]membership.Role{"ada": membership.RoleAdmin, "ana": membership.RoleApprover, "bo": membership.RoleApprover} {
			if err := tx.InsertMember(membership.Member{TenantID: r.tnt, Subject: s, Role: role, Status: membership.StatusActive}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"refund-agent", "payouts"} {
		if err := agentStore.Create(ctx, agents.Agent{TenantID: r.tnt, ID: id, Kind: agents.KindTaf, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := grants.AddGrant(ctx, access.Grant{TenantID: r.tnt, Agent: "refund-agent", Subject: "ana"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	rg.Register(mux)
	return grantRig{sentTrialRig: r, key: key, grants: grants, alerts: al, rg: rg, mux: mux, members: members, asked: asked}
}

func (r grantRig) grant(t *testing.T, agent, sub string) string {
	t.Helper()
	tok, _, err := r.key.SignRunGrant("https://api.kiff.dev", r.tnt, agent, sub, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// decide decides a call of refund-agent, with a run grant when one is given.
func (r grantRig) decide(t *testing.T, id auth.Identity, call string, amount int, grant string) (int, decided) {
	t.Helper()
	resolved, err := r.rt.For(context.Background(), r.tnt)
	if err != nil {
		t.Fatal(err)
	}
	// The gateway's TOOL_CALL_REQUESTED for the call, as call records it.
	_, _ = resolved.IngestRaw(context.Background(), kiffadapter.RawInput{ID: "evt-" + call, Adapter: "cloud", Type: "TOOL_CALL_REQUESTED",
		Source: "kiff-gateway", ReceivedAt: time.Now().UTC(), EntityID: call, EntityType: "ToolCall", ActorID: "refund-agent", Payload: map[string]any{}})
	body := map[string]any{"id": call, "entity_id": call, "entity_type": "ToolCall", "action_name": "refund_payment",
		"actor_id": "refund-agent", "parameters": map[string]any{"amount": amount}}
	if grant != "" {
		body["run_grant"] = grant
	}
	rec := drivePost(t, r.serve(id), "/v1/proposals/decide", body)
	var d decided
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	return rec.Code, d
}

var agentKey = auth.Identity{Subject: "key-refunds", Method: auth.MethodAPIKey, APIKey: true, AgentID: "refund-agent", Roles: []string{"gateway_agent"}}

// A person's run carries their run grant on each call: KIFF checks it is
// for this agent and that they may still use it, and the call and any
// hold it opens are theirs. The hold's alert names those who may answer
// it, never the person who asked; a grant that fails refuses the call.
func TestARunGrantMakesTheCallThePersons(t *testing.T) {
	r := newGrantRig(t)
	ana := r.grant(t, "refund-agent", "ana")

	// Over the Card: held, as Ana's.
	code, d := r.decide(t, agentKey, "call-held", 6000, ana)
	if code != http.StatusOK || d.Outcome != "owner_required" {
		t.Fatalf("held call: %d %+v", code, d)
	}
	held, err := r.store.Exceptions(context.Background(), r.tnt, mandate.ExceptionHeld)
	if err != nil || len(held) != 1 || held[0].Request.AskedBy != "ana" {
		t.Fatalf("the hold is Ana's: %+v %v", held, err)
	}
	// Usage by person reads who asked from here; a call
	// with no grant names no one.
	if r.asked.got["call-held"] != "ana" {
		t.Fatalf("who asked for call-held: %v", r.asked.got)
	}
	if code, _ := r.decide(t, agentKey, "call-no-grant", 100, ""); code != http.StatusOK || r.asked.got["call-no-grant"] != "" {
		t.Fatalf("a call with no grant: %d, recorded %v", code, r.asked.got)
	}
	var notify []string
	for _, ev := range r.alerts.got {
		if ev.Hold != nil && ev.Notify != nil {
			notify = *ev.Notify
		}
	}
	// Separation of duties is off: Ana may answer her own run's hold, so
	// it reaches her too.
	if !slices.Contains(notify, "bo") || !slices.Contains(notify, "ana") {
		t.Errorf("the hold's alert names %v: those who may answer, Ana among them", notify)
	}
	// With it on, the hold reaches the others only.
	if err := r.members.Mutate(context.Background(), r.tnt, func(tx membership.Tx) error {
		return tx.SetGovernance(membership.Governance{TenantID: r.tnt, SeparationOfDuties: true})
	}); err != nil {
		t.Fatal(err)
	}
	r.alerts.got = nil
	if code, d := r.decide(t, agentKey, "call-held-sod", 6000, ana); code != http.StatusOK || d.Outcome != "owner_required" {
		t.Fatalf("held call, SoD on: %d %+v", code, d)
	}
	notify = nil
	for _, ev := range r.alerts.got {
		if ev.Hold != nil && ev.Notify != nil {
			notify = *ev.Notify
		}
	}
	if !slices.Contains(notify, "bo") || slices.Contains(notify, "ana") {
		t.Errorf("SoD on: the hold's alert names %v: those who may answer, never Ana", notify)
	}
	if err := r.members.Mutate(context.Background(), r.tnt, func(tx membership.Tx) error {
		return tx.SetGovernance(membership.Governance{TenantID: r.tnt})
	}); err != nil {
		t.Fatal(err)
	}

	// Refused: a grant for another agent, a forged one, one sent by no
	// agent's key, and Ana's once Refunds isn't shared with her.
	for name, c := range map[string]struct {
		id     auth.Identity
		grant  string
		reason string
	}{
		"another agent's grant": {agentKey, r.grant(t, "payouts", "ana"), "run_grant"},
		"forged":                {agentKey, forged(ana), "run_grant"},
		"a person sends it":     {auth.Identity{Subject: "ada", Method: auth.MethodSession, MemberRole: membership.RoleAdmin}, ana, "run_grant_needs_agent_key"},
	} {
		code, d := r.decide(t, c.id, "call-"+name, 100, c.grant)
		if code != http.StatusForbidden || !slices.Contains(d.Reasons, c.reason) {
			t.Errorf("%s: %d %+v", name, code, d)
		}
	}
	if err := r.grants.RemoveGrant(context.Background(), access.Grant{TenantID: r.tnt, Agent: "refund-agent", Subject: "ana"}); err != nil {
		t.Fatal(err)
	}
	if code, d := r.decide(t, agentKey, "call-unshared", 100, ana); code != http.StatusForbidden || !slices.Contains(d.Reasons, "run_grant") {
		t.Errorf("Ana's grant once Refunds isn't shared with her: %d %+v", code, d)
	}
}

// A person asks for their own run grant for an agent they may use; the
// agent's runtime resolves it with the agent's own key, never another's.
func TestRunGrantRoutes(t *testing.T) {
	r := newGrantRig(t)
	post := func(id auth.Identity, path string, body any) (int, map[string]any) {
		t.Helper()
		id.Tenant = r.tnt
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(body)
		rec := httptest.NewRecorder()
		withFakeIdentity(r.mux, id).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, &buf))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	ana := auth.Identity{Subject: "ana", Method: auth.MethodSession, MemberRole: membership.RoleApprover}
	code, out := post(ana, "/v1/me/run-grants", map[string]string{"agent": "refund-agent"})
	if code != http.StatusOK || out["subject"] != "ana" || out["grant"] == nil {
		t.Fatalf("Ana's grant: %d %v", code, out)
	}
	grant := out["grant"].(string)
	if code, out := post(ana, "/v1/me/run-grants", map[string]string{"agent": "payouts"}); code != http.StatusForbidden || out["error"] != "no_access" {
		t.Errorf("a grant for an agent not shared with Ana: %d %v", code, out)
	}
	if code, out := post(agentKey, "/v1/me/run-grants/resolve", map[string]string{"grant": grant}); code != http.StatusOK || out["subject"] != "ana" {
		t.Errorf("resolved by the agent's key: %d %v", code, out)
	}
	other := auth.Identity{Subject: "key-payouts", Method: auth.MethodAPIKey, APIKey: true, AgentID: "payouts", Roles: []string{"gateway_agent"}}
	if code, out := post(other, "/v1/me/run-grants/resolve", map[string]string{"grant": grant}); code != http.StatusForbidden || out["error"] != "run_grant" {
		t.Errorf("resolved by another agent's key: %d %v", code, out)
	}
	if code, _ := post(agentKey, "/v1/me/run-grants", map[string]string{"agent": "refund-agent"}); code != http.StatusForbidden {
		t.Errorf("a key minted a person's grant: %d", code)
	}
}

// forged is a grant with the first character of its signature changed.
func forged(tok string) string {
	i := strings.LastIndex(tok, ".") + 1
	c := "A"
	if tok[i] == 'A' {
		c = "B"
	}
	return tok[:i] + c + tok[i+1:]
}

// The taf's words come with the call and are kept with its hold, made
// plain: the decision is the one the same call without them gets. Words
// that aren't text are dropped and the call goes on.
func TestTheTafsWordsAreKeptWithTheHold(t *testing.T) {
	r := newGrantRig(t)
	resolved, err := r.rt.For(context.Background(), r.tnt)
	if err != nil {
		t.Fatal(err)
	}
	decide := func(call string, amount int, about, reason any) decided {
		t.Helper()
		_, _ = resolved.IngestRaw(context.Background(), kiffadapter.RawInput{ID: "evt-" + call, Adapter: "cloud", Type: "TOOL_CALL_REQUESTED",
			Source: "kiff-gateway", ReceivedAt: time.Now().UTC(), EntityID: call, EntityType: "ToolCall", ActorID: "refund-agent", Payload: map[string]any{}})
		rec := drivePost(t, r.serve(agentKey), "/v1/proposals/decide", map[string]any{"id": call, "entity_id": call, "entity_type": "ToolCall",
			"action_name": "refund_payment", "actor_id": "refund-agent", "parameters": map[string]any{"amount": amount},
			"taf_about": about, "taf_reason": reason})
		var d decided
		_ = json.Unmarshal(rec.Body.Bytes(), &d)
		return d
	}
	worded := decide("call-worded", 6000, "Order 1042\n", "arrived broken ‮ok‬, write to ana@example.com")
	bare := decide("call-bare", 6000, "", "")
	if worded.Outcome != "owner_required" || bare.Outcome != worded.Outcome {
		t.Fatalf("with words %+v, without %+v", worded, bare)
	}
	held, err := r.store.Exceptions(context.Background(), r.tnt, mandate.ExceptionHeld)
	if err != nil || len(held) != 2 {
		t.Fatalf("holds: %+v %v", held, err)
	}
	for _, e := range held {
		want := mandate.Request{}
		if e.ProposalID == "call-worded" {
			want.TafAbout, want.TafReason = "Order 1042", "arrived broken ok, write to …"
		}
		if e.Request.TafAbout != want.TafAbout || e.Request.TafReason != want.TafReason {
			t.Errorf("%s keeps %q / %q", e.ProposalID, e.Request.TafAbout, e.Request.TafReason)
		}
	}

	if d := decide("call-allowed", 100, "Order 1043", "damaged box"); d.Outcome != "allowed" {
		t.Fatalf("an allowed call with words: %+v", d)
	}
	if d := decide("call-odd", 100, 1044, []string{"x"}); d.Outcome != "allowed" {
		t.Fatalf("words that aren't text: %+v", d)
	}
}
