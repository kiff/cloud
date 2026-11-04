package connection_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kiff/cloud/gateway/internal/connection"
	"github.com/kiff/cloud/gateway/internal/egress"
	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
	"github.com/kiff/cloud/gateway/internal/mcp"
	"github.com/kiff/cloud/gateway/internal/secret"
	"github.com/kiff/cloud/gateway/internal/testdb"
)

const toolSecret = "sk_tool_7f3a9c"

// toolServer is a remote MCP server that only answers with toolSecret.
type refunds struct{ status bool }

func (r refunds) ListTools(context.Context) ([]mcp.Tool, error) {
	tools := []mcp.Tool{
		{Name: "refund", Title: "Refund a payment", Description: "Refund an order", InputSchema: json.RawMessage(
			`{"type":"object","properties":{"order":{"type":"string"},"amount_eur":{"type":"integer"},"note":{"type":"number"},"total":{"type":"string"},"idem":{"type":"string"}},"required":["order","amount_eur"]}`)},
		{Name: "refund_status", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}, InputSchema: json.RawMessage(`{"type":"object","properties":{"idem":{"type":"string"}},"required":["idem"]}`)},
		{Name: "unsafe_status", InputSchema: json.RawMessage(`{"type":"object","properties":{"idem":{"type":"string"}}}`)},
		{Name: "wrong_status", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}, InputSchema: json.RawMessage(`{"type":"object","properties":{"other":{"type":"string"}}}`)},
	}
	if !r.status {
		return tools[:1], nil
	}
	return tools, nil
}
func (refunds) CallTool(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return mcp.TextResult("ok", false), nil
}

func toolServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(&mcp.Server{Name: "tool", Version: "1", Auth: func(r *http.Request) (mcp.Handler, error) {
		if r.Header.Get("Authorization") != "Bearer "+toolSecret {
			return nil, errors.New("bad token")
		}
		return refunds{}, nil
	}})
	t.Cleanup(srv.Close)
	return srv
}

type fakeAPI struct {
	mu      sync.Mutex
	applied map[string]string
	targets map[string][]kiffapi.PermitTarget
	fail    bool
	// signedAsks are the requests DeviceSigned was asked about; signDown
	// makes it answer that KIFF is unavailable. A signature "t=ok" is
	// accepted, once.
	signedAsks []string
	signDown   bool
	signedUsed map[string]bool
}

func (f *fakeAPI) DeviceSigned(_ context.Context, key, method, uri, bodySHA256, signature string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signedAsks = append(f.signedAsks, key+" "+method+" "+uri+" "+bodySHA256)
	if f.signDown {
		return "", errors.Join(kiffapi.ErrUnavailable, errors.New("down"))
	}
	if f.signedUsed == nil {
		f.signedUsed = map[string]bool{}
	}
	if signature != "t=ok" || f.signedUsed[bodySHA256] {
		return "", kiffapi.ErrNotSigned
	}
	f.signedUsed[bodySHA256] = true
	return "kdg_test", nil
}

// CheckProfileApproval accepts an approval {"signed": true}, as apps/api
// would a valid signed approve request, and refuses any other.
func (f *fakeAPI) CheckProfileApproval(_ context.Context, key string, profile, approval json.RawMessage) (kiffapi.ProfileApproval, error) {
	if bytes.Contains(profile, []byte(`"v":"bad"`)) {
		return kiffapi.ProfileApproval{}, fmt.Errorf("%w: v must be kiff.profile.v1", kiffapi.ErrInvalidProfile)
	}
	if !bytes.Equal(approval, []byte(`{"signed":true}`)) {
		return kiffapi.ProfileApproval{Reason: "profile_not_approved"}, nil
	}
	return kiffapi.ProfileApproval{Valid: true, ProfileSHA256: "p256", WordsSHA256: "w256", DeviceID: "dev_1", SignedAt: "2026-11-02T20:00:00Z"}, nil
}

// phoneWrite refuses a phone's session that writes without the grant, as
// apps/api's route policy does.
func phoneWrite(ctx context.Context, key string) error {
	if strings.HasPrefix(key, "phone-") && kiffapi.DeviceGrantFrom(ctx) != "kdg_test" {
		return errors.New("kiffapi: 403: phone_session_forbidden")
	}
	return nil
}

func (f *fakeAPI) SetPermitTargets(ctx context.Context, key string, ts []kiffapi.PermitTarget) error {
	if err := phoneWrite(ctx, key); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("kiffapi: 403: forbidden")
	}
	if f.targets == nil {
		f.targets = map[string][]kiffapi.PermitTarget{}
	}
	f.targets[key] = ts
	return nil
}

func (f *fakeAPI) Me(_ context.Context, key string) (kiffapi.Me, error) {
	switch key {
	case "owner-a":
		return kiffapi.Me{TenantID: "tenant-a", Roles: []string{"tenant_owner"}}, nil
	case "owner-b":
		return kiffapi.Me{TenantID: "tenant-b", Roles: []string{"admin"}}, nil
	case "agent-a":
		return kiffapi.Me{TenantID: "tenant-a", AgentID: "coding-agent", Roles: []string{"admin"}}, nil
	case "runtime-a":
		return kiffapi.Me{TenantID: "tenant-a", Roles: []string{"ops_agent"}}, nil
	case "connector-a", "connector-a2":
		// The key planque keeps.
		return kiffapi.Me{TenantID: "tenant-a", APIKey: true, CredentialID: "key-" + key, Roles: []string{"connector"},
			Capabilities: []string{"read", "personal", "tools.connect"}}, nil
	case "phone-a":
		return kiffapi.Me{TenantID: "tenant-a", Roles: []string{"tenant_owner"}, Capabilities: []string{"read", "tools.connect"}, DeviceSession: true}, nil
	case "phone-approver":
		// A phone of a person who answers holds but doesn't connect tools.
		return kiffapi.Me{TenantID: "tenant-a", Capabilities: []string{"read", "holds.answer"}, DeviceSession: true}, nil
	}
	return kiffapi.Me{}, kiffapi.ErrUnauthorized
}

