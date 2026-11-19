package handlers

import (
	"strings"
	"testing"
)

// An invitation from an account nobody named is refused until it's
// named: the page says where, and why.
func TestPeopleErrorSaysToNameTheAccount(t *testing.T) {
	msg, reauth := peopleError("account_unnamed", "invite")
	if !strings.Contains(msg, "Name the account first") || !strings.Contains(msg, "Settings") || reauth != "" {
		t.Fatalf("message %q, reauth %q", msg, reauth)
	}
}
