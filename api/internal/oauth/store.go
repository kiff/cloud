package oauth

import (
	"context"
	_ "embed"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/oauth.sql
var schemaSQL string

// ApplySchema creates the OAuth tables. Idempotent.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	err := pgschema.Apply(ctx, pool, "oauth/oauth", schemaSQL)
	return err
}

// ErrNotFound: no such row, or it is no longer usable.
var ErrNotFound = errors.New("oauth: not found")

// Client is a registered OAuth client.
type Client struct {
	ID           string
	Name         string
	RedirectURIs []string
	CreatedAt    time.Time
}

// Request is an authorization request waiting for consent.
type Request struct {
	ID            string
	ClientID      string
	RedirectURI   string
	State         string
	CodeChallenge string
	Resource      string
	// LinkID is the connect link the request came through; "" on the
	// generic URL (Amendment 1).
	LinkID    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Grant is one agent connection. It owns exactly one key.
type Grant struct {
	ID       string
	TenantID string
	AgentID  string
	// KeyID and KeyHash are set when the code is exchanged (RedeemedAt).
	KeyID      string
	KeyHash    [32]byte
	RedeemedAt *time.Time
	ClientID   string
	ClientName string
	GrantedBy  string
	Resource   string
	LinkID     string // "" on the generic URL
	// Method is how the connection was made: "sign-in" or "code".
	Method      string
	CreatedAt   time.Time
	LastUsedAt  time.Time
	EndedAt     *time.Time
	EndedReason string
}

// Code is a single-use authorization code.
type Code struct {
	Hash          [32]byte
	TenantID      string
	GrantID       string
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	ExpiresAt     time.Time
}

// Token is an access or refresh token.
type Token struct {
	Hash      [32]byte
	TenantID  string
	GrantID   string
	Kind      string // "access" or "refresh"
	ExpiresAt *time.Time
}

// Store is the Postgres store.
type Store struct{ pool *pgxpool.Pool }

// NewStore returns a store on pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) CreateClient(ctx context.Context, c Client) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO cloud_meta.oauth_clients (id, name, redirect_uris, created_at)
		VALUES ($1, $2, $3, $4)`, c.ID, c.Name, c.RedirectURIs, c.CreatedAt)
	return err
}

// UpsertClient stores a client described by its metadata document (CIMD),
// keyed by its URL, refreshing its name and redirects.
func (s *Store) UpsertClient(ctx context.Context, c Client) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO cloud_meta.oauth_clients (id, name, redirect_uris, created_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, redirect_uris = EXCLUDED.redirect_uris`,
		c.ID, c.Name, c.RedirectURIs, c.CreatedAt)
	return err
}

func (s *Store) Client(ctx context.Context, id string) (Client, error) {
	var c Client
	err := s.pool.QueryRow(ctx, `SELECT id, name, redirect_uris, created_at FROM cloud_meta.oauth_clients WHERE id = $1`, id).
		Scan(&c.ID, &c.Name, &c.RedirectURIs, &c.CreatedAt)
	return c, notFound(err)
}

func (s *Store) CreateRequest(ctx context.Context, r Request) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO cloud_meta.oauth_requests
		(id, client_id, redirect_uri, state, code_challenge, resource, link_id, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		r.ID, r.ClientID, r.RedirectURI, r.State, r.CodeChallenge, r.Resource, r.LinkID, r.CreatedAt, r.ExpiresAt)
	return err
}

// Request returns an unused, unexpired request.
func (s *Store) Request(ctx context.Context, id string, now time.Time) (Request, error) {
	var r Request
	err := s.pool.QueryRow(ctx, `SELECT id, client_id, redirect_uri, state, code_challenge, resource, link_id, created_at, expires_at
		FROM cloud_meta.oauth_requests WHERE id = $1 AND used_at IS NULL AND expires_at > $2`, id, now).
		Scan(&r.ID, &r.ClientID, &r.RedirectURI, &r.State, &r.CodeChallenge, &r.Resource, &r.LinkID, &r.CreatedAt, &r.ExpiresAt)
	return r, notFound(err)
}

