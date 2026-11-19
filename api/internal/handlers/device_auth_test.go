package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	kiffactor "github.com/kiff/kiff/pkg/kiff/actor"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
)

// deviceRig wires a DeviceAuthHandler with an in-memory developer-
// session store and a controllable clock. Public routes (start/token)
// are served bare; approve is wrapped with a fake identity so it reads
// as an authenticated human owner.
type deviceRig struct {
	pub      *http.ServeMux
	authed   http.Handler
	inner    http.Handler // the authenticated routes, before an identity
	sessions *auth.MemoryDeveloperSessionStore
	mu       sync.Mutex
	now      time.Time
}

func newDeviceRig(t *testing.T, tenant, subject string) *deviceRig {
	t.Helper()
	rig := &deviceRig{
		sessions: auth.NewMemoryDeveloperSessionStore(),
		now:      time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	}
	h := handlers.NewDeviceAuthHandler(rig.sessions,
		handlers.WithDeviceClock(func() time.Time {
			rig.mu.Lock()
			defer rig.mu.Unlock()
			return rig.now
		}),
		handlers.WithDeviceVerificationURI("https://app.example.com/auth/device"),
	)
	rig.pub = http.NewServeMux()
	h.RegisterPublic(rig.pub)
	inner := http.NewServeMux()
	h.RegisterAuthenticated(inner)
	rig.inner = inner
	rig.authed = withFakeIdentity(inner, auth.Identity{Tenant: tenant, Subject: subject, Method: auth.MethodSession})
	return rig
}

// as serves the authenticated routes as another person of tenant.
func (r *deviceRig) as(tenant, subject string) http.Handler {
	return withFakeIdentity(r.inner, auth.Identity{Tenant: tenant, Subject: subject, Method: auth.MethodSession})
}

func (r *deviceRig) advance(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = r.now.Add(d)
}

func (r *deviceRig) start(t *testing.T) (deviceCode, userCode string) {
	t.Helper()
	rec := httptest.NewRecorder()
	r.pub.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/device/start", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		Interval                int    `json:"interval"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if body.DeviceCode == "" || body.UserCode == "" {
		t.Fatal("start must return device_code and user_code")
	}
	if !strings.Contains(body.VerificationURIComplete, body.UserCode) {
		t.Errorf("verification_uri_complete should embed the user_code: %q", body.VerificationURIComplete)
	}
	return body.DeviceCode, body.UserCode
}

func (r *deviceRig) poll(t *testing.T, deviceCode string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"device_code": deviceCode})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/device/token", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	r.pub.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body
}

func (r *deviceRig) approve(t *testing.T, userCode string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"user_code": userCode})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/device/approve", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	r.authed.ServeHTTP(rec, req)
	return rec
}

func TestDeviceAuth_HappyPath(t *testing.T) {
	t.Parallel()
	rig := newDeviceRig(t, "tenant-1", "user-abc")
	deviceCode, userCode := rig.start(t)

	// Poll before approval -> authorization_pending.
	rec, body := rig.poll(t, deviceCode)
	if rec.Code != http.StatusBadRequest || body["error"] != "authorization_pending" {
		t.Fatalf("pre-approval poll = %d %v", rec.Code, body)
	}

	// Approve in the "browser".
	if ar := rig.approve(t, userCode); ar.Code != http.StatusOK {
		t.Fatalf("approve status = %d, body = %s", ar.Code, ar.Body.String())
	}

	// Advance past the poll interval so we don't trip slow_down.
	rig.advance(6 * time.Second)

	rec, body = rig.poll(t, deviceCode)
	if rec.Code != http.StatusOK {
		t.Fatalf("post-approval poll = %d %v", rec.Code, body)
	}
	tok, _ := body["access_token"].(string)
	if !strings.HasPrefix(tok, auth.DevSessionPrefix) {
		t.Errorf("access_token should be a developer session: %q", tok)
	}
	if body["tenant"] != "tenant-1" || body["subject"] != "user-abc" {
		t.Errorf("token response tenant/subject = %v/%v", body["tenant"], body["subject"])
	}

	// The minted token authenticates against the store.
	parsed, err := auth.ParseDevSessionToken(tok)
	if err != nil {
		t.Fatalf("parse minted token: %v", err)
	}
	if _, err := rig.sessions.GetByHash(context.Background(), parsed.Hash, rig.now); err != nil {
		t.Fatalf("minted session not in store: %v", err)
	}

	// The token is delivered once: a second poll finds nothing.
	rig.advance(6 * time.Second)
	rec, body = rig.poll(t, deviceCode)
	if rec.Code != http.StatusBadRequest || body["error"] != "expired_token" {
		t.Fatalf("second poll should be expired_token, got %d %v", rec.Code, body)
	}
}

