package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
)

// A device removed by one of its sessions takes every session it signed in
// with it: none is left reading as live (an iPad's second session stayed
// live after its device was revoked), though the middleware already refused
// them. Another device's session stays.
func TestRemovingADeviceByOneSessionEndsItsOtherSessions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Now()
	sessions := auth.NewMemoryDeveloperSessionStore()
	devices := auth.NewMemoryDeviceStore()
	_, ipadKey := p256(t)
	_, phoneKey := p256(t)
	ipad := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: "tenant-1", Subject: "user-abc", Name: "tafs on iPad", Platform: "ios",
		PublicKey: ipadKey, CreatedAt: now}
	phone := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: "tenant-1", Subject: "user-abc", Name: "phone", Platform: "android",
		PublicKey: phoneKey, CreatedAt: now}
	for _, d := range []auth.ApprovalDevice{ipad, phone} {
		if err := devices.Insert(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	for i, s := range []struct{ id, device string }{{"sess-ipad-1", ipad.ID}, {"sess-ipad-2", ipad.ID}, {"sess-phone", phone.ID}} {
		key := ipadKey
		if s.device == phone.ID {
			key = phoneKey
		}
		if err := sessions.Insert(ctx, auth.DeveloperSession{ID: s.id, Tenant: "tenant-1", Subject: "user-abc", TokenHash: [32]byte{byte(i + 1)},
			CreatedAt: now, ExpiresAt: now.Add(time.Hour), DevicePublicKey: key, DeviceID: s.device}); err != nil {
			t.Fatal(err)
		}
	}
	h := handlers.NewDeviceAuthHandler(sessions, handlers.WithDeviceStore(devices))
	inner := http.NewServeMux()
	h.RegisterAuthenticated(inner)
	rec := httptest.NewRecorder()
	withFakeIdentity(inner, auth.Identity{Tenant: "tenant-1", Subject: "user-abc", Method: auth.MethodSession}).
		ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/me/devices/sess-ipad-1", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("removing the iPad by its first session: %d %s", rec.Code, rec.Body)
	}
	if d, _ := devices.Get(ctx, ipad.ID); !d.IsRevoked() {
		t.Fatal("the iPad isn't revoked")
	}
	live, err := sessions.ListActiveForSubject(ctx, "user-abc", now)
	if err != nil || len(live) != 1 || live[0].ID != "sess-phone" {
		t.Fatalf("live sessions after the iPad's removal: %+v %v, want the phone's alone", live, err)
	}
}
