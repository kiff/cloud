package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/guard"
)

func TestGuardHandlerConnectAndList(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	store := guard.NewMemoryStore()
	h := NewGuardHandler(store, WithGuardClock(func() time.Time { return now }))

	mux := http.NewServeMux()
	h.Register(mux)

	body := []byte(`{
		"project":"finance",
		"environment":"prod",
		"agent_id":"ap-agent",
		"workflow":"invoice-payments",
		"adapter":"openclaw",
		"sdk_version":"0.1.0",
		"mode":"observe"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/guard/connect", bytes.NewReader(body))
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
		Subject: "key-1",
		Tenant:  "tenant-a",
		Method:  auth.MethodAPIKey,
		APIKey:  true,
	}))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("connect status = %d body=%s", rr.Code, rr.Body.String())
	}
	var connected struct {
		RuntimeID         string           `json:"runtime_id"`
		HeartbeatInterval string           `json:"heartbeat_interval"`
		TenantID          string           `json:"tenant_id"`
		Project           string           `json:"project"`
		Environment       string           `json:"environment"`
		AgentID           string           `json:"agent_id"`
		Workflow          string           `json:"workflow"`
		Adapter           string           `json:"adapter"`
		SDKVersion        string           `json:"sdk_version"`
		Mode              string           `json:"mode"`
		SeenCount         uint64           `json:"seen_count"`
		Connection        guard.Connection `json:"connection"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &connected); err != nil {
		t.Fatalf("decode connect: %v", err)
	}
	if connected.TenantID != "tenant-a" || connected.AgentID != "ap-agent" || connected.Workflow != "invoice-payments" {
		t.Fatalf("top-level connection mismatch: %+v", connected)
	}
	if connected.RuntimeID == "" || connected.HeartbeatInterval != "60s" {
		t.Fatalf("runtime metadata mismatch: %+v", connected)
	}
	if connected.Connection.TenantID != "tenant-a" || connected.Connection.AgentID != "ap-agent" {
		t.Fatalf("nested connection mismatch: %+v", connected.Connection)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/guard/connections", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
		Subject: "key-1",
		Tenant:  "tenant-a",
		Method:  auth.MethodAPIKey,
		APIKey:  true,
	}))
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rr.Code, rr.Body.String())
	}
	var listed struct {
		Connections []guard.Connection `json:"connections"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Connections) != 1 || listed.Connections[0].Workflow != "invoice-payments" {
		t.Fatalf("list mismatch: %+v", listed.Connections)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/guard/runtimes", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
		Subject: "key-1",
		Tenant:  "tenant-a",
		Method:  auth.MethodAPIKey,
		APIKey:  true,
	}))
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("runtimes status = %d body=%s", rr.Code, rr.Body.String())
	}
	var runtimes struct {
		Runtimes []struct {
			RuntimeID         string    `json:"runtime_id"`
			HeartbeatInterval string    `json:"heartbeat_interval"`
			TenantID          string    `json:"tenant_id"`
			AgentID           string    `json:"agent_id"`
			Workflow          string    `json:"workflow"`
			LastSeenAt        time.Time `json:"last_seen_at"`
		} `json:"runtimes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &runtimes); err != nil {
		t.Fatalf("decode runtimes: %v", err)
	}
	if len(runtimes.Runtimes) != 1 || runtimes.Runtimes[0].RuntimeID == "" || runtimes.Runtimes[0].Workflow != "invoice-payments" {
		t.Fatalf("runtimes mismatch: %+v", runtimes.Runtimes)
	}
	if runtimes.Runtimes[0].HeartbeatInterval != "60s" {
		t.Fatalf("heartbeat interval = %q, want 60s", runtimes.Runtimes[0].HeartbeatInterval)
	}
}

