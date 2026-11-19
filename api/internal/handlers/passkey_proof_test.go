package handlers_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
)

// creation is what navigator.credentials.create answers for this credential
// with attestation "none", base64url: its clientDataJSON and its
// attestationObject, whose CBOR is written out by hand.
func (w windowsHello) creation(tenant, subject, issuedAt, origin string, flags byte) (clientData, attestation string) {
	return w.creationFor(auth.RegistrationMessage(tenant, subject, issuedAt), map[string]any{"origin": origin}, flags)
}

// creationFor is creation over any challenge message, with clientDataJSON's fields as client overrides them.
func (w windowsHello) creationFor(message []byte, client map[string]any, flags byte) (clientData, attestation string) {
	digest := sha256.Sum256(message)
	fields := map[string]any{"type": "webauthn.create", "challenge": base64.RawURLEncoding.EncodeToString(digest[:]), "origin": "https://kiff.dev"}
	for k, v := range client {
		fields[k] = v
	}
	cd, _ := json.Marshal(fields)
	x, y := make([]byte, 32), make([]byte, 32)
	w.key.PublicKey.X.FillBytes(x)
	w.key.PublicKey.Y.FillBytes(y)
	// {1: 2, 3: -7, -1: 1, -2: x, -3: y}
	cose := append(append(append([]byte{0xa5, 0x01, 0x02, 0x03, 0x26, 0x20, 0x01, 0x21, 0x58, 0x20}, x...), 0x22, 0x58, 0x20), y...)
	rp := sha256.Sum256([]byte(w.rpID))
	authData := append(rp[:], flags, 0, 0, 0, 0)
	authData = append(authData, make([]byte, 16)...)
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(w.credential)))
	authData = append(append(authData, w.credential...), cose...)
	// {"fmt": "none", "attStmt": {}, "authData": authData}
	obj := []byte{0xa3, 0x63, 'f', 'm', 't', 0x64, 'n', 'o', 'n', 'e', 0x67, 'a', 't', 't', 'S', 't', 'm', 't', 0xa0, 0x68, 'a', 'u', 't', 'h', 'D', 'a', 't', 'a', 0x59}
	obj = binary.BigEndian.AppendUint16(obj, uint16(len(authData)))
	obj = append(obj, authData...)
	return base64.RawURLEncoding.EncodeToString(cd), base64.RawURLEncoding.EncodeToString(obj)
}

