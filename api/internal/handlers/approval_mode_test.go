package handlers_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/membership"
)

// The desktop-client design, §1.1, §2.6: a view-only session, or one whose
// device key is in software, approves nothing: every PhoneApprovalRules
// route is refused with view_only whatever key signed it, and so is any
// window tap or signature over two requests. It still reads and makes the
// signed calls that grant nothing. A phone from before approval modes with
// no approval key signs every approval route with its device key, as before.
func TestASessionThatMustNotApproveNeverApproves(t *testing.T) {
	t.Parallel()
	reached := 0
	inner := http.NewServeMux()
	reach := func(w http.ResponseWriter, r *http.Request) { reached++; w.WriteHeader(http.StatusOK) }
	for _, pattern := range auth.PhoneApprovalRules {
		inner.HandleFunc(pattern, reach)
	}
	inner.HandleFunc("POST /v1/me/alert-targets", reach)
	inner.HandleFunc("GET /v1/me/mandates", reach)
	now := time.Now()
	policed := auth.DefaultRoutePolicy().WithClock(func() time.Time { return now }).Wrap(inner)
	deviceKey, deviceDER := p256(t)
	legacy := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"},
		DeviceKey: deviceDER, DeviceSessionID: "sess-phone", DeviceID: "dev_phone"}
	view := legacy
	view.ApprovalMode = auth.ApprovalModeView
	software := legacy
	software.ApprovalMode, software.KeyStore = auth.ApprovalModeKey, auth.KeyStoreSoftware
	path := func(pattern string) (method, p string) {
		var m, rest string
		for i := range pattern {
			if pattern[i] == ' ' {
				m, rest = pattern[:i], pattern[i+1:]
				break
			}
		}
		return m, regexp.MustCompile(`\{[a-z]+\}`).ReplaceAllString(rest, "x1")
	}
	n := 0
	call := func(id auth.Identity, method, p, header string) (int, string) {
		req := httptest.NewRequest(method, p, bytes.NewBufferString(`{}`))
		if header != "" {
			req.Header.Set(auth.DeviceSignatureHeader, header)
		}
		rec := httptest.NewRecorder()
		withFakeIdentity(policed, id).ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		code, _ := out["error"].(string)
		return rec.Code, code
	}
	// Each signature is fresh: a different second, so none is a replay.
	signed := func(method, p string) string {
		n++
		return signRequest(t, deviceKey, "tenant-1", method, p, `{}`, now.Add(time.Duration(n)*time.Second))
	}
	tap := func(method, p string) string {
		n++
		return signRequestWindow(t, deviceKey, "tenant-1", method, p, `{}`, now.Add(time.Duration(n)*time.Second), auth.NewWindowID())
	}
	other := make([]byte, 32)
	_, _ = rand.Read(other)
	together := "t=" + now.UTC().Format(time.RFC3339) + ",v3=" + base64.StdEncoding.EncodeToString([]byte("sig")) + ",with=sha256:" + hex.EncodeToString(other)

	for name, id := range map[string]auth.Identity{"view-only": view, "software-keyed": software} {
		for _, pattern := range auth.PhoneApprovalRules {
			m, p := path(pattern)
			if code, e := call(id, m, p, signed(m, p)); code != http.StatusForbidden || e != "view_only" {
				t.Errorf("%s: %s signed by the device key: %d %s", name, pattern, code, e)
			}
			if code, e := call(id, m, p, tap(m, p)); code != http.StatusForbidden || e != "view_only" {
				t.Errorf("%s: %s as a window's tap: %d %s", name, pattern, code, e)
			}
		}
		// A window's tap, or one signature over two requests, on a route that
		// grants nothing: refused too.
		if code, e := call(id, http.MethodPost, "/v1/me/alert-targets", tap(http.MethodPost, "/v1/me/alert-targets")); code != http.StatusForbidden || e != "view_only" {
			t.Errorf("%s: a window's tap on a signed route: %d %s", name, code, e)
		}
		if code, e := call(id, http.MethodPost, "/v1/me/alert-targets", together); code != http.StatusForbidden || e != "view_only" {
			t.Errorf("%s: a v3 signature: %d %s", name, code, e)
		}
		before := reached
		if code, _ := call(id, http.MethodPost, "/v1/me/alert-targets", signed(http.MethodPost, "/v1/me/alert-targets")); code != http.StatusOK {
			t.Errorf("%s: a signed call that grants nothing: %d", name, code)
		}
		if code, _ := call(id, http.MethodGet, "/v1/me/mandates", ""); code != http.StatusOK {
			t.Errorf("%s: a read: %d", name, code)
		}
		if reached != before+2 {
			t.Fatalf("%s: an approval reached its handler", name)
		}
	}
	// A session held to an approval key it doesn't have: the device key
	// signs nothing that grants authority.
	keyless := legacy
	keyless.ApprovalMode = auth.ApprovalModeKey
	if code, e := call(keyless, http.MethodPost, "/v1/me/mandates", signed(http.MethodPost, "/v1/me/mandates")); code != http.StatusForbidden || e != "approval_key_required" {
		t.Errorf("mode key without an approval key: %d %s", code, e)
	}
	// The legacy phone: every approval route, by its device key, as before.
	before := reached
	for _, pattern := range auth.PhoneApprovalRules {
		m, p := path(pattern)
		if code, e := call(legacy, m, p, signed(m, p)); code != http.StatusOK {
			t.Errorf("legacy: %s signed by the device key: %d %s", pattern, code, e)
		}
	}
	if reached != before+len(auth.PhoneApprovalRules) {
		t.Fatalf("legacy: %d approval routes reached, want %d", reached-before, len(auth.PhoneApprovalRules))
	}
}

