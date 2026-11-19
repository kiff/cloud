package runtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kiff/cloud/api/internal/domain"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
)

// manifestYAML is a tiny domain that parses against the default
// executor registry. Tests reuse it whenever they need a YAML
// the manifest resolver can load.
const manifestYAML = `
domain: manifest-test
entity: TestEntity
events:
  - CREATED
states:
  - NEW
transitions:
  - on: CREATED
    from: ""
    to: NEW
actions:
  - name: NOOP
    allowed_states: [NEW]
    risk: low
    approval: never
    executor: cafe.start_shift
`

// newManifestRig seeds an in-memory tenant store + domain source
// with one active tenant carrying the supplied metadata, then
// returns a ManifestResolver wired against them. Tests can
// optionally pass the metadata to drive plan resolution.
func newManifestRig(t *testing.T, status tenant.Status, meta map[string]any) (cloudruntime.ManifestResolver, *tenant.MemoryStore, *domain.MemorySource, string) {
	t.Helper()
	store := tenant.NewMemoryStore()
	src := domain.NewMemorySource()

	id := tenant.NewID()
	if err := store.Create(context.Background(), tenant.Tenant{
		ID:       id,
		Slug:     "tenant-manifest",
		Status:   status,
		Metadata: meta,
	}); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if status == tenant.StatusActive {
		if err := src.Save(context.Background(), id, []byte(manifestYAML)); err != nil {
			t.Fatalf("seed yaml: %v", err)
		}
	}
	return cloudruntime.NewManifestResolver(store, src), store, src, id
}

func TestManifest_ActiveTenantReturnsTypedFields(t *testing.T) {
	t.Parallel()
	rig, _, _, id := newManifestRig(t, tenant.StatusActive, nil)

	m, err := rig.ResolveManifest(context.Background(), id, "")
	if err != nil {
		t.Fatalf("ResolveManifest: %v", err)
	}
	if m.TenantID != id {
		t.Errorf("TenantID = %q, want %q", m.TenantID, id)
	}
	if m.TenantStatus != tenant.StatusActive {
		t.Errorf("TenantStatus = %q, want active", m.TenantStatus)
	}
	if m.SchemaName != tenant.SchemaNameFor(id) {
		t.Errorf("SchemaName = %q, want %q", m.SchemaName, tenant.SchemaNameFor(id))
	}
	if m.DomainVersion != 1 {
		t.Errorf("DomainVersion = %d, want 1", m.DomainVersion)
	}
	if m.Plan != tenant.PlanTrial {
		t.Errorf("Plan = %v, want trial", m.Plan)
	}
	if m.MonthlyProposalLimit != 5000 {
		t.Errorf("MonthlyProposalLimit = %d, want the trial's 5000 checks", m.MonthlyProposalLimit)
	}
	if m.Features == nil {
		t.Errorf("Features must be non-nil even when empty")
	}
}

func TestManifest_ReusesPlanForFromTenantPackage(t *testing.T) {
	t.Parallel()
	// tenant.PlanFor is the plan's source; the manifest must not reimplement plan
	// resolution. This proves the manifest reads "solo" via the tenant
	// package and surfaces its limit (paid plans don't bound checks).
	rig, _, _, id := newManifestRig(t, tenant.StatusActive, map[string]any{
		tenant.MetadataKeyPlan: "solo",
	})

	m, err := rig.ResolveManifest(context.Background(), id, "")
	if err != nil {
		t.Fatalf("ResolveManifest: %v", err)
	}
	if m.Plan != tenant.PlanSolo {
		t.Errorf("Plan = %v, want solo", m.Plan)
	}
	if m.MonthlyProposalLimit != 0 {
		t.Errorf("MonthlyProposalLimit = %d, want 0 (unbounded)", m.MonthlyProposalLimit)
	}
}

func TestManifest_InactiveTenantReturnsTypedError(t *testing.T) {
	t.Parallel()
	rig, _, _, id := newManifestRig(t, tenant.StatusSuspended, nil)

	m, err := rig.ResolveManifest(context.Background(), id, "")
	if err == nil {
		t.Fatalf("expected error for inactive tenant")
	}
	if !errors.Is(err, cloudruntime.ErrManifestTenantInactive) {
		t.Errorf("err = %v, want ErrManifestTenantInactive", err)
	}
	// The (zero) manifest still echoes status + schema so a
	// caller can render an inactive-tenant banner without a
	// second store lookup.
	if m.TenantStatus != tenant.StatusSuspended {
		t.Errorf("TenantStatus = %q, want suspended", m.TenantStatus)
	}
	if m.SchemaName == "" {
		t.Errorf("SchemaName should be populated even for inactive tenants")
	}
}

