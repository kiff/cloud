package handlers_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
)

// restartRigs are two API processes in turn, as a deploy runs them: the
// same sessions, the same waiting sign-ins, the same clock.
func restartRigs(t *testing.T) (before, after *deviceRig, advance func(time.Duration)) {
	t.Helper()
	waiting := auth.NewMemoryWaitingSignIns()
	sessions := auth.NewMemoryDeveloperSessionStore()
	clock := &deviceRig{now: time.Date(2026, 11, 10, 16, 0, 0, 0, time.UTC)}
	build := func() *deviceRig {
		rig := &deviceRig{sessions: sessions}
		h := handlers.NewDeviceAuthHandler(sessions,
			handlers.WithDeviceClock(func() time.Time {
				clock.mu.Lock()
				defer clock.mu.Unlock()
				return clock.now
			}),
			handlers.WithDeviceVerificationURI("https://app.example.com/auth/device"),
			handlers.WithDeviceWaitingSignIns(waiting),
		)
		rig.pub = http.NewServeMux()
		h.RegisterPublic(rig.pub)
		inner := http.NewServeMux()
		h.RegisterAuthenticated(inner)
		rig.inner = inner
		rig.authed = withFakeIdentity(inner, auth.Identity{Tenant: "tenant-1", Subject: "user-abc", Method: auth.MethodSession})
		return rig
	}
	// One clock for both.
	return build(), build(), clock.advance
}

// A deploy while the person approves loses nothing: the code a device
// shows, started on the process before, is approved on the one after, and
// the device's next poll there gets its session.
func TestDeviceAuth_AWaitingCodeOutlivesARestart(t *testing.T) {
	t.Parallel()
	before, after, advance := restartRigs(t)
	deviceCode, userCode := before.start(t)
	if rec, body := before.poll(t, deviceCode); rec.Code != http.StatusBadRequest || body["error"] != "authorization_pending" {
		t.Fatalf("before the restart: %d %v", rec.Code, body)
	}

	// The new process knows the code it never started.
	advance(6 * time.Second)
	if rec, body := after.poll(t, deviceCode); rec.Code != http.StatusBadRequest || body["error"] != "authorization_pending" {
		t.Fatalf("after the restart, before approval: %d %v", rec.Code, body)
	}
	if ar := after.approve(t, userCode); ar.Code != http.StatusOK {
		t.Fatalf("approve after the restart: %d %s", ar.Code, ar.Body.String())
	}
	advance(6 * time.Second)
	rec, body := after.poll(t, deviceCode)
	if rec.Code != http.StatusOK || body["access_token"] == "" || body["tenant"] != "tenant-1" {
		t.Fatalf("the device after approval: %d %v", rec.Code, body)
	}
}

// A kept code expires as today: past its 10 minutes, neither process
// takes it, and it can't be approved.
func TestDeviceAuth_AKeptCodeExpiresAsBefore(t *testing.T) {
	t.Parallel()
	before, after, advance := restartRigs(t)
	deviceCode, userCode := before.start(t)
	advance(10*time.Minute + time.Second)
	if rec, body := after.poll(t, deviceCode); rec.Code != http.StatusBadRequest || body["error"] != "expired_token" {
		t.Fatalf("an expired code polled after a restart: %d %v", rec.Code, body)
	}
	if ar := after.approve(t, userCode); ar.Code != http.StatusNotFound {
		t.Fatalf("an expired code approved after a restart: %d", ar.Code)
	}
}

// A used code stays used: once approved and taken, a restart doesn't bring
// it back to be approved or polled again; and an approved code is no
// longer kept, so the session it minted is never stored with it.
func TestDeviceAuth_AUsedCodeStaysUsedAcrossARestart(t *testing.T) {
	t.Parallel()
	before, after, advance := restartRigs(t)
	deviceCode, userCode := before.start(t)
	if ar := before.approve(t, userCode); ar.Code != http.StatusOK {
		t.Fatalf("approve: %d", ar.Code)
	}
	// Approved: the new process never sees it waiting.
	if ar := after.approve(t, userCode); ar.Code != http.StatusNotFound {
		t.Fatalf("an approved code approved again on another process: %d", ar.Code)
	}
	advance(6 * time.Second)
	if rec, _ := before.poll(t, deviceCode); rec.Code != http.StatusOK {
		t.Fatalf("the device takes its session: %d", rec.Code)
	}
	advance(6 * time.Second)
	for name, rig := range map[string]*deviceRig{"the same process": before, "after a restart": after} {
		if rec, body := rig.poll(t, deviceCode); rec.Code != http.StatusBadRequest || body["error"] != "expired_token" {
			t.Errorf("a used code polled again, %s: %d %v", name, rec.Code, body)
		}
		if ar := rig.approve(t, userCode); ar.Code != http.StatusNotFound {
			t.Errorf("a used code approved again, %s: %d", name, ar.Code)
		}
	}
}

