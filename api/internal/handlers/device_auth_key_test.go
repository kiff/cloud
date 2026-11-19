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
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
)

// A phone sends its key when it starts signing in. The person
// sees the device's name and the key's fingerprint before approving;
// approval binds the key to the minted session; the person lists the
// device and removing it signs the phone out.
func TestDeviceAuth_BindsThePhonesKey(t *testing.T) {
	t.Parallel()
	rig := newDeviceRig(t, "tenant-1", "user-abc")
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	pub := base64.StdEncoding.EncodeToString(der)

	startWith := func(body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		rig.pub.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/device/start", bytes.NewReader(b)))
		return rec
	}
	if rec := startWith(map[string]string{"public_key": "bm90IGEga2V5"}); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad key started a flow: %d", rec.Code)
	}
	if rec := startWith(map[string]string{"public_key": pub, "device_name": "Gabo's\niPhone"}); rec.Code != http.StatusBadRequest {
		t.Errorf("a device name with a newline started a flow: %d", rec.Code)
	}
	rec := startWith(map[string]string{"public_key": pub, "device_name": "tafs on iPhone"})
	var started struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}

	authed := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		rig.authed.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}
	info := authed(http.MethodGet, "/v1/auth/device/pending?user_code="+started.UserCode)
	var pending map[string]any
	_ = json.Unmarshal(info.Body.Bytes(), &pending)
	if info.Code != http.StatusOK || pending["device_name"] != "tafs on iPhone" || pending["key_fingerprint"] != auth.DeviceKeyFingerprint(der) {
		t.Fatalf("pending info: %d %s", info.Code, info.Body.String())
	}
	if miss := authed(http.MethodGet, "/v1/auth/device/pending?user_code=ZZZZ-ZZZZ"); miss.Code != http.StatusNotFound {
		t.Errorf("unknown code: %d", miss.Code)
	}

	if ar := rig.approve(t, started.UserCode); ar.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", ar.Code, ar.Body.String())
	}
	// Looked up again before the device took its token: approved, not expired (kiff/tafs#58).
	if again := authed(http.MethodGet, "/v1/auth/device/pending?user_code="+started.UserCode); again.Code != http.StatusConflict {
		t.Errorf("an approved code looked up again: %d %s", again.Code, again.Body.String())
	}
	// To anyone but its approver, an approved code is as unknown as any other.
	other := httptest.NewRecorder()
	rig.as("tenant-1", "user-other").ServeHTTP(other, httptest.NewRequest(http.MethodGet, "/v1/auth/device/pending?user_code="+started.UserCode, nil))
	if other.Code != http.StatusNotFound {
		t.Errorf("another person looked up an approved code: %d %s", other.Code, other.Body.String())
	}
	approveBody, _ := json.Marshal(map[string]string{"user_code": started.UserCode})
	twice := httptest.NewRecorder()
	rig.as("tenant-1", "user-other").ServeHTTP(twice, httptest.NewRequest(http.MethodPost, "/v1/auth/device/approve", bytes.NewReader(approveBody)))
	if twice.Code != http.StatusNotFound {
		t.Errorf("another person approving an approved code: %d %s", twice.Code, twice.Body.String())
	}
	if again := rig.approve(t, started.UserCode); again.Code != http.StatusConflict {
		t.Errorf("its approver approving it again: %d %s", again.Code, again.Body.String())
	}
	rig.advance(6 * time.Second)
	_, body := rig.poll(t, started.DeviceCode)
	parsed, err := auth.ParseDevSessionToken(body["access_token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := rig.sessions.GetByHash(context.Background(), parsed.Hash, rig.now)
	if err != nil || string(sess.DevicePublicKey) != string(der) || sess.Label != "tafs on iPhone" {
		t.Fatalf("session = %+v, %v; want the phone's key and name", sess, err)
	}

	// A CLI sign-in has no key and is not a device.
	cliCode, cliUser := rig.start(t)
	_ = cliCode
	if ar := rig.approve(t, cliUser); ar.Code != http.StatusOK {
		t.Fatal(ar.Body.String())
	}
	list := authed(http.MethodGet, "/v1/me/devices")
	var devices struct {
		Devices []map[string]any `json:"devices"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &devices); err != nil || len(devices.Devices) != 1 || devices.Devices[0]["id"] != sess.ID ||
		devices.Devices[0]["name"] != "tafs on iPhone" {
		t.Fatalf("devices: %s", list.Body.String())
	}

	if del := authed(http.MethodDelete, "/v1/me/devices/nope"); del.Code != http.StatusNotFound {
		t.Errorf("deleting an unknown device: %d", del.Code)
	}
	if del := authed(http.MethodDelete, "/v1/me/devices/"+sess.ID); del.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", del.Code, del.Body.String())
	}
	if _, err := rig.sessions.GetByHash(context.Background(), parsed.Hash, rig.now); err == nil {
		t.Error("removing the device left its session signed in")
	}
}
