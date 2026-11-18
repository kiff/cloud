package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
)

// failingPasskeys is a passkey store that can't be read.
type failingPasskeys struct{ auth.PasskeyStore }

func (failingPasskeys) ListForPerson(context.Context, string, string) ([]auth.StoredPasskey, error) {
	return nil, errors.New("down")
}

// A window counts only for the session that opened it, and only while what
// opened it is still that session's approval authority: its approval key, or,
// for a passkey session, its device's own passkey, still registered and not
// synced.
func TestAWindowCountsOnlyForWhatOpenedIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 2, 0, 0, 0, time.UTC)
	store := auth.NewMemoryPasskeyStore()
	approval, own, other, account, synced := p256(t), p256(t), p256(t), p256(t), p256(t)
	add := func(id string, der []byte, device string) {
		if err := store.Insert(ctx, auth.StoredPasskey{Passkey: auth.Passkey{ID: []byte(id), Tenant: "t1", PublicKey: der},
			Subject: "alice", DeviceID: device, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	add("own", own, "dev_pc")
	add("other", other, "dev_other")
	add("account", account, "")
	add("synced", synced, "dev_pc")
	if err := store.SetSignCount(ctx, "t1", []byte("synced"), 1, true); err != nil {
		t.Fatal(err)
	}
	window := func(openedBy []byte) auth.ApprovalWindow {
		return auth.ApprovalWindow{ID: "win_1", Tenant: "t1", Subject: "alice", DeviceID: "dev_pc", SessionID: "sess-pc",
			OpenedAt: now, ExpiresAt: now.Add(auth.WindowLength), CapAt: now.Add(auth.WindowCap), OpenedBy: auth.DeviceKeyFingerprint(openedBy)}
	}
	for _, c := range []struct {
		name     string
		passkeys auth.PasskeyStore
		open     auth.ApprovalWindow
		session  string
		key      []byte
		mode     string
		want     bool
	}{
		{"the session's approval key", store, window(approval), "sess-pc", approval, auth.ApprovalModeKey, true},
		{"another approval key than the one that opened it", store, window(approval), "sess-pc", own, auth.ApprovalModeKey, false},
		{"a passkey's window, for a key session", store, window(own), "sess-pc", approval, auth.ApprovalModeKey, false},
		{"the device's own passkey", store, window(own), "sess-pc", nil, auth.ApprovalModePasskey, true},
		{"the device's own passkey, for a view session", store, window(own), "sess-pc", nil, auth.ApprovalModeView, false},
		{"the device's own passkey, for a session from before modes", store, window(own), "sess-pc", nil, "", false},
		{"another device's passkey", store, window(other), "sess-pc", nil, auth.ApprovalModePasskey, false},
		{"an account passkey", store, window(account), "sess-pc", nil, auth.ApprovalModePasskey, false},
		{"a synced passkey of the device", store, window(synced), "sess-pc", nil, auth.ApprovalModePasskey, false},
		{"a key that's no passkey", store, window(approval), "sess-pc", nil, auth.ApprovalModePasskey, false},
		{"no passkey store", nil, window(own), "sess-pc", nil, auth.ApprovalModePasskey, false},
		{"a passkey store that can't be read", failingPasskeys{}, window(own), "sess-pc", nil, auth.ApprovalModePasskey, false},
		{"another person's window", store, func() auth.ApprovalWindow { w := window(own); w.Subject = "bob"; return w }(), "sess-pc", nil, auth.ApprovalModePasskey, false},
		{"a window with no device", store, func() auth.ApprovalWindow { w := window(own); w.DeviceID = ""; return w }(), "sess-pc", nil, auth.ApprovalModePasskey, false},
		// A window counts only for the session that opened it.
		{"the approval key, from another session on the device", store, window(approval), "sess-pc-2", approval, auth.ApprovalModeKey, false},
		{"the device's own passkey, from another session on the device", store, window(own), "sess-pc-2", nil, auth.ApprovalModePasskey, false},
		{"the approval key, with no session named", store, window(approval), "", approval, auth.ApprovalModeKey, false},
		{"a window kept with no session, for the approval key", store, func() auth.ApprovalWindow { w := window(approval); w.SessionID = ""; return w }(), "", approval, auth.ApprovalModeKey, false},
		{"a window kept with no session, for the device's own passkey", store, func() auth.ApprovalWindow { w := window(own); w.SessionID = ""; return w }(), "sess-pc", nil, auth.ApprovalModePasskey, false},
	} {
		if got := auth.WindowOpenedByApprover(ctx, c.passkeys, c.open, c.session, c.key, c.mode); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// Removed after the open: the window stops counting.
	if err := store.RevokeForDevice(ctx, "dev_pc", now); err != nil {
		t.Fatal(err)
	}
	if auth.WindowOpenedByApprover(ctx, store, window(own), "sess-pc", nil, auth.ApprovalModePasskey) {
		t.Error("a removed passkey's window still counts")
	}
}
