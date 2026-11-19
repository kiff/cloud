package handlers_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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

// devicesRig is deviceRig with an approval-device store.
func devicesRig(t *testing.T, tenant, subject string, extra ...handlers.DeviceAuthOption) (*deviceRig, *auth.MemoryDeviceStore) {
	t.Helper()
	devices := auth.NewMemoryDeviceStore()
	rig := &deviceRig{sessions: auth.NewMemoryDeveloperSessionStore(), now: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)}
	opts := append([]handlers.DeviceAuthOption{handlers.WithDeviceStore(devices),
		handlers.WithDeviceClock(func() time.Time {
			rig.mu.Lock()
			defer rig.mu.Unlock()
			return rig.now
		})}, extra...)
	h := handlers.NewDeviceAuthHandler(rig.sessions, opts...)
	rig.pub = http.NewServeMux()
	h.RegisterPublic(rig.pub)
	inner := http.NewServeMux()
	h.RegisterAuthenticated(inner)
	rig.inner = inner
	rig.authed = withFakeIdentity(inner, auth.Identity{Tenant: tenant, Subject: subject, Method: auth.MethodSession})
	return rig, devices
}

func newKey(t *testing.T) (der []byte, b64 string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ = x509.MarshalPKIXPublicKey(&k.PublicKey)
	return der, base64.StdEncoding.EncodeToString(der)
}