func TestManifest_MissingDomainBubblesUp(t *testing.T) {
	t.Parallel()
	// Active tenant but no domain seeded. Must error so the
	// resolver never builds a runtime over a missing config.
	store := tenant.NewMemoryStore()
	src := domain.NewMemorySource()
	id := tenant.NewID()
	if err := store.Create(context.Background(), tenant.Tenant{
		ID: id, Slug: "tenant-no-domain", Status: tenant.StatusActive,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rig := cloudruntime.NewManifestResolver(store, src)

	_, err := rig.ResolveManifest(context.Background(), id, "")
	if err == nil {
		t.Fatalf("expected error for missing domain")
	}
	// The manifest layer wraps with its own context but should
	// preserve the underlying ErrDomainNotFound for callers that
	// want to branch on it.
	if !errors.Is(err, domain.ErrDomainNotFound) {
		t.Errorf("err = %v, want wrap of ErrDomainNotFound", err)
	}
}

func TestManifest_UnknownTenantReturnsErrNotFound(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemoryStore()
	src := domain.NewMemorySource()
	rig := cloudruntime.NewManifestResolver(store, src)

	_, err := rig.ResolveManifest(context.Background(), tenant.NewID(), "")
	if err == nil {
		t.Fatalf("expected error for unknown tenant")
	}
	if !errors.Is(err, tenant.ErrNotFound) {
		t.Errorf("err = %v, want wrap of tenant.ErrNotFound", err)
	}
}

func TestManifest_UnknownMetadataIsIgnored(t *testing.T) {
	t.Parallel()
	// Acceptance criterion: "Unknown tenant metadata keys are
	// ignored." This proves a stray key does not change the
	// resolved plan or limit, and a typo on the plan key
	// collapses to the trial (the tenant.PlanFor behavior).
	rig, _, _, id := newManifestRig(t, tenant.StatusActive, map[string]any{
		tenant.MetadataKeyPlan: "enterprise_unlimited", // typo / unknown plan
		"billing_owner":        "ops@example.com",      // unknown key
		"feature_flags":        []any{"foo"},           // wrong shape; ignored
	})
	m, err := rig.ResolveManifest(context.Background(), id, "")
	if err != nil {
		t.Fatalf("ResolveManifest: %v", err)
	}
	if m.Plan != tenant.PlanTrial {
		t.Errorf("typo plan must collapse to the trial; got %v", m.Plan)
	}
	if m.MonthlyProposalLimit != 5000 {
		t.Errorf("limit = %d, want the trial's 5000", m.MonthlyProposalLimit)
	}
}

// stubManifest lets the resolver tests below drive specific
// manifest shapes without touching the real stores.
type stubManifest struct {
	manifest cloudruntime.RuntimeManifest
	err      error
}

func (s *stubManifest) ResolveManifest(_ context.Context, _ string, _ string) (cloudruntime.RuntimeManifest, error) {
	return s.manifest, s.err
}

// TestCloudResolver_RefusesNonActiveManifest documents the
// defence-in-depth check inside CloudResolver.Resolve: even if a
// custom ManifestResolver returns a non-active status without an
// error, the resolver must still refuse to build a runtime.
func TestCloudResolver_RefusesNonActiveManifest(t *testing.T) {
	t.Parallel()
	stub := &stubManifest{
		manifest: cloudruntime.RuntimeManifest{
			TenantID:     tenant.NewID(),
			TenantStatus: tenant.StatusSuspended,
			SchemaName:   "kiff_t_suspended",
		},
	}
	store := tenant.NewMemoryStore()
	src := domain.NewMemorySource()
	resolver := cloudruntime.NewCloudResolverWithManifest(
		"", store, src, nil, nil, stub,
	)
	_, err := resolver.Resolve(context.Background(), stub.manifest.TenantID, "")
	if err == nil {
		t.Fatalf("expected error for non-active manifest")
	}
	if !errors.Is(err, cloudruntime.ErrManifestTenantInactive) {
		t.Errorf("err = %v, want ErrManifestTenantInactive", err)
	}
}
