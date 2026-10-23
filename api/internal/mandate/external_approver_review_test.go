package mandate_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

type externalRecoveryStore interface {
	mandate.Store
	mandate.ExceptionStore
	mandate.ApproverStore
	mandate.RoomChangeStore
}

type externalRecoveryRig struct {
	store  externalRecoveryStore
	pool   *pgxpool.Pool
	a      *mandate.Authorizer
	now    time.Time
	g      mandate.ApproverGrant
	e      mandate.Exception
	review mandate.ExternalReview
	req    mandate.Request
}

func newExternalRecoveryRig(t *testing.T, postgres bool) *externalRecoveryRig {
	return newExternalAnswerRig(t, postgres, "approve")
}

func newExternalAnswerRig(t *testing.T, postgres bool, decision string) *externalRecoveryRig {
	t.Helper()
	ctx := context.Background()
	r := &externalRecoveryRig{now: time.Now().UTC()}
	tenant := fmt.Sprintf("external-review-%d", time.Now().UnixNano())
	if postgres {
		r.pool = requirePostgres(t)
		seedTenant(t, r.pool, tenant)
		if err := membership.ApplySchema(ctx, r.pool); err != nil {
			t.Fatal(err)
		}
		if _, err := r.pool.Exec(ctx, `INSERT INTO cloud_meta.tenant_memberships(id,tenant_id,subject,role,status)
VALUES($1||'-admin',$1,'admin','admin','active'),($1||'-backup',$1,'backup-admin','admin','active')`, tenant); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			r.pool.Exec(ctx, `DELETE FROM cloud_meta.approver_grants WHERE tenant_id=$1`, tenant)
			r.pool.Exec(ctx, `DELETE FROM cloud_meta.tenant_memberships WHERE tenant_id=$1`, tenant)
			r.pool.Exec(ctx, `DELETE FROM cloud_meta.tenant_governance WHERE tenant_id=$1`, tenant)
		})
		r.store = mandate.NewPostgresStore(r.pool)
	} else {
		s := mandate.NewMemoryStore()
		s.SetClock(func() time.Time { return r.now })
		s.SetApproverPolicy(func(context.Context, string, string) error { return nil })
		r.store = s
	}
	m := homeCard("card", 50, 200)
	m.TenantID, m.ValidFrom, m.ValidUntil = tenant, r.now.Add(-time.Hour), r.now.Add(2*time.Hour)
	m.HoldExpiry = 10 * time.Minute
	if err := r.store.Issue(ctx, m, "admin"); err != nil {
		t.Fatal(err)
	}
	r.a = mandate.NewAuthorizer(r.store, nil, func() time.Time { return r.now })
	r.req = req(m.Subject, m.Grants[0].Action, "PAID", map[string]int64{"amount_cents": 75})
	d, err := r.a.Authorize(ctx, tenant, "external", r.req)
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("hold %+v %v", d, err)
	}
	r.e, err = r.store.Exception(ctx, tenant, d.ExceptionID)
	if err != nil {
		t.Fatal(err)
	}
	r.g = mandate.ApproverGrant{ID: "grant", TenantID: tenant, Name: "reviewer", Principal: "reviewer", CreatedBy: "admin", CardID: m.ID, Worker: m.Subject, Action: m.Grants[0].Action, Parameter: "amount_cents", Max: 100, CreatedAt: r.now, ExpiresAt: r.now.Add(time.Minute), KeyHash: tenant}
	if err := r.store.CreateApproverGrant(ctx, r.g); err != nil {
		t.Fatal(err)
	}
	ms, err := r.store.ForSubject(ctx, tenant, m.Subject)
	if err != nil || len(ms) != 1 {
		t.Fatalf("cards %+v %v", ms, err)
	}
	r.review = mandate.ExternalReview{State: mandate.ReviewState(r.e), Card: mandate.ReviewCard(ms[0]), Answer: mandate.ExternalAnswer{GrantID: r.g.ID, Principal: r.g.Principal, Decision: decision, ReasonCode: "policy_satisfied", Reason: "Independent facts require human review.", ReviewToken: "original-review", Model: "fixture-model"}}
	if r.e, err = r.store.AnswerExternal(ctx, tenant, r.e.ID, r.g.ID, r.review, r.now); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestExternalApproverListingPreservesAuthority(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(fmt.Sprintf("postgres=%t", postgres), func(t *testing.T) {
			r := newExternalAnswerRig(t, postgres, "defer")
			ctx := context.Background()
			cards, err := r.store.ForSubject(ctx, r.g.TenantID, r.g.Worker)
			if err != nil || len(cards) != 1 {
				t.Fatalf("cards %+v %v", cards, err)
			}
			for _, phase := range []string{"new", "seen", "revoked"} {
				if phase == "seen" {
					err = r.store.TouchApprover(ctx, r.g.TenantID, r.g.ID, r.now)
				} else if phase == "revoked" {
					err = r.store.RevokeApproverGrant(ctx, r.g.TenantID, r.g.ID, r.now)
				}
				if err != nil {
					t.Fatal(err)
				}
				grants, err := r.store.ListApproverGrants(ctx, r.g.TenantID, r.g.CardID)
				if err != nil || len(grants) != 1 {
					t.Fatalf("%s listing %+v %v", phase, grants, err)
				}
				g := grants[0]
				if g.KeyHash != r.g.KeyHash || g.ValidFor(cards[0], r.now) != (phase != "revoked") {
					t.Fatalf("%s listing lost current authority", phase)
				}
				if (g.LastSeenAt != nil) != (phase != "new") || (g.RevokedAt != nil) != (phase == "revoked") {
					t.Fatalf("%s listing lost connection timestamps", phase)
				}
				b, err := json.Marshal(g)
				if err != nil || strings.Contains(string(b), "key_hash") || strings.Contains(string(b), `"KeyHash"`) {
					t.Fatalf("%s public JSON exposed credential hash: %v", phase, err)
				}
			}
		})
	}
}

