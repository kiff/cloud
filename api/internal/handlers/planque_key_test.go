//go:build !e2e

package handlers

import "testing"

// Only the end-to-end build may trust another planque key.
func TestThePlanqueKeyIsPinned(t *testing.T) {
	if planqueStepKey != pinnedPlanqueStepKey {
		t.Fatal("planque's step key isn't the pinned one")
	}
}
