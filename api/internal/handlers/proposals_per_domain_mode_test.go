package handlers_test

import (
	"context"
	"net/http"
	"testing"

	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// perDomainModeRig wires a decide handler with BOTH a tenant store
// (carrying the tenant-wide mode) and a control store whose active
// control carries a per-domain mode. This is the setup that proves the
// control's posture overrides the tenant default on the decide path.
type perDomainModeRig struct {
	mux      http.Handler
	rt       *cloudruntime.TenantRuntime
	tenantID string
	rec      *recordingRecorder
}

func newPerDomainModeRig(t *testing.T, tenantMode tenant.Mode, controlMode string) *perDomainModeRig {
	t.Helper()
	ctx := context.Background()
	store := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-pdm", Status: tenant.StatusActive}
	if err := store.Create(ctx, tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if tenantMode != "" {
		if err := store.SetMetadata(ctx, tnt.ID, tenant.MetadataKeyMode, string(tenantMode)); err != nil {
			t.Fatalf("set tenant mode: %v", err)
		}
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}
	controls := domain.NewMemoryControlStore()
	if err := controls.Create(ctx, domain.Control{
		ID: "ctrl-pdm", TenantID: tnt.ID, Domain: "refund-test", Entity: "Order",
		Actions: []string{"AUTO_REFUND"}, Status: "active", Mode: controlMode,
	}); err != nil {
		t.Fatalf("create control: %v", err)
	}

	resolver := cloudruntime.NewMemoryResolver(store, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)
	rec := &recordingRecorder{}
	h := handlers.NewProposalsHandler(rt,
		handlers.WithProposalsTenantStore(store),
		handlers.WithProposalsControlStore(controls),
		handlers.WithProposalsMetrics(func(_, _ string) kiffruntime.MetricsRecorder { return rec }),
	)
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{
		Tenant: tnt.ID, Subject: "subj", Roles: []string{"ops_operator"},
	}))

	rig := &perDomainModeRig{mux: wrapped, rt: rt, tenantID: tnt.ID, rec: rec}
	pr := &proposalRig{rt: rt, tenantID: tnt.ID}
	pr.seedEntityInState(t, "order-pdm")
	return rig
}

// The control's shadow posture overrides the tenant-wide enforce: the
// decision is unchanged (allowed) but reported advisory, and the
// shadowed counter fires — enforcement is decided per domain, not
// tenant-wide.
func TestProposals_ControlShadowOverridesTenantEnforce(t *testing.T) {
	t.Parallel()
	rig := newPerDomainModeRig(t, tenant.ModeEnforce, domain.ControlModeObserve)

	rec := drivePost(t, rig.mux, "/v1/proposals/decide", refundDecideBody("order-pdm", 100))
	got := decodeShadow(t, rec)
	if got.Outcome != "allowed" {
		t.Fatalf("outcome = %q, want allowed", got.Outcome)
	}
	if got.Mode != string(tenant.ModeObserve) {
		t.Fatalf("mode = %q, want shadow (control posture must win)", got.Mode)
	}
	if !got.Advisory {
		t.Fatal("shadow control posture must mark the decision advisory")
	}
	if n := countByName(rig.rec.calls, usage.CloudCounterProposalsShadowed); n != 1 {
		t.Fatalf("shadowed counter fired %d times, want 1", n)
	}
}

// The reverse: the control's enforce posture overrides a tenant-wide
// shadow default — the domain binds even though the workspace is
// otherwise observing.
func TestProposals_ControlEnforceOverridesTenantShadow(t *testing.T) {
	t.Parallel()
	rig := newPerDomainModeRig(t, tenant.ModeObserve, domain.ControlModeEnforce)

	rec := drivePost(t, rig.mux, "/v1/proposals/decide", refundDecideBody("order-pdm", 100))
	got := decodeShadow(t, rec)
	if got.Mode != string(tenant.ModeEnforce) {
		t.Fatalf("mode = %q, want enforce (control posture must win)", got.Mode)
	}
	if got.Advisory {
		t.Fatal("enforce control posture must not mark the decision advisory")
	}
	if n := countByName(rig.rec.calls, usage.CloudCounterProposalsShadowed); n != 0 {
		t.Fatalf("shadowed counter fired %d times under enforce, want 0", n)
	}
}

// An unset control mode falls back to the tenant-wide mode — the
// back-compat path for controls created before per-domain posture.
func TestProposals_UnsetControlModeFallsBackToTenant(t *testing.T) {
	t.Parallel()
	rig := newPerDomainModeRig(t, tenant.ModeObserve, "")

	rec := drivePost(t, rig.mux, "/v1/proposals/decide", refundDecideBody("order-pdm", 100))
	got := decodeShadow(t, rec)
	if got.Mode != string(tenant.ModeObserve) {
		t.Fatalf("mode = %q, want shadow (fallback to tenant mode)", got.Mode)
	}
	if !got.Advisory {
		t.Fatal("unset control mode should inherit the tenant's shadow (advisory)")
	}
}
