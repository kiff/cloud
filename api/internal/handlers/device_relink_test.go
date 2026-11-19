package handlers_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/notify"
)

// relinkRig is a phone signed in with a device key and an approval key,
// its person's browser, and the public poll.
type relinkRig struct {
	t         *testing.T
	sessions  *auth.MemoryDeveloperSessionStore
	now       time.Time
	deviceKey *ecdsa.PrivateKey
	deviceDER []byte
	oldDER    []byte
	phone     http.Handler // policed, as the phone's session
	inner     *http.ServeMux
	public    *http.ServeMux
	notices   *fakeNotices
}

type fakeNotices struct {
	mu   sync.Mutex
	sent []notify.AccountNotice
	to   []string
}

func (f *fakeNotices) MemberInvited(context.Context, string, notify.MemberInvite) error { return nil }
func (f *fakeNotices) AccountNotice(_ context.Context, to string, n notify.AccountNotice) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.to, f.sent = append(f.to, to), append(f.sent, n)
	return nil
}

type fakeEmails map[string]string

func (f fakeEmails) EmailForSubject(_ context.Context, subject string) (string, error) {
	return f[subject], nil
}

func newRelinkRig(t *testing.T) *relinkRig {
	t.Helper()
	r := &relinkRig{t: t, sessions: auth.NewMemoryDeveloperSessionStore(), now: time.Now().UTC(), notices: &fakeNotices{}}
	r.deviceKey, r.deviceDER = p256(t)
	_, r.oldDER = p256(t)
	if err := r.sessions.Insert(context.Background(), auth.DeveloperSession{ID: "sess-phone", Subject: "user-abc", Tenant: "tenant-1",
		TokenHash: sha256.Sum256([]byte("tok")), CreatedAt: r.now, ExpiresAt: r.now.Add(time.Hour),
		DevicePublicKey: r.deviceDER, ApprovalPublicKey: r.oldDER}); err != nil {
		t.Fatal(err)
	}
	h := handlers.NewDeviceAuthHandler(r.sessions, handlers.WithDeviceClock(func() time.Time { return r.now }),
		handlers.WithDeviceNotices(fakeEmails{"user-abc": "ana@example.com"}, r.notices))
	r.inner, r.public = http.NewServeMux(), http.NewServeMux()
	h.RegisterAuthenticated(r.inner)
	h.RegisterPublic(r.public)
	r.phone = withFakeIdentity(auth.DefaultRoutePolicy().Wrap(r.inner), auth.Identity{Subject: "user-abc", Tenant: "tenant-1",
		Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"}, DeviceKey: r.deviceDER, DeviceSessionID: "sess-phone"})
	return r
}

// proof is the new key's signature over the re-link message.
func (r *relinkRig) proof(key *ecdsa.PrivateKey, der []byte, tenant string, at time.Time) (string, string) {
	ts := at.UTC().Format(time.RFC3339)
	digest := sha256.Sum256(auth.RelinkMessage(tenant, r.deviceDER, der, ts))
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		r.t.Fatal(err)
	}
	return ts, base64.StdEncoding.EncodeToString(sig)
}

// ask is the phone's POST /v1/me/device/relink, signed by its device key.
func (r *relinkRig) ask(der []byte, signedAt, sig string) *httptest.ResponseRecorder {
	const path = "/v1/me/device/relink"
	body, _ := json.Marshal(map[string]string{"approval_public_key": base64.StdEncoding.EncodeToString(der), "signed_at": signedAt, "signature": sig})
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set(auth.DeviceSignatureHeader, signRequest(r.t, r.deviceKey, "tenant-1", http.MethodPost, path, string(body), r.now))
	rec := httptest.NewRecorder()
	r.phone.ServeHTTP(rec, req)
	return rec
}

func (r *relinkRig) started(der []byte, key *ecdsa.PrivateKey) (deviceCode, userCode string) {
	r.t.Helper()
	ts, sig := r.proof(key, der, "tenant-1", r.now)
	rec := r.ask(der, ts, sig)
	if rec.Code != http.StatusOK {
		r.t.Fatalf("relink: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out.DeviceCode, out.UserCode
}

// browser is a person signed in on the web.
func (r *relinkRig) browser(tenant, subject, method, path string, body any) *httptest.ResponseRecorder {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	rec := httptest.NewRecorder()
	withFakeIdentity(r.inner, auth.Identity{Tenant: tenant, Subject: subject, Method: auth.MethodSession}).
		ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewReader(b)))
	return rec
}

func (r *relinkRig) poll(deviceCode string) *httptest.ResponseRecorder {
	r.now = r.now.Add(10 * time.Second) // past the poll interval
	body, _ := json.Marshal(map[string]string{"device_code": deviceCode})
	rec := httptest.NewRecorder()
	r.public.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/device/token", bytes.NewReader(body)))
	return rec
}

func (r *relinkRig) approvalKey() []byte {
	got, err := r.sessions.GetByHash(context.Background(), sha256.Sum256([]byte("tok")), r.now)
	if err != nil {
		r.t.Fatal(err)
	}
	return got.ApprovalPublicKey
}

// Invariant: a phone's approval key is replaced only when its own person
// approves the new key in their browser, after the new key proved itself.
// Category: authority
// Attack: code holding the phone's session and device key (no prompt) swaps in an approval key of its own, or another person approves the swap
// Refused: the device key alone only asks; the new key must sign the request; only the session's person, in the browser, approves; nothing changes before that
func TestARelinkReplacesTheApprovalKeyOnlyWhenItsPersonApproves(t *testing.T) {
	t.Parallel()
	r := newRelinkRig(t)
	newKey, newDER := p256(t)

	// Without the new key's proof, or with a stale one, or signed by another key: refused.
	otherKey, _ := p256(t)
	ts, sig := r.proof(otherKey, newDER, "tenant-1", r.now)
	if rec := r.ask(newDER, ts, sig); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_proof") {
		t.Errorf("signed by another key: %d %s", rec.Code, rec.Body.String())
	}
	ts, sig = r.proof(newKey, newDER, "tenant-1", r.now.Add(-10*time.Minute))
	if rec := r.ask(newDER, ts, sig); rec.Code != http.StatusBadRequest {
		t.Errorf("a stale proof: %d %s", rec.Code, rec.Body.String())
	}
	ts, sig = r.proof(newKey, newDER, "tenant-2", r.now)
	if rec := r.ask(newDER, ts, sig); rec.Code != http.StatusBadRequest {
		t.Errorf("a proof for another account: %d %s", rec.Code, rec.Body.String())
	}
	ts, sig = r.proof(r.deviceKey, r.deviceDER, "tenant-1", r.now)
	if rec := r.ask(r.deviceDER, ts, sig); rec.Code != http.StatusBadRequest {
		t.Errorf("the device key as the approval key: %d %s", rec.Code, rec.Body.String())
	}

	deviceCode, userCode := r.started(newDER, newKey)
	if !bytes.Equal(r.approvalKey(), r.oldDER) {
		t.Fatal("asking changed the key")
	}
	if rec := r.poll(deviceCode); !strings.Contains(rec.Body.String(), "authorization_pending") {
		t.Fatalf("before approval: %d %s", rec.Code, rec.Body.String())
	}

	// The page says it's a re-link, with the new key's fingerprint.
	rec := r.browser("tenant-1", "user-abc", http.MethodGet, "/v1/auth/device/pending?user_code="+userCode, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"relink":true`) ||
		!strings.Contains(rec.Body.String(), auth.DeviceKeyFingerprint(newDER)) {
		t.Fatalf("pending: %d %s", rec.Code, rec.Body.String())
	}

	// Another person can't even look it up.
	if rec := r.browser("tenant-1", "user-other", http.MethodGet, "/v1/auth/device/pending?user_code="+userCode, nil); rec.Code != http.StatusNotFound {
		t.Errorf("looked up by another person: %d %s", rec.Code, rec.Body.String())
	}
	// Another person, or the same one on another account: as unknown as any code.
	for _, who := range [][2]string{{"tenant-1", "user-other"}, {"tenant-2", "user-abc"}} {
		if rec := r.browser(who[0], who[1], http.MethodPost, "/v1/auth/device/approve", map[string]string{"user_code": userCode}); rec.Code != http.StatusNotFound {
			t.Errorf("approved by %v: %d %s", who, rec.Code, rec.Body.String())
		}
	}
	if !bytes.Equal(r.approvalKey(), r.oldDER) {
		t.Fatal("someone else's approval changed the key")
	}

	if rec := r.browser("tenant-1", "user-abc", http.MethodPost, "/v1/auth/device/approve", map[string]string{"user_code": userCode}); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(r.approvalKey(), newDER) {
		t.Fatal("the key wasn't replaced")
	}
	if rec := r.browser("tenant-1", "user-abc", http.MethodPost, "/v1/auth/device/approve", map[string]string{"user_code": userCode}); rec.Code != http.StatusConflict {
		t.Errorf("a second approve: %d %s", rec.Code, rec.Body.String())
	}
	// The phone learns it, with no token: the session goes on.
	rec = r.poll(deviceCode)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"relinked"`) || strings.Contains(rec.Body.String(), "access_token") {
		t.Fatalf("poll: %d %s", rec.Code, rec.Body.String())
	}
	if rec := r.poll(deviceCode); !strings.Contains(rec.Body.String(), "expired_token") {
		t.Errorf("a second poll: %d %s", rec.Code, rec.Body.String())
	}
	// The person hears of it.
	if len(r.notices.sent) != 1 || r.notices.to[0] != "ana@example.com" || !strings.Contains(r.notices.sent[0].Subject, "can approve again, with a new key") {
		t.Errorf("notice: %v %v", r.notices.to, r.notices.sent)
	}
	// The one-time registration still refuses a second key.
	if _, err := r.sessions.SetApprovalKey(context.Background(), "sess-phone", r.oldDER, r.now); err == nil {
		t.Error("SetApprovalKey took a key over the re-linked one")
	}
}

