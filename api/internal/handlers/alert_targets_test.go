package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
)

func newAlertTargetsMux(t *testing.T, store alert.Store) http.Handler {
	t.Helper()
	// allowPrivate=true here so the create/list/delete test isn't
	// coupled to DNS; the SSRF guard itself is covered by
	// TestAlertTargets_RejectsPrivateURLWhenGuarded and the alert
	// package's ssrf_test.go.
	return newAlertTargetsMuxMode(t, store, true)
}

func newAlertTargetsMuxMode(t *testing.T, store alert.Store, allowPrivate bool) http.Handler {
	t.Helper()
	h := handlers.NewAlertTargetsHandler(store, allowPrivate)
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{Tenant: "tenant-alerts", Subject: "alice"}))
	return wrapped
}

func TestAlertTargets_CreateListDelete(t *testing.T) {
	t.Parallel()
	store := alert.NewMemoryStore()
	mux := newAlertTargetsMux(t, store)

	// Create a valid target.
	body := `{"url":"https://hooks.example.com/kiff","outcomes":["approval_required","blocked","card_warning"]}`
	rec := drivePost(t, mux, "/v1/me/alert-targets", json.RawMessage(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d: %s", rec.Code, rec.Body.String())
	}
	var created alert.Target
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.ID == "" || !strings.HasPrefix(created.ID, "alrt_") {
		t.Fatalf("expected generated id; got %q", created.ID)
	}
	if !created.Enabled {
		t.Errorf("target should default enabled")
	}

	// List includes it.
	listRec := driveGet(t, mux, "/v1/me/alert-targets")
	if listRec.Code != http.StatusOK {
		t.Fatalf("list: status %d", listRec.Code)
	}
	var listResp struct {
		Targets []alert.Target `json:"targets"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Targets) != 1 || listResp.Targets[0].ID != created.ID {
		t.Fatalf("list should contain the created target; got %+v", listResp.Targets)
	}

	// Delete it.
	delReq := httptest.NewRequest(http.MethodDelete, "/v1/me/alert-targets/"+created.ID, nil)
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d", delRec.Code)
	}

	// Gone from the list.
	after, _ := store.ListTargets(context.Background(), "tenant-alerts")
	if len(after) != 0 {
		t.Fatalf("target should be deleted; got %d", len(after))
	}
}

func TestAlertTargets_RejectsBadURL(t *testing.T) {
	t.Parallel()
	mux := newAlertTargetsMux(t, alert.NewMemoryStore())

	for _, body := range []string{
		`{"url":""}`,
		`{"url":"ftp://nope"}`,
		`{"url":"https://h","outcomes":["nonsense"]}`,
	} {
		rec := drivePost(t, mux, "/v1/me/alert-targets", json.RawMessage(body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status %d want 400 (%s)", body, rec.Code, rec.Body.String())
		}
	}
}

func driveGet(t *testing.T, mux http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// When the SSRF guard is active (production), registration rejects a
// webhook pointed at a private/metadata address. IP literals so the
// check needs no DNS.
func TestAlertTargets_RejectsPrivateURLWhenGuarded(t *testing.T) {
	t.Parallel()
	mux := newAlertTargetsMuxMode(t, alert.NewMemoryStore(), false)
	for _, body := range []string{
		`{"url":"http://169.254.169.254/latest/meta-data/"}`, // cloud metadata
		`{"url":"http://127.0.0.1:9000/hook"}`,               // loopback
		`{"url":"http://10.0.0.5/hook"}`,                     // RFC 1918
	} {
		rec := drivePost(t, mux, "/v1/me/alert-targets", json.RawMessage(body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status %d want 400 (%s)", body, rec.Code, rec.Body.String())
		}
	}
}
