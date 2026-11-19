package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/halts"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/receipt"
)

// planqueFake is a planque that confirms halts once it can.
type planqueFake struct {
	mu   sync.Mutex
	down bool
	seen []string
}

func (p *planqueFake) Confirm(_ context.Context, tenant, agent string, at time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.down {
		return errors.New("planque unreachable")
	}
	p.seen = append(p.seen, tenant+"/"+agent+"@"+at.Format(time.RFC3339Nano))
	return nil
}

type haltRig struct {
	mux      *http.ServeMux
	store    *halts.MemoryStore
	planque  *planqueFake
	receipts *receipt.MemoryStore
}

func newHaltRig() haltRig {
	r := haltRig{mux: http.NewServeMux(), store: halts.NewMemoryStore(), planque: &planqueFake{}, receipts: receipt.NewMemoryStore()}
	handlers.NewHaltsHandler(r.store, r.planque, r.receipts, nil).Register(r.mux)
	return r
}

type haltAnswer struct {
	State      string `json:"state"`
	Halted     bool   `json:"halted"`
	Own        bool   `json:"own"`
	Account    bool   `json:"account"`
	HaltedAt   string `json:"halted_at"`
	KIFFAck    bool   `json:"kiff_ack"`
	PlanqueAck bool   `json:"planque_ack"`
	Receipt    string `json:"receipt"`
	Error      string `json:"error"`
}

func (r haltRig) do(t *testing.T, id auth.Identity, method, path string) (int, haltAnswer) {
	t.Helper()
	var rec interface {
		Result() *http.Response
	}
	if method == http.MethodGet {
		rec = driveGet(t, withFakeIdentity(r.mux, id), path)
	} else {
		rec = drivePost(t, withFakeIdentity(r.mux, id), path, map[string]any{})
	}
	res := rec.Result()
	var a haltAnswer
	_ = json.NewDecoder(res.Body).Decode(&a)
	return res.StatusCode, a
}

func TestHaltIsHaltingUntilPlanqueConfirmsThenHalted(t *testing.T) {
	r := newHaltRig()
	r.planque.down = true
	code, a := r.do(t, haltAdmin(), http.MethodPost, "/v1/agents/refund-agent/halt")
	if code != http.StatusOK || a.State != "halting" || !a.Halted || !a.KIFFAck || a.PlanqueAck || a.Receipt == "" {
		t.Fatalf("halt with planque down: %d %+v", code, a)
	}
	if _, err := r.receipts.Get(context.Background(), "t1", a.Receipt); err != nil {
		t.Fatalf("the halt's receipt isn't kept: %v", err)
	}
	// Reading it asks planque again; once planque confirms, it's halted.
	if _, a := r.do(t, haltAdmin(), http.MethodGet, "/v1/agents/refund-agent/halt"); a.State != "halting" {
		t.Fatalf("still down: %+v", a)
	}
	r.planque.down = false
	if _, a := r.do(t, haltAdmin(), http.MethodGet, "/v1/agents/refund-agent/halt"); a.State != "halted" || !a.PlanqueAck {
		t.Fatalf("confirmed: %+v", a)
	}
	// A second halt keeps the first's time and makes no second event.
	_, again := r.do(t, haltAdmin(), http.MethodPost, "/v1/agents/refund-agent/halt")
	if again.State != "halted" || again.Receipt != "" {
		t.Fatalf("a second halt: %+v", again)
	}
	code, resumed := r.do(t, haltAdmin(), http.MethodPost, "/v1/agents/refund-agent/resume")
	if code != http.StatusOK || resumed.State != "running" || resumed.Halted || resumed.Receipt == "" || resumed.HaltedAt == "" {
		t.Fatalf("resume: %d %+v", code, resumed)
	}
	// Resuming what runs is a 200 that changes nothing.
	if code, a := r.do(t, haltAdmin(), http.MethodPost, "/v1/agents/refund-agent/resume"); code != http.StatusOK || a.State != "running" || a.Receipt != "" {
		t.Fatalf("resume of a running agent: %d %+v", code, a)
	}
	evs, _ := r.store.Events(context.Background(), "t1", 10)
	if len(evs) != 2 || evs[0].Kind != halts.KindResume || evs[1].Kind != halts.KindHalt || evs[0].Receipt == "" {
		t.Fatalf("the Record's events: %+v", evs)
	}
}