func (f *fakeAPI) ApplyDomain(ctx context.Context, key, domain, yaml string) error {
	if err := phoneWrite(ctx, key); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("kiffapi: 422: domain fails validation")
	}
	f.applied[key] = yaml
	return nil
}

type rig struct {
	h     *connection.Handler
	store *connection.Store
	api   *fakeAPI
	srv   *httptest.Server
	tool  *httptest.Server
}

func ring(t *testing.T, ids ...string) *secret.Keyring {
	var parts []string
	for _, id := range ids {
		k, _ := secret.NewKey(id)
		parts = append(parts, k)
	}
	k, err := secret.ParseKeyring(strings.Join(parts, ","))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newRig(t *testing.T, policy egress.Policy) *rig {
	t.Helper()
	pool := testdb.Pool(t)
	ctx := context.Background()
	if err := invocation.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE gateway.tools`); err != nil {
		t.Fatal(err)
	}
	r := &rig{store: &connection.Store{Pool: pool, Keys: ring(t, "v1")}, api: &fakeAPI{applied: map[string]string{}}, tool: toolServer(t)}
	mux := http.NewServeMux()
	r.h = &connection.Handler{Store: r.store, API: r.api, Remote: &mcp.Client{HTTP: policy.Client(0)}, Policy: policy}
	r.h.Register(mux)
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	return r
}

func (r *rig) do(t *testing.T, key, method, path string, body any) (int, string) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, r.srv.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

func (r *rig) refund(cred string) map[string]any {
	return map[string]any{"name": "issue_refund", "url": r.tool.URL, "tool": "refund",
		"amount_argument": "amount_eur", "idempotency_argument": "idem", "credential": cred}
}

var dev = egress.Policy{AllowPrivate: true}

func TestOutcomeLookupRequiresOwnerTrustedSeparateReadOnlyTool(t *testing.T) {
	for _, tc := range []struct {
		name, lookup, contract, credential string
		status                             int
	}{
		{"trusted", "refund_status", "terminal_no_effect_v1", "owner-a", http.StatusCreated},
		{"same_action", "refund", "terminal_no_effect_v1", "owner-a", http.StatusBadRequest},
		{"no_contract", "refund_status", "", "owner-a", http.StatusBadRequest},
		{"not_readonly", "unsafe_status", "terminal_no_effect_v1", "owner-a", http.StatusBadRequest},
		{"wrong_arguments", "wrong_status", "terminal_no_effect_v1", "owner-a", http.StatusBadRequest},
		{"runtime", "refund_status", "terminal_no_effect_v1", "agent-a", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, dev)
			r.tool = httptest.NewServer(&mcp.Server{Name: "status-fixture", Version: "1", Auth: func(req *http.Request) (mcp.Handler, error) {
				if req.Header.Get("Authorization") != "Bearer "+toolSecret {
					return nil, errors.New("bad token")
				}
				return refunds{status: true}, nil
			}})
			t.Cleanup(r.tool.Close)
			body := r.refund(toolSecret)
			body["outcome_contract"], body["outcome_lookup_tool"] = tc.contract, tc.lookup
			status, response := r.do(t, tc.credential, http.MethodPost, "/v1/connections", body)
			if status != tc.status {
				t.Fatalf("%d want %d: %s", status, tc.status, response)
			}
			if tc.status == http.StatusCreated {
				connections, err := r.store.List(context.Background(), "tenant-a")
				if err != nil || len(connections) != 1 || connections[0].OutcomeLookupTool != "refund_status" {
					t.Fatalf("stored lookup: %+v %v", connections, err)
				}
				tools, err := (&gateway.PostgresCatalog{Pool: r.store.Pool}).Tools(context.Background(), "tenant-a")
				if err != nil || len(tools) != 1 || tools[0].OutcomeLookupTool != "refund_status" || tools[0].ConnectionCreatedAt.IsZero() {
					t.Fatalf("executor binding: %+v %v", tools, err)
				}
			}
		})
	}
}

// Connect, list, test, rotate, remove — without the credential ever
// leaving the gateway, and with the contract applied each time.
func TestConnectionLifecycle(t *testing.T) {
	r := newRig(t, dev)
	ctx := context.Background()
	status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", r.refund(toolSecret))
	if status != http.StatusCreated || strings.Contains(body, toolSecret) || !strings.Contains(body, `"has_credential":true`) {
		t.Fatalf("create: %d %s", status, body)
	}
	if y := r.api.applied["owner-a"]; !strings.Contains(y, "name: issue_refund") || !strings.Contains(y, "required_parameters: [amount_eur]") {
		t.Fatalf("contract not applied: %q", y)
	}
	var stored []byte
	_ = r.store.Pool.QueryRow(ctx, `SELECT credential FROM gateway.tools WHERE tenant_id='tenant-a'`).Scan(&stored)
	if len(stored) == 0 || bytes.Contains(stored, []byte(toolSecret)) {
		t.Fatal("credential stored in the clear")
	}
	if got, err := r.store.Credential(ctx, "tenant-a", "issue_refund"); err != nil || got != toolSecret {
		t.Fatalf("credential: %v", err)
	}
	if status, body := r.do(t, "owner-a", http.MethodGet, "/v1/connections", nil); status != 200 ||
		!strings.Contains(body, `"tool":"refund"`) || strings.Contains(body, toolSecret) {
		t.Fatalf("list: %d %s", status, body)
	}
	if _, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections/issue_refund/test", nil); !strings.Contains(body, `"ok":true`) {
		t.Fatalf("test: %s", body)
	}
	// A new credential that does not work never replaces the old one.
	if status, body := r.do(t, "owner-a", http.MethodPut, "/v1/connections/issue_refund/credential", map[string]string{"credential": "sk_wrong_value"}); status != http.StatusBadGateway ||
		strings.Contains(body, "sk_wrong_value") {
		t.Fatalf("bad rotation: %d %s", status, body)
	}
	if got, _ := r.store.Credential(ctx, "tenant-a", "issue_refund"); got != toolSecret {
		t.Fatal("a failed rotation changed the credential")
	}
	if status, _ := r.do(t, "owner-a", http.MethodPut, "/v1/connections/issue_refund/credential", map[string]string{"credential": toolSecret}); status != 200 {
		t.Fatalf("rotation: %d", status)
	}
	if status, _ := r.do(t, "owner-a", http.MethodPost, "/v1/connections", r.refund(toolSecret)); status != http.StatusConflict {
		t.Fatalf("duplicate: %d", status)
	}
	if status, _ := r.do(t, "owner-a", http.MethodDelete, "/v1/connections/issue_refund", nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	if _, body := r.do(t, "owner-a", http.MethodGet, "/v1/connections", nil); !strings.Contains(body, `"connections":[]`) {
		t.Fatalf("after delete: %s", body)
	}
}

// One account cannot see, test, remove or decrypt another's connection,
// even with the ciphertext copied into its own row.
// Invariant: one account cannot see, test, remove or decrypt another
// account's tool connections.
// Category: isolation
// Attack: copy another account's encrypted tool credential into your own connection
// Refused: its seal is bound to the account it was made for, so it does not open; the other account's connection stays invisible
func TestConnectionsAreIsolatedByAccount(t *testing.T) {
	r := newRig(t, dev)
	ctx := context.Background()
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", r.refund(toolSecret)); status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	if _, body := r.do(t, "owner-b", http.MethodGet, "/v1/connections", nil); !strings.Contains(body, `"connections":[]`) {
		t.Fatalf("b sees a: %s", body)
	}
	for _, c := range [][2]string{{http.MethodPost, "/v1/connections/issue_refund/test"}, {http.MethodDelete, "/v1/connections/issue_refund"},
		{http.MethodPut, "/v1/connections/issue_refund/credential"}} {
		var body any
		if c[0] == http.MethodPut {
			body = map[string]string{"credential": toolSecret}
		}
		if status, _ := r.do(t, "owner-b", c[0], c[1], body); status != http.StatusNotFound {
			t.Errorf("b %s %s: %d", c[0], c[1], status)
		}
	}
	if _, err := r.store.Credential(ctx, "tenant-b", "issue_refund"); !errors.Is(err, connection.ErrNotFound) {
		t.Fatalf("b read a's credential: %v", err)
	}
	if _, err := r.store.Pool.Exec(ctx, `INSERT INTO gateway.tools (tenant_id, name, input_schema, upstream_url, upstream_tool, credential_key_id, credential)
		SELECT 'tenant-b', name, input_schema, upstream_url, upstream_tool, credential_key_id, credential FROM gateway.tools WHERE tenant_id = 'tenant-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.Credential(ctx, "tenant-b", "issue_refund"); !errors.Is(err, secret.ErrUnreadable) {
		t.Fatalf("copied ciphertext opened for another account: %v", err)
	}
}

