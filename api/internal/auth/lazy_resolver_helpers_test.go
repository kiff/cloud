package auth_test

import "time"

// timeNow is a fixed clock for deterministic test fixtures. The
// LazyResolver tests don't depend on the value beyond non-zero.
func timeNow() time.Time {
	return time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC)
}
