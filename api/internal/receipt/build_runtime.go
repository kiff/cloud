package receipt

import (
	"context"

	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"
)

// BuildFromRuntime is the runtime-aware variant of Build. It pulls
// the audit store off rt and delegates to Build.
//
// Handlers should prefer this over the bare Build entry point
// because the cloud-side request flow already resolves a
// per-tenant *kiffruntime.Runtime via TenantRuntime.For; calling
// BuildFromRuntime keeps the audit-store source-of-truth on the
// runtime instead of forcing the handler to reach into framework
// internals.
//
// rt must be non-nil and must have a non-nil Audit store. The
// runtime's defaults guarantee an Audit store exists, but this
// function still treats the field as a contract: nil is an
// explicit programmer error and surfaces ErrAuditStoreMissing
// rather than panicking.
func BuildFromRuntime(ctx context.Context, rt *kiffruntime.Runtime, opts BuildOptions) (Receipt, error) {
	if rt == nil {
		return Receipt{}, ErrRuntimeMissing
	}
	if rt.Audit == nil {
		return Receipt{}, ErrAuditStoreMissing
	}
	return Build(ctx, rt.Audit, opts)
}
