package tooloutcome_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

// releaser records each release it's asked for, as one per draw: it writes
// the release row the watch reads, so a second pass finds nothing to give.
type releaser struct {
	pool *pgxpool.Pool
	mu   sync.Mutex
	got  []mandate.NoEffectEvidence
}

func (r *releaser) ReleaseEffects(ctx context.Context, e mandate.NoEffectEvidence) ([]mandate.EffectRelease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, e)
	if _, err := r.pool.Exec(ctx, `INSERT INTO cloud_meta.mandate_effect_releases (tenant_id, mandate_id, proposal_id, quantity_key)
		SELECT tenant_id, mandate_id, proposal_id, quantity_key FROM cloud_meta.mandate_draws WHERE tenant_id=$1 AND proposal_id=$2
		ON CONFLICT DO NOTHING`, e.TenantID, e.ProposalID); err != nil {
		return nil, err
	}
	return []mandate.EffectRelease{{}}, nil
}

// notice is one refund notice the watch sent.
type notice struct {
	tenant, call, tool, agent, kind string
	at                              time.Time
}

type notifier struct {
	mu  sync.Mutex
	got []notice
}

func (n *notifier) NotifyRefund(tenant, call, tool, agent, kind string, at time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.got = append(n.got, notice{tenant, call, tool, agent, kind, at})
}

func (n *notifier) kinds() map[string]string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := map[string]string{}
	for _, e := range n.got {
		out[e.call] = e.kind
	}
	return out
}

func refundTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	withoutGatewaySchema(t, pool)
	for _, q := range []string{
		`CREATE SCHEMA gateway`,
		`CREATE TABLE gateway.invocations (key text PRIMARY KEY, tenant_id text, agent_id text, tool text, state text, tool_outcome text,
			updated_at timestamptz, no_effect_evidence jsonb)`,
		`CREATE TABLE gateway.thune_adjustments (key text PRIMARY KEY, tenant_id text, account text, object text,
			execution text, financial text, refund_id text, updated_at timestamptz)`,
		`CREATE TABLE gateway.thune_refunds (tenant_id text, account text, refund_id text, object text, status text, origin text)`,
		`CREATE SCHEMA IF NOT EXISTS cloud_meta`,
		`DROP TABLE IF EXISTS cloud_meta.mandate_effect_releases, cloud_meta.mandate_effect_surcharges, cloud_meta.mandate_draws, cloud_meta.refund_notices`,
		`CREATE TABLE cloud_meta.mandate_draws (tenant_id text, mandate_id text, proposal_id text, quantity_key text,
			PRIMARY KEY (tenant_id, mandate_id, proposal_id, quantity_key))`,
		`CREATE TABLE cloud_meta.mandate_effect_releases (tenant_id text, mandate_id text, proposal_id text, quantity_key text,
			source text NOT NULL DEFAULT '', PRIMARY KEY (tenant_id, mandate_id, proposal_id, quantity_key))`,
		`CREATE TABLE cloud_meta.mandate_effect_surcharges (tenant_id text, mandate_id text, proposal_id text, quantity_key text,
			source text NOT NULL, PRIMARY KEY (tenant_id, mandate_id, proposal_id, quantity_key))`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// The notices' table was dropped: have the schema applied again.
	_, _ = pool.Exec(ctx, `DELETE FROM cloud_meta.schemas_applied WHERE name = 'tooloutcome.refund_notices'`)
	if err := tooloutcome.ApplyRefundSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
}

