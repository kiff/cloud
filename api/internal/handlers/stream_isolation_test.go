package handlers

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/stream"
)

// agentBDecision is a decision made under agent B's credential, as the
// decide path publishes it.
var agentBDecision = stream.Event{Type: "decision", Data: map[string]any{
	"outcome": "allowed", "action_name": "refund", "entity_id": "inv-agent-b-42", "reasons": []string{}}}

// receivesDecision opens the stream with req and reports its status and
// whether agent B's decision arrived on it.
func receivesDecision(t *testing.T, b *stream.Broker, req *http.Request) (int, bool) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, false
	}
	rd := bufio.NewReader(resp.Body)
	if line, _ := rd.ReadString('\n'); !strings.HasPrefix(line, ": connected") {
		t.Fatalf("first line %q", line)
	}
	b.Publish("acme", agentBDecision)
	got := make(chan bool, 1)
	go func() {
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				got <- false
				return
			}
			if strings.Contains(line, "inv-agent-b-42") {
				got <- true
				return
			}
		}
	}()
	select {
	case ok := <-got:
		return resp.StatusCode, ok
	case <-time.After(2 * time.Second):
		return resp.StatusCode, false
	}
}

// Invariant: no key bound to an agent, and no runtime key, watches the
// account's live decisions: it cannot subscribe to the event stream, so
// it never receives another agent's decision.
// Category: authority
// Attack: agent A's runtime or gateway key opens GET /v1/me/stream and waits for agent B's decisions
// Refused: the stream is the account's view; it needs the account roles or a person's membership
func TestAgentKeysCannotWatchTheDecisionStream(t *testing.T) {
	ctx := context.Background()
	keys := auth.NewMemoryAPIKeyStore()
	plain := map[string]string{}
	for name, rec := range map[string]auth.APIKeyRecord{
		"gateway-a":       {Roles: []string{auth.RoleGatewayAgent}, AgentID: "agent-a"},
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
		plain[name] = gen.Plaintext
	}
	b := stream.NewBroker()
	mux := http.NewServeMux()
	NewStreamHandler(b, nil).Register(mux)
	// As production mounts it: authentication, then the route policy.
	mw := auth.NewMiddleware(auth.NewStubProvider(auth.NewMapResolver()), keys, auth.NewMapResolver())
	srv := httptest.NewServer(mw.Wrap(auth.DefaultRoutePolicy().Wrap(mux)))
	defer srv.Close()

	open := func(key string) *http.Request {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/me/stream", nil)
		req.Header.Set("Authorization", "Bearer "+plain[key])
		return req
	}
	for _, key := range []string{"gateway-a", "runtime-a", "runtime-b", "runtime-unbound"} {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		code, got := receivesDecision(t, b, open(key).WithContext(cctx))
		cancel()
		if code != http.StatusForbidden || got {
			t.Errorf("%s: status %d, received agent B's decision %v; want 403 and nothing", key, code, got)
		}
		if n := b.SubscriberCount("acme"); n != 0 {
			t.Errorf("%s left %d subscribers", key, n)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	if code, got := receivesDecision(t, b, open("admin").WithContext(cctx)); code != http.StatusOK || !got {
		t.Errorf("admin key: status %d, received %v; want 200 and the decision", code, got)
	}
	cancel()

	// People watch by their membership's role, which every role allows.
	// Sessions need the identity provider, so the identity is placed
	// below the middleware, as the middleware would place it.
	for _, role := range []membership.Role{membership.RoleAdmin, membership.RoleEditor, membership.RoleApprover, membership.RoleViewer} {
		person := auth.Identity{Subject: "person-" + string(role), Tenant: "acme", MemberRole: role,
			Roles: membership.IdentityRoles(role)}
		psrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mux.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), person)))
		}))
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		req, _ := http.NewRequestWithContext(cctx, http.MethodGet, psrv.URL+"/v1/me/stream", nil)
		if code, got := receivesDecision(t, b, req); code != http.StatusOK || !got {
			t.Errorf("%s: status %d, received %v; want 200 and the decision", role, code, got)
		}
		cancel()
		psrv.Close()
	}
}
