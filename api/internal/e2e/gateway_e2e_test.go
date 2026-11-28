// Package e2e runs KIFF's API and gateway as the binaries they ship as, on
// one Postgres, and sends real calls through them: the gateway, then the
// API's permit gate, then its decision. It's the end-to-end test every
// change to the gateway, proposals, permits or decide/evaluate carries.
// It runs with KIFF_E2E=1 and KIFF_TEST_POSTGRES_URL set (it builds both
// binaries with the Go toolchain); otherwise it's skipped.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/testdb"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// build builds one of the repository's commands into dir.
func build(t *testing.T, dir, module, cmd string, flags ...string) string {
	t.Helper()
	out := filepath.Join(dir, filepath.Base(cmd))
	c := exec.Command("go", append(append([]string{"build"}, flags...), "-o", out, "./"+cmd)...)
	c.Dir = module
	if b, err := c.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", cmd, err, b)
	}
	return out
}

// start runs a binary until the test ends, and waits for its health check.
func start(t *testing.T, bin string, env []string, health string, args ...string) {
	t.Helper()
	logf, _ := os.Create(filepath.Join(t.TempDir(), filepath.Base(bin)+".log"))
	c := exec.Command(bin, args...)
	c.Env = append(os.Environ(), env...)
	c.Stdout, c.Stderr = logf, logf
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Process.Kill()
		_, _ = c.Process.Wait()
		if t.Failed() {
			b, _ := os.ReadFile(logf.Name())
			if len(b) > 6000 {
				b = b[len(b)-6000:]
			}
			t.Logf("%s's log:\n%s", filepath.Base(bin), b)
		}
	})
	if health == "" {
		time.Sleep(300 * time.Millisecond)
		return
	}
	for i := 0; i < 100; i++ {
		if resp, err := http.Get(health); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s didn't come up at %s", filepath.Base(bin), health)
}

// call sends one JSON request and decodes the answer.
func call(t *testing.T, method, url, bearer string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, url, &buf)
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if out == nil {
		out = map[string]any{"raw": string(b)}
	}
	return resp.StatusCode, out
}

func seed() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// newTenant provisions an account as a first sign-in provisions it.
func newTenant(t *testing.T, ctx context.Context, dbURL string) (*pgxpool.Pool, string) {
	t.Helper()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := tenant.NewPostgresStore(pool)
	tenantID := tenant.NewID()
	if err := store.Create(ctx, tenant.Tenant{ID: tenantID, Slug: "e2e-" + strings.ToLower(tenantID[len(tenantID)-6:]), DisplayName: "E2E", Status: tenant.StatusProvisioning,
		Metadata: map[string]any{tenant.MetadataKeyPlan: string(tenant.PlanInternal)}}); err != nil {
		t.Fatal(err)
	}
	if err := tenant.NewProvisioner(pool, store).Provision(ctx, tenantID); err != nil {
		t.Fatal(err)
	}
	return pool, tenantID
}

