package auth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
)

// kiff/tafs#494: an opening is kiff.window.v1 for the signer's own session
// and device, with a nonce and an expiry at most 5 minutes ahead; the
// server's own expiry never passes 5 minutes from its clock.
func TestOpeningAWindowIsCheckedAgainstTheSessionAndTheClock(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	id := auth.Identity{Tenant: "tenant-1", Subject: "user-abc", DeviceSessionID: "sess-phone", DeviceID: "dev_abc"}
	good := auth.OpenWindow{Message: auth.WindowMessage, Session: "sess-phone", Device: "dev_abc", Expires: now.Add(4 * time.Minute).Format(time.RFC3339), Nonce: strings.Repeat("n", 22)}
	expires, err := good.Check(id, now)
	if err != nil || !expires.Equal(now.Add(4*time.Minute)) {
		t.Fatalf("a good opening: %v, expires %v", err, expires)
	}
	// A phone's clock a little ahead: its expiry is kept to the server's 5 minutes.
	ahead := good
	ahead.Expires = now.Add(5*time.Minute + 20*time.Second).Format(time.RFC3339)
	if expires, err := ahead.Check(id, now); err != nil || !expires.Equal(now.Add(5*time.Minute)) {
		t.Errorf("a little ahead: %v, expires %v", err, expires)
	}
	bad := map[string]auth.OpenWindow{
		"another message":   {Message: "kiff.request.v1", Session: good.Session, Device: good.Device, Expires: good.Expires, Nonce: good.Nonce},
		"another session":   {Message: good.Message, Session: "sess-other", Device: good.Device, Expires: good.Expires, Nonce: good.Nonce},
		"another device":    {Message: good.Message, Session: good.Session, Device: "dev_other", Expires: good.Expires, Nonce: good.Nonce},
		"a short nonce":     {Message: good.Message, Session: good.Session, Device: good.Device, Expires: good.Expires, Nonce: "short"},
		"expires past":      {Message: good.Message, Session: good.Session, Device: good.Device, Expires: now.Add(-time.Second).Format(time.RFC3339), Nonce: good.Nonce},
		"expires far":       {Message: good.Message, Session: good.Session, Device: good.Device, Expires: now.Add(6 * time.Minute).Format(time.RFC3339), Nonce: good.Nonce},
		"expires malformed": {Message: good.Message, Session: good.Session, Device: good.Device, Expires: "tomorrow", Nonce: good.Nonce},
	}
	for name, o := range bad {
		if _, err := o.Check(id, now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A phone that doesn't know its session's id, or its device's, leaves them out: the signer's own.
	bare := auth.OpenWindow{Message: auth.WindowMessage, Expires: good.Expires, Nonce: good.Nonce}
	if _, err := bare.Check(id, now); err != nil {
		t.Errorf("an opening naming no session or device: %v", err)
	}
	// A session with no device has no window.
	if _, err := good.Check(auth.Identity{Tenant: "tenant-1", Subject: "user-abc", DeviceSessionID: "sess-phone"}, now); err == nil {
		t.Error("a session without a device opened a window")
	}
}

// The memory store: one open window per device, restarted up to its cap,
// closed once, and ended together by what matches.
func TestWindowStoreKeepsOneOpenWindowPerDevice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := auth.NewMemoryWindowStore()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	w := auth.ApprovalWindow{ID: auth.NewWindowID(), Tenant: "tenant-1", Subject: "user-abc", DeviceID: "dev_abc", SessionID: "sess-1",
		OpenedAt: now, ExpiresAt: now.Add(auth.WindowLength), CapAt: now.Add(auth.WindowCap), OpenedBy: "3f2a 91c0 7b1e 0d44"}
	if err := store.Open(ctx, w); err != nil {
		t.Fatal(err)
	}
	if got, err := store.OpenFor(ctx, "dev_abc", now.Add(time.Minute)); err != nil || got.ID != w.ID {
		t.Fatalf("open for the device: %v %+v", err, got)
	}
	if _, err := store.OpenFor(ctx, "dev_other", now); !errors.Is(err, auth.ErrWindowNotFound) {
		t.Errorf("another device's window: %v", err)
	}
	// Expired: not open. Restarted at 4 minutes: open until 9; at 14: capped at 15.
	if _, err := store.OpenFor(ctx, "dev_abc", now.Add(6*time.Minute)); !errors.Is(err, auth.ErrWindowNotFound) {
		t.Errorf("after expiry: %v", err)
	}
	at := now.Add(4 * time.Minute)
	if err := store.Restart(ctx, w.ID, w.Restarted(at)); err != nil {
		t.Fatal(err)
	}
	if got, err := store.OpenFor(ctx, "dev_abc", now.Add(8*time.Minute)); err != nil || !got.ExpiresAt.Equal(now.Add(9*time.Minute)) {
		t.Errorf("restarted: %v %v", err, got.ExpiresAt)
	}
	at = now.Add(14 * time.Minute)
	got, _ := store.Get(ctx, w.ID)
	if e := got.Restarted(at); !e.Equal(now.Add(auth.WindowCap)) {
		t.Errorf("a restart near the cap: %v", e)
	}
	_ = store.Restart(ctx, w.ID, got.Restarted(at))
	if _, err := store.OpenFor(ctx, "dev_abc", now.Add(15*time.Minute)); !errors.Is(err, auth.ErrWindowNotFound) {
		t.Errorf("at the cap: %v", err)
	}
	// A new window on the device replaces the open one.
	w2 := w
	w2.ID, w2.OpenedAt, w2.ExpiresAt, w2.CapAt = auth.NewWindowID(), at, at.Add(auth.WindowLength), at.Add(auth.WindowCap)
	if err := store.Open(ctx, w2); err != nil {
		t.Fatal(err)
	}
	if old, _ := store.Get(ctx, w.ID); old.ClosedAt == nil || old.ClosedBy != auth.WindowClosedByReplaced {
		t.Errorf("the first window after a second: %+v", old)
	}
	// Closed once; a second close keeps the first.
	if err := store.Close(ctx, w2.ID, at.Add(time.Minute), auth.WindowClosedByApp); err != nil {
		t.Fatal(err)
	}
	_ = store.Close(ctx, w2.ID, at.Add(2*time.Minute), auth.WindowClosedByHalt)
	if got, _ := store.Get(ctx, w2.ID); got.ClosedBy != auth.WindowClosedByApp || !got.ClosedAt.Equal(at.Add(time.Minute)) {
		t.Errorf("closed twice: %+v", got)
	}
	if err := store.Close(ctx, "win_none", at, auth.WindowClosedByApp); !errors.Is(err, auth.ErrWindowNotFound) {
		t.Errorf("closing none: %v", err)
	}
	// CloseAll by tenant, session, device.
	w3 := w
	w3.ID, w3.SessionID = auth.NewWindowID(), "sess-2"
	w4 := w
	w4.ID, w4.DeviceID, w4.Tenant = auth.NewWindowID(), "dev_other", "tenant-2"
	for _, x := range []auth.ApprovalWindow{w3, w4} {
		if err := store.Open(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := store.CloseAll(ctx, auth.WindowMatch{Session: "sess-none"}, now, auth.WindowClosedBySignOut); n != 0 {
		t.Errorf("closed %d for a session with none", n)
	}
	if n, _ := store.CloseAll(ctx, auth.WindowMatch{Tenant: "tenant-1"}, now.Add(time.Second), auth.WindowClosedByHalt); n != 1 {
		t.Errorf("a halt closed %d, want 1", n)
	}
	if got, _ := store.Get(ctx, w4.ID); got.ClosedAt != nil {
		t.Error("the other account's window closed by the halt")
	}
	if n, _ := store.CloseAll(ctx, auth.WindowMatch{Device: "dev_other"}, now.Add(time.Second), auth.WindowClosedByRevoked); n != 1 {
		t.Errorf("a revoked device closed %d, want 1", n)
	}
}

// A signature inside a window names it in the header (v2) and in the signed
// message, so it can't be moved to another window; a v1 names none.
func TestASignatureInsideAWindowNamesIt(t *testing.T) {
	t.Parallel()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	ts := now.Format(time.RFC3339)
	window := auth.NewWindowID()
	body := []byte(`{"a":1}`)
	sign := func(msg []byte) string {
		digest := sha256.Sum256(msg)
		sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
		return base64.StdEncoding.EncodeToString(sig)
	}
	v2 := "t=" + ts + ",v2=" + sign(auth.RequestMessageWindow("tenant-1", http.MethodPost, "/v1/me/mandates", body, ts, window)) + ",w=" + window
	id, named, err := auth.VerifyRequestWindow(pub, "tenant-1", http.MethodPost, "/v1/me/mandates", body, v2, now)
	if err != nil || named != window || len(id) == 0 {
		t.Fatalf("v2: %v, window %q", err, named)
	}
	if auth.SignatureWindow(v2) != window {
		t.Error("the header's window isn't read")
	}
	// The same signature moved to another window, or sent as v1, fails.
	other := auth.NewWindowID()
	moved := strings.Replace(v2, ",w="+window, ",w="+other, 1)
	if _, _, err := auth.VerifyRequestWindow(pub, "tenant-1", http.MethodPost, "/v1/me/mandates", body, moved, now); err == nil {
		t.Error("a signature moved to another window verified")
	}
	stripped := "t=" + ts + ",v1=" + strings.TrimPrefix(strings.Split(v2, ",w=")[0], "t="+ts+",v2=")
	if _, _, err := auth.VerifyRequestWindow(pub, "tenant-1", http.MethodPost, "/v1/me/mandates", body, stripped, now); err == nil {
		t.Error("a window signature passed as v1 verified")
	}
	// A v2 without its window, a v1 with one, or a malformed window id, is no header.
	for _, h := range []string{strings.Split(v2, ",w=")[0], stripped + ",w=" + window, strings.Replace(v2, window, "win_short", 1), v2 + ",x=1"} {
		if _, _, err := auth.VerifyRequestWindow(pub, "tenant-1", http.MethodPost, "/v1/me/mandates", body, h, now); !errors.Is(err, auth.ErrRequestSignature) {
			t.Errorf("%q: %v", h, err)
		}
	}
	// A v1 still verifies as before, naming no window.
	v1 := "t=" + ts + ",v1=" + sign(auth.RequestMessage("tenant-1", http.MethodPost, "/v1/me/mandates", body, ts))
	if _, named, err := auth.VerifyRequestWindow(pub, "tenant-1", http.MethodPost, "/v1/me/mandates", body, v1, now); err != nil || named != "" {
		t.Errorf("v1: %v, window %q", err, named)
	}
}