// A refund KIFF sent that the processor says failed or was cancelled gives
// its Card room back and its owner is told; a charge KIFF refunded that was
// also refunded outside KIFF is told to its owner. Each once, and nothing
// for a refund that succeeded alone, one that was never sent, or one whose
// failure no processor said.
func TestTheRefundWatchGivesRoomBackAndTellsTheOwnerOnce(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, postgresURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	refundTables(t, pool)
	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	for _, a := range []struct{ key, charge, exec, fin, refund string }{
		{"tc-failed", "ch_1", "sent", "failed", "re_1"},
		{"tc-cancelled", "ch_2", "sent", "cancelled", "re_2"},
		{"tc-ok", "ch_3", "sent", "succeeded", "re_3"},
		{"tc-twice", "ch_4", "sent", "pending", "re_4"},
		{"tc-unsent", "ch_5", "not_sent", "none", ""},
		{"tc-unknown", "ch_6", "unknown", "none", ""},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO gateway.invocations VALUES ($1,'t1','agent-a','issue_refund','forwarded','done',$2,NULL)`, a.key, at); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO gateway.thune_adjustments VALUES ($1,'t1','acct_1',$2,$3,$4,$5,$6)`, a.key, a.charge, a.exec, a.fin, a.refund, at); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.mandate_draws VALUES ('t1','card-1',$1,'sum(amount_eur)')`, a.key); err != nil {
			t.Fatal(err)
		}
	}
	// ch_4 was also refunded in the dashboard; ch_3's outside refund failed.
	if _, err := pool.Exec(ctx, `INSERT INTO gateway.thune_refunds VALUES
		('t1','acct_1','re_9','ch_4','succeeded','outside'), ('t1','acct_1','re_8','ch_3','failed','outside'),
		('t2','acct_1','re_7','ch_3','succeeded','outside')`); err != nil {
		t.Fatal(err)
	}
	rel := &releaser{pool: pool}
	n := &notifier{}
	w := &tooloutcome.RefundWatch{Outcomes: tooloutcome.NewPostgres(pool), Releases: rel, Alerts: n, Now: func() time.Time { return at }}
	released, told, err := w.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if released != 2 || len(rel.got) != 2 {
		t.Fatalf("released %d: %+v", released, rel.got)
	}
	for _, e := range rel.got {
		if e.Source != tooloutcome.SourceRefundFailed || e.Subject != "agent-a" || e.Action != "issue_refund" || e.EvidenceID == "" || !e.At.Equal(at) {
			t.Fatalf("evidence: %+v", e)
		}
	}
	want := map[string]string{"tc-failed": tooloutcome.NoticeRefundFailed, "tc-cancelled": tooloutcome.NoticeRefundFailed, "tc-twice": tooloutcome.NoticeRefundedTwice}
	got := n.kinds()
	if told != 3 || len(got) != 3 {
		t.Fatalf("told %d: %v", told, got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("notices %v, want %v", got, want)
		}
	}
	for _, e := range n.got {
		if e.tenant != "t1" || e.agent != "agent-a" || e.tool != "issue_refund" || e.kind == "" {
			t.Fatalf("event: %+v", e)
		}
	}
	// A second pass gives nothing back and tells no one again.
	released, told, err = w.RunOnce(ctx)
	if err != nil || released != 0 || told != 0 || len(rel.got) != 2 || len(n.got) != 3 {
		t.Fatalf("second pass: released %d, told %d, %v; releases %d, notices %d", released, told, err, len(rel.got), len(n.got))
	}
	// The no-effect evidence the gateway's reconcile reads says the same.
	r := tooloutcome.NewPostgres(pool)
	for key, found := range map[string]bool{"tc-failed": true, "tc-ok": false, "tc-twice": false, "tc-unknown": false} {
		e, ok, err := r.NoEffect(ctx, "t1", key)
		if err != nil || ok != found || ok && e.Source != tooloutcome.SourceRefundFailed {
			t.Errorf("%s: %+v %v %v", key, e, ok, err)
		}
		if _, ok, _ := r.NoEffect(ctx, "t2", key); ok {
			t.Errorf("%s: another tenant's evidence", key)
		}
	}
}

// The owner's "No" to "Did this happen?" on a governed refund whose
// outcome was unknown ends it not sent in thune and releases its request,
// so another taf can take it. A refund already found, or one thune doesn't
// hold, changes nothing.
func TestTheOwnersNoReleasesAnUnknownRefundsRequest(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, postgresURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	withoutGatewaySchema(t, pool)
	for _, q := range []string{
		`CREATE SCHEMA gateway`,
		`CREATE TABLE gateway.thune_adjustments (key text PRIMARY KEY, tenant_id text, account text, object text, kind text,
			request text, execution text, financial text, updated_at timestamptz)`,
		`CREATE TABLE gateway.thune_claims (tenant_id text, account text, object text, kind text, request text, holder text,
			released_at timestamptz, PRIMARY KEY (tenant_id, account, object, kind, request))`,
		`INSERT INTO gateway.thune_adjustments VALUES
			('tc-unknown','t1','acct_1','ch_1','refund','ticket-7','unknown','none', now()),
			('tc-found','t1','acct_1','ch_2','refund','ticket-8','sent','pending', now())`,
		`INSERT INTO gateway.thune_claims VALUES
			('t1','acct_1','ch_1','refund','ticket-7','tc-unknown',NULL),
			('t1','acct_1','ch_2','refund','ticket-8','tc-found',NULL)`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	p := tooloutcome.NewPostgres(pool)
	for _, key := range []string{"tc-unknown", "tc-found", "tc-not-thunes"} {
		if err := p.OwnerSaidNotSent(ctx, "t1", key); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	// Another account's answer never reaches this account's rows.
	if err := p.OwnerSaidNotSent(ctx, "t2", "tc-found"); err != nil {
		t.Fatal(err)
	}
	state := func(key string) (exec string, released bool) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT a.execution, c.released_at IS NOT NULL FROM gateway.thune_adjustments a
			JOIN gateway.thune_claims c ON c.holder = a.key WHERE a.key = $1`, key).Scan(&exec, &released); err != nil {
			t.Fatal(err)
		}
		return
	}
	if exec, released := state("tc-unknown"); exec != "not_sent" || !released {
		t.Errorf("unknown refund after No: %s, released=%v; want not_sent and released", exec, released)
	}
	if exec, released := state("tc-found"); exec != "sent" || released {
		t.Errorf("found refund after No: %s, released=%v; want unchanged", exec, released)
	}
}