// A view-only session of a person who has an approval device gets an
// approval key only by pairing: it can't register one, or ask the browser to
// re-link one, signed by its device key.
func TestAViewOnlySessionGainsNoApprovalKey(t *testing.T) {
	t.Parallel()
	r := newPairRig(t)
	// The person's phone approves, so this session's first key would make a second approver.
	if err := r.devices.MarkApprovalKey(context.Background(), r.phone.DeviceID); err != nil {
		t.Fatal(err)
	}
	view := r.phone
	view.ApprovalKey, view.ApprovalMode = nil, auth.ApprovalModeView
	_, newDER := p256(t)
	body, _ := json.Marshal(map[string]string{"public_key": base64.StdEncoding.EncodeToString(newDER)})
	const register = "/v1/me/device/approval-key"
	if code, out := send(r.authed, &view, http.MethodPost, register, string(body), signRequest(t, r.device, "tenant-1", http.MethodPost, register, string(body), r.now)); code != http.StatusForbidden || out["error"] != "view_only" {
		t.Fatalf("registering an approval key: %d %v", code, out)
	}
	const relink = "/v1/me/device/relink"
	if code, out := send(r.authed, &view, http.MethodPost, relink, `{}`, signRequest(t, r.device, "tenant-1", http.MethodPost, relink, `{}`, r.now)); code != http.StatusForbidden || out["error"] != "view_only" {
		t.Fatalf("re-linking an approval key: %d %v", code, out)
	}
}

