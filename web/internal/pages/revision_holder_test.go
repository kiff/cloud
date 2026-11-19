package pages

import (
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
)

func TestRevisionHolder(t *testing.T) {
	t.Parallel()
	revs := []apiclient.MandateRevision{{Subject: ""}, {Subject: "agent://a"}, {Subject: "agent://a"}, {Subject: "agent://b"}}
	want := []string{"—", "agent://a", "agent://a", "agent://b (moved from agent://a)"}
	for i := range revs {
		if got := revisionHolder(revs, i); got != want[i] {
			t.Errorf("revisionHolder(%d) = %q, want %q", i, got, want[i])
		}
	}
}
