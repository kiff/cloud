package handlers

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
)

// A draft's confirm signed together with another request (kiff.requests.v1)
// is the approval key's: the device key's signature over the same message is
// refused, and the approval key's confirms the draft once.
// Category: authority
// Attack: a combined signature made with the device key (a window's tap), or replayed on the same confirm
// Refused: anything but the approval key's, used once
func TestADraftsConfirmTakesOneSignatureOverTwoRequests(t *testing.T) {
	t.Parallel()
	r := newDraftRig(t)
	r.startRae()
	policed := auth.DefaultRoutePolicy().WithClock(func() time.Time { return r.now }).Wrap(r.mux)
	key := func() (*ecdsa.PrivateKey, []byte) {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
		return k, der
	}
	deviceKey, deviceDER := key()
	approvalKey, approvalDER := key()
	phone := auth.Identity{Subject: "alice", Tenant: "t1", Method: auth.MethodSession, MemberRole: membership.RoleAdmin,
		Roles: membership.IdentityRoles(membership.RoleAdmin), DeviceKey: deviceDER, DeviceSessionID: "sess-phone", DeviceID: "dev_phone",
		DeviceName: "Pixel", ApprovalKey: approvalDER}
	code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{cardMax("rae-refund", "rae", 30000)}})
	if code != http.StatusCreated || d["status"] != "draft" {
		t.Fatalf("draft: %d %v", code, d)
	}
	path := "/v1/me/agent-drafts/" + d["id"].(string) + "/confirm"
	body, _ := json.Marshal(map[string]any{"version": d["version"]})
	at := r.now.Format(time.RFC3339)
	own := sha256.Sum256(auth.RequestMessage("t1", http.MethodPost, path, body, at))
	other := sha256.Sum256([]byte("the connection server's key request"))
	otherHex := hex.EncodeToString(other[:])
	send := func(k *ecdsa.PrivateKey) int {
		t.Helper()
		digest := sha256.Sum256(auth.RequestsMessage("t1", at, hex.EncodeToString(own[:]), otherHex))
		sig, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set(auth.DeviceSignatureHeader, "t="+at+",v3="+base64.StdEncoding.EncodeToString(sig)+",with=sha256:"+otherHex)
		rec := httptest.NewRecorder()
		policed.ServeHTTP(rec, req.WithContext(auth.WithIdentity(req.Context(), phone)))
		return rec.Code
	}
	if c := send(deviceKey); c != http.StatusForbidden {
		t.Fatalf("the device key's combined signature: %d", c)
	}
	if r.perActionMax("rae-refund") != 15000 {
		t.Fatal("confirmed by the device key")
	}
	if c := send(approvalKey); c != http.StatusOK || r.perActionMax("rae-refund") != 30000 {
		t.Fatalf("the approval key's combined signature: %d, per refund %d", c, r.perActionMax("rae-refund"))
	}

	// On a route the device key may sign alone, a combined signature is still the approval key's.
	trial := "/v1/me/agent-drafts/drf_none/trial"
	sendOn := func(k *ecdsa.PrivateKey) int {
		t.Helper()
		mine := sha256.Sum256(auth.RequestMessage("t1", http.MethodDelete, trial, nil, at))
		digest := sha256.Sum256(auth.RequestsMessage("t1", at, hex.EncodeToString(mine[:]), otherHex))
		sig, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
		req := httptest.NewRequest(http.MethodDelete, trial, nil)
		req.Header.Set(auth.DeviceSignatureHeader, "t="+at+",v3="+base64.StdEncoding.EncodeToString(sig)+",with=sha256:"+otherHex)
		rec := httptest.NewRecorder()
		policed.ServeHTTP(rec, req.WithContext(auth.WithIdentity(req.Context(), phone)))
		return rec.Code
	}
	if c := sendOn(deviceKey); c != http.StatusForbidden {
		t.Fatalf("the device key's combined signature on a device route: %d", c)
	}
	if c := sendOn(approvalKey); c == http.StatusForbidden {
		t.Fatalf("the approval key's combined signature on a device route: %d", c)
	}
}
