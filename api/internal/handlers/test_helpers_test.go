package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/kiff/cloud/api/internal/auth"
)

// requestWithIdentity returns an http.Request whose context carries
// a synthetic auth.Identity for tenant t1. Used by handler-level
// tests that need to skip the auth middleware and inject the
// identity directly.
func requestWithIdentity(method, path string) *http.Request {
	return requestWithIdentityRoles(method, path, nil)
}

// requestWithIdentityRoles is the role-aware variant. Tests for
// permission gates (e.g. the approval-review check in) use it
// to vary the principal's roles between cases.
func requestWithIdentityRoles(method, path string, roles []string) *http.Request {
	return requestWithScopedIdentity(method, path, roles, "")
}

func requestWithScopedIdentity(method, path string, roles []string, domainID string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	id := auth.Identity{
		Subject:  "subj-1",
		Tenant:   "t1",
		Method:   auth.MethodAPIKey,
		APIKey:   true,
		Roles:    append([]string(nil), roles...),
		DomainID: domainID,
		Metadata: map[string]string{"api_key_id": "key-test-1"},
	}
	ctx := auth.WithIdentity(r.Context(), id)
	return r.WithContext(ctx)
}
