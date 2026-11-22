package payreports_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/payreports"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/testdb"
)

var ctx = context.Background()

// contract is what every Store does: a receipt's first report holds, a
// second keeps it, a report without its receipt, call or taf is refused,
// and each account reads only its own.
func contract(t *testing.T, s payreports.Store, tenantID string) {
	t.Helper()
	at := time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC)
	first := payreports.Report{TenantID: tenantID, TraceID: "tr-1", ProposalID: "p-1", AgentID: "refund-agent", ReportedAt: at, ReportedBy: "alice"}
	got, err := s.Report(ctx, first)
	if err != nil || !got.ReportedAt.Equal(at) || got.AgentID != "refund-agent" || got.ReportedBy != "alice" {
		t.Fatalf("first: %+v %v", got, err)
	}
	again := first
	again.ReportedAt, again.ReportedBy = at.Add(time.Hour), "bob"
	if got, err := s.Report(ctx, again); err != nil || !got.ReportedAt.Equal(at) || got.ReportedBy != "alice" {
		t.Fatalf("again: %+v %v", got, err)
	}
	for name, bad := range map[string]payreports.Report{
		"no receipt": {TenantID: tenantID, ProposalID: "p-2", AgentID: "a", ReportedAt: at},
		"no call":    {TenantID: tenantID, TraceID: "tr-2", AgentID: "a", ReportedAt: at},
		"no taf":     {TenantID: tenantID, TraceID: "tr-2", ProposalID: "p-2", ReportedAt: at},
		"no time":    {TenantID: tenantID, TraceID: "tr-2", ProposalID: "p-2", AgentID: "a"},
	} {
		if _, err := s.Report(ctx, bad); err == nil {
			t.Errorf("%s: kept", name)
		}
	}
	reported, err := s.Reported(ctx, tenantID, []string{"tr-1", "tr-2"})
	if err != nil || len(reported) != 1 || !reported["tr-1"].Equal(at) {
		t.Fatalf("reported: %v %v", reported, err)
	}
	if other, err := s.Reported(ctx, tenantID+"-other", []string{"tr-1"}); err != nil || len(other) != 0 {
		t.Fatalf("another account read it: %v %v", other, err)
	}
	if none, err := s.Reported(ctx, tenantID, nil); err != nil || len(none) != 0 {
		t.Fatalf("none asked: %v %v", none, err)
	}
}

func TestMemoryStore(t *testing.T) { contract(t, payreports.NewMemoryStore(), "t1") }

func TestPostgresStore(t *testing.T) {
	url := testdb.URL(t)
	if url == "" {
		t.Skip(testdb.EnvVar + " not set")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := payreports.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM cloud_meta.payment_reports WHERE tenant_id = 'pg-payreports'`)
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.tenants (id, slug, status) VALUES ('pg-payreports', 'pg-payreports', 'active') ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	contract(t, payreports.NewPostgresStore(pool), "pg-payreports")
}