// reinstater takes back the owner's-No releases as surcharges, one per
// draw, as the mandate store does, and records each ask.
type reinstater struct {
	pool *pgxpool.Pool
	mu   sync.Mutex
	got  []mandate.ReinstateEvidence
}

func (r *reinstater) ReinstateEffects(ctx context.Context, e mandate.ReinstateEvidence) ([]mandate.EffectSurcharge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, e)
	_, err := r.pool.Exec(ctx, `INSERT INTO cloud_meta.mandate_effect_surcharges (tenant_id, mandate_id, proposal_id, quantity_key, source)
		SELECT tenant_id, mandate_id, proposal_id, quantity_key, $3 FROM cloud_meta.mandate_effect_releases
		 WHERE tenant_id=$1 AND proposal_id=$2 AND source=$4 ON CONFLICT DO NOTHING`, e.TenantID, e.ProposalID, e.Source, e.Undoes)
	return nil, err
}

// A refund the owner said didn't happen, then listed by the processor by
// its marker, takes its room on the Card again and its owner is told, each
// once. One that failed after the "No" was right: its room stays back.
func TestARefundFoundAfterTheOwnersNoTakesItsRoomAgainAndIsTold(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, postgresURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	refundTables(t, pool)
	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	for _, a := range []struct{ key, exec, fin, refund string }{
		{"tc-found", "sent", "succeeded", "re_1"},
		{"tc-right", "sent", "failed", "re_2"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO gateway.invocations VALUES ($1,'t1','agent-a','issue_refund','forwarded','done',$2,NULL)`, a.key, at); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO gateway.thune_adjustments VALUES ($1,'t1','acct_1','ch_1',$2,$3,$4,$5)`, a.key, a.exec, a.fin, a.refund, at); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.mandate_draws VALUES ('t1','card-1',$1,'sum(amount_eur)')`, a.key); err != nil {
			t.Fatal(err)
		}
		// The owner said "No": the room came back by that answer.
		if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.mandate_effect_releases VALUES ('t1','card-1',$1,'sum(amount_eur)',$2)`, a.key, tooloutcome.SourceOwnerNo); err != nil {
			t.Fatal(err)
		}
	}
	rr := &reinstater{pool: pool}
	n := &notifier{}
	w := &tooloutcome.RefundWatch{Outcomes: tooloutcome.NewPostgres(pool), Releases: &releaser{pool: pool}, Reinstates: rr, Alerts: n, Now: func() time.Time { return at }}
	for i := 0; i < 2; i++ {
		if _, _, err := w.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(rr.got) != 1 || rr.got[0].ProposalID != "tc-found" || rr.got[0].Undoes != tooloutcome.SourceOwnerNo ||
		rr.got[0].Source != tooloutcome.SourceRefundFound || rr.got[0].EvidenceID != "re_1" || !rr.got[0].At.Equal(at) {
		t.Fatalf("reinstated: %+v, want tc-found once, on re_1", rr.got)
	}
	// The one that failed is told as any failed refund is; its room came
	// back by the "No" already, once per draw.
	if k := n.kinds(); len(n.got) != 2 || k["tc-found"] != tooloutcome.NoticeRefundAfterNo || k["tc-right"] != tooloutcome.NoticeRefundFailed {
		t.Fatalf("notices: %v (%d sent), want tc-found as %s and tc-right as %s, once each", k, len(n.got), tooloutcome.NoticeRefundAfterNo, tooloutcome.NoticeRefundFailed)
	}
}
