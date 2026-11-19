package handlers

import (
	"bytes"
	"context"
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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// pairedWorld is an account whose owner's phone has its device key and, in
// its session, its approval key (behind the fingerprint), for the approvals
// planque forwards: Setup's read Card and a connection's profile.
type pairedWorld struct {
	now      time.Time
	store    membership.Store
	members  *membership.Service
	devices  auth.DeviceStore
	sessions auth.DeveloperSessionStore
	dev      auth.ApprovalDevice
	dk, ak   *ecdsa.PrivateKey
}

func newPairedWorld(t *testing.T) pairedWorld {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 0, 30, 0, 0, time.UTC)
	mstore := membership.NewMemoryStore()
	svc := membership.NewService(mstore)
	if _, err := svc.EnsureFounder(ctx, "t1", "alice"); err != nil {
		t.Fatal(err)
	}
	devices := auth.NewMemoryDeviceStore()
	sessions := auth.NewMemoryDeveloperSessionStore()
	dk, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	dder, _ := x509.MarshalPKIXPublicKey(&dk.PublicKey)
	ak, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ader, _ := x509.MarshalPKIXPublicKey(&ak.PublicKey)
	dev := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: "t1", Subject: "alice", Name: "phone", Platform: "ios", PublicKey: dder, CreatedAt: now}
	if err := devices.Insert(ctx, dev); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Insert(ctx, auth.DeveloperSession{ID: "sess-1", Subject: "alice", Tenant: "t1", TokenHash: sha256.Sum256([]byte("s1")), CreatedAt: now,
		ExpiresAt: now.Add(24 * time.Hour), DevicePublicKey: dder, DeviceID: dev.ID, ApprovalPublicKey: ader}); err != nil {
		t.Fatal(err)
	}
	return pairedWorld{now: now, store: mstore, members: svc, devices: devices, sessions: sessions, dev: dev, dk: dk, ak: ak}
}

// request is planque's own message for POST uri with body, signed at the world's time minus a minute.
func (w pairedWorld) request(uri string, body []byte) string {
	sum := sha256.Sum256(body)
	return strings.Join([]string{"planque.request.v1", "https://planque.kiff.dev", "t1", "POST", uri,
		hex.EncodeToString(sum[:]), w.now.Add(-time.Minute).Format(time.RFC3339)}, "\n")
}

// paired is the kiff.requests.v1 message over request and the Card's confirm, as the phone signs it.
func (w pairedWorld) paired(request string) string {
	own := sha256.Sum256([]byte(request))
	card := sha256.Sum256([]byte("KIFF's confirm of the Card's draft"))
	a, b := hex.EncodeToString(own[:]), hex.EncodeToString(card[:])
	if b < a {
		a, b = b, a
	}
	return strings.Join([]string{"kiff.requests.v1", "t1", w.now.Add(-time.Minute).Format(time.RFC3339), a, b}, "\n")
}

func sign(k *ecdsa.PrivateKey, msg string) string {
	digest := sha256.Sum256([]byte(msg))
	sig, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
	return base64.StdEncoding.EncodeToString(sig)
}

