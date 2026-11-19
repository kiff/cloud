package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/candidate"
	"github.com/kiff/cloud/api/internal/guard"
)

// candidatesFor drives GET /v1/guard/candidates for a tenant (optionally
// scoped by raw query string) and decodes the response.
func candidatesFor(t *testing.T, h *GuardHandler, tenant, rawQuery string) guardCandidatesResponse {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(mux)

	path := "/v1/guard/candidates"
	if rawQuery != "" {
		path += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
		Subject: "key-1",
		Tenant:  tenant,
		Method:  auth.MethodAPIKey,
		APIKey:  true,
	}))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("candidates status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp guardCandidatesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode candidates: %v", err)
	}
	return resp
}

func seedObservation(t *testing.T, store guard.Store, tenant string, in guard.ObservationInput) {
	t.Helper()
	if _, err := store.SaveObservation(context.Background(), tenant, in); err != nil {
		t.Fatalf("seed observation: %v", err)
	}
}

func newCandidatesHandler() (*GuardHandler, *guard.MemoryStore) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	store := guard.NewMemoryStore()
	h := NewGuardHandler(store, WithGuardClock(func() time.Time { return now }))
	return h, store
}

// No observations at all → a present-but-empty runtimes list (never null),
// so a Studio consumer renders the empty state without special-casing.
func TestCandidates_NoObservations(t *testing.T) {
	h, _ := newCandidatesHandler()
	resp := candidatesFor(t, h, "tenant-a", "")
	if resp.Source != candidate.SourceRuntime {
		t.Errorf("source = %q, want %q", resp.Source, candidate.SourceRuntime)
	}
	if resp.Runtimes == nil {
		t.Fatal("runtimes is null; want present-but-empty array")
	}
	if len(resp.Runtimes) != 0 {
		t.Fatalf("runtimes = %d, want 0", len(resp.Runtimes))
	}
}

// One observation / one ToolMap-bound tool → one runtime, one grounded
// candidate whose action/entity come from the binding, with a stable
// runtime coordinate for Studio.
func TestCandidates_OneObservationOneTool(t *testing.T) {
	h, store := newCandidatesHandler()
	seedObservation(t, store, "tenant-a", guard.ObservationInput{
		Project:     "cookbook",
		Environment: "aws",
		AgentID:     "ap-agent",
		Workflow:    "duplicate-payment",
		Adapter:     "openclaw",
		Mode:        "enforce",
		Tools: []guard.ToolObservation{{
			Name:              "pay_invoice",
			Description:       "Pay an outstanding invoice",
			EntityArg:         "invoice_id",
			Action:            "PAY_INVOICE",
			EntityType:        "Invoice",
			Required:          []string{"amount_cents"},
			ParameterSchema:   json.RawMessage(`{"type":"object","properties":{"invoice_id":{"type":"string"},"amount_cents":{"type":"integer"}},"required":["amount_cents"]}`),
			ObservedCallCount: 4,
		}},
	})

	resp := candidatesFor(t, h, "tenant-a", "")
	if len(resp.Runtimes) != 1 {
		t.Fatalf("runtimes = %d, want 1", len(resp.Runtimes))
	}
	rc := resp.Runtimes[0]

	// Stable runtime coordinate.
	if rc.Runtime.AgentID != "ap-agent" || rc.Runtime.Adapter != "openclaw" ||
		rc.Runtime.Project != "cookbook" || rc.Runtime.Environment != "aws" ||
		rc.Runtime.Workflow != "duplicate-payment" || rc.Runtime.Mode != "enforce" {
		t.Fatalf("runtime coordinate mismatch: %+v", rc.Runtime)
	}

	if len(rc.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(rc.Candidates))
	}
	c := rc.Candidates[0]
	if c.Tool != "pay_invoice" || c.Action != "PAY_INVOICE" || c.EntityType != "Invoice" || c.EntityArg != "invoice_id" {
		t.Fatalf("candidate binding mismatch: %+v", c)
	}
	if c.Risk != "high" {
		t.Errorf("risk = %q, want high (pay is a money-moving verb)", c.Risk)
	}
	if c.ObservedCallCount != 4 {
		t.Errorf("observed_call_count = %d, want 4", c.ObservedCallCount)
	}
	if c.Evidence.Source != candidate.SourceRuntime || c.Evidence.Project != "cookbook" || c.Evidence.Environment != "aws" {
		t.Errorf("evidence not runtime-grounded: %+v", c.Evidence)
	}
	if !rc.Grounding.OK {
		t.Errorf("grounding failed: %+v", rc.Grounding.Checks)
	}
}