// §2.6: a passkey's registration proves its key. KIFF reads the credential
// and its key from what the authenticator made for kiff.dev, for this person,
// user-verified, and still takes it only with the approval key's signature.
func TestAPasskeysRegistrationProvesItsKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	passkeys := auth.NewMemoryPasskeyStore()
	apps, err := auth.ParseAndroidApps("dev.kiff.tafs=" + strings.TrimSuffix(strings.Repeat("AB:", 32), ":"))
	if err != nil {
		t.Fatal(err)
	}
	rp := auth.PasskeyRelyingParty(apps)
	r := newPairRig(t, handlers.WithDevicePasskeys(passkeys), handlers.WithDevicePasskeyRP(rp))
	r.authed = auth.DefaultRoutePolicy().WithClock(func() time.Time { return r.now }).WithSignedRequests(auth.NewMemorySignedRequests()).
		WithPasskeys(passkeys, rp).Wrap(r.inner)
	phone := r.phone
	phone.Roles = []string{"tenant_owner"}
	const path = "/v1/me/passkeys"
	n := 0
	post := func(signer string, body map[string]string) (int, map[string]any) {
		b, _ := json.Marshal(body)
		n++
		key := r.approval
		if signer == "device" {
			key = r.device
		}
		return send(r.authed, &phone, http.MethodPost, path, string(b), signRequest(t, key, "tenant-1", http.MethodPost, path, string(b), r.now.Add(time.Duration(n)*time.Second)))
	}
	issued := r.now.Format(time.RFC3339)
	const made = 0x01 | 0x04 | 0x40 // UP, UV, AT
	hello := newWindowsHello(t)
	body := func(h windowsHello, tenant, subject, at, origin string, flags byte) map[string]string {
		cd, ao := h.creation(tenant, subject, at, origin, flags)
		return map[string]string{"name": "My security key", "issued_at": at, "client_data_json": cd, "attestation_object": ao}
	}

	for name, c := range map[string]struct {
		body   map[string]string
		reason string
	}{
		"another person's challenge":  {body(hello, "tenant-1", "user-zed", issued, "https://kiff.dev", made), "challenge"},
		"another account's challenge": {body(hello, "tenant-2", "user-abc", issued, "https://kiff.dev", made), "challenge"},
		"no user verification":        {body(hello, "tenant-1", "user-abc", issued, "https://kiff.dev", 0x01|0x40), "user_verification"},
		"another origin":              {body(hello, "tenant-1", "user-abc", issued, "https://evil.example", made), "origin"},
		"issued 11 minutes ago":       {body(hello, "tenant-1", "user-abc", r.now.Add(-11*time.Minute).Format(time.RFC3339), "https://kiff.dev", made), "issued_at"},
		"not base64url":               {map[string]string{"name": "x", "issued_at": issued, "client_data_json": "%%%", "attestation_object": "AA"}, "malformed"},
	} {
		if code, out := post("approval", c.body); code != http.StatusBadRequest || out["error"] != "registration_invalid" || out["reason"] != c.reason {
			t.Errorf("%s: %d %v, want reason %s", name, code, out, c.reason)
		}
	}
	both := body(hello, "tenant-1", "user-abc", issued, "https://kiff.dev", made)
	both["credential_id"] = b64url(hello.credential)
	if code, out := post("approval", both); code != http.StatusBadRequest || out["error"] != "invalid_request" {
		t.Errorf("both bodies: %d %v", code, out)
	}
	// The proof never replaces the approval key: the device key alone is refused.
	if code, out := post("device", body(hello, "tenant-1", "user-abc", issued, "https://kiff.dev", made)); code != http.StatusForbidden || out["error"] != "approval_key_required" {
		t.Errorf("signed with the device key: %d %v", code, out)
	}
	if _, err := passkeys.Find(ctx, "tenant-1", hello.credential); err == nil {
		t.Fatal("a refused registration was kept")
	}

	good := body(hello, "tenant-1", "user-abc", issued, "https://kiff.dev", made)
	code, out := post("approval", good)
	pk, _ := out["passkey"].(map[string]any)
	if code != http.StatusCreated || pk["synced"] != false || pk["name"] != "My security key" {
		t.Fatalf("the proven registration: %d %v", code, out)
	}
	key, err := passkeys.Find(ctx, "tenant-1", hello.credential)
	if err != nil || !bytes.Equal(key.PublicKey, hello.der) || key.Subject != "user-abc" || key.DeviceID != "" {
		t.Fatalf("kept %+v %v: want the key the authenticator made", key, err)
	}
	// The same creation again only finds it registered.
	if code, out := post("approval", good); code != http.StatusConflict || out["error"] != "passkey_registered" {
		t.Errorf("a replay: %d %v", code, out)
	}
	// The Android app's origin, configured, registers too, and a synced one says so.
	phonePasskey := newWindowsHello(t)
	if code, out := post("approval", body(phonePasskey, "tenant-1", "user-abc", issued, apps[0].Origin(), made|0x08|0x10)); code != http.StatusCreated ||
		out["passkey"].(map[string]any)["synced"] != true {
		t.Errorf("from the Android app: %d %v", code, out)
	}
	// Both are listed as the person's.
	if code, out := send(r.authed, &phone, http.MethodGet, path, "", ""); code != http.StatusOK || len(out["passkeys"].([]any)) != 2 {
		t.Errorf("the list: %d %v", code, out)
	}
}