// The desktop-client design, §1.1: the browser enrols an approval key for the
// person's first approval device only. Once they have one, active or removed
// in the last 30 days, a new device is view-only whatever it sent. With
// none, a sign-in with no approval key is view-only too.
func TestTheBrowserEnrolsOnlyTheFirstApprovalDevice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rig, devices := devicesRig(t, "tenant-1", "user-abc")

	_, pcKey := newKey(t)
	_, pcApproval := newKey(t)
	first := signIn(t, rig, map[string]string{"public_key": pcKey, "approval_public_key": pcApproval, "device_name": "tafs on this PC", "platform": "windows", "kind": "other"})
	if d, err := devices.Get(ctx, first.DeviceID); first.ApprovalMode != auth.ApprovalModeKey || len(first.ApprovalPublicKey) == 0 || err != nil || !d.HasApprovalKey {
		t.Fatalf("the first approval device: mode %q, key %x, device %+v %v", first.ApprovalMode, first.ApprovalPublicKey, d, err)
	}
	for name, body := range map[string]map[string]string{
		"a phone without one":             {"platform": "android", "kind": "android_phone"},
		"a computer with an approval key": {"approval_public_key": pcApproval, "platform": "macos", "kind": "other"},
	} {
		_, key := newKey(t)
		body["public_key"], body["device_name"] = key, "tafs"
		rig.advance(time.Minute)
		s := signIn(t, rig, body)
		if d, err := devices.Get(ctx, s.DeviceID); s.ApprovalMode != auth.ApprovalModeView || len(s.ApprovalPublicKey) != 0 || err != nil || d.HasApprovalKey {
			t.Errorf("%s: mode %q, key %x, device %+v %v", name, s.ApprovalMode, s.ApprovalPublicKey, d, err)
		}
	}

	// Someone with no approval device signing in without an approval key: a
	// new session never gets "" (approving with its device key), so it's
	// view-only.
	plain, _ := devicesRig(t, "tenant-1", "user-def")
	_, phoneKey := newKey(t)
	if s := signIn(t, plain, map[string]string{"public_key": phoneKey, "device_name": "tafs on iPhone"}); s.ApprovalMode != auth.ApprovalModeView || len(s.ApprovalPublicKey) != 0 {
		t.Fatalf("no approval device, no approval key: %+v", s)
	}

	// The last approval device removed: for 30 days the browser still enrols
	// none, then the first-device enrolment opens again.
	lost, lostDevices := devicesRig(t, "tenant-1", "user-ghi")
	_, k := newKey(t)
	_, a := newKey(t)
	phone := signIn(t, lost, map[string]string{"public_key": k, "approval_public_key": a, "device_name": "tafs on iPhone", "platform": "ios"})
	if err := lostDevices.Revoke(ctx, phone.DeviceID, lost.now); err != nil {
		t.Fatal(err)
	}
	lost.advance(29 * 24 * time.Hour)
	_, k = newKey(t)
	// A computer, which the rule still holds to (a phone enrols until phones
	// pair).
	if s := signIn(t, lost, map[string]string{"public_key": k, "approval_public_key": a, "device_name": "tafs", "platform": "macos", "kind": "other"}); s.ApprovalMode != auth.ApprovalModeView || len(s.ApprovalPublicKey) != 0 {
		t.Fatalf("29 days after the phone was removed: %+v", s)
	}
	lost.advance(2 * 24 * time.Hour)
	_, k = newKey(t)
	if s := signIn(t, lost, map[string]string{"public_key": k, "approval_public_key": a, "device_name": "tafs", "platform": "macos", "kind": "other"}); s.ApprovalMode != auth.ApprovalModeKey || len(s.ApprovalPublicKey) == 0 {
		t.Fatalf("31 days after the phone was removed: %+v", s)
	}
}

