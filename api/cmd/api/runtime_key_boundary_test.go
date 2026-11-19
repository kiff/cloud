package main

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	"github.com/kiff/cloud/api/internal/receipt"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/stream"
	"github.com/kiff/cloud/api/internal/support"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
	"github.com/kiff/kiff/pkg/kiff/actor"
)

const boundaryTenant = "01HZZZBNDRY000000000000000"

var pastExpiry = time.Now().Add(-time.Minute)

// boundaryRouter is the real router with in-memory stores and minted
// keys: two runtime keys (one bound to an agent, one role-less and
// unbound, as a KIFF Guard runtime's), an agent's gateway key, and an
// account key.
func boundaryRouter(t *testing.T) (http.Handler, map[string]string) {
	t.Helper()
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	if err := tenants.Create(ctx, tenant.Tenant{ID: boundaryTenant, Slug: "boundary", DisplayName: "Boundary", Status: tenant.StatusActive}); err != nil {
		t.Fatal(err)
	}
	source := domain.NewMemorySource()
	keys := auth.NewMemoryAPIKeyStore()
	plain := map[string]string{}
	for name, rec := range map[string]auth.APIKeyRecord{
		"runtime-agent":   {Roles: []string{"ops_agent"}, AgentID: "agent-a"},
		"runtime-agent-b": {Roles: []string{"ops_agent"}, AgentID: "agent-b"},
		"runtime-unbound": {Roles: []string{}},
		"gateway":         {Roles: []string{auth.RoleGatewayAgent}, AgentID: "agent-a"},
		"no-tools-agent":  {Roles: []string{}, AgentID: "setup"},
		"setup-reader":    {Roles: []string{auth.RoleSetupReader}, AgentID: "setup"},
		"account":         {Roles: []string{"admin"}},
		// A read-only account key, and the same role on an agent's key,
		// which stays that agent's runtime key.
		"read-only":    {Roles: []string{auth.RoleAccountReader}},
		"viewer-agent": {Roles: []string{auth.RoleAccountReader}, AgentID: "agent-a"},
		// Keys past their expiry: an account key and an agent's.
		"expired-account": {Roles: []string{"admin"}, ExpiresAt: &pastExpiry},
		"expired-agent":   {Roles: []string{"ops_agent"}, AgentID: "agent-a", ExpiresAt: &pastExpiry},
	} {
		gen, err := auth.NewAPIKey(boundaryTenant, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		rec.ID, rec.Tenant, rec.Subject, rec.KeyHash, rec.CreatedAt = "key-"+name, boundaryTenant, "ops", gen.Hash, time.Now()
		if err := keys.Insert(ctx, rec); err != nil {
			t.Fatal(err)
		}
		plain[name] = gen.Plaintext
	}
	// A person on the account, through the same middleware: the stub
	// provider resolves this bearer to an owner of the tenant.
	resolver := auth.NewMapResolver()
	resolver.Set("person-owner", actor.Actor{ID: "actor-owner", Type: actor.TypeHuman, DisplayName: "owner",
		Roles: []string{"tenant_owner"}}, boundaryTenant)
	plain["person"] = "person-owner"
	cards := mandate.NewMemoryStore()
	for _, m := range []mandate.Mandate{
		{ID: "card-a", Subject: "agent-a", Grants: []mandate.Grant{{Action: "refund"}}},
		{ID: "card-b", Subject: "agent-b", Grants: []mandate.Grant{{Action: "refund"}}},
		{ID: "card-shared", Subject: "agent-b", Holders: []string{"agent-b", "agent-a"}, Grants: []mandate.Grant{{Action: "refund"}}},
	} {
		m.TenantID = boundaryTenant
		if err := cards.Issue(ctx, m, "tester"); err != nil {
			t.Fatal(err)
		}
	}
	rt := cloudruntime.New(cloudruntime.NewMemoryResolver(tenants, source, executors.DefaultRegistry()))
	return newRouter(routerDeps{
		provider: auth.NewStubProvider(resolver), apiKeyStore: keys,
		walletNonceStore: auth.NewMemoryWalletNonceStore(), walletStore: auth.NewMemoryWalletStore(),
		receiptStore: receipt.NewMemoryStore(), tenantStore: tenants, domainSource: source,
		draftSource: domain.NewMemoryDraftSource(), controlStore: domain.NewMemoryControlStore(),
		supportStore: support.NewMemoryStore(), alertStore: alert.NewMemoryStore(), alertDeliveries: alert.NewMemoryDeliveryStore(),
		usageStore: usage.NewMemoryStore(), guardStore: guard.NewMemoryStore(), mandateStore: cards,
		agentStore: agents.NewMemoryStore(), broker: stream.NewBroker(),
		resolver: resolver, tenantRuntime: rt, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}), plain
}

// callAs makes one request with key and reports the status and whether
// the runtime-key boundary refused it.
func callAs(t *testing.T, h http.Handler, key, method, path string) (int, bool) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(req.Context(), 300*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	// Two boundaries refuse a key before any handler: the runtime-key
	// list, and the gateway credential's own shorter list.
	body := rec.Body.String()
	return rec.Code, rec.Code == http.StatusForbidden &&
		(strings.Contains(body, "runtime_key_forbidden") || strings.Contains(body, "reaches only the gateway's routes"))
}

// Invariant: a runtime key (an agent's key, or any key without the
// account roles) cannot read the account's activity, settings or
// people's things, nor change them: the decision stream, evidence,
// receipts, proof packs, usage, alert targets, billing budget,
// notification preferences, support tickets, the key list.
// Category: authority
// Attack: an agent's runtime key reads account-wide activity or changes account settings through GET/PUT/POST /v1/me/* routes that only check the tenant
// Refused: the route policy lets a runtime key reach only RuntimeKeyRules; every other route, including ones added later, is refused
func TestRuntimeKeysReachOnlyRuntimeRoutes(t *testing.T) {
	h, keys := boundaryRouter(t)
	refused := [][2]string{
		// Activity.
		{"GET", "/v1/me/stream"}, {"GET", "/v1/me/proof-pack/refund"}, {"GET", "/v1/receipts"},
		{"GET", "/v1/receipts/trace-1"}, {"GET", "/v1/proposals"}, {"GET", "/v1/approvals"},
		{"GET", "/v1/me/controls"}, {"GET", "/v1/me/controls/ctl-1/decisions"},
		{"GET", "/v1/me/domains/billing/evidence"}, {"GET", "/v1/me/domains/billing/activity/series"},
		{"GET", "/v1/me/review-ratio"}, {"GET", "/v1/me/usage"}, {"GET", "/v1/me/domains/billing/usage"},
		{"GET", "/v1/me/usage/credits"}, {"GET", "/v1/me/usage/credits/runs"}, {"PUT", "/v1/me/usage/limit"},
		{"GET", "/v1/me/card-exceptions"}, {"GET", "/v1/me/export"},
		// Account configuration that names agents, keys or people.
		{"GET", "/v1/me/domains"}, {"GET", "/v1/me/domains/billing"}, {"GET", "/v1/me/domains/billing/yaml"},
		{"GET", "/v1/me/domain"}, {"GET", "/v1/me/agents"}, {"GET", "/v1/keys"},
		{"GET", "/v1/guard/candidates"}, {"GET", "/v1/guard/connections"}, {"GET", "/v1/guard/coverage"},
		{"GET", "/v1/guard/runtimes"}, {"GET", "/v1/guard/observations"},
		// Settings, and their writes.
		{"GET", "/v1/me/alert-targets"}, {"POST", "/v1/me/alert-targets"}, {"DELETE", "/v1/me/alert-targets/at-1"},
		{"GET", "/v1/me/billing-budget"}, {"PUT", "/v1/me/billing-budget"},
		{"GET", "/v1/me/notification-prefs"}, {"PUT", "/v1/me/notification-prefs"},
		// A person's own things.
		{"GET", "/v1/me/support/tickets"}, {"GET", "/v1/me/support/tickets/t-1"},
		{"POST", "/v1/me/support/tickets"}, {"POST", "/v1/me/support/tickets/t-1/reply"},
		{"GET", "/v1/me/wallet"}, {"DELETE", "/v1/me/wallet"},
		// Answering holds is not an agent's act.
		{"POST", "/v1/approvals/ap-1/grant"}, {"POST", "/v1/approvals/ap-1/deny"},
	}
	for _, r := range refused {
		for _, key := range []string{"runtime-agent", "runtime-agent-b", "runtime-unbound"} {
			if code, byBoundary := callAs(t, h, keys[key], r[0], r[1]); !byBoundary {
				t.Errorf("%s %s %s: %d, want 403 runtime_key_forbidden", key, r[0], r[1], code)
			}
		}
		for _, who := range []string{"account", "person"} {
			if _, byBoundary := callAs(t, h, keys[who], r[0], r[1]); byBoundary {
				t.Errorf("%s refused on %s %s", who, r[0], r[1])
			}
		}
	}

	// What runtimes do: still reached, by the keys they belong to.
	for _, c := range []struct {
		method, path string
		allowed      []string
		refused      []string
	}{
		{"GET", "/v1/me", []string{"runtime-agent", "runtime-unbound", "gateway"}, nil},
		{"POST", "/v1/proposals/decide", []string{"runtime-agent", "runtime-unbound", "gateway"}, nil},
		{"POST", "/v1/events/raw", []string{"runtime-agent", "runtime-unbound", "gateway"}, nil},
		{"GET", "/v1/entities/inv-1/state", []string{"runtime-agent", "runtime-unbound"}, nil},
		{"POST", "/v1/entities/inv-1/actions/refund/execute", []string{"runtime-agent", "runtime-unbound"}, nil},
		{"GET", "/v1/me/card", []string{"runtime-agent", "gateway"}, []string{"runtime-unbound"}},
		{"GET", "/v1/me/mandates", []string{"runtime-agent", "runtime-agent-b"}, []string{"runtime-unbound", "gateway"}},
		// A cancelled run's runtime withdraws its held call with the key
		// it has, an agent key or a gateway key.
		{"POST", "/v1/me/holds/ex-1/withdraw", []string{"runtime-agent", "gateway"}, []string{"runtime-unbound"}},
		// A taf hands a step to its owner, reads it and names its read of the
		// vendor with a key bound to its agent ("Your step"); answering
		// stays a person's.
		{"POST", "/v1/me/handoffs", []string{"runtime-agent", "gateway"}, []string{"runtime-unbound"}},
		{"GET", "/v1/me/handoffs/ho_1", []string{"runtime-agent", "gateway"}, []string{"runtime-unbound"}},
		{"POST", "/v1/me/handoffs/ho_1/check", []string{"runtime-agent", "gateway"}, []string{"runtime-unbound"}},
		// A run's runtime reports what it used with the key it has, an agent
		// key or a taf's gateway key; one bound to no agent has no runs.
		{"PUT", "/v1/me/runs/r1/usage", []string{"runtime-agent", "gateway"}, []string{"runtime-unbound"}},
		// The same runtime asks whether a new run may start, with the same
		// keys.
		{"GET", "/v1/me/credits/allowance", []string{"runtime-agent", "gateway"}, []string{"runtime-unbound"}},
		// A removed taf's runtime archives its own agent with its key (4):
		// only a key bound to an agent; the person route stays closed to
		// every runtime key.
		{"POST", "/v1/agents/self/archive", []string{"runtime-agent", "gateway"}, []string{"runtime-unbound"}},
		{"POST", "/v1/me/agents/agent-a/archive", nil, []string{"runtime-agent", "gateway", "runtime-unbound"}},
		{"POST", "/v1/guard/connect", []string{"runtime-unbound"}, []string{"runtime-agent"}},
		{"POST", "/v1/guard/observations", []string{"runtime-unbound"}, []string{"runtime-agent"}},
		{"PUT", "/v1/me/domain/draft", []string{"runtime-unbound"}, []string{"runtime-agent"}},
	} {
		for _, key := range c.allowed {
			if code, byBoundary := callAs(t, h, keys[key], c.method, c.path); byBoundary {
				t.Errorf("%s %s %s refused by the boundary (%d)", key, c.method, c.path, code)
			}
		}
		for _, key := range c.refused {
			if code, byBoundary := callAs(t, h, keys[key], c.method, c.path); !byBoundary {
				t.Errorf("%s %s %s: %d, want 403 runtime_key_forbidden", key, c.method, c.path, code)
			}
		}
	}
}

// Setup's key is bound to Setup's agent, yet can't archive it through the
// self route: Setup acts as Coach, and only its owner removes it.
func TestSetupKeyCantArchiveSetup(t *testing.T) {
	h, keys := boundaryRouter(t)
	if code, _ := callAs(t, h, keys["setup-reader"], "POST", "/v1/agents/self/archive"); code != http.StatusForbidden {
		t.Fatalf("Setup's key on the self archive = %d, want 403", code)
	}
}

// Every route the API registers is either on the runtime-key list or
// refused to runtime keys through the real router, so a route added
// later is closed to them until someone decides otherwise.
func TestEveryOtherRouteRefusesRuntimeKeys(t *testing.T) {
	h, keys := boundaryRouter(t)
	listed := http.NewServeMux()
	for pattern := range auth.RuntimeKeyRules {
		listed.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	}
	param := regexp.MustCompile(`\{[^}]+\}`)
	for _, route := range registeredRoutes(t) {
		method, p, _ := strings.Cut(route, " ")
		path := param.ReplaceAllString(p, "x")
		if strings.HasSuffix(path, "/") {
			path += "x"
		}
		if _, pattern := listed.Handler(httptest.NewRequest(method, path, nil)); pattern != "" {
			continue
		}
		if code, byBoundary := callAs(t, h, keys["runtime-unbound"], method, path); !byBoundary && code != http.StatusUnauthorized {
			t.Errorf("runtime key reached %s (%d)", route, code)
		}
	}
}

// readOnlyRefused reports whether the read-only key's boundary refused
// the request (403 read_only_key), before any handler.
func readOnlyRefused(t *testing.T, h http.Handler, key, method, path string) (int, bool) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(req.Context(), 300*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	return rec.Code, rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "read_only_key")
}

