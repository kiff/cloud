package receipt_test

import (
	"context"
	"errors"
	"testing"

	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/receipt"
)

// TestBuildFromRuntime_DelegatesToBuild confirms the runtime-aware
// entry point produces the same receipt as Build, given the same
// audit data on the runtime's audit store.
func TestBuildFromRuntime_DelegatesToBuild(t *testing.T) {
	t.Parallel()

	traceID := "trace-runtime-001"
	auditStore := kiffaudit.NewInMemoryStore()
	for _, rec := range fixtureRefundTrace(traceID) {
		if err := auditStore.Append(context.Background(), rec); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	rt := &kiffruntime.Runtime{Audit: auditStore}

	r, err := receipt.BuildFromRuntime(context.Background(), rt, receipt.BuildOptions{
		TenantID:      "ten-demo",
		TraceID:       traceID,
		ComplianceMap: refundDomainCompliance(),
	})
	if err != nil {
		t.Fatalf("BuildFromRuntime: %v", err)
	}

	// Sanity: the receipt has the full chain, and the canonical
	// compliance map is stamped.
	if got, want := len(r.CausalChain), 4; got != want {
		t.Errorf("CausalChain length: got %d, want %d", got, want)
	}
	if got, want := len(r.ComplianceMap), 3; got != want {
		t.Errorf("ComplianceMap length: got %d, want %d", got, want)
	}
}

func TestBuildFromRuntime_RejectsNilRuntime(t *testing.T) {
	t.Parallel()

	_, err := receipt.BuildFromRuntime(context.Background(), nil, receipt.BuildOptions{
		TenantID: "ten-demo",
		TraceID:  "trace-x",
	})
	if !errors.Is(err, receipt.ErrRuntimeMissing) {
		t.Fatalf("err: got %v, want ErrRuntimeMissing", err)
	}
}

func TestBuildFromRuntime_RejectsRuntimeWithoutAudit(t *testing.T) {
	t.Parallel()

	rt := &kiffruntime.Runtime{} // explicit Audit-less runtime
	_, err := receipt.BuildFromRuntime(context.Background(), rt, receipt.BuildOptions{
		TenantID: "ten-demo",
		TraceID:  "trace-x",
	})
	if !errors.Is(err, receipt.ErrAuditStoreMissing) {
		t.Fatalf("err: got %v, want ErrAuditStoreMissing", err)
	}
}
