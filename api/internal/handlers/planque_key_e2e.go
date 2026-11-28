//go:build e2e

package handlers

import "os"

// Built for the end-to-end test only (-tags e2e), never deployed: the test's
// stand-in planque signs with its own key, so the API trusts that one.
func init() {
	if k := os.Getenv("KIFF_E2E_PLANQUE_STEP_KEY"); k != "" {
		planqueStepKey = k
	}
}
