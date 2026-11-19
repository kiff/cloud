package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
)

// listedDevices reads GET /v1/me/devices in lang (Accept-Language), by id.
func listedDevices(t *testing.T, rig *deviceRig, lang string) map[string]map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/me/devices", nil)
	if lang != "" {
		req.Header.Set("Accept-Language", lang)
	}
	rec := httptest.NewRecorder()
	rig.authed.ServeHTTP(rec, req)
	var listed struct {
		Devices []map[string]any `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("devices: %d %s", rec.Code, rec.Body.String())
	}
	out := map[string]map[string]any{}
	for _, d := range listed.Devices {
		out[d["id"].(string)] = d
	}
	return out
}

// A device's name in the list is drawn from what it is, in the reader's
// language, not fixed in the language its app had at sign-in (the owner's
// Devices, 14 Nov: "tafs en iPad" and "tafs sur Android appareil" among
// English ones). A phone says its kind; KIFF keeps it.
func TestADevicesNameIsDrawnFromItsKindInTheReadersLanguage(t *testing.T) {
	t.Parallel()
	rig, devices := devicesRig(t, "tenant-1", "user-abc")
	ctx := context.Background()

	_, tabletKey := newKey(t)
	tablet := signIn(t, rig, map[string]string{"public_key": tabletKey, "device_name": "tafs sur appareil Android", "platform": "android", "kind": "android_tablet"})
	if d, _ := devices.Get(ctx, tablet.DeviceID); d.Kind != auth.DeviceAndroidTablet || d.Name != "tafs sur appareil Android" {
		t.Fatalf("the tablet's kind wasn't kept: %+v", d)
	}
	// An app from before kinds sends a name only: its name says its kind.
	rig.advance(time.Minute)
	_, ipadKey := newKey(t)
	ipad := signIn(t, rig, map[string]string{"public_key": ipadKey, "device_name": "tafs en iPad", "platform": "ipados"})
	if d, _ := devices.Get(ctx, ipad.DeviceID); d.Kind != auth.DeviceIPad {
		t.Fatalf("the iPad's kind from its name: %+v", d)
	}
	// A kind KIFF doesn't know is the name's.
	rig.advance(time.Minute)
	_, phoneKey := newKey(t)
	phone := signIn(t, rig, map[string]string{"public_key": phoneKey, "device_name": "tafs on Android phone", "platform": "android", "kind": "foldable"})
	if d, _ := devices.Get(ctx, phone.DeviceID); d.Kind != auth.DeviceAndroidPhone {
		t.Fatalf("an unknown kind: %+v", d)
	}
	// The iPad signing in again, from an app that says its kind, keeps it.
	rig.advance(time.Minute)
	again := signIn(t, rig, map[string]string{"public_key": ipadKey, "device_name": "tafs on iPad", "platform": "ipados", "kind": "ipad"})
	if d, _ := devices.Get(ctx, again.DeviceID); again.DeviceID != ipad.DeviceID || d.Kind != auth.DeviceIPad || d.Name != "tafs on iPad" {
		t.Fatalf("the iPad again: %+v", d)
	}

	// Devices kept before kinds (no kind), named in whatever language the app had.
	legacy := map[string]string{}
	for _, name := range []string{"tafs en iPad", "tafs sur Android appareil", "tafs on Android phone", "tafs on iPhone", "Ana's laptop"} {
		der, _ := newKey(t)
		d := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: "tenant-1", Subject: "user-abc", Name: name, Platform: "ios", PublicKey: der, CreatedAt: rig.now}
		if err := devices.Insert(ctx, d); err != nil {
			t.Fatal(err)
		}
		legacy[name] = d.ID
	}

	want := map[string]struct{ kind, en, fr, es, de string }{
		tablet.DeviceID:                     {"android_tablet", "tafs on Android tablet", "tafs sur tablette Android", "tafs en tableta Android", "tafs auf dem Android-Tablet"},
		ipad.DeviceID:                       {"ipad", "tafs on iPad", "tafs sur iPad", "tafs en iPad", "tafs auf dem iPad"},
		phone.DeviceID:                      {"android_phone", "tafs on Android phone", "tafs sur téléphone Android", "tafs en teléfono Android", "tafs auf dem Android-Telefon"},
		legacy["tafs en iPad"]:              {"ipad", "tafs on iPad", "tafs sur iPad", "tafs en iPad", "tafs auf dem iPad"},
		legacy["tafs sur Android appareil"]: {"android_phone", "tafs on Android phone", "tafs sur téléphone Android", "tafs en teléfono Android", "tafs auf dem Android-Telefon"},
		legacy["tafs on Android phone"]:     {"android_phone", "tafs on Android phone", "tafs sur téléphone Android", "tafs en teléfono Android", "tafs auf dem Android-Telefon"},
		legacy["tafs on iPhone"]:            {"iphone", "tafs on iPhone", "tafs sur iPhone", "tafs en iPhone", "tafs auf dem iPhone"},
		// A name that says no kind shows as it was given, in every language.
		legacy["Ana's laptop"]: {"other", "Ana's laptop", "Ana's laptop", "Ana's laptop", "Ana's laptop"},
	}
	for _, lang := range []string{"en", "fr", "es", "de-AT, en;q=0.5"} {
		got := listedDevices(t, rig, lang)
		if len(got) != len(want) {
			t.Fatalf("%s: %d devices, want %d", lang, len(got), len(want))
		}
		for id, w := range want {
			d := got[id]
			name := map[string]string{"en": w.en, "fr": w.fr, "es": w.es, "de-AT, en;q=0.5": w.de}[lang]
			if d["kind"] != w.kind || d["display_name"] != name {
				t.Errorf("%s: %s (%v) = kind %v, display_name %q; want %s, %q", lang, id, d["name"], d["kind"], d["display_name"], w.kind, name)
			}
		}
	}
	// Negative: a reader in English never sees a name fixed in another
	// language; the name as given is still there, under name.
	for _, d := range listedDevices(t, rig, "") {
		switch d["display_name"] {
		case "tafs en iPad", "tafs sur Android appareil":
			t.Errorf("an English reader sees %q", d["display_name"])
		}
		if d["id"] == legacy["tafs en iPad"] && d["name"] != "tafs en iPad" {
			t.Errorf("the name as given was lost: %v", d)
		}
	}
}

// A phone signed in before devices shows by its session: its label says
// its kind too.
func TestAPhoneFromBeforeDevicesIsNamedInTheReadersLanguage(t *testing.T) {
	t.Parallel()
	rig := newDeviceRig(t, "tenant-1", "user-abc")
	der, _ := newKey(t)
	sess := auth.DeveloperSession{ID: "ses_old", Subject: "user-abc", Tenant: "tenant-1", Label: "tafs en iPhone", TokenHash: [32]byte{1},
		CreatedAt: rig.now, ExpiresAt: rig.now.Add(time.Hour), DevicePublicKey: der}
	if err := rig.sessions.Insert(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	d := listedDevices(t, rig, "en")["ses_old"]
	if d["kind"] != "iphone" || d["display_name"] != "tafs on iPhone" || d["name"] != "tafs en iPhone" {
		t.Fatalf("the old phone: %v", d)
	}
}
