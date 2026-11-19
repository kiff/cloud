package handlers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// approvalCheckWorld is an owner's phone with an approval window open, KIFF's
// device check (/v1/devices/verify) and Setup's read Card route, sharing one
// clock and KIFF's approval-check records.
type approvalCheckWorld struct {
	t         *testing.T
	ctx       context.Context
	now       time.Time
	devices   *auth.MemoryDeviceStore
	sessions  *auth.MemoryDeveloperSessionStore
	windows   *auth.MemoryWindowStore
	checks    *auth.MemoryApprovalCheckStore
	store     mandate.Store
	verify    *http.ServeMux
	reads     *http.ServeMux
	connector auth.Identity
	mandates  *MandatesHandler
	dev       auth.ApprovalDevice
	other     auth.ApprovalDevice
	dk, ok    *ecdsa.PrivateKey
	window    auth.ApprovalWindow
}

const checkApproveBody = `{"digest":"dg_1"}`

func newApprovalCheckWorld(t *testing.T) *approvalCheckWorld {
	t.Helper()
	w := &approvalCheckWorld{t: t, ctx: context.Background(), now: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return w.now }
	members := membership.NewMemoryStore()
	svc := membership.NewService(members)
	if _, err := svc.EnsureFounder(w.ctx, "t1", "alice"); err != nil {
		t.Fatal(err)
	}
	w.devices = auth.NewMemoryDeviceStore()
	w.sessions = auth.NewMemoryDeveloperSessionStore()
	sessions := w.sessions
	w.windows = auth.NewMemoryWindowStore()
	w.checks = auth.NewMemoryApprovalCheckStore()
	device := func(name string) (auth.ApprovalDevice, *ecdsa.PrivateKey, []byte) {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
		d := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: "t1", Subject: "alice", Name: name, Platform: "android", PublicKey: der, CreatedAt: w.now}
		if err := w.devices.Insert(w.ctx, d); err != nil {
			t.Fatal(err)
		}
		return d, k, der
	}
	var dder []byte
	w.dev, w.dk, dder = device("phone")
	w.other, w.ok, _ = device("tablet")
	ak, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ader, _ := x509.MarshalPKIXPublicKey(&ak.PublicKey)
	sess := auth.DeveloperSession{ID: "sess-1", Subject: "alice", Tenant: "t1", TokenHash: sha256.Sum256([]byte("s1")), CreatedAt: w.now,
		ExpiresAt: w.now.Add(24 * time.Hour), DevicePublicKey: dder, DeviceID: w.dev.ID, ApprovalPublicKey: ader}
	if err := sessions.Insert(w.ctx, sess); err != nil {
		t.Fatal(err)
	}
	w.window = auth.ApprovalWindow{ID: auth.NewWindowID(), Tenant: "t1", Subject: "alice", DeviceID: w.dev.ID, SessionID: sess.ID,
		OpenedAt: w.now, ExpiresAt: w.now.Add(auth.WindowLength), CapAt: w.now.Add(auth.WindowCap), OpenedBy: auth.DeviceKeyFingerprint(ader)}
	if err := w.windows.Open(w.ctx, w.window); err != nil {
		t.Fatal(err)
	}
	keys := auth.NewMemoryAPIKeyStore()
	if err := keys.Insert(w.ctx, auth.APIKeyRecord{ID: "key-setup", Tenant: "t1", Subject: "alice", AgentID: "setup",
		Roles: []string{auth.RoleSetupReader}, KeyHash: sha256.Sum256([]byte("s")), CreatedAt: w.now}); err != nil {
		t.Fatal(err)
	}
	tools := setupReadTools{owners: map[string]string{"list_accounts": "key_conn"}, readOnly: map[string]bool{"list_accounts": true}}
	w.connector = auth.Identity{Tenant: "t1", Subject: "alice", Method: auth.MethodAPIKey, APIKey: true,
		Roles: []string{auth.RoleConnector}, Metadata: map[string]string{"api_key_id": "key_conn"}}
	vh := NewDeviceVerifyHandler(w.devices, svc).WithSessions(sessions).WithWindows(w.windows).WithApprovalChecks(w.checks)
	vh.now = clock
	w.verify = http.NewServeMux()
	vh.Register(w.verify)
	w.store = mandate.NewMemoryStore()
	w.reads = http.NewServeMux()
	w.mandates = NewMandatesHandler(w.store, stubDomainSource{yaml: setupReadsYAML}, WithMandatesClock(clock), WithMandatesMemberships(members),
		WithMandatesSetupReads(w.devices, svc, tools, keys), WithMandatesApprovalWindows(sessions, w.windows),
		WithMandatesApprovalChecks(w.checks))
	w.mandates.Register(w.reads)
	return w
}

