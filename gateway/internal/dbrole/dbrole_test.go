package dbrole_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/gateway/internal/dbrole"
	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/testdb"
)

func randHex(t *testing.T, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// connectAs opens a pool on the test database as another role.
func connectAs(t *testing.T, base *pgxpool.Pool, role, password string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(base.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.User, cfg.ConnConfig.Password = role, password
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// TestGatewayRoleSeesOnlyTheGatewaySchema mirrors production: a
// non-superuser master role with CREATEROLE owns cloud_meta and the
// existing gateway tables, then hands the gateway schema to the gateway
// role. The gateway role must run its schema and queries, must be refused
// cloud_meta, and the master role must still delete gateway rows for
// account erasure.
func TestGatewayRoleSeesOnlyTheGatewaySchema(t *testing.T) {
	ctx := context.Background()
	super := testdb.Pool(t)
	suffix := randHex(t, 4)
	admin, gw := "kiff_admin_t"+suffix, "kiff_gateway_t"+suffix
	adminPW, gwPW := randHex(t, 16), randHex(t, 16)
	db := super.Config().ConnConfig.Database

	for _, q := range []string{
		`CREATE ROLE ` + admin + ` LOGIN CREATEROLE CREATEDB PASSWORD '` + adminPW + `'`,
		`GRANT CREATE ON DATABASE "` + db + `" TO ` + admin,
		`DROP SCHEMA IF EXISTS gateway CASCADE`,
		`DROP SCHEMA IF EXISTS cloud_meta CASCADE`,
	} {
		if _, err := super.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		for _, q := range []string{
			`DROP SCHEMA IF EXISTS gateway CASCADE`,
			`DROP SCHEMA IF EXISTS cloud_meta CASCADE`,
			`DROP OWNED BY ` + gw + ` CASCADE`,
			`DROP OWNED BY ` + admin + ` CASCADE`,
			`DROP ROLE IF EXISTS ` + gw,
			`DROP ROLE IF EXISTS ` + admin,
		} {
			if _, err := super.Exec(ctx, q); err != nil {
				t.Logf("cleanup %s: %v", q, err)
			}
		}
	})

	adminPool := connectAs(t, super, admin, adminPW)
	if err := invocation.ApplySchema(ctx, adminPool); err != nil {
		t.Fatalf("schema as admin: %v", err)
	}
	if _, err := adminPool.Exec(ctx, `CREATE SCHEMA cloud_meta;
		CREATE TABLE cloud_meta.api_keys (tenant_id TEXT, key_hash TEXT);
		INSERT INTO cloud_meta.api_keys VALUES ('t1', 'h1')`); err != nil {
		t.Fatal(err)
	}

	if err := dbrole.Setup(ctx, adminPool, gw, gwPW); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := dbrole.Setup(ctx, adminPool, gw, gwPW); err != nil {
		t.Fatalf("setup is not repeatable: %v", err)
	}

	gwPool := connectAs(t, super, gw, gwPW)
	if err := invocation.ApplySchema(ctx, gwPool); err != nil {
		t.Fatalf("schema as gateway role: %v", err)
	}
	if _, err := gwPool.Exec(ctx, `SELECT count(*) FROM gateway.invocations`); err != nil {
		t.Fatalf("gateway role reads its own schema: %v", err)
	}
	var pgErr *pgconn.PgError
	_, err := gwPool.Exec(ctx, `SELECT count(*) FROM cloud_meta.api_keys`)
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("gateway role reading cloud_meta: want permission denied (42501), got %v", err)
	}
	if _, err := adminPool.Exec(ctx, `DELETE FROM gateway.invocations WHERE tenant_id = 'none'`); err != nil {
		t.Fatalf("master role can no longer erase gateway rows: %v", err)
	}
	var owner string
	if err := super.QueryRow(ctx, `SELECT tableowner FROM pg_tables WHERE schemaname = 'gateway' AND tablename = 'invocations'`).Scan(&owner); err != nil || owner != gw {
		t.Fatalf("gateway.invocations owner = %q, %v; want %s", owner, err, gw)
	}
}

func TestSetupRejectsBadInput(t *testing.T) {
	if err := dbrole.Setup(context.Background(), nil, "Robert'); DROP", "x"); err == nil {
		t.Fatal("want error for an invalid role name")
	}
	if err := dbrole.Setup(context.Background(), nil, "kiff_gateway", "short"); err == nil {
		t.Fatal("want error for a short password")
	}
}