// Multiple tools → multiple candidates, ranked high-risk first.
func TestCandidates_MultipleToolsRanked(t *testing.T) {
	h, store := newCandidatesHandler()
	seedObservation(t, store, "tenant-a", guard.ObservationInput{
		Project:     "cookbook",
		Environment: "dev",
		AgentID:     "support-agent",
		Workflow:    "observe-quickstart",
		Adapter:     "agno",
		Mode:        "observe",
		Tools: []guard.ToolObservation{
			{Name: "send_email", Description: "Send an email to a customer."},
			{Name: "refund_order", Description: "Issue a refund on an order."},
		},
	})

	resp := candidatesFor(t, h, "tenant-a", "")
	if len(resp.Runtimes) != 1 {
		t.Fatalf("runtimes = %d, want 1", len(resp.Runtimes))
	}
	cands := resp.Runtimes[0].Candidates
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want 2", len(cands))
	}
	if cands[0].Tool != "refund_order" || cands[0].Risk != "high" {
		t.Errorf("top candidate = %s (%s), want refund_order (high)", cands[0].Tool, cands[0].Risk)
	}
	if !resp.Runtimes[0].Grounding.OK {
		t.Errorf("grounding failed: %+v", resp.Runtimes[0].Grounding.Checks)
	}
}

// An unbound observed tool (observe mode, no ToolMap) must still derive a
// grounded candidate: action falls back to the tool name, entity bindings
// stay empty (never fabricated), and grounding passes.
func TestCandidates_UnboundTool(t *testing.T) {
	h, store := newCandidatesHandler()
	seedObservation(t, store, "tenant-a", guard.ObservationInput{
		Project:     "cookbook",
		Environment: "dev",
		AgentID:     "support-agent",
		Workflow:    "observe-quickstart",
		Adapter:     "agno",
		Mode:        "observe",
		Tools: []guard.ToolObservation{{
			Name:        "escalate_ticket",
			Description: "Escalate a support ticket to a human.",
		}},
	})

	resp := candidatesFor(t, h, "tenant-a", "")
	c := resp.Runtimes[0].Candidates[0]
	if c.Action != "ESCALATE_TICKET" {
		t.Errorf("action = %q, want ESCALATE_TICKET (derived from tool name)", c.Action)
	}
	if c.EntityType != "" || c.EntityArg != "" {
		t.Errorf("entity bindings fabricated for unbound tool: type=%q arg=%q", c.EntityType, c.EntityArg)
	}
	if !resp.Runtimes[0].Grounding.OK {
		t.Errorf("grounding failed for unbound tool: %+v", resp.Runtimes[0].Grounding.Checks)
	}
}

