package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kiff/cloud/web/internal/handlers"
)

// These tests pin the outer-router mounting contract for the /ops
// console and guard the routing bug fixed in /: the console's POST
// mutation forms (suspend/unsuspend, plan, alert ack/resolve, support
// reply/status) must reach the gated handler. Mounting only GET under
// /ops/ makes Go's ServeMux return 405 for the POSTs (the path matches
// GET /ops/ but no pattern matches the POST method), so the mutations
// never run.
//
// Unlike the earlier version, these drive the REAL wiring —
// handlers.MountOps — the same function cmd/web calls, so a future
// refactor that drops the POST mount fails here.

// opsMountStub answers any method under /ops with a distinct code so
// the test can tell "the outer router forwarded the request" from
// "the outer router 405'd it before the handler."
func opsMountStub() http.Handler {
	inner := http.NewServeMux()
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	// The real OpsHandler.Register serves both /ops and /ops/; mirror
	// that so a bare GET /ops doesn't 307-redirect to /ops/.
	inner.HandleFunc("GET /ops", ok)
	inner.HandleFunc("GET /ops/", ok)
	inner.HandleFunc("POST /ops/tenants/{id}/suspend", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent) // 204: distinct proof the handler ran
	})
	return inner
}

func TestMountOps_ForwardsPostMutations(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	handlers.MountOps(mux, opsMountStub())

	// The mutation POST reaches the handler (204), not a 405 at the mux.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ops/tenants/x/suspend", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST /ops/tenants/x/suspend: got %d, want 204 (handler ran); a 405 means the POST mount regressed", rec.Code)
	}
}

func TestMountOps_ServesGet(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	handlers.MountOps(mux, opsMountStub())

	// GET still resolves through the same mount.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ops/support", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ops/support: got %d, want 200", rec.Code)
	}
	// And the bare /ops entrypoint.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ops", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ops: got %d, want 200", rec.Code)
	}
}

// TestMountOps_GetOnlyStillRegresses documents the exact bug: a mux
// with only the GET patterns (the earlier shape) 405s the POST. This
// is what MountOps exists to prevent, proven via the stdlib router.
func TestMountOps_GetOnlyStillRegresses(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	gated := opsMountStub()
	mux.Handle("GET /ops", gated)
	mux.Handle("GET /ops/", gated) // no POST /ops/ — the bug

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ops/tenants/x/suspend", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET-only mount: POST got %d, want 405 (documents the regression MountOps prevents)", rec.Code)
	}
}