// connect stamps the authenticating API key's id onto the runtime so the
// dashboard can revoke exactly that key; a later JWT-authed touch (no key
// id) must not wipe the last known key.
func TestGuardHandlerConnectStampsKeyID(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	store := guard.NewMemoryStore()
	h := NewGuardHandler(store, WithGuardClock(func() time.Time { return now }))
	mux := http.NewServeMux()
	h.Register(mux)

	body := []byte(`{"agent_id":"ap-agent","adapter":"openclaw","mode":"observe"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/guard/connect", bytes.NewReader(body))
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
		Subject: "principal-1", Tenant: "tenant-a", Method: auth.MethodAPIKey, APIKey: true,
		Metadata: map[string]string{"api_key_id": "key-abc"},
	}))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("connect status = %d body=%s", rr.Code, rr.Body.String())
	}
	var connected struct {
		KeyID      string           `json:"key_id"`
		Connection guard.Connection `json:"connection"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &connected); err != nil {
		t.Fatalf("decode connect: %v", err)
	}
	if connected.KeyID != "key-abc" || connected.Connection.KeyID != "key-abc" {
		t.Fatalf("key_id not stamped: top=%q nested=%q", connected.KeyID, connected.Connection.KeyID)
	}

	// A JWT-authed heartbeat (no api_key_id) must not wipe the key.
	req = httptest.NewRequest(http.MethodPost, "/v1/guard/connect", bytes.NewReader(body))
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
		Subject: "principal-1", Tenant: "tenant-a",
	}))
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("second connect status = %d body=%s", rr.Code, rr.Body.String())
	}
	var again struct {
		KeyID string `json:"key_id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &again)
	if again.KeyID != "key-abc" {
		t.Fatalf("JWT touch wiped key_id: got %q, want key-abc", again.KeyID)
	}
}

func TestGuardHandlerRejectsInvalidConnection(t *testing.T) {
	h := NewGuardHandler(guard.NewMemoryStore())
	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest(http.MethodPost, "/v1/guard/connect", bytes.NewReader([]byte(`{"adapter":"openclaw"}`)))
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{Subject: "key-1", Tenant: "tenant-a"}))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGuardHandlerObservations(t *testing.T) {
	now := time.Date(2026, 6, 1, 13, 0, 0, 0, time.UTC)
	store := guard.NewMemoryStore()
	h := NewGuardHandler(store, WithGuardClock(func() time.Time { return now }))

	mux := http.NewServeMux()
	h.Register(mux)

	body := []byte(`{
		"project":"cookbook",
		"environment":"aws",
		"agent_id":"ap-agent",
		"workflow":"duplicate-payment",
		"adapter":"openclaw",
		"sdk_version":"0.1.0",
		"mode":"enforce",
		"tools":[{
			"name":"pay_invoice",
			"description":"Pay an outstanding invoice",
			"entity_arg":"invoice_id",
			"action":"PAY_INVOICE",
			"entity_type":"Invoice",
			"required":["amount_cents"],
			"parameter_schema":{"type":"object","properties":{"invoice_id":{"type":"string"},"amount_cents":{"type":"integer"}}}
		}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/guard/observations", bytes.NewReader(body))
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
		Subject: "key-1",
		Tenant:  "tenant-a",
		Method:  auth.MethodAPIKey,
		APIKey:  true,
	}))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("observation status = %d body=%s", rr.Code, rr.Body.String())
	}
	var saved struct {
		Observation guard.Observation `json:"observation"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode observation: %v", err)
	}
	if len(saved.Observation.Tools) != 1 || saved.Observation.Tools[0].Name != "pay_invoice" {
		t.Fatalf("observation tools mismatch: %+v", saved.Observation.Tools)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/guard/observations?project=cookbook&environment=aws&agent_id=ap-agent&workflow=duplicate-payment", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
		Subject: "key-1",
		Tenant:  "tenant-a",
		Method:  auth.MethodAPIKey,
		APIKey:  true,
	}))
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get observation status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Observation guard.Observation `json:"observation"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get observation: %v", err)
	}
	if got.Observation.AgentID != "ap-agent" || got.Observation.Tools[0].EntityArg != "invoice_id" {
		t.Fatalf("get observation mismatch: %+v", got.Observation)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/guard/connections", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
		Subject: "key-1",
		Tenant:  "tenant-a",
		Method:  auth.MethodAPIKey,
		APIKey:  true,
	}))
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list after observation status = %d body=%s", rr.Code, rr.Body.String())
	}
	var listed struct {
		Connections []guard.Connection `json:"connections"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list after observation: %v", err)
	}
	if len(listed.Connections) != 1 || listed.Connections[0].SeenCount != 1 {
		t.Fatalf("observation did not touch runtime: %+v", listed.Connections)
	}
}

// the agent-as-unit design: a guard runtime authenticated by a key bound to an agent
// registers and reports as that agent. An empty agent_id is filled from
// the key; a different one is refused, so a runtime cannot heartbeat
// under a name its credential does not hold. Unbound keys still name
// the agent themselves.
func TestGuardHandler_BoundKeyCarriesTheAgent(t *testing.T) {
	store := guard.NewMemoryStore()
	h := NewGuardHandler(store, WithGuardClock(func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }))
	mux := http.NewServeMux()
	h.Register(mux)

	send := func(id auth.Identity, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req = req.WithContext(auth.WithIdentity(req.Context(), id))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	bound := auth.Identity{Tenant: "t1", Subject: "owner", Method: auth.MethodAPIKey, APIKey: true,
		AgentID: "refund-agent", Metadata: map[string]string{"api_key_id": "key-1"}}

	rec := send(bound, "/v1/guard/connect", `{"adapter":"agno","mode":"observe"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"agent_id":"refund-agent"`) {
		t.Fatalf("connect without agent_id: %d %s", rec.Code, rec.Body.String())
	}
	if rec := send(bound, "/v1/guard/connect", `{"agent_id":"refund-agent","adapter":"agno","mode":"observe"}`); rec.Code != http.StatusOK {
		t.Errorf("connect naming its own agent: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/v1/guard/connect", "/v1/guard/observations"} {
		rec := send(bound, path, `{"agent_id":"other-agent","adapter":"agno","mode":"observe","tools":[]}`)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s as another agent: %d %s, want 403", path, rec.Code, rec.Body.String())
		}
	}
	rec = send(bound, "/v1/guard/observations", `{"adapter":"agno","mode":"observe","tools":[{"name":"refund_order"}]}`)
	if rec.Code >= 300 {
		t.Errorf("observation without agent_id: %d %s", rec.Code, rec.Body.String())
	}

	unbound := bound
	unbound.AgentID = ""
	if rec := send(unbound, "/v1/guard/connect", `{"agent_id":"any-agent","adapter":"agno","mode":"observe"}`); rec.Code != http.StatusOK {
		t.Errorf("unbound key naming its agent: %d %s", rec.Code, rec.Body.String())
	}

	conns, err := store.List(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range conns {
		seen[c.AgentID] = true
	}
	if !seen["refund-agent"] || seen["other-agent"] || !seen["any-agent"] {
		t.Errorf("registered agents = %v", seen)
	}
}
