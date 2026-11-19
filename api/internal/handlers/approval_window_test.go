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
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/halts"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/receipt"
)

// signRequestWindow is a KIFF-Device-Signature made inside an approval
// window: a v2 signature by key, naming window.
func signRequestWindow(t *testing.T, key *ecdsa.PrivateKey, tenant, method, path, body string, at time.Time, window string) string {
	t.Helper()
	ts := at.UTC().Format(time.RFC3339)
	digest := sha256.Sum256(auth.RequestMessageWindow(tenant, method, path, []byte(body), ts, window))
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return "t=" + ts + ",v2=" + base64.StdEncoding.EncodeToString(sig) + ",w=" + window
}

// windowRig is a phone's session with both keys, the route policy with a
// window store and a clock, and the window routes beside stubs for a
// routine and a critical route.
type windowRig struct {
	t        *testing.T
	windows  *auth.MemoryWindowStore
	now      time.Time
	policed  http.Handler
	phone    auth.Identity
	device   *ecdsa.PrivateKey
	approval *ecdsa.PrivateKey
	reached  map[string]int
	halts    *halts.MemoryStore
}

func newWindowRig(t *testing.T) *windowRig {
	t.Helper()
	r := &windowRig{t: t, windows: auth.NewMemoryWindowStore(), now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC), reached: map[string]int{}, halts: halts.NewMemoryStore()}
	inner := http.NewServeMux()
	wh := handlers.NewApprovalWindowHandler(r.windows, nil)
	handlers.SetClock(wh, func() time.Time { return r.now })
	wh.Register(inner)
	hh := handlers.NewHaltsHandler(r.halts, nil, receipt.NewMemoryStore(), nil).WithWindows(r.windows)
	handlers.SetClock(hh, func() time.Time { return r.now })
	hh.Register(inner)
	for _, pattern := range []string{"POST /v1/me/mandates", "POST /v1/me/agent-drafts/{id}/confirm", "POST /v1/me/did-this-happen/{proposal}",
		"POST /v1/me/mandates/{id}/extra-room", "POST /v1/agents/{id}/grants", "POST /v1/keys", "POST /v1/me/device/approval-key"} {
		p := pattern
		inner.HandleFunc(p, func(w http.ResponseWriter, req *http.Request) {
			r.reached[p]++
			with, _ := auth.SignedWithFrom(req.Context())
			writeTestJSON(w, http.StatusCreated, map[string]any{"key": with.Key, "window": with.Window})
		})
	}
	r.policed = auth.DefaultRoutePolicy().WithWindows(r.windows).WithClock(func() time.Time { return r.now }).Wrap(inner)
	var deviceDER, approvalDER []byte
	r.device, deviceDER = p256(t)
	r.approval, approvalDER = p256(t)
	r.phone = auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, MemberRole: membership.RoleAdmin, Roles: []string{"tenant_owner"},
		DeviceKey: deviceDER, DeviceSessionID: "sess-phone", DeviceID: "dev_phone", DeviceName: "tafs on Pixel", ApprovalKey: approvalDER}
	return r
}

func writeTestJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// call sends a request as id, signed with header when given.
func (r *windowRig) call(id auth.Identity, method, path, body, header string) (int, map[string]any) {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if header != "" {
		req.Header.Set(auth.DeviceSignatureHeader, header)
	}
	rec := httptest.NewRecorder()
	withFakeIdentity(r.policed, id).ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// open opens a window with the approval key and returns its id.
func (r *windowRig) open() string {
	r.t.Helper()
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	body, _ := json.Marshal(auth.OpenWindow{Message: auth.WindowMessage, Session: "sess-phone", Device: "dev_phone",
		Expires: r.now.Add(5 * time.Minute).Format(time.RFC3339), Nonce: hex.EncodeToString(nonce)})
	code, out := r.call(r.phone, http.MethodPost, "/v1/me/window", string(body), signRequest(r.t, r.approval, "tenant-1", http.MethodPost, "/v1/me/window", string(body), r.now))
	if code != http.StatusCreated {
		r.t.Fatalf("opening a window: %d %v", code, out)
	}
	w, _ := out["window"].(map[string]any)
	id, _ := w["id"].(string)
	if id == "" || w["open"] != true || w["words"] != "No fingerprint or face for the next 5 minutes" {
		r.t.Fatalf("the window as opened: %v", w)
	}
	return id
}

// routine makes a routine call inside window, with the device key.
func (r *windowRig) routine(path, window string) (int, map[string]any) {
	body := `{"n":` + r.now.Format("150405") + `}` // each call its own body, so none is a replay of another
	return r.call(r.phone, http.MethodPost, path, body, signRequestWindow(r.t, r.device, "tenant-1", http.MethodPost, path, body, r.now, window))
}

// kiff/tafs#494: one fingerprint opens a window, held by KIFF; inside it a
// routine action is a tap, signed with the device key and the window's id;
// a critical action still needs the approval key; the window restarts with
// each approval, ends at its cap, and ends on Close, a halt, or expiry.
func TestAFingerprintOpensAWindowThenTapsSignRoutineActions(t *testing.T) {
	t.Parallel()
	r := newWindowRig(t)
	// Opening with the device key is no fingerprint: refused.
	body, _ := json.Marshal(auth.OpenWindow{Message: auth.WindowMessage, Session: "sess-phone", Device: "dev_phone",
		Expires: r.now.Add(5 * time.Minute).Format(time.RFC3339), Nonce: strings.Repeat("x", 32)})
	if code, out := r.call(r.phone, http.MethodPost, "/v1/me/window", string(body), signRequest(t, r.device, "tenant-1", http.MethodPost, "/v1/me/window", string(body), r.now)); code != http.StatusForbidden || out["error"] != "approval_key_required" {
		t.Fatalf("opened with the device key: %d %v", code, out)
	}
	// A session without an approval key has no window to open.
	without := r.phone
	without.ApprovalKey = nil
	if code, out := r.call(without, http.MethodPost, "/v1/me/window", string(body), signRequest(t, r.device, "tenant-1", http.MethodPost, "/v1/me/window", string(body), r.now)); code != http.StatusForbidden || out["error"] != "approval_key_required" {
		t.Fatalf("a session without an approval key opened a window: %d %v", code, out)
	}
	// Before any window, a routine route needs the approval key, as today.
	if code, out := r.call(r.phone, http.MethodPost, "/v1/me/mandates", `{}`, signRequest(t, r.device, "tenant-1", http.MethodPost, "/v1/me/mandates", `{}`, r.now)); code != http.StatusForbidden || out["error"] != "approval_key_required" {
		t.Fatalf("a routine route with the device key and no window: %d %v", code, out)
	}
	if code, _ := r.call(r.phone, http.MethodGet, "/v1/me/window", "", ""); code != http.StatusOK {
		t.Fatalf("reading no window: %d", code)
	}

	win := r.open()
	if code, out := r.call(r.phone, http.MethodGet, "/v1/me/window", "", ""); code != http.StatusOK || out["window"].(map[string]any)["id"] != win {
		t.Fatalf("reading the window: %d %v", code, out)
	}
	// A tap: the device key, inside the window. The handler sees which key.
	r.now = r.now.Add(time.Minute)
	if code, out := r.routine("/v1/me/mandates", win); code != http.StatusCreated || out["key"] != auth.KeyDevice || out["window"] != win {
		t.Fatalf("a routine action inside the window: %d %v", code, out)
	}
	// The window restarted: 5 minutes from this approval.
	if w, _ := r.windows.Get(context.Background(), win); !w.ExpiresAt.Equal(r.now.Add(5 * time.Minute)) {
		t.Errorf("not restarted: expires %v", w.ExpiresAt)
	}
	// Replayed: refused.
	code, out := r.call(r.phone, http.MethodPost, "/v1/me/mandates", `{"n":`+r.now.Format("150405")+`}`,
		signRequestWindow(t, r.device, "tenant-1", http.MethodPost, "/v1/me/mandates", `{"n":`+r.now.Format("150405")+`}`, r.now, win))
	if code != http.StatusConflict || out["error"] != "signature_replayed" {
		t.Errorf("a replay inside the window: %d %v", code, out)
	}
	// A critical action inside the window still needs the approval key. A draft's confirm is a
	// window route: its handler refuses one that widens a Card in force (card_widening.go).
	for _, path := range []string{"/v1/me/mandates/m1/extra-room", "/v1/agents/rae/grants"} {
		if code, out := r.routine(path, win); code != http.StatusForbidden || out["error"] != "critical_key_required" {
			t.Errorf("%s with a window: %d %v", path, code, out)
		}
		if code, out := r.call(r.phone, http.MethodPost, path, `{}`, signRequest(t, r.approval, "tenant-1", http.MethodPost, path, `{}`, r.now)); code != http.StatusCreated || out["key"] != auth.KeyCritical {
			t.Errorf("%s with the approval key: %d %v", path, code, out)
		}
	}
	// The approval key still signs a routine route, with no window needed.
	if code, out := r.call(r.phone, http.MethodPost, "/v1/me/agent-drafts/d1/confirm", `{"k":1}`, signRequest(t, r.approval, "tenant-1", http.MethodPost, "/v1/me/agent-drafts/d1/confirm", `{"k":1}`, r.now)); code != http.StatusCreated || out["key"] != auth.KeyCritical || out["window"] != "" {
		t.Errorf("a routine route with the approval key: %d %v", code, out)
	}
	// A window that isn't this phone's open one: refused, and the signature
	// over another window's id doesn't carry.
	if code, out := r.routine("/v1/me/mandates", auth.NewWindowID()); code != http.StatusForbidden || out["error"] != "window_required" {
		t.Errorf("another window's id: %d %v", code, out)
	}
	// The device key's safe routes take a window header too, and ignore it.
	if code, out := r.routine("/v1/keys", win); code != http.StatusCreated || out["key"] != auth.KeyDevice || out["window"] != "" {
		t.Errorf("a device-key route with a window: %d %v", code, out)
	}
	// Approvals every 4 minutes keep it open until the cap, 15 minutes from the opening.
	for i := 0; i < 3; i++ {
		r.now = r.now.Add(4 * time.Minute)
		if code, out := r.routine("/v1/me/mandates", win); code != http.StatusCreated {
			t.Fatalf("approval %d at %v: %d %v", i, r.now, code, out)
		}
	}
	if w, _ := r.windows.Get(context.Background(), win); !w.ExpiresAt.Equal(w.CapAt) {
		t.Errorf("near the cap, expires %v, cap %v", w.ExpiresAt, w.CapAt)
	}
	r.now = r.now.Add(2*time.Minute + time.Second) // 15:01 after the opening
	if code, out := r.routine("/v1/me/mandates", win); code != http.StatusForbidden || out["error"] != "window_required" {
		t.Errorf("past the cap: %d %v", code, out)
	}
	if code, out := r.call(r.phone, http.MethodGet, "/v1/me/window", "", ""); code != http.StatusOK || out["window"] != nil || out["words"] != "Your fingerprint or face at the next approval" {
		t.Errorf("reading past the cap: %d %v", code, out)
	}

	// A fresh fingerprint opens a new window; Close ends it.
	win2 := r.open()
	if code, out := r.call(r.phone, http.MethodPost, "/v1/me/window/"+win2+"/close", `{}`, signRequest(t, r.device, "tenant-1", http.MethodPost, "/v1/me/window/"+win2+"/close", `{}`, r.now)); code != http.StatusOK || out["window"].(map[string]any)["open"] != false || out["window"].(map[string]any)["closed_by"] != auth.WindowClosedByApp {
		t.Fatalf("closing: %d %v", code, out)
	}
	if code, out := r.routine("/v1/me/mandates", win2); code != http.StatusForbidden || out["error"] != "window_required" {
		t.Errorf("after Close: %d %v", code, out)
	}
	// Another phone's window can't be closed from here: it reads as none.
	other := r.phone
	other.DeviceID, other.DeviceSessionID = "dev_other", "sess-other"
	win3 := r.open()
	if code, _ := r.call(other, http.MethodPost, "/v1/me/window/"+win3+"/close", `{}`, signRequest(t, r.device, "tenant-1", http.MethodPost, "/v1/me/window/"+win3+"/close", `{}`, r.now)); code != http.StatusNotFound {
		t.Errorf("another phone closing it: %d", code)
	}
	// A halt ends every window of the account: a tap after it is refused.
	if code, out := r.call(r.phone, http.MethodPost, "/v1/halt", `{}`, ""); code != http.StatusOK {
		t.Fatalf("halt: %d %v", code, out)
	}
	if w, _ := r.windows.Get(context.Background(), win3); w.ClosedAt == nil || w.ClosedBy != auth.WindowClosedByHalt {
		t.Errorf("after the halt: %+v", w)
	}
	if code, out := r.routine("/v1/me/mandates", win3); code != http.StatusForbidden || out["error"] != "window_required" {
		t.Errorf("after the halt: %d %v", code, out)
	}
	// Expiry: a window with no approval for 5 minutes is gone.
	win4 := r.open()
	r.now = r.now.Add(5*time.Minute + time.Second)
	if code, out := r.routine("/v1/me/mandates", win4); code != http.StatusForbidden || out["error"] != "window_required" {
		t.Errorf("after expiry: %d %v", code, out)
	}
	// A session without an approval key: the device key as today, no window.
	if code, out := r.call(without, http.MethodPost, "/v1/me/mandates", `{"n":1}`, signRequest(t, r.device, "tenant-1", http.MethodPost, "/v1/me/mandates", `{"n":1}`, r.now)); code != http.StatusCreated || out["key"] != auth.KeyDevice {
		t.Errorf("a session without an approval key: %d %v", code, out)
	}
	// Does a window outlive the approval key that opened it? No: once the session's
	// approval key is another (a new enrolment and re-link), a tap in the old window is refused.
	win5 := r.open()
	relinked := r.phone
	_, relinked.ApprovalKey = p256(t)
	if code, out := r.call(relinked, http.MethodPost, "/v1/me/mandates", `{"n":5}`, signRequestWindow(t, r.device, "tenant-1", http.MethodPost, "/v1/me/mandates", `{"n":5}`, r.now, win5)); code != http.StatusForbidden || out["error"] != "window_required" {
		t.Errorf("a tap after the approval key changed: %d %v", code, out)
	}
}

// Setting a session's approval key, or re-linking it, ends the windows that
// session opened (kiff/tafs#494): the key that opened them is no longer its.
func TestANewApprovalKeyEndsTheSessionsWindows(t *testing.T) {
	t.Parallel()
	r := newWindowRig(t)
	sessions := auth.NewMemoryDeveloperSessionStore()
	if err := sessions.Insert(context.Background(), auth.DeveloperSession{ID: "sess-phone", Subject: "user-abc", Tenant: "tenant-1", TokenHash: sha256.Sum256([]byte("tok")),
		CreatedAt: r.now, ExpiresAt: r.now.Add(time.Hour), DevicePublicKey: r.phone.DeviceKey, DeviceID: "dev_phone"}); err != nil {
		t.Fatal(err)
	}
	inner := http.NewServeMux()
	handlers.NewDeviceAuthHandler(sessions, handlers.WithDeviceWindows(r.windows), handlers.WithDeviceClock(func() time.Time { return r.now })).RegisterAuthenticated(inner)
	r.policed = auth.DefaultRoutePolicy().WithWindows(r.windows).WithClock(func() time.Time { return r.now }).Wrap(inner)
	// A window the session opened earlier, under a key it no longer has.
	win := auth.ApprovalWindow{ID: auth.NewWindowID(), Tenant: "tenant-1", Subject: "user-abc", DeviceID: "dev_phone", SessionID: "sess-phone",
		OpenedAt: r.now, ExpiresAt: r.now.Add(auth.WindowLength), CapAt: r.now.Add(auth.WindowCap), OpenedBy: "old key"}
	if err := r.windows.Open(context.Background(), win); err != nil {
		t.Fatal(err)
	}
	without := r.phone
	without.ApprovalKey = nil
	_, newDER := p256(t)
	body, _ := json.Marshal(map[string]string{"public_key": base64.StdEncoding.EncodeToString(newDER)})
	if code, out := r.call(without, http.MethodPost, "/v1/me/device/approval-key", string(body), signRequest(t, r.device, "tenant-1", http.MethodPost, "/v1/me/device/approval-key", string(body), r.now)); code != http.StatusOK {
		t.Fatalf("registering the approval key: %d %v", code, out)
	}
	if got, _ := r.windows.Get(context.Background(), win.ID); got.ClosedAt == nil || got.ClosedBy != auth.WindowClosedByKeyChange {
		t.Errorf("the window after the key change: %+v", got)
	}
	// Re-linking: the store's swap closes them the same way (the handler's path is the browser's).
	win2 := win
	win2.ID, win2.OpenedAt = auth.NewWindowID(), r.now.Add(time.Minute)
	_ = r.windows.Open(context.Background(), win2)
	if _, err := sessions.ReplaceApprovalKey(context.Background(), "sess-phone", newDER, r.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
}

// The check another KIFF service makes (kiff/tafs#206) says which key signed
// and, for a signature made inside a window, whether that window is open.
func TestDeviceSignatureCheckSaysWhichKeyAndWindow(t *testing.T) {
	t.Parallel()
	r := newWindowRig(t)
	made := auth.NewMemorySignedRequests()
	mux := http.NewServeMux()
	handlers.NewDeviceSignatureHandler(made).WithWindows(r.windows).Register(mux)
	policed := auth.DefaultRoutePolicy().WithSignedRequests(made).Wrap(mux)
	body := []byte(`{"name":"taf_task"}`)
	sum := sha256.Sum256(body)
	ask := func(sig string) (int, map[string]any) {
		b, _ := json.Marshal(map[string]string{"method": http.MethodPost, "uri": "/v1/tafs/t1/approve", "body_sha256": hex.EncodeToString(sum[:]), "signature": sig})
		rec := httptest.NewRecorder()
		withFakeIdentity(policed, r.phone).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/device-signature", bytes.NewReader(b)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	now := time.Now().Truncate(time.Second)
	// The approval key: critical, no window.
	if code, out := ask(signRequest(t, r.approval, "tenant-1", http.MethodPost, "/v1/tafs/t1/approve", string(body), now)); code != http.StatusOK || out["key"] != auth.KeyCritical || out["window"] != nil {
		t.Fatalf("the approval key: %d %v", code, out)
	}
	// The device key inside an open window: device, window open, restarted.
	r.now = now
	win := r.open()
	code, out := ask(signRequestWindow(t, r.device, "tenant-1", http.MethodPost, "/v1/tafs/t1/approve", string(body), now.Add(time.Second), win))
	w, _ := out["window"].(map[string]any)
	if code != http.StatusOK || out["key"] != auth.KeyDevice || w["id"] != win || w["open"] != true {
		t.Fatalf("inside the window: %d %v", code, out)
	}
	// The device key naming a window that isn't open: device, open false.
	code, out = ask(signRequestWindow(t, r.device, "tenant-1", http.MethodPost, "/v1/tafs/t1/approve", string(body), now.Add(2*time.Second), auth.NewWindowID()))
	w, _ = out["window"].(map[string]any)
	if code != http.StatusOK || out["key"] != auth.KeyDevice || w["open"] != false {
		t.Fatalf("another window: %d %v", code, out)
	}
	// The device key outside a window: device, no window.
	if code, out := ask(signRequest(t, r.device, "tenant-1", http.MethodPost, "/v1/tafs/t1/approve", string(body), now.Add(3*time.Second))); code != http.StatusOK || out["key"] != auth.KeyDevice || out["window"] != nil {
		t.Fatalf("the device key: %d %v", code, out)
	}
	// Neither key: refused.
	stranger, _ := p256(t)
	if code, out := ask(signRequest(t, stranger, "tenant-1", http.MethodPost, "/v1/tafs/t1/approve", string(body), now)); code != http.StatusForbidden || out["error"] != "signature_invalid" {
		t.Fatalf("a stranger's key: %d %v", code, out)
	}
}

// planque's check (/v1/devices/verify) says which key signed, finding the
// approval key through the device's session, and counts a window only as
// the signed bytes name it.
func TestDeviceVerifySaysWhichKeyAndCountsAWindowOnlyFromTheSignedBytes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	members := membership.NewMemoryStore()
	svc := membership.NewService(members)
	if _, err := svc.EnsureFounder(ctx, "t1", "alice"); err != nil {
		t.Fatal(err)
	}
	devices := auth.NewMemoryDeviceStore()
	deviceKey, deviceDER := p256(t)
	approvalKey, approvalDER := p256(t)
	d := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: "t1", Subject: "alice", Name: "phone", Platform: "android", PublicKey: deviceDER, CreatedAt: time.Now()}
	if err := devices.Insert(ctx, d); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewMemoryDeveloperSessionStore()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if err := sessions.Insert(ctx, auth.DeveloperSession{ID: "sess-1", Subject: "alice", Tenant: "t1", TokenHash: sha256.Sum256([]byte("tok")), CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		DevicePublicKey: deviceDER, DeviceID: d.ID, ApprovalPublicKey: approvalDER}); err != nil {
		t.Fatal(err)
	}
	windows := auth.NewMemoryWindowStore()
	win := auth.ApprovalWindow{ID: auth.NewWindowID(), Tenant: "t1", Subject: "alice", DeviceID: d.ID, SessionID: "sess-1", OpenedAt: now, ExpiresAt: now.Add(auth.WindowLength), CapAt: now.Add(auth.WindowCap),
		OpenedBy: auth.DeviceKeyFingerprint(approvalDER)}
	if err := windows.Open(ctx, win); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h := handlers.NewDeviceVerifyHandler(devices, svc).WithSessions(sessions).WithWindows(windows)
	handlers.SetClock(h, func() time.Time { return now.Add(time.Minute) })
	h.Register(mux)
	connector := auth.Identity{Tenant: "t1", Subject: "alice", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{auth.RoleConnector}}
	sign := func(key *ecdsa.PrivateKey, msg string) string {
		digest := sha256.Sum256([]byte(msg))
		sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
		return base64.StdEncoding.EncodeToString(sig)
	}
	ask := func(msg, sig, window string) map[string]any {
		b, _ := json.Marshal(map[string]string{"device_id": d.ID, "message": base64.StdEncoding.EncodeToString([]byte(msg)), "signature": sig, "capability": "tools.connect", "window": window})
		rec := httptest.NewRecorder()
		withFakeIdentity(mux, connector).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/devices/verify", bytes.NewReader(b)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != http.StatusOK {
			t.Fatalf("verify: %d %v", rec.Code, out)
		}
		return out
	}
	v1 := "planque.request.v1\nt1\nPOST\n/v1/accounts/t1/grants/g1/approve\nabc\n2026-11-05T12:00:30Z"
	v2 := "planque.request.v2\nt1\nPOST\n/v1/accounts/t1/grants/g1/approve\nabc\n2026-11-05T12:00:30Z\n" + win.ID
	if out := ask(v1, sign(deviceKey, v1), ""); out["valid"] != true || out["key"] != auth.KeyDevice || out["window"] != nil {
		t.Errorf("the device key: %v", out)
	}
	if out := ask(v1, sign(approvalKey, v1), ""); out["valid"] != true || out["key"] != auth.KeyCritical {
		t.Errorf("the approval key, through the session: %v", out)
	}
	stranger, _ := p256(t)
	if out := ask(v1, sign(stranger, v1), ""); out["valid"] != false || out["reason"] != "signature_invalid" {
		t.Errorf("a stranger's key: %v", out)
	}
	// The window counts only as a line of the signed bytes.
	if out := ask(v1, sign(deviceKey, v1), win.ID); out["valid"] != false || out["reason"] != "window_not_signed" {
		t.Errorf("a window beside the signature: %v", out)
	}
	out := ask(v2, sign(deviceKey, v2), win.ID)
	w, _ := out["window"].(map[string]any)
	if out["valid"] != true || out["key"] != auth.KeyDevice || w["id"] != win.ID || w["open"] != true {
		t.Errorf("inside the window: %v", out)
	}
	if got, _ := windows.Get(ctx, win.ID); !got.ExpiresAt.Equal(now.Add(6 * time.Minute)) {
		t.Errorf("not restarted: %v", got.ExpiresAt)
	}
	// Once the session's approval key is another, the window doesn't count.
	if _, err := sessions.ReplaceApprovalKey(ctx, "sess-1", deviceDER, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	out = ask(v2, sign(deviceKey, v2), win.ID)
	w, _ = out["window"].(map[string]any)
	if out["valid"] != true || w["open"] != false {
		t.Errorf("after the approval key changed: %v", out)
	}
	if _, err := sessions.ReplaceApprovalKey(ctx, "sess-1", approvalDER, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Closed: named, but not open.
	_ = windows.Close(ctx, win.ID, now.Add(time.Minute), auth.WindowClosedByApp)
	out = ask(v2, sign(deviceKey, v2), win.ID)
	w, _ = out["window"].(map[string]any)
	if out["valid"] != true || w["open"] != false {
		t.Errorf("a closed window: %v", out)
	}
}

// "Did this happen?" splits by direction (kiff/tafs#494): Yes is routine, a
// tap inside the window; No gives the Card its room back, so it needs the
// approval key, and a device-signed No inside a window is refused.
func TestDidThisHappenNoNeedsTheApprovalKeyEvenInsideAWindow(t *testing.T) {
	t.Parallel()
	r := newWindowRig(t)
	inner := http.NewServeMux()
	handlers.NewMandatesHandler(mandate.NewMemoryStore(), nil).Register(inner)
	wh := handlers.NewApprovalWindowHandler(r.windows, nil)
	handlers.SetClock(wh, func() time.Time { return r.now })
	wh.Register(inner)
	r.policed = auth.DefaultRoutePolicy().WithWindows(r.windows).WithClock(func() time.Time { return r.now }).Wrap(inner)
	win := r.open()
	path := "/v1/me/did-this-happen/prop-1"
	no := `{"happened": false}`
	if code, out := r.call(r.phone, http.MethodPost, path, no, signRequestWindow(t, r.device, "tenant-1", http.MethodPost, path, no, r.now, win)); code != http.StatusForbidden || out["error"] != "critical_key_required" {
		t.Fatalf("a device-signed No inside the window: %d %v", code, out)
	}
	// Yes inside the window passes the key check (and stops at the store this rig has none of).
	yes := `{"happened": true}`
	if code, out := r.call(r.phone, http.MethodPost, path, yes, signRequestWindow(t, r.device, "tenant-1", http.MethodPost, path, yes, r.now, win)); code == http.StatusForbidden {
		t.Fatalf("a device-signed Yes inside the window: %d %v", code, out)
	}
	// No with the approval key passes it too.
	if code, out := r.call(r.phone, http.MethodPost, path, no, signRequest(t, r.approval, "tenant-1", http.MethodPost, path, no, r.now)); code == http.StatusForbidden {
		t.Fatalf("No with the approval key: %d %v", code, out)
	}
	// "Not sure" decides nothing: the device key alone takes it, with no
	// window open (kiff/tafs#498); unsigned, it's refused.
	check := path + "/check"
	if code, out := r.call(r.phone, http.MethodPost, check, "", signRequest(t, r.device, "tenant-1", http.MethodPost, check, "", r.now)); code == http.StatusForbidden {
		t.Fatalf("not sure with the device key: %d %v", code, out)
	}
	if code, out := r.call(r.phone, http.MethodPost, check, "", ""); code != http.StatusForbidden {
		t.Fatalf("not sure unsigned: %d %v", code, out)
	}
}