// Removing another of the person's devices that holds an approval
// key takes an approval. A view-only session can't remove the phone; a
// session held to its approval key signs it with that key; any session
// removes its own device with no signature; a phone from before approval
// modes removes as before.
func TestRemovingADeviceThatApprovesTakesAnApproval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newPairRig(t)
	if err := r.devices.MarkApprovalKey(ctx, r.phone.DeviceID); err != nil {
		t.Fatal(err)
	}
	r.phone.ApprovalMode = auth.ApprovalModeKey
	pcKey, pcDER := p256(t)
	if err := r.devices.Insert(ctx, auth.ApprovalDevice{ID: "dev_pc", Tenant: "tenant-1", Subject: "user-abc", Name: "pc", Platform: "windows",
		Kind: auth.DeviceOther, PublicKey: pcDER, CreatedAt: r.now}); err != nil {
		t.Fatal(err)
	}
	pc := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, MemberRole: membership.RoleAdmin,
		DeviceKey: pcDER, DeviceSessionID: "sess-pc", DeviceID: "dev_pc", ApprovalMode: auth.ApprovalModeView}
	// remove sends the removal as id, signed by signer unless it's nil.
	remove := func(id auth.Identity, device string, signer *ecdsa.PrivateKey, at time.Time) (int, map[string]any) {
		p := "/v1/me/devices/" + device
		header := ""
		if signer != nil {
			header = signRequest(t, signer, "tenant-1", http.MethodDelete, p, "", at)
		}
		return send(r.authed, &id, http.MethodDelete, p, "", header)
	}
	gone := func(device string) bool {
		d, err := r.devices.Get(ctx, device)
		return err == nil && d.IsRevoked()
	}

	if code, out := remove(pc, "dev_phone", nil, r.now); code != http.StatusForbidden || out["error"] != "view_only" || gone("dev_phone") {
		t.Fatalf("a view-only computer removing the phone: %d %v", code, out)
	}
	if code, out := remove(pc, "dev_phone", pcKey, r.now); code != http.StatusForbidden || out["error"] != "view_only" || gone("dev_phone") {
		t.Fatalf("a view-only computer removing the phone, signed by its device key: %d %v", code, out)
	}
	// A computer that approves, but hasn't signed with its approval key.
	keyed := pc
	_, pcApprovalDER := p256(t)
	keyed.ApprovalMode, keyed.ApprovalKey = auth.ApprovalModeKey, pcApprovalDER
	if code, out := remove(keyed, "dev_phone", nil, r.now); code != http.StatusForbidden || out["error"] != "approval_key_required" || gone("dev_phone") {
		t.Fatalf("an unsigned removal of the phone: %d %v", code, out)
	}
	if code, out := remove(keyed, "dev_phone", pcKey, r.now.Add(time.Second)); code != http.StatusForbidden || out["error"] != "approval_key_required" || gone("dev_phone") {
		t.Fatalf("the phone's removal signed by a device key: %d %v", code, out)
	}

	// The phone's approval key removes another device that approves.
	if err := r.devices.MarkApprovalKey(ctx, "dev_pc"); err != nil {
		t.Fatal(err)
	}
	if code, out := remove(r.phone, "dev_pc", r.approval, r.now.Add(2*time.Second)); code != http.StatusNoContent || !gone("dev_pc") {
		t.Fatalf("the phone's approval key removing the computer: %d %v", code, out)
	}

	// A view-only session removes its own device, unsigned.
	_, ownDER := p256(t)
	if err := r.devices.Insert(ctx, auth.ApprovalDevice{ID: "dev_mac", Tenant: "tenant-1", Subject: "user-abc", Name: "mac", Platform: "macos",
		Kind: auth.DeviceOther, PublicKey: ownDER, CreatedAt: r.now}); err != nil {
		t.Fatal(err)
	}
	mac := pc
	mac.DeviceKey, mac.DeviceSessionID, mac.DeviceID = ownDER, "sess-mac", "dev_mac"
	if code, out := remove(mac, "dev_mac", nil, r.now); code != http.StatusNoContent || !gone("dev_mac") {
		t.Fatalf("a view-only session removing itself: %d %v", code, out)
	}

	// A phone from before approval modes removes the phone that approves,
	// unsigned, as it did.
	_, oldDER := p256(t)
	if err := r.devices.Insert(ctx, auth.ApprovalDevice{ID: "dev_old", Tenant: "tenant-1", Subject: "user-abc", Name: "old", Platform: "ios",
		Kind: auth.DeviceOther, PublicKey: oldDER, CreatedAt: r.now}); err != nil {
		t.Fatal(err)
	}
	old := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, MemberRole: membership.RoleAdmin,
		DeviceKey: oldDER, DeviceSessionID: "sess-old", DeviceID: "dev_old"}
	if code, out := remove(old, "dev_phone", nil, r.now); code != http.StatusNoContent || !gone("dev_phone") {
		t.Fatalf("a phone from before approval modes removing the phone: %d %v", code, out)
	}
}

