package kiffapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kiff/cloud/gateway/internal/kiffapi"
)

// Me says whether a key was refused or KIFF couldn't say: a 401 is
// ErrUnauthorized; a 5xx or an unreachable API is ErrUnavailable; another
// refusal is neither.
func TestMeTellsARefusedKeyFromAnOutage(t *testing.T) {
	status := http.StatusUnauthorized
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"x"}`))
	}))
	c := kiffapi.New(srv.URL, srv.Client())
	if _, err := c.Me(context.Background(), "k"); !errors.Is(err, kiffapi.ErrUnauthorized) || errors.Is(err, kiffapi.ErrUnavailable) {
		t.Fatalf("401: %v", err)
	}
	status = http.StatusServiceUnavailable
	if _, err := c.Me(context.Background(), "k"); !errors.Is(err, kiffapi.ErrUnavailable) {
		t.Fatalf("503: %v", err)
	}
	status = http.StatusForbidden
	if _, err := c.Me(context.Background(), "k"); err == nil || errors.Is(err, kiffapi.ErrUnavailable) || errors.Is(err, kiffapi.ErrUnauthorized) {
		t.Fatalf("403: %v", err)
	}
	srv.Close()
	if _, err := c.Me(context.Background(), "k"); !errors.Is(err, kiffapi.ErrUnavailable) {
		t.Fatalf("unreachable: %v", err)
	}
}