// One re-link at a time per session: a new one cancels the last, whose
// device_code then polls as expired.
func TestANewRelinkCancelsTheLast(t *testing.T) {
	t.Parallel()
	r := newRelinkRig(t)
	firstKey, firstDER := p256(t)
	secondKey, secondDER := p256(t)
	firstCode, firstUser := r.started(firstDER, firstKey)
	secondCode, secondUser := r.started(secondDER, secondKey)
	if rec := r.poll(firstCode); !strings.Contains(rec.Body.String(), "expired_token") {
		t.Errorf("the cancelled one: %d %s", rec.Code, rec.Body.String())
	}
	if rec := r.browser("tenant-1", "user-abc", http.MethodPost, "/v1/auth/device/approve", map[string]string{"user_code": firstUser}); rec.Code != http.StatusNotFound {
		t.Errorf("approving the cancelled one: %d %s", rec.Code, rec.Body.String())
	}
	if rec := r.browser("tenant-1", "user-abc", http.MethodPost, "/v1/auth/device/approve", map[string]string{"user_code": secondUser}); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(r.approvalKey(), secondDER) {
		t.Error("the second key wasn't the one replaced in")
	}
	if rec := r.poll(secondCode); !strings.Contains(rec.Body.String(), "relinked") {
		t.Errorf("poll: %d %s", rec.Code, rec.Body.String())
	}
}

