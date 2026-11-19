package handlers

import (
	"net/http"
	"testing"
)

// A hosted runtime's refusal is a state block by its typed reason, never by
// the text of its error.
func TestStateBlockResponseIsReadFromItsReason(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   bool
	}{
		{http.StatusBadRequest, `{"outcome":"blocked","reason":"state_not_allowed","error":"action state not allowed: \"PAID\""}`, true},
		{http.StatusBadRequest, `{"outcome":"invalid","reason":"missing_parameter","error":"missing amount; action state not allowed"}`, false},
		{http.StatusBadRequest, `{"error":"action state not allowed"}`, true},              // a framework before the envelope
		{http.StatusBadRequest, `{"reason":"","error":"action state not allowed"}`, false}, // a reason, even empty, decides
		{http.StatusForbidden, `{"reason":"state_not_allowed"}`, false},
	} {
		if got := isStateNotAllowedResponse(&bufferedWriter{status: c.status, body: []byte(c.body)}); got != c.want {
			t.Errorf("%d %s: %v, want %v", c.status, c.body, got, c.want)
		}
	}
}