// UseRequest marks a request used; only one caller wins.
func (s *Store) UseRequest(ctx context.Context, id string, now time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE cloud_meta.oauth_requests SET used_at = $2
		WHERE id = $1 AND used_at IS NULL AND expires_at > $2`, id, now)
	return tag.RowsAffected() == 1, err
}

const grantCols = `id, tenant_id, agent_id, key_id, key_hash, redeemed_at, client_id, client_name, granted_by, resource,
	link_id, method, created_at, last_used_at, ended_at, ended_reason`

func scanGrant(row pgx.Row) (Grant, error) {
	var g Grant
	var hash []byte
	err := row.Scan(&g.ID, &g.TenantID, &g.AgentID, &g.KeyID, &hash, &g.RedeemedAt, &g.ClientID, &g.ClientName, &g.GrantedBy,
		&g.Resource, &g.LinkID, &g.Method, &g.CreatedAt, &g.LastUsedAt, &g.EndedAt, &g.EndedReason)
	copy(g.KeyHash[:], hash)
	return g, notFound(err)
}

func insertGrant(ctx context.Context, tx pgx.Tx, g Grant) error {
	if g.Method == "" {
		g.Method = "sign-in"
	}
	_, err := tx.Exec(ctx, `INSERT INTO cloud_meta.oauth_grants
		(id, tenant_id, agent_id, client_id, client_name, granted_by, resource, link_id, method, created_at, last_used_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		g.ID, g.TenantID, g.AgentID, g.ClientID, g.ClientName, g.GrantedBy, g.Resource, g.LinkID, g.Method,
		g.CreatedAt, g.LastUsedAt)
	return err
}

// CreateGrantWithCode stores a pending grant and its code together.
func (s *Store) CreateGrantWithCode(ctx context.Context, g Grant, c Code) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := insertGrant(ctx, tx, g); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO cloud_meta.oauth_codes
		(hash, tenant_id, grant_id, client_id, redirect_uri, code_challenge, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		c.Hash[:], c.TenantID, c.GrantID, c.ClientID, c.RedirectURI, c.CodeChallenge, c.ExpiresAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Grant(ctx context.Context, id string) (Grant, error) {
	return scanGrant(s.pool.QueryRow(ctx, `SELECT `+grantCols+` FROM cloud_meta.oauth_grants WHERE id = $1`, id))
}

// RedeemGrant records the grant's key on its first exchange. Only one
// caller wins, and only for a pending, live grant.
func (s *Store) RedeemGrant(ctx context.Context, id, keyID string, keyHash [32]byte, now time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE cloud_meta.oauth_grants SET key_id = $2, key_hash = $3, redeemed_at = $4, last_used_at = $4
		WHERE id = $1 AND redeemed_at IS NULL AND ended_at IS NULL`, id, keyID, keyHash[:], now)
	return tag.RowsAffected() == 1, err
}

// LatestGrant is the most recent connected grant of a client by name in
// a tenant, ended or not: the agent it connected as before.
func (s *Store) LatestGrant(ctx context.Context, tenantID, clientName string) (Grant, error) {
	return scanGrant(s.pool.QueryRow(ctx, `SELECT `+grantCols+` FROM cloud_meta.oauth_grants
		WHERE tenant_id = $1 AND client_name = $2 AND redeemed_at IS NOT NULL ORDER BY created_at DESC LIMIT 1`, tenantID, clientName))
}

// LiveGrants are the connected grants of a client by name for one agent
// that have not ended.
func (s *Store) LiveGrants(ctx context.Context, tenantID, clientName, agentID string) ([]Grant, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+grantCols+` FROM cloud_meta.oauth_grants
		WHERE tenant_id = $1 AND client_name = $2 AND agent_id = $3 AND redeemed_at IS NOT NULL AND ended_at IS NULL`,
		tenantID, clientName, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// EndGrant ends a live grant and its tokens. It reports whether this
// call ended it.
func (s *Store) EndGrant(ctx context.Context, id, reason string, now time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE cloud_meta.oauth_grants SET ended_at = $2, ended_reason = $3
		WHERE id = $1 AND ended_at IS NULL`, id, now, reason)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE cloud_meta.oauth_tokens SET used_at = COALESCE(used_at, $2), expires_at = $2
		WHERE grant_id = $1`, id, now); err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, tx.Commit(ctx)
}

// TouchGrant records use, at most once a minute.
func (s *Store) TouchGrant(ctx context.Context, id string, now time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE cloud_meta.oauth_grants SET last_used_at = $2
		WHERE id = $1 AND last_used_at < $2::timestamptz - interval '1 minute'`, id, now)
	return err
}

