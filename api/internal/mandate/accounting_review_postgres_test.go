package mandate_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

func TestPostgres_ProjectionDoesNotWaitForAuthorizationLock(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	tid := "projection-no-lock"
	seedTenant(t, pool, tid)
	s := mandate.NewPostgresStore(pool)
	m := pgHomeCard(tid, "a", 50000, 500000)
	if err := s.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(s, nil, at(base))
	d, err := a.Authorize(ctx, tid, "p", pgRefund(150000))
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.ApproveOnce(ctx, tid, d.ExceptionID, "alice", base)
	if err != nil {
		t.Fatal(err)
	}
	locked, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- s.WithCards(ctx, tid, []string{m.ID}, func(mandate.CardTx) error { close(locked); <-release; return nil })
	}()
	<-locked
	defer func() {
		close(release)
		if err := <-finished; err != nil {
			t.Error(err)
		}
	}()
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	p, err := mandate.ReadException(readCtx, s, e, base)
	if err != nil || p.Status != mandate.ExceptionApproved || p.Exception.ResolvedBy != "alice" {
		t.Fatalf("read blocked or changed: %+v %v", p, err)
	}
	draws, err := s.ProposalDraws(ctx, tid, "p")
	if err != nil || len(draws) != 0 {
		t.Fatalf("read drew: %+v %v", draws, err)
	}
}

func TestPostgres_PriorApprovalSurvivesRenewedHold(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	tid := "approval-history"
	seedTenant(t, pool, tid)
	s := mandate.NewPostgresStore(pool)
	m := pgHomeCard(tid, "a", 50000, 500000)
	if err := s.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(s, nil, at(base))
	d, err := a.Authorize(ctx, tid, "p", pgRefund(150000))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveOnce(ctx, tid, d.ExceptionID, "alice", base); err != nil {
		t.Fatal(err)
	}
	m.Grants[0].PerAction[0].Max = 40000
	if err := s.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	got, err := a.Authorize(ctx, tid, "p", pgRefund(150000))
	if err != nil || got.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("renew: %+v %v", got, err)
	}
	e, err := s.Exception(ctx, tid, d.ExceptionID)
	if err != nil || len(e.PriorApprovals) != 1 || e.PriorApprovals[0].By != "alice" || e.PriorApprovals[0].Breaches[0].Limit != 50000 || e.ResolvedAt != nil {
		t.Fatalf("lost history: %+v %v", e, err)
	}
	if _, err := s.ApproveOnce(ctx, tid, d.ExceptionID, "bob", base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, tid, "a", base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	e, err = s.Exception(ctx, tid, d.ExceptionID)
	if err != nil || len(e.PriorApprovals) != 2 || e.PriorApprovals[1].By != "bob" {
		t.Fatalf("withdrawal lost consent: %+v %v", e, err)
	}
}

func TestPostgres_CutoverRestartSkipsLegacyBackfillAndRejectsOldWriter(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	tid := "cutover-restart"
	seedTenant(t, pool, tid)
	s := mandate.NewPostgresStore(pool)
	m := pgHomeCard(tid, "a", 50000, 500000)
	if err := s.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	var cutover time.Time
	if err := pool.QueryRow(ctx, "SELECT activated_at FROM cloud_meta.mandate_accounting_cutover WHERE version=1").Scan(&cutover); err != nil {
		t.Fatal(err)
	}
	// A leftover historical draw without a claim must not make a later
	// boot attempt a version-zero INSERT into the guarded writer table.
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.mandate_draws(tenant_id,mandate_id,proposal_id,quantity_key,amount,drawn_at) VALUES($1,'a','old-unclaimed','sum(amount_cents)',100,$2)`, tid, base); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := mandate.ApplySchema(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var after time.Time
	if err := pool.QueryRow(ctx, "SELECT activated_at FROM cloud_meta.mandate_accounting_cutover WHERE version=1").Scan(&after); err != nil || !after.Equal(cutover) {
		t.Fatalf("cutover moved: %s %s %v", cutover, after, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.mandate_proposal_claims(tenant_id,proposal_id) VALUES($1,'old-writer')`, tid); err == nil {
		t.Fatal("old writer accepted")
	}
	a := mandate.NewAuthorizer(s, nil, at(base))
	d, err := a.Authorize(ctx, tid, "old-unclaimed", pgRefund(100))
	if err != nil || d.Outcome != mandate.OutcomeProposalReused {
		t.Fatalf("unclaimed historical operation reused: %+v %v", d, err)
	}
}

func TestPostgres_FirstCutoverBackfillsLegacyClaimWithoutRewritingDraw(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	tid := "first-cutover"
	seedTenant(t, pool, tid)
	s := mandate.NewPostgresStore(pool)
	if err := s.Issue(ctx, pgHomeCard(tid, "a", 50000, 500000), "owner"); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Stage the pre-cutover schema transactionally; rollback leaves other
	// tests' writer guard and immutable activation record untouched.
	if _, err := tx.Exec(ctx, `DROP TRIGGER mandate_accounting_writer_guard ON cloud_meta.mandate_proposal_claims; DELETE FROM cloud_meta.mandate_accounting_cutover WHERE version=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO cloud_meta.mandate_draws(tenant_id,mandate_id,proposal_id,quantity_key,amount,drawn_at) VALUES($1,'a','old','sum(amount_cents)',123,$2)`, tid, base); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, mandate.SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	var version int
	var claimed time.Time
	var amount int64
	if err := tx.QueryRow(ctx, `SELECT accounting_version,claimed_at FROM cloud_meta.mandate_proposal_claims WHERE tenant_id=$1 AND proposal_id='old'`, tid).Scan(&version, &claimed); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT amount FROM cloud_meta.mandate_draws WHERE tenant_id=$1 AND proposal_id='old'`, tid).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	if version != 0 || !claimed.Equal(base) || amount != 123 {
		t.Fatalf("history changed: version=%d at=%s amount=%d", version, claimed, amount)
	}
	if _, err := tx.Exec(ctx, mandate.SchemaSQL()); err != nil {
		t.Fatal("restart", err)
	}
}
