package kiffapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The gateway reads which holds their agent withdrew from
// GET /v1/me/holds, so kiff_pending can tell one from an expired hold.
func TestHoldsReadsWithdrawn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me/holds" || r.URL.Query().Get("ids") != "ex_w,ex_r" {
			t.Errorf("request %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"holds":[{"exception_id":"ex_w","action":"refund","status":"invalidated","withdrawn":true},{"exception_id":"ex_r","action":"refund","status":"invalidated"}]}`))
	}))
	defer srv.Close()
	holds, err := New(srv.URL, srv.Client()).Holds(context.Background(), "key", []string{"ex_w", "ex_r"})
	if err != nil {
		t.Fatal(err)
	}
	if len(holds) != 2 || !holds[0].Withdrawn || holds[1].Withdrawn {
		t.Fatalf("holds: %+v", holds)
	}
}
