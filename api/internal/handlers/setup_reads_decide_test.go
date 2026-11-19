package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/mandate"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
)

// The gateway's contract as gateway.Contract writes it for a read-only tool
// and a tool that writes: both to gateway_agent, the read to setup_reader.
const setupReaderGatewayDomain = `domain: gateway
entity: ToolCall
events: [TOOL_CALL_REQUESTED]
states: [requested]
transitions:
  - on: TOOL_CALL_REQUESTED
    from: ""
    to: requested
actions:
  - name: list_accounts
    allowed_states: [requested]
    required_permissions: [tools.list_accounts]
    risk: low
    approval: never
    executor: cloud.proxy
    requires_authority: true
  - name: create_refund
    allowed_states: [requested]
    required_permissions: [tools.create_refund]
    risk: medium
    approval: never
    executor: cloud.proxy
    requires_authority: true
permissions:
  roles:
    gateway_agent: [tools.list_accounts, tools.create_refund]
    setup_reader: [tools.list_accounts]
`

// Invariant: Setup's key (setup_reader) reads a read-only tool only on its
// own read Card, and can't call a tool that writes, whatever Card names it.
// Category: authority
// Attack: Setup, which holds no tools, calls a tool that writes, or reads without the owner's approval of its reads
// Refused: a write is refused for the role; a read with no Card of Setup's own is refused as not issued
func TestSetupReadsOnlyWhatItsCardAllows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-setup-reads", Status: tenant.StatusActive}
	if err := tenants.Create(ctx, tnt); err != nil {
		t.Fatal(err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, []byte(setupReaderGatewayDomain)); err != nil {
		t.Fatal(err)
	}
	rt := cloudruntime.New(cloudruntime.NewMemoryResolver(tenants, src, executors.DefaultRegistry()))
	store := mandate.NewMemoryStore()
	h := handlers.NewProposalsHandler(rt, handlers.WithProposalsMandates(mandate.NewAuthorizer(store, nil, nil)))
	mux := http.NewServeMux()
	h.Register(mux)
	setup := auth.Identity{Tenant: tnt.ID, Subject: "key-s", Method: auth.MethodAPIKey, APIKey: true,
		Roles: []string{auth.RoleSetupReader}, AgentID: "setup"}
	call := func(action, id string) (int, []string, string) {
		t.Helper()
		resolved, err := rt.For(ctx, tnt.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = resolved.IngestRaw(ctx, kiffadapter.RawInput{ID: "evt-" + id, Adapter: "cloud", Type: "TOOL_CALL_REQUESTED",
			Source: "kiff-gateway", ReceivedAt: time.Now().UTC(), EntityID: id, EntityType: "ToolCall", ActorID: "setup", Payload: map[string]any{}})
		rec := drivePost(t, withFakeIdentity(mux, setup), "/v1/proposals/decide", map[string]any{"id": id, "entity_id": id,
			"entity_type": "ToolCall", "action_name": action, "actor_id": "setup", "parameters": map[string]any{}})
		var d struct {
			Outcome string   `json:"outcome"`
			Reasons []string `json:"reasons"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &d)
		return rec.Code, d.Reasons, d.Outcome
	}

	// No Card of its own yet: not even a read.
	if _, _, outcome := call("list_accounts", "tc-1"); outcome == "allowed" {
		t.Fatal("Setup read with no Card of its own")
	}
	// Setup's read Card, as POST /v1/me/setup-reads issues it.
	if err := store.Issue(ctx, mandate.Mandate{ID: handlers.SetupReadsCardID("grt_1"), TenantID: tnt.ID, Subject: "setup", Domain: "gateway",
		OnExceed: mandate.OnExceedRefuse, ValidFrom: time.Now().Add(-time.Hour),
		Grants:     []mandate.Grant{{Action: "list_accounts"}, {Action: "create_refund"}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 2, Window: mandate.WindowCalendarDay}}}, "planque"); err != nil {
		t.Fatal(err)
	}
	if _, reasons, outcome := call("list_accounts", "tc-2"); outcome != "allowed" {
		t.Fatalf("Setup's read on its Card: %s %v", outcome, reasons)
	}
	// A tool that writes stays out of reach, even on a Card that names it.
	if _, _, outcome := call("create_refund", "tc-3"); outcome == "allowed" {
		t.Fatal("Setup's key called a tool that writes")
	}
	// Past its reads for the day, refused.
	call("list_accounts", "tc-4")
	if _, _, outcome := call("list_accounts", "tc-5"); outcome == "allowed" {
		t.Fatal("a read past the Card's limit")
	}
}

// The same contract with the account's own control server connected
// (kiff/tafs#206): its tool that writes goes to setup_reader too.
var controlGatewayDomain = strings.Replace(strings.Replace(setupReaderGatewayDomain,
	"\npermissions:", `
  - name: taf_task
    allowed_states: [requested]
    required_permissions: [tools.taf_task]
    risk: medium
    approval: never
    executor: cloud.proxy
    requires_authority: true
permissions:`, 1),
	"    gateway_agent: [tools.list_accounts, tools.create_refund]\n    setup_reader: [tools.list_accounts]\n",
	"    gateway_agent: [tools.list_accounts, tools.create_refund, tools.taf_task]\n    setup_reader: [tools.list_accounts, tools.taf_task]\n", 1)

// Invariant: Setup's key acts on the account's own control server only on a
// Card of its own (Coach's), and still never on a vendor's tool that writes.
// Category: authority
// Attack: Setup calls the control server's tool with no Card, or a vendor's write once the control server is connected
// Refused: no Card of Setup's own is refused as not issued; the vendor's write is refused for the role
func TestSetupActsOnTheControlServerOnlyOnItsCard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-setup-control", Status: tenant.StatusActive}
	if err := tenants.Create(ctx, tnt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(controlGatewayDomain, "setup_reader: [tools.list_accounts, tools.taf_task]") {
		t.Fatalf("fixture: %s", controlGatewayDomain)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, []byte(controlGatewayDomain)); err != nil {
		t.Fatal(err)
	}
	rt := cloudruntime.New(cloudruntime.NewMemoryResolver(tenants, src, executors.DefaultRegistry()))
	store := mandate.NewMemoryStore()
	h := handlers.NewProposalsHandler(rt, handlers.WithProposalsMandates(mandate.NewAuthorizer(store, nil, nil)))
	mux := http.NewServeMux()
	h.Register(mux)
	setup := auth.Identity{Tenant: tnt.ID, Subject: "key-s", Method: auth.MethodAPIKey, APIKey: true,
		Roles: []string{auth.RoleSetupReader}, AgentID: "setup"}
	call := func(action, id string) string {
		t.Helper()
		resolved, err := rt.For(ctx, tnt.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = resolved.IngestRaw(ctx, kiffadapter.RawInput{ID: "evt-" + id, Adapter: "cloud", Type: "TOOL_CALL_REQUESTED",
			Source: "kiff-gateway", ReceivedAt: time.Now().UTC(), EntityID: id, EntityType: "ToolCall", ActorID: "setup", Payload: map[string]any{}})
		rec := drivePost(t, withFakeIdentity(mux, setup), "/v1/proposals/decide", map[string]any{"id": id, "entity_id": id,
			"entity_type": "ToolCall", "action_name": action, "actor_id": "setup", "parameters": map[string]any{}})
		var d struct {
			Outcome string `json:"outcome"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &d)
		return d.Outcome
	}

	if outcome := call("taf_task", "tc-1"); outcome == "allowed" {
		t.Fatal("Setup acted on the control server with no Card of its own")
	}
	// Coach's Card, confirmed by the owner, names both tools that write.
	if err := store.Issue(ctx, mandate.Mandate{ID: "coach-tasks", TenantID: tnt.ID, Subject: "setup", Domain: "gateway",
		OnExceed: mandate.OnExceedRefuse, ValidFrom: time.Now().Add(-time.Hour),
		Grants:     []mandate.Grant{{Action: "taf_task"}, {Action: "create_refund"}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 50, Window: mandate.WindowCalendarDay}}}, "owner"); err != nil {
		t.Fatal(err)
	}
	if outcome := call("taf_task", "tc-2"); outcome != "allowed" {
		t.Fatalf("Setup on the control server, on its Card: %s", outcome)
	}
	if outcome := call("create_refund", "tc-3"); outcome == "allowed" {
		t.Fatal("Setup's key called a vendor's tool that writes")
	}
}
