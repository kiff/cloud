// Package dbrole gives the gateway its own Postgres login. The gateway
// reads and writes only the gateway schema, so it should not connect as the
// database's master user, which can read every tenant's rows, API key
// hashes and OAuth tokens in cloud_meta. Setup runs with the master user's
// login before each restart of the gateway, and gives the gateway's role a fresh
// password that only the gateway's process receives.
package dbrole

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Role is the gateway's login in production.
const Role = "kiff_gateway"

// Schema is the only schema the gateway role owns or can use.
const Schema = "gateway"

var roleName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// Setup creates role (or resets its password), makes it own the gateway
// schema and everything in it, and makes the connecting user a member of
// it. Membership keeps the master user able to reach gateway rows, which
// account erasure needs: the API deletes a tenant's gateway rows
// when it deprovisions the tenant. The role gets no grant anywhere else, so
// it cannot read cloud_meta or a tenant schema. Safe to run on every deploy.
func Setup(ctx context.Context, pool *pgxpool.Pool, role, password string) error {
	if !roleName.MatchString(role) {
		return fmt.Errorf("dbrole: invalid role name %q", role)
	}
	if len(password) < 32 {
		return errors.New("dbrole: password must be at least 32 characters")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	ident := pgx.Identifier{role}.Sanitize()
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := tx.Exec(ctx, `CREATE ROLE `+ident+` LOGIN`); err != nil {
			return fmt.Errorf("dbrole: create role: %w", err)
		}
	}
	// ALTER ROLE takes no bind parameters; format %L quotes the password
	// as a literal inside Postgres, so it never passes through Go string
	// building.
	var alter string
	if err := tx.QueryRow(ctx,
		`SELECT format('ALTER ROLE %I WITH LOGIN NOCREATEDB NOCREATEROLE PASSWORD %L', $1::text, $2::text)`,
		role, password).Scan(&alter); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, alter); err != nil {
		return fmt.Errorf("dbrole: set password: %w", err)
	}
	if _, err := tx.Exec(ctx, `GRANT `+ident+` TO CURRENT_USER`); err != nil {
		return fmt.Errorf("dbrole: grant membership: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+Schema); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `ALTER SCHEMA `+Schema+` OWNER TO `+ident); err != nil {
		return fmt.Errorf("dbrole: schema owner: %w", err)
	}
	// Tables, views and free-standing sequences. Indexes and owned
	// sequences follow their table.
	rows, err := tx.Query(ctx, `
		SELECT c.relname FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f')
		  AND pg_get_userbyid(c.relowner) <> $2
		  AND NOT (c.relkind = 'S' AND EXISTS (
		      SELECT 1 FROM pg_depend d WHERE d.objid = c.oid AND d.deptype IN ('a', 'i')))`,
		Schema, role)
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, name := range names {
		if _, err := tx.Exec(ctx, `ALTER TABLE `+pgx.Identifier{Schema, name}.Sanitize()+` OWNER TO `+ident); err != nil {
			return fmt.Errorf("dbrole: owner of %s: %w", name, err)
		}
	}
	return tx.Commit(ctx)
}
