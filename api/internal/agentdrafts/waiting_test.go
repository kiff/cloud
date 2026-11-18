package agentdrafts_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/agentdrafts"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/testdb"
)

// Waiting lists only an account's own drafts still waiting for someone to
// confirm them: not confirmed, discarded or expired ones, and not another
// account's. Newest first.
func checkWaiting(t *testing.T, s agentdrafts.Store, tenantID, otherTenant string) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	cards := json.RawMessage(`[{"id":"c","subject":"a"}]`)
	draft := func(id, tenantID string, created time.Time, ttl time.Duration) agentdrafts.D {
		_, hash := agentdrafts.NewClaimSecret()
		return agentdrafts.D{ID: id, TenantID: tenantID, AgentID: "a-" + id, Label: id, Cards: cards,
			Version: agentdrafts.VersionOf("a-"+id, id, cards), Status: agentdrafts.Draft, CreatedBy: "setup-agent",
			CreatedAt: created, ExpiresAt: created.Add(ttl), ClaimHash: hash}
	}
	for _, d := range []agentdrafts.D{
		draft("drf_w_old", tenantID, now.Add(-2*time.Hour), agentdrafts.DefaultTTL),
		draft("drf_w_new", tenantID, now.Add(-time.Hour), agentdrafts.DefaultTTL),
		draft("drf_w_expired", tenantID, now.Add(-25*time.Hour), agentdrafts.DefaultTTL),
		draft("drf_w_confirmed", tenantID, now.Add(-time.Minute), agentdrafts.DefaultTTL),
		draft("drf_w_discarded", tenantID, now.Add(-time.Minute), agentdrafts.DefaultTTL),
		draft("drf_w_other", otherTenant, now.Add(-time.Minute), agentdrafts.DefaultTTL),
	} {
		if err := s.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	confirmed := draft("drf_w_confirmed", tenantID, now.Add(-time.Minute), agentdrafts.DefaultTTL)
	if _, err := s.Confirm(ctx, tenantID, confirmed.ID, confirmed.Version, "alice", "admin", nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Discard(ctx, tenantID, "drf_w_discarded"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Waiting(ctx, tenantID, now)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range got {
		ids = append(ids, d.ID)
	}
	if len(ids) != 2 || ids[0] != "drf_w_new" || ids[1] != "drf_w_old" {
		t.Fatalf("waiting = %v", ids)
	}
}

func TestMemoryWaitingDrafts(t *testing.T) {
	checkWaiting(t, agentdrafts.NewMemoryStore(), "t1", "t2")
}

func TestPostgresWaitingDrafts(t *testing.T) {
	url := testdb.URL(t)
	if url == "" {
		t.Skip(testdb.EnvVar + " not set")
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
	if err := agentdrafts.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"pg-waiting", "pg-waiting-2"} {
		_, _ = pool.Exec(ctx, `DELETE FROM cloud_meta.agent_drafts WHERE tenant_id = $1`, id)
		if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.tenants (id, slug, status) VALUES ($1, $1, 'active') ON CONFLICT (id) DO NOTHING`, id); err != nil {
			t.Fatal(err)
		}
	}
	checkWaiting(t, agentdrafts.NewPostgresStore(pool), "pg-waiting", "pg-waiting-2")
}
