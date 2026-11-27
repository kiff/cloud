package handlers

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
)

// kiff/tafs#494: what a draft's view says about the key its confirm takes
// is what the confirm enforces. A revision that widens nothing says
// confirm_key "window" and a tap inside the window confirms it; one that
// widens says "critical" with each way it widens, and a tap is refused
// with critical_key_required while the approval key confirms it. A draft
// that issues a new Card, for a taf that holds none, is critical too.
// Category: authority
// Attack: the app is shown "window" for a draft whose confirm then takes a tap that widens a Card, or the reverse
// Refused: the view's confirm_key and the confirm's check agree, both from the widening rule
func TestADraftsConfirmKeyIsWhatItsConfirmEnforces(t *testing.T) {
	t.Parallel()
	r := newDraftRig(t)
	r.startRae()
	windows := auth.NewMemoryWindowStore()
	policed := auth.DefaultRoutePolicy().WithWindows(windows).WithClock(func() time.Time { return r.now }).Wrap(r.mux)
	key := func() (*ecdsa.PrivateKey, []byte) {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
		return k, der
	}
	deviceKey, deviceDER := key()
	approvalKey, approvalDER := key()
	phone := auth.Identity{Subject: "alice", Tenant: "t1", Method: auth.MethodSession, MemberRole: membership.RoleAdmin,
		Roles: membership.IdentityRoles(membership.RoleAdmin), DeviceKey: deviceDER, DeviceSessionID: "sess-phone", DeviceID: "dev_phone", ApprovalKey: approvalDER}
	win := auth.ApprovalWindow{ID: auth.NewWindowID(), Tenant: "t1", Subject: "alice", DeviceID: "dev_phone", SessionID: "sess-phone",
		OpenedAt: r.now, ExpiresAt: r.now.Add(auth.WindowLength), CapAt: r.now.Add(auth.WindowCap), OpenedBy: auth.DeviceKeyFingerprint(approvalDER)}
	if err := windows.Open(t.Context(), win); err != nil {
		t.Fatal(err)
	}
	confirm := func(draft, version string, k *ecdsa.PrivateKey, window string) (int, map[string]any) {
		path := "/v1/me/agent-drafts/" + draft + "/confirm"
		body, _ := json.Marshal(map[string]string{"version": version})
		ts := r.now.Format(time.RFC3339)
		msg := auth.RequestMessageWindow("t1", http.MethodPost, path, body, ts, window)
		digest := sha256.Sum256(msg)
		sig, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
		header := "t=" + ts + ",v1=" + base64.StdEncoding.EncodeToString(sig)
		if window != "" {
			header = "t=" + ts + ",v2=" + base64.StdEncoding.EncodeToString(sig) + ",w=" + window
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set(auth.DeviceSignatureHeader, header)
		rec := httptest.NewRecorder()
		policed.ServeHTTP(rec, req.WithContext(auth.WithIdentity(req.Context(), phone)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	view := func(draft string) map[string]any {
		rr := do(t, r.mux, admin(), http.MethodGet, "/v1/me/agent-drafts/"+draft, nil)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return out
	}
	drafted := func(max int) (string, string) {
		code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{cardMax("rae-refund", "rae", max)}})
		if code != http.StatusCreated || d["status"] != "draft" {
			t.Fatalf("drafting %d: %d %v", max, code, d)
		}
		return d["id"].(string), d["version"].(string)
	}

	// Wider: 150.00 to 300.00 a refund. The view says the fingerprint, and why.
	wide, wideVersion := drafted(30000)
	if v := view(wide); v["confirm_key"] != "critical" || v["widens"] == nil {
		t.Fatalf("a widening draft's view: confirm_key %v, widens %v", v["confirm_key"], v["widens"])
	}
	if code, out := confirm(wide, wideVersion, deviceKey, win.ID); code != http.StatusForbidden || out["error"] != "critical_key_required" {
		t.Fatalf("a tap confirming a widening draft: %d %v", code, out)
	}
	if r.perActionMax("rae-refund") != 15000 {
		t.Fatalf("the refused tap changed the Card: %d", r.perActionMax("rae-refund"))
	}
	if code, out := confirm(wide, wideVersion, approvalKey, ""); code != http.StatusOK {
		t.Fatalf("the approval key confirming the widening draft: %d %v", code, out)
	}
	if r.perActionMax("rae-refund") != 30000 {
		t.Fatalf("the confirmed widening isn't in force: %d", r.perActionMax("rae-refund"))
	}

	// Narrower: back to 100.00 a refund. The view says a tap does, and a tap does.
	narrow, narrowVersion := drafted(10000)
	if v := view(narrow); v["confirm_key"] != "window" || v["widens"] != nil {
		t.Fatalf("a narrowing draft's view: confirm_key %v, widens %v", v["confirm_key"], v["widens"])
	}
	if code, out := confirm(narrow, narrowVersion, deviceKey, win.ID); code != http.StatusOK {
		t.Fatalf("a tap confirming a narrowing draft: %d %v", code, out)
	}
	if r.perActionMax("rae-refund") != 10000 {
		t.Fatalf("the confirmed narrowing isn't in force: %d", r.perActionMax("rae-refund"))
	}

	// A new Card, for a taf that holds none: authority it didn't have. The
	// view says the fingerprint; a tap inside the window is refused and
	// nothing is issued; the approval key confirms it.
	code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "sam", "cards": []any{cardMax("sam-refund", "sam", 10000)}})
	if code != http.StatusCreated || d["status"] != "draft" {
		t.Fatalf("drafting sam's first Card: %d %v", code, d)
	}
	fresh, freshVersion := d["id"].(string), d["version"].(string)
	if v := view(fresh); v["confirm_key"] != "critical" || !bytes.Contains(must(json.Marshal(v["widens"])), []byte("new_card")) {
		t.Fatalf("a new Card's draft view: confirm_key %v, widens %v", v["confirm_key"], v["widens"])
	}
	if code, out := confirm(fresh, freshVersion, deviceKey, win.ID); code != http.StatusForbidden || out["error"] != "critical_key_required" {
		t.Fatalf("a tap confirming a new Card: %d %v", code, out)
	}
	if ms, _ := r.store.List(t.Context(), "t1"); len(ms) != 1 {
		t.Fatalf("the refused tap issued a Card: %+v", ms)
	}
	if code, out := confirm(fresh, freshVersion, approvalKey, ""); code != http.StatusOK {
		t.Fatalf("the approval key confirming a new Card: %d %v", code, out)
	}
	if r.perActionMax("sam-refund") != 10000 {
		t.Fatalf("the confirmed new Card isn't in force")
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}