// Only an owner or admin key manages connections; an agent's key, even
// with admin, cannot change its own tools.
func TestOnlyOwnersManage(t *testing.T) {
	r := newRig(t, dev)
	for key, want := range map[string]int{"agent-a": 403, "runtime-a": 403, "nobody": 401} {
		if status, _ := r.do(t, key, http.MethodGet, "/v1/connections", nil); status != want {
			t.Errorf("%s: %d, want %d", key, status, want)
		}
	}
}

// A tool that rejects the credential, a contract the API refuses, or an
// amount argument a Card cannot sum leaves nothing stored.
func TestFailedConnectStoresNothing(t *testing.T) {
	r := newRig(t, dev)
	status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", r.refund("sk_wrong_value"))
	if status != http.StatusBadGateway || strings.Contains(body, "sk_wrong_value") {
		t.Fatalf("wrong credential: %d %s", status, body)
	}
	req := r.refund(toolSecret)
	req["amount_argument"] = "note"
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", req); status != 400 || !strings.Contains(body, "integer") {
		t.Fatalf("number amount: %d %s", status, body)
	}
	req = r.refund(toolSecret)
	req["tool"] = "drop_tables"
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", req); status != http.StatusBadGateway || !strings.Contains(body, "refund") {
		t.Fatalf("unknown tool: %d %s", status, body)
	}
	r.api.fail = true
	if status, _ := r.do(t, "owner-a", http.MethodPost, "/v1/connections", r.refund(toolSecret)); status != http.StatusUnprocessableEntity {
		t.Fatalf("refused contract: %d", status)
	}
	if conns, _ := r.store.List(context.Background(), "tenant-a"); len(conns) != 0 {
		t.Fatalf("stored after failures: %+v", conns)
	}
}

// kiff_card is the gateway's own tool; a connected tool cannot
// take its name and shadow it, in any case.
func TestCardToolNameIsReserved(t *testing.T) {
	r := newRig(t, dev)
	for _, name := range []string{"kiff_card", "KIFF_Card"} {
		req := r.refund(toolSecret)
		req["name"] = name
		if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", req); status != 400 || !strings.Contains(body, "KIFF's own tool") {
			t.Errorf("%s: %d %s", name, status, body)
		}
	}
}

// With the production policy, tool URLs that reach internal addresses
// are refused before anything is contacted.
// Invariant: a connected tool cannot point KIFF at internal, loopback,
// link-local or cloud metadata addresses.
// Category: isolation
// Attack: connect a tool whose URL resolves to localhost or the cloud metadata address
// Refused: the connection is refused before anything is contacted
func TestUntrustedURLsAreRefused(t *testing.T) {
	r := newRig(t, egress.Policy{})
	for _, u := range []string{r.tool.URL, "https://127.0.0.1/mcp", "https://169.254.169.254/latest/meta-data", "https://[::1]/mcp", "https://localhost/mcp"} {
		req := r.refund(toolSecret)
		req["url"] = u
		if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", req); status != 400 {
			t.Errorf("%s: %d %s", u, status, body)
		}
	}
}