func TestExternalApproverDeferredRetry(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		for _, change := range []string{"card", "usage"} {
			t.Run(fmt.Sprintf("postgres=%t/%s", postgres, change), func(t *testing.T) {
				r := newExternalAnswerRig(t, postgres, "defer")
				ctx := context.Background()
				for i := 0; i < 2; i++ {
					r.now = r.now.Add(time.Second)
					d, err := r.a.Authorize(ctx, r.g.TenantID, r.e.ProposalID, r.req)
					if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
						t.Fatalf("retry %+v %v", d, err)
					}
					got, err := r.store.Exception(ctx, r.g.TenantID, r.e.ID)
					if err != nil || !reflect.DeepEqual(got.ExternalAnswer, r.e.ExternalAnswer) {
						t.Fatalf("worker retry erased external defer: %+v %v", got.ExternalAnswer, err)
					}
					if mandate.ReviewState(got) != mandate.ReviewState(r.e) || got.Status != mandate.ExceptionHeld || got.ResolvedAt != nil || len(got.PriorApprovals) != 0 {
						t.Fatalf("unchanged retry changed review or authority: %+v", got)
					}
				}
				if change == "card" {
					cards, err := r.store.ForSubject(ctx, r.g.TenantID, r.g.Worker)
					if err != nil || len(cards) != 1 {
						t.Fatalf("cards %+v %v", cards, err)
					}
					cards[0].Grants[0].PerAction[0].Max = 40
					if err := r.store.Issue(ctx, cards[0], "admin"); err != nil {
						t.Fatal(err)
					}
				} else {
					other := r.req
					other.Parameters = map[string]int64{"amount_cents": 40}
					for i := 0; i < 4; i++ {
						if d, err := r.a.Authorize(ctx, r.g.TenantID, fmt.Sprintf("other-%d", i), other); err != nil || !d.Allowed() {
							t.Fatalf("other usage %+v %v", d, err)
						}
					}
				}
				for i := 0; i < 2; i++ {
					d, err := r.a.Authorize(ctx, r.g.TenantID, r.e.ProposalID, r.req)
					if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
						t.Fatalf("changed review %+v %v", d, err)
					}
					got, err := r.store.Exception(ctx, r.g.TenantID, r.e.ID)
					if err != nil || got.ExternalAnswer != nil || len(got.PriorApprovals) != 1 || !reflect.DeepEqual(got.PriorApprovals[0].ExternalAnswer, r.e.ExternalAnswer) {
						t.Fatalf("changed review lost defer history: %+v %v", got, err)
					}
					if got.Status != mandate.ExceptionHeld || got.ResolvedAt != nil || got.ResolvedBy != "" {
						t.Fatalf("historical defer carried authority: %+v", got)
					}
				}
			})
		}
	}
}

