package auth_test

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/agentdrafts"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
)

// A live key claimed from a no-tools draft before setup_reader existed gets
// it (kiff/tafs#138): the key the draft names, not one that only looks like
// it. No other key changes, and running it again changes none.
func TestBackfillSetupReaderOnlyOnNoToolsDraftKeys(t *testing.T) {
	pool := requirePostgres(t)
	truncateAPIKeys(t, pool)
	ctx := context.Background()
	tnt := tenant.NewID()
	seedTenant(t, pool, tnt)
	store := auth.NewPostgresAPIKeyStore(pool)
	now := time.Now().UTC()
	revoked := now.Add(-time.Hour)
	if err := agentdrafts.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, k := range []auth.APIKeyRecord{
		{ID: "k-setup", Label: "draft Setup", AgentID: "setup"},
		{ID: "k-taf", Label: "draft Rae", AgentID: "rae", Roles: []string{auth.RoleGatewayAgent}},
		{ID: "k-plain", Label: "eval", AgentID: "chargebacks-eval"},
		{ID: "k-lookalike", Label: "draft Setup", AgentID: "setup-2"}, // labelled like one, no draft names it
		{ID: "k-revoked", Label: "draft Old setup", AgentID: "setup-old"},
	} {
		k.Tenant, k.Subject, k.KeyHash, k.CreatedAt = tnt, "alice", sha256.Sum256([]byte(k.ID)), now
		if err := store.Insert(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Revoke(ctx, "k-revoked", revoked); err != nil {
		t.Fatal(err)
	}
	// The drafts the keys were claimed from: Setup's and the old one with no
	// Cards, Rae's with one.
	for _, d := range []struct{ id, agent, key, cards string }{
		{"drf_setup", "setup", "k-setup", "[]"}, {"drf_old", "setup-old", "k-revoked", "[]"}, {"drf_rae", "rae", "k-taf", `[{"id":"c1"}]`},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.agent_drafts (id, tenant_id, agent_id, cards, version, status, expires_at, claim_hash, key_id)
			VALUES ($1, $2, $3, $4::jsonb, 'v1', 'claimed', now(), 'h', $5)`, d.id+"-"+tnt, tnt, d.agent, d.cards, d.key); err != nil {
			t.Fatal(err)
		}
	}
	n, err := auth.BackfillSetupReader(ctx, pool)
	if err != nil || n != 1 {
		t.Fatalf("backfill: %d %v", n, err)
	}
	for id, want := range map[string]string{
		"k-setup": `["setup_reader"]`, "k-taf": `["gateway_agent"]`, "k-plain": "null", "k-lookalike": "null", "k-revoked": "null",
	} {
		var roles string
		if err := pool.QueryRow(ctx, `SELECT roles::text FROM cloud_meta.api_keys WHERE id = $1`, id).Scan(&roles); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if roles != want {
			t.Errorf("%s: roles %s, want %s", id, roles, want)
		}
	}
	if n, err := auth.BackfillSetupReader(ctx, pool); err != nil || n != 0 {
		t.Fatalf("again: %d %v", n, err)
	}
}
