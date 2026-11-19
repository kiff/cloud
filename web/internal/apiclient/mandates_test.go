package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A limit whose usage could not be read must never render as an unused
// limit. Zero is the most dangerous number this surface can show.
func TestUnavailableLimitDoesNotRenderAsUnused(t *testing.T) {
	l := MandateLimit{Quantity: "sum(amount_cents)", Limit: 500000, Status: "unavailable"}
	if !l.Unavailable() {
		t.Error("a non-ok status must report as unavailable")
	}
	if l.PercentUsed() != 0 {
		t.Errorf("PercentUsed = %d; an unavailable limit has no meaningful bar", l.PercentUsed())
	}
}

func TestPercentUsedIsClamped(t *testing.T) {
	for _, tc := range []struct {
		used, limit int64
		want        int
	}{
		{0, 500000, 0},
		{250000, 500000, 50},
		{500000, 500000, 100},
		// Over-drawn is possible: a limit can be lowered after draws
		// were already recorded against it. It renders full, not 140%.
		{700000, 500000, 100},
		{10, 0, 0},
	} {
		l := MandateLimit{Used: tc.used, Limit: tc.limit, Status: "ok"}
		if got := l.PercentUsed(); got != tc.want {
			t.Errorf("used=%d limit=%d: PercentUsed = %d, want %d", tc.used, tc.limit, got, tc.want)
		}
	}
}

// The web and the api are separate modules, so nothing in the compiler
// connects this request shape to the body the api decodes. That gap has
// already produced one wrong body written from memory. This pins the
// wire keys against the api's mandateBody: id, subject, domain,
// environment, grants[].action, aggregates[].{quantity,limit,window}.
func TestIssueMandateSendsTheBodyTheAPIDecodes(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"limit-refund-agent","subject":"refund-agent","active":true}`))
	}))
	defer srv.Close()

	m, err := testClient(t, srv.URL).IssueMandateBearer(context.Background(), "tok", IssueMandateRequest{
		ID: "limit-refund-agent", Subject: "refund-agent", Domain: "refund-control",
		Environment: "production",
		Grants:      []MandateGrant{{Action: "AUTO_REFUND"}},
		Aggregates:  []MandateAggregate{{Quantity: "sum(amount)", Limit: 110000, Window: "calendar_day"}},
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if m.Subject != "refund-agent" {
		t.Errorf("decoded subject = %q", m.Subject)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/me/mandates" {
		t.Errorf("called %s %s, want POST /v1/me/mandates", gotMethod, gotPath)
	}
	for k, want := range map[string]any{
		"id": "limit-refund-agent", "subject": "refund-agent",
		"domain": "refund-control", "environment": "production",
	} {
		if gotBody[k] != want {
			t.Errorf("body[%q] = %v, want %v", k, gotBody[k], want)
		}
	}
	grants, _ := gotBody["grants"].([]any)
	if len(grants) != 1 {
		t.Fatalf("grants = %v", gotBody["grants"])
	}
	if g, _ := grants[0].(map[string]any); g["action"] != "AUTO_REFUND" {
		t.Errorf("grants[0] = %v, want action AUTO_REFUND", grants[0])
	}
	aggs, _ := gotBody["aggregates"].([]any)
	if len(aggs) != 1 {
		t.Fatalf("aggregates = %v", gotBody["aggregates"])
	}
	a, _ := aggs[0].(map[string]any)
	if a["quantity"] != "sum(amount)" || a["window"] != "calendar_day" {
		t.Errorf("aggregates[0] = %v", a)
	}
	if n, _ := a["limit"].(float64); int64(n) != 110000 {
		t.Errorf("aggregates[0].limit = %v, want 110000", a["limit"])
	}
	// revoked_at is never sent: revocation is its own act with its own
	// endpoint, and a body that could set it could clear it.
	if _, ok := gotBody["revoked_at"]; ok {
		t.Error("the issue body carried revoked_at")
	}
}

// The refusals are the useful part of this endpoint. Each says what is
// wrong with the limit, and that sentence has to reach the person at
// the form intact rather than as "unexpected status 400".
func TestIssueMandateCarriesTheCloudsRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"mandate: sum(amount) sums \"amount\", which action MARK_PAID does not declare"}`))
	}))
	defer srv.Close()

	_, err := testClient(t, srv.URL).IssueMandateBearer(context.Background(), "tok", IssueMandateRequest{})
	var rejected *MandateRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("err = %v (%T), want *MandateRejectedError", err, err)
	}
	if rejected.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d", rejected.StatusCode)
	}
	if !strings.Contains(rejected.Error(), "MARK_PAID does not declare") {
		t.Errorf("refusal lost its explanation: %q", rejected.Error())
	}
}

// A refusal with no readable body still has to say something a person
// can act on, rather than an empty string.
func TestIssueMandateRefusalWithoutABodyStillExplainsItself(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := testClient(t, srv.URL).IssueMandateBearer(context.Background(), "tok", IssueMandateRequest{})
	var rejected *MandateRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("err = %v, want *MandateRejectedError", err)
	}
	if strings.TrimSpace(rejected.Message) == "" {
		t.Error("a refusal with no body produced an empty message")
	}
}

func TestRevokeMandateCallsTheRevokeEndpoint(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.EscapedPath(), r.Method
		_, _ = w.Write([]byte(`{"id":"limit-refund-agent"}`))
	}))
	defer srv.Close()

	if err := testClient(t, srv.URL).RevokeMandateBearer(context.Background(), "tok", "limit/refund agent"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	// The id is escaped on the wire, so an id carrying a slash stays
	// one path segment and cannot reach a different endpoint than the
	// one named.
	if gotPath != "/v1/me/mandates/limit%2Frefund%20agent/revoke" {
		t.Errorf("path = %q, want the escaped revoke path", gotPath)
	}
}

// testClient is a session-aware client pointed at a test server: the
// same construction the dashboard uses, where every call carries the
// signed-in person's bearer rather than an operator key.
func testClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := NewSessionAware(baseURL, 0)
	if err != nil {
		t.Fatalf("NewSessionAware: %v", err)
	}
	return c
}