// Thresholds and states must marshal to JSON null for every candidate —
// the no-invention rule, asserted on the actual wire bytes.
func TestCandidates_ThresholdsAndStatesNull(t *testing.T) {
	h, store := newCandidatesHandler()
	seedObservation(t, store, "tenant-a", guard.ObservationInput{
		Project: "cookbook", Environment: "aws", AgentID: "ap-agent",
		Workflow: "duplicate-payment", Adapter: "openclaw", Mode: "enforce",
		Tools: []guard.ToolObservation{{Name: "pay_invoice", Action: "PAY_INVOICE", EntityType: "Invoice", EntityArg: "invoice_id"}},
	})

	mux := http.NewServeMux()
	h.Register(mux)
	req := httptest.NewRequest(http.MethodGet, "/v1/guard/candidates", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{Subject: "key-1", Tenant: "tenant-a", APIKey: true}))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	var generic struct {
		Runtimes []struct {
			Candidates []map[string]json.RawMessage `json:"candidates"`
		} `json:"runtimes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &generic); err != nil {
		t.Fatalf("decode: %v", err)
	}
	c := generic.Runtimes[0].Candidates[0]
	if string(c["policy_thresholds"]) != "null" {
		t.Errorf("policy_thresholds = %s, want null", c["policy_thresholds"])
	}
	if string(c["states"]) != "null" {
		t.Errorf("states = %s, want null", c["states"])
	}
}

// Tenant scoping: a tenant only sees candidates derived from its own
// stored observations, never another tenant's.
func TestCandidates_TenantScoping(t *testing.T) {
	h, store := newCandidatesHandler()
	seedObservation(t, store, "tenant-a", guard.ObservationInput{
		Project: "cookbook", Environment: "aws", AgentID: "agent-a",
		Workflow: "wf-a", Adapter: "agno", Mode: "enforce",
		Tools: []guard.ToolObservation{{Name: "pay_invoice", Action: "PAY_INVOICE"}},
	})
	seedObservation(t, store, "tenant-b", guard.ObservationInput{
		Project: "cookbook", Environment: "aws", AgentID: "agent-b",
		Workflow: "wf-b", Adapter: "agno", Mode: "enforce",
		Tools: []guard.ToolObservation{{Name: "delete_account", Action: "DELETE_ACCOUNT"}},
	})

	respA := candidatesFor(t, h, "tenant-a", "")
	if len(respA.Runtimes) != 1 || respA.Runtimes[0].Runtime.AgentID != "agent-a" {
		t.Fatalf("tenant-a leak/miss: %+v", respA.Runtimes)
	}

	respB := candidatesFor(t, h, "tenant-b", "")
	if len(respB.Runtimes) != 1 || respB.Runtimes[0].Runtime.AgentID != "agent-b" {
		t.Fatalf("tenant-b leak/miss: %+v", respB.Runtimes)
	}
}

// Optional query params scope the result to a single runtime.
func TestCandidates_ScopedByQuery(t *testing.T) {
	h, store := newCandidatesHandler()
	for _, agent := range []string{"agent-1", "agent-2"} {
		seedObservation(t, store, "tenant-a", guard.ObservationInput{
			Project: "cookbook", Environment: "aws", AgentID: agent,
			Workflow: "wf-" + agent, Adapter: "agno", Mode: "enforce",
			Tools: []guard.ToolObservation{{Name: "pay_invoice", Action: "PAY_INVOICE"}},
		})
	}

	all := candidatesFor(t, h, "tenant-a", "")
	if len(all.Runtimes) != 2 {
		t.Fatalf("unscoped runtimes = %d, want 2", len(all.Runtimes))
	}

	scoped := candidatesFor(t, h, "tenant-a", "agent_id=agent-2")
	if len(scoped.Runtimes) != 1 || scoped.Runtimes[0].Runtime.AgentID != "agent-2" {
		t.Fatalf("scoped result mismatch: %+v", scoped.Runtimes)
	}
}

// A connected runtime that never pushed a catalog (no observation) yields
// no candidate set — it is skipped, not surfaced empty.
func TestCandidates_ConnectedButNoObservation(t *testing.T) {
	h, store := newCandidatesHandler()
	if _, err := store.Touch(context.Background(), "tenant-a", guard.TouchInput{
		Project: "cookbook", Environment: "aws", AgentID: "ap-agent",
		Workflow: "duplicate-payment", Adapter: "openclaw", Mode: "enforce",
	}); err != nil {
		t.Fatalf("touch: %v", err)
	}

	resp := candidatesFor(t, h, "tenant-a", "")
	if len(resp.Runtimes) != 0 {
		t.Fatalf("runtimes = %d, want 0 (connection without observation)", len(resp.Runtimes))
	}
}
