package codesignin

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

// Setting is a person's: whether they sign in with a code sent by email,
// and when that last changed. The zero Setting is off.
type Setting struct {
	EmailCode bool
	ChangedAt time.Time
}

// Code is a code that was sent, as its HMAC only.
type Code struct {
	ID string
	// Flow and Address are keyed hashes of the browser's flow and of the
	// address the code went to.
	Flow, Address string
	Subject       string
	MAC           []byte
	ExpiresAt     time.Time
}

// Store keeps the settings, the codes, the rate limits' counts and the
// code sign-ins step-up refuses.
type Store interface {
	Setting(ctx context.Context, subject string) (Setting, error)
	SetSetting(ctx context.Context, subject string, s Setting) error
	// EraseSetting drops a person's setting, with their sign-in.
	EraseSetting(ctx context.Context, subject string) error

	// Add counts one more in key's window and answers the window's count;
	// Count answers it without adding.
	Add(ctx context.Context, key string, window time.Time) (int, error)
	Count(ctx context.Context, key string, window time.Time) (int, error)

	// PutCode keeps c, in place of any code for the same flow and address.
	PutCode(ctx context.Context, c Code) error
	// FindCode is the live code for a flow and address.
	FindCode(ctx context.Context, flow, address string, now time.Time) (Code, bool, error)
	// UseCode deletes a live code and reports whether it was there: only one
	// caller ever gets true for a code.
	UseCode(ctx context.Context, id string, now time.Time) (bool, error)
	// DropCodes deletes every code of a person.
	DropCodes(ctx context.Context, subject string) error

	// NoteSignIn records that the person signed in with a code at at;
	// LastSignIn reads it back.
	NoteSignIn(ctx context.Context, subject string, at time.Time) error
	LastSignIn(ctx context.Context, subject string) (time.Time, bool, error)

	// Sweep deletes what's past keeping: lapsed codes, counts older than a
	// day, and code sign-ins older than UseKept.
	Sweep(ctx context.Context, now time.Time) error
}

//go:embed sql/code_sign_in.sql
var schemaSQL string

// ApplySchema runs the code sign-in DDL. Idempotent.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "codesignin/code_sign_in", schemaSQL); err != nil {
		return fmt.Errorf("apply code sign-in schema: %w", err)
	}
	return nil
}

// Eraser erases a person's code sign-in records with their sign-in: the
// account erasure's IdentityEraser, around the provider's (Next, which may
// be nil).
type Eraser struct {
	Store Store
	Next  interface {
		EraseIdentity(ctx context.Context, subject string) error
	}
}

// EraseIdentity drops the person's setting, codes and code sign-ins, then
// their sign-in.
func (e Eraser) EraseIdentity(ctx context.Context, subject string) error {
	if err := e.Store.EraseSetting(ctx, subject); err != nil {
		return err
	}
	if e.Next == nil {
		return nil
	}
	return e.Next.EraseIdentity(ctx, subject)
}

// Postgres keeps everything in cloud_meta.
type Postgres struct{ pool *pgxpool.Pool }

// NewPostgres wraps pool; the caller ran ApplySchema.
func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (p *Postgres) Setting(ctx context.Context, subject string) (Setting, error) {
	var s Setting
	err := p.pool.QueryRow(ctx, `SELECT email_code, changed_at FROM cloud_meta.signin_settings WHERE subject = $1`, subject).
		Scan(&s.EmailCode, &s.ChangedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Setting{}, nil
	}
	if err != nil {
		return Setting{}, fmt.Errorf("codesignin: read setting: %w", err)
	}
	return s, nil
}

func (p *Postgres) SetSetting(ctx context.Context, subject string, s Setting) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO cloud_meta.signin_settings (subject, email_code, changed_at) VALUES ($1, $2, $3)
		ON CONFLICT (subject) DO UPDATE SET email_code = EXCLUDED.email_code, changed_at = EXCLUDED.changed_at`,
		subject, s.EmailCode, s.ChangedAt)
	if err != nil {
		return fmt.Errorf("codesignin: write setting: %w", err)
	}
	return nil
}

func (p *Postgres) EraseSetting(ctx context.Context, subject string) error {
	if _, err := p.pool.Exec(ctx, `DELETE FROM cloud_meta.signin_settings WHERE subject = $1`, subject); err != nil {
		return fmt.Errorf("codesignin: erase setting: %w", err)
	}
	if err := p.DropCodes(ctx, subject); err != nil {
		return err
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM cloud_meta.signin_code_uses WHERE subject = $1`, subject); err != nil {
		return fmt.Errorf("codesignin: erase code sign-ins: %w", err)
	}
	return nil
}

func (p *Postgres) Add(ctx context.Context, key string, window time.Time) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx, `INSERT INTO cloud_meta.signin_counts (key, window_start, n) VALUES ($1, $2, 1)
		ON CONFLICT (key, window_start) DO UPDATE SET n = cloud_meta.signin_counts.n + 1 RETURNING n`, key, window).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("codesignin: count: %w", err)
	}
	return n, nil
}

func (p *Postgres) Count(ctx context.Context, key string, window time.Time) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx, `SELECT n FROM cloud_meta.signin_counts WHERE key = $1 AND window_start = $2`, key, window).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("codesignin: read count: %w", err)
	}
	return n, nil
}