func TestHaltAllHoldsEveryAgentUntilTheAccountResumes(t *testing.T) {
	r := newHaltRig()
	if _, a := r.do(t, haltAdmin(), http.MethodPost, "/v1/halt"); a.State != "halted" || !a.Account {
		t.Fatalf("halt all: %+v", a)
	}
	// An agent no one halted on its own, made before or during the halt.
	if _, a := r.do(t, haltAdmin(), http.MethodGet, "/v1/agents/made-during/halt"); a.State != "halted" || a.Own || !a.Account {
		t.Fatalf("an agent under the account's halt: %+v", a)
	}
	// Resuming one agent doesn't release it while the account is halted.
	if _, a := r.do(t, haltAdmin(), http.MethodPost, "/v1/agents/made-during/resume"); a.State != "halted" {
		t.Fatalf("resumed alone under halt all: %+v", a)
	}
	// An agent halted on its own stays halted after resume all.
	r.do(t, haltAdmin(), http.MethodPost, "/v1/agents/own-halt/halt")
	r.do(t, haltAdmin(), http.MethodPost, "/v1/resume")
	if _, a := r.do(t, haltAdmin(), http.MethodGet, "/v1/agents/made-during/halt"); a.State != "running" {
		t.Fatalf("released with the account: %+v", a)
	}
	if _, a := r.do(t, haltAdmin(), http.MethodGet, "/v1/agents/own-halt/halt"); a.State != "halted" || !a.Own {
		t.Fatalf("an agent halted on its own after resume all: %+v", a)
	}
}

func TestOnlyAManagerHaltsAndResumesAndNoAgentKey(t *testing.T) {
	r := newHaltRig()
	agentKey := gatewayKey("refund-agent", "")
	agentKey.Tenant = "t1"
	viewer := auth.Identity{Subject: "vic", Tenant: "t1", Method: auth.MethodSession, MemberRole: membership.RoleViewer,
		Roles: membership.IdentityRoles(membership.RoleViewer)}
	for _, c := range []struct {
		name string
		id   auth.Identity
		path string
	}{
		{"a viewer halts", viewer, "/v1/agents/refund-agent/halt"},
		{"a viewer resumes", viewer, "/v1/resume"},
		{"an agent's key halts", agentKey, "/v1/agents/refund-agent/halt"},
		{"an agent's key resumes itself", agentKey, "/v1/agents/refund-agent/resume"},
	} {
		if code, _ := r.do(t, c.id, http.MethodPost, c.path); code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", c.name, code)
		}
	}
	if code, _ := r.do(t, agentKey, http.MethodGet, "/v1/halt"); code != http.StatusForbidden {
		t.Errorf("an agent's key reads the account's halt: %d", code)
	}
	// A viewer reads where things stand.
	if code, a := r.do(t, viewer, http.MethodGet, "/v1/halt"); code != http.StatusOK || a.State != "running" {
		t.Errorf("a viewer's read: %d %+v", code, a)
	}
	if h, _ := r.store.Get(context.Background(), "t1", "refund-agent"); h.On {
		t.Fatal("a refused request halted the agent")
	}
}

func haltAdmin() auth.Identity {
	return auth.Identity{Subject: "alice", Tenant: "t1", Method: auth.MethodSession, MemberRole: membership.RoleAdmin,
		Roles: membership.IdentityRoles(membership.RoleAdmin)}
}

// readBackPlanque confirms as planque does (kiff/planque#40): it reads the
// halt back from KIFF, through the handler, with the account's connector
// key, and confirms only once it sees it.
type readBackPlanque struct {
	t     *testing.T
	mux   http.Handler
	calls int
}