func TestDeviceAuth_SlowDown(t *testing.T) {
	t.Parallel()
	rig := newDeviceRig(t, "tenant-1", "user-abc")
	deviceCode, _ := rig.start(t)
	// First poll registers lastPollAt (pending); the second, at the
	// same clock, is inside the interval -> slow_down.
	if rec, body := rig.poll(t, deviceCode); rec.Code != http.StatusBadRequest || body["error"] != "authorization_pending" {
		t.Fatalf("first poll = %d %v", rec.Code, body)
	}
	if rec, body := rig.poll(t, deviceCode); rec.Code != http.StatusBadRequest || body["error"] != "slow_down" {
		t.Fatalf("rapid re-poll should slow_down, got %d %v", rec.Code, body)
	}
}

func TestDeviceAuth_Expired(t *testing.T) {
	t.Parallel()
	rig := newDeviceRig(t, "tenant-1", "user-abc")
	deviceCode, _ := rig.start(t)
	rig.advance(11 * time.Minute) // past the 10m device TTL
	if rec, body := rig.poll(t, deviceCode); rec.Code != http.StatusBadRequest || body["error"] != "expired_token" {
		t.Fatalf("expired device code should be expired_token, got %d %v", rec.Code, body)
	}
}

func TestDeviceAuth_ApproveUnknownCode(t *testing.T) {
	t.Parallel()
	rig := newDeviceRig(t, "tenant-1", "user-abc")
	if rec := rig.approve(t, "BCDF-GHJK"); rec.Code != http.StatusNotFound {
		t.Fatalf("approve unknown code = %d, want 404", rec.Code)
	}
}

func TestDeviceAuth_ApproveNormalizesUserCode(t *testing.T) {
	t.Parallel()
	rig := newDeviceRig(t, "tenant-1", "user-abc")
	deviceCode, userCode := rig.start(t)
	// Lowercased + spaced input must still match.
	messy := strings.ToLower(strings.Replace(userCode, "-", " - ", 1))
	if rec := rig.approve(t, messy); rec.Code != http.StatusOK {
		t.Fatalf("approve with messy user_code = %d, body=%s", rec.Code, rec.Body.String())
	}
	rig.advance(6 * time.Second)
	if rec, body := rig.poll(t, deviceCode); rec.Code != http.StatusOK {
		t.Fatalf("poll after normalized approve = %d %v", rec.Code, body)
	}
}

