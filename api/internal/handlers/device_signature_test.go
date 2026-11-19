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
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/membership"
)

// Invariant: another KIFF service takes a person's act signed on their phone
// only when KIFF confirms that phone signed that exact request, once, and the
// person may connect tools (kiff/tafs#206: connecting the account's control
// server through the gateway).
// Category: authority
// Attack: a phone's session replays a signed request, changes its body, or asks without the right to connect tools; another key asks
// Refused: a replay is 409, another body or path is 403, a role without tools.connect is 403, and only a phone's session may ask
func TestAnotherServiceAsksWhetherThePhoneSignedIt(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	handlers.NewDeviceSignatureHandler(nil).Register(mux)
	policed := auth.DefaultRoutePolicy().Wrap(mux)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	phone := func(role membership.Role) auth.Identity {
		return auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, MemberRole: role,
			DeviceKey: der, DeviceSessionID: "sess-phone"}
	}
	body := []byte(`{"name":"taf_task","mode":"verify","control":true}`)
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	sign := func(path string, at time.Time) string {
		ts := at.UTC().Format(time.RFC3339)
		digest := sha256.Sum256(auth.RequestMessage("tenant-1", http.MethodPost, path, body, ts))
		sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		return "t=" + ts + ",v1=" + base64.StdEncoding.EncodeToString(sig)
	}
	ask := func(id auth.Identity, uri, bodyHash, sig string) int {
		b, _ := json.Marshal(map[string]string{"method": http.MethodPost, "uri": uri, "body_sha256": bodyHash, "signature": sig})
		rec := httptest.NewRecorder()
		withFakeIdentity(policed, id).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/device-signature", bytes.NewReader(b)))
		return rec.Code
	}
	now := time.Now()
	signed := sign("/v1/connections", now)

	if got := ask(phone(membership.RoleApprover), "/v1/connections", hash, signed); got != http.StatusForbidden {
		t.Fatalf("a role that can't connect tools: %d", got)
	}
	if got := ask(phone(membership.RoleAdmin), "/v1/connections", hash, signed); got != http.StatusOK {
		t.Fatalf("the owner's phone, signed: %d", got)
	}
	if got := ask(phone(membership.RoleAdmin), "/v1/connections", hash, signed); got != http.StatusConflict {
		t.Fatalf("the same signed request again: %d", got)
	}
	other := sum
	other[0] ^= 1
	fresh := sign("/v1/connections", now.Add(time.Second))
	if got := ask(phone(membership.RoleAdmin), "/v1/connections", hex.EncodeToString(other[:]), fresh); got != http.StatusForbidden {
		t.Fatalf("another body: %d", got)
	}
	if got := ask(phone(membership.RoleAdmin), "/v1/connections/x/mode", hash, fresh); got != http.StatusForbidden {
		t.Fatalf("another path: %d", got)
	}
	if got := ask(phone(membership.RoleAdmin), "/v1/connections", hash, sign("/v1/connections", now.Add(-10*time.Minute))); got != http.StatusBadRequest {
		t.Fatalf("an old signature: %d", got)
	}
	account := auth.Identity{Subject: "key-1", Tenant: "tenant-1", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{"tenant_owner"}}
	if got := ask(account, "/v1/connections", hash, fresh); got != http.StatusForbidden {
		t.Fatalf("an account key, not a phone: %d", got)
	}
}

