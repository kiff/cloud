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
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// kiff/tafs#494: Setup's read Card is issued on the owner's approval of the
// connection when it was signed inside an approval window too
// (planque.request.v2, the device key, the window's id last), the window
// open for that phone and opened by the session's approval key now; the same
// check as the profile's approval (planqueApproval). A closed window, or a
// handler without the window stores, refuses it.
// Category: authority
// Attack: a v2 approval names a window that ended, so a tap after the window closed issues Setup's Card
// Refused: 403, and no Card is issued
func TestSetupReadsCardOnAnApprovalSignedInsideAWindow(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 5, 13, 0, 0, 0, time.UTC)
	members := membership.NewMemoryStore()
	svc := membership.NewService(members)
	if _, err := svc.EnsureFounder(ctx, "t1", "alice"); err != nil {
		t.Fatal(err)
	}
	devices := auth.NewMemoryDeviceStore()
	sessions := auth.NewMemoryDeveloperSessionStore()
	windows := auth.NewMemoryWindowStore()
	dk, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	dder, _ := x509.MarshalPKIXPublicKey(&dk.PublicKey)
	ak, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ader, _ := x509.MarshalPKIXPublicKey(&ak.PublicKey)
	dev := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: "t1", Subject: "alice", Name: "phone", Platform: "android", PublicKey: dder, CreatedAt: now}
	if err := devices.Insert(ctx, dev); err != nil {
		t.Fatal(err)
	}
	sess := auth.DeveloperSession{ID: "sess-1", Subject: "alice", Tenant: "t1", TokenHash: sha256.Sum256([]byte("s1")), CreatedAt: now,
		ExpiresAt: now.Add(24 * time.Hour), DevicePublicKey: dder, DeviceID: dev.ID, ApprovalPublicKey: ader}
	if err := sessions.Insert(ctx, sess); err != nil {
		t.Fatal(err)
	}
	open := func() auth.ApprovalWindow {
		w := auth.ApprovalWindow{ID: auth.NewWindowID(), Tenant: "t1", Subject: "alice", DeviceID: dev.ID, SessionID: sess.ID,
			OpenedAt: now, ExpiresAt: now.Add(auth.WindowLength), CapAt: now.Add(auth.WindowCap), OpenedBy: auth.DeviceKeyFingerprint(ader)}
		if err := windows.Open(ctx, w); err != nil {
			t.Fatal(err)
		}
		return w
	}
	const approveBody = `{"digest":"dg_1"}`
	sum := sha256.Sum256([]byte(approveBody))
	v2 := func(window string) string {
		return strings.Join([]string{"planque.request.v2", "https://planque.kiff.dev", "t1", "POST",
			"/v1/accounts/t1/grants/grt_1/approve", hex.EncodeToString(sum[:]), now.Add(-time.Minute).Format(time.RFC3339), window}, "\n")
	}
	sign := func(msg string) string {
		digest := sha256.Sum256([]byte(msg))
		sig, _ := ecdsa.SignASN1(rand.Reader, dk, digest[:])
		return base64.StdEncoding.EncodeToString(sig)
	}
	keys := auth.NewMemoryAPIKeyStore()
	if err := keys.Insert(ctx, auth.APIKeyRecord{ID: "key-setup", Tenant: "t1", Subject: "alice", AgentID: "setup",
		Roles: []string{auth.RoleSetupReader}, KeyHash: sha256.Sum256([]byte("s")), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	tools := setupReadTools{owners: map[string]string{"list_accounts": "key_conn"}, readOnly: map[string]bool{"list_accounts": true}}
	connector := auth.Identity{Tenant: "t1", Subject: "alice", Method: auth.MethodAPIKey, APIKey: true,
		Roles: []string{auth.RoleConnector}, Metadata: map[string]string{"api_key_id": "key_conn"}}
	handler := func(store mandate.Store, withWindows bool) *http.ServeMux {
		opts := []MandatesOption{WithMandatesClock(func() time.Time { return now }), WithMandatesMemberships(members),
			WithMandatesSetupReads(devices, svc, tools, keys)}
		if withWindows {
			opts = append(opts, WithMandatesApprovalWindows(sessions, windows))
		}
		mux := http.NewServeMux()
		NewMandatesHandler(store, stubDomainSource{yaml: setupReadsYAML}, opts...).Register(mux)
		return mux
	}
	ask := func(mux *http.ServeMux, msg string) int {
		t.Helper()
		return do(t, mux, connector, http.MethodPost, "/v1/me/setup-reads", map[string]any{"agent": "setup", "grant": "grt_1", "tools": []string{"list_accounts"},
			"approval": map[string]string{"device_id": dev.ID, "message": msg, "body": approveBody, "signature": sign(msg)}}).Code
	}

	// Without the window stores, a v2 approval can't be checked: refused.
	store := mandate.NewMemoryStore()
	win := open()
	if code := ask(handler(store, false), v2(win.ID)); code != http.StatusForbidden {
		t.Fatalf("v2 without the window stores: %d, want 403", code)
	}
	withWindows := handler(store, true)
	// A window that ended: refused, nothing issued.
	if err := windows.Close(ctx, win.ID, now, auth.WindowClosedByApp); err != nil {
		t.Fatal(err)
	}
	if code := ask(withWindows, v2(win.ID)); code != http.StatusForbidden {
		t.Fatalf("v2 in a closed window: %d, want 403", code)
	}
	if ms, _ := store.List(ctx, "t1"); len(ms) != 0 {
		t.Fatalf("a refused approval issued %d Card(s)", len(ms))
	}
	// Inside an open window that this session's approval key opened: issued.
	win = open()
	if code := ask(withWindows, v2(win.ID)); code != http.StatusCreated {
		t.Fatalf("v2 inside the window: %d, want 201", code)
	}
	if ms, _ := store.List(ctx, "t1"); len(ms) != 1 || ms[0].ID != SetupReadsCardID("grt_1") {
		t.Fatalf("the Card issued: %+v", ms)
	}
}
