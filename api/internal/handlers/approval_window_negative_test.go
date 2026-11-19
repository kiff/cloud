package handlers_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
)

// Negative tests for the approval window (kiff/tafs#494), from the design's
// list: a window signature never moves to another request, phone, person
// or account; no key but the phone's own device key signs inside it; every
// approval route that isn't routine refuses it; and an opening is made once.
// Each refusal reaches no handler.

// A tap's signature covers its request: sent with another path, body or
// account, it is refused, and nothing is reached.
// Category: authority
// Attack: a device-key signature made inside the window is moved to another request
// Refused: the signature doesn't verify for the other request
func TestAWindowSignatureDoesNotMoveToAnotherRequest(t *testing.T) {
	t.Parallel()
	r := newWindowRig(t)
	win := r.open()
	body := `{"moved":1}`
	header := signRequestWindow(t, r.device, "tenant-1", http.MethodPost, "/v1/me/mandates", body, r.now, win)
	other := r.phone
	other.Tenant = "tenant-2"
	for _, tc := range []struct {
		name     string
		id       auth.Identity
		path     string
		body     string
		reachKey string
	}{
		{"another route", r.phone, "/v1/me/did-this-happen/p1", body, "POST /v1/me/did-this-happen/{proposal}"},
		{"another body", r.phone, "/v1/me/mandates", `{"moved":2}`, "POST /v1/me/mandates"},
		{"another account", other, "/v1/me/mandates", body, "POST /v1/me/mandates"},
	} {
		before := r.reached[tc.reachKey]
		code, out := r.call(tc.id, http.MethodPost, tc.path, tc.body, header)
		if code < 400 || r.reached[tc.reachKey] != before {
			t.Errorf("%s: %d %v (reached %d)", tc.name, code, out, r.reached[tc.reachKey]-before)
		}
	}
	// The signature itself is still good for its own request.
	if code, out := r.call(r.phone, http.MethodPost, "/v1/me/mandates", body, header); code != http.StatusCreated || out["window"] != win {
		t.Fatalf("its own request: %d %v", code, out)
	}
}

// A window is the phone's that opened it: another phone of the same person,
// another person, or the same person on another account, naming it with
// its own device key, gets window_required, and nothing is reached.
// Category: authority
// Attack: a window opened behind one phone's fingerprint is used by another session
// Refused: window_required
func TestAWindowIsOnlyThePhoneThatOpenedIt(t *testing.T) {
	t.Parallel()
	r := newWindowRig(t)
	win := r.open()
	// Each case keeps the opener's approval key on the other session, so the
	// check that the window's opener is the session's key can't refuse it
	// for the check under test: the window's device, person and account.
	keyed := func(edit func(*auth.Identity)) (auth.Identity, string) {
		device, deviceDER := p256(t)
		id := r.phone
		id.DeviceKey = deviceDER
		edit(&id)
		body := `{"from":"` + id.Subject + id.DeviceID + id.Tenant + `"}`
		return id, signRequestWindow(t, device, id.Tenant, http.MethodPost, "/v1/me/mandates", body, r.now, win) + "|" + body
	}
	for _, tc := range []struct {
		name string
		edit func(*auth.Identity)
	}{
		{"another phone of the same person", func(id *auth.Identity) { id.DeviceID, id.DeviceSessionID = "dev_other", "sess-other" }},
		{"another person on the same phone id", func(id *auth.Identity) { id.Subject, id.DeviceSessionID = "user-mallory", "sess-mallory" }},
		{"the same person and phone on another account", func(id *auth.Identity) { id.Tenant = "tenant-2" }},
	} {
		id, signed := keyed(tc.edit)
		header, body, _ := strings.Cut(signed, "|")
		before := r.reached["POST /v1/me/mandates"]
		code, out := r.call(id, http.MethodPost, "/v1/me/mandates", body, header)
		if code != http.StatusForbidden || out["error"] != "window_required" || r.reached["POST /v1/me/mandates"] != before {
			t.Errorf("%s: %d %v", tc.name, code, out)
		}
	}
	// The phone that opened it still taps inside it.
	if code, out := r.routine("/v1/me/mandates", win); code != http.StatusCreated {
		t.Fatalf("the phone's own tap: %d %v", code, out)
	}
}

