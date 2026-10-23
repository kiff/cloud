package mandate_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// Force redemption to commit between the initial answer lookup and the
// duplicate-draw check. Both an identical retry and an altered retry retain
// their correct meaning; neither can charge another draw.
type staleApprovalStore struct {
	*mandate.MemoryStore
	stale mandate.Exception
	reads int
}

func (s *staleApprovalStore) GetException(ctx context.Context, tenant, proposal string) (mandate.Exception, bool, error) {
	s.reads++
	if s.reads == 1 {
		return s.stale, true, nil
	}
	return s.MemoryStore.GetException(ctx, tenant, proposal)
}
func TestConsumedApprovalVisibleAfterDrawLookup(t *testing.T) {
	for _, amount := range []int64{150000, 160000} {
		t.Run(time.Duration(amount).String(), func(t *testing.T) {
			r := newRig(t, homeCard("a", 50000, 500000))
			d := r.decide("p", 150000)
			approved, err := r.store.ApproveOnce(context.Background(), tenantID, d.ExceptionID, "alice", base)
			if err != nil {
				t.Fatal(err)
			}
			if got := r.decide("p", 150000); !got.Allowed() {
				t.Fatal(got)
			}
			wrapped := &staleApprovalStore{MemoryStore: r.store, stale: approved}
			a := mandate.NewAuthorizer(wrapped, nil, at(base))
			got, err := a.Authorize(context.Background(), tenantID, "p", refund(amount))
			want := mandate.OutcomeWithinLimit
			if amount != 150000 {
				want = mandate.OutcomeProposalReused
			}
			if err != nil || got.Outcome != want || r.used("a") != 150000 {
				t.Fatalf("retry: %+v %v used=%d", got, err, r.used("a"))
			}
		})
	}
}

func TestNewThresholdTakesPrecedenceOverNoRoomAndPreservesApprover(t *testing.T) {
	ctx := context.Background()
	a, b := homeCard("a", 50000, 5000000), homeCard("b", 1000000, 200000)
	r := newRig(t, a, b)
	d := r.decide("p", 150000)
	approved, err := r.store.ApproveOnce(ctx, tenantID, d.ExceptionID, "alice", base)
	if err != nil {
		t.Fatal(err)
	}
	r.decide("spend1", 40000)
	r.decide("spend2", 40000)
	b.Grants[0].PerAction[0].Max = 100000
	if err := r.store.Issue(ctx, b, "owner"); err != nil {
		t.Fatal(err)
	}
	projection, err := mandate.ReadException(ctx, r.store, approved, base)
	if err != nil || projection.Status != mandate.ExceptionHeld || projection.Exception.ResolvedAt != nil || len(projection.Exception.PriorApprovals) != 1 {
		t.Fatalf("projection: %+v %v", projection, err)
	}
	if got := r.decide("p", 150000); got.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("new threshold: %+v", got)
	}
	e, err := r.store.Exception(ctx, tenantID, d.ExceptionID)
	if err != nil || e.Status != mandate.ExceptionHeld || e.ResolvedAt != nil || len(e.PriorApprovals) != 1 || e.PriorApprovals[0].By != "alice" || len(e.PriorApprovals[0].Breaches) != 1 {
		t.Fatalf("history: %+v %v", e, err)
	}
	r.decide("p", 150000)
	e, _ = r.store.Exception(ctx, tenantID, d.ExceptionID)
	if len(e.PriorApprovals) != 1 {
		t.Fatalf("retry duplicated consent: %+v", e.PriorApprovals)
	}
	if _, err := r.store.ApproveOnce(ctx, tenantID, d.ExceptionID, "bob", base); err != mandate.ErrCumulativeApproval {
		t.Fatalf("approval waived total: %v", err)
	}
}

func TestLegacyProjectionMatchesExpiryAndValidityWithoutWriting(t *testing.T) {
	for _, kind := range []string{"live", "expired", "revoked", "card-expired", "moved"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			m := homeCard("a", 50000, 500000)
			r := newRig(t, m)
			d := r.decide("p", 150000)
			e, _ := r.store.ApproveOnce(ctx, tenantID, d.ExceptionID, "alice", base)
			e.AccountingVersion = 0
			expiry := base.Add(time.Hour)
			want := mandate.ExceptionHeld
			switch kind {
			case "expired":
				expiry = base.Add(-time.Minute)
				want = mandate.ExceptionExpired
			case "revoked":
				when := base.Add(-time.Minute)
				m.RevokedAt = &when
				want = mandate.ExceptionInvalidated
			case "card-expired":
				m.ValidUntil = base.Add(-time.Minute)
				want = mandate.ExceptionInvalidated
			case "moved":
				m.Subject = "agent://other"
				want = mandate.ExceptionInvalidated
			}
			e.ExpiresAt = &expiry
			// Fixture directly changes validity before restoring the legacy answer.
			if kind != "live" && kind != "expired" {
				if err := r.store.Issue(ctx, m, "owner"); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.store.SaveException(ctx, e); err != nil {
				t.Fatal(err)
			}
			p, err := mandate.ReadException(ctx, r.store, e, base)
			if err != nil || p.Status != want || p.Exception.LegacyApproval == nil {
				t.Fatalf("projection: %+v %v want %s", p, err, want)
			}
			stored, _ := r.store.Exception(ctx, tenantID, e.ID)
			if stored.Status != mandate.ExceptionApproved || stored.AccountingVersion != 0 || stored.LegacyApproval != nil || r.used("a") != 0 {
				t.Fatalf("read wrote: %+v", stored)
			}
			// Expiry must agree exactly with the next locked authorization.
			if kind == "expired" {
				got := r.decide("p", 150000)
				if got.Outcome != mandate.OutcomeOwnerExpired {
					t.Fatalf("writer: %+v", got)
				}
			}
		})
	}
}

func TestRenewedThresholdReviewDoesNotExtendExpiredDeadline(t *testing.T) {
	ctx := context.Background()
	m := homeCard("a", 50000, 500000)
	r := newRig(t, m)
	d := r.decide("p", 150000)
	e, err := r.store.ApproveOnce(ctx, tenantID, d.ExceptionID, "alice", base)
	if err != nil {
		t.Fatal(err)
	}
	r.clock = e.ExpiresAt.Add(time.Minute)
	m.Grants[0].PerAction[0].Max = 40000
	if err := r.store.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	p, err := mandate.ReadException(ctx, r.store, e, r.clock)
	if err != nil || p.Status != mandate.ExceptionExpired {
		t.Fatalf("projection extends deadline: %+v %v", p, err)
	}
	if got := r.decide("p", 150000); got.Outcome != mandate.OutcomeOwnerExpired {
		t.Fatalf("writer extends deadline: %+v", got)
	}
	if r.used("a") != 0 {
		t.Fatal("expired action drew budget")
	}
}

func TestProjectionInvalidatesApprovalWhenCardNoLongerCoversTheAction(t *testing.T) {
	ctx := context.Background()
	m := homeCard("a", 50000, 500000)
	r := newRig(t, m)
	d := r.decide("p", 150000)
	e, err := r.store.ApproveOnce(ctx, tenantID, d.ExceptionID, "alice", base)
	if err != nil {
		t.Fatal(err)
	}
	m.Domain = "another-domain"
	if err := r.store.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	p, err := mandate.ReadException(ctx, r.store, e, base)
	if err != nil || p.Status != mandate.ExceptionInvalidated {
		t.Fatalf("uncovered approval offered for collection: %+v %v", p, err)
	}
	if r.used("a") != 0 {
		t.Fatal("read drew budget")
	}
}
