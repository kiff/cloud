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

// Coach's first start under one fingerprint (kiff/tafs/docs/coach.md): the
// phone signs two requests once, with its approval key (kiff.requests.v1):
// the tools batch, which the gateway asks KIFF about (/v1/me/device-signature),
// and Coach's agent draft with its Cards, which KIFF confirms on its signed
// submit. Each is taken once, for itself only; nothing a device key makes
// counts as that one signature.
// Category: authority
// Attack: the paired signature made with the device key (a window's tap), replayed on either request, or brought to a third request
// Refused: anything but the approval key's, each request once, and no request the phone didn't sign
func TestCoachsFirstStartTakesOneSignatureOverItsTwoRequests(t *testing.T) {
	t.Parallel()
	r := newDraftRig(t)
	// The gateway's check reads the real clock: the draft's must agree.
	r.now = time.Now().UTC().Truncate(time.Second)
	// One record of signed requests for KIFF's routes and the gateway's check,
	// as cmd/api shares it: a signature's use on one is seen by the other.
	made := auth.NewMemorySignedRequests()
	drafts := auth.DefaultRoutePolicy().WithSignedRequests(made).WithClock(func() time.Time { return r.now }).Wrap(r.mux)
	gw := http.NewServeMux()
	NewDeviceSignatureHandler(made).Register(gw)
	gateway := auth.DefaultRoutePolicy().WithSignedRequests(made).Wrap(gw)

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

	at := r.now.Format(time.RFC3339)
	batchPath, draftPath := "/v1/connections/batch", "/v1/me/agent-drafts"
	batch := []byte(`{"connections":[{"name":"taf_task","mode":"verify","control":true}]}`)
	draft, _ := json.Marshal(map[string]any{"agent_id": "coach", "label": "Coach", "cards": []any{card("coach-refund", "coach")}})
	hashOf := func(m []byte) string { s := sha256.Sum256(m); return hex.EncodeToString(s[:]) }
	batchMsg := hashOf(auth.RequestMessage("t1", http.MethodPost, batchPath, batch, at))
	draftMsg := hashOf(auth.RequestMessage("t1", http.MethodPost, draftPath, draft, at))
	signOnce := func(k *ecdsa.PrivateKey) string {
		digest := sha256.Sum256(auth.RequestsMessage("t1", at, batchMsg, draftMsg))
		sig, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
		return base64.StdEncoding.EncodeToString(sig)
	}
	header := func(sig, with string) string { return "t=" + at + ",v3=" + sig + ",with=sha256:" + with }

	// The gateway asks KIFF whether the phone signed its batch.
	askBatch := func(sig, uri string) (int, map[string]any) {
		t.Helper()
		sum := sha256.Sum256(batch)
		b, _ := json.Marshal(map[string]string{"method": http.MethodPost, "uri": uri, "body_sha256": hex.EncodeToString(sum[:]), "signature": header(sig, draftMsg)})
		req := httptest.NewRequest(http.MethodPost, "/v1/me/device-signature", bytes.NewReader(b))
		rec := httptest.NewRecorder()
		gateway.ServeHTTP(rec, req.WithContext(auth.WithIdentity(req.Context(), phone)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	// The phone submits Coach's agent draft with its Cards to KIFF.
	submit := func(sig, path string, body []byte) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set(auth.DeviceSignatureHeader, header(sig, batchMsg))
		rec := httptest.NewRecorder()
		drafts.ServeHTTP(rec, req.WithContext(auth.WithIdentity(req.Context(), phone)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// A window's tap is the device key's: never the one signature, on either.
	tap := signOnce(deviceKey)
	if c, out := askBatch(tap, batchPath); c != http.StatusForbidden {
		t.Fatalf("the batch, the device key's paired signature: %d %v", c, out)
	}
	if c, out := submit(tap, draftPath, draft); c != http.StatusForbidden {
		t.Fatalf("the draft, the device key's paired signature: %d %v", c, out)
	}

	one := signOnce(approvalKey)
	// The batch first: the gateway's check takes it as the approval key's.
	if c, out := askBatch(one, batchPath); c != http.StatusOK || out["key"] != auth.KeyCritical {
		t.Fatalf("the batch, one signature: %d %v", c, out)
	}
	if c, _ := askBatch(one, batchPath); c != http.StatusConflict {
		t.Fatalf("the batch again: %d", c)
	}
	// The same signature on another request than the two it names.
	if c, _ := askBatch(one, "/v1/connections"); c != http.StatusForbidden {
		t.Fatalf("a third request: %d", c)
	}
	// Then Coach's agent and its Cards, confirmed by that signed submit.
	c, d := submit(one, draftPath, draft)
	if c != http.StatusCreated || d["status"] != "confirmed" || d["claim_secret"] == nil {
		t.Fatalf("the draft, one signature: %d %v", c, d)
	}
	if c, _ := submit(one, draftPath, draft); c == http.StatusCreated || c == http.StatusOK {
		t.Fatalf("the draft again: %d", c)
	}
	other, _ := json.Marshal(map[string]any{"agent_id": "coach2", "label": "Coach", "cards": []any{card("coach2-refund", "coach2")}})
	if c, _ := submit(one, draftPath, other); c != http.StatusForbidden {
		t.Fatalf("another draft under that signature: %d", c)
	}
}