func TestExternalApproverReviewRecoveryWithoutWorkerRetry(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		for _, withdrawal := range []string{"revoke", "expiry"} {
			for _, answer := range []string{"human", "replacement", "change_card"} {
				t.Run(fmt.Sprintf("postgres=%t/%s/%s", postgres, withdrawal, answer), func(t *testing.T) {
					r := newExternalRecoveryRig(t, postgres)
					ctx := context.Background()
					if withdrawal == "revoke" {
						if err := r.store.RevokeApproverGrant(ctx, r.g.TenantID, r.g.ID, r.now); err != nil {
							t.Fatal(err)
						}
					} else {
						r.now = r.now.Add(2 * time.Minute)
					}
					p, err := mandate.ReadException(ctx, r.store, r.e, r.now)
					if err != nil || p.Status != mandate.ExceptionHeld {
						t.Fatalf("projection %+v %v", p, err)
					}
					var got mandate.Exception
					want := mandate.ExceptionApproved
					switch answer {
					case "human":
						got, err = r.store.ApproveOnce(ctx, r.g.TenantID, r.e.ID, "human", r.now)
					case "change_card":
						want = mandate.ExceptionChanged
						got, err = r.store.ChangeCardRoom(ctx, r.g.TenantID, r.e.ID, "human", r.now, mandate.RoomThisWindow, time.Time{})
					case "replacement":
						g := r.g
						g.ID, g.KeyHash, g.Principal = "replacement", g.KeyHash+"-replacement", "replacement"
						g.ExpiresAt = r.now.Add(time.Hour)
						if err := r.store.CreateApproverGrant(ctx, g); err != nil {
							t.Fatal(err)
						}
						review := r.review
						review.State = mandate.ReviewState(p.Exception)
						review.Answer.GrantID, review.Answer.Principal, review.Answer.ReviewToken = g.ID, g.Principal, "replacement-review"
						got, err = r.store.AnswerExternal(ctx, g.TenantID, r.e.ID, g.ID, review, r.now)
					}
					if err != nil || got.Status != want {
						t.Fatalf("answer %+v %v", got, err)
					}
					if len(got.PriorApprovals) != 1 || !reflect.DeepEqual(got.PriorApprovals[0].ExternalAnswer, r.e.ExternalAnswer) || !got.ExpiresAt.Equal(*r.e.ExpiresAt) {
						t.Fatalf("history/deadline %+v", got)
					}
					if answer != "replacement" && got.ExternalAnswer != nil {
						t.Fatal("old external authority remained attached to the human answer")
					}
					d, err := r.a.Authorize(ctx, r.g.TenantID, "external", r.req)
					if err != nil || !d.Allowed() {
						t.Fatalf("collect %+v %v", d, err)
					}
				})
			}
		}
	}
}

