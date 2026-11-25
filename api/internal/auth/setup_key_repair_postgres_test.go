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

// Setup's key minted gateway_agent from a draft of Setup's own Cards
// (kiff/tafs#852) is narrowed to exactly setup_reader: the key a draft for
// Setup, the agent every app gives it, names. A taf's own gateway key (one
// beside a setup_reader key from its first draft with no tools included),
// an OAuth grant's key no draft names, a revoked key and a key with more
// roles are untouched, and running it again changes none.
func TestRepairSetupKeysOnlySetupsGatewayKeys(t *testing.T) {
	pool := requirePostgres(t)
	truncateAPIKeys(t, pool)
	ctx := context.Background()
	tnt, other := tenant.NewID(), tenant.NewID()
	seedTenant(t, pool, tnt)
	seedTenant(t, pool, other)
	store := auth.NewPostgresAPIKeyStore(pool)
	now := time.Now().UTC()
	if err := agentdrafts.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	gw, sr := []string{auth.RoleGatewayAgent}, []string{auth.RoleSetupReader}
	for _, k := range []auth.APIKeyRecord{
		{ID: "k-coach", Tenant: tnt, Label: "draft Coach", AgentID: "setup", Roles: gw},
		{ID: "k-coach-other", Tenant: other, Label: "draft Coach", AgentID: "setup", Roles: gw},
		{ID: "k-nora-reader", Tenant: tnt, Label: "draft Nora", AgentID: "nora", Roles: sr},
		{ID: "k-nora", Tenant: tnt, Label: "draft Nora", AgentID: "nora", Roles: gw},
		{ID: "k-taf", Tenant: tnt, Label: "draft Rae", AgentID: "rae", Roles: gw},
		{ID: "k-grant", Tenant: tnt, Label: "Claude (sign-in)", AgentID: "setup", Roles: gw},
		{ID: "k-revoked", Tenant: tnt, Label: "draft Coach", AgentID: "setup", Roles: gw},
		{ID: "k-wider", Tenant: tnt, Label: "draft Coach", AgentID: "setup", Roles: []string{auth.RoleGatewayAgent, "ops_agent"}},
		{ID: "k-other-agent", Tenant: tnt, Label: "draft Coach", AgentID: "rae", Roles: gw}, // a draft for setup names it
	} {
		k.Subject, k.KeyHash, k.CreatedAt = "alice", sha256.Sum256([]byte(k.ID)), now
		if err := store.Insert(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Revoke(ctx, "k-revoked", now); err != nil {
		t.Fatal(err)
	}
	for _, d := range []struct{ tenant, agent, key, cards string }{
		{tnt, "setup", "k-coach", `[{"id":"coach-tasks"}]`},
		{other, "setup", "k-coach-other", `[{"id":"coach-tasks"}]`},
		{tnt, "nora", "k-nora-reader", "[]"},
		{tnt, "nora", "k-nora", `[{"id":"nora-refund"}]`},
		{tnt, "rae", "k-taf", `[{"id":"c1"}]`},
		{tnt, "setup", "k-revoked", `[{"id":"coach-tasks"}]`},
		{tnt, "setup", "k-wider", `[{"id":"coach-tasks"}]`},
		{tnt, "setup", "k-other-agent", `[{"id":"coach-tasks"}]`},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.agent_drafts (id, tenant_id, agent_id, cards, version, status, expires_at, claim_hash, key_id)
			VALUES ($1, $2, $3, $4::jsonb, 'v1', 'claimed', now(), 'h', $5)`, "drf_"+d.key+"-"+d.tenant, d.tenant, d.agent, d.cards, d.key); err != nil {
			t.Fatal(err)
		}
	}

	repaired, err := auth.RepairSetupKeys(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, k := range repaired {
		got[k.KeyID] = k.Tenant + "/" + k.AgentID
	}
	want := map[string]string{"k-coach": tnt + "/setup", "k-coach-other": other + "/setup"}
	if len(got) != len(want) {
		t.Fatalf("repaired %v, want %v", got, want)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: repaired as %q, want %q", id, got[id], w)
		}
	}
	for id, roles := range map[string]string{
		"k-coach": `["setup_reader"]`, "k-coach-other": `["setup_reader"]`,
		"k-nora-reader": `["setup_reader"]`, "k-nora": `["gateway_agent"]`, "k-taf": `["gateway_agent"]`,
		"k-grant": `["gateway_agent"]`, "k-revoked": `["gateway_agent"]`, "k-wider": `["gateway_agent", "ops_agent"]`,
		"k-other-agent": `["gateway_agent"]`,
	} {
		var have string
		if err := pool.QueryRow(ctx, `SELECT roles::text FROM cloud_meta.api_keys WHERE id = $1`, id).Scan(&have); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if have != roles {
			t.Errorf("%s: roles %s, want %s", id, have, roles)
		}
	}
	if again, err := auth.RepairSetupKeys(ctx, pool); err != nil || len(again) != 0 {
		t.Fatalf("again: %v %v", again, err)
	}
}
