package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
)

// Approving a device gives it its own way to answer held calls, so it
// needs a recent sign-in, checked by the API and not only the page.
// The window is auth.StepUpWindow: a sign-in exactly that old
// still approves, a second older is refused and approves nothing.
func TestDeviceApproveNeedsARecentSignIn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		age     *time.Duration
		approve bool
	}{
		{"at the window's edge", durationPtr(auth.StepUpWindow), true},
		{"a second past it", durationPtr(auth.StepUpWindow + time.Second), false},
		{"no sign-in time at all", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sessions := auth.NewMemoryDeveloperSessionStore()
			h := handlers.NewDeviceAuthHandler(sessions, handlers.WithDeviceStepUp(&auth.StepUp{Window: auth.StepUpWindow}))
			pub, inner := http.NewServeMux(), http.NewServeMux()
			h.RegisterPublic(pub)
			h.RegisterAuthenticated(inner)

			rec := httptest.NewRecorder()
			pub.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/device/start", nil))
			var started struct {
				DeviceCode string `json:"device_code"`
				UserCode   string `json:"user_code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil || started.UserCode == "" {
				t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
			}

			id := auth.Identity{Tenant: "tenant-1", Subject: "user-abc", Method: auth.MethodSession, FactorAge: tc.age}
			// The lookup says it before approving, so the page signs the
			// person in again first; approve still checks it.
			rec = httptest.NewRecorder()
			withFakeIdentity(inner, id).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/device/pending?user_code="+started.UserCode, nil))
			var pending struct {
				RecentSignIn *bool `json:"recent_sign_in"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &pending); err != nil || pending.RecentSignIn == nil || *pending.RecentSignIn != tc.approve {
				t.Fatalf("pending = %d %s, want recent_sign_in %v", rec.Code, rec.Body.String(), tc.approve)
			}
			b, _ := json.Marshal(map[string]string{"user_code": started.UserCode})
			rec = httptest.NewRecorder()
			withFakeIdentity(inner, id).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/device/approve", bytes.NewReader(b)))
			if tc.approve {
				if rec.Code != http.StatusOK {
					t.Fatalf("approve = %d %s, want 200", rec.Code, rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusForbidden || !bytes.Contains(rec.Body.Bytes(), []byte("reauth_required")) {
				t.Fatalf("approve = %d %s, want 403 reauth_required", rec.Code, rec.Body.String())
			}
			// Nothing was approved: the device still waits.
			tb, _ := json.Marshal(map[string]string{"device_code": started.DeviceCode})
			rec = httptest.NewRecorder()
			pub.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/device/token", bytes.NewReader(tb)))
			if !bytes.Contains(rec.Body.Bytes(), []byte("authorization_pending")) {
				t.Fatalf("token after a refused approve = %d %s, want authorization_pending", rec.Code, rec.Body.String())
			}
		})
	}
}

func durationPtr(d time.Duration) *time.Duration { return &d }
