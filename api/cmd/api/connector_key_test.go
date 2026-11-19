package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/guard"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/permit"
	"github.com/kiff/cloud/api/internal/receipt"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/stream"
	"github.com/kiff/cloud/api/internal/support"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
	"github.com/kiff/kiff/pkg/kiff/actor"
)

const connectorTenant = "01HZZZCNCTR000000000000000"

// gwAction is one gateway tool as gateway.Contract writes it.
type gwAction struct {
	name     string
	requires string // "true" as the gateway writes it
	executor string // "cloud.proxy" as the gateway writes it
	noAmount bool
	readOnly bool // written low risk, and granted to setup_reader too
	control  bool // the account's own control server's: granted to setup_reader too
}

func tool(name string) gwAction {
	return gwAction{name: name, requires: "true", executor: "cloud.proxy"}
}

// gatewayYAML is the contract the gateway writes for actions
// (gateway.Contract), each as given.
func gatewayYAML(actions ...gwAction) string {
	var b strings.Builder
	b.WriteString(`domain: gateway
entity: ToolCall
events: [TOOL_CALL_REQUESTED]
states: [requested]
transitions:
  - on: TOOL_CALL_REQUESTED
    from: ""
    to: requested
actions:
`)
	var perms, reads []string
	for _, a := range actions {
		b.WriteString("  - name: " + a.name + "\n    allowed_states: [requested]\n")
		if !a.noAmount {
			b.WriteString("    required_parameters: [amount]\n    parameters:\n      - name: amount\n        type: int\n        min: 0\n")
		}
		risk := "medium"
		if a.readOnly {
			risk = "low"
		}
		if a.readOnly || a.control {
			reads = append(reads, "tools."+a.name)
		}
		b.WriteString("    required_permissions: [tools." + a.name + "]\n    risk: " + risk + "\n    approval: never\n    executor: " + a.executor +
			"\n    requires_authority: " + a.requires + "\n")
		perms = append(perms, "tools."+a.name)
	}
	b.WriteString("permissions:\n  roles:\n    gateway_agent: [" + strings.Join(perms, ", ") + "]\n")
	if len(reads) > 0 {
		b.WriteString("    setup_reader: [" + strings.Join(reads, ", ") + "]\n")
	}
	return b.String()
}

// fakeOwners is gateway.tools.created_by as the gateway commits it.
type fakeOwners struct {
	m   map[string]string
	err error
}

func (f *fakeOwners) ToolOwners(context.Context, string) (map[string]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]string{}
	for k, v := range f.m {
		out[k] = v
	}
	return out, nil
}

type connectorWorld struct {
	h       http.Handler
	keys    map[string]string
	members *membership.MemoryStore
	source  domain.Source
	targets *permit.MemoryTargets
	owners  *fakeOwners
}

