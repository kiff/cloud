package handlers

import (
	"context"
	"testing"
)

type countingEmails map[string]int

func (c countingEmails) EmailForSubject(_ context.Context, subject string) (string, error) {
	c[subject]++
	return subject + "@acme.test", nil
}

// Within one read, each person's email is looked up once, however many
// shares and groups name them.
func TestPersonNameLooksEachEmailUpOncePerRead(t *testing.T) {
	calls := countingEmails{}
	h := &AccessHandler{emails: calls}
	ctx := withEmailCache(context.Background())
	for i := 0; i < 3; i++ {
		if got := h.personName(ctx, "ana"); got != "ana@acme.test" {
			t.Fatalf("name: %q", got)
		}
	}
	h.personName(context.Background(), "ana") // another read: its own lookup
	if calls["ana"] != 2 {
		t.Fatalf("looked up %d times, want once per read", calls["ana"])
	}
}
