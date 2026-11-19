package handlers_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
)

const tafsAlerts = "https://tafs.example.com/v1/kiff/alerts"

// A phone's session registers its account's held-call alert target
// with its key's signature, only at an app backend KIFF lists, for held
// calls only. It sees and removes only that target; the account's other
// targets are not its business.
func TestPhoneRegistersOnlyItsAppsHeldCallTarget(t *testing.T) {
	t.Parallel()
	store := alert.NewMemoryStore()
	ctx := context.Background()
	other := alert.Target{ID: "alrt_ops", TenantID: "tenant-1", URL: "https://ops.example.com/hook", Enabled: true, CreatedAt: time.Now()}
	if err := store.CreateTarget(ctx, other); err != nil {
		t.Fatal(err)
	}
	inner := http.NewServeMux()
	handlers.NewAlertTargetsHandler(store, true).WithPhoneURLs([]string{tafsAlerts}).Register(inner)
	policed := auth.DefaultRoutePolicy().Wrap(inner)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	phone := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"},
		DeviceKey: der, DeviceSessionID: "sess-phone"}
	signedAt := time.Now()
	do := func(method, path, body string, signed bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if signed {
			// Each call is signed a second apart, so none is a replay.
			signedAt = signedAt.Add(time.Second)
			ts := signedAt.UTC().Format(time.RFC3339)
			digest := sha256.Sum256(auth.RequestMessage("tenant-1", method, path, []byte(body), ts))
			sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set(auth.DeviceSignatureHeader, "t="+ts+",v1="+base64.StdEncoding.EncodeToString(sig))
		}
		rec := httptest.NewRecorder()
		withFakeIdentity(policed, phone).ServeHTTP(rec, req)
		return rec
	}
	create := `{"url":"` + tafsAlerts + `"}`

	// A stolen token alone registers nothing.
	if rec := do(http.MethodPost, "/v1/me/alert-targets", create, false); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "signature_required") {
		t.Fatalf("unsigned register: %d %s", rec.Code, rec.Body.String())
	}
	for name, body := range map[string]string{
		"another URL":      `{"url":"https://evil.example.com/hook"}`,
		"other outcomes":   `{"url":"` + tafsAlerts + `","outcomes":["owner_required","blocked"]}`,
		"one action":       `{"url":"` + tafsAlerts + `","action":"refund"}`,
		"registered off":   `{"url":"` + tafsAlerts + `","enabled":false}`,
		"every decision":   `{"url":"` + tafsAlerts + `","outcomes":["allowed"]}`,
		"approvals too":    `{"url":"` + tafsAlerts + `","outcomes":["approval_required"]}`,
		"warnings too":     `{"url":"` + tafsAlerts + `","outcomes":["card_warning"]}`,
		"trailing slashes": `{"url":"` + tafsAlerts + `/"}`,
	} {
		if rec := do(http.MethodPost, "/v1/me/alert-targets", body, true); rec.Code < 400 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	register := func() (string, string) {
		t.Helper()
		rec := do(http.MethodPost, "/v1/me/alert-targets", create, true)
		if rec.Code != http.StatusCreated {
			t.Fatalf("signed register: %d %s", rec.Code, rec.Body.String())
		}
		var got struct {
			ID       string   `json:"id"`
			Outcomes []string `json:"outcomes"`
			Secret   string   `json:"signing_secret"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.Outcomes, []string{"owner_required", alert.HoldAnswered, alert.PackRequested, alert.PackRequestAnswered, alert.ConnectionSignedOut, alert.RefundNotice, alert.HandoffAnswered}) || !strings.HasPrefix(got.Secret, "whsec_") {
			t.Fatalf("registered %+v", got)
		}
		return got.ID, got.Secret
	}
	first, firstSecret := register()
	// Setting it up again replaces it: one target, the new secret.
	second, secondSecret := register()
	if second == first || secondSecret == firstSecret {
		t.Fatal("registering again kept the old target or secret")
	}
	all, _ := store.ListTargets(ctx, "tenant-1")
	if len(all) != 2 || !slices.ContainsFunc(all, func(t alert.Target) bool { return t.ID == second && t.Secret == secondSecret }) {
		t.Fatalf("after registering again, the account has %+v", all)
	}

	// The phone lists only its app's target.
	rec := do(http.MethodGet, "/v1/me/alert-targets", "", false)
	var listed struct{ Targets []alert.Target }
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if len(listed.Targets) != 1 || listed.Targets[0].ID != second {
		t.Fatalf("phone listed %+v", listed.Targets)
	}

	// It removes its target only with a signature, and can't remove another.
	if rec := do(http.MethodDelete, "/v1/me/alert-targets/"+second, "", false); rec.Code != http.StatusForbidden {
		t.Errorf("unsigned delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodDelete, "/v1/me/alert-targets/"+other.ID, "", true); rec.Code != http.StatusNoContent {
		t.Errorf("delete another target: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodDelete, "/v1/me/alert-targets/"+second, "", true); rec.Code != http.StatusNoContent {
		t.Errorf("signed delete: %d %s", rec.Code, rec.Body.String())
	}
	all, _ = store.ListTargets(ctx, "tenant-1")
	if len(all) != 1 || all[0].ID != other.ID {
		t.Fatalf("after the phone's deletes, the account has %+v", all)
	}

	// The rest of the alert surface stays off a phone, signed or not.
	for _, path := range []string{"/v1/me/alert-targets/" + other.ID + "/enable", "/v1/me/alert-targets/" + other.ID + "/rotate-secret"} {
		if rec := do(http.MethodPost, path, "", true); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

// a review: two phones setting up alerts at once each get a 201,
// and the account is left with one of their targets, not none. Both
// requests are held inside the store call until both have arrived.
func TestPhoneRegistrationsAtOnceLeaveOneTarget(t *testing.T) {
	t.Parallel()
	store := &pausedReplace{MemoryStore: alert.NewMemoryStore(), arrived: make(chan struct{}, 2), release: make(chan struct{})}
	inner := http.NewServeMux()
	handlers.NewAlertTargetsHandler(store, true).WithPhoneURLs([]string{tafsAlerts}).Register(inner)
	phone := auth.Identity{Tenant: "tenant-1", Subject: "user-abc", DeviceKey: []byte{1}}
	ids := make(chan string, 2)
	for range 2 {
		go func() {
			req := httptest.NewRequest(http.MethodPost, "/v1/me/alert-targets", strings.NewReader(`{"url":"`+tafsAlerts+`"}`))
			rec := httptest.NewRecorder()
			withFakeIdentity(inner, phone).ServeHTTP(rec, req)
			var got struct{ ID string }
			_ = json.Unmarshal(rec.Body.Bytes(), &got)
			if rec.Code != http.StatusCreated {
				got.ID = ""
			}
			ids <- got.ID
		}()
	}
	<-store.arrived
	<-store.arrived
	close(store.release)
	a, b := <-ids, <-ids
	if a == "" || b == "" {
		t.Fatalf("a registration failed: %q %q", a, b)
	}
	all, _ := store.ListTargets(context.Background(), "tenant-1")
	if len(all) != 1 || (all[0].ID != a && all[0].ID != b) {
		t.Fatalf("after two registrations at once, the account has %+v", all)
	}
}

// pausedReplace holds each ReplaceTarget until release is closed.
type pausedReplace struct {
	*alert.MemoryStore
	arrived chan struct{}
	release chan struct{}
}

func (p *pausedReplace) ReplaceTarget(ctx context.Context, t alert.Target) error {
	p.arrived <- struct{}{}
	<-p.release
	return p.MemoryStore.ReplaceTarget(ctx, t)
}

// With no app backend listed, a phone registers no target at all.
func TestPhoneRegistersNoTargetWhenNoneListed(t *testing.T) {
	t.Parallel()
	inner := http.NewServeMux()
	handlers.NewAlertTargetsHandler(alert.NewMemoryStore(), true).Register(inner)
	req := httptest.NewRequest(http.MethodPost, "/v1/me/alert-targets", strings.NewReader(`{"url":"`+tafsAlerts+`"}`))
	rec := httptest.NewRecorder()
	withFakeIdentity(inner, auth.Identity{Tenant: "tenant-1", Subject: "user-abc", DeviceKey: []byte{1}}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "phone_alert_url_forbidden") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}
