package tooloutcome_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

func TestTrustedNoEffectRequiresDurableTerminalBinding(t *testing.T) {
	ctx := context.Background()
	p, err := pgxpool.New(ctx, postgresURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	withoutGatewaySchema(t, p)
	r := tooloutcome.NewPostgres(p)
	if _, found, err := r.NoEffect(ctx, "t1", "operation"); found || err != nil {
		t.Fatal(found, err)
	}
	if _, err := p.Exec(ctx, `CREATE SCHEMA gateway; CREATE TABLE gateway.invocations (
	 key text PRIMARY KEY, tenant_id text, agent_id text, tool text, state text, tool_outcome text,
	 updated_at timestamptz, no_effect_evidence jsonb)`); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, state, outcome, operation, agent, action, contract, source string
		null, found, invalid                                             bool
	}{
		{name: "terminal", state: "forwarded", outcome: "error", operation: "terminal", agent: "agent-a", action: "refund", contract: "terminal_no_effect_v1", found: true},
		{name: "lookup", state: "forwarded", outcome: "error", operation: "lookup", agent: "agent-a", action: "refund", contract: "terminal_no_effect_v1", source: "read_only_lookup", found: true},
		{name: "untrusted-source", state: "forwarded", outcome: "error", operation: "untrusted-source", agent: "agent-a", action: "refund", contract: "terminal_no_effect_v1", source: "agent_assertion", invalid: true},
		{name: "generic", state: "forwarded", outcome: "error", null: true},
		{name: "unknown", state: "unknown", outcome: "error", operation: "unknown", agent: "agent-a", action: "refund", contract: "terminal_no_effect_v1"},
		{name: "success", state: "forwarded", outcome: "done", operation: "success", agent: "agent-a", action: "refund", contract: "terminal_no_effect_v1"},
		{name: "wrong-op", state: "forwarded", outcome: "error", operation: "other", agent: "agent-a", action: "refund", contract: "terminal_no_effect_v1", invalid: true},
		{name: "wrong-agent", state: "forwarded", outcome: "error", operation: "wrong-agent", agent: "agent-b", action: "refund", contract: "terminal_no_effect_v1", invalid: true},
		{name: "wrong-tool", state: "forwarded", outcome: "error", operation: "wrong-tool", agent: "agent-a", action: "delete", contract: "terminal_no_effect_v1", invalid: true},
		{name: "wrong-contract", state: "forwarded", outcome: "error", operation: "wrong-contract", agent: "agent-a", action: "refund", contract: "agent-assertion", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var evidence any
			if !tc.null {
				b, _ := json.Marshal(map[string]any{"operation_id": tc.operation, "agent_id": tc.agent, "action": tc.action, "contract": tc.contract, "reason": "final-refusal", "source": tc.source})
				evidence = b
			}
			if _, err := p.Exec(ctx, `INSERT INTO gateway.invocations VALUES($1,'t1','agent-a','refund',$2,$3,$4,$5)`, tc.name, tc.state, tc.outcome, at, evidence); err != nil {
				t.Fatal(err)
			}
			e, found, err := r.NoEffect(ctx, "t1", tc.name)
			if found != tc.found || (err != nil) != tc.invalid {
				t.Fatal(e, found, err)
			}
			if found && (e.ProposalID != tc.name || e.Subject != "agent-a" || e.Action != "refund" || !e.At.Equal(at)) {
				t.Fatal(e)
			}
			if found && tc.source != "" && e.Source != "gateway:terminal_no_effect_v1:read_only_lookup" {
				t.Fatal("lost source", e.Source)
			}
			if _, found, err := r.NoEffect(ctx, "t2", tc.name); found || err != nil {
				t.Fatal("cross-tenant evidence", found, err)
			}
		})
	}
}
