package agents_test

import (
	"context"
	"errors"
	"github.com/kiff/cloud/api/internal/testdb"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/tenant"
)

func TestNormalizeID(t *testing.T) {
	for _, ok := range []string{"refund-agent", " agent://refund-agent-prod ", "Claude e2e"} {
		if _, err := agents.NormalizeID(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "   ", strings.Repeat("a", 201), "bad\nname"} {
		if _, err := agents.NormalizeID(bad); !errors.Is(err, agents.ErrInvalidID) {
			t.Errorf("%q: err = %v, want ErrInvalidID", bad, err)
		}
	}
}

func TestMemoryStore(t *testing.T) { exerciseStore(t, agents.NewMemoryStore(), "t1") }

func TestPostgresStore(t *testing.T) {
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping Postgres-backed test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // idempotent
		if err := agents.ApplySchema(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	const tid = "pg-agents"
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.tenants (id, slug, display_name) VALUES ($1,$1,$1) ON CONFLICT (id) DO NOTHING`, tid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM cloud_meta.tenants WHERE id = $1`, tid) })
	_, _ = pool.Exec(ctx, `DELETE FROM cloud_meta.agents WHERE tenant_id = $1`, tid)
	exerciseStore(t, agents.NewPostgresStore(pool), tid)
}

func exerciseStore(t *testing.T, s agents.Store, tid string) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	if err := s.Create(ctx, agents.Agent{TenantID: tid, ID: "refund-agent", CreatedAt: at, CreatedBy: "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, agents.Agent{TenantID: tid, ID: "refund-agent", CreatedAt: at}); !errors.Is(err, agents.ErrExists) {
		t.Errorf("duplicate create: %v, want ErrExists", err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Ensure(ctx, agents.Agent{TenantID: tid, ID: "credit-agent", CreatedAt: at.Add(time.Minute)}); err != nil {
			t.Fatalf("ensure %d: %v", i, err)
		}
	}
	if err := s.Ensure(ctx, agents.Agent{TenantID: tid, ID: "refund-agent", CreatedAt: at.Add(time.Hour), CreatedBy: "later"}); err != nil {
		t.Fatal(err)
	}

	list, err := s.List(ctx, tid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "refund-agent" || list[0].CreatedBy != "owner" || list[1].ID != "credit-agent" {
		t.Fatalf("list = %+v", list)
	}
	if other, _ := s.List(ctx, tid+"-other"); len(other) != 0 {
		t.Errorf("another tenant sees %d agents", len(other))
	}
	// Another tenant that knows the id cannot archive the agent.
	if err := s.Archive(ctx, tid+"-other", "credit-agent", at); !errors.Is(err, agents.ErrNotFound) {
		t.Errorf("archive across tenants: %v, want ErrNotFound", err)
	}
	if list, _ := s.List(ctx, tid); list[1].ArchivedAt != nil {
		t.Error("another tenant archived the agent")
	}

	if err := s.Archive(ctx, tid, "refund-agent", at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.Archive(ctx, tid, "refund-agent", at.Add(3*time.Hour)); err != nil {
		t.Errorf("re-archive: %v", err)
	}
	if err := s.Archive(ctx, tid, "nobody", at); !errors.Is(err, agents.ErrNotFound) {
		t.Errorf("archive unknown: %v, want ErrNotFound", err)
	}
	list, _ = s.List(ctx, tid)
	if list[0].ArchivedAt == nil || !list[0].ArchivedAt.Equal(at.Add(2*time.Hour)) {
		t.Errorf("archived_at = %v, want the first archive time", list[0].ArchivedAt)
	}

	// A name, from the draft confirmed for the agent: set, replaced by the
	// next one, and kept by an Ensure that brings none.
	named := func() string {
		t.Helper()
		list, _ := s.List(ctx, tid)
		for _, a := range list {
			if a.ID == "credit-agent" {
				return a.Name
			}
		}
		return "?"
	}
	for _, c := range []struct{ name, want string }{{"Credits", "Credits"}, {"", "Credits"}, {"Credits 2", "Credits 2"}} {
		if err := s.Ensure(ctx, agents.Agent{TenantID: tid, ID: "credit-agent", CreatedAt: at, Name: c.name}); err != nil {
			t.Fatal(err)
		}
		if got := named(); got != c.want {
			t.Errorf("after Ensure with %q: name %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStoresRefuseOutOfRuleIDs(t *testing.T) {
	s := agents.NewMemoryStore()
	for _, bad := range []string{"", strings.Repeat("a", 201), "bad\nid", " refund-agent", "refund-agent "} {
		if err := s.Create(context.Background(), agents.Agent{TenantID: "t1", ID: bad}); !errors.Is(err, agents.ErrInvalidID) {
			t.Errorf("Create(%q) = %v", bad, err)
		}
		if err := s.Ensure(context.Background(), agents.Agent{TenantID: "t1", ID: bad}); !errors.Is(err, agents.ErrInvalidID) {
			t.Errorf("Ensure(%q) = %v", bad, err)
		}
	}
}

// "~" is refused in an agent id: an agent's trial Cards are held by
// "<agent>~trial", so an agent named that way would be another's trial.
func TestAnAgentIDCantNameATrial(t *testing.T) {
	for _, id := range []string{"rae~trial", "~", "a~b"} {
		if _, err := agents.NormalizeID(id); err == nil {
			t.Errorf("%q accepted", id)
		}
	}
	if _, err := agents.NormalizeID("rae-trial"); err != nil {
		t.Errorf("rae-trial: %v", err)
	}
}