func (p *Postgres) PutCode(ctx context.Context, c Code) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO cloud_meta.signin_codes (id, flow, address, subject, mac, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (flow, address) DO UPDATE SET id = EXCLUDED.id, subject = EXCLUDED.subject, mac = EXCLUDED.mac, expires_at = EXCLUDED.expires_at`,
		c.ID, c.Flow, c.Address, c.Subject, c.MAC, c.ExpiresAt)
	if err != nil {
		return fmt.Errorf("codesignin: keep code: %w", err)
	}
	return nil
}

func (p *Postgres) FindCode(ctx context.Context, flow, address string, now time.Time) (Code, bool, error) {
	c := Code{Flow: flow, Address: address}
	err := p.pool.QueryRow(ctx, `SELECT id, subject, mac, expires_at FROM cloud_meta.signin_codes
		WHERE flow = $1 AND address = $2 AND expires_at > $3`, flow, address, now).Scan(&c.ID, &c.Subject, &c.MAC, &c.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Code{}, false, nil
	}
	if err != nil {
		return Code{}, false, fmt.Errorf("codesignin: find code: %w", err)
	}
	return c, true, nil
}

func (p *Postgres) UseCode(ctx context.Context, id string, now time.Time) (bool, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM cloud_meta.signin_codes WHERE id = $1 AND expires_at > $2`, id, now)
	if err != nil {
		return false, fmt.Errorf("codesignin: use code: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (p *Postgres) DropCodes(ctx context.Context, subject string) error {
	if _, err := p.pool.Exec(ctx, `DELETE FROM cloud_meta.signin_codes WHERE subject = $1`, subject); err != nil {
		return fmt.Errorf("codesignin: drop codes: %w", err)
	}
	return nil
}

func (p *Postgres) NoteSignIn(ctx context.Context, subject string, at time.Time) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO cloud_meta.signin_code_uses (subject, at) VALUES ($1, $2)
		ON CONFLICT (subject) DO UPDATE SET at = GREATEST(cloud_meta.signin_code_uses.at, EXCLUDED.at)`, subject, at)
	if err != nil {
		return fmt.Errorf("codesignin: note sign-in: %w", err)
	}
	return nil
}

func (p *Postgres) LastSignIn(ctx context.Context, subject string) (time.Time, bool, error) {
	var at time.Time
	err := p.pool.QueryRow(ctx, `SELECT at FROM cloud_meta.signin_code_uses WHERE subject = $1`, subject).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("codesignin: read code sign-in: %w", err)
	}
	return at, true, nil
}

func (p *Postgres) Sweep(ctx context.Context, now time.Time) error {
	for _, q := range []struct {
		sql string
		at  time.Time
	}{
		{`DELETE FROM cloud_meta.signin_codes WHERE expires_at <= $1`, now},
		{`DELETE FROM cloud_meta.signin_counts WHERE window_start < $1`, now.Add(-CountsKept)},
		{`DELETE FROM cloud_meta.signin_code_uses WHERE at < $1`, now.Add(-UseKept)},
	} {
		if _, err := p.pool.Exec(ctx, q.sql, q.at); err != nil {
			return fmt.Errorf("codesignin: sweep: %w", err)
		}
	}
	return nil
}

// Memory keeps everything in memory: local runs and tests.
type Memory struct {
	mu       sync.Mutex
	settings map[string]Setting
	counts   map[string]int
	codes    map[string]Code
	uses     map[string]time.Time
}

// NewMemory is an empty store.
func NewMemory() *Memory {
	return &Memory{settings: map[string]Setting{}, counts: map[string]int{}, codes: map[string]Code{}, uses: map[string]time.Time{}}
}

func countKey(key string, window time.Time) string {
	return key + "@" + window.UTC().Format(time.RFC3339)
}

func (m *Memory) Setting(_ context.Context, subject string) (Setting, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings[subject], nil
}

func (m *Memory) SetSetting(_ context.Context, subject string, s Setting) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings[subject] = s
	return nil
}

func (m *Memory) EraseSetting(_ context.Context, subject string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.settings, subject)
	delete(m.uses, subject)
	for id, c := range m.codes {
		if c.Subject == subject {
			delete(m.codes, id)
		}
	}
	return nil
}

func (m *Memory) Add(_ context.Context, key string, window time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts[countKey(key, window)]++
	return m.counts[countKey(key, window)], nil
}

func (m *Memory) Count(_ context.Context, key string, window time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counts[countKey(key, window)], nil
}

func (m *Memory) PutCode(_ context.Context, c Code) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, old := range m.codes {
		if old.Flow == c.Flow && old.Address == c.Address {
			delete(m.codes, id)
		}
	}
	m.codes[c.ID] = c
	return nil
}

func (m *Memory) FindCode(_ context.Context, flow, address string, now time.Time) (Code, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.codes {
		if c.Flow == flow && c.Address == address && c.ExpiresAt.After(now) {
			return c, true, nil
		}
	}
	return Code{}, false, nil
}

func (m *Memory) UseCode(_ context.Context, id string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.codes[id]
	if !ok || !c.ExpiresAt.After(now) {
		return false, nil
	}
	delete(m.codes, id)
	return true, nil
}

func (m *Memory) DropCodes(_ context.Context, subject string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, c := range m.codes {
		if c.Subject == subject {
			delete(m.codes, id)
		}
	}
	return nil
}

func (m *Memory) NoteSignIn(_ context.Context, subject string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if at.After(m.uses[subject]) {
		m.uses[subject] = at
	}
	return nil
}

func (m *Memory) LastSignIn(_ context.Context, subject string) (time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	at, ok := m.uses[subject]
	return at, ok, nil
}

func (m *Memory) Sweep(_ context.Context, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, c := range m.codes {
		if !c.ExpiresAt.After(now) {
			delete(m.codes, id)
		}
	}
	for s, at := range m.uses {
		if at.Before(now.Add(-UseKept)) {
			delete(m.uses, s)
		}
	}
	return nil
}

// Codes is how many codes the store holds (tests).
func (m *Memory) Codes() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.codes)
}