// Invariant: A read-only account key (role viewer, bound to no agent)
// reads the account's activity, held work and stream, and changes
// nothing: every write route the API registers, including ones added
// later, refuses it before any handler, and so do reads that need more
// than reading (the export, the audit log). The same role on an agent's
// key leaves it that agent's runtime key.
// Category: authority
// Attack: A key minted to read the account writes through it (answers a hold, mints a key, changes a Card, halts, approves a device sign-in, leaves the account)
// Refused: The route policy lets a read-only key make only GET and HEAD requests that need the read capability; it holds no other capability
func TestReadOnlyAccountKeyOnlyReads(t *testing.T) {
	h, keys := boundaryRouter(t)
	key := keys["read-only"]

	for _, path := range []string{"/v1/me/card-exceptions", "/v1/me/card-exceptions?status=held"} {
		if code, refused := readOnlyRefused(t, h, key, "GET", path); refused || code != http.StatusOK {
			t.Errorf("read-only key GET %s: %d, want 200", path, code)
		}
	}
	// The other listed reads: past the boundary (what the handler then
	// answers is the handler's; this rig mounts no activity reader, and
	// the activity handler's own test reads it with this key).
	for _, path := range []string{"/v1/me/activity", "/v1/me", "/v1/me/mandates", "/v1/me/mandates/card-a/statement", "/v1/me/card-exceptions/ex-1", "/v1/me/stream"} {
		if code, refused := readOnlyRefused(t, h, key, "GET", path); refused {
			t.Errorf("read-only key refused on GET %s (%d)", path, code)
		}
	}
	// Reads not listed, or needing more than CapRead: deny by default.
	for _, path := range []string{"/v1/keys", "/v1/me/card", "/v1/me/usage", "/v1/receipts",
		"/v1/me/export", "/v1/tenant/audit", "/v1/me/approver-grants", "/v1/me/did-this-happen"} {
		if code, refused := readOnlyRefused(t, h, key, "GET", path); !refused {
			t.Errorf("read-only key GET %s: %d, want 403 read_only_key", path, code)
		}
	}

	// Every write, by name first for the account's own settings, then all of them.
	for _, r := range [][2]string{
		{"POST", "/v1/keys"}, {"DELETE", "/v1/keys/key-account"}, {"PATCH", "/v1/keys/key-account"},
		{"POST", "/v1/me/card-exceptions/ex-1/approve"}, {"POST", "/v1/me/card-exceptions/ex-1/reject"},
		{"POST", "/v1/approvals/ap-1/grant"}, {"POST", "/v1/approvals/ap-1/deny"},
		{"POST", "/v1/mandates"}, {"POST", "/v1/halt"}, {"DELETE", "/v1/halt"},
		{"PUT", "/v1/me/time-zone"}, {"POST", "/v1/auth/device/approve"}, {"POST", "/v1/tenant/leave"},
	} {
		if code, refused := readOnlyRefused(t, h, key, r[0], r[1]); !refused {
			t.Errorf("read-only key %s %s: %d, want 403 read_only_key", r[0], r[1], code)
		}
	}
	param := regexp.MustCompile(`\{[^}]+\}`)
	writes := 0
	for _, route := range registeredRoutes(t) {
		method, p, _ := strings.Cut(route, " ")
		if method == http.MethodGet || method == http.MethodHead {
			continue
		}
		writes++
		path := param.ReplaceAllString(p, "x")
		if strings.HasSuffix(path, "/") {
			path += "x"
		}
		if code, refused := readOnlyRefused(t, h, key, method, path); !refused && code != http.StatusUnauthorized {
			t.Errorf("read-only key reached %s (%d)", route, code)
		}
	}
	if writes < 50 {
		t.Fatalf("checked only %d write routes; the scan is not seeing them", writes)
	}
	// Every GET the API registers that ReadOnlyKeyRules doesn't name is
	// refused too, including one added later: deny by default.
	listed := http.NewServeMux()
	for _, pattern := range auth.ReadOnlyKeyRules {
		listed.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	}
	reads := 0
	for _, route := range registeredRoutes(t) {
		method, p, _ := strings.Cut(route, " ")
		if method != http.MethodGet {
			continue
		}
		path := param.ReplaceAllString(p, "x")
		if strings.HasSuffix(path, "/") {
			path += "x"
		}
		if _, pattern := listed.Handler(httptest.NewRequest(method, path, nil)); pattern != "" {
			continue
		}
		reads++
		if code, refused := readOnlyRefused(t, h, key, method, path); !refused && code != http.StatusUnauthorized {
			t.Errorf("read-only key reached unlisted %s (%d)", route, code)
		}
	}
	if reads < 50 {
		t.Fatalf("checked only %d unlisted reads; the scan is not seeing them", reads)
	}

	// The same role on an agent's key: still that agent's runtime key.
	if code, byBoundary := callAs(t, h, keys["viewer-agent"], "GET", "/v1/me/activity?show=all"); !byBoundary {
		t.Errorf("viewer-role agent key GET /v1/me/activity: %d, want 403 runtime_key_forbidden", code)
	}
	if code, byBoundary := callAs(t, h, keys["viewer-agent"], "GET", "/v1/me/card-exceptions"); !byBoundary {
		t.Errorf("viewer-role agent key GET /v1/me/card-exceptions: %d, want 403 runtime_key_forbidden", code)
	}
}

