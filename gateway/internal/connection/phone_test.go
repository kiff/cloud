package connection_test

import (
	"net/http"
	"strings"
	"testing"
)

// A phone's session lists the account's tools, to give a
// new taf, but never connects, changes or removes one: a stolen phone
// token must not change what the account's agents reach.
func TestAPhoneSessionListsButNeverChangesConnections(t *testing.T) {
	r := newRig(t, dev)
	if code, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", r.refund(toolSecret)); code != http.StatusCreated {
		t.Fatalf("owner connect: %d %s", code, body)
	}
	if code, body := r.do(t, "phone-a", http.MethodGet, "/v1/connections", nil); code != http.StatusOK || !strings.Contains(body, "issue_refund") {
		t.Fatalf("phone list: %d %s", code, body)
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/v1/connections", r.refund("cred-x")},
		{http.MethodPut, "/v1/connections/issue_refund/credential", map[string]string{"credential": "x"}},
		{http.MethodPut, "/v1/connections/issue_refund/mode", map[string]string{"mode": "verify"}},
		{http.MethodDelete, "/v1/connections/issue_refund", nil},
	} {
		if code, body := r.do(t, "phone-a", c.method, c.path, c.body); code != http.StatusForbidden || !strings.Contains(body, "phone") {
			t.Errorf("phone %s %s: %d %s", c.method, c.path, code, body)
		}
	}
	if code, body := r.do(t, "owner-a", http.MethodGet, "/v1/connections", nil); code != http.StatusOK || !strings.Contains(body, "issue_refund") {
		t.Errorf("the connection changed: %d %s", code, body)
	}
}
