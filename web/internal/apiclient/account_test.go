package apiclient_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// TestExportBearer_ReturnsFullBodyOverCap verifies the export path
// does not truncate at the shared do() helper's 1MB cap — a real
// export can exceed it, and a clipped body is corrupt JSON.
func TestExportBearer_ReturnsFullBodyOverCap(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("x", (2<<20)+13) // > 1MB
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"GET /v1/me/export": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(big))
		},
	})
	c := newSessionClient(t, srv)

	body, err := c.ExportBearer(context.Background(), "abc")
	if err != nil {
		t.Fatalf("ExportBearer: %v", err)
	}
	if len(body) != len(big) {
		t.Fatalf("body length = %d, want %d (export must not be capped)", len(body), len(big))
	}
}

func TestExportBearer_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"GET /v1/me/export": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	c := newSessionClient(t, srv)
	if _, err := c.ExportBearer(context.Background(), "abc"); !errors.Is(err, apiclient.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestDeleteAccountBearer_HappyPath(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"POST /v1/me/delete": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"deleted_pending","deleted_at":"2026-06-05T00:00:00Z","grace_window_ends":"2026-07-05T00:00:00Z","message":"scheduled"}`))
		},
	})
	c := newSessionClient(t, srv)
	resp, err := c.DeleteAccountBearer(context.Background(), "abc")
	if err != nil {
		t.Fatalf("DeleteAccountBearer: %v", err)
	}
	if resp.Status != "deleted_pending" {
		t.Errorf("status = %q, want deleted_pending", resp.Status)
	}
	if resp.GraceWindowEnds == "" {
		t.Errorf("expected grace_window_ends in response")
	}
}

func TestDeleteAccountBearer_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"POST /v1/me/delete": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	c := newSessionClient(t, srv)
	if _, err := c.DeleteAccountBearer(context.Background(), "abc"); !errors.Is(err, apiclient.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}