// UseCode consumes a code. first is false when it was already used.
func (s *Store) UseCode(ctx context.Context, hash [32]byte, now time.Time) (Code, bool, error) {
	var c Code
	var h []byte
	var used *time.Time
	err := s.pool.QueryRow(ctx, `SELECT hash, tenant_id, grant_id, client_id, redirect_uri, code_challenge, expires_at, used_at
		FROM cloud_meta.oauth_codes WHERE hash = $1`, hash[:]).
		Scan(&h, &c.TenantID, &c.GrantID, &c.ClientID, &c.RedirectURI, &c.CodeChallenge, &c.ExpiresAt, &used)
	if err != nil {
		return c, false, notFound(err)
	}
	copy(c.Hash[:], h)
	tag, err := s.pool.Exec(ctx, `UPDATE cloud_meta.oauth_codes SET used_at = $2 WHERE hash = $1 AND used_at IS NULL`, hash[:], now)
	if err != nil {
		return c, false, err
	}
	return c, tag.RowsAffected() == 1, nil
}

func (s *Store) InsertTokens(ctx context.Context, ts ...Token) error {
	for _, t := range ts {
		if _, err := s.pool.Exec(ctx, `INSERT INTO cloud_meta.oauth_tokens (hash, tenant_id, grant_id, kind, expires_at)
			VALUES ($1, $2, $3, $4, $5)`, t.Hash[:], t.TenantID, t.GrantID, t.Kind, t.ExpiresAt); err != nil {
			return err
		}
	}
	return nil
}

// Token returns a token by hash, with whether it was already used (a
// rotated refresh token).
func (s *Store) Token(ctx context.Context, hash [32]byte) (Token, bool, error) {
	var t Token
	var h []byte
	var used *time.Time
	err := s.pool.QueryRow(ctx, `SELECT hash, tenant_id, grant_id, kind, expires_at, used_at
		FROM cloud_meta.oauth_tokens WHERE hash = $1`, hash[:]).Scan(&h, &t.TenantID, &t.GrantID, &t.Kind, &t.ExpiresAt, &used)
	copy(t.Hash[:], h)
	return t, used != nil, notFound(err)
}

// UseToken marks a refresh token used; only one caller wins.
func (s *Store) UseToken(ctx context.Context, hash [32]byte, now time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE cloud_meta.oauth_tokens SET used_at = $2 WHERE hash = $1 AND used_at IS NULL`, hash[:], now)
	return tag.RowsAffected() == 1, err
}

// Link is a connect link (Amendment 1).
type Link struct {
	ID        string
	TenantID  string
	AgentID   string
	CreatedBy string
	CreatedAt time.Time
	RevokedAt *time.Time
	RevokedBy string
}

const linkCols = `id, tenant_id, agent_id, created_by, created_at, revoked_at, revoked_by`

func scanLink(row pgx.Row) (Link, error) {
	var l Link
	err := row.Scan(&l.ID, &l.TenantID, &l.AgentID, &l.CreatedBy, &l.CreatedAt, &l.RevokedAt, &l.RevokedBy)
	return l, notFound(err)
}

func (s *Store) CreateLink(ctx context.Context, l Link) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO cloud_meta.oauth_connect_links (id, tenant_id, agent_id, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5)`, l.ID, l.TenantID, l.AgentID, l.CreatedBy, l.CreatedAt)
	return err
}

func (s *Store) Link(ctx context.Context, id string) (Link, error) {
	return scanLink(s.pool.QueryRow(ctx, `SELECT `+linkCols+` FROM cloud_meta.oauth_connect_links WHERE id = $1`, id))
}

// Links are an agent's links, newest first, revoked ones included.
func (s *Store) Links(ctx context.Context, tenantID, agentID string) ([]Link, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+linkCols+` FROM cloud_meta.oauth_connect_links
		WHERE tenant_id = $1 AND agent_id = $2 ORDER BY created_at DESC`, tenantID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// RevokeLink revokes a live link of the tenant; only one caller wins.
func (s *Store) RevokeLink(ctx context.Context, tenantID, id, by string, now time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE cloud_meta.oauth_connect_links SET revoked_at = $3, revoked_by = $4
		WHERE id = $1 AND tenant_id = $2 AND revoked_at IS NULL`, id, tenantID, now, by)
	return tag.RowsAffected() == 1, err
}

// LinkGrants are the grants made through a link that have not ended,
// pending ones included.
func (s *Store) LinkGrants(ctx context.Context, linkID string) ([]Grant, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+grantCols+` FROM cloud_meta.oauth_grants
		WHERE link_id = $1 AND ended_at IS NULL`, linkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// TenantGrants are a tenant's connected grants (redeemed) that are live,
// or ended at or after since, newest first.
func (s *Store) TenantGrants(ctx context.Context, tenantID string, since time.Time) ([]Grant, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+grantCols+` FROM cloud_meta.oauth_grants
		WHERE tenant_id = $1 AND redeemed_at IS NOT NULL AND (ended_at IS NULL OR ended_at >= $2)
		ORDER BY created_at DESC LIMIT 500`, tenantID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Device is a device authorization (RFC 8628).
type Device struct {
	Hash       [32]byte
	UserCode   string
	ClientID   string
	Resource   string
	LinkID     string
	Status     string // pending, claimed, approved, redeemed, denied, failed
	TenantID   string
	GrantID    string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastPollAt *time.Time
}

const deviceCols = `hash, user_code, client_id, resource, link_id, status, tenant_id, grant_id, created_at, expires_at, last_poll_at`

func scanDevice(row pgx.Row) (Device, error) {
	var d Device
	var h []byte
	err := row.Scan(&h, &d.UserCode, &d.ClientID, &d.Resource, &d.LinkID, &d.Status, &d.TenantID, &d.GrantID,
		&d.CreatedAt, &d.ExpiresAt, &d.LastPollAt)
	copy(d.Hash[:], h)
	return d, notFound(err)
}

func (s *Store) CreateDevice(ctx context.Context, d Device) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO cloud_meta.oauth_device_codes
		(hash, user_code, client_id, resource, link_id, status, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7)`,
		d.Hash[:], d.UserCode, d.ClientID, d.Resource, d.LinkID, d.CreatedAt, d.ExpiresAt)
	return err
}