// A relay's (verify-mode, permit) call and a sealed tool's call go through
// the gateway, the API's permit gate and its decision on this code, and
// both reach their tool: the relay's with a permit. The gateway's class
// parameters ride along on both, as in production.
func TestARelayCallAndASealedCallReachTheirTools(t *testing.T) {
	if os.Getenv("KIFF_E2E") != "1" {
		t.Skip("set KIFF_E2E=1 (and KIFF_TEST_POSTGRES_URL) to run the end-to-end test")
	}
	dbURL := testdb.URL(t)
	if dbURL == "" {
		t.Skip("set " + testdb.EnvVar)
	}
	ctx := context.Background()
	root, _ := filepath.Abs("../../..")
	bins := t.TempDir()
	apiBin := build(t, bins, filepath.Join(root, "api"), "cmd/api")
	gwBin := build(t, bins, filepath.Join(root, "gateway"), "cmd/gateway")
	toolBin := build(t, bins, filepath.Join(root, "gateway"), "cmd/testtool")

	pool, tenantID := newTenant(t, ctx, dbURL)
	// Each test signs in as its own owner, so each finds its own account.
	owner := "e2e-owner-" + strings.ToLower(tenantID)

	direct, relay := freeAddr(t), freeAddr(t)
	start(t, toolBin, nil, "http://"+direct+"/calls", "-addr", direct)
	start(t, toolBin, nil, "http://"+relay+"/calls", "-addr", relay)
	relayURL := "http://" + relay + "/v1/relay"

	apiAddr, gwAddr := freeAddr(t), freeAddr(t)
	start(t, apiBin, []string{"KIFF_DATABASE_URL=" + dbURL, "KIFF_AUTH_PROVIDER=stub", "KIFF_STUB_SUBJECTS=" + owner, "KIFF_DEMO_TENANT_ID=" + tenantID,
		"KIFF_PERMIT_SIGNING_KEY=e2e-1:" + seed(), "KIFF_PLANQUE_URLS=" + relayURL, "KIFF_LOG_FORMAT=text"}, "http://"+apiAddr+"/healthz", "-addr", apiAddr)
	gwKeys := "e2e:" + base64.StdEncoding.EncodeToString(func() []byte { b := make([]byte, 32); _, _ = rand.Read(b); return b }())
	start(t, gwBin, []string{"KIFF_DATABASE_URL=" + dbURL, "KIFF_GATEWAY_CREDENTIAL_KEYS=" + gwKeys, "KIFF_API_URL=http://" + apiAddr,
		"KIFF_GATEWAY_ALLOW_PRIVATE_TOOLS=true", "KIFF_PLANQUE_URLS=" + relayURL, "KIFF_SIGN_PINS=" + tenantID, "KIFF_LOG_FORMAT=text"}, "http://"+gwAddr+"/healthz", "-addr", gwAddr)
	api, gw := "http://"+apiAddr, "http://"+gwAddr

	// Keys: the owner's, a connector's (the relay registers its tools with
	// one), and the agent's.
	key := func(body map[string]any) string {
		code, out := call(t, http.MethodPost, api+"/v1/keys", owner, body)
		if code != http.StatusCreated && code != http.StatusOK {
			t.Fatalf("mint key %v: %d %v", body, code, out)
		}
		return out["plaintext"].(string)
	}
	ownerKey := key(map[string]any{"label": "owner", "roles": []string{"admin"}})
	connector := key(map[string]any{"label": "relay", "roles": []string{"connector"}})
	agentKey := key(map[string]any{"label": "agent", "agent_id": "agt_e2e", "roles": []string{"gateway_agent"}})

	// The tools: one the owner connects straight to KIFF (sealed), one the
	// relay registers on its own host (verify mode, so its calls carry a
	// permit).
	if code, out := call(t, http.MethodPost, gw+"/v1/connections", ownerKey, map[string]any{"name": "quote_direct", "url": "http://" + direct + "/", "tool": "quote"}); code != http.StatusCreated {
		t.Fatalf("connect the sealed tool: %d %v", code, out)
	}
	if code, out := call(t, http.MethodPost, gw+"/v1/connections", connector, map[string]any{"name": "refund_relay", "url": relayURL, "tool": "refund",
		"mode": "verify", "audience": relayURL, "idempotency_argument": "idempotency_key", "app": "e2e"}); code != http.StatusCreated {
		t.Fatalf("connect the relay's tool: %d %v", code, out)
	}
	// A relay's tool registered with today's pins, beside the one above registered before them.
	pins := []map[string]any{{"path": "currency", "value": "eur"}, {"path": "metadata.kiff_op", "marker": true}}
	if code, out := call(t, http.MethodPost, gw+"/v1/connections", connector, map[string]any{"name": "refund_fresh", "url": relayURL, "tool": "refund",
		"mode": "verify", "audience": relayURL, "idempotency_argument": "idempotency_key", "vendor_pins": pins}); code != http.StatusCreated {
		t.Fatalf("connect the relay's fresh tool: %d %v", code, out)
	}
	// A Card for the agent granting them.
	card := map[string]any{"id": "card-e2e", "subject": "agt_e2e", "domain": "gateway",
		"grants":     []map[string]any{{"action": "quote_direct"}, {"action": "refund_relay"}, {"action": "refund_fresh"}},
		"aggregates": []map[string]any{{"quantity": "count", "limit": 100, "window": "calendar_day"}}}
	if code, out := call(t, http.MethodPost, api+"/v1/me/mandates", owner, card); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("issue the Card: %d %v", code, out)
	}

	callTool := func(name string, args map[string]any) (bool, string) {
		code, out := call(t, http.MethodPost, gw+"/mcp", agentKey, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": args}})
		res, _ := out["result"].(map[string]any)
		if code != http.StatusOK || res == nil {
			t.Fatalf("%s: %d %v", name, code, out)
		}
		var texts []string
		for _, c := range res["content"].([]any) {
			texts = append(texts, fmt.Sprint(c.(map[string]any)["text"]))
		}
		isErr, _ := res["isError"].(bool)
		return !isErr, strings.Join(texts, " | ")
	}
	if ok, text := callTool("quote_direct", map[string]any{"order": "o-1", "amount_eur": 5}); !ok || !strings.Contains(text, "Quote #") {
		t.Fatalf("the sealed call: %v %s", ok, text)
	}
	if ok, text := callTool("refund_relay", map[string]any{"order": "o-2", "amount_eur": 5, "idempotency_key": "op-e2e-1"}); !ok || !strings.Contains(text, "Refund #") {
		t.Fatalf("the relay's call: %v %s", ok, text)
	}
	// The relay's call went through the permit gate: one permit minted, for
	// its tool and audience; the sealed call needed none.
	var tool, audience string
	var minted bool
	if err := pool.QueryRow(ctx, `SELECT tool, audience, permit_jti IS NOT NULL FROM cloud_meta.permit_bindings WHERE tenant_id = $1 ORDER BY created_at LIMIT 1`, tenantID).Scan(&tool, &audience, &minted); err != nil {
		t.Fatalf("the relay's permit: %v", err)
	}
	if tool != "refund" || audience != relayURL || !minted {
		t.Fatalf("the relay's permit: %s %s %v", tool, audience, minted)
	}
	var calls struct{ Calls int }
	for _, addr := range []string{direct, relay} {
		resp, err := http.Get("http://" + addr + "/calls")
		if err != nil {
			t.Fatal(err)
		}
		_ = json.NewDecoder(resp.Body).Decode(&calls)
		resp.Body.Close()
		if calls.Calls != 1 {
			t.Fatalf("%s's tool received %d calls, want 1", addr, calls.Calls)
		}
	}

	// The relay brings its older tool up to today's registration in place
	// (PATCH): its next call's permit carries exactly the pins a tool
	// registered with them does, and the sealed tool's call is unchanged.
	if code, out := call(t, http.MethodPatch, gw+"/v1/connections/refund_relay", connector, map[string]any{"app": "e2e", "destructive_hint": false, "vendor_pins": pins}); code != http.StatusNoContent {
		t.Fatalf("patch the relay's tool: %d %v", code, out)
	}
	if code, out := call(t, http.MethodPatch, gw+"/v1/connections/quote_direct", connector, map[string]any{"app": "", "destructive_hint": false, "vendor_pins": []any{}}); code != http.StatusForbidden {
		t.Fatalf("the relay patching the owner's tool: %d %v", code, out)
	}
	if ok, text := callTool("refund_relay", map[string]any{"order": "o-3", "amount_eur": 5, "idempotency_key": "op-e2e-patched"}); !ok || !strings.Contains(text, "Refund #") {
		t.Fatalf("the patched tool's call: %v %s", ok, text)
	}
	if ok, text := callTool("refund_fresh", map[string]any{"order": "o-4", "amount_eur": 5, "idempotency_key": "op-e2e-fresh"}); !ok || !strings.Contains(text, "Refund #") {
		t.Fatalf("the fresh tool's call: %v %s", ok, text)
	}
	if ok, text := callTool("quote_direct", map[string]any{"order": "o-5", "amount_eur": 5}); !ok || !strings.Contains(text, "Quote #") {
		t.Fatalf("the sealed call after the patch: %v %s", ok, text)
	}
	// The relay calls' bindings, in the order the calls were made: before the patch, patched, fresh.
	rows, err := pool.Query(ctx, `SELECT pins FROM cloud_meta.permit_bindings WHERE tenant_id = $1 ORDER BY created_at`, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	var bound []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		bound = append(bound, p)
	}
	rows.Close()
	if len(bound) != 3 {
		t.Fatalf("%d relay bindings, want 3", len(bound))
	}
	before, patched, fresh := bound[0], bound[1], bound[2]
	if patched == "" || patched != fresh || before != "" {
		t.Fatalf("pins: patched %q, fresh %q, before the patch %q", patched, fresh, before)
	}
}