// Only the phone's own device key signs inside its window: a key that is
// neither of the session's, naming the open window, is refused, and
// nothing is reached.
// Category: authority
// Attack: a stolen session token with a key of the attacker's, naming the open window
// Refused: the signature doesn't verify for the session's keys
func TestAStrangersKeyNamingTheOpenWindowGetsNothing(t *testing.T) {
	t.Parallel()
	r := newWindowRig(t)
	win := r.open()
	stranger, _ := p256(t)
	body := `{"stranger":1}`
	code, out := r.call(r.phone, http.MethodPost, "/v1/me/mandates", body, signRequestWindow(t, stranger, "tenant-1", http.MethodPost, "/v1/me/mandates", body, r.now, win))
	if code != http.StatusForbidden || r.reached["POST /v1/me/mandates"] != 0 {
		t.Fatalf("a stranger's key in the window: %d %v (reached %d)", code, out, r.reached["POST /v1/me/mandates"])
	}
	// The same token with no signature at all gets nothing either.
	if code, out := r.call(r.phone, http.MethodPost, "/v1/me/mandates", body, ""); code < 400 || r.reached["POST /v1/me/mandates"] != 0 {
		t.Fatalf("no signature: %d %v", code, out)
	}
}

// Every approval route that isn't routine refuses a window signature with
// critical_key_required, before its handler: the approval key, behind the
// fingerprint, every time.
// Category: authority
// Attack: a critical approval (extra room, resume, sharing, a draft's submit, a new window) signed as a tap
// Refused: critical_key_required
func TestEveryCriticalApprovalRouteRefusesAWindow(t *testing.T) {
	t.Parallel()
	r := newWindowRig(t)
	win := r.open()
	routine := map[string]bool{}
	for _, p := range auth.PhoneWindowRules {
		routine[p] = true
	}
	checked := 0
	for _, pattern := range auth.PhoneApprovalRules {
		if routine[pattern] {
			continue
		}
		method, path, _ := strings.Cut(pattern, " ")
		for _, param := range []string{"{id}", "{proposal}"} {
			path = strings.ReplaceAll(path, param, "x1")
		}
		before := r.reached[pattern]
		body := `{"critical":"` + path + `"}`
		code, out := r.call(r.phone, method, path, body, signRequestWindow(t, r.device, "tenant-1", method, path, body, r.now, win))
		if code != http.StatusForbidden || out["error"] != "critical_key_required" || r.reached[pattern] != before {
			t.Errorf("%s with a window: %d %v", pattern, code, out)
		}
		checked++
	}
	if checked < 5 {
		t.Fatalf("only %d critical routes checked", checked)
	}
}

// An opening is used once: the same signed kiff.window.v1, sent again, is
// a replay, and opens nothing.
// Category: replay
// Attack: a captured window opening is sent again to open a window without a fingerprint
// Refused: signature_replayed
func TestAWindowOpeningIsUsedOnce(t *testing.T) {
	t.Parallel()
	r := newWindowRig(t)
	body := `{"message":"kiff.window.v1","expires":"` + r.now.Add(5*time.Minute).Format(time.RFC3339) + `","nonce":"` + strings.Repeat("ab", 16) + `"}`
	header := signRequest(t, r.approval, "tenant-1", http.MethodPost, "/v1/me/window", body, r.now)
	code, out := r.call(r.phone, http.MethodPost, "/v1/me/window", body, header)
	if code != http.StatusCreated {
		t.Fatalf("opening: %d %v", code, out)
	}
	first, _ := out["window"].(map[string]any)
	if code, out := r.call(r.phone, http.MethodPost, "/v1/me/window", body, header); code != http.StatusConflict || out["error"] != "signature_replayed" {
		t.Fatalf("the same opening again: %d %v", code, out)
	}
	// The window open is still the first one: the replay replaced nothing.
	got, err := r.windows.OpenFor(context.Background(), "dev_phone", r.now)
	if err != nil || got.ID != first["id"] {
		t.Fatalf("the open window after the replay: %v %v, first %v", got.ID, err, first["id"])
	}
}

// A window closed by a halt, a sign-out or a removed device stays closed:
// a restart that races the close never reopens it.
// Category: authority
// Attack: a tap's restart lands just after a halt closed the window
// Refused: the window stays closed
func TestARestartNeverReopensAClosedWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	for _, why := range []string{auth.WindowClosedByHalt, auth.WindowClosedBySignOut, auth.WindowClosedByRevoked, auth.WindowClosedByApp} {
		store := auth.NewMemoryWindowStore()
		w := auth.ApprovalWindow{ID: auth.NewWindowID(), Tenant: "tenant-1", Subject: "user-abc", DeviceID: "dev_phone", SessionID: "sess-phone",
			OpenedAt: now, ExpiresAt: now.Add(auth.WindowLength), CapAt: now.Add(auth.WindowCap), OpenedBy: "fp"}
		if err := store.Open(ctx, w); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CloseAll(ctx, auth.WindowMatch{Tenant: "tenant-1"}, now.Add(time.Minute), why); err != nil {
			t.Fatal(err)
		}
		_ = store.Restart(ctx, w.ID, w.Restarted(now.Add(time.Minute)))
		if got, err := store.OpenFor(ctx, "dev_phone", now.Add(2*time.Minute)); err == nil {
			t.Errorf("closed by %s, then restarted: open again (%v)", why, got.ID)
		}
	}
}
