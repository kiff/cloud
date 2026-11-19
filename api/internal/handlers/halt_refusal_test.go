package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/halts"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/tenant"
)

// Invariant: while the owner has halted an agent, or its whole account,
// KIFF decides none of its calls: a new call, a retry of one decided before
// and a held call asked again are all refused (halted), so nothing is sent
// and no permit is issued. Once resumed, only new calls go through: what was
// asked before the halt stays refused.
// Category: authority
// Attack: an agent keeps calling after the owner halts it, retries an operation allowed before the halt, or waits out the halt to collect a held call the owner had approved
// Refused: every decision for a halted agent or account is blocked with halted; after a resume, an operation first decided before the halt is blocked with halted too
func TestAHaltRefusesEveryCallAndNothingFromBeforeComesBack(t *testing.T) {
	t.Parallel()
	store := halts.NewMemoryStore()
	r := newSentTrialRig(t, handlers.WithProposalsHalts(store))
	agent := gatewayKey("refund-agent", "")
	ctx := context.Background()
	if _, d := r.call(t, agent, "tc-before", 1000, ""); d.Outcome != "allowed" {
		t.Fatalf("before the halt: %+v", d)
	}
	if _, d := r.call(t, agent, "tc-held", 7000, ""); d.Outcome != "owner_required" {
		t.Fatalf("a held call before the halt: %+v", d)
	}
	// Decided before the halt, and asked again only after the resume: no
	// refusal of it is cached, so only the halt's own check refuses it.
	if _, d := r.call(t, agent, "tc-quiet", 1000, ""); d.Outcome != "allowed" {
		t.Fatalf("a second call before the halt: %+v", d)
	}
	if _, _, err := store.Halt(ctx, r.tnt, "refund-agent", time.Now(), "owner"); err != nil {
		t.Fatal(err)
	}
	for call, amount := range map[string]int{"tc-new": 100, "tc-before": 1000, "tc-held": 7000} {
		if code, d := r.call(t, agent, call, amount, ""); code != http.StatusOK && code != http.StatusForbidden || d.Outcome != "blocked" || d.Reasons[0] != "halted" {
			t.Fatalf("%s while halted: %d %+v", call, code, d)
		}
	}
	// A trial of the halted agent is halted too.
	if _, d := r.call(t, agent, "tc-trial", 100, "drf_try"); d.Outcome != "blocked" || d.Reasons[0] != "halted" {
		t.Fatalf("its trial while halted: %+v", d)
	}
	// Another agent isn't, until the account is.
	other := gatewayKey("other-agent", "")
	if _, d := r.call(t, other, "tc-other", 100, ""); d.Reasons != nil && len(d.Reasons) > 0 && d.Reasons[0] == "halted" {
		t.Fatalf("another agent was refused for this one's halt: %+v", d)
	}
	time.Sleep(2 * time.Millisecond)
	if _, _, err := store.Resume(ctx, r.tnt, "refund-agent", time.Now(), "owner"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, d := r.call(t, agent, "tc-after", 100, ""); d.Outcome != "allowed" {
		t.Fatalf("a new call after the resume: %+v", d)
	}
	for call, amount := range map[string]int{"tc-before": 1000, "tc-held": 7000, "tc-quiet": 1000} {
		if _, d := r.call(t, agent, call, amount, ""); d.Outcome != "blocked" || d.Reasons[0] != "halted" {
			t.Fatalf("%s, from before the halt, after the resume: %+v", call, d)
		}
	}
	// Halt all: every agent, whatever its own halt says.
	if _, _, err := store.Halt(ctx, r.tnt, halts.Account, time.Now(), "owner"); err != nil {
		t.Fatal(err)
	}
	if _, d := r.call(t, other, "tc-other-2", 100, ""); d.Outcome != "blocked" || d.Reasons[0] != "halted" {
		t.Fatalf("another agent while the account is halted: %+v", d)
	}
}

// Invariant: the owner's halt binds in observe mode too. Observe makes
// KIFF's decisions advisory while an account watches what enforcing would
// do; a halt is the owner stopping an agent, not a decision to watch.
// Category: authority
// Attack: an agent of an account in observe mode keeps acting after the owner halts it, because the refusal comes back advisory
// Refused: the halted refusal is labelled observe and is not advisory
func TestAHaltBindsInObserveMode(t *testing.T) {
	t.Parallel()
	store := halts.NewMemoryStore()
	r := newSentTrialRig(t, handlers.WithProposalsHalts(store))
	ctx := context.Background()
	if err := r.tenants.SetMetadata(ctx, r.tnt, tenant.MetadataKeyMode, string(tenant.ModeObserve)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Halt(ctx, r.tnt, "refund-agent", time.Now(), "owner"); err != nil {
		t.Fatal(err)
	}
	agent := gatewayKey("refund-agent", "")
	agent.Tenant = r.tnt
	rec := drivePost(t, r.serve(agent), "/v1/proposals/decide", map[string]any{"id": "tc-observe", "entity_id": "tc-observe",
		"entity_type": "ToolCall", "action_name": "refund_payment", "actor_id": agent.AgentID, "parameters": map[string]any{"amount": 100}})
	var d struct {
		Outcome  string   `json:"outcome"`
		Reasons  []string `json:"reasons"`
		Mode     string   `json:"mode"`
		Advisory bool     `json:"advisory"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Outcome != "blocked" || len(d.Reasons) == 0 || d.Reasons[0] != "halted" || d.Mode != "observe" || d.Advisory {
		t.Fatalf("a halted agent in observe mode: %d %+v", rec.Code, d)
	}
}