// Invariant: once KIFF accepts a phone's signature for another service, that
// service saves the account's contract and permit targets with the phone's
// session, each once, within a minute, and nothing else (kiff/tafs#206).
// Category: authority
// Attack: a phone's session writes the contract with no grant, reuses a grant, uses it on another route, after it ended, or with another session
// Refused: 403 without a live grant of this session, 409 for a second use, and no route but the two writes
func TestADeviceGrantAdmitsTheTwoWritesOnce(t *testing.T) {
	t.Parallel()
	made := auth.NewMemorySignedRequests()
	mux := http.NewServeMux()
	handlers.NewDeviceSignatureHandler(made).Register(mux)
	for _, pattern := range []string{"PUT /v1/me/domains/{domain}", "POST /v1/me/domains", "PUT /v1/me/permit-targets", "POST /v1/me/mandates"} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	}
	policed := auth.DefaultRoutePolicy().WithSignedRequests(made).Wrap(mux)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	phone := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, MemberRole: membership.RoleAdmin,
		DeviceKey: der, DeviceSessionID: "sess-phone"}
	call := func(id auth.Identity, method, path, grant string, body []byte) (int, []byte) {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if grant != "" {
			req.Header.Set(auth.DeviceGrantHeader, grant)
		}
		rec := httptest.NewRecorder()
		withFakeIdentity(policed, id).ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}

	if got, _ := call(phone, http.MethodPut, "/v1/me/permit-targets", "", nil); got != http.StatusForbidden {
		t.Fatalf("the phone writes permit targets with no grant: %d", got)
	}
	// The gateway asks about the phone's signed request, and gets a grant.
	body := []byte(`{"name":"taf_task","control":true}`)
	sum := sha256.Sum256(body)
	ts := time.Now().UTC().Format(time.RFC3339)
	digest := sha256.Sum256(auth.RequestMessage("tenant-1", http.MethodPost, "/v1/connections", body, ts))
	sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
	ask, _ := json.Marshal(map[string]string{"method": http.MethodPost, "uri": "/v1/connections",
		"body_sha256": hex.EncodeToString(sum[:]), "signature": "t=" + ts + ",v1=" + base64.StdEncoding.EncodeToString(sig)})
	code, raw := call(phone, http.MethodPost, "/v1/me/device-signature", "", ask)
	var got struct {
		Grant string `json:"grant"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &got) != nil || got.Grant == "" {
		t.Fatalf("device-signature: %d %s", code, raw)
	}

	if c, _ := call(phone, http.MethodPost, "/v1/me/mandates", got.Grant, nil); c == http.StatusOK {
		t.Fatal("the grant admitted a route it isn't for")
	}
	if c, _ := call(phone, http.MethodPut, "/v1/me/domains/billing", got.Grant, nil); c == http.StatusOK {
		t.Fatal("the grant admitted a domain other than the gateway's")
	}
	other := phone
	other.DeviceSessionID = "sess-other"
	if c, _ := call(other, http.MethodPut, "/v1/me/domains/gateway", got.Grant, nil); c != http.StatusForbidden {
		t.Fatalf("another session used the grant: %d", c)
	}
	if c, _ := call(phone, http.MethodPut, "/v1/me/domains/gateway", got.Grant, nil); c != http.StatusOK {
		t.Fatalf("the contract, with the grant: %d", c)
	}
	if c, _ := call(phone, http.MethodPut, "/v1/me/permit-targets", got.Grant, nil); c != http.StatusOK {
		t.Fatalf("the permit targets, with the grant: %d", c)
	}
	if c, _ := call(phone, http.MethodPut, "/v1/me/domains/gateway", got.Grant, nil); c != http.StatusConflict {
		t.Fatalf("the contract again, same grant: %d", c)
	}
	// An account with no gateway domain yet: the grant creates it, once, and
	// creates no other domain.
	if c, _ := call(phone, http.MethodPost, "/v1/me/domains", got.Grant, []byte("domain: billing\n")); c != http.StatusForbidden {
		t.Fatalf("the grant created another domain: %d", c)
	}
	if c, _ := call(phone, http.MethodPost, "/v1/me/domains", got.Grant, []byte("domain: gateway\n---\ndomain: billing\n")); c != http.StatusForbidden {
		t.Fatalf("the grant took a second document naming another domain: %d", c)
	}
	if c, _ := call(phone, http.MethodPost, "/v1/me/domains", got.Grant, []byte("domain: gateway\nentity: ToolCall\n")); c != http.StatusOK {
		t.Fatalf("the gateway's domain, created with the grant: %d", c)
	}
	if c, _ := call(phone, http.MethodPost, "/v1/me/domains", got.Grant, []byte("domain: gateway\n")); c != http.StatusConflict {
		t.Fatalf("created again, same grant: %d", c)
	}
	// A grant that has ended admits nothing.
	old, _, err := auth.IssueDeviceGrant(context.Background(), made, phone, time.Now().Add(-2*auth.DeviceGrantLife))
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := call(phone, http.MethodPut, "/v1/me/permit-targets", old, nil); c != http.StatusForbidden {
		t.Fatalf("an ended grant: %d", c)
	}
}

// Each answer is logged, one line with its status, its error code or the key
// that signed, and how the session approves: the Mac's refused approval (15
// Nov) left no line to read its two 403s from. Never the signature, nor the
// grant an acceptance answers.
func TestEachDeviceSignatureAnswerIsLoggedWithoutTheSignatureOrGrant(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	mux := http.NewServeMux()
	handlers.NewDeviceSignatureHandler(nil).WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))).Register(mux)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	body := []byte(`{"name":"taf_task"}`)
	sum := sha256.Sum256(body)
	ts := time.Now().UTC().Format(time.RFC3339)
	digest := sha256.Sum256(auth.RequestMessage("tenant-1", http.MethodPost, "/v1/connections", body, ts))
	sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
	signed := "t=" + ts + ",v1=" + base64.StdEncoding.EncodeToString(sig)
	ask := func(id auth.Identity) (int, map[string]any) {
		b, _ := json.Marshal(map[string]string{"method": http.MethodPost, "uri": "/v1/connections", "body_sha256": hex.EncodeToString(sum[:]), "signature": signed})
		rec := httptest.NewRecorder()
		withFakeIdentity(mux, id).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/device-signature", bytes.NewReader(b)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	lines := func() []map[string]any {
		var out []map[string]any
		for _, l := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
			var m map[string]any
			if err := json.Unmarshal(l, &m); err == nil {
				out = append(out, m)
			}
		}
		return out
	}
	phone := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, MemberRole: membership.RoleAdmin,
		DeviceKey: der, DeviceSessionID: "sess-mac"}

	view := phone
	view.ApprovalMode = auth.ApprovalModeView
	if code, _ := ask(view); code != http.StatusForbidden {
		t.Fatalf("a view session: %d", code)
	}
	got := lines()
	if len(got) != 1 || got[0]["msg"] != "device signature" || got[0]["error"] != "view_only" || got[0]["status"] != float64(403) ||
		got[0]["approval_mode"] != "view" || got[0]["view_only"] != true || got[0]["device_session"] != "sess-mac" || got[0]["level"] != "WARN" {
		t.Fatalf("a view session's refusal, as logged: %v", got)
	}

	logs.Reset()
	code, answer := ask(phone)
	if code != http.StatusOK || answer["grant"] == nil {
		t.Fatalf("the phone, signed: %d %v", code, answer)
	}
	got = lines()
	if len(got) != 1 || got[0]["key"] != "device" || got[0]["status"] != float64(200) || got[0]["error"] != nil || got[0]["level"] != "INFO" {
		t.Fatalf("an acceptance, as logged: %v", got)
	}
	if strings.Contains(logs.String(), answer["grant"].(string)) || strings.Contains(logs.String(), base64.StdEncoding.EncodeToString(sig)) {
		t.Fatalf("the grant or the signature was logged: %s", logs.String())
	}
}
