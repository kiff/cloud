package mandate_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

// "Did this happen?" lists a money call KIFF can't prove (lost, or refused
// without proof of no effect) once, and the owner's "No" gives its Card's
// room back through a release, while "Yes" keeps it counted (connection profiles: "Did this happen?"). Answered, it is asked no more.
func TestPostgres_DidThisHappen(t *testing.T) {
	p := requirePostgres(t)
	tenant := "did-happen"
	seedTenant(t, p, tenant)
	ctx := context.Background()
	if _, err := p.Exec(ctx, `DROP SCHEMA IF EXISTS gateway CASCADE; CREATE SCHEMA gateway;
	 CREATE TABLE gateway.invocations (key text PRIMARY KEY, tenant_id text, agent_id text, tool text, state text,
	 tool_outcome text, updated_at timestamptz, no_effect_evidence jsonb, probation boolean NOT NULL DEFAULT false, profile_revision bigint, measure jsonb);
	 CREATE TABLE gateway.tools (tenant_id text, name text, title text, amount_unit text, amount_scale int);
	 CREATE TABLE gateway.profiles (tenant_id text, revision bigint, profile jsonb)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = p.Exec(context.Background(), `DROP SCHEMA IF EXISTS gateway CASCADE`) })
	s := mandate.NewPostgresStore(p)
	m := homeCard("happened", 50000, 500000)
	m.TenantID = tenant
	m.Aggregates = append(m.Aggregates, mandate.Aggregate{Quantity: m.Aggregates[0].Quantity, Limit: 500000, Window: mandate.WindowRollingHour, Measure: mandate.MeasureEffects})
	if err := s.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(s, nil, at(base))
	r := refund(40000)
	for _, op := range []string{"lost", "refused", "proven", "done", "probation"} {
		if d, err := a.Authorize(ctx, tenant, op, r); err != nil || !d.Allowed() {
			t.Fatal(op, d, err)
		}
	}
	for _, row := range []struct {
		key, state, outcome, evidence string
		probation                     bool
	}{
		{"lost", "unknown", "", "", false}, {"refused", "forwarded", "error", "", false},
		{"proven", "forwarded", "error", `{"contract":"terminal_no_effect_v1"}`, false}, {"done", "forwarded", "done", "", false},
		{"probation", "forwarded", "done", "", true},
	} {
		var ev any
		if row.evidence != "" {
			ev = row.evidence
		}
		if _, err := p.Exec(ctx, `INSERT INTO gateway.invocations VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,1)`,
			row.key, tenant, r.Subject, r.Action, row.state, row.outcome, base.Add(time.Second), ev, row.probation); err != nil {
			t.Fatal(err)
		}
	}
	// The calls were decided under revision 1, in EUR at scale 2; the
	// connection has since moved to JPY at scale 0 and another title. The
	// question keeps the revision's words.
	// The "lost" call was fixed with its measure when it was decided, as the
	// gateway does for every call: a unit read per call has no unit in the
	// profile, and the connection may be gone.
	if _, err := p.Exec(ctx, `UPDATE gateway.invocations SET measure = '{"unit":"EUR","scale":2,"title":"Refund a payment"}', profile_revision = NULL WHERE key = 'lost'`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO gateway.tools VALUES ($1,$2,'Another title','JPY',0)`, tenant, r.Action); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO gateway.profiles VALUES ($1, 1, jsonb_build_object('operations', jsonb_build_array(
		jsonb_build_object('tool', $2::text, 'title', 'Refund a payment', 'amount', jsonb_build_object('unit', 'EUR', 'scale', 2)))))`, tenant, r.Action); err != nil {
		t.Fatal(err)
	}
	o := tooloutcome.NewPostgres(p)
	calls, err := o.Unproven(ctx, tenant, "")
	if err != nil || len(calls) != 3 {
		t.Fatalf("unproven: %+v %v", calls, err)
	}
	for _, c := range calls {
		if c.Proposal == "probation" && c.Status != tooloutcome.Probation {
			t.Fatalf("probation call %+v", c)
		}
		if (c.Proposal != "lost" && c.Proposal != "refused" && c.Proposal != "probation") || c.Amount != 40000 || c.Unit != "EUR" || c.Title != "Refund a payment" ||
			c.Words != "Refund a payment: 400.00 EUR." || c.StatusWords == "" {
			t.Fatalf("call %+v", c)
		}
	}
	// No: the room comes back, from the owner's word.
	if _, err := s.ReleaseEffects(ctx, mandate.NoEffectEvidence{TenantID: tenant, ProposalID: "lost", Subject: r.Subject, Action: r.Action,
		Source: "owner:did_this_happen", EvidenceID: "happened-lost", Reason: "the owner said it didn't happen", At: base.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RecordHappened(ctx, tenant, "lost", tooloutcome.HappenedAnswer{Happened: false, AnsweredBy: "owner", AnsweredAt: base.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	// Yes: it stays counted, and a later "No" answers the first answer.
	if _, err := o.RecordHappened(ctx, tenant, "refused", tooloutcome.HappenedAnswer{Happened: true, AnsweredBy: "owner", AnsweredAt: base.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := o.RecordHappened(ctx, tenant, "refused", tooloutcome.HappenedAnswer{Happened: false, AnsweredBy: "x", AnsweredAt: base.Add(3 * time.Second)}); !got.Happened {
		t.Fatalf("a second answer replaced the first: %+v", got)
	}
	if _, err := o.RecordHappened(ctx, tenant, "probation", tooloutcome.HappenedAnswer{Happened: true, AnsweredBy: "owner", AnsweredAt: base.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if calls, err := o.Unproven(ctx, tenant, ""); err != nil || len(calls) != 0 {
		t.Fatalf("answered calls still asked: %+v %v", calls, err)
	}
	effects := m.Aggregates[len(m.Aggregates)-1]
	b, err := mandate.ReadLimitBalance(ctx, s, tenant, m, effects, base.Add(3*time.Second), "")
	if err != nil || b.Released != 40000 || b.Net != 4*40000 {
		t.Fatalf("balance %+v %v: want the lost call's room back only", b, err)
	}
}