// Two processes running at once (a deploy's overlap) that both hold a
// waiting code: approved on both, one approval wins and one session is
// minted; the other is told the code is unknown.
func TestDeviceAuth_TwoProcessesApprovingOneCodeMintOneSession(t *testing.T) {
	t.Parallel()
	before, after, advance := restartRigs(t)
	deviceCode, userCode := before.start(t)
	// Both know it: the second read it back on a poll.
	advance(6 * time.Second)
	if rec, _ := after.poll(t, deviceCode); rec.Code != http.StatusBadRequest {
		t.Fatalf("the second process doesn't hold it: %d", rec.Code)
	}
	first, second := before.approve(t, userCode), after.approve(t, userCode)
	if first.Code != http.StatusOK || second.Code != http.StatusNotFound {
		t.Fatalf("approvals: %d and %d, want 200 and 404", first.Code, second.Code)
	}
	live, _ := before.sessions.ListActiveForSubject(context.Background(), "user-abc", time.Now().Add(time.Hour))
	if n := len(live); n != 1 {
		t.Fatalf("sessions minted: %d, want 1", n)
	}
}

// heldInsert is a session store whose first Insert waits for release: an
// approval held midway, as a slow write leaves it.
type heldInsert struct {
	*auth.MemoryDeveloperSessionStore
	entered, release chan struct{}
	once             sync.Once
}

func (s *heldInsert) Insert(ctx context.Context, sess auth.DeveloperSession) error {
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
	return s.MemoryDeveloperSessionStore.Insert(ctx, sess)
}

// A double click on one process: the second approval, while the first is
// minting, is told the code is being approved and claims nothing; the
// first goes through, and the device gets its session.
func TestDeviceAuth_ADoubleClickApprovesOnce(t *testing.T) {
	t.Parallel()
	store := &heldInsert{MemoryDeveloperSessionStore: auth.NewMemoryDeveloperSessionStore(), entered: make(chan struct{}), release: make(chan struct{})}
	clock := &deviceRig{now: time.Date(2026, 11, 10, 16, 0, 0, 0, time.UTC)}
	h := handlers.NewDeviceAuthHandler(store,
		handlers.WithDeviceClock(func() time.Time {
			clock.mu.Lock()
			defer clock.mu.Unlock()
			return clock.now
		}),
		handlers.WithDeviceWaitingSignIns(auth.NewMemoryWaitingSignIns()),
	)
	rig := &deviceRig{sessions: store.MemoryDeveloperSessionStore, pub: http.NewServeMux()}
	h.RegisterPublic(rig.pub)
	inner := http.NewServeMux()
	h.RegisterAuthenticated(inner)
	rig.authed = withFakeIdentity(inner, auth.Identity{Tenant: "tenant-1", Subject: "user-abc", Method: auth.MethodSession})
	deviceCode, userCode := rig.start(t)

	first := make(chan int, 1)
	go func() { first <- rig.approve(t, userCode).Code }()
	<-store.entered
	if second := rig.approve(t, userCode); second.Code != http.StatusConflict {
		t.Fatalf("the second click: %d %s, want 409", second.Code, second.Body.String())
	}
	close(store.release)
	if code := <-first; code != http.StatusOK {
		t.Fatalf("the first click: %d, want 200", code)
	}
	clock.advance(6 * time.Second)
	if rec, body := rig.poll(t, deviceCode); rec.Code != http.StatusOK || body["access_token"] == "" {
		t.Fatalf("the device after a double click: %d %v", rec.Code, body)
	}
	live, _ := store.ListActiveForSubject(context.Background(), "user-abc", time.Now().Add(time.Hour))
	if len(live) != 1 {
		t.Fatalf("live sessions: %d, want 1", len(live))
	}
}