func (s *Store) DeviceByUserCode(ctx context.Context, userCode string) (Device, error) {
	return scanDevice(s.pool.QueryRow(ctx, `SELECT `+deviceCols+` FROM cloud_meta.oauth_device_codes WHERE user_code = $1`, userCode))
}

func (s *Store) DeviceByHash(ctx context.Context, hash [32]byte) (Device, error) {
	return scanDevice(s.pool.QueryRow(ctx, `SELECT `+deviceCols+` FROM cloud_meta.oauth_device_codes WHERE hash = $1`, hash[:]))
}

// SetDeviceStatus moves a device from one status to another; only one
// caller wins.
func (s *Store) SetDeviceStatus(ctx context.Context, hash [32]byte, from, to string, now time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE cloud_meta.oauth_device_codes SET status = $3
		WHERE hash = $1 AND status = $2 AND expires_at > $4`, hash[:], from, to, now)
	return tag.RowsAffected() == 1, err
}

// ApproveDevice stores the device's pending grant and marks it approved,
// together.
func (s *Store) ApproveDevice(ctx context.Context, hash [32]byte, g Grant) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := insertGrant(ctx, tx, g); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE cloud_meta.oauth_device_codes SET status = 'approved', tenant_id = $2, grant_id = $3
		WHERE hash = $1 AND status = 'claimed'`, hash[:], g.TenantID, g.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("oauth: device is no longer being approved")
	}
	return tx.Commit(ctx)
}

// PollDevice records a poll and returns the previous poll time.
func (s *Store) PollDevice(ctx context.Context, hash [32]byte, now time.Time) (*time.Time, error) {
	var prev *time.Time
	err := s.pool.QueryRow(ctx, `UPDATE cloud_meta.oauth_device_codes d SET last_poll_at = $2
		FROM (SELECT last_poll_at FROM cloud_meta.oauth_device_codes WHERE hash = $1 FOR UPDATE) old
		WHERE d.hash = $1 RETURNING old.last_poll_at`, hash[:], now).Scan(&prev)
	return prev, notFound(err)
}

// EndedRetention is how long a sign-in connection is kept after it ends:
// the Connections view shows ended ones for this long, then they are
// deleted. Account erasure deletes them at once.
const EndedRetention = 30 * 24 * time.Hour

// Purged counts what PurgeEnded deleted.
type Purged struct {
	Grants, Tokens, Codes, Devices, Keys, Links int64
}

