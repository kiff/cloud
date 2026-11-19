package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExternalApproverCredentialCannotUseOrdinaryRoutes(t *testing.T) {
	h := buildTestRouter(t, nil)
	for _, path := range []string{"/v1/me/card", "/v1/proposals/decide", "/v1/me/approver-grants", "/v1/keys"} {
		t.Run(path, func(t *testing.T) {
			method := http.MethodPost
			if path == "/v1/me/card" {
				method = http.MethodGet
			}
			r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
			r.Header.Set("Authorization", "Bearer kiff_approver_restricted-fixture")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("restricted credential reached ordinary route: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
