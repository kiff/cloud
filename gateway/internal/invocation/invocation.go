// Package invocation records the tool calls the gateway has seen and
// the state each one is in.
//
// It is the gateway's only guard against doing an action twice: apps/api
// answers `allowed` to every retry of an approved proposal, so "forward at
// most once" holds only because exactly one caller can move an invocation
// into StateForwarding.
package invocation

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// State is where an invocation is.
type State string

const (
	StateDeciding   State = "deciding"
	StateHeld       State = "held"
	StateRefused    State = "refused"
	StateForwarding State = "forwarding"
	StateForwarded  State = "forwarded"
	StateFailed     State = "failed"
	// StateUnknown: sent to the tool, response lost. Never re-sent.
	StateUnknown State = "unknown"
)

// ErrOperationReused: the same kiff_operation_id with other arguments.
var ErrOperationReused = errors.New("invocation: operation id reused with different arguments")

// ErrNotFound: no invocation with that key.
var ErrNotFound = errors.New("invocation: not found")

// Invocation is one tool call.
type Invocation struct {
	Key          string
	TenantID     string
	CredentialID string
	AgentID      string
	Tool         string
	OperationID  string
	ArgsHash     string
	Arguments    json.RawMessage
	State        State
	ExceptionID  string
	ReviewURL    string
	Message      string
	// Reasons are the machine-readable codes for how the call ended:
	// apps/api's for a refusal, the gateway's own for a failure.
	Reasons []string
	// HoldExpiresAt is when a held call stops waiting for the owner,
	// as apps/api reported it.
	HoldExpiresAt *time.Time
	Result        json.RawMessage
	// ToolError is the tool's own error message when a delivered call
	// came back as an error; empty otherwise. Kept after the
	// result is purged.
	ToolError string
	// ToolOutcome is "done" or "error" once the gateway recorded how a
	// delivered call ended; "" when it was not recorded.
	ToolOutcome      string
	NoEffectEvidence json.RawMessage
	CreatedAt        time.Time
	ExecutionBinding json.RawMessage
	UpdatedAt        time.Time
}

// Call identifies a tool call before it has a key.
type Call struct {
	TenantID     string
	CredentialID string
	AgentID      string
	Tool         string
	// OperationID is the agent's kiff_operation_id, or "".
	OperationID string
	// Arguments are the tool's arguments without kiff_operation_id.
	Arguments json.RawMessage
	// ReadOnly is the owner's declaration that the tool only reads. Such
	// a call without an operation id joins an identical call only while
	// that call is unfinished; after it finishes, an identical call is a
	// new invocation, so it is never answered with a stale result.
	ReadOnly bool
}

// CanonicalJSON re-encodes v with object keys sorted (encoding/json
// sorts map keys), so equal arguments hash equally whatever their order.
func CanonicalJSON(raw json.RawMessage) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte("{}"), nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("invocation: arguments: %w", err)
	}
	return json.Marshal(v)
}

func hash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// operationKey is the key of a call that carries an operation id. It
// never expires: a retry with the same id is the same call at any time.
func operationKey(c Call) string {
	return "tc-" + hash("op", c.CredentialID, c.Tool, c.OperationID)[:40]
}

// windowKey is the key of a call without an operation id, opened at t.
func windowKey(c Call, argsHash string, t time.Time) string {
	return "tc-" + hash("dd", c.CredentialID, c.Tool, argsHash, t.UTC().Format(time.RFC3339Nano))[:40]
}

//go:embed sql/schema.sql
var schemaSQL string