// Key rotation: re-seal under the new key, then the old key can go.
func TestResealUnderNewKey(t *testing.T) {
	r := newRig(t, dev)
	ctx := context.Background()
	v1, _ := secret.NewKey("v1")
	old, _ := secret.ParseKeyring(v1)
	r.store.Keys = old
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", r.refund(toolSecret)); status != http.StatusCreated {
		t.Fatalf("%d %s", status, body)
	}
	v2, _ := secret.NewKey("v2")
	both, _ := secret.ParseKeyring(v2 + "," + v1)
	r.store.Keys = both
	if n, err := r.store.Reseal(ctx); err != nil || n != 1 {
		t.Fatalf("reseal: %d %v", n, err)
	}
	if n, _ := r.store.Reseal(ctx); n != 0 {
		t.Fatal("reseal is not idempotent")
	}
	onlyNew, _ := secret.ParseKeyring(v2)
	r.store.Keys = onlyNew
	if got, err := r.store.Credential(ctx, "tenant-a", "issue_refund"); err != nil || got != toolSecret {
		t.Fatalf("after rotation: %v", err)
	}
}

// read_only is stored, listed, and carried into the contract;
// the default stays false.
func TestReadOnlyConnection(t *testing.T) {
	r := newRig(t, dev)
	req := r.refund(toolSecret)
	req["read_only"] = true
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", req); status != http.StatusCreated || !strings.Contains(body, `"read_only":true`) {
		t.Fatalf("create: %d %s", status, body)
	}
	if !strings.Contains(r.api.applied["owner-a"], "risk: low") {
		t.Fatalf("contract: %s", r.api.applied["owner-a"])
	}
	req = r.refund(toolSecret)
	req["name"] = "issue_refund_2"
	if _, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", req); !strings.Contains(body, `"read_only":false`) {
		t.Fatalf("default: %s", body)
	}
}