// Invariant: An API key past its expiry authenticates nowhere: every
// route the API registers answers 401 to it, an account key's and an
// agent's alike, as it does to a revoked key.
// Category: authority
// Attack: A leaked key that was meant to stop working keeps reading or acting after its expiry date
// Refused: authentication checks the key's expires_at against the clock before any route or handler runs
func TestExpiredKeysAuthenticateNowhere(t *testing.T) {
	h, keys := boundaryRouter(t)
	param := regexp.MustCompile(`\{[^}]+\}`)
	routes := registeredRoutes(t)
	for _, who := range []string{"expired-account", "expired-agent"} {
		for _, route := range routes {
			method, p, _ := strings.Cut(route, " ")
			path := param.ReplaceAllString(p, "x")
			if strings.HasSuffix(path, "/") {
				path += "x"
			}
			if code, _ := callAs(t, h, keys[who], method, path); code != http.StatusUnauthorized {
				t.Errorf("%s key on %s: %d, want 401", who, route, code)
			}
		}
	}
	// The same keys, unexpired, still work: the expiry is what refuses them.
	if code, _ := callAs(t, h, keys["account"], "GET", "/v1/me"); code != http.StatusOK {
		t.Fatalf("an unexpired account key on /v1/me: %d", code)
	}
}

func registeredRoutes(t *testing.T) []string {
	t.Helper()
	pattern := regexp.MustCompile(`Handle(?:Func)?\("((?:GET|POST|PUT|PATCH|DELETE) /v1[^"]*)"`)
	set := map[string]bool{}
	err := filepath.WalkDir("../../internal", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range pattern.FindAllStringSubmatch(string(src), -1) {
			set[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	if len(out) < 100 {
		t.Fatalf("found only %d routes; the scan is not seeing the handlers", len(out))
	}
	return out
}

// Invariant: through the real router, agent A's key reads only Cards
// agent A holds: not agent B's, by list or by id; a Card they share is
// visible to both. People and account keys read every Card.
// Category: authority
// Attack: agent A's runtime key lists Cards or reads agent B's Card statement by id
// Refused: the Card list and statement are scoped to the key's agent; unbound runtime keys do not reach them
func TestAgentKeysSeeOnlyTheirOwnCardsThroughTheRouter(t *testing.T) {
	h, keys := boundaryRouter(t)
	statement := map[string]map[string]int{
		"runtime-agent":   {"card-a": 200, "card-shared": 200, "card-b": 404},
		"runtime-agent-b": {"card-b": 200, "card-shared": 200, "card-a": 404},
		"account":         {"card-a": 200, "card-b": 200, "card-shared": 200},
		"read-only":       {"card-a": 200, "card-b": 200, "card-shared": 200},
		"person":          {"card-a": 200, "card-b": 200, "card-shared": 200},
	}
	for who, cards := range statement {
		for card, want := range cards {
			if code, _ := callAs(t, h, keys[who], "GET", "/v1/me/mandates/"+card+"/statement"); code != want {
				t.Errorf("%s statement %s: %d, want %d", who, card, code, want)
			}
		}
	}
	listed := func(who string) string {
		req := httptest.NewRequest("GET", "/v1/me/mandates", nil)
		req.Header.Set("Authorization", "Bearer "+keys[who])
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out struct {
			Mandates []struct {
				ID string `json:"id"`
			} `json:"mandates"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		ids := []string{}
		for _, m := range out.Mandates {
			ids = append(ids, m.ID)
		}
		sort.Strings(ids)
		return strings.Join(ids, ",")
	}
	for who, want := range map[string]string{
		"runtime-agent": "card-a,card-shared", "runtime-agent-b": "card-b,card-shared",
		"account": "card-a,card-b,card-shared", "read-only": "card-a,card-b,card-shared", "person": "card-a,card-b,card-shared",
	} {
		if got := listed(who); got != want {
			t.Errorf("%s lists %q, want %q", who, got, want)
		}
	}
	if _, byBoundary := callAs(t, h, keys["runtime-unbound"], "GET", "/v1/me/mandates/card-b/statement"); !byBoundary {
		t.Error("unbound runtime key reached a Card statement")
	}
}
