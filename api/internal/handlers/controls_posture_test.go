package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/posture"
	"github.com/kiff/cloud/api/internal/tenant"
)

type controlPostureResp struct {
	Control struct {
		Mode    string `json:"mode"`
		Posture *struct {
			Current         string     `json:"current"`
			BecameEnforceAt *time.Time `json:"became_enforce_at"`
			BecameEnforceBy string     `json:"became_enforce_by"`
			LastChangedAt   *time.Time `json:"last_changed_at"`
		} `json:"posture"`
	} `json:"control"`
}

func postureRig(t *testing.T, mode string, pr posture.Reader) (http.Handler, string) {
	t.Helper()
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{
		ID: tenant.NewID(), Slug: "cp", Status: tenant.StatusActive,
		Metadata: map[string]any{tenant.MetadataKeyMode: mode},
	}
	if err := tenants.Create(ctx, tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	controls := domain.NewMemoryControlStore()
	if err := controls.Create(ctx, domain.Control{
		ID: "ctrl-1", TenantID: tnt.ID, Domain: "refund", Entity: "Order",
		Actions: []string{"REFUND_ORDER"}, Status: "active",
	}); err != nil {
		t.Fatalf("create control: %v", err)
	}
	opts := []handlers.ControlsOption{handlers.WithControlsTenantStore(tenants)}
	if pr != nil {
		opts = append(opts, handlers.WithControlsPostureReader(pr))
	}
	h := handlers.NewControlsHandler(controls, nil, opts...)
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{Tenant: tnt.ID, Subject: "u"}))
	return wrapped, tnt.ID
}

// With a posture reader wired, the control header carries the posture
// block: current mode, when it became enforce and by whom, and the
// last change.
func TestControlDecisions_SurfacesPostureHistory(t *testing.T) {
	t.Parallel()
	pr := posture.NewMemoryReader()
	t0 := time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC)
	enforceAt := t0.Add(time.Hour)

	mux, tenantID := postureRig(t, "enforce", pr)
	pr.Append(tenantID, posture.Transition{From: "enforce", To: "shadow", ChangedBy: "ops-alice", ChangedAt: t0})
	pr.Append(tenantID, posture.Transition{From: "shadow", To: "enforce", ChangedBy: "ops-bob", ChangedAt: enforceAt})

	rec := getJSON(t, mux, "/v1/me/controls/ctrl-1/decisions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp controlPostureResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Control.Posture == nil {
		t.Fatal("posture block missing when reader is wired")
	}
	p := resp.Control.Posture
	if p.Current != "enforce" {
		t.Errorf("current = %q, want enforce", p.Current)
	}
	if p.BecameEnforceBy != "ops-bob" {
		t.Errorf("became_enforce_by = %q, want ops-bob", p.BecameEnforceBy)
	}
	if p.BecameEnforceAt == nil || !p.BecameEnforceAt.Equal(enforceAt) {
		t.Errorf("became_enforce_at = %v, want %v", p.BecameEnforceAt, enforceAt)
	}
	if p.LastChangedAt == nil || !p.LastChangedAt.Equal(enforceAt) {
		t.Errorf("last_changed_at = %v, want %v (most recent change)", p.LastChangedAt, enforceAt)
	}
}

// A tenant that has only ever been in shadow has a posture block with
// current=shadow and no became_enforce_* (never flipped to enforce).
func TestControlDecisions_PostureNeverEnforced(t *testing.T) {
	t.Parallel()
	pr := posture.NewMemoryReader()
	mux, tenantID := postureRig(t, "shadow", pr)
	pr.Append(tenantID, posture.Transition{From: "enforce", To: "shadow", ChangedBy: "ops", ChangedAt: time.Now().UTC()})

	rec := getJSON(t, mux, "/v1/me/controls/ctrl-1/decisions")
	var resp controlPostureResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Control.Posture == nil {
		t.Fatal("posture block missing")
	}
	if resp.Control.Posture.Current != "observe" {
		t.Errorf("current = %q, want observe", resp.Control.Posture.Current)
	}
	if resp.Control.Posture.BecameEnforceAt != nil {
		t.Errorf("became_enforce_at = %v, want nil (never enforced)", resp.Control.Posture.BecameEnforceAt)
	}
}

// Without a posture reader wired, the posture block is omitted — the
// contract's "not yet recorded" state is preserved.
func TestControlDecisions_PostureOmittedWithoutReader(t *testing.T) {
	t.Parallel()
	mux, _ := postureRig(t, "enforce", nil)
	rec := getJSON(t, mux, "/v1/me/controls/ctrl-1/decisions")
	var resp controlPostureResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Control.Posture != nil {
		t.Errorf("posture block present without a reader: %+v", resp.Control.Posture)
	}
}

// errPostureReader always fails, to prove a posture read hiccup does not
// take down the decisions endpoint (the primary payload).
type errPostureReader struct{}

func (errPostureReader) Transitions(context.Context, string) ([]posture.Transition, error) {
	return nil, errPostureBoom
}

var errPostureBoom = errors.New("posture store unavailable")

// A posture read error must NOT fail the decisions endpoint: it still
// 200s with decisions present and the posture block omitted (restores
// the prior best-effort contract through the composer).
func TestControlDecisions_PostureReadErrorStillServesDecisions(t *testing.T) {
	t.Parallel()
	mux, _ := postureRig(t, "enforce", errPostureReader{})
	rec := getJSON(t, mux, "/v1/me/controls/ctrl-1/decisions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s (a posture error must not 500 the decisions read)", rec.Code, rec.Body.String())
	}
	var resp controlPostureResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Control.Posture != nil {
		t.Errorf("posture block should be omitted on a read error, got %+v", resp.Control.Posture)
	}
}