// A session that ended since the phone asked is never given a key.
func TestARelinkOfAnEndedSessionChangesNothing(t *testing.T) {
	t.Parallel()
	r := newRelinkRig(t)
	key, der := p256(t)
	_, userCode := r.started(der, key)
	if err := r.sessions.Revoke(context.Background(), "sess-phone", r.now); err != nil {
		t.Fatal(err)
	}
	if rec := r.browser("tenant-1", "user-abc", http.MethodPost, "/v1/auth/device/approve", map[string]string{"user_code": userCode}); rec.Code != http.StatusNotFound {
		t.Errorf("approve after sign-out: %d %s", rec.Code, rec.Body.String())
	}
	if len(r.notices.sent) != 0 {
		t.Errorf("a notice for a re-link that didn't happen: %v", r.notices.sent)
	}
}

// A re-link needs the phone's device-key signature: a session token alone
// can't even ask.
func TestARelinkNeedsTheDeviceSignature(t *testing.T) {
	t.Parallel()
	r := newRelinkRig(t)
	key, der := p256(t)
	ts, sig := r.proof(key, der, "tenant-1", r.now)
	body, _ := json.Marshal(map[string]string{"approval_public_key": base64.StdEncoding.EncodeToString(der), "signed_at": ts, "signature": sig})
	rec := httptest.NewRecorder()
	r.phone.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/device/relink", bytes.NewReader(body)))
	if rec.Code != http.StatusForbidden {
		t.Errorf("unsigned: %d %s", rec.Code, rec.Body.String())
	}
}