// A computer that approves with a passkey, or whose device
// key is in software, never approves with its device key, even as the
// person's first device: its session is view-only (until passkey approvals),
// its device records the key store, and a computer that says neither an
// approval key nor a passkey is refused at the start.
func TestASoftwareKeyedOrPasskeyFirstComputerApprovesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, c := range map[string]struct {
		body     map[string]string
		keyStore string
	}{
		"software, passkey": {map[string]string{"key_store": "software", "approval": "passkey", "platform": "macos", "kind": "other"}, auth.KeyStoreSoftware},
		"passkey, hardware": {map[string]string{"approval": "passkey", "platform": "linux", "kind": "other"}, ""},
	} {
		rig, devices := devicesRig(t, "tenant-1", "user-"+name)
		_, key := newKey(t)
		c.body["public_key"], c.body["device_name"] = key, "tafs"
		s := signIn(t, rig, c.body)
		d, err := devices.Get(ctx, s.DeviceID)
		if err != nil {
			t.Fatal(err)
		}
		id := auth.Identity{ApprovalMode: s.ApprovalMode, ApprovalKey: s.ApprovalPublicKey, KeyStore: d.KeyStore, DeviceKey: d.PublicKey}
		if !id.ViewOnly() || s.ApprovalMode != auth.ApprovalModeView || d.KeyStore != c.keyStore || d.HasApprovalKey {
			t.Errorf("%s: mode %q, key store %q, device %+v: the device key approves", name, s.ApprovalMode, d.KeyStore, d)
		}
	}

	rig, _ := devicesRig(t, "tenant-1", "user-abc")
	_, key := newKey(t)
	_, approval := newKey(t)
	for name, c := range map[string]struct {
		body map[string]string
		want string
	}{
		"a computer that says neither": {map[string]string{"platform": "windows", "kind": "other"}, "approval_required"},
		"a Mac that says neither":      {map[string]string{"platform": "macos"}, "approval_required"},
		"key_store without passkey":    {map[string]string{"key_store": "software", "approval_public_key": approval, "platform": "macos"}, "invalid_request"},
		"key_store not software":       {map[string]string{"key_store": "tpm", "approval": "passkey", "platform": "windows"}, "invalid_request"},
		"passkey with an approval key": {map[string]string{"approval": "passkey", "approval_public_key": approval, "platform": "linux"}, "invalid_request"},
		"another approval":             {map[string]string{"approval": "key", "platform": "linux"}, "invalid_request"},
		"passkey with no device key":   {map[string]string{"approval": "passkey", "public_key": ""}, "invalid_request"},
	} {
		body := map[string]string{"public_key": key, "device_name": "tafs"}
		for k, v := range c.body {
			body[k] = v
		}
		if body["public_key"] == "" {
			delete(body, "public_key")
		}
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		rig.pub.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/device/start", bytes.NewReader(b)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != http.StatusBadRequest || out["error"] != c.want {
			t.Errorf("%s: %d %v, want 400 %s", name, rec.Code, out, c.want)
		}
	}
}