func TestExternalApproverReviewWithdrawalReappliesDeadline(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		for _, answer := range []string{"human", "replacement", "worker"} {
			t.Run(fmt.Sprintf("postgres=%t/%s", postgres, answer), func(t *testing.T) {
				r := newExternalRecoveryRig(t, postgres)
				ctx := context.Background()
				r.now = r.e.ExpiresAt.Add(time.Second)
				p, err := mandate.ReadException(ctx, r.store, r.e, r.now)
				if err != nil || p.Status != mandate.ExceptionExpired {
					t.Fatalf("expired projection %+v %v", p, err)
				}
				switch answer {
				case "human":
					_, err = r.store.ApproveOnce(ctx, r.g.TenantID, r.e.ID, "human", r.now)
				case "replacement":
					g := r.g
					g.ID, g.Principal, g.KeyHash, g.ExpiresAt = "replacement", "replacement", g.KeyHash+"-replacement", r.now.Add(time.Hour)
					if err := r.store.CreateApproverGrant(ctx, g); err != nil {
						t.Fatal(err)
					}
					review := r.review
					review.Answer.GrantID, review.Answer.Principal, review.Answer.ReviewToken = g.ID, g.Principal, "replacement-review"
					_, err = r.store.AnswerExternal(ctx, g.TenantID, r.e.ID, g.ID, review, r.now)
				case "worker":
					var d mandate.Decision
					d, err = r.a.Authorize(ctx, r.g.TenantID, "external", r.req)
					if err != nil || d.Outcome != mandate.OutcomeOwnerExpired {
						t.Fatalf("worker %+v %v", d, err)
					}
				}
				if answer != "worker" && !errors.Is(err, mandate.ErrExceptionExpired) {
					t.Fatalf("late answer %v", err)
				}
				got, err := r.store.Exception(ctx, r.g.TenantID, r.e.ID)
				if err != nil || got.Status != mandate.ExceptionExpired || len(got.PriorApprovals) != 1 || !reflect.DeepEqual(got.PriorApprovals[0].ExternalAnswer, r.e.ExternalAnswer) || !got.ExpiresAt.Equal(*r.e.ExpiresAt) {
					t.Fatalf("expired record %+v %v", got, err)
				}
			})
		}
	}
}

