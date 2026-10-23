// Package connection manages the remote MCP tools an account connects to
// the gateway: adding, testing, listing and removing them,
// and storing their credentials encrypted.
package connection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/secret"
)

// ErrNotFound: the account has no connection by that name.
var ErrNotFound = errors.New("connection: not found")

// ErrExists: the account already has a connection by that name.
var ErrExists = errors.New("connection: a tool with this name is already connected")

// Connection is one connected tool, without its credential.
type Connection struct {
	Name string `json:"name"`
	// Title is the name people see: the vendor's MCP title, or its tool's
	// name in words.
	Title          string `json:"title"`
	Description    string `json:"description"`
	URL            string `json:"url"`
	UpstreamTool   string `json:"tool"`
	AmountArg      string `json:"amount_argument,omitempty"`
	AmountUnit     string `json:"amount_unit,omitempty"`
	AmountScale    int    `json:"amount_scale,omitempty"`
	IdempotencyArg string `json:"idempotency_argument,omitempty"`
	// ResourceArg is the string argument naming what a call is on,
	// sent to KIFF so a Card's resource scope can read it.
	ResourceArg string `json:"resource_argument,omitempty"`
	// AmountPath and ResourcePath say where the call carries the amount and
	// the resource when they are nested ("parameters.amount"); FixedArgs are
	// arguments KIFF always sends and the agent can't set.
	AmountPath        string          `json:"amount_path,omitempty"`
	ResourcePath      string          `json:"resource_path,omitempty"`
	FixedArgs         json.RawMessage `json:"fixed_arguments,omitempty"`
	OutcomeContract   string          `json:"outcome_contract,omitempty"`
	OutcomeLookupTool string          `json:"outcome_lookup_tool,omitempty"`
	// UnitLookup is the read that says which unit a call is in, so a call
	// in another unit than AmountUnit is refused.
	UnitLookup *gateway.UnitLookup `json:"unit_lookup,omitempty"`
	// ReadOnly is the owner's declaration that the tool only reads.
	ReadOnly bool `json:"read_only"`
	// Mode is who holds the downstream credential: sealed,
	// verify or relay. Audience is what the tool or relay expects in the
	// execution permit; empty for sealed.
	Mode          string          `json:"mode"`
	Audience      string          `json:"audience,omitempty"`
	HasCredential bool            `json:"has_credential"`
	InputSchema   json.RawMessage `json:"input_schema"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	// CreatedBy is the KIFF key that connected the tool, or "" for a
	// person. A connector key manages only its own.
	CreatedBy string `json:"-"`
}

// Store keeps connections in gateway.tools.
type Store struct {
	Pool *pgxpool.Pool
	Keys *secret.Keyring
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const cols = `name, description, upstream_url, upstream_tool, amount_arg, amount_unit, amount_scale, idempotency_arg, read_only,
	mode, audience, credential IS NOT NULL, input_schema, created_at, updated_at, outcome_contract, outcome_lookup_tool, resource_arg,
	amount_path, resource_path, fixed_args, created_by, title, unit_lookup`

func scanConn(row pgx.Row) (Connection, error) {
	var c Connection
	var schema, fixed, unit []byte
	err := row.Scan(&c.Name, &c.Description, &c.URL, &c.UpstreamTool, &c.AmountArg, &c.AmountUnit, &c.AmountScale, &c.IdempotencyArg, &c.ReadOnly,
		&c.Mode, &c.Audience, &c.HasCredential, &schema, &c.CreatedAt, &c.UpdatedAt, &c.OutcomeContract, &c.OutcomeLookupTool, &c.ResourceArg,
		&c.AmountPath, &c.ResourcePath, &fixed, &c.CreatedBy, &c.Title, &unit)
	c.InputSchema = schema
	if len(fixed) > 0 {
		c.FixedArgs = fixed
	}
	if err == nil && len(unit) > 0 {
		var l gateway.UnitLookup
		if err = json.Unmarshal(unit, &l); err == nil {
			c.UnitLookup = &l
		}
	}
	return c, err
}

func list(ctx context.Context, q querier, tenantID string) ([]Connection, error) {
	rows, err := q.Query(ctx, `SELECT `+cols+` FROM gateway.tools WHERE tenant_id = $1 ORDER BY name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		c, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// List returns the account's connections.
func (s *Store) List(ctx context.Context, tenantID string) ([]Connection, error) {
	return list(ctx, s.Pool, tenantID)
}

// Get returns one connection.
func (s *Store) Get(ctx context.Context, tenantID, name string) (Connection, error) {
	c, err := scanConn(s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM gateway.tools WHERE tenant_id = $1 AND name = $2`, tenantID, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	return c, err
}

// Credential decrypts the account's credential for a tool, or "" when the
// tool has none. A row sealed for another account or tool does not open.
func (s *Store) Credential(ctx context.Context, tenantID, name string) (string, error) {
	var keyID string
	var sealed []byte
	err := s.Pool.QueryRow(ctx, `SELECT credential_key_id, credential FROM gateway.tools WHERE tenant_id = $1 AND name = $2`,
		tenantID, name).Scan(&keyID, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil || sealed == nil {
		return "", err
	}
	return s.Keys.Open(tenantID, name, keyID, sealed)
}

// Tx is a change to an account's connections in progress.
type Tx struct {
	tx       pgx.Tx
	s        *Store
	tenantID string
}

// Change runs fn while holding the account's connection lock, then calls
// apply with the account's tools as they will be committed. If apply
// fails, nothing is committed, so the stored tools and the contract
// applied to apps/api cannot drift apart.
func (s *Store) Change(ctx context.Context, tenantID string, fn func(*Tx) error, apply func([]gateway.Tool) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('gateway.tools:' || $1))`, tenantID); err != nil {
		return err
	}
	t := &Tx{tx: tx, s: s, tenantID: tenantID}
	if err := fn(t); err != nil {
		return err
	}
	conns, err := list(ctx, tx, tenantID)
	if err != nil {
		return err
	}
	tools := make([]gateway.Tool, 0, len(conns))
	for _, c := range conns {
		tools = append(tools, gateway.Tool{Name: c.Name, UpstreamTool: c.UpstreamTool, AmountArg: c.AmountArg, AmountUnit: c.AmountUnit,
			AmountScale: c.AmountScale, ResourceArg: c.ResourceArg, ReadOnly: c.ReadOnly, Mode: c.Mode, Audience: c.Audience, OutcomeContract: c.OutcomeContract, OutcomeLookupTool: c.OutcomeLookupTool, ConnectionCreatedAt: c.CreatedAt,
			UnitLookup: c.UnitLookup})
	}
	if err := apply(tools); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Insert adds a connection, sealing credential when it is not empty.
func (t *Tx) Insert(ctx context.Context, c Connection, credential string) error {
	keyID, sealed, err := t.seal(c.Name, credential)
	if err != nil {
		return err
	}
	tag, err := t.tx.Exec(ctx, `
		INSERT INTO gateway.tools (tenant_id, name, description, input_schema, upstream_url, upstream_tool,
		                           amount_arg, idempotency_arg, credential_key_id, credential, read_only,
		                           amount_unit, amount_scale, mode, audience, outcome_contract, outcome_lookup_tool, resource_arg,
		                           amount_path, resource_path, fixed_args, created_by, title, unit_lookup)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)
		ON CONFLICT (tenant_id, name) DO NOTHING`,
		t.tenantID, c.Name, c.Description, []byte(c.InputSchema), c.URL, c.UpstreamTool,
		c.AmountArg, c.IdempotencyArg, keyID, sealed, c.ReadOnly, c.AmountUnit, c.AmountScale, modeOrSealed(c.Mode), c.Audience, c.OutcomeContract, c.OutcomeLookupTool, c.ResourceArg,
		c.AmountPath, c.ResourcePath, nullJSON(c.FixedArgs), c.CreatedBy, c.Title, unitJSON(c.UnitLookup))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrExists
	}
	return nil
}

// SetCredential replaces a connection's credential; "" removes it.
func (t *Tx) SetCredential(ctx context.Context, name, credential string) error {
	keyID, sealed, err := t.seal(name, credential)
	if err != nil {
		return err
	}
	tag, err := t.tx.Exec(ctx, `UPDATE gateway.tools SET credential_key_id = $3, credential = $4, updated_at = now()
		WHERE tenant_id = $1 AND name = $2`, t.tenantID, name, keyID, sealed)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetMode changes who holds a connection's downstream credential, and
// the URL it is reached at (the relay's, in relay mode). The caller
// replaces the credential in the same change.
func (t *Tx) SetMode(ctx context.Context, name, mode, audience, url string) error {
	tag, err := t.tx.Exec(ctx, `UPDATE gateway.tools SET mode = $3, audience = $4, upstream_url = $5, updated_at = now()
		WHERE tenant_id = $1 AND name = $2`, t.tenantID, name, modeOrSealed(mode), audience, url)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Get reads one connection inside the change, under the account's lock.
func (t *Tx) Get(ctx context.Context, name string) (Connection, error) {
	c, err := scanConn(t.tx.QueryRow(ctx, `SELECT `+cols+` FROM gateway.tools WHERE tenant_id = $1 AND name = $2`, t.tenantID, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	return c, err
}

// SetSettings changes what KIFF checks on a connection: its
// resource argument and its no-effect outcome contract with the status
// lookup that depends on it.
func (t *Tx) SetSettings(ctx context.Context, name, resourceArg, outcomeContract, outcomeLookupTool string) error {
	tag, err := t.tx.Exec(ctx, `UPDATE gateway.tools SET resource_arg = $3, outcome_contract = $4, outcome_lookup_tool = $5, updated_at = now()
		WHERE tenant_id = $1 AND name = $2`, t.tenantID, name, resourceArg, outcomeContract, outcomeLookupTool)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func modeOrSealed(m string) string {
	if m == "" {
		return gateway.ModeSealed
	}
	return m
}

// Delete removes a connection.
func (t *Tx) Delete(ctx context.Context, name string) error {
	tag, err := t.tx.Exec(ctx, `DELETE FROM gateway.tools WHERE tenant_id = $1 AND name = $2`, t.tenantID, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (t *Tx) seal(name, credential string) (string, []byte, error) {
	if credential == "" {
		return "", nil, nil
	}
	return t.s.Keys.Seal(t.tenantID, name, credential)
}

// Reseal re-encrypts every stored credential not sealed with the current
// key, for key rotation: add the new key first in the keyring, run this,
// then remove the old key.
func (s *Store) Reseal(ctx context.Context) (int, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT tenant_id, name, credential_key_id, credential FROM gateway.tools
		WHERE credential IS NOT NULL AND credential_key_id <> $1 FOR UPDATE`, s.Keys.Current())
	if err != nil {
		return 0, err
	}
	type row struct {
		tenant, name, keyID string
		sealed              []byte
	}
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.tenant, &r.name, &r.keyID, &r.sealed); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, r := range todo {
		plain, err := s.Keys.Open(r.tenant, r.name, r.keyID, r.sealed)
		if err != nil {
			return 0, fmt.Errorf("connection: %s/%s: %w", r.tenant, r.name, err)
		}
		keyID, sealed, err := s.Keys.Seal(r.tenant, r.name, plain)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE gateway.tools SET credential_key_id = $3, credential = $4
			WHERE tenant_id = $1 AND name = $2`, r.tenant, r.name, keyID, sealed); err != nil {
			return 0, err
		}
	}
	return len(todo), tx.Commit(ctx)
}

// nullJSON stores no fixed arguments as NULL.
func nullJSON(b json.RawMessage) any {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return []byte(b)
}

// unitJSON is a unit lookup as stored: NULL when there is none.
func unitJSON(l *gateway.UnitLookup) any {
	if l == nil {
		return nil
	}
	b, _ := json.Marshal(l)
	return b
}