// signIn runs a device flow with body, approved by the rig's person, and
// returns the session it mints.
func signIn(t *testing.T, rig *deviceRig, body any) auth.DeveloperSession {
	t.Helper()
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	rig.pub.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/device/start", bytes.NewReader(b)))
	var started struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	if ar := rig.approve(t, started.UserCode); ar.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", ar.Code, ar.Body.String())
	}
	rig.advance(6 * time.Second)
	_, tok := rig.poll(t, started.DeviceCode)
	parsed, err := auth.ParseDevSessionToken(tok["access_token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := rig.sessions.GetByHash(context.Background(), parsed.Hash, rig.now)
	if err != nil {
		t.Fatal(err)
	}
	// The device is told how its session approves, as KIFF will check it
	// (a software key store only ever comes with "view"); a sign-in with no
	// device key is told nothing.
	want := ""
	if len(sess.DevicePublicKey) > 0 {
		want = auth.ShownApprovalMode(sess.ApprovalMode, len(sess.ApprovalPublicKey) > 0, "")
	}
	if got, _ := tok["approval_mode"].(string); got != want {
		t.Errorf("the token's approval_mode %q, want %q (session mode %q)", got, want, sess.ApprovalMode)
	}
	return sess
}

// A device with a key signs in as an approval device: KIFF keeps it
// with an id it makes and the platform it said, the session names it, a
// person lists their devices by those ids, and removing one ends its
// sessions. A CLI's keyless sign-in is no device.
func TestSigningInWithAKeyKeepsTheDevice(t *testing.T) {
	t.Parallel()
	rig, devices := devicesRig(t, "tenant-1", "user-abc")
	ctx := context.Background()

	ipadDER, ipadKey := newKey(t)
	ipad := signIn(t, rig, map[string]string{"public_key": ipadKey, "device_name": "tafs on iPad", "platform": "ipados"})
	if !strings.HasPrefix(ipad.DeviceID, "dev_") {
		t.Fatalf("the iPad's session names no device: %+v", ipad)
	}
	d, err := devices.Get(ctx, ipad.DeviceID)
	if err != nil || d.Platform != "ipados" || d.Tenant != "tenant-1" || d.Subject != "user-abc" || d.Name != "tafs on iPad" ||
		string(d.PublicKey) != string(ipadDER) || d.Attestation != "" {
		t.Fatalf("the iPad's device = %+v, %v", d, err)
	}
	// A device that sends no platform is kept with none: a key alone is never
	// taken for an iPhone's, so a computer can't pass as a phone by leaving it out.
	_, phoneKey := newKey(t)
	rig.advance(time.Minute)
	phone := signIn(t, rig, map[string]string{"public_key": phoneKey, "device_name": "tafs on iPhone"})
	if p, _ := devices.Get(ctx, phone.DeviceID); p.Platform != "" {
		t.Fatalf("a key without a platform was given one: %+v", p)
	}
	// The iPad signing in again (a new session, the same key) is the same device.
	rig.advance(time.Minute)
	again := signIn(t, rig, map[string]string{"public_key": ipadKey, "device_name": "tafs on iPad", "platform": "ipados"})
	if again.DeviceID != ipad.DeviceID || again.ID == ipad.ID {
		t.Fatalf("the iPad signed in again as device %q (session %q); want %q in a new session", again.DeviceID, again.ID, ipad.DeviceID)
	}
	// An iPad that first signed in from the iPhone-only build (no platform,
	// "tafs on iPhone") and then from the iPad build is the same device,
	// named as it is now.
	tabletDER, tabletKey := newKey(t)
	first := signIn(t, rig, map[string]string{"public_key": tabletKey, "device_name": "tafs on iPhone"})
	rig.advance(time.Minute)
	later := signIn(t, rig, map[string]string{"public_key": tabletKey, "device_name": "tafs on iPad", "platform": "ipados"})
	if later.DeviceID != first.DeviceID {
		t.Fatalf("the tablet signed in as two devices: %q, %q", first.DeviceID, later.DeviceID)
	}
	if d, _ := devices.Get(ctx, later.DeviceID); d.Name != "tafs on iPad" || d.Platform != "ipados" || string(d.PublicKey) != string(tabletDER) {
		t.Fatalf("the tablet's device after the iPad build = %+v", d)
	}
	// A sign-in that sends no platform leaves the one the device has.
	rig.advance(time.Minute)
	_ = signIn(t, rig, map[string]string{"public_key": tabletKey, "device_name": "tafs on iPad"})
	if d, _ := devices.Get(ctx, later.DeviceID); d.Platform != "ipados" {
		t.Fatalf("a sign-in without a platform reset it: %+v", d)
	}
	cli := signIn(t, rig, map[string]string{})
	if cli.DeviceID != "" || cli.DevicePublicKey != nil {
		t.Fatalf("a keyless sign-in made a device: %+v", cli)
	}

	for name, body := range map[string]map[string]string{
		"an unknown platform":    {"public_key": phoneKey, "platform": "palmos"},
		"a platform with no key": {"platform": "ios"},
	} {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		rig.pub.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/device/start", bytes.NewReader(b)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s started a sign-in: %d", name, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	rig.authed.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/devices", nil))
	var listed struct {
		Devices []map[string]any `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || len(listed.Devices) != 3 ||
		listed.Devices[0]["id"] != first.DeviceID || listed.Devices[1]["id"] != phone.DeviceID || listed.Devices[2]["id"] != ipad.DeviceID ||
		listed.Devices[2]["platform"] != "ipados" {
		t.Fatalf("devices: %s", rec.Body.String())
	}
	// Another person's device is theirs to remove.
	other := httptest.NewRecorder()
	rig.as("tenant-1", "user-other").ServeHTTP(other, httptest.NewRequest(http.MethodDelete, "/v1/me/devices/"+ipad.DeviceID, nil))
	if other.Code != http.StatusNotFound {
		t.Errorf("another person removed the iPad: %d", other.Code)
	}
	del := httptest.NewRecorder()
	rig.authed.ServeHTTP(del, httptest.NewRequest(http.MethodDelete, "/v1/me/devices/"+ipad.DeviceID, nil))
	if del.Code != http.StatusNoContent {
		t.Fatalf("remove the iPad: %d %s", del.Code, del.Body.String())
	}
	if d, _ := devices.Get(ctx, ipad.DeviceID); !d.IsRevoked() {
		t.Error("the removed iPad still approves")
	}
	// Both of the iPad's sessions end with it.
	if active, _ := rig.sessions.ListActiveForSubject(ctx, "user-abc", rig.now); len(active) != 5 {
		t.Errorf("after removing the iPad, sessions = %d; want the phone's, the tablet's three and the CLI's", len(active))
	}
	if p, _ := devices.Get(ctx, phone.DeviceID); p.IsRevoked() {
		t.Error("removing the iPad removed the phone")
	}
}

// A device that names no platform is kept with none, never as an iPhone, so a
// computer can't pass as a phone by leaving it out; a later sign-in of the
// same key that names it fills it in.
func TestAKeyWithoutAPlatformIsKeptWithNone(t *testing.T) {
	t.Parallel()
	rig, devices := devicesRig(t, "tenant-1", "user-abc")
	ctx := context.Background()
	_, key := newKey(t)
	first := signIn(t, rig, map[string]string{"public_key": key, "device_name": "tafs"})
	if d, err := devices.Get(ctx, first.DeviceID); err != nil || d.Platform != "" {
		t.Fatalf("a key without a platform: %+v %v", d, err)
	}
	rig.advance(time.Minute)
	later := signIn(t, rig, map[string]string{"public_key": key, "device_name": "tafs on iPhone", "platform": "ios"})
	if d, err := devices.Get(ctx, later.DeviceID); err != nil || later.DeviceID != first.DeviceID || d.Platform != "ios" {
		t.Fatalf("the same key naming its platform later: %+v %v (device %q, first %q)", d, err, later.DeviceID, first.DeviceID)
	}
}
