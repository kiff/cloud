package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// shadowRig wires a proposal handler with a tenant store (so the
// handler can read the tenant's mode) and a recording metrics
// recorder (so the shadowed counter is observable). The tenant's
// mode is set via tenant metadata, exactly as the operator CLI
// does, so the test exercises the real read path.
type shadowRig struct {
	mux      http.Handler
	rt       *cloudruntime.TenantRuntime
	tenantID string
	store    tenant.Store
	rec      *recordingRecorder
}

func newShadowRig(t *testing.T, mode tenant.Mode) *shadowRig {
	t.Helper()
	store := tenant.NewMemoryStore()
	tnt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "tenant-shadow",
		DisplayName: "tenant shadow",
		Status:      tenant.StatusActive,
	}
	if err := store.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	// Set the mode the same way the operator CLI does: server-side
	// metadata. An empty mode means "leave default (enforce)".
	if mode != "" {
		if err := store.SetMetadata(context.Background(), tnt.ID, tenant.MetadataKeyMode, string(mode)); err != nil {
			t.Fatalf("set mode: %v", err)
		}
	}

	src := domain.NewMemorySource()
	if err := src.Save(context.Background(), tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}
	resolver := cloudruntime.NewMemoryResolver(store, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)

	rec := &recordingRecorder{}
	h := handlers.NewProposalsHandler(rt,
		handlers.WithProposalsTenantStore(store),
		handlers.WithProposalsMetrics(func(_, _ string) kiffruntime.MetricsRecorder { return rec }),
	)
	innerMux := http.NewServeMux()
	h.Register(innerMux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{
		Tenant:  tnt.ID,
		Subject: "shadow-subject",
		Roles:   []string{"ops_operator"},
	}))

	rig := &shadowRig{mux: wrapped, rt: rt, tenantID: tnt.ID, store: store, rec: rec}
	rig.seedPaid(t, "order-shadow")
	return rig
}

func (rig *shadowRig) seedPaid(t *testing.T, orderID string) {
	t.Helper()
	pr := &proposalRig{rt: rig.rt, tenantID: rig.tenantID}
	pr.seedEntityInState(t, orderID)
}

type shadowResp struct {
	Outcome  string `json:"outcome"`
	Mode     string `json:"mode"`
	Advisory bool   `json:"advisory"`
}

func decodeShadow(t *testing.T, rec *httptest.ResponseRecorder) shadowResp {
	t.Helper()
	var out shadowResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

func refundDecideBody(orderID string, amount int) map[string]any {
	return map[string]any{
		"entity_id":   orderID,
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent-refunds",
		"parameters":  map[string]any{"amount": amount, "reason": "x"},
	}
}

// ─── tests ────────────────────────────────────────────────────

// Default mode (no metadata set) is enforce: the decision is
// binding, no advisory flag, and the shadowed counter never fires.
func TestProposals_DefaultModeIsEnforce(t *testing.T) {
	t.Parallel()
	rig := newShadowRig(t, "") // no mode set → default enforce

	rec := drivePost(t, rig.mux, "/v1/proposals/decide", refundDecideBody("order-shadow", 100))
	got := decodeShadow(t, rec)

	if got.Outcome != "allowed" {
		t.Fatalf("outcome = %q, want allowed", got.Outcome)
	}
	if got.Mode != string(tenant.ModeEnforce) {
		t.Fatalf("mode = %q, want enforce", got.Mode)
	}
	if got.Advisory {
		t.Fatalf("enforce mode must not mark the decision advisory")
	}
	if n := countByName(rig.rec.calls, usage.CloudCounterProposalsShadowed); n != 0 {
		t.Fatalf("shadowed counter = %d, want 0 in enforce mode", n)
	}
}

// Shadow mode returns the same outcome but marks it advisory and
// fires the shadowed counter. The decision is unchanged — shadow
// is a contract annotation, not a different answer.
func TestProposals_ShadowModeMarksAdvisory(t *testing.T) {
	t.Parallel()
	rig := newShadowRig(t, tenant.ModeObserve)

	rec := drivePost(t, rig.mux, "/v1/proposals/decide", refundDecideBody("order-shadow", 100))
	got := decodeShadow(t, rec)

	if got.Outcome != "allowed" {
		t.Fatalf("outcome = %q, want allowed (shadow must not change the decision)", got.Outcome)
	}
	if got.Mode != string(tenant.ModeObserve) {
		t.Fatalf("mode = %q, want shadow", got.Mode)
	}
	if !got.Advisory {
		t.Fatalf("shadow mode must mark the decision advisory")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (shadow does not change status)", rec.Code)
	}
	if n := countByName(rig.rec.calls, usage.CloudCounterProposalsShadowed); n != 1 {
		t.Fatalf("shadowed counter = %d, want 1", n)
	}
}

// A blocked decision under shadow still reports blocked (so the
// dashboard can show what enforcing would have stopped) but is
// advisory and counted as shadowed.
func TestProposals_ShadowModeBlockedStillAdvisory(t *testing.T) {
	t.Parallel()
	rig := newShadowRig(t, tenant.ModeObserve)

	// order-unseen was never put into PAID, so AUTO_REFUND is
	// state-not-allowed → blocked.
	rec := drivePost(t, rig.mux, "/v1/proposals/decide", refundDecideBody("order-unseen", 100))
	got := decodeShadow(t, rec)

	if got.Outcome != "blocked" {
		t.Fatalf("outcome = %q, want blocked", got.Outcome)
	}
	if !got.Advisory {
		t.Fatalf("shadow mode must mark a blocked decision advisory too")
	}
	if n := countByName(rig.rec.calls, usage.CloudCounterProposalsShadowed); n != 1 {
		t.Fatalf("shadowed counter = %d, want 1 (blocked counts toward the readiness signal)", n)
	}
}

// An invalid proposal under shadow carries the mode label but is
// NOT marked advisory or counted as shadowed: a malformed request
// is not a governed decision and would pollute the readiness signal.
func TestProposals_ShadowModeInvalidNotAdvisory(t *testing.T) {
	t.Parallel()
	rig := newShadowRig(t, tenant.ModeObserve)

	// Missing action_name → invalid shape.
	body := map[string]any{
		"entity_id":   "order-shadow",
		"entity_type": "Order",
		"actor_id":    "agent-refunds",
	}
	rec := drivePost(t, rig.mux, "/v1/proposals/decide", body)
	got := decodeShadow(t, rec)

	if got.Outcome != "invalid" {
		t.Fatalf("outcome = %q, want invalid", got.Outcome)
	}
	if got.Mode != string(tenant.ModeObserve) {
		t.Fatalf("mode = %q, want shadow (label still present on invalid)", got.Mode)
	}
	if got.Advisory {
		t.Fatalf("invalid must not be marked advisory")
	}
	if n := countByName(rig.rec.calls, usage.CloudCounterProposalsShadowed); n != 0 {
		t.Fatalf("shadowed counter = %d, want 0 for invalid", n)
	}
}
