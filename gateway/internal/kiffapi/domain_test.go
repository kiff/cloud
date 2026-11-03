package kiffapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// kiff/tafs#206: a phone's signed control connection saves the account's
// contract with the phone's device grant. The contract's own call carries it,
// and the account a person named, on the PUT and on the POST that follows a
// 404, as every other call does; without it apps/api refuses a phone's session.
func TestApplyDomainCarriesTheDeviceGrantAndTenant(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.Header.Get(DeviceGrantHeader)+" "+r.Header.Get(TenantHeader))
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	ctx := WithDeviceGrant(WithTenant(context.Background(), "t_1"), "kdg_x")
	if err := New(srv.URL, srv.Client()).ApplyDomain(ctx, "phone-session", "gateway", "domain: gateway\n"); err != nil {
		t.Fatal(err)
	}
	want := []string{"PUT kdg_x t_1", "POST kdg_x t_1"}
	if len(seen) != len(want) || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("calls = %q; want %q", seen, want)
	}
}
