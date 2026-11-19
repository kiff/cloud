package tenant_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/newdevice"
	"github.com/kiff/cloud/api/internal/operatoraudit"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/support"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// The decision against the real schemas: a hard delete erases the
// tenant's Cards, keys, tickets, receipts and person-to-account links,
// keeps its billing records and operator audit, removes the wallets of its
// members (left with no account) and no one else's, reduces the tenant row, and leaves other
// tenants alone.
func TestDeprovisionAppliesCloudMetaDecision(t *testing.T) {
	pool := requirePostgres(t)
	truncateTenants(t, pool)
	dropTenantSchemas(t, pool)
	ctx := context.Background()
	defer func() { dropTenantSchemas(t, pool); truncateTenants(t, pool) }()
	for name, apply := range map[string]func(context.Context, *pgxpool.Pool) error{
		"keys": auth.ApplyAPIKeysSchema, "wallets": auth.ApplyWalletsSchema, "subjects": tenant.ApplySubjectTenantsSchema,
		"mandate": mandate.ApplySchema, "support": support.ApplySchema, "receipt": receipt.ApplySchema,
		"billing": billing.ApplySchema, "audit": operatoraudit.ApplySchema, "usage": usage.ApplySchema,
		"devices": newdevice.ApplySchema, "sign-ins": auth.ApplyDeviceSignInsSchema,
	} {
		if err := apply(ctx, pool); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	store := tenant.NewPostgresStore(pool)
	prov := tenant.NewProvisioner(pool, store)
	a, b := sampleTenant("e953-a"), sampleTenant("e953-b")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, tt := range []tenant.Tenant{a, b} {
		tt.DisplayName = "Acme " + tt.Slug
		if err := store.Create(ctx, tt); err != nil {
			t.Fatal(err)
		}
		if err := prov.Provision(ctx, tt.ID); err != nil {
			t.Fatal(err)
		}
		id := tt.ID
		exec(`INSERT INTO cloud_meta.mandates (id, tenant_id, subject) VALUES ('card-1', $1, 'agent-a')`, id)
		exec(`INSERT INTO cloud_meta.mandate_draws (tenant_id, mandate_id, proposal_id, quantity_key, amount, drawn_at) VALUES ($1, 'card-1', 'p-1', 'count', 1, now())`, id)
		exec(`INSERT INTO cloud_meta.api_keys (id, tenant_id, subject, key_hash) VALUES ($1 || '-key', $1, 'k', $1 || '-hash')`, id)
		exec(`INSERT INTO cloud_meta.support_tickets (id, tenant_id, title, body) VALUES ($1 || '-t', $1, 'help', 'my email is x@y.z')`, id)
		exec(`INSERT INTO cloud_meta.support_ticket_messages (id, ticket_id, author_kind, body) VALUES ($1 || '-m', $1 || '-t', 'user', 'more')`, id)
		exec(`INSERT INTO cloud_meta.receipts (tenant_id, trace_id, schema_version, entity_id, entity_type, action_name, issued_at, causal_chain_hash, body)
			VALUES ($1, 'tr-1', '1', 'e', 'Order', 'REFUND', now(), 'h', '{}')`, id)
		exec(`INSERT INTO cloud_meta.billing_usage_events (operation_id, tenant_id, outcome, occurred_at) VALUES ($1 || '-op', $1, 'allowed', now())`, id)
		exec(`INSERT INTO cloud_meta.tenant_usage (tenant_id, counter_name, entity_type, hour_bucket) VALUES ($1, 'decide', 'Order', date_trunc('hour', now()))`, id)
		exec(`INSERT INTO cloud_meta.operator_audit (id, operator_subject, action, target_tenant_id) VALUES ($1 || '-audit', 'op', 'set-plan', $1)`, id)
	}
	// alice belongs to A, bob to B (a person belongs to one account).
	exec(`INSERT INTO cloud_meta.subject_tenants (subject, tenant_id) VALUES ('alice', $1), ('bob', $2)`, a.ID, b.ID)
	exec(`INSERT INTO cloud_meta.user_wallets (subject, address, chain_id, linked_at) VALUES ('alice', '0xa', 1, now()), ('bob', '0xb', 1, now())`)
	exec(`INSERT INTO cloud_meta.wallet_nonces (nonce, subject, address, chain_id, issued_at, expires_at) VALUES
		('n-alice', 'alice', '0xa', 1, now(), now() + interval '5 minutes'), ('n-bob', 'bob', '0xb', 1, now(), now() + interval '5 minutes')`)
	exec(`INSERT INTO cloud_meta.known_devices (subject, client_hash, first_seen, last_seen) VALUES ('alice', '\x01', now(), now()), ('bob', '\x02', now(), now())`)
	exec(`INSERT INTO cloud_meta.new_device_emails (session_id, subject, expires_at) VALUES ('sess-alice', 'alice', now() + interval '5 minutes'), ('sess-bob', 'bob', now() + interval '5 minutes')`)
	exec(`INSERT INTO cloud_meta.device_sign_ins (device_hash, user_code, subject, relink_session, created_at, expires_at) VALUES ('h-alice', 'AAAA-AAAA', 'alice', 'dsess-alice', now(), now() + interval '5 minutes'), ('h-bob', 'BBBB-BBBB', 'bob', 'dsess-bob', now(), now() + interval '5 minutes')`)
	if err := store.SetMetadata(ctx, a.ID, tenant.MetadataKeyDeletedAt, "2026-10-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMetadata(ctx, a.ID, tenant.MetadataKeyPlan, "growth"); err != nil {
		t.Fatal(err)
	}

	if err := prov.Deprovision(ctx, a.ID); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	for _, table := range []string{"mandates", "mandate_draws", "api_keys", "support_tickets", "receipts", "subject_tenants"} {
		if n := count(`SELECT count(*) FROM cloud_meta.`+table+` WHERE tenant_id = $1`, a.ID); n != 0 {
			t.Errorf("erased tenant still has %d row(s) in %s", n, table)
		}
		if n := count(`SELECT count(*) FROM cloud_meta.`+table+` WHERE tenant_id = $1`, b.ID); n == 0 {
			t.Errorf("other tenant lost its rows in %s", table)
		}
	}
	if n := count(`SELECT count(*) FROM cloud_meta.support_ticket_messages WHERE ticket_id = $1`, a.ID+"-t"); n != 0 {
		t.Errorf("ticket messages kept: %d", n)
	}
	for _, table := range []string{"billing_usage_events", "tenant_usage"} {
		if n := count(`SELECT count(*) FROM cloud_meta.`+table+` WHERE tenant_id = $1`, a.ID); n != 1 {
			t.Errorf("retained %s has %d row(s), want 1", table, n)
		}
	}
	if n := count(`SELECT count(*) FROM cloud_meta.operator_audit WHERE target_tenant_id = $1`, a.ID); n != 1 {
		t.Errorf("operator audit not retained: %d", n)
	}
	if n := count(`SELECT count(*) FROM cloud_meta.user_wallets WHERE subject = 'alice'`); n != 0 {
		t.Error("the wallet of a person left with no account was kept")
	}
	if n := count(`SELECT count(*) FROM cloud_meta.wallet_nonces WHERE subject = 'alice'`); n != 0 {
		t.Error("the wallet nonces of a person left with no account were kept")
	}
	if n := count(`SELECT count(*) FROM cloud_meta.wallet_nonces WHERE subject = 'bob'`); n != 1 {
		t.Error("another account's member lost their wallet nonce")
	}
	if n := count(`SELECT count(*) FROM cloud_meta.user_wallets WHERE subject = 'bob'`); n != 1 {
		t.Error("the wallet of another account's member was removed")
	}
	// The devices a person signed in on, and an email waiting to tell them of
	// one, go with them; another account's member keeps theirs.
	for _, table := range []string{"known_devices", "new_device_emails", "device_sign_ins"} {
		if n := count(`SELECT count(*) FROM cloud_meta.` + table + ` WHERE subject = 'alice'`); n != 0 {
			t.Errorf("%s of a person left with no account was kept", table)
		}
		if n := count(`SELECT count(*) FROM cloud_meta.` + table + ` WHERE subject = 'bob'`); n != 1 {
			t.Errorf("another account's member lost their %s", table)
		}
	}
	got, err := store.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != tenant.StatusDeleted || got.DisplayName != "" || got.Metadata[tenant.MetadataKeyPlan] != nil ||
		got.Metadata[tenant.MetadataKeyDeletedAt] == nil {
		t.Errorf("tenant row not reduced: %+v", got)
	}
	if other, _ := store.Get(ctx, b.ID); other.DisplayName == "" {
		t.Error("the other tenant's row was reduced")
	}
	// Idempotent: a second run (the re-run path) succeeds and changes nothing.
	if err := prov.Deprovision(ctx, a.ID); err != nil {
		t.Fatalf("re-run: %v", err)
	}
}