func (p *readBackPlanque) Confirm(_ context.Context, tenant, agent string, _ time.Time) error {
	p.calls++
	if p.calls > 10 {
		return errors.New("confirm loop")
	}
	path := "/v1/halt"
	if agent != halts.Account {
		path = "/v1/agents/" + agent + "/halt"
	}
	connector := auth.Identity{Subject: "admin-who-minted", Tenant: tenant, Method: auth.MethodAPIKey, APIKey: true, Roles: []string{auth.RoleConnector}}
	res := driveGet(p.t, withFakeIdentity(p.mux, connector), path).Result()
	var a haltAnswer
	_ = json.NewDecoder(res.Body).Decode(&a)
	if res.StatusCode != http.StatusOK || !a.Halted {
		return errors.New("planque doesn't see the halt")
	}
	return nil
}

// Invariant: planque confirms a halt by reading it back with its connector
// key, and that read never asks planque to confirm again, so one halt is one
// confirm, not a loop.
// Category: failure-modes
// Attack: a halt whose confirm re-enters itself, so the ack is never recorded and every dispatch read starts the same storm
// Refused: planque's read only answers; the halt is confirmed once and reads halted
func TestPlanquesReadBackConfirmsOnceWithoutLooping(t *testing.T) {
	mux := http.NewServeMux()
	store := halts.NewMemoryStore()
	p := &readBackPlanque{t: t, mux: mux}
	handlers.NewHaltsHandler(store, p, receipt.NewMemoryStore(), nil).Register(mux)
	r := haltRig{mux: mux, store: store}
	for _, path := range []string{"/v1/agents/refund-agent/halt", "/v1/halt"} {
		p.calls = 0
		code, a := r.do(t, haltAdmin(), http.MethodPost, path)
		if code != http.StatusOK || a.State != "halted" || !a.PlanqueAck || p.calls != 1 {
			t.Fatalf("%s: %d %+v after %d confirms, want halted after 1", path, code, a, p.calls)
		}
	}
	// planque's read of a halt still unacked doesn't confirm either.
	p.calls = 0
	if _, _, err := store.Halt(context.Background(), "t1", "other-agent", time.Now(), "owner"); err != nil {
		t.Fatal(err)
	}
	connector := auth.Identity{Subject: "admin-who-minted", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{auth.RoleConnector}}
	if _, a := r.do(t, connector, http.MethodGet, "/v1/agents/other-agent/halt"); !a.Halted || p.calls != 0 {
		t.Fatalf("planque's read: %+v, %d confirms", a, p.calls)
	}
}

// Invariant: a trial (agent~trial) reads and halts as its agent, so planque,
// which asks KIFF about a trial permit's subject, refuses a trial's calls
// under its agent's halt, and after a resume a trial permit issued before it.
// Category: authority
// Attack: a trial permit issued just before the owner halts the agent is sent through planque within its lifetime, or after the resume
// Refused: the trial's halt is its agent's, with the same halted_at
func TestATrialReadsAsItsAgentsHalt(t *testing.T) {
	r := newHaltRig()
	_, halted := r.do(t, haltAdmin(), http.MethodPost, "/v1/agents/refund-agent/halt")
	connector := auth.Identity{Subject: "admin-who-minted", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{auth.RoleConnector}}
	code, a := r.do(t, connector, http.MethodGet, "/v1/agents/refund-agent~trial/halt")
	if code != http.StatusOK || !a.Halted || !a.Own || a.HaltedAt == "" || a.HaltedAt != halted.HaltedAt {
		t.Fatalf("the trial while its agent is halted: %d %+v (agent %+v)", code, a, halted)
	}
	r.do(t, haltAdmin(), http.MethodPost, "/v1/agents/refund-agent/resume")
	if _, a := r.do(t, connector, http.MethodGet, "/v1/agents/refund-agent~trial/halt"); a.Halted || a.HaltedAt != halted.HaltedAt {
		t.Fatalf("the trial after the resume keeps the halt's time: %+v", a)
	}
	// Halting the trial halts its agent.
	r.do(t, haltAdmin(), http.MethodPost, "/v1/agents/other-agent~trial/halt")
	if h, _ := r.store.Get(context.Background(), "t1", "other-agent"); !h.On {
		t.Fatal("halting a trial didn't halt its agent")
	}
}