// Until phones pair, a phone
// or tablet (ios, ipados, android) that sends its own approval key still
// enrols it while the person has another approval device, so a second iPhone
// or iPad isn't stranded, and the person gets an email each time. A computer
// with an approval key stays view-only (it pairs), and so does a phone with
// none. A first device enrols as before, with no email.
func TestASecondPhoneEnrolsItsApprovalKeyAndThePersonIsTold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	notices := &fakeNotices{}
	rig, devices := devicesRig(t, "tenant-1", "user-abc", handlers.WithDeviceNotices(fakeEmails{"user-abc": "ana@example.com"}, notices))
	sent := func() int { notices.mu.Lock(); defer notices.mu.Unlock(); return len(notices.sent) }

	_, k := newKey(t)
	_, a := newKey(t)
	first := signIn(t, rig, map[string]string{"public_key": k, "approval_public_key": a, "device_name": "Ana's iPhone", "platform": "ios", "kind": "iphone"})
	if first.ApprovalMode != auth.ApprovalModeKey || sent() != 0 {
		t.Fatalf("the first phone: mode %q, %d emails", first.ApprovalMode, sent())
	}
	for _, c := range []struct {
		name, platform, kind string
	}{
		{"an iPad", "ipados", "ipad"},
		{"another iPhone", "ios", "iphone"},
		{"an Android tablet", "android", "android_tablet"},
	} {
		before := sent()
		_, key := newKey(t)
		_, approval := newKey(t)
		rig.advance(time.Minute)
		s := signIn(t, rig, map[string]string{"public_key": key, "approval_public_key": approval, "device_name": c.name, "platform": c.platform, "kind": c.kind})
		d, err := devices.Get(ctx, s.DeviceID)
		if s.ApprovalMode != auth.ApprovalModeKey || len(s.ApprovalPublicKey) == 0 || err != nil || !d.HasApprovalKey {
			t.Errorf("%s with an approval key: mode %q, device %+v %v", c.name, s.ApprovalMode, d, err)
		}
		notices.mu.Lock()
		got := notices.sent[len(notices.sent)-1:]
		to := notices.to[len(notices.to)-1:]
		notices.mu.Unlock()
		if sent() != before+1 || to[0] != "ana@example.com" || !strings.Contains(got[0].Subject, c.name) || !strings.Contains(got[0].Body, c.name) {
			t.Errorf("%s: the new-approver email: %d sent, %v %+v", c.name, sent()-before, to, got)
		}
	}
	// A computer with an approval key, and a phone with none: view-only, no email.
	for name, body := range map[string]map[string]string{
		"a Mac with an approval key":     {"platform": "macos", "kind": "other", "approval_public_key": ""},
		"a Windows PC with one":          {"platform": "windows", "kind": "other", "approval_public_key": ""},
		"a Linux machine with one":       {"platform": "linux", "kind": "other", "approval_public_key": ""},
		"a phone with no approval key":   {"platform": "android", "kind": "android_phone"},
		"an iPhone with no approval key": {"platform": "ios", "kind": "iphone"},
	} {
		before := sent()
		_, key := newKey(t)
		body["public_key"], body["device_name"] = key, "tafs"
		if _, ok := body["approval_public_key"]; ok {
			_, body["approval_public_key"] = newKey(t)
		}
		rig.advance(time.Minute)
		s := signIn(t, rig, body)
		if s.ApprovalMode != auth.ApprovalModeView || len(s.ApprovalPublicKey) != 0 || sent() != before {
			t.Errorf("%s: mode %q, key %x, %d emails", name, s.ApprovalMode, s.ApprovalPublicKey, sent()-before)
		}
	}
}

