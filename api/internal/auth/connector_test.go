package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/membership"
)

// A phone's signed setup call mints the connector key planque
// keeps, unbound, holding that role alone. An editor cannot (minting keys
// is an admin's act), and the role never goes with another or onto an
// agent's key.
func TestKeysHandler_PhoneMintsAConnectorKey(t *testing.T) {
	t.Parallel()
	store := NewMemoryAPIKeyStore()
	keys := NewKeysHandler(store, WithKeysClock(fixedClock(time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC))))
	mux := http.NewServeMux()
	keys.Register(mux)
	phone := Identity{Subject: "owner-1", Tenant: "tenant-acme", Method: MethodDeveloper, Roles: []string{"admin", "tenant_owner", "operator"},
		MemberRole: membership.RoleAdmin, DeviceKey: []byte{0x30}, DeviceSessionID: "sess-phone"}
	editor := phone
	editor.Roles, editor.MemberRole = []string{"editor", "operator"}, membership.RoleEditor
	mint := func(id Identity, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/keys", strings.NewReader(body))
		req = req.WithContext(WithIdentity(req.Context(), id))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	rec := mint(phone, `{"label":"planque","roles":["connector"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("a phone could not mint a connector key: %d %s", rec.Code, rec.Body.String())
	}
	var got CreateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Plaintext == "" || got.AgentID != "" || !slices.Equal(got.Roles, []string{RoleConnector}) {
		t.Fatalf("minted %+v", got)
	}
	listed, _ := store.ListActive(context.Background(), "tenant-acme")
	if len(listed) != 1 || listed[0].AgentID != "" || !slices.Equal(listed[0].Roles, []string{RoleConnector}) {
		t.Fatalf("stored %+v", listed)
	}

	for name, c := range map[string]struct {
		id   Identity
		body string
		code int
	}{
		"an editor":                {editor, `{"roles":["connector"]}`, http.StatusForbidden},
		"with another role":        {phone, `{"roles":["connector","admin"]}`, http.StatusBadRequest},
		"with a runtime role":      {phone, `{"roles":["connector","ops_agent"]}`, http.StatusBadRequest},
		"bound to an agent":        {phone, `{"roles":["connector"],"agent_id":"rae"}`, http.StatusBadRequest},
		"bound to an environment":  {phone, `{"roles":["connector"],"environment":"live"}`, http.StatusBadRequest},
		"an unbound role-less key": {phone, `{"roles":[]}`, http.StatusForbidden},
	} {
		if rec := mint(c.id, c.body); rec.Code != c.code {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body.String(), c.code)
		}
	}
	if listed, _ := store.ListActive(context.Background(), "tenant-acme"); len(listed) != 1 {
		t.Fatalf("a refused mint stored a key: %+v", listed)
	}
}

// The connector role is given only by minting. It is not added to
// an existing key, and a connector key is given no other role.
func TestKeysHandler_ConnectorRoleIsNotGranted(t *testing.T) {
	t.Parallel()
	store := NewMemoryAPIKeyStore()
	keys := NewKeysHandler(store)
	mux := http.NewServeMux()
	keys.Register(mux)
	ctx := context.Background()
	for _, rec := range []APIKeyRecord{
		{ID: "key-runtime", Tenant: "tenant-acme", Subject: "owner-1", Roles: []string{"ops_agent"}, CreatedAt: time.Now()},
		{ID: "key-connector", Tenant: "tenant-acme", Subject: "owner-1", Roles: []string{RoleConnector}, CreatedAt: time.Now()},
	} {
		gen, err := NewAPIKey(rec.Tenant, rec.CreatedAt)
		if err != nil {
			t.Fatal(err)
		}
		rec.KeyHash = gen.Hash
		if err := store.Insert(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	admin := Identity{Subject: "owner-1", Tenant: "tenant-acme", Method: MethodSession, Roles: []string{"admin"}}
	patch := func(key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/v1/keys/"+key, strings.NewReader(body))
		req = req.WithContext(WithIdentity(req.Context(), admin))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := patch("key-runtime", `{"add_roles":["connector"]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("added connector to a key: %d %s", rec.Code, rec.Body.String())
	}
	if rec := patch("key-connector", `{"add_roles":["admin"]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("added admin to a connector key: %d %s", rec.Code, rec.Body.String())
	}
	if rec := patch("key-connector", `{"add_roles":["ops_agent"]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("added a role to a connector key: %d %s", rec.Code, rec.Body.String())
	}
	recs, _ := store.ListActive(ctx, "tenant-acme")
	for _, r := range recs {
		if want := map[string][]string{"key-runtime": {"ops_agent"}, "key-connector": {RoleConnector}}[r.ID]; !slices.Equal(r.Roles, want) {
			t.Errorf("%s now holds %v", r.ID, r.Roles)
		}
	}
}

// A connector key holds read, personal and tools.connect, and acts
// for the account in nothing else, whatever other role its record holds.
func TestConnectorKeyCapabilities(t *testing.T) {
	t.Parallel()
	for _, roles := range [][]string{{RoleConnector}, {RoleConnector, "admin", "tenant_owner"}} {
		id := Identity{Subject: "owner-1", Tenant: "tenant-acme", Method: MethodAPIKey, APIKey: true, Roles: roles}
		if !id.IsConnector() || id.ManagesAccount() || id.isRuntimeKey() {
			t.Fatalf("%v: connector=%v manages=%v runtime=%v", roles, id.IsConnector(), id.ManagesAccount(), id.isRuntimeKey())
		}
		var held []membership.Capability
		for _, c := range membership.Capabilities(membership.RoleAdmin) {
			if id.Can(c) {
				held = append(held, c)
			}
		}
		if want := []membership.Capability{membership.CapRead, membership.CapPersonal, membership.CapToolsConnect}; !slices.Equal(held, want) {
			t.Errorf("%v holds %v, want %v", roles, held, want)
		}
	}
	person := Identity{Subject: "owner-1", Tenant: "tenant-acme", Method: MethodSession, Roles: []string{RoleConnector, "admin"}}
	if person.IsConnector() {
		t.Error("a person's session is never a connector key")
	}
}

// A connector key reaches who it is and the two writes a
// connection makes; nothing else, including routes added later.
func TestConnectorRouteAllowed(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/v1/me", true},
		{"POST", "/v1/me/domains", true},
		{"PUT", "/v1/me/domains/gateway", true},
		{"PUT", "/v1/me/permit-targets", true},
		{"PUT", "/v1/me/domains/gateway/mode", false},
		{"DELETE", "/v1/me/domains/gateway", false},
		{"GET", "/v1/me/domains/gateway", false},
		{"GET", "/v1/me/permit-targets", false},
		{"PUT", "/v1/me/domains/", false},
		{"POST", "/v1/me/mandates", false},
		{"POST", "/v1/keys", false},
		{"GET", "/v1/keys", false},
		{"POST", "/v1/proposals/decide", false},
		{"POST", "/v1/me/card-exceptions/x/approve-once", false},
		{"POST", "/v1/tenant/invites", false},
		{"GET", "/v1/me/stream", false},
		{"GET", "/v1/me/something-new", false},
		{"GET", "/v1/halt", true},
		{"GET", "/v1/agents/refund-agent/halt", true},
		{"GET", "/v1/agents/refund-agent~trial/halt", true},
		{"GET", "/v1/agents//halt", false},
		{"GET", "/v1/agents/a/b/halt", false},
		{"POST", "/v1/agents/refund-agent/halt", false},
		{"POST", "/v1/halt", false},
		{"POST", "/v1/resume", false},
		{"GET", "/v1/agents/refund-agent", false},
		{"POST", "/v1/me/setup-reads", true},
		{"GET", "/v1/me/setup-reads", false},
		{"POST", "/v1/me/mandates", false},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := ConnectorRouteAllowed(r); got != c.want {
			t.Errorf("%s %s: %v, want %v", c.method, c.path, got, c.want)
		}
	}
}