// connectorRouter is the real router with memberships on: the owner is
// the account's admin and minted a connector key and an admin key.
func connectorRouter(t *testing.T) connectorWorld {
	t.Helper()
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	if err := tenants.Create(ctx, tenant.Tenant{ID: connectorTenant, Slug: "connector", DisplayName: "Connector", Status: tenant.StatusActive}); err != nil {
		t.Fatal(err)
	}
	members := membership.NewMemoryStore()
	if err := members.Mutate(ctx, connectorTenant, func(tx membership.Tx) error {
		return tx.InsertMember(membership.Member{ID: "mem-owner", TenantID: connectorTenant, Subject: "owner",
			Role: membership.RoleAdmin, Status: membership.StatusActive, CreatedAt: tx.Now(), UpdatedAt: tx.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	source := domain.NewMemorySource()
	keys := auth.NewMemoryAPIKeyStore()
	plain := map[string]string{}
	for name, rec := range map[string]auth.APIKeyRecord{
		"connector": {Roles: []string{auth.RoleConnector}},
		"account":   {Roles: []string{"admin"}},
	} {
		gen, err := auth.NewAPIKey(connectorTenant, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		rec.ID, rec.Tenant, rec.Subject, rec.KeyHash, rec.CreatedAt = "key-"+name, connectorTenant, "owner", gen.Hash, time.Now()
		if err := keys.Insert(ctx, rec); err != nil {
			t.Fatal(err)
		}
		plain[name] = gen.Plaintext
	}
	resolver := auth.NewMapResolver()
	resolver.Set("person-owner", actor.Actor{ID: "actor-owner", Type: actor.TypeHuman, DisplayName: "owner"}, connectorTenant)
	targets := permit.NewMemoryTargets()
	owners := &fakeOwners{m: map[string]string{}}
	registry := executors.DefaultRegistry()
	rt := cloudruntime.New(cloudruntime.NewMemoryResolver(tenants, source, registry))
	h := newRouter(routerDeps{
		provider: auth.NewStubProvider(resolver), apiKeyStore: keys,
		walletNonceStore: auth.NewMemoryWalletNonceStore(), walletStore: auth.NewMemoryWalletStore(),
		receiptStore: receipt.NewMemoryStore(), tenantStore: tenants, domainSource: source,
		draftSource: domain.NewMemoryDraftSource(), controlStore: domain.NewMemoryControlStore(),
		supportStore: support.NewMemoryStore(), alertStore: alert.NewMemoryStore(), alertDeliveries: alert.NewMemoryDeliveryStore(),
		usageStore: usage.NewMemoryStore(), guardStore: guard.NewMemoryStore(), mandateStore: mandate.NewMemoryStore(),
		agentStore: agents.NewMemoryStore(), broker: stream.NewBroker(), memberStore: members, permitTargets: targets,
		executorRegistry: registry, runtimeResetter: rt, gatewayOwners: owners,
		resolver: resolver, tenantRuntime: rt, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return connectorWorld{h: h, keys: plain, members: members, source: source, targets: targets, owners: owners}
}

func (c connectorWorld) call(t *testing.T, key, method, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+c.keys[key])
	req.Header.Set("Content-Type", contentType)
	ctx, cancel := context.WithTimeout(req.Context(), 300*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func (c connectorWorld) gatewayActions(t *testing.T) []string {
	t.Helper()
	ds, err := c.source.List(context.Background(), connectorTenant)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		p, err := domain.ParseAndBuild(d.YAML, executors.DefaultRegistry())
		if err == nil && p.Spec.Domain == "gateway" {
			var out []string
			for _, a := range p.Spec.Actions {
				out = append(out, a.Name)
			}
			return out
		}
	}
	return nil
}

// /v1/me names a connector key and what it may do, so planque's
// check passes for it: tools.connect, and nothing that issues Cards, mints
// keys, answers holds or runs the account.
func TestConnectorKeyMe(t *testing.T) {
	w := connectorRouter(t)
	rec := w.call(t, "connector", "GET", "/v1/me", "application/json", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/me: %d %s", rec.Code, rec.Body.String())
	}
	var me struct {
		APIKey       bool     `json:"api_key"`
		AgentID      string   `json:"agent_id"`
		Roles        []string `json:"roles"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if !me.APIKey || me.AgentID != "" || !slices.Equal(me.Roles, []string{"connector"}) ||
		!slices.Equal(me.Capabilities, []string{"read", "personal", "tools.connect"}) {
		t.Fatalf("connector /v1/me = %+v", me)
	}
}

// A connector key makes the writes a connection makes: the
// gateway's contract and its permit targets, adding a tool and removing
// its own. Every other write, and every other read, is
// refused, and the tools the owner connected stay exactly as they are.
// Category: authority
// Attack: the connector key planque keeps is used to issue a Card, mint a key, answer a hold, invite a member, change another domain, let a gateway tool run without a Card, or drop or change the owner's tools in the gateway contract
// Refused: the auth middleware keeps a connector key to its routes; the domain and permit-target handlers keep it to adding gateway tools and removing the ones gateway.tools says it connected
func TestConnectorKeyConnectsToolsAndNothingElse(t *testing.T) {
	w := connectorRouter(t)
	const yamlType = "application/x-yaml"
	jsonType := "application/json"

	// Another domain, and the owner's own gateway tool, made by the admin key.
	billing := strings.ReplaceAll(gatewayYAML(tool("refund")), "domain: gateway", "domain: billing")
	if rec := w.call(t, "account", "POST", "/v1/me/domains", yamlType, billing); rec.Code != http.StatusCreated {
		t.Fatalf("admin creates billing: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.call(t, "account", "POST", "/v1/me/domains", yamlType, gatewayYAML(tool("owner_refund"))); rec.Code != http.StatusCreated {
		t.Fatalf("admin creates the gateway domain: %d %s", rec.Code, rec.Body.String())
	}
	w.owners.m["owner_refund"] = ""

	// Connect: planque's tool is added, with its permit target. The gateway
	// writes both before its own row is committed.
	if rec := w.call(t, "connector", "PUT", "/v1/me/domains/gateway", yamlType, gatewayYAML(tool("owner_refund"), tool("planque_refund"))); rec.Code != http.StatusOK {
		t.Fatalf("connector adds a tool: %d %s", rec.Code, rec.Body.String())
	}
	target := `{"domain":"gateway","action":"planque_refund","tool":"create_refund","audience":"https://planque.example/v1/relay","amount_argument":"amount"}`
	if rec := w.call(t, "connector", "PUT", "/v1/me/permit-targets", jsonType, `{"targets":[`+target+`]}`); rec.Code != http.StatusOK {
		t.Fatalf("connector sets its tool's target: %d %s", rec.Code, rec.Body.String())
	}
	w.owners.m["planque_refund"] = "key-connector"

	other := strings.ReplaceAll(billing, "domain: billing", "domain: payouts")
	refused := []struct{ name, method, path, contentType, body string }{
		// The owner's tool, and the contract's shape.
		{"dropping the owner's tool", "PUT", "/v1/me/domains/gateway", yamlType, gatewayYAML(tool("planque_refund"))},
		{"changing the owner's tool", "PUT", "/v1/me/domains/gateway", yamlType,
			gatewayYAML(gwAction{name: "owner_refund", requires: "true", executor: "cloud.proxy", noAmount: true}, tool("planque_refund"))},
		{"changing its own tool", "PUT", "/v1/me/domains/gateway", yamlType,
			gatewayYAML(tool("owner_refund"), gwAction{name: "planque_refund", requires: "true", executor: "cloud.proxy", noAmount: true})},
		{"a gateway tool needing no Card", "PUT", "/v1/me/domains/gateway", yamlType,
			gatewayYAML(tool("owner_refund"), tool("planque_refund"), gwAction{name: "free", requires: "false", executor: "cloud.proxy"})},
		{"a built-in executor", "PUT", "/v1/me/domains/gateway", yamlType,
			gatewayYAML(tool("owner_refund"), tool("planque_refund"), gwAction{name: "paid", requires: "true", executor: "refund.issue_refund"})},
		{"another role", "PUT", "/v1/me/domains/gateway", yamlType,
			strings.Replace(gatewayYAML(tool("owner_refund"), tool("planque_refund")), "permissions:\n  roles:\n", "permissions:\n  roles:\n    ops_agent: [tools.owner_refund]\n", 1)},
		{"another shape", "PUT", "/v1/me/domains/gateway", yamlType,
			strings.Replace(gatewayYAML(tool("owner_refund"), tool("planque_refund")), "entity: ToolCall", "entity: Payout", 1)},
		// Other domains.
		{"another domain, in place", "PUT", "/v1/me/domains/billing", yamlType, billing},
		{"another domain, created", "POST", "/v1/me/domains", yamlType, other},
		{"a template", "POST", "/v1/me/domains?template=refunds", yamlType, ""},
		{"the gateway contract under another name", "PUT", "/v1/me/domains/billing", yamlType, gatewayYAML(tool("refund"))},
		// Permit targets.
		{"a target for the owner's tool", "PUT", "/v1/me/permit-targets", jsonType,
			`{"targets":[` + target + `,{"domain":"gateway","action":"owner_refund","tool":"refund","audience":"https://evil.example"}]}`},
		{"moving its own tool's target", "PUT", "/v1/me/permit-targets", jsonType,
			`{"targets":[` + strings.Replace(target, "planque.example", "evil.example", 1) + `]}`},
		{"another domain's permit target", "PUT", "/v1/me/permit-targets", jsonType,
			`{"targets":[{"domain":"billing","action":"refund","tool":"refund","audience":"https://evil.example"}]}`},
		// Everything else.
		{"the gateway's mode", "PUT", "/v1/me/domains/gateway/mode", jsonType, `{"mode":"observe"}`},
		{"deleting a domain", "DELETE", "/v1/me/domains/gateway", jsonType, ""},
		{"reading a contract", "GET", "/v1/me/domains/billing/yaml", jsonType, ""},
		{"issuing a Card", "POST", "/v1/me/mandates", jsonType, `{"subject":"rae","grants":[{"action":"planque_refund"}]}`},
		{"reading Cards", "GET", "/v1/me/mandates", jsonType, ""},
		{"minting a key", "POST", "/v1/keys", jsonType, `{"roles":["connector"]}`},
		{"listing keys", "GET", "/v1/keys", jsonType, ""},
		{"revoking a key", "DELETE", "/v1/keys/key-account", jsonType, ""},
		{"answering a hold", "POST", "/v1/me/card-exceptions/hold-1/approve-once", jsonType, "{}"},
		{"granting an approval", "POST", "/v1/approvals/ap-1/grant", jsonType, "{}"},
		{"inviting a member", "POST", "/v1/tenant/invites", jsonType, `{"role":"admin"}`},
		{"changing a member's role", "PUT", "/v1/tenant/members/owner/role", jsonType, `{"role":"viewer"}`},
		{"billing", "PUT", "/v1/me/billing-budget", jsonType, `{"cents":1}`},
		{"proposing", "POST", "/v1/proposals/decide", jsonType, "{}"},
		{"activity", "GET", "/v1/me/stream", jsonType, ""},
		{"exporting the account", "GET", "/v1/me/export", jsonType, ""},
	}
	for _, c := range refused {
		rec := w.call(t, "connector", c.method, c.path, c.contentType, c.body)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "connector_key_forbidden") {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
	if got := w.gatewayActions(t); !slices.Equal(got, []string{"owner_refund", "planque_refund"}) {
		t.Errorf("gateway actions after the refusals: %v", got)
	}
	if ts, _ := w.targets.List(context.Background(), connectorTenant); len(ts) != 1 || ts[0].Action != "planque_refund" || ts[0].Audience != "https://planque.example/v1/relay" {
		t.Errorf("targets after the refusals: %+v", ts)
	}

	// Remove: planque takes away its own tool and its target; the owner's stays.
	if rec := w.call(t, "connector", "PUT", "/v1/me/permit-targets", jsonType, `{"targets":[]}`); rec.Code != http.StatusOK {
		t.Fatalf("connector clears its tool's target: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.call(t, "connector", "PUT", "/v1/me/domains/gateway", yamlType, gatewayYAML(tool("owner_refund"))); rec.Code != http.StatusOK {
		t.Fatalf("connector removes its tool: %d %s", rec.Code, rec.Body.String())
	}
	if got := w.gatewayActions(t); !slices.Equal(got, []string{"owner_refund"}) {
		t.Errorf("gateway actions after the removal: %v", got)
	}

	// An account whose targets include another domain's keeps them.
	two := `{"targets":[` + target + `,{"domain":"billing","action":"refund","tool":"refund","audience":"https://billing.example"}]}`
	if rec := w.call(t, "account", "PUT", "/v1/me/permit-targets", jsonType, two); rec.Code != http.StatusOK {
		t.Fatalf("admin sets two domains' targets: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.call(t, "connector", "PUT", "/v1/me/permit-targets", jsonType, `{"targets":[]}`); rec.Code != http.StatusForbidden {
		t.Errorf("connector replaced another domain's targets: %d %s", rec.Code, rec.Body.String())
	}
}

// A connector key creates the gateway's domain when the account has
// none, in the gateway's shape. When who connected what can't be read, it
// can still add a tool, but it removes and re-targets none.
func TestConnectorKeyFirstToolAndUnknownOwners(t *testing.T) {
	const yamlType = "application/x-yaml"
	w := connectorRouter(t)
	if rec := w.call(t, "connector", "POST", "/v1/me/domains", yamlType, gatewayYAML(tool("planque_refund"), gwAction{name: "free", requires: "false", executor: "cloud.proxy"})); rec.Code != http.StatusForbidden {
		t.Errorf("connector created a tool needing no Card: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.call(t, "connector", "POST", "/v1/me/domains", yamlType, gatewayYAML(tool("planque_refund"))); rec.Code != http.StatusCreated {
		t.Fatalf("connector creates the gateway domain: %d %s", rec.Code, rec.Body.String())
	}
	w.owners.m["planque_refund"] = "key-connector"
	w.owners.err = errors.New("gateway.tools unreadable")
	if rec := w.call(t, "connector", "PUT", "/v1/me/domains/gateway", yamlType, gatewayYAML(tool("planque_refund"), tool("second"))); rec.Code != http.StatusOK {
		t.Errorf("connector adds a tool without owners: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.call(t, "connector", "PUT", "/v1/me/domains/gateway", yamlType, gatewayYAML(tool("second"))); rec.Code != http.StatusForbidden {
		t.Errorf("connector removed a tool when owners were unknown: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.call(t, "connector", "PUT", "/v1/me/permit-targets", "application/json",
		`{"targets":[{"domain":"gateway","action":"second","tool":"t","audience":"https://planque.example"}]}`); rec.Code != http.StatusForbidden {
		t.Errorf("connector set a target when owners were unknown: %d %s", rec.Code, rec.Body.String())
	}
}

// Revoking the connector key stops planque from registering
// anything new, and so does its minter no longer being an admin.
func TestConnectorKeyStopsWhenRevokedOrItsMinterIsNoAdmin(t *testing.T) {
	const yamlType = "application/x-yaml"
	t.Run("revoked", func(t *testing.T) {
		w := connectorRouter(t)
		if rec := w.call(t, "account", "DELETE", "/v1/keys/key-connector", "application/json", ""); rec.Code/100 != 2 {
			t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
		}
		for _, c := range [][2]string{{"GET", "/v1/me"}, {"POST", "/v1/me/domains"}} {
			if rec := w.call(t, "connector", c[0], c[1], yamlType, gatewayYAML(tool("planque_refund"))); rec.Code != http.StatusUnauthorized {
				t.Errorf("revoked key %s %s: %d %s", c[0], c[1], rec.Code, rec.Body.String())
			}
		}
	})
	t.Run("minter now an editor", func(t *testing.T) {
		w := connectorRouter(t)
		if err := w.members.Mutate(context.Background(), connectorTenant, func(tx membership.Tx) error {
			m, _, err := tx.Member("owner")
			if err != nil {
				return err
			}
			m.Role = membership.RoleEditor
			return tx.UpdateMember(m)
		}); err != nil {
			t.Fatal(err)
		}
		if rec := w.call(t, "connector", "POST", "/v1/me/domains", yamlType, gatewayYAML(tool("planque_refund"))); rec.Code/100 == 2 {
			t.Errorf("a connector key whose minter is no admin connected a tool: %d", rec.Code)
		}
		if got := w.gatewayActions(t); got != nil {
			t.Errorf("gateway actions: %v", got)
		}
	})
}

// kiff/tafs#138: the gateway's contract grants its read-only tools, and only
// those, to setup_reader too, so an agent holding no tools (Setup) reads one
// on a Card of its own. A connector key writes exactly that, and nothing
// wider.
// Category: authority
// Attack: planque's connector key grants setup_reader a tool that writes, or another role a read-only tool
// Refused: the contract a connector key writes grants setup_reader exactly the low-risk (read-only) tools
func TestConnectorKeyGrantsSetupReaderOnlyTheReads(t *testing.T) {
	const yamlType = "application/x-yaml"
	w := connectorRouter(t)
	read := gwAction{name: "planque_list", requires: "true", executor: "cloud.proxy", noAmount: true, readOnly: true}
	if rec := w.call(t, "connector", "POST", "/v1/me/domains", yamlType, gatewayYAML(tool("planque_refund"), read)); rec.Code != http.StatusCreated {
		t.Fatalf("connector connects a write and a read: %d %s", rec.Code, rec.Body.String())
	}
	w.owners.m["planque_refund"], w.owners.m["planque_list"] = "key-connector", "key-connector"
	both := gatewayYAML(tool("planque_refund"), read)
	for name, body := range map[string]string{
		"setup_reader granted a write": strings.Replace(both, "setup_reader: [tools.planque_list]", "setup_reader: [tools.planque_list, tools.planque_refund]", 1),
		"a read not granted":           strings.Replace(both, "    setup_reader: [tools.planque_list]\n", "", 1),
		"setup_reader with no reads":   strings.Replace(gatewayYAML(tool("planque_refund")), "permissions:\n  roles:\n", "permissions:\n  roles:\n    setup_reader: [tools.planque_refund]\n", 1),
	} {
		if rec := w.call(t, "connector", "PUT", "/v1/me/domains/gateway", yamlType, body); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "connector_key_forbidden") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

// The account's own control server (kiff/tafs#206), which only a person
// connects, grants setup_reader its tools that write, so Setup acts there
// as Coach on its own Cards. A connector key connecting a vendor next to it
// keeps that grant as it is: it neither drops it nor adds a write of its own.
// Category: authority
// Attack: planque's connector key, with a control server connected, grants setup_reader its own write, or drops the control server's
// Refused: a connector key changes no write's grant to setup_reader; the control server's grant stays
func TestConnectorKeyKeepsTheControlServersGrant(t *testing.T) {
	const yamlType = "application/x-yaml"
	w := connectorRouter(t)
	taf := gwAction{name: "taf_task", requires: "true", executor: "cloud.proxy", noAmount: true, control: true}
	if rec := w.call(t, "account", "POST", "/v1/me/domains", yamlType, gatewayYAML(taf)); rec.Code != http.StatusCreated {
		t.Fatalf("a person connects the control server: %d %s", rec.Code, rec.Body.String())
	}
	w.owners.m["taf_task"] = ""
	read := gwAction{name: "planque_list", requires: "true", executor: "cloud.proxy", noAmount: true, readOnly: true}
	next := gatewayYAML(taf, tool("planque_refund"), read)
	if !strings.Contains(next, "setup_reader: [tools.taf_task, tools.planque_list]") {
		t.Fatalf("fixture: %s", next)
	}
	for name, body := range map[string]string{
		"its own write to setup_reader": strings.Replace(next, "setup_reader: [tools.taf_task, tools.planque_list]", "setup_reader: [tools.taf_task, tools.planque_refund, tools.planque_list]", 1),
		"the control server's dropped":  strings.Replace(next, "setup_reader: [tools.taf_task, tools.planque_list]", "setup_reader: [tools.planque_list]", 1),
	} {
		if rec := w.call(t, "connector", "PUT", "/v1/me/domains/gateway", yamlType, body); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "connector_key_forbidden") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := w.call(t, "connector", "PUT", "/v1/me/domains/gateway", yamlType, next); rec.Code != http.StatusOK {
		t.Fatalf("the connector adds a vendor, the control server's grant kept: %d %s", rec.Code, rec.Body.String())
	}
}
