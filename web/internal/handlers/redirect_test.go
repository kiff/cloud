package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRedirect asserts the retired-URL handler answers 301 with the
// right Location, locking the consolidation contract: /solutions →
// /#use-cases, /how-it-works → /#how.
func TestRedirect(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"/#use-cases", "/#how"} {
		req := httptest.NewRequest(http.MethodGet, "/retired", nil)
		rec := httptest.NewRecorder()

		Redirect(target).ServeHTTP(rec, req)

		if rec.Code != http.StatusMovedPermanently {
			t.Errorf("target %q: status got %d, want 301", target, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != target {
			t.Errorf("target %q: Location got %q, want %q", target, got, target)
		}
	}
}
