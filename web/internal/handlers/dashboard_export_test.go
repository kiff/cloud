package handlers

import (
	"time"

	"github.com/kiff/cloud/web/internal/pages"
)

// dashboard_export_test.go re-exports unexported helpers for the
// external _test package so tests can call the production code
// path directly. Keeps the public API surface clean while
// closing the coverage hole a review caught:
// TestOverviewNextAction_BranchesOnTenantState was previously
// re-implementing the branch table in the test and validating
// its own copy against itself, so a regression in the priority
// order in computeNextAction stayed green.
//
// File ends in _test.go so it only compiles into the test binary.

// ComputeNextAction is the test-visible alias for the unexported
// computeNextAction helper. Used by
// TestComputeNextAction_BranchesOnTenantState.
func ComputeNextAction(v pages.DashboardView) *pages.NextAction {
	return computeNextAction(v)
}

// SetNow pins the clock a page's "today" is read by, so a test's fixture
// and the page agree on which day it is whenever the test runs.
func (h *DashboardHandler) SetNow(now func() time.Time) {
	h.now = now
}
