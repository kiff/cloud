package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
)

// isolationRig serves the Card routes behind the real auth middleware, so
// each request is made with a minted key, as an agent would make it.
type isolationRig struct {
	srv   http.Handler
	mux   *http.ServeMux // the routes without the middleware, for people
	store *mandate.MemoryStore
	keys  map[string]string // name -> plaintext
}

func newIsolationRig(t *testing.T) *isolationRig {
	t.Helper()
	ctx := context.Background()
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	grants := []mandate.Grant{{Action: "refund"}}
	for _, m := range []mandate.Mandate{
		{ID: "card-a", Subject: "agent-a", Domain: "gateway", Grants: grants},
		{ID: "card-b", Subject: "agent-b", Domain: "gateway", Grants: grants},
		{ID: "card-shared", Subject: "agent-b", Holders: []string{"agent-b", "agent-a"}, Domain: "gateway", Grants: grants},
		{ID: "card-c", Subject: "agent-c", Domain: "billing", Grants: grants},
	} {
		m.TenantID = "acme"
		if err := store.Issue(ctx, m, "tester"); err != nil {
			t.Fatalf("seed %s: %v", m.ID, err)
		}
	}
	h := NewMandatesHandler(store, nil, WithMandatesClock(func() time.Time {
		return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	}))
	mux := http.NewServeMux()
	h.Register(mux)

	keys := auth.NewMemoryAPIKeyStore()
	r := &isolationRig{keys: map[string]string{}, store: store, mux: mux}
	for name, rec := range map[string]auth.APIKeyRecord{
		"gateway-a":       {Roles: []string{auth.RoleGatewayAgent}, AgentID: "agent-a"},
		"gateway-b":       {Roles: []string{auth.RoleGatewayAgent}, AgentID: "agent-b"},
		"runtime-a":       {Roles: []string{"ops_agent"}, AgentID: "agent-a"},
		"runtime-b":       {Roles: []string{"ops_agent"}, AgentID: "agent-b"},
		"runtime-unbound": {Roles: []string{"ops_agent"}},
		"admin":           {Roles: []string{"admin"}},
	} {
		gen, err := auth.NewAPIKey("acme", time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		rec.ID, rec.Tenant, rec.Subject, rec.KeyHash, rec.CreatedAt = "key-"+name, "acme", "ops", gen.Hash, time.Now()
		if err := keys.Insert(ctx, rec); err != nil {
			t.Fatal(err)
		}
		r.keys[name] = gen.Plaintext
	}
	mw := auth.NewMiddleware(auth.NewStubProvider(auth.NewMapResolver()), keys, auth.NewMapResolver())
	r.srv = mw.Wrap(mux)
	return r
}

func (r *isolationRig) get(t *testing.T, key, path string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+r.keys[key])
	rec := httptest.NewRecorder()
	r.srv.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (r *isolationRig) listed(t *testing.T, key string) []string {
	t.Helper()
	code, body := r.get(t, key, "/v1/me/mandates")
	if code != http.StatusOK {
		t.Fatalf("%s list: %d %s", key, code, body)
	}
	var out struct {
		Mandates []struct {
			ID string `json:"id"`
		} `json:"mandates"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(out.Mandates))
	for _, m := range out.Mandates {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids
}

// Invariant: a key bound to an agent reads only the Cards that agent
// holds, alone or shared; another agent's Card is not listed and its
// statement is not found, even by its id. An agent's gateway key
// does not reach these routes at all.
// Category: authority
// Attack: agent A's key lists the account's Cards, or asks for agent B's Card statement by id
// Refused: gateway keys stop at the gateway route list; other agent keys see only Cards their agent holds
func TestAgentKeyReadsOnlyCardsItsAgentHolds(t *testing.T) {
	r := newIsolationRig(t)
	for _, key := range []string{"gateway-a", "gateway-b"} {
		for _, path := range []string{"/v1/me/mandates", "/v1/me/mandates/card-a/statement", "/v1/me/mandates/card-b/statement"} {
			if code, body := r.get(t, key, path); code != http.StatusForbidden {
				t.Errorf("%s %s: %d, want 403 (%s)", key, path, code, body)
			}
		}
	}
	want := map[string][]string{
		"runtime-a": {"card-a", "card-shared"},
		"runtime-b": {"card-b", "card-shared"},
		"admin":     {"card-a", "card-b", "card-c", "card-shared"},
	}
	for key, w := range want {
		if got := r.listed(t, key); fmt.Sprint(got) != fmt.Sprint(w) {
			t.Errorf("%s lists %v, want %v", key, got, w)
		}
	}
	statement := map[string]map[string]int{
		"runtime-a": {"card-a": 200, "card-shared": 200, "card-b": 404, "card-c": 404, "card-none": 404},
		"runtime-b": {"card-b": 200, "card-shared": 200, "card-a": 404, "card-c": 404},
		"admin":     {"card-a": 200, "card-b": 200, "card-c": 200, "card-shared": 200},
	}
	for key, cards := range statement {
		for card, w := range cards {
			if code, body := r.get(t, key, "/v1/me/mandates/"+card+"/statement"); code != w {
				t.Errorf("%s statement %s: %d, want %d (%s)", key, card, code, w, body)
			}
		}
	}
}
