package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/testdb"
)

// A relay's pay, with purchases on, goes gateway → planque's describe →
// the API's permit gate and decision on this code. A pay whose checkout
// planque signed opens its case, is decided within the Card's limits and
// reaches the tool. One whose block was changed after planque signed it
// is refused, opens no case, and the pay never reaches the tool.
func TestAPayOpensItsCaseFromPlanquesSignedCheckout(t *testing.T) {
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
	// The API trusts the stand-in planque's key only in its e2e build.
	apiBin := build(t, bins, filepath.Join(root, "api"), "cmd/api", "-tags", "e2e")
	gwBin := build(t, bins, filepath.Join(root, "gateway"), "cmd/gateway")
	toolBin := build(t, bins, filepath.Join(root, "gateway"), "cmd/testtool")
	pool, tenantID := newTenant(t, ctx, dbURL)
	// Each test signs in as its own owner, so each finds its own account.
	owner := "e2e-owner-" + strings.ToLower(tenantID)

	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	priv, _ := x509.MarshalPKCS8PrivateKey(k)
	pub, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)

	relay := freeAddr(t)
	start(t, toolBin, nil, "http://"+relay+"/calls", "-addr", relay, "-checkout-key", base64.RawURLEncoding.EncodeToString(priv), "-tenant", tenantID)
	// A relay grant's audience: <planque base>/mcp/<account>/<grant>, the
	// grant the stand-in signs its blocks for.
	relayURL := "http://" + relay + "/mcp/" + tenantID + "/grt_e2e"

	apiAddr, gwAddr := freeAddr(t), freeAddr(t)
	start(t, apiBin, []string{"KIFF_DATABASE_URL=" + dbURL, "KIFF_AUTH_PROVIDER=stub", "KIFF_STUB_SUBJECTS=" + owner, "KIFF_DEMO_TENANT_ID=" + tenantID,
		"KIFF_PERMIT_SIGNING_KEY=e2e-1:" + seed(), "KIFF_PLANQUE_URLS=" + relayURL, "KIFF_LOG_FORMAT=text",
		"KIFF_PURCHASES=on", "KIFF_PURCHASE_CARD_FILL=test", "KIFF_E2E_PLANQUE_STEP_KEY=" + base64.RawURLEncoding.EncodeToString(pub)}, "http://"+apiAddr+"/healthz", "-addr", apiAddr)
	gwKeys := "e2e:" + base64.StdEncoding.EncodeToString(func() []byte { b := make([]byte, 32); _, _ = rand.Read(b); return b }())
	start(t, gwBin, []string{"KIFF_DATABASE_URL=" + dbURL, "KIFF_GATEWAY_CREDENTIAL_KEYS=" + gwKeys, "KIFF_API_URL=http://" + apiAddr,
		"KIFF_GATEWAY_ALLOW_PRIVATE_TOOLS=true", "KIFF_PLANQUE_URLS=" + relayURL, "KIFF_LOG_FORMAT=text", "KIFF_PURCHASES=on"}, "http://"+gwAddr+"/healthz", "-addr", gwAddr)
	api, gw := "http://"+apiAddr, "http://"+gwAddr

	key := func(body map[string]any) string {
		code, out := call(t, http.MethodPost, api+"/v1/keys", owner, body)
		if code != http.StatusCreated && code != http.StatusOK {
			t.Fatalf("mint key %v: %d %v", body, code, out)
		}
		return out["plaintext"].(string)
	}
	connector := key(map[string]any{"label": "relay", "roles": []string{"connector"}})
	agentKey := key(map[string]any{"label": "agent", "agent_id": "agt_e2e", "roles": []string{"gateway_agent"}})
	if code, out := call(t, http.MethodPost, gw+"/v1/connections", connector, map[string]any{"name": "pay_relay", "url": relayURL, "tool": "browse_pay",
		"mode": "verify", "audience": relayURL, "idempotency_argument": "planque_op", "app": "e2e"}); code != http.StatusCreated {
		t.Fatalf("connect the relay's pay: %d %v", code, out)
	}
	card := map[string]any{"id": "card-e2e-pay", "subject": "agt_e2e", "domain": "gateway",
		"grants": []map[string]any{{"action": "pay_relay", "opens": []map[string]any{{"class": "money"}}, "purchase": map[string]any{"mode": "within_limits", "currency": "EUR",
			"per_purchase": 5000, "per_day": 20000, "purchases_per_hour": 3, "purchases_per_day": 10, "categories": []string{"stationery_stores_office_and_school_supply_stores"}}}},
		"aggregates": []map[string]any{{"quantity": "count", "limit": 100, "window": "calendar_day"}}}
	if code, out := call(t, http.MethodPost, api+"/v1/me/mandates", owner, card); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("issue the Card: %d %v", code, out)
	}

	pay := func(ref, op string) (bool, string) {
		code, out := call(t, http.MethodPost, gw+"/mcp", agentKey, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "pay_relay", "arguments": map[string]any{"url": "https://shop.example/checkout", "ref": ref, "kiff_operation_id": op}}})
		res, _ := out["result"].(map[string]any)
		if code != http.StatusOK || res == nil {
			t.Fatalf("pay %s: %d %v", ref, code, out)
		}
		var texts []string
		for _, c := range res["content"].([]any) {
			texts = append(texts, fmt.Sprint(c.(map[string]any)["text"]))
		}
		isErr, _ := res["isError"].(bool)
		return !isErr, strings.Join(texts, " | ")
	}
	calls := func() int {
		resp, err := http.Get("http://" + relay + "/calls")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var c struct{ Calls int }
		_ = json.NewDecoder(resp.Body).Decode(&c)
		return c.Calls
	}
	cases := func() (n int, state string) {
		_ = pool.QueryRow(ctx, `SELECT count(DISTINCT case_id), coalesce(max(state), '') FROM purchase.cases WHERE tenant_id = $1`, tenantID).Scan(&n, &state)
		return
	}

	// planque's signed checkout opens the case. planque's read names no
	// merchant category (the issuer gives it at the authorization), so the
	// Card's limits can't decide it here: it waits for the owner with the
	// purchase ask, and only the describe
	// reached the tool.
	ok, text := pay("c7", "op-pay-1")
	if ok || !strings.Contains(text, "HELD") || !strings.Contains(text, "this buys something") {
		t.Fatalf("the pay: %v %s", ok, text)
	}
	if n := calls(); n != 1 {
		t.Fatalf("the tool received %d calls, want only the describe", n)
	}
	if n, state := cases(); n != 1 || state != "held" {
		t.Fatalf("the case: %d, %q", n, state)
	}
	// The describe was permitted under its own op; the held pay wasn't.
	var describes, pays int
	_ = pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE op LIKE '%:describe'), count(*) FILTER (WHERE op NOT LIKE '%:describe') FROM cloud_meta.permit_bindings
		WHERE tenant_id = $1 AND tool = 'browse_pay' AND permit_jti IS NOT NULL`, tenantID).Scan(&describes, &pays)
	if describes != 1 || pays != 0 {
		t.Fatalf("the permits: %d describes, %d pays", describes, pays)
	}

	// A block changed after planque signed it: refused, no case, and only
	// the describe reached the tool.
	if ok, text := pay("tampered", "op-pay-2"); ok || !strings.Contains(text, "purchase_checkout") || !strings.Contains(text, "isn't signed by planque") {
		t.Fatalf("the tampered pay: %v %s", ok, text)
	}
	if n := calls(); n != 2 {
		t.Fatalf("the tool received %d calls, want only the tampered pay's describe more", n)
	}
	if n, _ := cases(); n != 1 {
		t.Fatalf("a tampered checkout opened a case: %d", n)
	}
}
