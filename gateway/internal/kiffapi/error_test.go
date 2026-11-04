package kiffapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A refusal from apps/api keeps its message, which names the check that
// refused, after the code: "connector_key_forbidden" alone didn't say which.
func TestAPIErrorKeepsTheMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"connector_key_forbidden","message":"a connector key sets targets for the tools it connects; it doesn't change refund's"}`))
	}))
	defer srv.Close()
	err := New(srv.URL, srv.Client()).SetPermitTargets(context.Background(), "connector", nil)
	want := "kiffapi: 403: connector_key_forbidden: a connector key sets targets for the tools it connects; it doesn't change refund's"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v; want %q", err, want)
	}
	for raw, want := range map[string]string{
		`{"error":"not_found"}`:                 "kiffapi: 403: not_found",
		`{"error":"halted","message":"halted"}`: "kiffapi: 403: halted",
		`plain text`:                            "kiffapi: 403: plain text",
	} {
		if got := apiError(http.StatusForbidden, []byte(raw)).Error(); got != want {
			t.Errorf("%s: %q; want %q", raw, got, want)
		}
	}
}