// ApplySchema creates the gateway's tables. Idempotent.
//
// The schema is created only when it is missing: CREATE SCHEMA IF NOT
// EXISTS still needs CREATE on the database, which the production gateway
// role does not hold. There the schema already exists and the role owns it
// (internal/dbrole).
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'gateway')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS gateway`); err != nil {
			return err
		}
	}
	_, err := pool.Exec(ctx, schemaSQL)
	return err
}

// Store is the Postgres-backed invocation store.
type Store struct {
	pool *pgxpool.Pool
	// Window is how long an identical call without an operation id is
	// treated as a retry of the first.
	Window time.Duration
	now    func() time.Time
}

// DefaultWindow is the default dedup window.
const DefaultWindow = 10 * time.Minute

// NewStore returns a store over pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, Window: DefaultWindow, now: time.Now}
}

// Pool is the store's pool (tests read raw rows through it).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// SetClock replaces the clock (tests).
func (s *Store) SetClock(now func() time.Time) { s.now = now }

const cols = `key, tenant_id, credential_id, agent_id, tool, operation_id, args_hash, arguments,
	state, exception_id, review_url, message, reasons, result, created_at, updated_at, hold_expires_at, tool_error, tool_outcome, no_effect_evidence, execution_binding`

func scan(row pgx.Row) (Invocation, error) {
	var in Invocation
	var args, result []byte
	err := row.Scan(&in.Key, &in.TenantID, &in.CredentialID, &in.AgentID, &in.Tool, &in.OperationID,
		&in.ArgsHash, &args, &in.State, &in.ExceptionID, &in.ReviewURL, &in.Message, &in.Reasons, &result,
		&in.CreatedAt, &in.UpdatedAt, &in.HoldExpiresAt, &in.ToolError, &in.ToolOutcome, &in.NoEffectEvidence, &in.ExecutionBinding)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invocation{}, ErrNotFound
	}
	in.Arguments = args
	in.Result = result
	return in, err
}

// PendingStates are the states kiff_pending lists: waiting on the
// owner, being sent, or sent with the outcome unknown.
var PendingStates = []string{string(StateHeld), string(StateForwarding), string(StateUnknown)}

// PendingPage is where a Pending listing goes on: after the call created
// at At with key Key, in its newest-first order.
type PendingPage struct {
	At  time.Time
	Key string
}

// Pending lists credentialID's calls in PendingStates created at or after
// since, newest first by (created_at, key), at most limit, after page when
// it is set. Only that credential's: an agent sees its own calls and no
// other's. Paging by position, not by count, never skips or repeats a call
// whatever the newer ones do between reads.
func (s *Store) Pending(ctx context.Context, credentialID string, since time.Time, page *PendingPage, limit int) ([]Invocation, error) {
	var afterAt *time.Time
	afterKey := ""
	if page != nil {
		at := page.At.UTC()
		afterAt, afterKey = &at, page.Key
	}
	rows, err := s.pool.Query(ctx, `SELECT `+cols+` FROM gateway.invocations
		WHERE credential_id = $1 AND state = ANY($2) AND created_at >= $3
		  AND ($4::timestamptz IS NULL OR (created_at, key) < ($4::timestamptz, $5::text))
		ORDER BY created_at DESC, key DESC LIMIT $6`, credentialID, PendingStates, since.UTC(), afterAt, afterKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invocation
	for rows.Next() {
		in, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// Get loads one invocation.
func (s *Store) Get(ctx context.Context, key string) (Invocation, error) {
	return scan(s.pool.QueryRow(ctx, `SELECT `+cols+` FROM gateway.invocations WHERE key = $1`, key))
}

// Begin returns the invocation for c: a new one in StateDeciding
// (created true), or the existing one this call is a retry of (created
// false), whatever its state. Concurrent identical calls get one
// invocation and exactly one of them gets created true.
func (s *Store) Begin(ctx context.Context, c Call) (Invocation, bool, error) {
	canon, err := CanonicalJSON(c.Arguments)
	if err != nil {
		return Invocation{}, false, err
	}
	argsHash := hash(string(canon))
	now := s.now().UTC()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Invocation{}, false, err
	}
	defer tx.Rollback(ctx)

	var key string
	if c.OperationID != "" {
		// An operation id that is the key of this credential's own
		// call made without one names that call, so a hold kiff_pending
		// lists can be collected after the identical-call window has
		// passed. Same tool and same arguments, or it is a reused id.
		if strings.HasPrefix(c.OperationID, "tc-") {
			in, err := scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM gateway.invocations
				WHERE key = $1 AND credential_id = $2 AND tool = $3 AND operation_id = ''`,
				c.OperationID, c.CredentialID, c.Tool))
			switch {
			case err == nil && in.ArgsHash != argsHash:
				return Invocation{}, false, ErrOperationReused
			case err == nil:
				return in, false, tx.Commit(ctx)
			case !errors.Is(err, ErrNotFound):
				return Invocation{}, false, err
			}
		}
		key = operationKey(c)
	} else {
		if c.ReadOnly {
			k, joined, err := s.readOnlySlot(ctx, tx, c, argsHash, now)
			if err != nil {
				return Invocation{}, false, err
			}
			if joined != nil {
				return *joined, false, tx.Commit(ctx)
			}
			key = k
		} else {
			// Open a slot, or take over one whose window has passed. A
			// concurrent identical call blocks on the row lock and then
			// sees the committed slot, so it gets no row back here.
			fresh := windowKey(c, argsHash, now)
			err = tx.QueryRow(ctx, `
				INSERT INTO gateway.dedup_slots (credential_id, tool, args_hash, invocation_key, opened_at, tenant_id)
				VALUES ($1, $2, $3, $4, $5, $7)
				ON CONFLICT (credential_id, tool, args_hash) DO UPDATE
				   SET invocation_key = EXCLUDED.invocation_key, opened_at = EXCLUDED.opened_at
				 WHERE gateway.dedup_slots.opened_at <= $6
				RETURNING invocation_key`,
				c.CredentialID, c.Tool, argsHash, fresh, now, now.Add(-s.Window), c.TenantID).Scan(&key)
			if errors.Is(err, pgx.ErrNoRows) {
				if err := tx.QueryRow(ctx, `SELECT invocation_key FROM gateway.dedup_slots
					WHERE credential_id = $1 AND tool = $2 AND args_hash = $3`,
					c.CredentialID, c.Tool, argsHash).Scan(&key); err != nil {
					return Invocation{}, false, err
				}
				in, err := scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM gateway.invocations WHERE key = $1`, key))
				if err != nil {
					return Invocation{}, false, err
				}
				return in, false, tx.Commit(ctx)
			}
			if err != nil {
				return Invocation{}, false, err
			}
		}
	}

	in, err := scan(tx.QueryRow(ctx, `
		INSERT INTO gateway.invocations
		  (key, tenant_id, credential_id, agent_id, tool, operation_id, args_hash, arguments, state, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
		ON CONFLICT (key) DO NOTHING
		RETURNING `+cols,
		key, c.TenantID, c.CredentialID, c.AgentID, c.Tool, c.OperationID, argsHash, canon, StateDeciding, now))
	if err == nil {
		return in, true, tx.Commit(ctx)
	}
	if !errors.Is(err, ErrNotFound) {
		return Invocation{}, false, err
	}
	// The operation id was used before.
	in, err = scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM gateway.invocations WHERE key = $1`, key))
	if err != nil {
		return Invocation{}, false, err
	}
	if in.ArgsHash != argsHash {
		return Invocation{}, false, ErrOperationReused
	}
	return in, false, tx.Commit(ctx)
}

// readOnlySlot resolves the dedup slot for a read-only call without an
// operation id. An identical read joins the slot's call only
// while that call is unfinished (deciding, held, forwarding), whatever
// the time; otherwise the call gets a new key. It returns the call to
// join, or the key for a new call with the slot already pointing at it.
//
// Each step is its own statement, after the slot row is locked, so the
// state read sees what a concurrent identical call committed. (A
// condition inside ON CONFLICT DO UPDATE would read the statement's
// older snapshot and miss it.)
func (s *Store) readOnlySlot(ctx context.Context, tx pgx.Tx, c Call, argsHash string, now time.Time) (string, *Invocation, error) {
	fresh := windowKey(c, argsHash, now)
	var key string
	err := tx.QueryRow(ctx, `
		INSERT INTO gateway.dedup_slots (credential_id, tool, args_hash, invocation_key, opened_at, tenant_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (credential_id, tool, args_hash) DO NOTHING
		RETURNING invocation_key`,
		c.CredentialID, c.Tool, argsHash, fresh, now, c.TenantID).Scan(&key)
	if err == nil {
		return key, nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", nil, err
	}
	if err := tx.QueryRow(ctx, `SELECT invocation_key FROM gateway.dedup_slots
		WHERE credential_id = $1 AND tool = $2 AND args_hash = $3 FOR UPDATE`,
		c.CredentialID, c.Tool, argsHash).Scan(&key); err != nil {
		return "", nil, err
	}
	in, err := scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM gateway.invocations WHERE key = $1`, key))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", nil, err
	}
	if err == nil {
		switch in.State {
		case StateDeciding, StateHeld, StateForwarding:
			return "", &in, nil
		}
	}
	// Derived from the key it replaces, so it differs from it even when
	// both calls carry the same timestamp.
	next := "tc-" + hash("ro-next", key, now.UTC().Format(time.RFC3339Nano))[:40]
	if _, err := tx.Exec(ctx, `UPDATE gateway.dedup_slots SET invocation_key = $4, opened_at = $5
		WHERE credential_id = $1 AND tool = $2 AND args_hash = $3`,
		c.CredentialID, c.Tool, argsHash, next, now); err != nil {
		return "", nil, err
	}
	return next, nil, nil
}

// Update is what a transition writes besides the state.
type Update struct {
	ExceptionID string
	ReviewURL   string
	Message     string
	// Reasons replace the recorded ones when non-nil.
	Reasons []string
	// HoldExpiresAt replaces the recorded one when non-nil.
	HoldExpiresAt *time.Time
	Result        json.RawMessage
	// ToolError replaces the recorded one when non-empty.
	ToolError string
	// ToolOutcome ("done" or "error") replaces the recorded one when
	// non-empty.
	ToolOutcome      string
	NoEffectEvidence json.RawMessage
	ExecutionBinding json.RawMessage
}

// ReasonGatewayRestarted is recorded when a stale in-flight invocation
// is resolved.
const ReasonGatewayRestarted = "gateway_restarted"

// Messages recorded when a stale in-flight invocation is resolved.
const (
	StaleDecidingMessage = "KIFF lost track of this call before sending it (the gateway restarted), so it was not sent to the tool. " +
		"Retry with a new kiff_operation_id."
	StaleForwardingMessage = "This call was being sent to the tool when the gateway restarted, so whether the tool received it is unknown. " +
		"KIFF will not send it again; check the tool before retrying with a new kiff_operation_id."
)

// RecoverStale resolves in-flight invocations whose last update is at or
// before staleBefore, which only happens when the process handling them
// died: deciding becomes failed (nothing was sent), forwarding becomes
// unknown (it may have been sent; it is never re-sent). key "" sweeps
// every stale row. A live handler that later tries to move the row loses
// the conditional update, so nothing is overwritten silently.
func (s *Store) RecoverStale(ctx context.Context, key string, staleBefore time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE gateway.invocations
		   SET state = CASE state WHEN 'deciding' THEN 'failed' ELSE 'unknown' END,
		       arguments = '{}'::jsonb,
		       message = CASE state WHEN 'deciding' THEN $3 ELSE $4 END,
		       reasons = ARRAY[$6::text],
		       updated_at = $5
		 WHERE state IN ('deciding', 'forwarding') AND updated_at <= $1 AND ($2 = '' OR key = $2)`,
		staleBefore, key, StaleDecidingMessage, StaleForwardingMessage, s.now().UTC(), ReasonGatewayRestarted)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// finishedStates are the states a call never leaves. A finished call is
// never forwarded again, so its arguments are dropped when it gets there:
// args_hash still detects a reused operation id and an identical repeat,
// and nothing else reads them (DATA_MINIMIZATION.md in kiff-plugins).
var finishedStates = []string{string(StateRefused), string(StateForwarded), string(StateFailed), string(StateUnknown)}

// PurgeResults drops the tool's own result from forwarded calls that
// finished at or before before. A retry after that is told the call was
// sent, without the result; it is still never sent again.
func (s *Store) PurgeResults(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE gateway.invocations SET result = NULL
		 WHERE result IS NOT NULL AND state = $2 AND updated_at <= $1`, before, string(StateForwarded))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Transition moves key from one of from to to, and reports whether this
// caller made the move. Only one concurrent caller can win a given move,
// which is what makes forwarding at most once.
func (s *Store) Transition(ctx context.Context, key string, from []State, to State, u Update) (bool, error) {
	fromS := make([]string, len(from))
	for i, f := range from {
		fromS[i] = string(f)
	}
	var result any
	if len(u.Result) > 0 {
		result = []byte(u.Result)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE gateway.invocations
		   SET state = $3,
		       exception_id = CASE WHEN $4 = '' THEN exception_id ELSE $4 END,
		       review_url   = CASE WHEN $5 = '' THEN review_url ELSE $5 END,
		       arguments = CASE WHEN $3 = ANY($11) THEN '{}'::jsonb ELSE arguments END,
		       message = $6, result = COALESCE($7::jsonb, result), updated_at = $8,
		       reasons = COALESCE($9::text[], reasons),
		       hold_expires_at = COALESCE($10::timestamptz, hold_expires_at),
		       tool_error = CASE WHEN $12 = '' THEN tool_error ELSE $12 END,
		       tool_outcome = CASE WHEN $13 = '' THEN tool_outcome ELSE $13 END,
		       no_effect_evidence = COALESCE(no_effect_evidence, $14::jsonb),
		       execution_binding = COALESCE(execution_binding, $15::jsonb)
		 WHERE key = $1 AND state = ANY($2)`,
		key, fromS, string(to), u.ExceptionID, u.ReviewURL, u.Message, result, s.now().UTC(), u.Reasons, u.HoldExpiresAt, finishedStates, u.ToolError, u.ToolOutcome, nullableJSON(u.NoEffectEvidence), nullableJSON(u.ExecutionBinding))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

// BindExecution records the executor before sending, without claiming
// forwarding a second time or extending its original recovery lease.
func (s *Store) BindExecution(ctx context.Context, key string, binding json.RawMessage) (bool, error) {
	if len(binding) == 0 {
		return false, errors.New("invocation: executor binding is required")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE gateway.invocations
	 SET execution_binding=COALESCE(execution_binding,$2::jsonb)
	 WHERE key=$1 AND state='forwarding'
	 AND (execution_binding IS NULL OR execution_binding=$2::jsonb)`, key, []byte(binding))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
