package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// the multi-domain design / (replacing the trick): the cloud proposal
// counters are attributed to the request's resolved domain via the
// real domain_id dimension — the recorder is bound to that domain.
// The domain is the *server-validated* one (the active control's
// domain here), never the caller-supplied body, and entity_type is
// back to the fixed bounded label.
func TestProposals_PerDomainCounterUsesServerValidatedDomain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-pd", Status: tenant.StatusActive}
	if err := tenantStore.Create(ctx, tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}
	controls := domain.NewMemoryControlStore()
	// Active control whose Domain is the server-validated dimension.
	if err := controls.Create(ctx, domain.Control{
		ID: "c-pd", TenantID: tnt.ID, Domain: "refund-test", Entity: "Order",
		Actions: []string{"AUTO_REFUND"}, Status: "active",
	}); err != nil {
		t.Fatalf("create control: %v", err)
	}

	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)
	rec := &recordingRecorder{}
	// Capture the domain_id the recorder is bound to on each
	// incCounter call (this is where per-domain attribution now lives).
	var factoryDomains []string
	factory := func(_, domainID string) kiffruntime.MetricsRecorder {
		factoryDomains = append(factoryDomains, domainID)
		return rec
	}
	h := handlers.NewProposalsHandler(rt,
		handlers.WithProposalsMetrics(factory),
		handlers.WithProposalsControlStore(controls),
	)
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{Tenant: tnt.ID, Subject: "p"}))

	// Advance an entity to PAID so AUTO_REFUND (allowed_states=[PAID]) is allowed.
	resolved, err := rt.For(ctx, tnt.ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, kind := range []string{"ORDER_PLACED", "ORDER_PAID"} {
		raw := kiffadapter.RawInput{
			ID: kind + "-pd", Adapter: "cloud", Type: kind, Source: "test",
			ReceivedAt: time.Now().UTC(), EntityID: "o-pd", EntityType: "Order",
			ActorID: "a", Metadata: kiffevent.Metadata{TraceID: "t-pd"},
			Payload: map[string]any{},
		}
		if _, err := resolved.IngestRaw(ctx, raw); err != nil {
			t.Fatalf("ingest %s: %v", kind, err)
		}
	}

	rec2 := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
		"entity_id": "o-pd", "entity_type": "Order", "action_name": "AUTO_REFUND",
		"actor_id":   "agent",
		"parameters": map[string]any{"amount": 100, "reason": "x"},
	})
	assertOutcome(t, rec2, http.StatusOK, "allowed", nil)

	// The counters must be attributed to the domain "refund-test" via
	// the recorder's domain binding — never the body entity_type.
	sawDomain := false
	for _, d := range factoryDomains {
		if d == "refund-test" {
			sawDomain = true
		}
		if d == "Order" {
			t.Error("domain_id must NOT be the caller-supplied body entity_type")
		}
	}
	if !sawDomain {
		t.Errorf("counters not attributed to domain refund-test; factory domains = %v", factoryDomains)
	}

	// entity_type is back to the fixed bounded label (no longer
	// overloaded with the domain).
	for _, c := range rec.calls {
		if c.Name == usage.CloudCounterProposalsSubmitted && c.EntityType != usage.CloudEntityProposal {
			t.Errorf("submitted entity_type = %q, want fixed %q", c.EntityType, usage.CloudEntityProposal)
		}
	}
}

// The per-domain usage read sums only the rows attributed to the
// domain via the real domain_id dimension, leaving other
// domains' usage and pre-dimension (empty domain_id) rows out.
func TestUsage_DomainSummaryScopesToDomain(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	rig := newUsageRig(t, now)
	hour := usage.HourBucket(now)
	if err := rig.store.Flush(context.Background(), []usage.Counter{
		{TenantID: rig.tenantID, CounterName: usage.CloudCounterProposalsSubmitted, EntityType: usage.CloudEntityProposal, DomainID: "refund-test", HourBucket: hour, Count: 7},
		{TenantID: rig.tenantID, CounterName: usage.CloudCounterProposalsAllowed, EntityType: usage.CloudEntityProposal, DomainID: "refund-test", HourBucket: hour, Count: 4},
		// A different domain's usage must not bleed into the slice.
		{TenantID: rig.tenantID, CounterName: usage.CloudCounterProposalsSubmitted, EntityType: usage.CloudEntityProposal, DomainID: "other-domain", HourBucket: hour, Count: 99},
		// Pre-dimension rows (empty domain_id) must not count per-domain.
		{TenantID: rig.tenantID, CounterName: usage.CloudCounterProposalsSubmitted, EntityType: usage.CloudEntityProposal, DomainID: "", HourBucket: hour, Count: 50},
	}); err != nil {
		t.Fatalf("flush: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/me/domains/refund-test/usage", nil)
	rec := httptest.NewRecorder()
	rig.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Domain   string            `json:"domain"`
		Counters map[string]uint64 `json:"counters"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Domain != "refund-test" {
		t.Errorf("domain = %q", got.Domain)
	}
	if got.Counters[usage.CloudCounterProposalsSubmitted] != 7 {
		t.Errorf("submitted = %d, want 7 (refund-test only, not other-domain's 99 or the fixed-label 50)", got.Counters[usage.CloudCounterProposalsSubmitted])
	}
	if got.Counters[usage.CloudCounterProposalsAllowed] != 4 {
		t.Errorf("allowed = %d, want 4", got.Counters[usage.CloudCounterProposalsAllowed])
	}
}

// An unknown domain returns zeroed counters, not a 404 — honest "no
// governed operations here" without revealing whether the slug exists.
func TestUsage_DomainSummaryUnknownDomainZeroed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	rig := newUsageRig(t, now)

	req := httptest.NewRequest(http.MethodGet, "/v1/me/domains/nope/usage", nil)
	rec := httptest.NewRecorder()
	rig.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Counters map[string]uint64 `json:"counters"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Counters[usage.CloudCounterProposalsSubmitted] != 0 {
		t.Errorf("submitted = %d, want 0 for an unknown domain", got.Counters[usage.CloudCounterProposalsSubmitted])
	}
}
