package handlers_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
)

// p256 is a fresh P-256 key and its public half (SPKI DER).
func p256(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key, der
}

// signRequest is a KIFF-Device-Signature over a request, by key.
func signRequest(t *testing.T, key *ecdsa.PrivateKey, tenant, method, path, body string, at time.Time) string {
	t.Helper()
	ts := at.UTC().Format(time.RFC3339)
	digest := sha256.Sum256(auth.RequestMessage(tenant, method, path, []byte(body), ts))
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return "t=" + ts + ",v1=" + base64.StdEncoding.EncodeToString(sig)
}

// kiff/tafs#233: once a phone's session has an approval key, what grants
// authority (issuing or raising a Card, submitting or confirming a draft,
// resuming) counts only with that key's signature, which its own access
// control gives only after Face ID. Every other signed call keeps the
// device key. A session without one works as before.
func TestWhatGrantsAuthorityNeedsTheApprovalKey(t *testing.T) {
	t.Parallel()
	reached := 0
	inner := http.NewServeMux()
	for _, pattern := range []string{"POST /v1/me/mandates", "POST /v1/me/agent-drafts", "POST /v1/me/agent-drafts/{id}/confirm",
		"POST /v1/me/mandates/{id}/extra-room", "POST /v1/resume", "POST /v1/agents/{id}/resume", "POST /v1/me/alert-targets"} {
		inner.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) { reached++; w.WriteHeader(http.StatusCreated) })
	}
	policed := auth.DefaultRoutePolicy().Wrap(inner)
	deviceKey, deviceDER := p256(t)
	approvalKey, approvalDER := p256(t)
	withApproval := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"},
		DeviceKey: deviceDER, DeviceSessionID: "sess-phone", ApprovalKey: approvalDER}
	without := withApproval
	without.ApprovalKey, without.DeviceSessionID = nil, "sess-old"
	post := func(id auth.Identity, key *ecdsa.PrivateKey, path, body string, at time.Time) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		req.Header.Set(auth.DeviceSignatureHeader, signRequest(t, key, "tenant-1", http.MethodPost, path, body, at))
		rec := httptest.NewRecorder()
		withFakeIdentity(policed, id).ServeHTTP(rec, req)
		return rec
	}

	now := time.Now()
	granting := []string{"/v1/me/mandates", "/v1/me/agent-drafts", "/v1/me/agent-drafts/d1/confirm", "/v1/me/mandates/m1/extra-room",
		"/v1/resume", "/v1/agents/rae/resume"}
	for i, path := range granting {
		at := now.Add(time.Duration(i) * time.Second)
		if rec := post(withApproval, deviceKey, path, `{}`, at); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "approval_key_required") {
			t.Errorf("%s signed by the device key: %d %s", path, rec.Code, rec.Body.String())
		}
		if rec := post(withApproval, approvalKey, path, `{}`, at); rec.Code != http.StatusCreated {
			t.Errorf("%s signed by the approval key: %d %s", path, rec.Code, rec.Body.String())
		}
		// A session from before approval keys signs it with its device key, as before.
		if rec := post(without, deviceKey, path, `{"old":true}`, at); rec.Code != http.StatusCreated {
			t.Errorf("%s from a session without an approval key: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	// What doesn't grant authority keeps the device key, and the approval key doesn't sign it.
	if rec := post(withApproval, deviceKey, "/v1/me/alert-targets", `{}`, now); rec.Code != http.StatusCreated {
		t.Errorf("alert target signed by the device key: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(withApproval, approvalKey, "/v1/me/alert-targets", `{"x":1}`, now); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "signature_required") {
		t.Errorf("alert target signed by the approval key: %d %s", rec.Code, rec.Body.String())
	}
	if want := 2*len(granting) + 1; reached != want {
		t.Fatalf("handlers reached %d times, want %d", reached, want)
	}
}

// kiff/tafs#233: a session that signed in without an approval key
// registers one, once, signed by its device key. A second is refused with
// the first one's fingerprint, so code holding only the device key can't
// swap in a key of its own; the approval key given at sign-in counts the
// same.
func TestASessionRegistersItsApprovalKeyOnce(t *testing.T) {
	t.Parallel()
	sessions := auth.NewMemoryDeveloperSessionStore()
	now := time.Now()
	deviceKey, deviceDER := p256(t)
	if err := sessions.Insert(context.Background(), auth.DeveloperSession{ID: "sess-phone", Subject: "user-abc", Tenant: "tenant-1",
		TokenHash: sha256.Sum256([]byte("tok")), CreatedAt: now, ExpiresAt: now.Add(time.Hour), DevicePublicKey: deviceDER}); err != nil {
		t.Fatal(err)
	}
	h := handlers.NewDeviceAuthHandler(sessions, handlers.WithDeviceClock(func() time.Time { return now }))
	inner := http.NewServeMux()
	h.RegisterAuthenticated(inner)
	policed := auth.DefaultRoutePolicy().Wrap(inner)
	phone := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"},
		DeviceKey: deviceDER, DeviceSessionID: "sess-phone"}
	const path = "/v1/me/device/approval-key"
	register := func(der []byte, signer *ecdsa.PrivateKey, at time.Time) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"public_key": base64.StdEncoding.EncodeToString(der)})
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		if signer != nil {
			req.Header.Set(auth.DeviceSignatureHeader, signRequest(t, signer, "tenant-1", http.MethodPost, path, string(body), at))
		}
		rec := httptest.NewRecorder()
		withFakeIdentity(policed, phone).ServeHTTP(rec, req)
		return rec
	}

	_, first := p256(t)
	_, second := p256(t)
	if rec := register(first, nil, now); rec.Code != http.StatusForbidden {
		t.Errorf("unsigned registration: %d %s", rec.Code, rec.Body.String())
	}
	if rec := register(deviceDER, deviceKey, now); rec.Code != http.StatusBadRequest {
		t.Errorf("the device key as its own approval key: %d %s", rec.Code, rec.Body.String())
	}
	rec := register(first, deviceKey, now.Add(time.Second))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), auth.DeviceKeyFingerprint(first)) {
		t.Fatalf("registration: %d %s", rec.Code, rec.Body.String())
	}
	rec = register(second, deviceKey, now.Add(2*time.Second))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "approval_key_set") || !strings.Contains(rec.Body.String(), auth.DeviceKeyFingerprint(first)) {
		t.Fatalf("a second key: %d %s", rec.Code, rec.Body.String())
	}
	got, err := sessions.GetByHash(context.Background(), sha256.Sum256([]byte("tok")), now)
	if err != nil || !bytes.Equal(got.ApprovalPublicKey, first) {
		t.Fatalf("the session keeps the first key: %v %x", err, got.ApprovalPublicKey)
	}
}