// A connection approved together with the taf's Card, under one fingerprint
// (kiff.requests.v1), still gets Setup's read Card: planque sends the paired
// message and, as request, the approve request it names. KIFF checks the
// signature over the paired message with the approval key, the request's
// hash as one of its two, and the request as this grant's, with this body.
// A key app's key request counts as its approval, signed alone or paired.
// Category: authority
// Attack: a paired message signed by the device key (a window's tap); a request the paired message doesn't name; a request for another grant or with another body; a paired message with no request; a request beside a message signed alone
// Refused: 403 each, and no Card is issued; the approval key's pairing over this grant's request issues Setup's Card
func TestSetupReadsCardOnAnApprovalPairedWithTheCard(t *testing.T) {
	ctx := context.Background()
	w := newPairedWorld(t)
	keys := auth.NewMemoryAPIKeyStore()
	if err := keys.Insert(ctx, auth.APIKeyRecord{ID: "key-setup", Tenant: "t1", Subject: "alice", AgentID: "setup",
		Roles: []string{auth.RoleSetupReader}, KeyHash: sha256.Sum256([]byte("s")), CreatedAt: w.now}); err != nil {
		t.Fatal(err)
	}
	tools := setupReadTools{owners: map[string]string{"list_accounts": "key_conn"}, readOnly: map[string]bool{"list_accounts": true}}
	connector := auth.Identity{Tenant: "t1", Subject: "alice", Method: auth.MethodAPIKey, APIKey: true,
		Roles: []string{auth.RoleConnector}, Metadata: map[string]string{"api_key_id": "key_conn"}}
	handler := func(store mandate.Store) *http.ServeMux {
		mux := http.NewServeMux()
		NewMandatesHandler(store, stubDomainSource{yaml: setupReadsYAML}, WithMandatesClock(func() time.Time { return w.now }), WithMandatesMemberships(w.store),
			WithMandatesSetupReads(w.devices, w.members, tools, keys), WithMandatesApprovalWindows(w.sessions, auth.NewMemoryWindowStore())).Register(mux)
		return mux
	}
	const body = `{"digest":"dg_1"}`
	ask := func(store mandate.Store, msg, request, sig string) int {
		t.Helper()
		approval := map[string]string{"device_id": w.dev.ID, "message": msg, "body": body, "signature": sig}
		if request != "" {
			approval["request"] = request
		}
		return do(t, handler(store), connector, http.MethodPost, "/v1/me/setup-reads", map[string]any{"agent": "setup", "grant": "grt_1",
			"tools": []string{"list_accounts"}, "approval": approval}).Code
	}
	approve := w.request("/v1/accounts/t1/grants/grt_1/approve", []byte(body))
	key := w.request("/v1/accounts/t1/grants/grt_1/key", []byte(body))
	for name, c := range map[string]struct{ msg, request, sig string }{
		"a pairing signed by the device key":      {w.paired(approve), approve, sign(w.dk, w.paired(approve))},
		"a request the pairing doesn't name":      {w.paired(approve), key, sign(w.ak, w.paired(approve))},
		"a request for another grant":             {w.paired(w.request("/v1/accounts/t1/grants/grt_2/approve", []byte(body))), w.request("/v1/accounts/t1/grants/grt_2/approve", []byte(body)), sign(w.ak, w.paired(w.request("/v1/accounts/t1/grants/grt_2/approve", []byte(body))))},
		"a request with another body":             {w.paired(w.request("/v1/accounts/t1/grants/grt_1/approve", []byte(`{}`))), w.request("/v1/accounts/t1/grants/grt_1/approve", []byte(`{}`)), sign(w.ak, w.paired(w.request("/v1/accounts/t1/grants/grt_1/approve", []byte(`{}`))))},
		"a pairing with no request":               {w.paired(approve), "", sign(w.ak, w.paired(approve))},
		"a request beside a message signed alone": {approve, approve, sign(w.dk, approve)},
	} {
		store := mandate.NewMemoryStore()
		if code := ask(store, c.msg, c.request, c.sig); code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, code)
		}
		if ms, _ := store.List(ctx, "t1"); len(ms) != 0 {
			t.Errorf("%s: %d Card(s) issued", name, len(ms))
		}
	}
	for name, c := range map[string]struct{ msg, request, sig string }{
		"the approve, paired with the Card, by the approval key": {w.paired(approve), approve, sign(w.ak, w.paired(approve))},
		"the key, paired with the Card, by the approval key":     {w.paired(key), key, sign(w.ak, w.paired(key))},
		"the key, signed alone by the device key":                {key, "", sign(w.dk, key)},
		"the approve, signed alone by the device key":            {approve, "", sign(w.dk, approve)},
	} {
		store := mandate.NewMemoryStore()
		if code := ask(store, c.msg, c.request, c.sig); code != http.StatusCreated {
			t.Errorf("%s: %d, want 201", name, code)
			continue
		}
		if ms, _ := store.List(ctx, "t1"); len(ms) != 1 || ms[0].ID != SetupReadsCardID("grt_1") {
			t.Errorf("%s: the Card issued: %+v", name, ms)
		}
	}
}

// A connection's profile, approved together with the taf's Card, applies as
// one approved alone: the request the paired message names carries the
// profile's digests, and the approval key signed the pairing. A pairing by
// the device key, or one that doesn't name the request, applies nothing.
// Category: authority
// Attack: a paired profile approval signed by the device key, or naming another request
// Refused: valid false with the reason; the approval key's pairing is valid
func TestProfileApprovalPairedWithTheCard(t *testing.T) {
	w := newPairedWorld(t)
	prof, err := os.ReadFile("../profile/testdata/valid/stripe-refunds.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	golden, _ := os.ReadFile("../profile/testdata/valid/stripe-refunds.expected.json")
	_ = json.Unmarshal(golden, &want)
	body, _ := json.Marshal(map[string]any{"tools": []string{"create_refund"},
		"kiff_profile": map[string]any{"profile_sha256": want["profile_sha256"], "words_sha256": want["words_sha256"]}})
	h := NewProfilesHandler(w.devices, w.members).WithWindows(w.sessions, auth.NewMemoryWindowStore())
	h.clock = func() time.Time { return w.now }
	mux := http.NewServeMux()
	h.Register(mux)
	connector := auth.Identity{Tenant: "t1", Subject: "alice", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{auth.RoleConnector}}
	check := func(msg, request, sig string) map[string]any {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"profile": json.RawMessage(prof), "approval": map[string]string{
			"message": base64.StdEncoding.EncodeToString([]byte(msg)), "body": base64.StdEncoding.EncodeToString(body),
			"request": base64.StdEncoding.EncodeToString([]byte(request)), "signature": sig, "device_id": w.dev.ID}})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/me/profile-approvals", bytes.NewReader(b)).WithContext(auth.WithIdentity(context.Background(), connector))
		mux.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	request := w.request("/v1/accounts/t1/grants/g1/key", body)
	if out := check(w.paired(request), request, sign(w.ak, w.paired(request))); out["valid"] != true || out["device_id"] != w.dev.ID {
		t.Fatalf("the approval key's pairing: %v", out)
	}
	if out := check(w.paired(request), request, sign(w.dk, w.paired(request))); out["valid"] != false || out["reason"] != "approval_key_required" {
		t.Fatalf("a pairing by the device key: %v", out)
	}
	other := w.request("/v1/accounts/t1/grants/g2/key", body)
	if out := check(w.paired(other), request, sign(w.ak, w.paired(other))); out["valid"] != false || out["reason"] != "request_not_named" {
		t.Fatalf("a pairing that doesn't name the request: %v", out)
	}
}
