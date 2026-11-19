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
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/membership"
)

// planqueRig is an account with a phone whose device key signs planque's
// requests and whose session holds an approval key, an approval window
// store, and helpers to sign planque.request.v1 and v2 (kiff/tafs#494).
type planqueRig struct {
	t        *testing.T
	ctx      context.Context
	members  *membership.Service
	devices  *auth.MemoryDeviceStore
	sessions *auth.MemoryDeveloperSessionStore
	windows  *auth.MemoryWindowStore
	now      time.Time
}

type planquePhone struct {
	device      string
	session     string
	subject     string
	tenant      string
	deviceKey   *ecdsa.PrivateKey
	approvalKey *ecdsa.PrivateKey
	approvalDER []byte
}

func newPlanqueRig(t *testing.T) *planqueRig {
	t.Helper()
	r := &planqueRig{t: t, ctx: context.Background(), devices: auth.NewMemoryDeviceStore(),
		sessions: auth.NewMemoryDeveloperSessionStore(), windows: auth.NewMemoryWindowStore(), now: time.Now().UTC().Truncate(time.Second)}
	r.members = membership.NewService(membership.NewMemoryStore())
	for _, tenant := range []string{"t1", "t2"} {
		if _, err := r.members.EnsureFounder(r.ctx, tenant, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// phone enrols a device for subject on tenant, with a session that holds an approval key.
func (r *planqueRig) phone(tenant, subject string) planquePhone {
	r.t.Helper()
	dk, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	dder, _ := x509.MarshalPKIXPublicKey(&dk.PublicKey)
	ak, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ader, _ := x509.MarshalPKIXPublicKey(&ak.PublicKey)
	d := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: tenant, Subject: subject, Name: "phone", Platform: "android", PublicKey: dder, CreatedAt: r.now}
	if err := r.devices.Insert(r.ctx, d); err != nil {
		r.t.Fatal(err)
	}
	s := auth.DeveloperSession{ID: "sess-" + d.ID, Subject: subject, Tenant: tenant, TokenHash: sha256.Sum256([]byte(d.ID)), CreatedAt: r.now, ExpiresAt: r.now.Add(24 * time.Hour),
		DevicePublicKey: dder, DeviceID: d.ID, ApprovalPublicKey: ader}
	if err := r.sessions.Insert(r.ctx, s); err != nil {
		r.t.Fatal(err)
	}
	return planquePhone{device: d.ID, session: s.ID, subject: subject, tenant: tenant, deviceKey: dk, approvalKey: ak, approvalDER: ader}
}

// open opens an approval window for p, opened by the approval key whose SPKI is openedBy.
func (r *planqueRig) open(p planquePhone, openedBy []byte) auth.ApprovalWindow {
	r.t.Helper()
	w := auth.ApprovalWindow{ID: auth.NewWindowID(), Tenant: p.tenant, Subject: p.subject, DeviceID: p.device, SessionID: p.session,
		OpenedAt: r.now, ExpiresAt: r.now.Add(auth.WindowLength), CapAt: r.now.Add(auth.WindowCap), OpenedBy: auth.DeviceKeyFingerprint(openedBy)}
	if err := r.windows.Open(r.ctx, w); err != nil {
		r.t.Fatal(err)
	}
	return w
}

// message is planque's signed request: v1 with window "", else v2 with the window's id last.
func planqueMessage(tenant, uri string, body []byte, at time.Time, window string) string {
	sum := sha256.Sum256(body)
	lines := []string{"planque.request.v1", "https://planque.kiff.dev", tenant, "POST", uri, hex.EncodeToString(sum[:]), at.UTC().Format(time.RFC3339)}
	if window != "" {
		lines[0] = "planque.request.v2"
		lines = append(lines, window)
	}
	return strings.Join(lines, "\n")
}

func planqueSign(k *ecdsa.PrivateKey, msg string) string {
	digest := sha256.Sum256([]byte(msg))
	sig, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
	return base64.StdEncoding.EncodeToString(sig)
}

// The owner's approval of a profile, as planque forwards it: v1 signed by
// the device's key, as before windows; v2 signed by the device key inside
// an approval window that is open for that phone, person and account and
// was opened by the approval key the session holds now. Every other v2 is
// refused (kiff/tafs#494: the v30 phone's approvals were refused as
// not_an_approval).
func TestAProfileApprovalSignedInsideAWindowCounts(t *testing.T) {
	t.Parallel()
	r := newPlanqueRig(t)
	alice := r.phone("t1", "alice")
	otherPhone := r.phone("t1", "alice")
	elsewhere := r.phone("t2", "alice")

	mux := http.NewServeMux()
	handlers.NewProfilesHandler(r.devices, r.members).WithWindows(r.sessions, r.windows).Register(mux)
	connector := auth.Identity{Tenant: "t1", Subject: "alice", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{auth.RoleConnector}}

	prof, err := os.ReadFile("../profile/testdata/valid/stripe-refunds.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	golden, _ := os.ReadFile("../profile/testdata/valid/stripe-refunds.expected.json")
	_ = json.Unmarshal(golden, &want)
	body, _ := json.Marshal(map[string]any{"tools": []string{"create_refund"},
		"kiff_profile": map[string]string{"profile_sha256": want["profile_sha256"].(string), "words_sha256": want["words_sha256"].(string)}})
	uri := "/v1/accounts/t1/grants/g1/approve"
	check := func(device string, msg, sig string) map[string]any {
		b, _ := json.Marshal(map[string]any{"profile": json.RawMessage(prof), "approval": map[string]string{
			"message": base64.StdEncoding.EncodeToString([]byte(msg)), "body": base64.StdEncoding.EncodeToString(body),
			"signature": sig, "device_id": device}})
		rec := httptest.NewRecorder()
		withFakeIdentity(mux, connector).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/profile-approvals", bytes.NewReader(b)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != http.StatusOK {
			t.Fatalf("the check answered %d %v", rec.Code, out)
		}
		return out
	}
	valid := func(name string, out map[string]any) {
		t.Helper()
		if out["valid"] != true || out["profile_sha256"] != want["profile_sha256"] {
			t.Errorf("%s: %v", name, out)
		}
	}
	refused := func(name string, out map[string]any, reason string) {
		t.Helper()
		if out["valid"] != false || out["reason"] != reason {
			t.Errorf("%s: %v; want %s", name, out, reason)
		}
	}

	// v1, as before windows.
	v1 := planqueMessage("t1", uri, body, r.now, "")
	valid("v1 by the device key", check(alice.device, v1, planqueSign(alice.deviceKey, v1)))

	// v2 inside an open window that this session's approval key opened.
	win := r.open(alice, alice.approvalDER)
	v2 := planqueMessage("t1", uri, body, r.now, win.ID)
	valid("v2 inside the window", check(alice.device, v2, planqueSign(alice.deviceKey, v2)))

	// The window's line edited after signing: the signature no longer holds.
	edited := strings.Replace(v2, win.ID, auth.NewWindowID(), 1)
	refused("v2 with its window line edited", check(alice.device, edited, planqueSign(alice.deviceKey, v2)), "signature_invalid")
	// A v2 naming a window that isn't this device's open one.
	stranger := planqueMessage("t1", uri, body, r.now, auth.NewWindowID())
	refused("v2 naming a window that isn't open", check(alice.device, stranger, planqueSign(alice.deviceKey, stranger)), "window_required")
	// Another phone of the same person names this phone's window.
	refused("v2 from another phone", check(otherPhone.device, v2, planqueSign(otherPhone.deviceKey, v2)), "window_required")
	// A v2 signed by the approval key, or a v1 by the approval key: not the device's key.
	refused("v2 signed by the approval key", check(alice.device, v2, planqueSign(alice.approvalKey, v2)), "signature_invalid")
	refused("v1 signed by the approval key", check(alice.device, v1, planqueSign(alice.approvalKey, v1)), "signature_invalid")
	// Another account's phone, inside its own window, for this account's grant.
	ew := r.open(elsewhere, elsewhere.approvalDER)
	cross := planqueMessage("t1", uri, body, r.now, ew.ID)
	refused("another account's phone", check(elsewhere.device, cross, planqueSign(elsewhere.deviceKey, cross)), "unknown_device")
	// A window opened by a key the session no longer holds (a re-link).
	stale := r.open(alice, otherPhone.approvalDER)
	vs := planqueMessage("t1", uri, body, r.now, stale.ID)
	refused("v2 in a window opened by a replaced key", check(alice.device, vs, planqueSign(alice.deviceKey, vs)), "window_required")
	// A closed window.
	shut := r.open(alice, alice.approvalDER)
	if err := r.windows.Close(r.ctx, shut.ID, r.now, auth.WindowClosedByApp); err != nil {
		t.Fatal(err)
	}
	vc := planqueMessage("t1", uri, body, r.now, shut.ID)
	refused("v2 in a closed window", check(alice.device, vc, planqueSign(alice.deviceKey, vc)), "window_required")
	// A message that is neither v1 nor v2.
	v3 := strings.Replace(v2, "planque.request.v2", "planque.request.v3", 1)
	refused("an unknown version", check(alice.device, v3, planqueSign(alice.deviceKey, v3)), "not_an_approval")
	empty := planqueMessage("t1", uri, body, r.now, "x")
	empty = strings.TrimSuffix(empty, "x")
	refused("v2 with an empty window line", check(alice.device, empty, planqueSign(alice.deviceKey, empty)), "not_an_approval")
}

// Without the window stores, only v1 counts: a v2 can't be checked.
func TestAProfileApprovalInsideAWindowNeedsTheWindowStores(t *testing.T) {
	t.Parallel()
	r := newPlanqueRig(t)
	alice := r.phone("t1", "alice")
	mux := http.NewServeMux()
	handlers.NewProfilesHandler(r.devices, r.members).Register(mux)
	connector := auth.Identity{Tenant: "t1", Subject: "alice", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{auth.RoleConnector}}
	prof, _ := os.ReadFile("../profile/testdata/valid/stripe-refunds.json")
	var want map[string]any
	golden, _ := os.ReadFile("../profile/testdata/valid/stripe-refunds.expected.json")
	_ = json.Unmarshal(golden, &want)
	body, _ := json.Marshal(map[string]any{"kiff_profile": map[string]string{"profile_sha256": want["profile_sha256"].(string), "words_sha256": want["words_sha256"].(string)}})
	win := r.open(alice, alice.approvalDER)
	v2 := planqueMessage("t1", "/v1/accounts/t1/grants/g1/approve", body, r.now, win.ID)
	b, _ := json.Marshal(map[string]any{"profile": json.RawMessage(prof), "approval": map[string]string{
		"message": base64.StdEncoding.EncodeToString([]byte(v2)), "body": base64.StdEncoding.EncodeToString(body),
		"signature": planqueSign(alice.deviceKey, v2), "device_id": alice.device}})
	rec := httptest.NewRecorder()
	withFakeIdentity(mux, connector).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/profile-approvals", bytes.NewReader(b)))
	if !strings.Contains(rec.Body.String(), `"window_required"`) {
		t.Fatalf("v2 without the stores: %d %s", rec.Code, rec.Body.String())
	}
}