// PurgeEnded deletes the sign-in connections that ended before cutoff, and
// those never redeemed and created before it (their 60-second code
// expired, so they never became connections), with their tokens, codes,
// device codes and their own API key (each grant owns one, revoked when the
// grant ends; only a revoked key is deleted); then revoked connect links
// older than cutoff that no connection names any more. Live connections
// and live links are never touched. It runs in one transaction; grants are
// locked in id order, so concurrent purges on several API instances wait
// rather than deadlock.
func (s *Store) PurgeEnded(ctx context.Context, cutoff time.Time) (Purged, error) {
	var p Purged
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id, key_id FROM cloud_meta.oauth_grants
		WHERE (ended_at IS NOT NULL AND ended_at < $1)
		   OR (ended_at IS NULL AND redeemed_at IS NULL AND created_at < $1)
		ORDER BY id
		FOR UPDATE`, cutoff)
	if err != nil {
		return p, err
	}
	var ids, keyIDs []string
	for rows.Next() {
		var id, keyID string
		if err := rows.Scan(&id, &keyID); err != nil {
			rows.Close()
			return p, err
		}
		ids = append(ids, id)
		if keyID != "" {
			keyIDs = append(keyIDs, keyID)
		}
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	if len(keyIDs) > 0 {
		tag, err := tx.Exec(ctx, `DELETE FROM cloud_meta.api_keys WHERE id = ANY($1) AND revoked_at IS NOT NULL`, keyIDs)
		if err != nil {
			return p, err
		}
		p.Keys = tag.RowsAffected()
	}
	if len(ids) > 0 {
		for _, step := range []struct {
			sql string
			n   *int64
		}{
			{`DELETE FROM cloud_meta.oauth_tokens WHERE grant_id = ANY($1)`, &p.Tokens},
			{`DELETE FROM cloud_meta.oauth_codes WHERE grant_id = ANY($1)`, &p.Codes},
			{`DELETE FROM cloud_meta.oauth_device_codes WHERE grant_id = ANY($1)`, &p.Devices},
			{`DELETE FROM cloud_meta.oauth_grants WHERE id = ANY($1)`, &p.Grants},
		} {
			tag, err := tx.Exec(ctx, step.sql, ids)
			if err != nil {
				return p, err
			}
			*step.n = tag.RowsAffected()
		}
	}
	tag, err := tx.Exec(ctx, `DELETE FROM cloud_meta.oauth_connect_links l
		WHERE l.revoked_at IS NOT NULL AND l.revoked_at < $1
		  AND NOT EXISTS (SELECT 1 FROM cloud_meta.oauth_grants g WHERE g.link_id = l.id)`, cutoff)
	if err != nil {
		return p, err
	}
	p.Links = tag.RowsAffected()
	return p, tx.Commit(ctx)
}

// ExpiredGrace is how long a sign-in request, an authorization code or a
// device code that never became a connection is kept after it expires
// long enough to explain a failed sign-in, then it is deleted.
const ExpiredGrace = 24 * time.Hour

// Expired counts what PurgeExpired deleted.
type Expired struct {
	Requests, Codes, Devices int64
}

// PurgeExpired deletes what expired before cutoff and can never be used
// again: sign-in requests, authorization codes (used or not; a
// connection keeps its tokens, not its codes), and device codes that never
// became a connection (pending, denied or failed ones; those that did go
// with their connection in PurgeEnded). A request, code or device code
// still inside its window is never touched.
func (s *Store) PurgeExpired(ctx context.Context, cutoff time.Time) (Expired, error) {
	var e Expired
	for _, step := range []struct {
		sql string
		n   *int64
	}{
		{`DELETE FROM cloud_meta.oauth_requests WHERE expires_at < $1`, &e.Requests},
		{`DELETE FROM cloud_meta.oauth_codes WHERE expires_at < $1`, &e.Codes},
		{`DELETE FROM cloud_meta.oauth_device_codes WHERE grant_id = '' AND expires_at < $1`, &e.Devices},
	} {
		tag, err := s.pool.Exec(ctx, step.sql, cutoff)
		if err != nil {
			return e, err
		}
		*step.n = tag.RowsAffected()
	}
	return e, nil
}

// RunRetention deletes ended sign-in connections older than EndedRetention,
// and what expired more than ExpiredGrace ago, now and then every interval,
// until ctx is done.
func RunRetention(ctx context.Context, s *Store, logger *slog.Logger, interval time.Duration, now func() time.Time) {
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	run := func() {
		p, err := s.PurgeEnded(ctx, now().Add(-EndedRetention))
		if err != nil {
			if ctx.Err() == nil {
				logger.WarnContext(ctx, "oauth: retention purge failed", slog.String("err", err.Error()))
			}
			return
		}
		if p != (Purged{}) {
			logger.InfoContext(ctx, "oauth: retention purge", slog.Int64("grants", p.Grants), slog.Int64("tokens", p.Tokens),
				slog.Int64("codes", p.Codes), slog.Int64("device_codes", p.Devices), slog.Int64("keys", p.Keys),
				slog.Int64("links", p.Links))
		}
		e, err := s.PurgeExpired(ctx, now().Add(-ExpiredGrace))
		if err != nil {
			if ctx.Err() == nil {
				logger.WarnContext(ctx, "oauth: expired purge failed", slog.String("err", err.Error()))
			}
			return
		}
		if e != (Expired{}) {
			logger.InfoContext(ctx, "oauth: expired purge", slog.Int64("requests", e.Requests),
				slog.Int64("codes", e.Codes), slog.Int64("device_codes", e.Devices))
		}
	}
	run()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