func TestExternalApproverReviewPolicyWithdrawalSerializesWithCollection(t *testing.T) {
	for _, change := range []string{"demote", "suspend", "strict"} {
		t.Run(change, func(t *testing.T) {
			r := newExternalRecoveryRig(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			second, err := r.a.Authorize(ctx, r.g.TenantID, "uncollected", r.req)
			if err != nil || second.Outcome != mandate.OutcomeOwnerRequired {
				t.Fatalf("second hold %+v %v", second, err)
			}
			pending, err := r.store.Exception(ctx, r.g.TenantID, second.ExceptionID)
			if err != nil {
				t.Fatal(err)
			}
			review := r.review
			review.State, review.Answer.ReviewToken = mandate.ReviewState(pending), "second-review"
			if _, err := r.store.AnswerExternal(ctx, r.g.TenantID, pending.ID, r.g.ID, review, r.now); err != nil {
				t.Fatal(err)
			}
			checked, resume := make(chan struct{}), make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(resume) }) }
			defer release()
			mandate.SetBeforeWrite(r.a, func() { close(checked); <-resume })
			collection := make(chan error, 1)
			go func() {
				d, err := r.a.Authorize(ctx, r.g.TenantID, "external", r.req)
				if err == nil && !d.Allowed() {
					err = fmt.Errorf("collection %+v", d)
				}
				collection <- err
			}()
			select {
			case <-checked:
			case <-ctx.Done():
				t.Fatal("collection did not reach barrier")
			}
			entered := make(chan struct{})
			mutation := make(chan error, 1)
			go func() {
				mutation <- membership.NewPostgresStore(r.pool).Mutate(ctx, r.g.TenantID, func(tx membership.Tx) error {
					close(entered)
					if change == "strict" {
						g, err := tx.Governance()
						if err != nil {
							return err
						}
						g.SeparationOfDuties = true
						return tx.SetGovernance(g)
					}
					m, ok, err := tx.Member("admin")
					if err != nil {
						return err
					}
					if !ok {
						return errors.New("fixture minter missing")
					}
					if change == "demote" {
						m.Role = membership.RoleViewer
					} else {
						m.Status = membership.StatusSuspended
					}
					return tx.UpdateMember(m)
				})
			}()
			// Observe the actual PostgreSQL wait, rather than assuming a
			// goroutine has reached the lock after an arbitrary sleep.
			for {
				select {
				case <-entered:
					t.Fatal("policy withdrawal entered between check and redemption")
				default:
				}
				var waiting bool
				err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND query LIKE '%SELECT NOW() FROM cloud_meta.tenant_governance%' AND wait_event_type='Lock' AND cardinality(pg_blocking_pids(pid))>0)`).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case <-entered:
					t.Fatal("policy withdrawal was not serialized")
				case <-ctx.Done():
					t.Fatal("policy writer did not reach lock")
				case <-time.After(5 * time.Millisecond):
				}
			}
			// Polling remains nonlocking even while a policy writer waits.
			if p, err := mandate.ReadException(ctx, r.store, r.e, r.now); err != nil || p.Status != mandate.ExceptionApproved {
				t.Fatalf("read projection %+v %v", p, err)
			}
			release()
			if err := <-collection; err != nil {
				t.Fatal(err)
			}
			if err := <-mutation; err != nil {
				t.Fatal(err)
			}
			if _, err := r.store.ApproverByHash(ctx, r.g.KeyHash); !errors.Is(err, mandate.ErrApproverGrant) {
				t.Fatalf("withdrawn credential %v", err)
			}
			got, err := r.store.Exception(ctx, r.g.TenantID, r.e.ID)
			if err != nil || got.Status != mandate.ExceptionConsumed {
				t.Fatalf("serialized collection %+v %v", got, err)
			}
			mandate.SetBeforeWrite(r.a, nil)
			// When withdrawal wins first, an uncollected approval cannot
			// become a second draw under the old policy.
			d, err := r.a.Authorize(ctx, r.g.TenantID, "uncollected", r.req)
			if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
				t.Fatalf("collection after withdrawal %+v %v", d, err)
			}
			got, err = r.store.Exception(ctx, r.g.TenantID, pending.ID)
			if err != nil || got.Status != mandate.ExceptionHeld || len(got.PriorApprovals) != 1 || !got.ExpiresAt.Equal(*pending.ExpiresAt) {
				t.Fatalf("withdrawal record %+v %v", got, err)
			}
		})
	}
}

func TestExternalApproverReviewPostgresInvalidationHistory(t *testing.T) {
	for _, transition := range []string{"revoke", "move", "remove_holder"} {
		t.Run(transition, func(t *testing.T) {
			r := newExternalRecoveryRig(t, true)
			ctx := context.Background()
			var err error
			switch transition {
			case "revoke":
				err = r.store.Revoke(ctx, r.g.TenantID, r.g.CardID, r.now)
			case "move":
				_, err = r.store.Move(ctx, r.g.TenantID, r.g.CardID, "backup-worker", "admin", r.now)
			case "remove_holder":
				if _, err = r.store.SetHolders(ctx, r.g.TenantID, r.g.CardID, []string{r.g.Worker, "backup-worker"}, nil, "admin", r.now); err != nil {
					t.Fatal(err)
				}
				_, err = r.store.SetHolders(ctx, r.g.TenantID, r.g.CardID, []string{"backup-worker"}, nil, "admin", r.now)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.store.Exception(ctx, r.g.TenantID, r.e.ID)
			if err != nil || got.Status != mandate.ExceptionInvalidated || got.ExternalAnswer != nil || len(got.PriorApprovals) != 1 || !reflect.DeepEqual(got.PriorApprovals[0].ExternalAnswer, r.e.ExternalAnswer) {
				t.Fatalf("invalidation history %+v %v", got, err)
			}
			if transition == "revoke" {
				return
			}
			if _, err := r.store.Move(ctx, r.g.TenantID, r.g.CardID, r.g.Worker, "admin", r.now); err != nil {
				t.Fatal(err)
			}
			d, err := r.a.Authorize(ctx, r.g.TenantID, "external", r.req)
			if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
				t.Fatalf("fresh hold %+v %v", d, err)
			}
			got, err = r.store.Exception(ctx, r.g.TenantID, r.e.ID)
			if err != nil || got.ExternalAnswer != nil || len(got.PriorApprovals) != 1 || !reflect.DeepEqual(got.PriorApprovals[0].ExternalAnswer, r.e.ExternalAnswer) {
				t.Fatalf("fresh hold lost history %+v %v", got, err)
			}
		})
	}
}