// approve is the connection's approve of grant as planque.request.v2, signed
// by key inside the window.
func (w *approvalCheckWorld) approve(grant string) string {
	sum := sha256.Sum256([]byte(checkApproveBody))
	return strings.Join([]string{"planque.request.v2", "https://planque.kiff.dev", "t1", "POST",
		"/v1/accounts/t1/grants/" + grant + "/approve", hex.EncodeToString(sum[:]), w.now.Add(-time.Minute).Format(time.RFC3339), w.window.ID}, "\n")
}

// verified is planque's check of the approve at the tap: KIFF finds the
// window open, and records it.
func (w *approvalCheckWorld) verified(device auth.ApprovalDevice, msg, sig string) {
	w.t.Helper()
	rr := do(w.t, w.verify, w.connector, http.MethodPost, "/v1/devices/verify", map[string]string{"device_id": device.ID,
		"message": base64.StdEncoding.EncodeToString([]byte(msg)), "signature": sig, "capability": string(membership.CapToolsConnect), "window": w.window.ID})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"valid":true`) {
		w.t.Fatalf("verify: %d %s", rr.Code, rr.Body.String())
	}
}

// later is the window closed, as the phone does on leaving the app for the
// vendor's sign-in, and the clock moved on by d.
func (w *approvalCheckWorld) later(d time.Duration) {
	w.t.Helper()
	if err := w.windows.Close(w.ctx, w.window.ID, w.now, auth.WindowClosedByApp); err != nil {
		w.t.Fatal(err)
	}
	w.now = w.now.Add(d)
}

// reads asks for Setup's read Card of grant on the approval, as planque does
// when the connection is done.
func (w *approvalCheckWorld) readsCard(grant string, device auth.ApprovalDevice, msg, sig string) int {
	w.t.Helper()
	return do(w.t, w.reads, w.connector, http.MethodPost, "/v1/me/setup-reads", map[string]any{"agent": "setup", "grant": grant, "tools": []string{"list_accounts"},
		"approval": map[string]string{"device_id": device.ID, "message": msg, "body": checkApproveBody, "signature": sig}}).Code
}

func (w *approvalCheckWorld) cards() int {
	ms, _ := w.store.List(w.ctx, "t1")
	return len(ms)
}

// kiff/tafs window_required: the phone signs the connection's approve inside
// its approval window, KIFF verifies it there at the tap, and planque asks
// for Setup's read Card only once the vendor's sign-in is done, after the
// phone closed its window on leaving the app. KIFF's own record of that check
// lets the Card be issued once, within 30 minutes; anything else is refused
// as before.
// Category: authority
// Attack: a window-signed approval replayed after its window closed, past 30 minutes, for another grant or device, or twice
// Refused: 403, and no Card is issued
func TestSetupReadsCardOnAnApprovalKIFFVerifiedInsideAWindow(t *testing.T) {
	t.Run("verified at the approve, then the window closed: the Card is issued once", func(t *testing.T) {
		w := newApprovalCheckWorld(t)
		msg := w.approve("grt_1")
		sig := sign(w.dk, msg)
		w.verified(w.dev, msg, sig)
		w.later(10 * time.Minute)
		if code := w.readsCard("grt_1", w.dev, msg, sig); code != http.StatusCreated {
			t.Fatalf("within 30 minutes of KIFF's check: %d, want 201", code)
		}
		if w.cards() != 1 {
			t.Fatalf("Cards issued: %d, want 1", w.cards())
		}
		// A replay of the same approval: refused, nothing more.
		if code := w.readsCard("grt_1", w.dev, msg, sig); code != http.StatusForbidden {
			t.Fatalf("the approval again: %d, want 403", code)
		}
	})

	t.Run("never verified inside a window: refused as before", func(t *testing.T) {
		w := newApprovalCheckWorld(t)
		msg := w.approve("grt_1")
		sig := sign(w.dk, msg)
		w.later(time.Minute)
		if code := w.readsCard("grt_1", w.dev, msg, sig); code != http.StatusForbidden || w.cards() != 0 {
			t.Fatalf("no record: %d, %d Card(s); want 403, none", code, w.cards())
		}
	})

	t.Run("minute 31 after KIFF's check: refused", func(t *testing.T) {
		w := newApprovalCheckWorld(t)
		msg := w.approve("grt_1")
		sig := sign(w.dk, msg)
		w.verified(w.dev, msg, sig)
		w.later(31 * time.Minute)
		if code := w.readsCard("grt_1", w.dev, msg, sig); code != http.StatusForbidden || w.cards() != 0 {
			t.Fatalf("past 30 minutes: %d, %d Card(s); want 403, none", code, w.cards())
		}
	})

	t.Run("another grant on the same approval: refused", func(t *testing.T) {
		w := newApprovalCheckWorld(t)
		msg := w.approve("grt_1")
		sig := sign(w.dk, msg)
		w.verified(w.dev, msg, sig)
		w.later(time.Minute)
		if code := w.readsCard("grt_2", w.dev, msg, sig); code != http.StatusForbidden || w.cards() != 0 {
			t.Fatalf("another grant: %d, %d Card(s); want 403, none", code, w.cards())
		}
	})

	t.Run("another device naming the same approval: refused", func(t *testing.T) {
		w := newApprovalCheckWorld(t)
		msg := w.approve("grt_1")
		sig := sign(w.dk, msg)
		w.verified(w.dev, msg, sig)
		w.later(time.Minute)
		if code := w.readsCard("grt_1", w.other, msg, sig); code != http.StatusForbidden || w.cards() != 0 {
			t.Fatalf("another device: %d, %d Card(s); want 403, none", code, w.cards())
		}
		// Its own signature over the same message was never verified in a window: refused too.
		if code := w.readsCard("grt_1", w.other, msg, sign(w.ok, msg)); code != http.StatusForbidden || w.cards() != 0 {
			t.Fatalf("another device's own signature: %d, %d Card(s); want 403, none", code, w.cards())
		}
	})

	t.Run("the device revoked inside the 30 minutes: refused", func(t *testing.T) {
		w := newApprovalCheckWorld(t)
		msg := w.approve("grt_1")
		sig := sign(w.dk, msg)
		w.verified(w.dev, msg, sig)
		w.later(time.Minute)
		if err := w.devices.Revoke(w.ctx, w.dev.ID, w.now); err != nil {
			t.Fatal(err)
		}
		if code := w.readsCard("grt_1", w.dev, msg, sig); code != http.StatusForbidden || w.cards() != 0 {
			t.Fatalf("a revoked device: %d, %d Card(s); want 403, none", code, w.cards())
		}
	})

	t.Run("a re-link inside the 30 minutes: refused", func(t *testing.T) {
		w := newApprovalCheckWorld(t)
		msg := w.approve("grt_1")
		sig := sign(w.dk, msg)
		w.verified(w.dev, msg, sig)
		w.later(time.Minute)
		// A re-link gives the session a new approval key: the window it opened is over, and its record with it.
		nk, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		nder, _ := x509.MarshalPKIXPublicKey(&nk.PublicKey)
		if _, err := w.sessions.ReplaceApprovalKey(w.ctx, w.window.SessionID, nder, w.now); err != nil {
			t.Fatal(err)
		}
		if code := w.readsCard("grt_1", w.dev, msg, sig); code != http.StatusForbidden || w.cards() != 0 {
			t.Fatalf("after a re-link: %d, %d Card(s); want 403, none", code, w.cards())
		}
	})

	t.Run("the record valid, the device revoked: checkedAtApprove itself refuses", func(t *testing.T) {
		w := newApprovalCheckWorld(t)
		msg := w.approve("grt_1")
		sig := sign(w.dk, msg)
		w.verified(w.dev, msg, sig)
		w.later(time.Minute)
		if _, _, id, reason, err := w.mandates.setupReads.checkedAtApprove(w.ctx, "t1", w.dev.ID, "grt_1", msg, sig, w.now); err != nil || reason != "" || id == "" {
			t.Fatalf("a valid record: id %q, reason %q, err %v", id, reason, err)
		}
		if err := w.devices.Revoke(w.ctx, w.dev.ID, w.now); err != nil {
			t.Fatal(err)
		}
		if _, _, id, reason, err := w.mandates.setupReads.checkedAtApprove(w.ctx, "t1", w.dev.ID, "grt_1", msg, sig, w.now); err != nil || reason == "" || id != "" {
			t.Fatalf("a revoked device: id %q, reason %q, err %v; want refused", id, reason, err)
		}
	})

	t.Run("verified again later: the record's check doesn't move", func(t *testing.T) {
		w := newApprovalCheckWorld(t)
		msg := w.approve("grt_1")
		sig := sign(w.dk, msg)
		w.verified(w.dev, msg, sig)
		first, err := w.checks.Get(w.ctx, auth.ApprovalCheckID([]byte(msg), sig))
		if err != nil {
			t.Fatal(err)
		}
		w.now = w.now.Add(4 * time.Minute)
		w.verified(w.dev, msg, sig)
		again, _ := w.checks.Get(w.ctx, first.ID)
		if !again.CheckedAt.Equal(first.CheckedAt) {
			t.Fatalf("the check moved from %v to %v", first.CheckedAt, again.CheckedAt)
		}
		w.later(27 * time.Minute) // 31 minutes after the first check
		if code := w.readsCard("grt_1", w.dev, msg, sig); code != http.StatusForbidden {
			t.Fatalf("31 minutes after the first check: %d, want 403", code)
		}
	})

	t.Run("two parallel uses: one Card", func(t *testing.T) {
		w := newApprovalCheckWorld(t)
		msg := w.approve("grt_1")
		sig := sign(w.dk, msg)
		w.verified(w.dev, msg, sig)
		w.later(time.Minute)
		codes := make([]int, 2)
		var wg sync.WaitGroup
		for i := range codes {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				codes[i] = w.readsCard("grt_1", w.dev, msg, sig)
			}(i)
		}
		wg.Wait()
		created := 0
		for _, c := range codes {
			if c == http.StatusCreated {
				created++
			} else if c != http.StatusForbidden {
				t.Fatalf("a parallel use answered %d", c)
			}
		}
		if created != 1 || w.cards() != 1 {
			t.Fatalf("parallel uses: %v, %d Card(s); want one 201 and one Card", codes, w.cards())
		}
	})
}

// Only KIFF's own check writes a record: a v1 approval, a closed window, another
// capability, or a request that isn't a grant's approve records nothing.
// Category: authority
// Attack: planque, or a caller with its key, has a record written for an approval KIFF didn't verify inside an open window
// Refused: no record
func TestDeviceVerifyRecordsOnlyAWindowedApproveItVerified(t *testing.T) {
	w := newApprovalCheckWorld(t)
	none := func(name, msg, sig, capability, window string) {
		t.Helper()
		do(t, w.verify, w.connector, http.MethodPost, "/v1/devices/verify", map[string]string{"device_id": w.dev.ID,
			"message": base64.StdEncoding.EncodeToString([]byte(msg)), "signature": sig, "capability": capability, "window": window})
		if _, err := w.checks.Get(w.ctx, auth.ApprovalCheckID([]byte(msg), sig)); err == nil {
			t.Fatalf("%s: recorded", name)
		}
	}
	approve := w.approve("grt_1")
	// Another capability.
	none("holds.answer", approve, sign(w.dk, approve), string(membership.CapHoldsAnswer), w.window.ID)
	// Another request: not a grant's approve or key.
	other := strings.Replace(approve, "/grants/grt_1/approve", "/grants/grt_1/end", 1)
	none("another request", other, sign(w.dk, other), string(membership.CapToolsConnect), w.window.ID)
	// A signature that doesn't check.
	none("a bad signature", approve, sign(w.ok, approve), string(membership.CapToolsConnect), w.window.ID)
	// The open window's id on another line of the message than its window line.
	sum := sha256.Sum256([]byte(checkApproveBody))
	elsewhere := strings.Join([]string{"planque.request.v2", w.window.ID, "t1", "POST", "/v1/accounts/t1/grants/grt_1/approve",
		hex.EncodeToString(sum[:]), w.now.Format(time.RFC3339), "win_other"}, "\n")
	none("the window on another line", elsewhere, sign(w.dk, elsewhere), string(membership.CapToolsConnect), w.window.ID)
	// The window closed before the check.
	w.later(0)
	none("a closed window", approve, sign(w.dk, approve), string(membership.CapToolsConnect), w.window.ID)
}
