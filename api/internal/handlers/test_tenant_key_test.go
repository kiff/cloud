package handlers_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
)

// A proof test tenant holds only proof test keys: a device or passkey whose
// key isn't one never joins it, by any path, and the refusal is 409
// test_tenant_key naming that key's fingerprint alone (auth.TestTenantGuard).
// One test per path that adds a key.

// testKeysGuard holds tenant to the keys in test.
func testKeysGuard(tenant string, test ...[]byte) *auth.TestTenantGuard {
	return auth.NewTestTenantGuard([]string{tenant}, func(der []byte) bool {
		for _, k := range test {
			if bytes.Equal(der, k) {
				return true
			}
		}
		return false
	})
}

// guardedRig is a pairing rig whose handler keeps devices and passkeys in
// stores held to the test keys, with the approving phone already there.
func guardedRig(t *testing.T, test ...[]byte) (*pairRig, auth.DeviceStore, *auth.MemoryPasskeyStore) {
	t.Helper()
	devices, passkeys := auth.NewMemoryDeviceStore(), auth.NewMemoryPasskeyStore()
	guard := func() *auth.TestTenantGuard { return testKeysGuard("tenant-1", test...) }
	var r *pairRig
	r = newPairRig(t, handlers.WithDeviceStore(auth.GuardDevices(devices, guard())),
		handlers.WithDevicePasskeys(auth.GuardPasskeys(passkeys, guard())))
	phone, err := r.devices.Get(context.Background(), r.phone.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	// Put in the store under the guard as an operator would: the guard holds what's added from now on.
	if err := devices.Insert(context.Background(), phone); err != nil {
		t.Fatal(err)
	}
	r.devices = devices
	return r, devices, passkeys
}

func wantTestTenantKey(t *testing.T, what string, code int, out map[string]any, key []byte) {
	t.Helper()
	if code != http.StatusConflict || out["error"] != "test_tenant_key" || out["fingerprint"] != auth.DeviceKeyFingerprint(key) {
		t.Fatalf("%s: %d %v, want 409 test_tenant_key with the key's fingerprint", what, code, out)
	}
}

func TestASignInWithAKeyThatIsntATestKeyNeverJoinsATestTenant(t *testing.T) {
	t.Parallel()
	_, testDER := p256(t)
	_, phoneDER := p256(t)
	for _, c := range []struct {
		name string
		key  []byte
		want int
	}{{"a person's own phone", phoneDER, http.StatusConflict}, {"the test phone", testDER, http.StatusOK}} {
		devices := auth.NewMemoryDeviceStore()
		sessions := auth.NewMemoryDeveloperSessionStore()
		h := handlers.NewDeviceAuthHandler(sessions, handlers.WithDeviceStore(auth.GuardDevices(devices, testKeysGuard("tenant-test", testDER))))
		pub, inner := http.NewServeMux(), http.NewServeMux()
		h.RegisterPublic(pub)
		h.RegisterAuthenticated(inner)

		body, _ := json.Marshal(map[string]string{"public_key": base64.StdEncoding.EncodeToString(c.key)})
		rec := httptest.NewRecorder()
		pub.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/device/start", bytes.NewReader(body)))
		var start struct {
			UserCode string `json:"user_code"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &start) != nil {
			t.Fatalf("%s, start: %d %s", c.name, rec.Code, rec.Body)
		}
		approve, _ := json.Marshal(map[string]string{"user_code": start.UserCode})
		rec = httptest.NewRecorder()
		withFakeIdentity(inner, auth.Identity{Tenant: "tenant-test", Subject: "user-maria", Method: auth.MethodSession}).
			ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/device/approve", bytes.NewReader(approve)))
		if rec.Code != c.want {
			t.Fatalf("%s, approve: %d %s, want %d", c.name, rec.Code, rec.Body, c.want)
		}
		kept, _ := devices.ListActiveInTenant(context.Background(), "tenant-test")
		if c.want == http.StatusConflict {
			var out map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			wantTestTenantKey(t, c.name, rec.Code, out, c.key)
			if bytes.Contains(rec.Body.Bytes(), []byte(auth.DeviceKeyFingerprint(testDER))) {
				t.Fatalf("the refusal names the test key: %s", rec.Body)
			}
			if len(kept) != 0 {
				t.Fatalf("%s: a device was kept anyway: %+v", c.name, kept)
			}
			continue
		}
		if len(kept) != 1 {
			t.Fatalf("%s: devices kept: %+v", c.name, kept)
		}
	}
}

func TestAComputerWhoseKeyIsntATestKeyNeverPairsOnATestTenant(t *testing.T) {
	t.Parallel()
	r, devices, _ := guardedRig(t)
	q := r.qr()
	if code, out := r.approve(q); code != http.StatusCreated {
		t.Fatalf("the phone's approval: %d %v", code, out)
	}
	code, out := r.claim(q["nonce"].(string), r.pcDER, r.pcKey)
	wantTestTenantKey(t, "the computer's claim", code, out, r.pcDER)
	kept, _ := devices.ListActiveInTenant(context.Background(), "tenant-1")
	if len(kept) != 1 || kept[0].ID != r.phone.DeviceID {
		t.Fatalf("only the phone is on the tenant: %+v", kept)
	}
}

func TestAPasskeyThatIsntATestKeyNeverJoinsATestTenantWithItsComputer(t *testing.T) {
	t.Parallel()
	hello := newWindowsHello(t)
	r, devices, passkeys := guardedRigWithComputer(t)
	q := passkeyQR(r, hello)
	if code, out := r.approve(q); code != http.StatusCreated {
		t.Fatalf("the phone's approval: %d %v", code, out)
	}
	code, out := r.claim(q["nonce"].(string), r.pcDER, r.pcKey)
	wantTestTenantKey(t, "the passkey computer's claim", code, out, hello.der)
	if _, err := passkeys.Find(context.Background(), "tenant-1", hello.credential); err == nil {
		t.Fatal("the passkey was kept anyway")
	}
	// The computer, whose own key is a test key, is undone with its passkey.
	kept, _ := devices.ListActiveInTenant(context.Background(), "tenant-1")
	if len(kept) != 1 || kept[0].ID != r.phone.DeviceID {
		t.Fatalf("only the phone is on the tenant: %+v", kept)
	}
}

// guardedRigWithComputer is guardedRig whose test keys are the phone's and
// the computer's own, so only the computer's passkey isn't one.
func guardedRigWithComputer(t *testing.T) (*pairRig, auth.DeviceStore, *auth.MemoryPasskeyStore) {
	t.Helper()
	keys := &[][]byte{}
	devices, passkeys := auth.NewMemoryDeviceStore(), auth.NewMemoryPasskeyStore()
	guard := auth.NewTestTenantGuard([]string{"tenant-1"}, func(der []byte) bool {
		for _, k := range *keys {
			if bytes.Equal(der, k) {
				return true
			}
		}
		return false
	})
	r := newPairRig(t, handlers.WithDeviceStore(auth.GuardDevices(devices, guard)), handlers.WithDevicePasskeys(auth.GuardPasskeys(passkeys, guard)))
	phone, err := r.devices.Get(context.Background(), r.phone.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := devices.Insert(context.Background(), phone); err != nil {
		t.Fatal(err)
	}
	r.devices = devices
	*keys = append(*keys, phone.PublicKey, r.pcDER)
	return r, devices, passkeys
}

func TestARegisteredPasskeyThatIsntATestKeyNeverJoinsATestTenant(t *testing.T) {
	t.Parallel()
	r, _, passkeys := guardedRig(t)
	_, other := p256(t)
	reg, _ := json.Marshal(map[string]string{"credential_id": b64url([]byte("a person's passkey")), "public_key": base64.StdEncoding.EncodeToString(other), "name": "mine"})
	// An admin's phone, as only an account's owners and admins register a passkey.
	admin := r.phone
	admin.Roles = []string{"admin"}
	code, out := send(r.authed, &admin, http.MethodPost, "/v1/me/passkeys", string(reg),
		signRequest(t, r.approval, "tenant-1", http.MethodPost, "/v1/me/passkeys", string(reg), r.now))
	wantTestTenantKey(t, "registering a passkey", code, out, other)
	if kept, _ := passkeys.ListActiveInTenant(context.Background(), "tenant-1"); len(kept) != 0 {
		t.Fatalf("the passkey was kept anyway: %+v", kept)
	}
}

// An approval key signs approvals as a device's key does: one that isn't a
// test key never joins a test tenant either, at sign-in, at a pairing or
// enrolled later.

func TestASignInWhoseApprovalKeyIsntATestKeyNeverJoinsATestTenant(t *testing.T) {
	t.Parallel()
	_, testDER := p256(t)
	_, securityKey := p256(t)
	guard := testKeysGuard("tenant-test", testDER)
	inner := auth.NewMemoryDeveloperSessionStore()
	devices := auth.NewMemoryDeviceStore()
	h := handlers.NewDeviceAuthHandler(auth.GuardSessions(inner, guard), handlers.WithDeviceStore(auth.GuardDevices(devices, guard)))
	pub, authed := http.NewServeMux(), http.NewServeMux()
	h.RegisterPublic(pub)
	h.RegisterAuthenticated(authed)
	body, _ := json.Marshal(map[string]string{"public_key": base64.StdEncoding.EncodeToString(testDER),
		"approval_public_key": base64.StdEncoding.EncodeToString(securityKey)})
	rec := httptest.NewRecorder()
	pub.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/device/start", bytes.NewReader(body)))
	var start struct {
		UserCode string `json:"user_code"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &start) != nil {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	approve, _ := json.Marshal(map[string]string{"user_code": start.UserCode})
	rec = httptest.NewRecorder()
	withFakeIdentity(authed, auth.Identity{Tenant: "tenant-test", Subject: "user-maria", Method: auth.MethodSession}).
		ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/device/approve", bytes.NewReader(approve)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	wantTestTenantKey(t, "a sign-in with a person's security key as its approval key", rec.Code, out, securityKey)
	if keys, _ := inner.ApprovalKeysInTenant(context.Background(), "tenant-test", time.Now()); len(keys) != 0 {
		t.Fatalf("the approval key was kept anyway: %d", len(keys))
	}
	// The test phone's device, made first, goes with the refused session.
	if kept, _ := devices.ListActiveInTenant(context.Background(), "tenant-test"); len(kept) != 0 {
		t.Fatalf("the device was kept anyway: %+v", kept)
	}
}

func TestAComputerWhoseApprovalKeyIsntATestKeyNeverPairsOnATestTenant(t *testing.T) {
	t.Parallel()
	keys := &[][]byte{}
	guard := auth.NewTestTenantGuard([]string{"tenant-1"}, func(der []byte) bool {
		for _, k := range *keys {
			if bytes.Equal(der, k) {
				return true
			}
		}
		return false
	})
	devices := auth.NewMemoryDeviceStore()
	r := newPairRigWithSessions(t, func(s auth.DeveloperSessionStore) auth.DeveloperSessionStore { return auth.GuardSessions(s, guard) },
		handlers.WithDeviceStore(auth.GuardDevices(devices, guard)))
	phone, err := r.devices.Get(context.Background(), r.phone.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := devices.Insert(context.Background(), phone); err != nil {
		t.Fatal(err)
	}
	r.devices = devices
	// The computer's own key is a test key; its approval key isn't.
	*keys = append(*keys, phone.PublicKey, r.pcDER)
	q := r.qr()
	if code, out := r.approve(q); code != http.StatusCreated {
		t.Fatalf("the phone's approval: %d %v", code, out)
	}
	code, out := r.claim(q["nonce"].(string), r.pcDER, r.pcKey)
	wantTestTenantKey(t, "the computer's claim", code, out, r.pcApprovalDER)
	if kept, _ := devices.ListActiveInTenant(context.Background(), "tenant-1"); len(kept) != 1 || kept[0].ID != r.phone.DeviceID {
		t.Fatalf("the computer was undone: %+v", kept)
	}
}

func TestAnApprovalKeyThatIsntATestKeyIsNeverEnrolledOnATestTenant(t *testing.T) {
	t.Parallel()
	var phoneKey []byte
	guard := auth.NewTestTenantGuard([]string{"tenant-1"}, func(der []byte) bool { return bytes.Equal(der, phoneKey) })
	r := newPairRigWithSessions(t, func(s auth.DeveloperSessionStore) auth.DeveloperSessionStore { return auth.GuardSessions(s, guard) })
	phoneKey = r.phone.DeviceKey
	// The phone's session, with no approval key yet.
	if err := r.sessions.Insert(context.Background(), auth.DeveloperSession{ID: r.phone.DeviceSessionID, Tenant: "tenant-1", Subject: r.phone.Subject,
		TokenHash: [32]byte{7}, CreatedAt: r.now, ExpiresAt: r.now.Add(time.Hour), DevicePublicKey: r.phone.DeviceKey, DeviceID: r.phone.DeviceID}); err != nil {
		t.Fatal(err)
	}
	phone := r.phone
	phone.ApprovalKey = nil
	_, securityKey := p256(t)
	body, _ := json.Marshal(map[string]string{"public_key": base64.StdEncoding.EncodeToString(securityKey)})
	code, out := send(r.authed, &phone, http.MethodPost, "/v1/me/device/approval-key", string(body),
		signRequest(t, r.device, "tenant-1", http.MethodPost, "/v1/me/device/approval-key", string(body), r.now))
	wantTestTenantKey(t, "enrolling a person's security key", code, out, securityKey)
	if keys, _ := r.sessions.ApprovalKeysInTenant(context.Background(), "tenant-1", r.now); len(keys) != 0 {
		t.Fatalf("the approval key was kept anyway: %d", len(keys))
	}
}