// Discover lists a server's tools for the connect form: each tool's
// integer and string arguments, nothing stored, the credential never
// returned. It has create's auth and egress rules.
func TestDiscover(t *testing.T) {
	r := newRig(t, dev)
	status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections/discover", map[string]string{"url": r.tool.URL, "credential": toolSecret})
	if status != http.StatusOK || strings.Contains(body, toolSecret) {
		t.Fatalf("discover: %d %s", status, body)
	}
	var got struct {
		Tools []struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			IntegerArgs []string `json:"integer_arguments"`
			StringArgs  []string `json:"string_arguments"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "refund" || got.Tools[0].Description != "Refund an order" ||
		strings.Join(got.Tools[0].IntegerArgs, ",") != "amount_eur" || strings.Join(got.Tools[0].StringArgs, ",") != "idem,order,total" {
		t.Fatalf("tools: %+v", got.Tools)
	}
	if conns, _ := r.store.List(context.Background(), "tenant-a"); len(conns) != 0 {
		t.Fatalf("discover stored a connection: %+v", conns)
	}
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections/discover", map[string]string{"url": r.tool.URL, "credential": "sk_wrong_value"}); status != http.StatusBadGateway ||
		strings.Contains(body, "sk_wrong_value") {
		t.Fatalf("wrong credential: %d %s", status, body)
	}
	for key, want := range map[string]int{"agent-a": 403, "nobody": 401} {
		if status, _ := r.do(t, key, http.MethodPost, "/v1/connections/discover", map[string]string{"url": r.tool.URL, "credential": toolSecret}); status != want {
			t.Errorf("%s: %d, want %d", key, status, want)
		}
	}
	strict := newRig(t, egress.Policy{})
	if status, _ := strict.do(t, "owner-a", http.MethodPost, "/v1/connections/discover", map[string]string{"url": "https://169.254.169.254/mcp"}); status != 400 {
		t.Fatalf("metadata address: %d", status)
	}
}

// The owner can say what the amount means when connecting a tool. It is
// stored, listed, and reaches the applied contract; a bad unit, a scale
// without a unit, or a unit without an amount argument is refused.
func TestConnectWithAnAmountUnit(t *testing.T) {
	r := newRig(t, dev)
	req := r.refund(toolSecret)
	req["amount_unit"], req["amount_scale"] = "EUR", 2
	status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", req)
	if status != http.StatusCreated || !strings.Contains(body, `"amount_unit":"EUR"`) || !strings.Contains(body, `"amount_scale":2`) {
		t.Fatalf("create: %d %s", status, body)
	}
	if y := r.api.applied["owner-a"]; !strings.Contains(y, "unit: EUR") || !strings.Contains(y, "scale: 2") {
		t.Fatalf("contract without the unit: %q", y)
	}
	if conns, _ := r.store.List(context.Background(), "tenant-a"); len(conns) != 1 || conns[0].AmountUnit != "EUR" || conns[0].AmountScale != 2 {
		t.Fatalf("stored: %+v", conns)
	}
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"bad unit", func(m map[string]any) { m["amount_unit"] = "€" }, "letters only"},
		{"scale without unit", func(m map[string]any) { m["amount_scale"] = 2 }, "needs an amount_unit"},
		{"no amount argument", func(m map[string]any) { delete(m, "amount_argument"); m["amount_unit"] = "EUR" }, "need an amount_argument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := r.refund(toolSecret)
			req["name"] = "other_tool"
			tc.edit(req)
			if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", req); status != http.StatusBadRequest || !strings.Contains(body, tc.want) {
				t.Fatalf("%d %s", status, body)
			}
		})
	}
}

// A whole-number amount's display scale stays off its permit target:
// apps/api reads a target's scale only for a decimal amount, and refuses a
// connector key that changes a target, so a target written before decimals
// (scale 0) must come out the same (connector_key_forbidden on every
// connect for an account with a scaled verify or relay tool).
func TestPermitTargetScaleOnlyForDecimal(t *testing.T) {
	r := newRig(t, dev)
	open := openToolServer(t)
	cents := map[string]any{"name": "issue_refund", "url": open.URL, "tool": "refund", "mode": "verify", "audience": "https://refunds.example.com/mcp",
		"idempotency_argument": "idem", "amount_argument": "amount_eur", "amount_unit": "EUR", "amount_scale": 2}
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", cents); status != http.StatusCreated {
		t.Fatalf("connect issue_refund: %d %s", status, body)
	}
	if got := r.api.targets["owner-a"]; len(got) != 1 || got[0] != (kiffapi.PermitTarget{Domain: "gateway", Action: "issue_refund", Tool: "refund",
		Audience: "https://refunds.example.com/mcp", AmountArg: "amount_eur"}) {
		t.Fatalf("permit targets: %+v", got)
	}
	invoice := map[string]any{"name": "record_invoice", "url": open.URL, "tool": "refund", "mode": "verify", "audience": "https://refunds.example.com/mcp",
		"idempotency_argument": "idem", "amount_argument": "total", "amount_unit": "EUR", "amount_scale": 2, "amount_format": "decimal_string"}
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", invoice); status != http.StatusCreated {
		t.Fatalf("connect record_invoice: %d %s", status, body)
	}
	for _, got := range r.api.targets["owner-a"] {
		if want := got.Action == "record_invoice"; got.AmountDecimal != want || (got.AmountScale == 2) != want {
			t.Fatalf("%s: decimal %v scale %d", got.Action, got.AmountDecimal, got.AmountScale)
		}
	}
}

// openToolServer lists its tools to anyone: a tool in verify mode checks
// KIFF's execution permit on each call instead of a credential.
func openToolServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(&mcp.Server{Name: "tool", Version: "1", Auth: func(*http.Request) (mcp.Handler, error) {
		return refunds{}, nil
	}})
	t.Cleanup(srv.Close)
	return srv
}

// Moving a connection to verify deletes the stored
// credential in the same change; verify refuses a credential; moving back
// to sealed needs the credential again, and it must work first.
func TestCustodyModeMoves(t *testing.T) {
	r := newRig(t, dev)
	ctx := context.Background()
	open := openToolServer(t)
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", r.refund(toolSecret)); status != http.StatusCreated ||
		!strings.Contains(body, `"mode":"sealed"`) {
		t.Fatalf("create: %d %s", status, body)
	}
	if status, body := r.do(t, "owner-a", http.MethodPut, "/v1/connections/issue_refund/mode",
		map[string]string{"mode": "verify", "audience": "https://refunds.example.com/mcp", "url": open.URL}); status != http.StatusOK ||
		!strings.Contains(body, `"mode":"verify"`) || !strings.Contains(body, `"has_credential":false`) ||
		!strings.Contains(body, `"audience":"https://refunds.example.com/mcp"`) {
		t.Fatalf("to verify: %d %s", status, body)
	}
	var sealed []byte
	_ = r.store.Pool.QueryRow(ctx, `SELECT credential FROM gateway.tools WHERE tenant_id='tenant-a'`).Scan(&sealed)
	if sealed != nil {
		t.Fatal("a verify connection kept its credential")
	}
	// apps/api is told where the permit goes, with the owner's key: the
	// decide path signs this target, never one a decide request names.
	if got := r.api.targets["owner-a"]; len(got) != 1 || got[0] != (kiffapi.PermitTarget{Domain: "gateway", Action: "issue_refund", Tool: "refund",
		Audience: "https://refunds.example.com/mcp", AmountArg: "amount_eur"}) {
		t.Fatalf("permit targets: %+v", got)
	}
	if status, _ := r.do(t, "owner-a", http.MethodPut, "/v1/connections/issue_refund/credential", map[string]string{"credential": toolSecret}); status != http.StatusBadRequest {
		t.Fatalf("credential stored on a verify connection: %d", status)
	}
	if _, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections/issue_refund/test", nil); !strings.Contains(body, "without a credential") {
		t.Fatalf("test: %s", body)
	}
	if status, _ := r.do(t, "owner-a", http.MethodPut, "/v1/connections/issue_refund/mode", map[string]string{"mode": "sealed"}); status != http.StatusBadRequest {
		t.Fatalf("back to sealed without a credential: %d", status)
	}
	if status, body := r.do(t, "owner-a", http.MethodPut, "/v1/connections/issue_refund/mode",
		map[string]string{"mode": "sealed", "url": r.tool.URL, "credential": "sk_wrong_value"}); status != http.StatusBadGateway ||
		strings.Contains(body, "sk_wrong_value") {
		t.Fatalf("back to sealed with a wrong credential: %d %s", status, body)
	}
	if status, body := r.do(t, "owner-a", http.MethodPut, "/v1/connections/issue_refund/mode",
		map[string]string{"mode": "sealed", "url": r.tool.URL, "credential": toolSecret}); status != http.StatusOK || !strings.Contains(body, `"has_credential":true`) {
		t.Fatalf("back to sealed: %d %s", status, body)
	}
	if got, _ := r.store.Credential(ctx, "tenant-a", "issue_refund"); got != toolSecret {
		t.Fatal("credential not stored again")
	}
	if got := r.api.targets["owner-a"]; len(got) != 0 {
		t.Fatalf("a sealed connection kept a permit target: %+v", got)
	}
}

// If apps/api does not take the permit target, the mode does not change.
func TestCustodyModeNeedsTheTargetRecorded(t *testing.T) {
	r := newRig(t, dev)
	open := openToolServer(t)
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", r.refund(toolSecret)); status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	r.api.mu.Lock()
	r.api.fail = true
	r.api.mu.Unlock()
	if status, _ := r.do(t, "owner-a", http.MethodPut, "/v1/connections/issue_refund/mode",
		map[string]string{"mode": "verify", "audience": "https://refunds.example.com/mcp", "url": open.URL}); status != http.StatusUnprocessableEntity {
		t.Fatalf("mode changed without its target: %d", status)
	}
	if c, _ := r.store.Get(context.Background(), "tenant-a", "issue_refund"); c.Mode != "sealed" || !c.HasCredential {
		t.Fatalf("connection changed: %+v", c)
	}
}

// verify and relay need an audience and an idempotency argument, and
// verify takes no credential; the catalog carries the mode to the gateway.
func TestCustodyModeIsValidated(t *testing.T) {
	r := newRig(t, dev)
	open := openToolServer(t)
	base := func() map[string]any {
		return map[string]any{"name": "issue_refund", "url": open.URL, "tool": "refund",
			"amount_argument": "amount_eur", "idempotency_argument": "idem", "mode": "verify", "audience": "https://refunds.example.com/mcp"}
	}
	for name, change := range map[string]func(map[string]any){
		"no audience":       func(m map[string]any) { delete(m, "audience") },
		"no idempotency":    func(m map[string]any) { delete(m, "idempotency_argument") },
		"credential":        func(m map[string]any) { m["credential"] = "x" },
		"unknown mode":      func(m map[string]any) { m["mode"] = "vault" },
		"sealed + audience": func(m map[string]any) { m["mode"] = "sealed" },
	} {
		m := base()
		change(m)
		if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", m); status != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, status, body)
		}
	}
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", base()); status != http.StatusCreated {
		t.Fatalf("verify create: %d %s", status, body)
	}
	tools, err := gateway.PostgresCatalog{Pool: r.store.Pool}.Tools(context.Background(), "tenant-a")
	if err != nil || len(tools) != 1 || tools[0].Mode != gateway.ModeVerify || tools[0].Audience != "https://refunds.example.com/mcp" {
		t.Fatalf("catalog: %+v %v", tools, err)
	}
}

// A connector key connects
// tools in relay or verify mode and tests or removes only the ones it
// connected. It can't re-point, re-key, re-configure or remove a tool the
// owner connected, nor one another connector key connected.
func TestConnectorKeyManagesOnlyItsOwnTools(t *testing.T) {
	r := newRig(t, dev)
	ctx := context.Background()
	open := openToolServer(t)
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", r.refund(toolSecret)); status != http.StatusCreated {
		t.Fatalf("owner connects: %d %s", status, body)
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, "/v1/connections/issue_refund/mode", map[string]string{"mode": "relay", "audience": "https://evil.example/mcp", "url": open.URL}},
		{http.MethodPut, "/v1/connections/issue_refund/credential", map[string]string{"credential": "sk_other"}},
		{http.MethodPut, "/v1/connections/issue_refund/settings", map[string]string{"resource_argument": "idem"}},
		{http.MethodPost, "/v1/connections/issue_refund/test", nil},
		{http.MethodDelete, "/v1/connections/issue_refund", nil},
		{http.MethodPost, "/v1/connections", r.refund(toolSecret)},
	} {
		if status, body := r.do(t, "connector-a", c.method, c.path, c.body); status != http.StatusForbidden {
			t.Errorf("connector %s %s: %d %s", c.method, c.path, status, body)
		}
	}
	owned, err := r.store.Get(ctx, "tenant-a", "issue_refund")
	if err != nil || owned.Mode != "sealed" || owned.URL != r.tool.URL || !owned.HasCredential {
		t.Fatalf("the owner's tool changed: %+v %v", owned, err)
	}

	verify := map[string]any{"name": "planque_refund", "url": open.URL, "tool": "refund", "mode": "verify",
		"audience": "https://planque.example/v1/relay", "amount_argument": "amount_eur", "idempotency_argument": "idem"}
	if status, body := r.do(t, "connector-a", http.MethodPost, "/v1/connections", verify); status != http.StatusCreated {
		t.Fatalf("connector connects in verify mode: %d %s", status, body)
	}
	var by string
	if err := r.store.Pool.QueryRow(ctx, `SELECT created_by FROM gateway.tools WHERE tenant_id='tenant-a' AND name='planque_refund'`).Scan(&by); err != nil || by != "key-connector-a" {
		t.Fatalf("created_by = %q %v", by, err)
	}
	for _, c := range [][2]string{{http.MethodPost, "/v1/connections/planque_refund/test"}, {http.MethodDelete, "/v1/connections/planque_refund"}} {
		if status, body := r.do(t, "connector-a2", c[0], c[1], nil); status != http.StatusForbidden {
			t.Errorf("another connector %s %s: %d %s", c[0], c[1], status, body)
		}
	}
	if status, body := r.do(t, "connector-a", http.MethodPost, "/v1/connections/planque_refund/test", nil); status != http.StatusOK || !strings.Contains(body, `"ok":true`) {
		t.Errorf("connector tests its tool: %d %s", status, body)
	}
	if status, body := r.do(t, "connector-a", http.MethodPut, "/v1/connections/planque_refund/mode", map[string]string{"mode": "sealed", "url": r.tool.URL, "credential": toolSecret}); status != http.StatusForbidden {
		t.Errorf("connector changes its tool's mode: %d %s", status, body)
	}
	if status, body := r.do(t, "connector-a", http.MethodDelete, "/v1/connections/planque_refund", nil); status != http.StatusNoContent {
		t.Fatalf("connector removes its tool: %d %s", status, body)
	}
	if _, err := r.store.Get(ctx, "tenant-a", "planque_refund"); !errors.Is(err, connection.ErrNotFound) {
		t.Fatalf("removed tool still there: %v", err)
	}
	if _, err := r.store.Get(ctx, "tenant-a", "issue_refund"); err != nil {
		t.Fatalf("the owner's tool went: %v", err)
	}
}

// A connected tool keeps the name people see: the
// vendor's own MCP title, unless the connect asks for one; never KIFF's
// name for it. Discover shows the vendor's title to the connect form.
func TestAToolKeepsTheVendorsTitle(t *testing.T) {
	r := newRig(t, dev)
	status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", r.refund(toolSecret))
	if status != http.StatusCreated || !strings.Contains(body, `"title":"Refund a payment"`) {
		t.Fatalf("create: %d %s", status, body)
	}
	asked := r.refund(toolSecret)
	asked["name"], asked["title"] = "other_refund", "  Refund an order  "
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", asked); status != http.StatusCreated || !strings.Contains(body, `"title":"Refund an order"`) {
		t.Fatalf("an asked title: %d %s", status, body)
	}
	long := r.refund(toolSecret)
	long["name"], long["title"] = "long_refund", strings.Repeat("x", 101)
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", long); status != http.StatusBadRequest || !strings.Contains(body, "at most 100") {
		t.Fatalf("a long title: %d %s", status, body)
	}
	conns, _ := r.store.List(context.Background(), "tenant-a")
	titles := map[string]string{}
	for _, c := range conns {
		titles[c.Name] = c.Title
	}
	if titles["issue_refund"] != "Refund a payment" || titles["other_refund"] != "Refund an order" {
		t.Fatalf("stored titles: %v", titles)
	}
	status, body = r.do(t, "owner-a", http.MethodPost, "/v1/connections/discover", map[string]any{"url": r.tool.URL, "credential": toolSecret})
	if status != http.StatusOK || !strings.Contains(body, `"title":"Refund a payment"`) {
		t.Fatalf("discover: %d %s", status, body)
	}
}

// The account's own control server (kiff/tafs#206): its tools, writes
// included, go to setup_reader, so Setup can act there as Coach on its own
// Cards. Only a person connects one, only in verify mode, and it stays
// there. A vendor's tool that writes never goes to setup_reader.
func TestControlServer(t *testing.T) {
	r := newRig(t, dev)
	open := openToolServer(t)
	control := func(name string) map[string]any {
		return map[string]any{"name": name, "url": open.URL, "tool": "refund", "mode": "verify",
			"audience": "https://tafs.example/mcp", "idempotency_argument": "idem", "control": true}
	}
	// Attack: a vendor's tool, KIFF holding its credential, marked control.
	sealed := r.refund(toolSecret)
	sealed["control"] = true
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", sealed); status != http.StatusForbidden {
		t.Fatalf("control on a sealed tool: %d %s", status, body)
	}
	// Attack: planque's connector key marks its tool control.
	if status, body := r.do(t, "connector-a", http.MethodPost, "/v1/connections", control("planque_task")); status != http.StatusForbidden {
		t.Fatalf("control from a connector key: %d %s", status, body)
	}
	// A vendor tool that writes, connected as usual: not setup_reader's.
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", r.refund(toolSecret)); status != http.StatusCreated {
		t.Fatalf("vendor tool: %d %s", status, body)
	}
	if strings.Contains(r.api.applied["owner-a"], "setup_reader") {
		t.Fatalf("a vendor's write went to setup_reader:\n%s", r.api.applied["owner-a"])
	}
	// The owner connects the account's own server, control.
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", control("taf_task")); status != http.StatusCreated ||
		!strings.Contains(body, `"control":true`) {
		t.Fatalf("control server: %d %s", status, body)
	}
	if !strings.Contains(r.api.applied["owner-a"], "    setup_reader: [tools.taf_task]\n") {
		t.Fatalf("setup_reader without the control server's tool:\n%s", r.api.applied["owner-a"])
	}
	// It stays in verify mode.
	if status, body := r.do(t, "owner-a", http.MethodPut, "/v1/connections/taf_task/mode",
		map[string]string{"mode": "relay", "audience": "https://evil.example/mcp", "url": open.URL}); status != http.StatusForbidden {
		t.Fatalf("control server moved out of verify: %d %s", status, body)
	}
}

// The owner connects the account's control server from their phone
// (kiff/tafs#206): only control, only with the phone's signature over that
// exact request, which KIFF confirms and accepts once, and only for a person
// who may connect tools. It is filed as a person's, not a key's.
func TestPhoneConnectsTheControlServerSigned(t *testing.T) {
	r := newRig(t, dev)
	open := openToolServer(t)
	body := map[string]any{"name": "taf_task", "url": open.URL, "tool": "refund", "mode": "verify",
		"audience": "https://tafs.example/mcp", "idempotency_argument": "idem", "control": true}
	raw, _ := json.Marshal(body)
	sum := sha256.Sum256(raw)
	post := func(key, sig string, b any) (int, string) {
		t.Helper()
		var payload []byte
		if bb, ok := b.([]byte); ok {
			payload = bb
		} else {
			payload, _ = json.Marshal(b)
		}
		req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/v1/connections", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+key)
		if sig != "" {
			req.Header.Set(kiffapi.DeviceSignatureHeader, sig)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return resp.StatusCode, buf.String()
	}
	// Attack: a phone's session connects a tool that isn't the control server.
	plain := map[string]any{"name": "issue_refund", "url": r.tool.URL, "tool": "refund", "credential": toolSecret}
	if status, body := post("phone-a", "t=ok", plain); status != http.StatusForbidden {
		t.Fatalf("a phone connects an ordinary tool: %d %s", status, body)
	}
	// Attack: the control server without the phone's signature, or another one.
	for _, sig := range []string{"", "t=forged"} {
		if status, body := post("phone-a", sig, raw); status != http.StatusForbidden {
			t.Fatalf("signature %q: %d %s", sig, status, body)
		}
	}
	// A person who can't connect tools isn't let in at all.
	if status, body := post("phone-approver", "t=ok", raw); status != http.StatusForbidden {
		t.Fatalf("a phone without tools.connect: %d %s", status, body)
	}
	r.api.mu.Lock()
	r.api.signDown = true
	r.api.mu.Unlock()
	if status, body := post("phone-a", "t=ok", raw); status != http.StatusServiceUnavailable {
		t.Fatalf("KIFF can't check the signature: %d %s", status, body)
	}
	r.api.mu.Lock()
	r.api.signDown = false
	r.api.mu.Unlock()
	if status, body := post("phone-a", "t=ok", raw); status != http.StatusCreated || !strings.Contains(body, `"control":true`) {
		t.Fatalf("the owner's phone, signed: %d %s", status, body)
	}
	// KIFF was asked about this exact request.
	want := "phone-a POST /v1/connections " + hex.EncodeToString(sum[:])
	if asks := r.api.signedAsks; len(asks) == 0 || asks[len(asks)-1] != want {
		t.Fatalf("asked KIFF about %v, want %q", asks, want)
	}
	if !strings.Contains(r.api.applied["phone-a"], "    setup_reader: [tools.taf_task]\n") {
		t.Fatalf("contract:\n%s", r.api.applied["phone-a"])
	}
	conn, err := r.store.Get(context.Background(), "tenant-a", "taf_task")
	if err != nil || conn.CreatedBy != "" || !conn.Control {
		t.Fatalf("stored %+v %v: want a person's control server", conn, err)
	}
	// The same signed request again is refused (KIFF accepts it once).
	_, _ = r.do(t, "owner-a", http.MethodDelete, "/v1/connections/taf_task", nil)
	if status, body := post("phone-a", "t=ok", raw); status != http.StatusForbidden {
		t.Fatalf("the signed request replayed: %d %s", status, body)
	}
}

// A vendor connected by API key reaches the gateway as planque registers it
// (amount lookups): its connector key connects each operation in
// relay mode, with planque's transport credential and the permit-bound
// idempotency argument; a write with terminal_no_effect_v1, a read without.
// From there it is a connected tool like any other: the same contract applied,
// the same permits and record, and a re-send of an existing name is 409.
func TestAPIKeyGrantToolsConnectLikeAnyTool(t *testing.T) {
	r := newRig(t, dev)
	ctx := context.Background()
	open := httptest.NewServer(&mcp.Server{Name: "planque", Version: "1", Auth: func(*http.Request) (mcp.Handler, error) {
		return refunds{status: true}, nil
	}})
	t.Cleanup(open.Close)
	write := map[string]any{"name": "vendor_refund", "url": open.URL, "tool": "refund", "mode": "relay",
		"audience": "https://planque.example/v1/relay", "credential": "planque-transport", "amount_argument": "amount_eur",
		"idempotency_argument": "idem", "outcome_contract": "terminal_no_effect_v1"}
	if status, body := r.do(t, "connector-a", http.MethodPost, "/v1/connections", write); status != http.StatusCreated ||
		!strings.Contains(body, `"mode":"relay"`) || !strings.Contains(body, `"outcome_contract":"terminal_no_effect_v1"`) || strings.Contains(body, "planque-transport") {
		t.Fatalf("connector connects an API-key write: %d %s", status, body)
	}
	read := map[string]any{"name": "vendor_refund_status", "url": open.URL, "tool": "refund_status", "mode": "relay",
		"audience": "https://planque.example/v1/relay", "credential": "planque-transport", "idempotency_argument": "idem", "read_only": true}
	if status, body := r.do(t, "connector-a", http.MethodPost, "/v1/connections", read); status != http.StatusCreated || strings.Contains(body, "outcome_contract") {
		t.Fatalf("connector connects an API-key read: %d %s", status, body)
	}
	if y := r.api.applied["connector-a"]; !strings.Contains(y, "name: vendor_refund") || !strings.Contains(y, "required_parameters: [amount_eur]") {
		t.Fatalf("contract not applied: %q", y)
	}
	tools, err := (&gateway.PostgresCatalog{Pool: r.store.Pool}).Tools(ctx, "tenant-a")
	if err != nil || len(tools) != 2 {
		t.Fatalf("catalog: %+v %v", tools, err)
	}
	for _, tool := range tools {
		want := ""
		if tool.Name == "vendor_refund" {
			want = gateway.TerminalNoEffectV1
		}
		if tool.Mode != gateway.ModeRelay || tool.IdempotencyArg != "idem" || tool.OutcomeContract != want {
			t.Errorf("%s: mode %q idempotency %q contract %q", tool.Name, tool.Mode, tool.IdempotencyArg, tool.OutcomeContract)
		}
	}
	if status, body := r.do(t, "connector-a", http.MethodPost, "/v1/connections", write); status != http.StatusConflict {
		t.Fatalf("re-sent tool: %d %s", status, body)
	}
}

// Attack: a person marks a vendor's server, fronted in verify mode by planque,
// as the account's control server, handing its writes to setup_reader.
// Refused: control is refused on a host where a connector key connected a tool.
func TestControlRefusedWhereAConnectorConnects(t *testing.T) {
	r := newRig(t, dev)
	relay, own := openToolServer(t), openToolServer(t)
	vendor := map[string]any{"name": "planque_refund", "url": relay.URL, "tool": "refund", "mode": "verify",
		"audience": "https://planque.example/v1/relay", "amount_argument": "amount_eur", "idempotency_argument": "idem"}
	if status, body := r.do(t, "connector-a", http.MethodPost, "/v1/connections", vendor); status != http.StatusCreated {
		t.Fatalf("planque connects a vendor tool: %d %s", status, body)
	}
	control := func(name, url string) map[string]any {
		return map[string]any{"name": name, "url": url, "tool": "refund", "mode": "verify",
			"audience": "https://tafs.example/mcp", "idempotency_argument": "idem", "control": true}
	}
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", control("relay_task", relay.URL)); status != http.StatusForbidden {
		t.Fatalf("control on planque's host: %d %s", status, body)
	}
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", control("taf_task", own.URL)); status != http.StatusCreated {
		t.Fatalf("control on the account's own server: %d %s", status, body)
	}
	// The other order: a relay the KIFF knows (KIFF_PLANQUE_URLS), before
	// planque connected anything there.
	fresh := openToolServer(t)
	r.h.RelayURLs = []string{fresh.URL + "/v1/relay"}
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", control("relay_first", fresh.URL)); status != http.StatusForbidden {
		t.Fatalf("control on a known relay's host: %d %s", status, body)
	}
	if y := r.api.applied["owner-a"]; !strings.Contains(y, "    setup_reader: [tools.taf_task]\n") {
		t.Fatalf("contract:\n%s", y)
	}
}
