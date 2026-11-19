package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
)

// Invariant: only the account's connector key says a vendor signed a
// connection out, and each sign-out alerts once an hour at most.
// Category: abuse
// Attack: an agent's key or a person's session sends sign-out alerts for another taf, or a flapping vendor floods the owner's phone
// Refused: anything but a connector key gets 403; a second alert for the same grant within the hour is dropped
func TestConnectionSignedOutAlertsOnceFromTheConnectorKey(t *testing.T) {
	t.Parallel()
	seen := &alertsSeen{}
	now := time.Date(2026, 10, 30, 22, 30, 0, 0, time.UTC)
	h := NewConnectionsHandler(seen).WithClock(func() time.Time { return now })
	mux := http.NewServeMux()
	h.Register(mux)
	post := func(id auth.Identity, body map[string]string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/me/connections/signed-out", bytes.NewReader(b))
		req = req.WithContext(auth.WithIdentity(req.Context(), id))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	connector := auth.Identity{Tenant: "t1", APIKey: true, Roles: []string{auth.RoleConnector}}
	good := map[string]string{"grant": "g_1", "agent": "refunds-k3f9", "vendor": "Stripe", "reason": "refresh_refused", "signed_out_at": "2026-10-30T22:10:00Z"}

	for _, who := range []auth.Identity{
		{Tenant: "t1", APIKey: true, AgentID: "refunds-k3f9", Roles: []string{"agent"}},
		{Tenant: "t1", Subject: "user_1", Method: auth.MethodSession, Roles: []string{"tenant_owner"}},
		{Tenant: "t1", Subject: "user_1", Method: auth.MethodDeveloper, DeviceSessionID: "s", Roles: []string{"tenant_owner"}},
	} {
		if rec := post(who, good); rec.Code != http.StatusForbidden {
			t.Errorf("%+v: %d %s", who, rec.Code, rec.Body.String())
		}
	}
	for field, bad := range map[string]string{"grant": "g/1", "agent": "rae~trial", "vendor": "", "reason": "because",
		"signed_out_at": "2026-10-01T00:00:00Z"} {
		body := map[string]string{}
		for k, v := range good {
			body[k] = v
		}
		body[field] = bad
		if rec := post(connector, body); rec.Code != http.StatusBadRequest {
			t.Errorf("bad %s: %d %s", field, rec.Code, rec.Body.String())
		}
	}
	if len(seen.events) != 0 {
		t.Fatalf("refused requests alerted: %+v", seen.events)
	}

	if rec := post(connector, good); rec.Code != http.StatusAccepted {
		t.Fatalf("connector: %d %s", rec.Code, rec.Body.String())
	}
	if len(seen.events) != 1 {
		t.Fatalf("events: %+v", seen.events)
	}
	ev := seen.events[0]
	at := time.Date(2026, 10, 30, 22, 10, 0, 0, time.UTC)
	if ev.TenantID != "t1" || ev.Outcome != alert.ConnectionSignedOut || ev.EventID != "conn-out:g_1:2026-10-30T22:10:00Z" ||
		ev.Connection == nil || *ev.Connection != (alert.Connection{Grant: "g_1", Agent: "refunds-k3f9", Vendor: "Stripe", Reason: "refresh_refused", SignedOutAt: at}) {
		t.Errorf("event: %+v %+v", ev, ev.Connection)
	}

	// A resend, or a new sign-out within the hour: accepted, not sent again.
	now = now.Add(30 * time.Minute)
	if rec := post(connector, good); rec.Code != http.StatusAccepted || len(seen.events) != 1 {
		t.Errorf("within the hour: %d, %d events", rec.Code, len(seen.events))
	}
	// Another grant is its own.
	other := map[string]string{"grant": "g_2", "agent": "refunds-k3f9", "vendor": "Stripe", "reason": "no_refresh_token", "signed_out_at": "2026-10-30T22:40:00Z"}
	if rec := post(connector, other); rec.Code != http.StatusAccepted || len(seen.events) != 2 {
		t.Errorf("another grant: %d, %d events", rec.Code, len(seen.events))
	}
	// After the hour, the same grant alerts again.
	now = now.Add(31 * time.Minute)
	good["signed_out_at"] = "2026-10-30T23:20:00Z"
	if rec := post(connector, good); rec.Code != http.StatusAccepted || len(seen.events) != 3 {
		t.Errorf("after the hour: %d, %d events", rec.Code, len(seen.events))
	}
}
