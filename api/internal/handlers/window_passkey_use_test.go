package handlers_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/membership"
)

// passkeyWindowRig is one person with a Windows computer that approves with
// its own passkey (dev_pc), a phone that approves with its approval key
// (dev_phone), and KIFF's three places that use a window: the route policy
// (KIFF's own routes), /v1/me/device-signature (another service asking for
// the phone) and /v1/devices/verify (planque's v2 approval check, which
// Setup's reads and profile approvals share).
type passkeyWindowRig struct {
	t        *testing.T
	ctx      context.Context
	now      time.Time
	n        int
	windows  *auth.MemoryWindowStore
	passkeys *auth.MemoryPasskeyStore
	sessions auth.DeveloperSessionStore
	devices  *auth.MemoryDeviceStore
	policed  http.Handler
	verifier http.Handler
	reached  int

	pc, phone              auth.Identity
	pcKey, phoneKey        *ecdsa.PrivateKey
	phoneApproval          *ecdsa.PrivateKey
	hello                  windowsHello
	pcDER, phoneApproveDER []byte
}

func newPasskeyWindowRig(t *testing.T) *passkeyWindowRig {
	t.Helper()
	r := &passkeyWindowRig{t: t, ctx: context.Background(), now: time.Date(2026, 11, 17, 2, 0, 0, 0, time.UTC),
		windows: auth.NewMemoryWindowStore(), passkeys: auth.NewMemoryPasskeyStore(), sessions: auth.NewMemoryDeveloperSessionStore(),
		devices: auth.NewMemoryDeviceStore()}
	clock := func() time.Time { return r.now }
	svc := membership.NewService(membership.NewMemoryStore())
	if _, err := svc.EnsureFounder(r.ctx, "t1", "alice"); err != nil {
		t.Fatal(err)
	}
	var phoneDER []byte
	r.pcKey, r.pcDER = p256(t)
	r.phoneKey, phoneDER = p256(t)
	r.phoneApproval, r.phoneApproveDER = p256(t)
	r.hello = newWindowsHello(t)
	r.addDevice("dev_pc", "windows", r.pcDER)
	r.addDevice("dev_phone", "android", phoneDER)
	r.addSession("sess-pc", "dev_pc", r.pcDER, nil, auth.ApprovalModePasskey)
	r.addSession("sess-phone", "dev_phone", phoneDER, r.phoneApproveDER, auth.ApprovalModeKey)
	r.register(r.hello, "dev_pc")
	r.pc = r.identity("sess-pc", "dev_pc", r.pcDER, nil, auth.ApprovalModePasskey)
	r.phone = r.identity("sess-phone", "dev_phone", phoneDER, r.phoneApproveDER, auth.ApprovalModeKey)

	made := auth.NewMemorySignedRequests()
	inner := http.NewServeMux()
	wh := handlers.NewApprovalWindowHandler(r.windows, nil).WithPasskeys(r.passkeys)
	handlers.SetClock(wh, clock)
	wh.Register(inner)
	sh := handlers.NewDeviceSignatureHandler(made).WithWindows(r.windows).WithPasskeys(r.passkeys, auth.KiffRelyingParty)
	handlers.SetClock(sh, clock)
	sh.Register(inner)
	inner.HandleFunc("POST /v1/me/agent-drafts/{id}/confirm", func(w http.ResponseWriter, _ *http.Request) {
		r.reached++
		w.WriteHeader(http.StatusCreated)
	})
	r.policed = auth.DefaultRoutePolicy().WithClock(clock).WithSignedRequests(made).WithWindows(r.windows).
		WithPasskeys(r.passkeys, auth.KiffRelyingParty).Wrap(inner)

	mux := http.NewServeMux()
	vh := handlers.NewDeviceVerifyHandler(r.devices, svc).WithSessions(r.sessions).WithPasskeys(r.passkeys).WithWindows(r.windows)
	handlers.SetClock(vh, clock)
	vh.Register(mux)
	r.verifier = mux
	return r
}

