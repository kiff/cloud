package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/tenant"
)

func TestAccountTimeZonePermissionsAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		id     auth.Identity
		body   string
		status int
	}{
		{"owner", owner(), `{"time_zone":"Europe/Paris"}`, 200},
		{"admin", auth.Identity{Tenant: "t1", Roles: []string{"admin"}}, `{"time_zone":"America/Bogota"}`, 200},
		{"viewer", auth.Identity{Tenant: "t1", MemberRole: "viewer"}, `{"time_zone":"UTC"}`, 403},
		{"runtime", runtimeKey(), `{"time_zone":"UTC"}`, 403},
		{"bound admin", auth.Identity{Tenant: "t1", AgentID: "agent", MemberRole: "admin", Roles: []string{"admin"}}, `{"time_zone":"UTC"}`, 403},
		{"invalid", owner(), `{"time_zone":"not/a-zone"}`, 400},
		{"local", owner(), `{"time_zone":"Local"}`, 400},
		{"empty", owner(), `{"time_zone":""}`, 400},
		{"extra object", owner(), `{"time_zone":"UTC"} {}`, 400},
		{"unknown field", owner(), `{"time_zone":"UTC","location":"UTC"}`, 400},
		{"unauthenticated", auth.Identity{}, `{"time_zone":"UTC"}`, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := tenant.NewMemoryStore()
			tenantID, otherID := tenant.NewID(), tenant.NewID()
			if tc.id.Tenant != "" {
				tc.id.Tenant = tenantID
			}
			for _, id := range []string{tenantID, otherID} {
				if err := store.Create(context.Background(), tenant.Tenant{ID: id, Slug: "tz-" + id, Status: tenant.StatusActive}); err != nil {
					t.Fatal(err)
				}
			}
			h := NewMeHandler(store)
			mux := http.NewServeMux()
			h.Register(mux)
			r := httptest.NewRequest(http.MethodPut, "/v1/me/time-zone", strings.NewReader(tc.body))
			r = r.WithContext(auth.WithIdentity(r.Context(), tc.id))
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, r)
			if rr.Code != tc.status {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			a, _ := store.Get(context.Background(), tenantID)
			b, _ := store.Get(context.Background(), otherID)
			if tenant.TimeZoneFor(b) != "UTC" {
				t.Fatal("changed another tenant")
			}
			if tc.status != 200 && tenant.TimeZoneFor(a) != "UTC" {
				t.Fatal("failed request changed setting")
			}
			if tc.status == 200 && tenant.TimeZoneFor(a) == "UTC" {
				t.Fatal("setting not saved")
			}
		})
	}
}

func TestCardTimeZoneAccountDefaultAndReissue(t *testing.T) {
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	tenantID := tenant.NewID()
	identity := owner()
	identity.Tenant = tenantID
	if err := tenants.Create(ctx, tenant.Tenant{ID: tenantID, Slug: "tz-account", Status: tenant.StatusActive, Metadata: map[string]any{"time_zone": "Europe/Paris"}}); err != nil {
		t.Fatal(err)
	}
	cards := mandate.NewMemoryStore()
	h := NewMandatesHandler(cards, stubDomainSource{yaml: mandateDomainYAML}, WithMandatesTenants(tenants))
	mux := http.NewServeMux()
	h.Register(mux)
	body := map[string]any{"id": "new", "subject": "agent://a", "grants": []map[string]any{{"action": "AUTO_REFUND"}}, "aggregates": []map[string]any{{"quantity": "count", "limit": 10, "window": "calendar_day"}}}
	issue := func(want string) {
		t.Helper()
		rr := do(t, mux, identity, http.MethodPost, "/v1/me/mandates", body)
		if rr.Code != 201 && rr.Code != 200 {
			t.Fatalf("issue: %d %s", rr.Code, rr.Body.String())
		}
		list, err := cards.List(ctx, tenantID)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range list {
			if m.ID == body["id"] {
				if m.Location != want {
					t.Fatalf("location %q want %q", m.Location, want)
				}
				return
			}
		}
		t.Fatal("card not found")
	}
	issue("Europe/Paris")
	if err := tenants.SetMetadata(ctx, tenantID, "time_zone", "America/Bogota"); err != nil {
		t.Fatal(err)
	}
	issue("Europe/Paris")
	body["id"] = "later"
	issue("America/Bogota")
	body["id"] = "explicit"
	body["location"] = "Asia/Tokyo"
	issue("Asia/Tokyo")
}
