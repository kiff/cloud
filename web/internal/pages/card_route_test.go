package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// A Card's route reads on its own, beside its name: Stop or Ask, what
// that means, and for Ask the hold it actually has.
func TestCardRouteBadgeSaysWhatHappens(t *testing.T) {
	for _, c := range []struct {
		m         apiclient.Mandate
		tag, text string
	}{
		{apiclient.Mandate{OnExceed: "refuse"}, "STOP", "Refused outside limits."},
		{apiclient.Mandate{OnExceed: "owner", HoldExpirySeconds: 3600}, "ASK", "Held for an authorized approver · 1 hour"},
		{apiclient.Mandate{OnExceed: "owner"}, "ASK", "Held for an authorized approver · 10 min"},
	} {
		var b bytes.Buffer
		if err := cardRouteBadge(c.m).Render(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
		got := b.String()
		if !strings.Contains(got, ">"+c.tag+"<") || !strings.Contains(got, c.text) {
			t.Errorf("%+v: badge %q, want %s and %q", c.m, got, c.tag, c.text)
		}
	}
}
