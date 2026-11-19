package handlers_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/permit"
)

// Coach's tool server holds no key of the account's: the permit KIFF
// issued for Coach's call, naming the person (act.sub), is the
// credential. KIFF answers for that person, and gives their run grant for
// a taf they may use, once per permit and taf. A permit that isn't KIFF's,
// isn't for this account's Coach, or names no one, gets nothing.
func TestCoachActsForThePersonItsPermitNames(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	key := permit.Key{ID: "k1", Priv: priv}
	members := membership.NewMemoryStore()
	if err := members.Mutate(ctx, "t1", func(tx membership.Tx) error {
		for s, role := range map[string]membership.Role{"ana": membership.RoleApprover, "bo": membership.RoleViewer, "cy": membership.RoleEditor} {
			if err := tx.InsertMember(membership.Member{TenantID: "t1", Subject: s, Role: role, Status: membership.StatusActive}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	agentStore := agents.NewMemoryStore()
	for id, by := range map[string]string{"coach": "", "refunds": "", "bills": "cy"} {
		if err := agentStore.Create(ctx, agents.Agent{TenantID: "t1", ID: id, Kind: agents.KindTaf, CreatedAt: now, CreatedBy: by}); err != nil {
			t.Fatal(err)
		}
	}
	keys := auth.NewMemoryAPIKeyStore()
	if err := keys.Insert(ctx, auth.APIKeyRecord{ID: "k-coach", Tenant: "t1", Subject: "ana", Roles: []string{auth.RoleSetupReader},
		AgentID: "coach", KeyHash: [32]byte{1}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	grants := access.NewMemoryStore()
	for _, g := range []access.Grant{
		{TenantID: "t1", Agent: "refunds", Subject: "ana"},
		{TenantID: "t1", Agent: "refunds", Subject: "cy"},
		{TenantID: "t1", Agent: "bills", Subject: "cy", CreatedBy: "cy"},
	} {
		if err := grants.AddGrant(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	targets := permit.NewMemoryTargets()
	if err := targets.Replace(ctx, "t1", []permit.Target{{Domain: "gateway", Action: "taf_task", Tool: "taf_task", Audience: "https://tafs.example/v1/mcp"}}, "ana", now); err != nil {
		t.Fatal(err)
	}
	ah := handlers.NewAccessHandler(grants, members, agentStore, nil).WithKeys(keys)
	c := &handlers.CoachAct{Grants: &handlers.RunGrants{Key: key, Published: []permit.PublicKey{key.Public()}, Iss: "https://api.kiff.dev", Access: ah, Now: time.Now},
		Targets: targets, Used: auth.NewMemorySignedRequests()}
	mux := http.NewServeMux()
	c.Register(mux)

	signed := func(sub, aud, tenant string, act *permit.Actor, k permit.Key) string {
		t.Helper()
		tok, err := k.Sign(permit.Claims{Iss: "https://api.kiff.dev", Aud: aud, Sub: sub, Iat: now.Unix(), Exp: now.Add(time.Minute).Unix(), Jti: permit.NewJTI(),
			Details: []permit.Detail{{Type: permit.DetailType, Tenant: tenant, Tool: "taf_task"}}, Act: act})
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	ask := func(p, agent string) (int, map[string]any) {
		t.Helper()
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(map[string]string{"permit": p, "agent": agent})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/coach/act", &buf))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	const aud = "https://tafs.example/v1/mcp"
	anas := signed("coach", aud, "t1", &permit.Actor{Sub: "ana"}, key)
	code, out := ask(anas, "refunds")
	if code != http.StatusOK || out["use"] != true || out["grant"] == nil {
		t.Fatalf("Ana through Coach: %d %v", code, out)
	}
	if g, err := permit.VerifyRunGrant(out["grant"].(string), []permit.PublicKey{key.Public()}, now); err != nil || g.Sub != "ana" || g.Agent != "refunds" {
		t.Fatalf("the grant is Ana's for Refunds: %+v %v", g, err)
	}
	if code, out := ask(anas, "refunds"); code != http.StatusOK || out["grant"] != nil {
		t.Errorf("a second grant from the same permit: %d %v", code, out)
	}
	if code, out := ask(anas, ""); code != http.StatusOK || out["agents"].(map[string]any)["refunds"].(map[string]any)["use"] != true {
		t.Errorf("Ana's answer for every taf: %d %v", code, out)
	}
	if code, out := ask(signed("coach", aud, "t1", &permit.Actor{Sub: "bo"}, key), "refunds"); code != http.StatusOK || out["use"] == true || out["grant"] != nil {
		t.Errorf("Bo, Refunds not shared with him: %d %v", code, out)
	}
	// An editor uses Coach without it being shared, and through it manages
	// only the tafs they made: Bills, not Refunds, which is shared with them.
	cys := signed("coach", aud, "t1", &permit.Actor{Sub: "cy"}, key)
	if code, out := ask(cys, "coach"); code != http.StatusOK || out["use"] != true {
		t.Errorf("Cy, an editor, uses Coach: %d %v", code, out)
	}
	if code, out := ask(cys, "bills"); code != http.StatusOK || out["use"] != true || out["manage"] != true {
		t.Errorf("Cy manages Bills, which they made: %d %v", code, out)
	}
	if code, out := ask(cys, "refunds"); code != http.StatusOK || out["use"] != true || out["manage"] != false {
		t.Errorf("Cy uses Refunds, shared with them, but doesn't manage it through Coach: %d %v", code, out)
	}
	if code, out := ask(cys, ""); code != http.StatusOK || out["agents"].(map[string]any)["refunds"].(map[string]any)["manage"] != false ||
		out["agents"].(map[string]any)["bills"].(map[string]any)["manage"] != true {
		t.Errorf("Cy's answer for every taf: %d %v", code, out)
	}
	if code, out := ask(anas, "coach"); code != http.StatusOK || out["use"] == true {
		t.Errorf("Ana, an approver, has no Coach unless it's shared: %d %v", code, out)
	}
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	for name, p := range map[string]string{
		"names no one":     signed("coach", aud, "t1", nil, key),
		"not for Coach":    signed("refunds", aud, "t1", &permit.Actor{Sub: "ana"}, key),
		"another audience": signed("coach", "https://evil.example/v1/mcp", "t1", &permit.Actor{Sub: "ana"}, key),
		"another account":  signed("coach", aud, "t2", &permit.Actor{Sub: "ana"}, key),
		"not KIFF's":       signed("coach", aud, "t1", &permit.Actor{Sub: "ana"}, permit.Key{ID: "k1", Priv: otherPriv}),
	} {
		if code, out := ask(p, "refunds"); code != http.StatusUnauthorized || out["grant"] != nil {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
}
