package handlers_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/handlers"
)

// modeRig builds a ControlsHandler with one active control on the
// "refund" domain and wraps it with the given identity, returning the
// mux and the control store so tests can assert the persisted mode.
func modeRig(t *testing.T, id auth.Identity) (http.Handler, *domain.MemoryControlStore) {
	t.Helper()
	controls := domain.NewMemoryControlStore()
	if err := controls.Create(context.Background(), domain.Control{
		ID: "ctrl-1", TenantID: id.Tenant, Domain: "refund", Entity: "Order",
		Actions: []string{"REFUND_ORDER"}, Status: "active",
	}); err != nil {
		t.Fatalf("create control: %v", err)
	}
	h := handlers.NewControlsHandler(controls, nil)
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, id))
	return wrapped, controls
}

// A management caller can flip a domain's posture; the new mode is
// persisted on the active control (the decide path reads it there).
func TestSetDomainMode_FlipsPosture(t *testing.T) {
	t.Parallel()
	owner := auth.Identity{Tenant: "t1", Subject: "u", Roles: []string{"tenant_owner"}}
	mux, controls := modeRig(t, owner)

	rec := doJSON(t, mux, http.MethodPut, "/v1/me/domains/refund/mode", `{"mode":"enforce"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	list, err := controls.List(context.Background(), "t1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Mode != domain.ControlModeEnforce {
		t.Fatalf("mode not persisted; got %+v", list)
	}

	// Flip back to shadow.
	rec = doJSON(t, mux, http.MethodPut, "/v1/me/domains/refund/mode", `{"mode":"shadow"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("shadow flip status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	list, _ = controls.List(context.Background(), "t1")
	if list[0].Mode != domain.ControlModeObserve {
		t.Fatalf("mode = %q, want shadow", list[0].Mode)
	}
}

// A runtime key (no management role) is refused: a governed agent
// cannot change the enforcement of the domain it is governed by.
func TestSetDomainMode_ForbiddenWithoutManagement(t *testing.T) {
	t.Parallel()
	runtimeKey := auth.Identity{
		Tenant: "t1", Subject: "kiff_live_t1_runtime",
		Method: auth.MethodAPIKey, APIKey: true, Roles: []string{"runtime"},
	}
	mux, controls := modeRig(t, runtimeKey)

	rec := doJSON(t, mux, http.MethodPut, "/v1/me/domains/refund/mode", `{"mode":"enforce"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	list, _ := controls.List(context.Background(), "t1")
	if list[0].Mode != "" {
		t.Fatalf("posture must not change on a refused flip; got %q", list[0].Mode)
	}
}

// An unknown domain (no active control) 404s.
func TestSetDomainMode_UnknownDomain(t *testing.T) {
	t.Parallel()
	owner := auth.Identity{Tenant: "t1", Subject: "u", Roles: []string{"tenant_owner"}}
	mux, _ := modeRig(t, owner)

	rec := doJSON(t, mux, http.MethodPut, "/v1/me/domains/does-not-exist/mode", `{"mode":"enforce"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
}

// A mode other than observe/enforce is a 400 and never touches the store.
func TestSetDomainMode_InvalidMode(t *testing.T) {
	t.Parallel()
	owner := auth.Identity{Tenant: "t1", Subject: "u", Roles: []string{"tenant_owner"}}
	mux, controls := modeRig(t, owner)

	rec := doJSON(t, mux, http.MethodPut, "/v1/me/domains/refund/mode", `{"mode":"bogus"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	list, _ := controls.List(context.Background(), "t1")
	if list[0].Mode != "" {
		t.Fatalf("invalid mode must not persist; got %q", list[0].Mode)
	}
}
