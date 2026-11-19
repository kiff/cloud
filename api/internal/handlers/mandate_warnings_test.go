package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
)

func TestCardWarningSettingsAdminOnlyAndSeparateFromAuthority(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	m := mandate.Mandate{ID: "notice-card", TenantID: "t1", Subject: "claude", Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Window: mandate.WindowCalendarDay, Limit: 10}}}
	if err := store.Issue(ctx, m, "admin"); err != nil {
		t.Fatal(err)
	}
	settings := mandate.DefaultWarningSettings()
	settings.Runway = false
	request := map[string]any{"revision": 0, "settings": settings}
	for _, id := range []auth.Identity{runtimeKey(), agentKey("claude", ""), {Tenant: "t1", Subject: "editor", MemberRole: "editor"}, {Tenant: "t1", Subject: "viewer", MemberRole: "viewer"}, {Tenant: "t1", Subject: "runtime", AgentID: "claude", MemberRole: "admin", APIKey: true}} {
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			rr := do(t, mux, id, method, "/v1/me/mandates/notice-card/warnings", request)
			if rr.Code != 403 {
				t.Fatalf("%s %s: %d %s", id.Subject, method, rr.Code, rr.Body.String())
			}
		}
	}
	admin := auth.Identity{Tenant: "t1", Subject: "admin", MemberRole: "admin"}
	if rr := do(t, mux, admin, http.MethodPut, "/v1/me/mandates/notice-card/warnings", request); rr.Code != 200 {
		t.Fatalf("save: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, mux, admin, http.MethodPut, "/v1/me/mandates/notice-card/warnings", request); rr.Code != 409 {
		t.Fatalf("stale revision: %d", rr.Code)
	}
	if rr := do(t, mux, auth.Identity{Tenant: "different", Subject: "admin", MemberRole: "admin"}, http.MethodGet, "/v1/me/mandates/notice-card/warnings", nil); rr.Code != 404 {
		t.Fatalf("tenant leak: %d", rr.Code)
	}
	revisions, err := store.Revisions(ctx, "t1", m.ID)
	if err != nil || len(revisions) != 1 {
		t.Fatalf("notice changed authority: %+v %v", revisions, err)
	}
	rr := do(t, mux, admin, http.MethodGet, "/v1/me/mandates/notice-card/warnings", nil)
	var config mandate.WarningConfig
	if err = json.Unmarshal(rr.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	if config.Revision != 1 || config.Settings.Runway || config.By != "admin" {
		t.Fatalf("%+v", config)
	}
}