func TestDeviceAuth_ApproveRequiresTenant(t *testing.T) {
	t.Parallel()
	sessions := auth.NewMemoryDeveloperSessionStore()
	h := handlers.NewDeviceAuthHandler(sessions)
	inner := http.NewServeMux()
	h.RegisterAuthenticated(inner)
	// Identity with no tenant.
	authed := withFakeIdentity(inner, auth.Identity{Subject: "ghost", Method: auth.MethodSession})
	b, _ := json.Marshal(map[string]string{"user_code": "BCDF-GHJK"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/device/approve", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	authed.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("approve without tenant = %d, want 401", rec.Code)
	}
}

func TestDeviceAuth_ApproveRateLimited(t *testing.T) {
	t.Parallel()
	rig := newDeviceRig(t, "tenant-1", "user-abc")
	// 10 failed approvals (unknown codes) are allowed; the 11th is
	// rate-limited with 429.
	for i := 0; i < 10; i++ {
		if rec := rig.approve(t, "ZZZZ-ZZZZ"); rec.Code != http.StatusNotFound {
			t.Fatalf("attempt %d: status = %d, want 404", i+1, rec.Code)
		}
	}
	if rec := rig.approve(t, "ZZZZ-ZZZZ"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("11th attempt should be 429, got %d", rec.Code)
	}
}

func TestDeviceAuth_RateLimitResetsOnSuccess(t *testing.T) {
	t.Parallel()
	rig := newDeviceRig(t, "tenant-1", "user-abc")
	deviceCode, userCode := rig.start(t)
	// A few failures, then a real approval clears the counter.
	for i := 0; i < 3; i++ {
		_ = rig.approve(t, "ZZZZ-ZZZZ")
	}
	if rec := rig.approve(t, userCode); rec.Code != http.StatusOK {
		t.Fatalf("valid approve after some fails = %d", rec.Code)
	}
	// Counter cleared: another batch of fails is allowed again.
	for i := 0; i < 5; i++ {
		if rec := rig.approve(t, "ZZZZ-ZZZZ"); rec.Code == http.StatusTooManyRequests {
			t.Fatalf("counter should have reset after success; got 429 at attempt %d", i+1)
		}
	}
	_ = deviceCode
}

func TestDeviceAuth_Logout(t *testing.T) {
	t.Parallel()
	rig := newDeviceRig(t, "tenant-1", "user-abc")
	// Seed a developer session and hold its plaintext token.
	gen, _ := auth.NewDevSessionToken()
	if err := rig.sessions.Insert(context.Background(), auth.DeveloperSession{
		ID: "sess-x", Subject: "user-abc", Tenant: "tenant-1",
		TokenHash: gen.Hash, CreatedAt: rig.now, ExpiresAt: rig.now.Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	// Logout presents the dev token as the bearer.
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/device/logout", nil)
	req.Header.Set("Authorization", "Bearer "+gen.Plaintext)
	rec := httptest.NewRecorder()
	rig.authed.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	// The session is now revoked.
	if _, err := rig.sessions.GetByHash(context.Background(), gen.Hash, rig.now); err != auth.ErrDevSessionRevoked {
		t.Fatalf("session should be revoked; got %v", err)
	}
}

func TestDeviceAuth_LogoutIdempotentOnUnknownToken(t *testing.T) {
	t.Parallel()
	rig := newDeviceRig(t, "tenant-1", "user-abc")
	gen, _ := auth.NewDevSessionToken() // never inserted
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/device/logout", nil)
	req.Header.Set("Authorization", "Bearer "+gen.Plaintext)
	rec := httptest.NewRecorder()
	rig.authed.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout with unknown token should be 204 (idempotent), got %d", rec.Code)
	}
}

func TestDeviceAuth_LogoutAll(t *testing.T) {
	t.Parallel()
	rig := newDeviceRig(t, "tenant-1", "user-abc")
	// Two sessions for the same subject (two devices).
	for i, id := range []string{"sess-a", "sess-b"} {
		gen, _ := auth.NewDevSessionToken()
		if err := rig.sessions.Insert(context.Background(), auth.DeveloperSession{
			ID: id, Subject: "user-abc", Tenant: "tenant-1",
			TokenHash: gen.Hash, CreatedAt: rig.now, ExpiresAt: rig.now.Add(24 * time.Hour),
		}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/device/logout?all=true", nil)
	rec := httptest.NewRecorder()
	rig.authed.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout --all status = %d", rec.Code)
	}
	// No active sessions remain for the subject.
	list, _ := rig.sessions.ListActiveForSubject(context.Background(), "user-abc", rig.now)
	if len(list) != 0 {
		t.Fatalf("expected all sessions revoked; %d active remain", len(list))
	}
}

// Invariant: an API key cannot approve a device sign-in, so an agent's key cannot turn itself into an owner session.
// Category: authority
// Attack: start a CLI sign-in, approve it with an agent's gateway key, and use the developer session it mints, which acts as the account's owner
// Refused: approving a device sign-in requires a signed-in person; an API key gets 403 and no session is minted
func TestDeviceAuth_APIKeyCannotApprove(t *testing.T) {
	sessions := auth.NewMemoryDeveloperSessionStore()
	keys := auth.NewMemoryAPIKeyStore()
	resolver := auth.NewMapResolver()
	resolver.Set("owner", kiffactor.Actor{ID: "owner"}, "t1")
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	gen, err := auth.NewAPIKey("t1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Insert(context.Background(), auth.APIKeyRecord{
		ID: "agent-key", Tenant: "t1", Subject: "owner", Roles: []string{"gateway_agent"},
		AgentID: "refund-agent", KeyHash: gen.Hash, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	clock := now
	dev := handlers.NewDeviceAuthHandler(sessions, handlers.WithDeviceClock(func() time.Time { return clock }))
	pub := http.NewServeMux()
	dev.RegisterPublic(pub)
	inner := http.NewServeMux()
	dev.RegisterAuthenticated(inner)
	var who auth.Identity
	inner.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) { who, _ = auth.IdentityFrom(r.Context()) })
	// The middleware reads the same clock as the handler: on the wall clock, the
	// session minted at the fixed `now` expired once its 90 days passed.
	mw := auth.NewMiddleware(auth.NewStubProvider(resolver), keys, resolver, auth.WithDeveloperSessions(sessions),
		auth.WithClock(func() time.Time { return clock }))
	authed := mw.Wrap(inner)

	post := func(h http.Handler, path, bearer string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := post(pub, "/v1/auth/device/start", "", nil)
	var started struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil || started.UserCode == "" {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}

	if rec := post(authed, "/v1/auth/device/approve", gen.Plaintext, map[string]string{"user_code": started.UserCode}); rec.Code != http.StatusForbidden {
		t.Fatalf("agent key approved a device sign-in: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(pub, "/v1/auth/device/token", "", map[string]string{"device_code": started.DeviceCode}); rec.Code == http.StatusOK {
		t.Fatalf("a developer session was issued: %s", rec.Body.String())
	}

	// The owner, signed in, still approves; the session it mints acts
	// as the owner, which is why only a person may approve.
	if rec := post(authed, "/v1/auth/device/approve", "owner", map[string]string{"user_code": started.UserCode}); rec.Code != http.StatusOK {
		t.Fatalf("owner approve: %d %s", rec.Code, rec.Body.String())
	}
	clock = clock.Add(time.Minute)
	rec = post(pub, "/v1/auth/device/token", "", map[string]string{"device_code": started.DeviceCode})
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("token: %d %s", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	authed.ServeHTTP(httptest.NewRecorder(), req)
	if !who.HasRole("tenant_owner") {
		t.Fatalf("developer session roles = %v; the test's premise is that it acts as the owner", who.Roles)
	}
}
