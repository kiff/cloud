package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
)

// Through the real route policy, a phone revokes an agent's key
// only with its signature over that call, once; a stolen token alone
// revokes nothing, and an account key stays the dashboard's even signed.
func TestPhoneRevokesAnAgentKeyOnlySigned(t *testing.T) {
	store := auth.NewMemoryAPIKeyStore()
	mux := http.NewServeMux()
	auth.NewKeysHandler(store).Register(mux)
	policed := auth.DefaultRoutePolicy().Wrap(mux)
	insert := func(id, agent string, hash byte, roles ...string) {
		t.Helper()
		if err := store.Insert(context.Background(), auth.APIKeyRecord{ID: id, Tenant: "t1", Subject: "alice", AgentID: agent, Roles: roles,
			KeyHash: [auth.APIKeyHashSize]byte{hash}, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	insert("key-rae", "rae", 1, auth.RoleGatewayAgent)
	insert("key-account", "", 2, "admin")
	p := newPhoneRig(t)
	phone := p.on(owner())
	phone.MemberRole = membership.RoleAdmin
	signed := func(path string, at time.Time) map[string]string {
		return map[string]string{auth.DeviceSignatureHeader: p.signedRequest("t1", http.MethodDelete, path, "", at)}
	}

	if rr := serve(policed, phone, http.MethodDelete, "/v1/keys/key-rae", "", nil); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "signature_required") {
		t.Fatalf("unsigned revoke: %d %s", rr.Code, rr.Body.String())
	}
	if rr := serve(policed, phone, http.MethodDelete, "/v1/keys/key-rae", "", signed("/v1/keys/key-account", time.Now())); rr.Code != http.StatusForbidden {
		t.Fatalf("a signature over another key's revoke: %d %s", rr.Code, rr.Body.String())
	}
	if rr := serve(policed, phone, http.MethodDelete, "/v1/keys/key-account", "", signed("/v1/keys/key-account", time.Now())); rr.Code != http.StatusForbidden {
		t.Fatalf("a signed revoke of an account key: %d %s", rr.Code, rr.Body.String())
	}
	sig := signed("/v1/keys/key-rae", time.Now())
	if rr := serve(policed, phone, http.MethodDelete, "/v1/keys/key-rae", "", sig); rr.Code != http.StatusNoContent {
		t.Fatalf("signed revoke: %d %s", rr.Code, rr.Body.String())
	}
	if rr := serve(policed, phone, http.MethodDelete, "/v1/keys/key-rae", "", sig); rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "signature_replayed") {
		t.Fatalf("replayed revoke: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := store.GetByHash(context.Background(), [auth.APIKeyHashSize]byte{1}); !errors.Is(err, auth.ErrAPIKeyRevoked) {
		t.Fatalf("the agent's key after the revoke: %v", err)
	}
	if _, err := store.GetByHash(context.Background(), [auth.APIKeyHashSize]byte{2}); err != nil {
		t.Fatalf("the account key after the phone's revokes: %v", err)
	}
}