// K4 (kiff/cloud#228): on an account whose calls are decided by their class,
// a Card that covers a relay's money tool without opening its class refuses
// the call as closed, never as not covered. The agent hears the closed next
// step, the Record says it's closed until the owner opens it (in the
// reader's language), and the relay's upstream gets no request.
func TestAClosedCategoryIsRefusedAsClosedAndNothingIsSent(t *testing.T) {
	if os.Getenv("KIFF_E2E") != "1" {
		t.Skip("set KIFF_E2E=1 (and KIFF_TEST_POSTGRES_URL) to run the end-to-end test")
	}
	dbURL := testdb.URL(t)
	if dbURL == "" {
		t.Skip("set " + testdb.EnvVar)
	}
	ctx := context.Background()
	root, _ := filepath.Abs("../../..")
	bins := t.TempDir()
	apiBin := build(t, bins, filepath.Join(root, "api"), "cmd/api")
	gwBin := build(t, bins, filepath.Join(root, "gateway"), "cmd/gateway")
	toolBin := build(t, bins, filepath.Join(root, "gateway"), "cmd/testtool")

	_, tenantID := newTenant(t, ctx, dbURL)
	owner := "e2e-owner-" + strings.ToLower(tenantID)
	relay := freeAddr(t)
	start(t, toolBin, nil, "http://"+relay+"/calls", "-addr", relay)
	relayURL := "http://" + relay + "/v1/relay"

	apiAddr, gwAddr := freeAddr(t), freeAddr(t)
	start(t, apiBin, []string{"KIFF_DATABASE_URL=" + dbURL, "KIFF_AUTH_PROVIDER=stub", "KIFF_STUB_SUBJECTS=" + owner, "KIFF_DEMO_TENANT_ID=" + tenantID,
		"KIFF_PERMIT_SIGNING_KEY=e2e-1:" + seed(), "KIFF_PLANQUE_URLS=" + relayURL, "KIFF_K4_TENANTS=" + tenantID, "KIFF_LOG_FORMAT=text"}, "http://"+apiAddr+"/healthz", "-addr", apiAddr)
	gwKeys := "e2e:" + base64.StdEncoding.EncodeToString(func() []byte { b := make([]byte, 32); _, _ = rand.Read(b); return b }())
	start(t, gwBin, []string{"KIFF_DATABASE_URL=" + dbURL, "KIFF_GATEWAY_CREDENTIAL_KEYS=" + gwKeys, "KIFF_API_URL=http://" + apiAddr,
		"KIFF_GATEWAY_ALLOW_PRIVATE_TOOLS=true", "KIFF_PLANQUE_URLS=" + relayURL, "KIFF_LOG_FORMAT=text"}, "http://"+gwAddr+"/healthz", "-addr", gwAddr)
	api, gw := "http://"+apiAddr, "http://"+gwAddr

	key := func(body map[string]any) string {
		code, out := call(t, http.MethodPost, api+"/v1/keys", owner, body)
		if code != http.StatusCreated && code != http.StatusOK {
			t.Fatalf("mint key %v: %d %v", body, code, out)
		}
		return out["plaintext"].(string)
	}
	connector := key(map[string]any{"label": "relay", "roles": []string{"connector"}})
	agentKey := key(map[string]any{"label": "agent", "agent_id": "agt_k4", "roles": []string{"gateway_agent"}})

	// A relay's refund tool: money by its name, so K4 gates it.
	if code, out := call(t, http.MethodPost, gw+"/v1/connections", connector, map[string]any{"name": "refund_closed", "url": relayURL, "tool": "refund",
		"mode": "verify", "audience": relayURL, "idempotency_argument": "idempotency_key", "app": "e2e"}); code != http.StatusCreated {
		t.Fatalf("connect the relay's tool: %d %v", code, out)
	}
	// A Card that covers it, opening no class.
	card := map[string]any{"id": "card-k4", "subject": "agt_k4", "domain": "gateway",
		"grants":     []map[string]any{{"action": "refund_closed"}},
		"aggregates": []map[string]any{{"quantity": "count", "limit": 100, "window": "calendar_day"}}}
	if code, out := call(t, http.MethodPost, api+"/v1/me/mandates", owner, card); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("issue the Card: %d %v", code, out)
	}

	code, out := call(t, http.MethodPost, gw+"/mcp", agentKey, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "refund_closed", "arguments": map[string]any{"order": "o-k4", "amount_eur": 5, "idempotency_key": "op-k4-1"}}})
	res, _ := out["result"].(map[string]any)
	if code != http.StatusOK || res == nil {
		t.Fatalf("the call: %d %v", code, out)
	}
	raw, _ := json.Marshal(res)
	text := string(raw)
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("a closed category's call went through: %s", text)
	}
	// The agent's words: closed until the owner opens it, the Card does cover the tool, never "does not cover".
	if !strings.Contains(text, "mandate_category_closed") || !strings.Contains(text, "closed on the agent's Card until the account owner opens it") || strings.Contains(text, "does not cover") {
		t.Fatalf("the agent's words: %s", text)
	}

	// The relay's upstream got no request.
	resp, err := http.Get("http://" + relay + "/calls")
	if err != nil {
		t.Fatal(err)
	}
	var calls struct{ Calls int }
	_ = json.NewDecoder(resp.Body).Decode(&calls)
	resp.Body.Close()
	if calls.Calls != 0 {
		t.Fatalf("the relay's tool received %d calls, want 0", calls.Calls)
	}

	// The Record line, in the reader's language: closed until opened, never "not covered".
	for lang, want := range map[string]string{"en": "It's closed on your Card until you open it.", "fr": "Il est fermé sur votre Card tant que vous ne l'ouvrez pas."} {
		req, _ := http.NewRequest(http.MethodGet, api+"/v1/me/activity?show=refused", nil)
		req.Header.Set("Authorization", "Bearer "+owner)
		req.Header.Set("Accept-Language", lang)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Calls []struct {
				ActionName string `json:"action_name"`
				Outcome    string `json:"outcome"`
				Reason     string `json:"reason"`
			} `json:"calls"`
		}
		_ = json.NewDecoder(r.Body).Decode(&page)
		r.Body.Close()
		found := false
		for _, c := range page.Calls {
			if c.Reason == want {
				found = true
			}
			if strings.Contains(c.Reason, "No Card covers") || strings.Contains(c.Reason, "Aucune Card") {
				t.Fatalf("%s: a Record line says not covered: %+v", lang, c)
			}
		}
		if !found {
			t.Fatalf("%s: no Record line %q in %+v", lang, want, page.Calls)
		}
	}
}