func (r *passkeyWindowRig) addDevice(id, platform string, der []byte) {
	r.t.Helper()
	if err := r.devices.Insert(r.ctx, auth.ApprovalDevice{ID: id, Tenant: "t1", Subject: "alice", Name: "tafs on " + platform,
		Platform: platform, PublicKey: der, CreatedAt: r.now}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *passkeyWindowRig) addSession(id, device string, deviceDER, approvalDER []byte, mode string) {
	r.t.Helper()
	if err := r.sessions.Insert(r.ctx, auth.DeveloperSession{ID: id, Subject: "alice", Tenant: "t1", TokenHash: sha256.Sum256([]byte(id)),
		CreatedAt: r.now, ExpiresAt: r.now.Add(24 * time.Hour), DevicePublicKey: deviceDER, DeviceID: device,
		ApprovalPublicKey: approvalDER, ApprovalMode: mode}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *passkeyWindowRig) register(h windowsHello, device string) {
	r.t.Helper()
	if err := r.passkeys.Insert(r.ctx, auth.StoredPasskey{Passkey: auth.Passkey{ID: h.credential, Tenant: "t1", PublicKey: h.der},
		Subject: "alice", DeviceID: device, CreatedAt: r.now}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *passkeyWindowRig) identity(session, device string, deviceDER, approvalDER []byte, mode string) auth.Identity {
	return auth.Identity{Subject: "alice", Tenant: "t1", Method: auth.MethodDeveloper, MemberRole: membership.RoleAdmin, Roles: []string{"tenant_owner"},
		DeviceKey: deviceDER, DeviceSessionID: session, DeviceID: device, ApprovalKey: approvalDER, ApprovalMode: mode}
}

func (r *passkeyWindowRig) tick() time.Time {
	r.n++
	return r.now.Add(time.Duration(r.n) * time.Millisecond)
}

func (r *passkeyWindowRig) post(id auth.Identity, path, body string, headers map[string]string) (int, map[string]any) {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	withFakeIdentity(r.policed, id).ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (r *passkeyWindowRig) windowBody(id auth.Identity) string {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	b, _ := json.Marshal(auth.OpenWindow{Message: auth.WindowMessage, Session: id.DeviceSessionID, Device: id.DeviceID,
		Expires: r.now.Add(5 * time.Minute).Format(time.RFC3339), Nonce: hex.EncodeToString(nonce)})
	return string(b)
}

// openWithPasskey opens id's window with h's assertion.
func (r *passkeyWindowRig) openWithPasskey(id auth.Identity, h windowsHello) (int, map[string]any) {
	b := r.windowBody(id)
	return r.post(id, "/v1/me/window", b, map[string]string{auth.PasskeyAssertionHeader: h.header(r.t, "t1", http.MethodPost, "/v1/me/window", b, r.tick(), "")})
}

// openWithKey opens id's window with the approval key.
func (r *passkeyWindowRig) openWithKey(id auth.Identity, key *ecdsa.PrivateKey) (int, map[string]any) {
	b := r.windowBody(id)
	return r.post(id, "/v1/me/window", b, map[string]string{auth.DeviceSignatureHeader: signRequest(r.t, key, "t1", http.MethodPost, "/v1/me/window", b, r.tick())})
}

func (r *passkeyWindowRig) mustOpen(code int, out map[string]any) string {
	r.t.Helper()
	w, _ := out["window"].(map[string]any)
	id, _ := w["id"].(string)
	if code != http.StatusCreated || id == "" {
		r.t.Fatalf("opening a window: %d %v", code, out)
	}
	return id
}

// asked is /v1/me/device-signature for a request signed by key inside
// window: the HTTP status, the key, and whether the window is open.
func (r *passkeyWindowRig) asked(id auth.Identity, key *ecdsa.PrivateKey, window string) (int, any, bool) {
	r.t.Helper()
	body := `{"n":` + strconv.Itoa(r.n) + `}`
	sum := sha256.Sum256([]byte(body))
	sig := signRequestWindow(r.t, key, "t1", http.MethodPost, "/v1/tafs/t1/approve", body, r.tick(), window)
	b, _ := json.Marshal(map[string]string{"method": http.MethodPost, "uri": "/v1/tafs/t1/approve", "body_sha256": hex.EncodeToString(sum[:]), "signature": sig})
	code, out := r.post(id, "/v1/me/device-signature", string(b), nil)
	w, _ := out["window"].(map[string]any)
	return code, out["key"], w["open"] == true
}

// verified is planque's v2 approval check (/v1/devices/verify) of an
// approve signed by key on device, naming window: valid, key, window open.
func (r *passkeyWindowRig) verified(device string, key *ecdsa.PrivateKey, window string) (any, any, bool) {
	r.t.Helper()
	msg := "planque.request.v2\nt1\nPOST\n/v1/accounts/t1/grants/g1/approve\nabc\n" + r.tick().UTC().Format(time.RFC3339) + "\n" + window
	digest := sha256.Sum256([]byte(msg))
	sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
	b, _ := json.Marshal(map[string]string{"device_id": device, "message": base64.StdEncoding.EncodeToString([]byte(msg)),
		"signature": base64.StdEncoding.EncodeToString(sig), "capability": "tools.connect", "window": window})
	connector := auth.Identity{Tenant: "t1", Subject: "alice", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{auth.RoleConnector}}
	rec := httptest.NewRecorder()
	withFakeIdentity(r.verifier, connector).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/devices/verify", bytes.NewReader(b)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK {
		r.t.Fatalf("verify: %d %v", rec.Code, out)
	}
	w, _ := out["window"].(map[string]any)
	return out["valid"], out["key"], w["open"] == true
}

// A window a computer opened with its own passkey is used as a phone's is:
// planque's approve or confirm, signed with the device key and the window's
// id, is taken by /v1/me/device-signature and by planque's v2 approval check.
// KIFF's own approval routes still take only a fresh passkey assertion from
// a passkey session (TestAComputersOwnPasskeyApprovesExactlyWhatItSigned).
func TestAComputersPasskeyWindowIsUsedForPlanquesApprovals(t *testing.T) {
	t.Parallel()
	r := newPasskeyWindowRig(t)
	opened := r.now
	win := r.mustOpen(r.openWithPasskey(r.pc, r.hello))
	r.now = opened.Add(time.Minute)

	if code, key, open := r.asked(r.pc, r.pcKey, win); code != http.StatusOK || key != auth.KeyDevice || !open {
		t.Errorf("device-signature inside the passkey's window: %d %v open=%v", code, key, open)
	}
	if valid, key, open := r.verified("dev_pc", r.pcKey, win); valid != true || key != auth.KeyDevice || !open {
		t.Errorf("planque's v2 check inside the passkey's window: %v %v open=%v", valid, key, open)
	}
	// Each use restarts it, as for a phone.
	if got, err := r.windows.Get(r.ctx, win); err != nil || !got.ExpiresAt.Equal(r.now.Add(auth.WindowLength)) {
		t.Errorf("not restarted: %+v %v", got, err)
	}
	// A window id that isn't the open one: not open.
	if _, _, open := r.verified("dev_pc", r.pcKey, auth.NewWindowID()); open {
		t.Error("a window that isn't open counted")
	}
	// KIFF's own approval route: the window's tap approves nothing for a
	// passkey session.
	body := `{}`
	code, out := r.post(r.pc, "/v1/me/agent-drafts/dr_1/confirm", body,
		map[string]string{auth.DeviceSignatureHeader: signRequestWindow(t, r.pcKey, "t1", http.MethodPost, "/v1/me/agent-drafts/dr_1/confirm", body, r.tick(), win)})
	if code != http.StatusForbidden || out["error"] != "passkey_required" || r.reached != 0 {
		t.Errorf("KIFF's own route inside the passkey's window: %d %v (reached %d)", code, out, r.reached)
	}
}

// What ends or never gives a passkey's window.
func TestAComputersPasskeyWindowEnds(t *testing.T) {
	t.Parallel()
	closed := func(t *testing.T, r *passkeyWindowRig, name, win string) {
		t.Helper()
		if code, _, open := r.asked(r.pc, r.pcKey, win); code != http.StatusOK || open {
			t.Errorf("%s: device-signature %d open=%v", name, code, open)
		}
		if valid, _, open := r.verified("dev_pc", r.pcKey, win); valid != true || open {
			t.Errorf("%s: planque's v2 check valid=%v open=%v", name, valid, open)
		}
	}

	t.Run("the passkey removed after the open", func(t *testing.T) {
		r := newPasskeyWindowRig(t)
		win := r.mustOpen(r.openWithPasskey(r.pc, r.hello))
		if err := r.passkeys.RevokeForDevice(r.ctx, "dev_pc", r.now); err != nil {
			t.Fatal(err)
		}
		closed(t, r, "removed", win)
	})

	t.Run("the passkey found synced after the open", func(t *testing.T) {
		r := newPasskeyWindowRig(t)
		win := r.mustOpen(r.openWithPasskey(r.pc, r.hello))
		if err := r.passkeys.SetSignCount(r.ctx, "t1", r.hello.credential, 0, true); err != nil {
			t.Fatal(err)
		}
		closed(t, r, "synced", win)
	})

	t.Run("another device naming the computer's window", func(t *testing.T) {
		r := newPasskeyWindowRig(t)
		win := r.mustOpen(r.openWithPasskey(r.pc, r.hello))
		// The phone, a key-mode device of the same person.
		if code, _, open := r.asked(r.phone, r.phoneKey, win); code != http.StatusOK || open {
			t.Errorf("the phone naming the computer's window: %d open=%v", code, open)
		}
		if _, _, open := r.verified("dev_phone", r.phoneKey, win); open {
			t.Error("planque's check: the phone naming the computer's window counted")
		}
		// A second passkey computer, with its own passkey.
		key2, der2 := p256(t)
		r.addDevice("dev_pc2", "windows", der2)
		r.addSession("sess-pc2", "dev_pc2", der2, nil, auth.ApprovalModePasskey)
		r.register(newWindowsHello(t), "dev_pc2")
		pc2 := r.identity("sess-pc2", "dev_pc2", der2, nil, auth.ApprovalModePasskey)
		if code, _, open := r.asked(pc2, key2, win); code != http.StatusOK || open {
			t.Errorf("another computer naming the window: %d open=%v", code, open)
		}
		if _, _, open := r.verified("dev_pc2", key2, win); open {
			t.Error("planque's check: another computer naming the window counted")
		}
		// The computer itself still has it.
		if _, _, open := r.verified("dev_pc", r.pcKey, win); !open {
			t.Error("the computer's own window stopped counting")
		}
	})

	t.Run("a view-only session opens none and uses none", func(t *testing.T) {
		r := newPasskeyWindowRig(t)
		keyV, derV := p256(t)
		r.addDevice("dev_view", "macos", derV)
		r.addSession("sess-view", "dev_view", derV, nil, auth.ApprovalModeView)
		helloV := newWindowsHello(t)
		r.register(helloV, "dev_view")
		view := r.identity("sess-view", "dev_view", derV, nil, auth.ApprovalModeView)
		if code, out := r.openWithPasskey(view, helloV); code != http.StatusForbidden || out["error"] != "view_only" {
			t.Errorf("a view session opening a window: %d %v", code, out)
		}
		if _, err := r.windows.OpenFor(r.ctx, "dev_view", r.now); err == nil {
			t.Fatal("a view session opened a window")
		}
		// Even a window kept for it, opened by its device's passkey, counts
		// for nothing: the session approves nothing.
		win := auth.ApprovalWindow{ID: auth.NewWindowID(), Tenant: "t1", Subject: "alice", DeviceID: "dev_view", SessionID: "sess-view",
			OpenedAt: r.now, ExpiresAt: r.now.Add(auth.WindowLength), CapAt: r.now.Add(auth.WindowCap), OpenedBy: auth.DeviceKeyFingerprint(helloV.der)}
		if err := r.windows.Open(r.ctx, win); err != nil {
			t.Fatal(err)
		}
		if code, out := r.post(view, "/v1/me/device-signature", `{}`, nil); code != http.StatusForbidden || out["error"] != "view_only" {
			t.Errorf("a view session asking device-signature: %d %v", code, out)
		}
		if _, _, open := r.verified("dev_view", keyV, win.ID); open {
			t.Error("planque's check: a view session's window counted")
		}
	})

	t.Run("key-mode and passkey windows don't cross", func(t *testing.T) {
		r := newPasskeyWindowRig(t)
		pcWin := r.mustOpen(r.openWithPasskey(r.pc, r.hello))
		phoneWin := r.mustOpen(r.openWithKey(r.phone, r.phoneApproval))
		if _, _, open := r.verified("dev_phone", r.phoneKey, phoneWin); !open {
			t.Error("the phone's own window doesn't count")
		}
		if _, _, open := r.verified("dev_pc", r.pcKey, pcWin); !open {
			t.Error("the computer's own window doesn't count")
		}
		if code, _, open := r.asked(r.pc, r.pcKey, phoneWin); code != http.StatusOK || open {
			t.Errorf("the computer naming the phone's window: %d open=%v", code, open)
		}
		if _, _, open := r.verified("dev_pc", r.pcKey, phoneWin); open {
			t.Error("planque's check: the computer naming the phone's window counted")
		}
		if code, _, open := r.asked(r.phone, r.phoneKey, pcWin); code != http.StatusOK || open {
			t.Errorf("the phone naming the computer's window: %d open=%v", code, open)
		}
		if _, _, open := r.verified("dev_phone", r.phoneKey, pcWin); open {
			t.Error("planque's check: the phone naming the computer's window counted")
		}
	})

	t.Run("expiry ends it", func(t *testing.T) {
		r := newPasskeyWindowRig(t)
		win := r.mustOpen(r.openWithPasskey(r.pc, r.hello))
		r.now = r.now.Add(auth.WindowLength + time.Second)
		closed(t, r, "expired", win)
	})

	t.Run("the cap ends it, however often it's used", func(t *testing.T) {
		r := newPasskeyWindowRig(t)
		start := r.now
		win := r.mustOpen(r.openWithPasskey(r.pc, r.hello))
		for at := 4 * time.Minute; at < auth.WindowCap; at += 4 * time.Minute {
			r.now = start.Add(at)
			if _, _, open := r.verified("dev_pc", r.pcKey, win); !open {
				t.Fatalf("closed at +%v, before its cap", at)
			}
		}
		r.now = start.Add(auth.WindowCap + time.Second)
		closed(t, r, "past its cap", win)
	})
}

// A window counts only for the session that opened it, in key and passkey
// modes alike: a second session on the same device, even one holding the
// same approval key, gets nothing from it, and nor does a window kept with
// no session (one opened before windows named their session).
func TestAWindowCountsOnlyForTheSessionThatOpenedIt(t *testing.T) {
	t.Parallel()
	confirm := func(r *passkeyWindowRig, id auth.Identity, key *ecdsa.PrivateKey, win string) (int, map[string]any) {
		body := `{}`
		return r.post(id, "/v1/me/agent-drafts/dr_1/confirm", body,
			map[string]string{auth.DeviceSignatureHeader: signRequestWindow(r.t, key, "t1", http.MethodPost, "/v1/me/agent-drafts/dr_1/confirm", body, r.tick(), win)})
	}

	t.Run("key mode: a second session with the same approval key", func(t *testing.T) {
		r := newPasskeyWindowRig(t)
		r.addSession("sess-phone-2", "dev_phone", r.phone.DeviceKey, r.phoneApproveDER, auth.ApprovalModeKey)
		second := r.identity("sess-phone-2", "dev_phone", r.phone.DeviceKey, r.phoneApproveDER, auth.ApprovalModeKey)
		win := r.mustOpen(r.openWithKey(r.phone, r.phoneApproval))

		if code, _, open := r.asked(second, r.phoneKey, win); code != http.StatusOK || open {
			t.Errorf("device-signature from the second session: %d open=%v", code, open)
		}
		if code, out := confirm(r, second, r.phoneKey, win); code != http.StatusForbidden || out["error"] != "window_required" || r.reached != 0 {
			t.Errorf("KIFF's own route from the second session: %d %v (reached %d)", code, out, r.reached)
		}
		// The session that opened it still uses it.
		if code, key, open := r.asked(r.phone, r.phoneKey, win); code != http.StatusOK || key != auth.KeyDevice || !open {
			t.Errorf("device-signature from the opener: %d %v open=%v", code, key, open)
		}
		if code, out := confirm(r, r.phone, r.phoneKey, win); code != http.StatusCreated || r.reached != 1 {
			t.Errorf("KIFF's own route from the opener: %d %v (reached %d)", code, out, r.reached)
		}
	})

	t.Run("passkey mode: a second passkey session on the computer", func(t *testing.T) {
		r := newPasskeyWindowRig(t)
		r.addSession("sess-pc-2", "dev_pc", r.pcDER, nil, auth.ApprovalModePasskey)
		second := r.identity("sess-pc-2", "dev_pc", r.pcDER, nil, auth.ApprovalModePasskey)
		win := r.mustOpen(r.openWithPasskey(r.pc, r.hello))

		if code, _, open := r.asked(second, r.pcKey, win); code != http.StatusOK || open {
			t.Errorf("device-signature from the second passkey session: %d open=%v", code, open)
		}
		if code, key, open := r.asked(r.pc, r.pcKey, win); code != http.StatusOK || key != auth.KeyDevice || !open {
			t.Errorf("device-signature from the opener: %d %v open=%v", code, key, open)
		}
	})

	t.Run("planque's check: the opener's session gone, another still on the device", func(t *testing.T) {
		r := newPasskeyWindowRig(t)
		win := r.mustOpen(r.openWithPasskey(r.pc, r.hello))
		if _, _, open := r.verified("dev_pc", r.pcKey, win); !open {
			t.Fatal("the opener's window doesn't count at planque's check")
		}
		// A second session on the computer, then the opener's ends without
		// its window being closed (it expired): the window is the opener's
		// only, so it stops counting.
		r.addSession("sess-pc-2", "dev_pc", r.pcDER, nil, auth.ApprovalModePasskey)
		if err := r.sessions.Revoke(r.ctx, "sess-pc", r.now); err != nil {
			t.Fatal(err)
		}
		if valid, _, open := r.verified("dev_pc", r.pcKey, win); valid != true || open {
			t.Errorf("planque's check after the opener's session ended: valid=%v open=%v", valid, open)
		}
	})

	t.Run("a window kept with no session counts for nothing", func(t *testing.T) {
		r := newPasskeyWindowRig(t)
		for _, c := range []struct {
			name     string
			id       auth.Identity
			key      *ecdsa.PrivateKey
			device   string
			openedBy []byte
		}{
			{"the phone's", r.phone, r.phoneKey, "dev_phone", r.phoneApproveDER},
			{"the computer's", r.pc, r.pcKey, "dev_pc", r.hello.der},
		} {
			w := auth.ApprovalWindow{ID: auth.NewWindowID(), Tenant: "t1", Subject: "alice", DeviceID: c.device,
				OpenedAt: r.now, ExpiresAt: r.now.Add(auth.WindowLength), CapAt: r.now.Add(auth.WindowCap), OpenedBy: auth.DeviceKeyFingerprint(c.openedBy)}
			if err := r.windows.Open(r.ctx, w); err != nil {
				t.Fatal(err)
			}
			if code, _, open := r.asked(c.id, c.key, w.ID); code != http.StatusOK || open {
				t.Errorf("%s window with no session, at device-signature: %d open=%v", c.name, code, open)
			}
			if valid, _, open := r.verified(c.device, c.key, w.ID); valid != true || open {
				t.Errorf("%s window with no session, at planque's check: valid=%v open=%v", c.name, valid, open)
			}
		}
	})
}