// A view-only session registers its own first approval key, signed by its
// device key, exactly when a fresh browser sign-in with that key would enrol
// it: the person has no approval device, active or removed in the last 30
// days, and its device key isn't in software. It's then held to that key.
// Otherwise it stays view-only: a stolen token never mints an approver.
func TestAViewOnlySessionRegistersItsFirstKeyOnlyAsTheBrowserWould(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const register = "/v1/me/device/approval-key"
	type device struct {
		rig     *deviceRig
		devices *auth.MemoryDeviceStore
		sess    auth.DeveloperSession
		key     *ecdsa.PrivateKey
		der     []byte
	}
	signInView := func(t *testing.T, subject string, extra map[string]string) device {
		rig, devices := devicesRig(t, "tenant-1", subject)
		k, der := p256(t)
		body := map[string]string{"public_key": base64.StdEncoding.EncodeToString(der), "device_name": "Pixel", "platform": "android", "kind": "android_phone"}
		for f, v := range extra {
			body[f] = v
		}
		s := signIn(t, rig, body)
		if s.ApprovalMode != auth.ApprovalModeView {
			t.Fatalf("the sign-in with no approval key: mode %q", s.ApprovalMode)
		}
		return device{rig: rig, devices: devices, sess: s, key: k, der: der}
	}
	registerAs := func(t *testing.T, d device) (int, map[string]any) {
		dev, err := d.devices.Get(ctx, d.sess.DeviceID)
		if err != nil {
			t.Fatal(err)
		}
		id := auth.Identity{Subject: d.sess.Subject, Tenant: "tenant-1", Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"},
			DeviceKey: d.der, DeviceSessionID: d.sess.ID, DeviceID: d.sess.DeviceID, ApprovalMode: d.sess.ApprovalMode, KeyStore: dev.KeyStore}
		_, approval := p256(t)
		body, _ := json.Marshal(map[string]string{"public_key": base64.StdEncoding.EncodeToString(approval)})
		d.rig.advance(time.Second)
		now := d.rig.now
		h := auth.DefaultRoutePolicy().WithClock(func() time.Time { return now }).Wrap(d.rig.inner)
		return send(h, &id, http.MethodPost, register, string(body), signRequest(t, d.key, "tenant-1", http.MethodPost, register, string(body), now))
	}
	reread := func(t *testing.T, d device) auth.DeveloperSession {
		s, err := d.rig.sessions.GetByHash(ctx, d.sess.TokenHash, d.rig.now)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	addApprover := func(t *testing.T, d device, removedAgo time.Duration) {
		_, der := p256(t)
		phone := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: "tenant-1", Subject: d.sess.Subject, Name: "iPhone", Platform: "ios",
			Kind: auth.DeviceOther, PublicKey: der, HasApprovalKey: true, CreatedAt: d.rig.now.Add(-60 * 24 * time.Hour)}
		if err := d.devices.Insert(ctx, phone); err != nil {
			t.Fatal(err)
		}
		if removedAgo > 0 {
			if err := d.devices.Revoke(ctx, phone.ID, d.rig.now.Add(-removedAgo)); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("the person's first device, with no other: registered, then held to it", func(t *testing.T) {
		d := signInView(t, "user-first", nil)
		if code, out := registerAs(t, d); code != http.StatusOK || out["fingerprint"] == nil {
			t.Fatalf("the first key: %d %v", code, out)
		}
		s := reread(t, d)
		dev, derr := d.devices.Get(ctx, d.sess.DeviceID)
		if derr != nil || s.ApprovalMode != auth.ApprovalModeKey || len(s.ApprovalPublicKey) == 0 || !dev.HasApprovalKey {
			t.Fatalf("after the first key: mode %q, key %x, device %+v (%v)", s.ApprovalMode, s.ApprovalPublicKey, dev, derr)
		}
		// Its first key only: a second is a new sign-in.
		d.sess = s
		if code, out := registerAs(t, d); code != http.StatusConflict || out["error"] != "approval_key_set" {
			t.Fatalf("a second key: %d %v", code, out)
		}
	})
	for name, c := range map[string]struct {
		extra      map[string]string
		removedAgo time.Duration
		approver   bool
	}{
		"another approval device":  {approver: true},
		"one removed 29 days ago":  {approver: true, removedAgo: 29 * 24 * time.Hour},
		"a device key in software": {extra: map[string]string{"approval": "passkey", "key_store": "software", "platform": "macos", "kind": "other"}},
	} {
		t.Run(name, func(t *testing.T) {
			d := signInView(t, "user-"+strings.ReplaceAll(name, " ", "-"), c.extra)
			if c.approver {
				addApprover(t, d, c.removedAgo)
			}
			if code, out := registerAs(t, d); code != http.StatusForbidden || out["error"] != "view_only" {
				t.Fatalf("%d %v", code, out)
			}
			if s := reread(t, d); s.ApprovalMode != auth.ApprovalModeView || len(s.ApprovalPublicKey) != 0 {
				t.Fatalf("the session gained authority: %+v", s)
			}
		})
	}
	t.Run("one removed 31 days ago: registered, as the browser would", func(t *testing.T) {
		d := signInView(t, "user-31", nil)
		addApprover(t, d, 31*24*time.Hour)
		if code, out := registerAs(t, d); code != http.StatusOK {
			t.Fatalf("%d %v", code, out)
		}
	})
}
