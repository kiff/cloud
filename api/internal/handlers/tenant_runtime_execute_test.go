package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tenant_runtime_execute_test.go pins the authenticated-actor rewrite at the
// Cloud/framework trust boundary.

func newExecReq(t *testing.T, body string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost,
		"/entities/order-1/actions/MARK_PAID/execute", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return r
}

func readBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var doc map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("unmarshal rewritten body %q: %v", raw, err)
		}
	}
	return doc
}

func actorRoles(t *testing.T, doc map[string]any) ([]string, bool) {
	t.Helper()
	actor, ok := doc["actor"].(map[string]any)
	if !ok {
		return nil, false
	}
	raw, present := actor["roles"]
	if !present {
		return nil, false
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("actor.roles is not an array: %T", raw)
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, v.(string))
	}
	return out, true
}

func TestIsExecutePath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/entities/o1/actions/MARK_PAID/execute", true},
		{http.MethodPost, "/entities/o1/execute", true},
		{http.MethodPost, "/entities/o1/actions/MARK_PAID/validate", false},
		{http.MethodGet, "/entities/o1/actions/MARK_PAID/execute", false},
		{http.MethodPost, "/approvals/a1/grant", false},
		{http.MethodPost, "/events/raw", false},
	}
	for _, c := range cases {
		r, _ := http.NewRequest(c.method, c.path, nil)
		if got := isExecutePath(r); got != c.want {
			t.Errorf("isExecutePath(%s %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestIsAuthenticatedActorPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/entities/o1/actions/PAY/execute", true},
		{http.MethodPost, "/entities/o1/actions/PAY/validate", true},
		{http.MethodPost, "/entities/o1/actions/PAY/approvals", true},
		{http.MethodPost, "/approvals/a1/grant", true},
		{http.MethodPost, "/approvals/a1/deny", true},
		{http.MethodGet, "/entities/o1/approvals", false},
		{http.MethodPost, "/events/raw", true},
	}
	for _, c := range cases {
		r, _ := http.NewRequest(c.method, c.path, nil)
		if got := isAuthenticatedActorPath(r); got != c.want {
			t.Errorf("isAuthenticatedActorPath(%s %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

// TestRewrite_OverwritesBodyAssertedRoles: the core property — a body
// that asserts admin is rewritten to the caller's real roles.
func TestRewrite_OverwritesBodyAssertedRoles(t *testing.T) {
	t.Parallel()
	r := newExecReq(t, `{"actor":{"id":"forged","type":"human","display_name":"Admin","roles":["admin","superuser"]},"parameters":{"k":"v"}}`)
	out, err := rewriteAuthenticatedActor(r, "runtime-key-1", []string{"viewer"}, actorShapeObject)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	doc := readBody(t, out)
	roles, ok := actorRoles(t, doc)
	if !ok {
		t.Fatal("rewritten actor has no roles")
	}
	if len(roles) != 1 || roles[0] != "viewer" {
		t.Errorf("roles = %v, want [viewer] (caller's resolved roles, not the body's)", roles)
	}
	actor := doc["actor"].(map[string]any)
	if actor["id"] != "runtime-key-1" {
		t.Errorf("actor.id = %v, want authenticated subject", actor["id"])
	}
	if _, ok := actor["type"]; ok {
		t.Error("caller-supplied actor.type survived rewrite")
	}
	if _, ok := actor["display_name"]; ok {
		t.Error("caller-supplied actor.display_name survived rewrite")
	}
	if params := doc["parameters"].(map[string]any); params["k"] != "v" {
		t.Errorf("parameters not preserved: %v", params)
	}
}

// TestRewrite_NoCallerRolesDropsBodyRoles: a caller with no resolved
// roles gets an actor with no roles, even if the body asserted some.
func TestRewrite_NoCallerRolesDropsBodyRoles(t *testing.T) {
	t.Parallel()
	r := newExecReq(t, `{"actor":{"id":"x","roles":["admin"]}}`)
	out, err := rewriteAuthenticatedActor(r, "runtime-key-1", nil, actorShapeObject)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	doc := readBody(t, out)
	if roles, ok := actorRoles(t, doc); ok {
		t.Errorf("expected no roles on the actor, got %v", roles)
	}
}

// TestRewrite_FillsRolesWhenBodyOmitsActor: an execute body with no
// actor object still gets the caller's roles injected (the dashboard
// run-decision path that sends only an id is the common case).
func TestRewrite_InjectsRolesWhenBodyOmitsRoles(t *testing.T) {
	t.Parallel()
	r := newExecReq(t, `{"actor":{"id":"agent"},"parameters":{}}`)
	out, err := rewriteAuthenticatedActor(r, "runtime-key-1", []string{"admin"}, actorShapeObject)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	doc := readBody(t, out)
	roles, ok := actorRoles(t, doc)
	if !ok || len(roles) != 1 || roles[0] != "admin" {
		t.Errorf("roles = %v (ok=%v), want [admin]", roles, ok)
	}
}

// TestRewrite_MalformedBodyPassedThrough: a non-JSON body is returned
// unchanged so the framework produces its own parse error (the gate
// never adds a new failure mode).
func TestRewrite_MalformedBodyPassedThrough(t *testing.T) {
	t.Parallel()
	r := newExecReq(t, `not json`)
	out, err := rewriteAuthenticatedActor(r, "runtime-key-1", []string{"admin"}, actorShapeObject)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	raw, _ := io.ReadAll(out.Body)
	if string(raw) != "not json" {
		t.Errorf("malformed body not passed through verbatim: %q", raw)
	}
}

func TestRewrite_JSONNullPassedThroughWithoutPanic(t *testing.T) {
	t.Parallel()
	r := newExecReq(t, `null`)
	out, err := rewriteAuthenticatedActor(r, "runtime-key-1", []string{"admin"}, actorShapeObject)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	raw, _ := io.ReadAll(out.Body)
	if string(raw) != "null" {
		t.Fatalf("body = %q, want null", raw)
	}
}

// TestRewrite_NilBodyUnchanged guards the no-body case.
func TestRewrite_NilBodyUnchanged(t *testing.T) {
	t.Parallel()
	r, _ := http.NewRequest(http.MethodPost, "/entities/o1/execute", nil)
	out, err := rewriteAuthenticatedActor(r, "runtime-key-1", []string{"admin"}, actorShapeObject)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if out.Body != nil {
		t.Error("expected nil body to remain nil")
	}
}

func TestRewrite_OversizedBodyFailsClosed(t *testing.T) {
	t.Parallel()
	body := `{"actor":{"id":"forged","roles":["admin"]},"padding":"` + strings.Repeat("x", maxActorBodyBytes) + `"}`
	r := newExecReq(t, body)
	if _, err := rewriteAuthenticatedActor(r, "runtime-key-1", nil, actorShapeObject); !errors.Is(err, errActorBodyTooLarge) {
		t.Fatalf("error = %v, want errActorBodyTooLarge", err)
	}
}

// TestRewriteAuthenticatedActorIDOnRawEvent covers the ingest shape. RawInput
// carries actor_id as a string, not an actor object, so rewriting the object
// shape here would silently leave the caller's value in place — the framework
// reads actor_id and ignores actor. Because ingested events drive state
// transitions and land in the audit trail, an unrewritten actor_id lets an
// authenticated tenant attribute state changes to any identity it names.
func TestRewriteAuthenticatedActorIDOnRawEvent(t *testing.T) {
	t.Parallel()

	body := `{"id":"evt-1","adapter":"cloud","type":"ORDER_PAID","source":"agent",` +
		`"entity_id":"order-1","actor_id":"ops-human","payload":{"amount":100}}`
	r := httptest.NewRequest(http.MethodPost, "/events/raw", strings.NewReader(body))

	shape := authenticatedActorShape(r)
	if shape != actorShapeID {
		t.Fatalf("authenticatedActorShape(/events/raw) = %v, want actorShapeID", shape)
	}

	out, err := rewriteAuthenticatedActor(r, "runtime-key-1", []string{"viewer"}, shape)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	rewritten, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatalf("read rewritten body: %v", err)
	}

	var doc struct {
		ActorID  string         `json:"actor_id"`
		EntityID string         `json:"entity_id"`
		Payload  map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(rewritten, &doc); err != nil {
		t.Fatalf("decode rewritten body: %v", err)
	}
	if doc.ActorID == "ops-human" {
		t.Fatal("ACTOR FORGERY: caller-supplied actor_id survived the trust boundary")
	}
	if doc.ActorID != "runtime-key-1" {
		t.Errorf("actor_id = %q, want %q", doc.ActorID, "runtime-key-1")
	}
	// Everything the caller legitimately owns must survive untouched.
	if doc.EntityID != "order-1" {
		t.Errorf("entity_id = %q, want order-1", doc.EntityID)
	}
	if got, ok := doc.Payload["amount"]; !ok || got != float64(100) {
		t.Errorf("payload amount = %v, want 100", got)
	}
}
