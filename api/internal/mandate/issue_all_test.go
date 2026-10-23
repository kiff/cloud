package mandate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// IssueAll issues only over the revisions it was made from (kiff/tafs#107):
// a Card revised since refuses the whole issue, and nothing is issued.
func TestIssueAllOnlyOverTheRevisionShown(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 100000))
	ctx := context.Background()
	changed := homeCard("card-a", 90000, 100000)
	added := homeCard("card-b", 1000, 5000)
	if err := r.store.IssueAll(ctx, []mandate.Mandate{changed, added}, "owner", map[string]int64{"card-a": 0}, nil, time.Time{}); !errors.Is(err, mandate.ErrCardRevised) {
		t.Fatalf("over a revision that isn't the latest: %v", err)
	}
	if revs, _ := r.store.Revisions(ctx, tenantID, "card-b"); len(revs) != 0 {
		t.Fatal("a refused IssueAll issued a Card")
	}
	if err := r.store.IssueAll(ctx, []mandate.Mandate{changed, added}, "owner", map[string]int64{"card-a": 1}, nil, time.Time{}); err != nil {
		t.Fatalf("over the latest revision: %v", err)
	}
	if revs, _ := r.store.Revisions(ctx, tenantID, "card-a"); len(revs) != 2 {
		t.Fatalf("card-a revisions = %d", len(revs))
	}
}

// IssueAll retires Cards in the same issue (kiff/tafs#107): revoked from at,
// with the issued ones. One that doesn't exist refuses the whole issue.
func TestIssueAllRetires(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 100000), homeCard("card-b", 1000, 5000))
	ctx := context.Background()
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	if err := r.store.IssueAll(ctx, []mandate.Mandate{homeCard("card-a", 40000, 100000)}, "owner", nil, []string{"card-b", "card-z"}, at); !errors.Is(err, mandate.ErrNotFound) {
		t.Fatalf("retiring a Card that doesn't exist: %v", err)
	}
	if revs, _ := r.store.Revisions(ctx, tenantID, "card-a"); len(revs) != 1 {
		t.Fatal("a refused IssueAll issued a Card")
	}
	if err := r.store.IssueAll(ctx, []mandate.Mandate{homeCard("card-a", 40000, 100000)}, "owner", map[string]int64{"card-b": 1}, []string{"card-b"}, at); err != nil {
		t.Fatal(err)
	}
	ms, err := r.store.List(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range ms {
		if m.ID == "card-b" {
			found = true
			if m.RevokedAt == nil || !m.RevokedAt.Equal(at) {
				t.Fatalf("card-b after being retired: revoked %v", m.RevokedAt)
			}
		}
	}
	if !found {
		t.Fatal("card-b isn't listed")
	}
	if revs, _ := r.store.Revisions(ctx, tenantID, "card-a"); len(revs) != 2 {
		t.Fatal("card-a wasn't issued with the retire")
	}
}
