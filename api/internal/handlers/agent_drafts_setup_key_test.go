package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
)

// mintFrom confirms a draft for agent, as its owner, with one Card, claims
// it, and answers the minted key's plaintext and roles.
func (r *draftRig) mintFrom(agent string) (string, []string) {
	r.t.Helper()
	code, d := r.submit(admin(), map[string]any{"agent_id": agent, "label": "Coach", "cards": []any{card(agent+"-refund", agent)}})
	if code != http.StatusCreated {
		r.t.Fatalf("%s: submit: %d %v", agent, code, d)
	}
	code, got := r.claim(d["id"].(string), d["claim_secret"].(string))
	plaintext, _ := got["plaintext"].(string)
	if code != http.StatusOK || plaintext == "" {
		r.t.Fatalf("%s: claim: %d %v", agent, code, got)
	}
	parsed, err := auth.ParseAPIKey(plaintext)
	if err != nil {
		r.t.Fatal(err)
	}
	rec, err := r.keys.GetByHash(context.Background(), parsed.Hash)
	if err != nil || rec.AgentID != agent {
		r.t.Fatalf("%s: minted key = %+v, %v", agent, rec, err)
	}
	return plaintext, rec.Roles
}

// A new account's first start drafts Coach's Cards at once (kiff/tafs#852),
// for the agent every app gives Setup. The key its claim mints is Setup's,
// setup_reader alone, so Coach's submit passes KIFF's own route check, where
// a gateway key is refused. A taf's Cards draft still mints a gateway key,
// and that check still refuses it the submit.
func TestSetupsCardsDraftMintsSetupsKey(t *testing.T) {
	r := newDraftRig(t)
	srv := auth.NewMiddleware(auth.NewStubProvider(auth.NewMapResolver()), r.keys, auth.NewMapResolver()).Wrap(r.mux)
	send := func(key string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/me/agent-drafts", &buf)
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	setupKey, roles := r.mintFrom(auth.SetupAgentID)
	if !slices.Equal(roles, []string{auth.RoleSetupReader}) {
		t.Fatalf("Setup's key from its Cards draft: roles %v, want [%s]", roles, auth.RoleSetupReader)
	}
	tafKey, roles := r.mintFrom("rae")
	if !slices.Equal(roles, []string{auth.RoleGatewayAgent}) {
		t.Fatalf("a taf's key: roles %v, want [%s]", roles, auth.RoleGatewayAgent)
	}

	// Coach's submit, a taf's draft for the owner, through the route check.
	nora := map[string]any{"agent_id": "nora", "label": "Nora", "cards": []any{card("nora-refund", "nora")}}
	if rr := send(setupKey, nora); rr.Code != http.StatusCreated {
		t.Fatalf("Coach's submit with Setup's key: %d %s", rr.Code, rr.Body.String())
	}
	if rr := send(tafKey, nora); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "reaches only the gateway's routes") {
		t.Fatalf("the submit with a taf's gateway key: %d %s", rr.Code, rr.Body.String())
	}
}

// Setup is known by the server's own record, never by Cards alone: an agent
// a live setup_reader key is already bound to is Setup under any id, and
// any other agent's Cards draft mints a gateway key, whichever tools its
// Cards name.
func TestASetupKeyIsKnownByItsAgent(t *testing.T) {
	r := newDraftRig(t)
	_, d := r.submit(admin(), map[string]any{"agent_id": "coach-1", "label": "Setup", "no_tools": true})
	if code, got := r.claim(d["id"].(string), d["claim_secret"].(string)); code != http.StatusOK {
		t.Fatalf("no-tools claim: %d %v", code, got)
	}
	if _, roles := r.mintFrom("coach-1"); !slices.Equal(roles, []string{auth.RoleSetupReader}) {
		t.Errorf("Setup's Cards draft under its own id: roles %v", roles)
	}
	for _, agent := range []string{"manager", "setup-2", "Setup"} {
		if _, roles := r.mintFrom(agent); !slices.Equal(roles, []string{auth.RoleGatewayAgent}) {
			t.Errorf("%s: roles %v, want [%s]", agent, roles, auth.RoleGatewayAgent)
		}
	}
}
