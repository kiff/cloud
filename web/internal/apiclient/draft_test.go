package apiclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// TestGetDraftBearer_HappyPath confirms the success envelope
// shape: yaml + parsed (when valid) + updated_at, no issues.
func TestGetDraftBearer_HappyPath(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"GET /v1/me/domain/draft": func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "Bearer abc" {
				t.Errorf("Authorization = %q, want Bearer abc", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"yaml":       "domain: refund-flow\nentity: Order\n",
				"updated_at": int64(1716927000),
				"parsed": map[string]any{
					"domain": "refund-flow",
					"entity": "Order",
				},
			})
		},
	})
	c := newSessionClient(t, srv)
	got, err := c.GetDraftBearer(context.Background(), "abc")
	if err != nil {
		t.Fatalf("GetDraftBearer: %v", err)
	}
	if !strings.Contains(got.YAML, "refund-flow") {
		t.Errorf("YAML missing domain: %q", got.YAML)
	}
	if got.UpdatedAt != 1716927000 {
		t.Errorf("UpdatedAt = %d, want 1716927000", got.UpdatedAt)
	}
	if got.Parsed == nil || got.Parsed.Domain != "refund-flow" {
		t.Errorf("parsed.domain mismatch: %#v", got.Parsed)
	}
}

// TestGetDraftBearer_404MapsToErrDraftNotFound confirms the
// no-draft path uses the package's distinct sentinel rather than
// the legacy ErrReceiptNotFound.
func TestGetDraftBearer_404MapsToErrDraftNotFound(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"GET /v1/me/domain/draft": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		},
	})
	c := newSessionClient(t, srv)
	_, err := c.GetDraftBearer(context.Background(), "abc")
	if !errors.Is(err, apiclient.ErrDraftNotFound) {
		t.Fatalf("got %v, want ErrDraftNotFound", err)
	}
}

// TestPutDraftBearer_HappyPath confirms the YAML body forwards
// verbatim and the response Parsed shape lands.
func TestPutDraftBearer_HappyPath(t *testing.T) {
	t.Parallel()
	var captured string
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"PUT /v1/me/domain/draft": func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			captured = string(b)
			if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-yaml") {
				t.Errorf("Content-Type = %q, want application/x-yaml", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"yaml":       captured,
				"updated_at": int64(1716928000),
				"parsed":     map[string]any{"domain": "refund-flow"},
			})
		},
	})
	c := newSessionClient(t, srv)
	yaml := []byte("domain: refund-flow\nentity: Order\n")
	got, err := c.PutDraftBearer(context.Background(), "abc", yaml)
	if err != nil {
		t.Fatalf("PutDraftBearer: %v", err)
	}
	if captured != string(yaml) {
		t.Errorf("captured body = %q, want %q", captured, yaml)
	}
	if got.UpdatedAt != 1716928000 {
		t.Errorf("UpdatedAt = %d, want 1716928000", got.UpdatedAt)
	}
}

// TestPutDraftBearer_ValidationIssuesPropagate confirms the
// auto-save promise: an invalid YAML still gets a 200 with
// Issues populated; the editor can render them inline.
func TestPutDraftBearer_ValidationIssuesPropagate(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"PUT /v1/me/domain/draft": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"yaml":       "broken: yaml\n",
				"updated_at": int64(1716928100),
				"issues": []map[string]any{
					{"path": "actions[0]", "message": "executor key is required"},
				},
			})
		},
	})
	c := newSessionClient(t, srv)
	got, err := c.PutDraftBearer(context.Background(), "abc", []byte("broken: yaml\n"))
	if err != nil {
		t.Fatalf("PutDraftBearer: %v", err)
	}
	if got.Parsed != nil {
		t.Errorf("invalid YAML should not produce a parsed summary")
	}
	if len(got.Issues) != 1 || got.Issues[0].Path != "actions[0]" {
		t.Errorf("issues mismatch: %#v", got.Issues)
	}
}

// TestDeleteDraftBearer_204IsClean confirms idempotent delete.
func TestDeleteDraftBearer_204IsClean(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"DELETE /v1/me/domain/draft": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	})
	c := newSessionClient(t, srv)
	if err := c.DeleteDraftBearer(context.Background(), "abc"); err != nil {
		t.Fatalf("DeleteDraftBearer: %v", err)
	}
}

// TestDeleteDraftBearer_404IsAlsoCleanIdempotent confirms the
// no-draft path doesn't bubble an error to the caller — DELETE
// is idempotent at the API.
func TestDeleteDraftBearer_404IsAlsoCleanIdempotent(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"DELETE /v1/me/domain/draft": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		},
	})
	c := newSessionClient(t, srv)
	if err := c.DeleteDraftBearer(context.Background(), "abc"); err != nil {
		t.Fatalf("DeleteDraftBearer should be idempotent on 404: %v", err)
	}
}

// TestActivateDraftBearer_HappyPath returns the active domain
// summary on success.
func TestActivateDraftBearer_HappyPath(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"POST /v1/me/domain/draft/activate": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"domain":  "refund-flow",
				"entity":  "Order",
				"version": 7,
				"events":  []string{"X"},
				"states":  []string{"S"},
			})
		},
	})
	c := newSessionClient(t, srv)
	got, err := c.ActivateDraftBearer(context.Background(), "abc")
	if err != nil {
		t.Fatalf("ActivateDraftBearer: %v", err)
	}
	if got.Domain != "refund-flow" {
		t.Errorf("Domain = %q, want refund-flow", got.Domain)
	}
	if got.Version != 7 {
		t.Errorf("Version = %d, want 7", got.Version)
	}
}

// TestActivateDraftBearer_422ReturnsValidationError confirms
// the api's validation-failure path returns a typed error
// carrying the issue list.
func TestActivateDraftBearer_422ReturnsValidationError(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"POST /v1/me/domain/draft/activate": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": "draft fails validation; cannot activate",
				"issues": []map[string]any{
					{"path": "actions[0]", "message": "executor key is required"},
				},
			})
		},
	})
	c := newSessionClient(t, srv)
	_, err := c.ActivateDraftBearer(context.Background(), "abc")
	var verr *apiclient.DomainValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected *DomainValidationError, got %T: %v", err, err)
	}
	if len(verr.Issues) != 1 || verr.Issues[0].Path != "actions[0]" {
		t.Errorf("issues mismatch: %#v", verr.Issues)
	}
}

// TestActivateDraftBearer_404ReturnsErrDraftNotFound covers the
// "no draft exists, can't activate" path.
func TestActivateDraftBearer_404ReturnsErrDraftNotFound(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"POST /v1/me/domain/draft/activate": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		},
	})
	c := newSessionClient(t, srv)
	_, err := c.ActivateDraftBearer(context.Background(), "abc")
	if !errors.Is(err, apiclient.ErrDraftNotFound) {
		t.Fatalf("got %v, want ErrDraftNotFound", err)
	}
}
