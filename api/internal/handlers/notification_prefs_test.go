package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/tenant"
)

func newNotifyPrefsRig(t *testing.T) (http.Handler, tenant.Store, string) {
	t.Helper()
	store := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-notify", Status: tenant.StatusActive}
	if err := store.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create: %v", err)
	}
	inner := http.NewServeMux()
	handlers.NewNotificationPrefsHandler(store, nil).Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{Tenant: tnt.ID, Subject: "p", APIKey: true}))
	return wrapped, store, tnt.ID
}

func notifyPrefsReq(t *testing.T, mux http.Handler, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, "/v1/me/notification-prefs", strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, "/v1/me/notification-prefs", nil)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

type notifyPrefsResp struct {
	Prefs map[string]bool `json:"prefs"`
}

// A fresh tenant defaults every known rule to enabled.
func TestNotificationPrefs_DefaultsAllOn(t *testing.T) {
	t.Parallel()
	mux, _, _ := newNotifyPrefsRig(t)
	rec := notifyPrefsReq(t, mux, http.MethodGet, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d", rec.Code)
	}
	var got notifyPrefsResp
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.Prefs["approval"] || !got.Prefs["runtime_idle"] || !got.Prefs["held_email"] {
		t.Errorf("defaults should be all-on, got %+v", got.Prefs)
	}
}

// PUT persists a disabled rule and it reads back; unknown keys are dropped
// and omitted rules default back to enabled.
func TestNotificationPrefs_SetAndReadBack(t *testing.T) {
	t.Parallel()
	mux, _, _ := newNotifyPrefsRig(t)

	rec := notifyPrefsReq(t, mux, http.MethodPut, `{"prefs":{"runtime_idle":false,"bogus_rule":true}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var put notifyPrefsResp
	_ = json.Unmarshal(rec.Body.Bytes(), &put)
	if put.Prefs["runtime_idle"] != false {
		t.Errorf("runtime_idle should be off, got %+v", put.Prefs)
	}
	if _, ok := put.Prefs["bogus_rule"]; ok {
		t.Errorf("unknown rule should be dropped, got %+v", put.Prefs)
	}
	if put.Prefs["approval"] != true {
		t.Errorf("omitted rule should default to on, got %+v", put.Prefs)
	}

	// Reads back from storage.
	rec = notifyPrefsReq(t, mux, http.MethodGet, "")
	var got notifyPrefsResp
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Prefs["runtime_idle"] != false || got.Prefs["approval"] != true {
		t.Errorf("read-back mismatch: %+v", got.Prefs)
	}
}

func TestNotificationPrefs_RequiresTenantIdentity(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemoryStore()
	inner := http.NewServeMux()
	handlers.NewNotificationPrefsHandler(store, nil).Register(inner)
	rec := notifyPrefsReq(t, inner, http.MethodGet, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without identity", rec.Code)
	}
}
