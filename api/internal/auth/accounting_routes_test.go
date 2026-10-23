package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGatewayReconciliationRouteIsNarrow(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/v1/me/card-effects/tc-123/reconcile", true},
		{http.MethodPost, "/v1/me/card-effects/tc-123/reconcile/", true},
		{http.MethodGet, "/v1/me/card-effects/tc-123/reconcile", false},
		{http.MethodPost, "/v1/me/card-effects//reconcile", false},
		{http.MethodPost, "/v1/me/card-effects/../reconcile", false},
		{http.MethodPost, "/v1/me/card-effects/one/two/reconcile", false},
		{http.MethodPost, "/v1/me/mandates/card/extra-room", false},
		{http.MethodPost, "/v1/me/holds/ex-1/withdraw", true},
		{http.MethodPost, "/v1/me/holds/ex-1/withdraw/", true},
		{http.MethodGet, "/v1/me/holds/ex-1/withdraw", false},
		{http.MethodPost, "/v1/me/holds//withdraw", false},
		{http.MethodPost, "/v1/me/holds/../withdraw", false},
		{http.MethodPost, "/v1/me/holds/a/b/withdraw", false},
		{http.MethodPost, "/v1/me/holds/ex-1/grant", false},
		{http.MethodGet, "/v1/me/stream", false},
		// Its own draft's trial (kiff/tafs#173): read it, and end it.
		{http.MethodGet, "/v1/me/agent-drafts/drf_1/trial", true},
		{http.MethodDelete, "/v1/me/agent-drafts/drf_1/trial", true},
		{http.MethodPost, "/v1/me/agent-drafts/drf_1/trial", false},
		{http.MethodGet, "/v1/me/agent-drafts/a/b/trial", false},
		{http.MethodDelete, "/v1/me/agent-drafts/drf_1", false},
		{http.MethodPost, "/v1/me/agent-drafts/drf_1/confirm", false},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if got := OAuthRouteAllowed(r); got != tc.want {
			t.Fatalf("%s %s: %v", tc.method, tc.path, got)
		}
	}
}
