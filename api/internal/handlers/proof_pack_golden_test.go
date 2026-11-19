package handlers_test

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite the proof-pack golden file")

// TestProofPack_GoldenByteForByte pins the kiff.proofpack.v1 artifact
// byte-for-byte for a fixed (tenant, action, window, clock). It guards
// the public contract: the evidence-composer refactor must not
// move a single byte of the exported pack. A fixed clock makes
// generated_at deterministic; an explicit ?from/?to window removes the
// time.Now dependency from the rest of the pack.
//
//	Regenerate intentionally with: go test ./internal/handlers/ \
//	  -run TestProofPack_GoldenByteForByte -update-golden
func TestProofPack_GoldenByteForByte(t *testing.T) {
	ctx := context.Background()

	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: "01HZZZZZZZZZZZZZZZZZZZZZZA", Slug: "golden", DisplayName: "Golden Co", Status: tenant.StatusActive}
	if err := tenants.Create(ctx, tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, []byte(proofPackYAML)); err != nil {
		t.Fatalf("save yaml: %v", err)
	}
	controls := domain.NewMemoryControlStore()
	if err := controls.Create(ctx, domain.Control{
		ID: "ctrl-golden", TenantID: tnt.ID, Domain: "refund-test", Entity: "Order",
		Actions: []string{"REFUND_ORDER", "AUTO_REFUND"}, Environment: "dev", Workflow: "default",
		Version: 1, ActivatedBy: "alice",
		ActivatedAt: time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("create control: %v", err)
	}

	reader := proposals.NewMemoryReader()
	base := time.Date(2026, time.May, 20, 12, 0, 0, 0, time.UTC)
	reader.Append(tnt.ID, proposals.DecisionRow{ProposalID: "p1", EntityID: "o1", EntityType: "Order", ActionName: "REFUND_ORDER", Outcome: "allowed", CreatedAt: base})
	reader.Append(tnt.ID, proposals.DecisionRow{ProposalID: "p2", EntityID: "o2", EntityType: "Order", ActionName: "REFUND_ORDER", Outcome: "blocked", CreatedAt: base.Add(time.Hour)})

	recs := receipt.NewMemoryStore()
	if err := recs.Save(ctx, receipt.Receipt{
		SchemaVersion: receipt.SchemaVersion, TenantID: tnt.ID, TraceID: "tr1", EntityID: "o1",
		ActionName: "REFUND_ORDER", IssuedAt: base.Add(2 * time.Hour),
		Attestation: &receipt.Attestation{Algorithm: receipt.AttestationAlgorithm, Signature: "0xsig", WalletAddress: "0xwallet"},
	}); err != nil {
		t.Fatalf("save receipt: %v", err)
	}

	clock := func() time.Time { return time.Date(2026, time.May, 25, 9, 0, 0, 0, time.UTC) }
	h := handlers.NewProofPackHandler(tenants, controls, src, executors.DefaultRegistry(), reader, recs,
		handlers.WithProofPackClock(clock))
	inner := http.NewServeMux()
	h.Register(inner)
	mux := http.NewServeMux()
	mux.Handle("/", withFakeIdentity(inner, auth.Identity{Tenant: tnt.ID, Subject: "alice"}))

	get := func() []byte {
		req := httptest.NewRequest(http.MethodGet,
			"/v1/me/proof-pack/REFUND_ORDER?from=2026-05-19T00:00:00Z&to=2026-05-24T00:00:00Z", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.Bytes()
	}

	got := get()

	// Determinism: a second identical request yields identical bytes
	// (catches a non-fixed clock or unstable ordering).
	if got2 := get(); string(got2) != string(got) {
		t.Fatal("proof pack is not byte-stable across identical requests")
	}

	goldenPath := filepath.Join("testdata", "proofpack_golden.json")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote golden %s (%d bytes)", goldenPath, len(got))
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update-golden to create): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("proof pack drifted from golden.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
