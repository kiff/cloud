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

// A timeout, a throttle or an answer that isn't the identity says nothing
// about the key: ErrUnavailable (the gateway's 503), never ErrUnauthorized
// (its 401 "a valid agent key is required"), which ends a caller's paused
// run as a revoked key's. Only apps/api's own 401 is a refused key.
func TestMeTellsABusyAPIFromARefusedKey(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
	}{
		{"408", http.StatusRequestTimeout, `{"error":"timeout"}`},
		{"429", http.StatusTooManyRequests, `{"error":"rate_limited"}`},
		{"a body that isn't JSON", http.StatusOK, `<html>bad gateway</html>`},
		{"a body cut short", http.StatusOK, `{"tenant_id":"t`},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			_, err := kiffapi.New(srv.URL, srv.Client()).Me(context.Background(), "k")
			if !errors.Is(err, kiffapi.ErrUnavailable) || errors.Is(err, kiffapi.ErrUnauthorized) {
				t.Fatalf("%v", err)
			}
		})
	}
}

// AgentCard tells no answer from a refusal the same way: a 5xx or an
// unreachable API is ErrUnavailable, so the gateway lists a taf the account's
// tools as before; a 4xx is KIFF's answer, never ErrUnavailable.
func TestAgentCardTellsARefusalFromAnOutage(t *testing.T) {
	status := http.StatusUnauthorized
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"x"}`))
	}))
	c := kiffapi.New(srv.URL, srv.Client())
	if _, err := c.AgentCard(context.Background(), "k", ""); err == nil || errors.Is(err, kiffapi.ErrUnavailable) {
		t.Fatalf("401: %v", err)
	}
	status = http.StatusBadGateway
	if _, err := c.AgentCard(context.Background(), "k", ""); !errors.Is(err, kiffapi.ErrUnavailable) {
		t.Fatalf("502: %v", err)
	}
	srv.Close()
	if _, err := c.AgentCard(context.Background(), "k", ""); !errors.Is(err, kiffapi.ErrUnavailable) {
		t.Fatalf("unreachable: %v", err)
	}
}
