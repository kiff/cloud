package handlers_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
)

// A computer that approves with its own passkey opens an approval window with
// it, as a phone does with its approval key (Windows' Connect apps, P9): the
// window stands for Windows Hello, so it opens only with the passkey's
// assertion, user verified, this device's own, used once. Its device key, or
// a passkey without user verification, another device's, a synced one or a
// replayed assertion, opens nothing.
func TestAComputersPasskeyOpensAnApprovalWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 1, 0, 0, 0, time.UTC)
	windows := auth.NewMemoryWindowStore()
	inner := http.NewServeMux()
	passkeys := auth.NewMemoryPasskeyStore()
	wh := handlers.NewApprovalWindowHandler(windows, nil).WithPasskeys(passkeys)
	handlers.SetClock(wh, func() time.Time { return now })
	wh.Register(inner)
	policed := auth.DefaultRoutePolicy().WithClock(func() time.Time { return now }).WithSignedRequests(auth.NewMemorySignedRequests()).
		WithWindows(windows).WithPasskeys(passkeys, auth.KiffRelyingParty).Wrap(inner)

	deviceKey, deviceDER := p256(t)
	pc := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"},
		DeviceKey: deviceDER, DeviceSessionID: "sess-pc", DeviceID: "dev_pc", ApprovalMode: auth.ApprovalModePasskey}
	register := func(h windowsHello, deviceID string) {
		if err := passkeys.Insert(ctx, auth.StoredPasskey{Passkey: auth.Passkey{ID: h.credential, Tenant: "tenant-1", PublicKey: h.der},
			Subject: "user-abc", DeviceID: deviceID, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	hello := newWindowsHello(t)
	register(hello, "dev_pc")

	body := func() string {
		nonce := make([]byte, 16)
		_, _ = rand.Read(nonce)
		b, _ := json.Marshal(auth.OpenWindow{Message: auth.WindowMessage, Session: "sess-pc", Device: "dev_pc",
			Expires: now.Add(5 * time.Minute).Format(time.RFC3339), Nonce: hex.EncodeToString(nonce)})
		return string(b)
	}
	open := func(b, passkey, signature string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/v1/me/window", strings.NewReader(b))
		if passkey != "" {
			req.Header.Set(auth.PasskeyAssertionHeader, passkey)
		}
		if signature != "" {
			req.Header.Set(auth.DeviceSignatureHeader, signature)
		}
		rec := httptest.NewRecorder()
		withFakeIdentity(policed, pc).ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	n := 0
	fresh := func(h windowsHello, b string) string {
		n++
		return h.header(t, "tenant-1", http.MethodPost, "/v1/me/window", b, now.Add(time.Duration(n)*time.Second), "")
	}
	refused := func(name string, code int, out map[string]any, wantCode int, wantError string) {
		t.Helper()
		if code != wantCode || out["error"] != wantError {
			t.Errorf("%s: %d %v, want %d %s", name, code, out, wantCode, wantError)
		}
		if _, err := windows.OpenFor(ctx, "dev_pc", now); err == nil {
			t.Fatalf("%s opened a window", name)
		}
	}

	// The device key's signature opens nothing: the window stands for Windows Hello.
	b := body()
	code, out := open(b, "", signRequest(t, deviceKey, "tenant-1", http.MethodPost, "/v1/me/window", b, now))
	refused("the device key", code, out, http.StatusForbidden, "passkey_required")

	// No user verification (UP only).
	noUV := hello
	noUV.flags = 0x01
	b = body()
	code, out = open(b, fresh(noUV, b), "")
	refused("no user verification", code, out, http.StatusForbidden, "user_verification_required")

	// Another device's credential, registered for another computer.
	other := newWindowsHello(t)
	register(other, "dev_other")
	b = body()
	code, out = open(b, fresh(other, b), "")
	refused("another device's passkey", code, out, http.StatusForbidden, "passkey_not_this_device")

	// A synced passkey (BE and BS) isn't this computer's.
	synced := hello
	synced.flags = 0x01 | 0x04 | 0x08 | 0x10
	b = body()
	code, out = open(b, fresh(synced, b), "")
	refused("a synced passkey", code, out, http.StatusForbidden, "passkey_synced")

	// An account passkey (no device) approves a request, but opens no window:
	// only this computer's own passkey can use one.
	account := newWindowsHello(t)
	register(account, "")
	b = body()
	code, out = open(b, fresh(account, b), "")
	refused("an account passkey", code, out, http.StatusForbidden, "passkey_not_this_device")

	// Its own passkey, user verified: the window opens, opened by the passkey.
	b = body()
	assertion := fresh(hello, b)
	code, out = open(b, assertion, "")
	if code != http.StatusCreated {
		t.Fatalf("the computer's passkey opening a window: %d %v", code, out)
	}
	w, err := windows.OpenFor(ctx, "dev_pc", now)
	if err != nil || w.OpenedBy != auth.DeviceKeyFingerprint(hello.der) || w.SessionID != "sess-pc" {
		t.Fatalf("the window as kept: %+v %v", w, err)
	}

	// The same assertion again: refused as a replay, whatever the window's state.
	code, out = open(b, assertion, "")
	if code != http.StatusConflict || out["error"] != "signature_replayed" {
		t.Errorf("the same assertion twice: %d %v", code, out)
	}
}
