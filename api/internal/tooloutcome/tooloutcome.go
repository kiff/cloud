// Package tooloutcome reads how a call KIFF authorized ended at the tool,
// as the gateway recorded it. The decision and its receipt say
// what KIFF authorized; this says what the tool reported back, a separate
// fact shown next to it. A tool's success is its own report, not proof of
// a business effect.
//
// The gateway keeps one row per tool call in gateway.invocations, keyed by
// the proposal id it decided under. The API reads three columns there and
// never writes them: an agent's key cannot record an outcome, so an agent
// cannot claim its call succeeded.
package tooloutcome

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Status is how the call ended at the tool.
type Status string

const (
	// Done: the tool received the call and reported success.
	Done Status = "done"
	// RefusedByTool: the tool received the call and answered with an error.
	RefusedByTool Status = "refused_by_tool"
	// Unknown: the call was sent and its answer was lost; KIFF will not
	// send it again.
	Unknown Status = "unknown"
	// NotSent: authorized, but the gateway could not send it (an
	// unreadable credential, an unreachable tool).
	NotSent Status = "not_sent"
	// Sending: sent, and the tool has not answered yet.
	Sending Status = "sending"
	// NotRecorded: delivered, but how it ended was not recorded (a call
	// from before KIFF recorded it, or from an older gateway during a
	// rollout) and the tool's result is no longer kept. Never read as done.
	NotRecorded Status = "not_recorded"
)

// Outcome is one call's outcome at the tool.
type Outcome struct {
	Status Status `json:"status"`
	// Message is the tool's error for RefusedByTool, or the gateway's
	// explanation for Unknown and NotSent; empty for Done and Sending.
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at"`
}

// Reader reads outcomes by proposal id. A call the gateway has no record
// of, or that has not reached the tool, has no outcome.
type Reader interface {
	Outcomes(ctx context.Context, tenantID string, proposalIDs []string) (map[string]Outcome, error)
}

// Record is what the gateway keeps on a call that bears on its outcome.
type Record struct {
	State, Message string
	// ToolError and ToolOutcome ("done" or "error") are what the gateway
	// recorded; ToolOutcome is "" when it recorded nothing.
	ToolError, ToolOutcome string
	// HasResult and ResultError are the tool's own result while it is kept
	// (24 hours): whether there is one, and its isError. ResultText is its
	// text, for a legacy error.
	HasResult, ResultError bool
	ResultText             string
	At                     time.Time
}

// maxMessage bounds a message reconstructed from a legacy result.
const maxMessage = 300

// FromRecord is the outcome of a gateway record, if it has one. A
// delivered call is done or refused only on evidence: what the gateway
// recorded, or else the tool's result while it is still kept. Without
// either, its outcome was not recorded.
func FromRecord(r Record) (Outcome, bool) {
	at := r.At
	switch r.State {
	case "forwarded":
		switch {
		case r.ToolOutcome == "error":
			return Outcome{Status: RefusedByTool, Message: r.ToolError, At: at}, true
		case r.ToolOutcome == "done":
			return Outcome{Status: Done, At: at}, true
		case r.HasResult && r.ResultError:
			return Outcome{Status: RefusedByTool, Message: legacyMessage(r.ResultText), At: at}, true
		case r.HasResult:
			return Outcome{Status: Done, At: at}, true
		}
		return Outcome{Status: NotRecorded, At: at}, true
	case "unknown":
		return Outcome{Status: Unknown, Message: r.Message, At: at}, true
	case "failed":
		return Outcome{Status: NotSent, Message: r.Message, At: at}, true
	case "forwarding":
		return Outcome{Status: Sending, At: at}, true
	}
	return Outcome{}, false
}

// Postgres reads gateway.invocations in the shared database.
type Postgres struct{ pool *pgxpool.Pool }

// NewPostgres reads outcomes through pool.
func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Outcomes reads the outcomes of proposalIDs in tenantID. Where the
// gateway's tables do not exist (an API without a gateway), there are
// none.
func (p *Postgres) Outcomes(ctx context.Context, tenantID string, proposalIDs []string) (map[string]Outcome, error) {
	out := map[string]Outcome{}
	if p == nil || p.pool == nil || len(proposalIDs) == 0 {
		return out, nil
	}
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT to_regclass('gateway.invocations') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		return out, err
	}
	// A gateway older than these columns has neither: every delivered call
	// then reads from its result, or as not recorded.
	var hasOutcome bool
	if err := p.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema = 'gateway' AND table_name = 'invocations' AND column_name = 'tool_outcome')`).Scan(&hasOutcome); err != nil {
		return nil, err
	}
	recorded := `'' AS tool_error, '' AS tool_outcome`
	if hasOutcome {
		recorded = `tool_error, tool_outcome`
	}
	// isError is compared as JSON, never cast: result is the tool's own
	// output, and a non-boolean isError cast to boolean would fail the
	// whole read. As in the gateway, only an absent,
	// null or false isError is success; any other value is an error.
	rows, err := p.pool.Query(ctx, `
		SELECT key, state, message, `+recorded+`, updated_at,
		       result IS NOT NULL, COALESCE(result->'isError' NOT IN ('false'::jsonb, 'null'::jsonb), false),
		       COALESCE((SELECT string_agg(c->>'text', ' ') FROM jsonb_array_elements(
		           CASE WHEN jsonb_typeof(result->'content') = 'array' THEN result->'content' ELSE '[]'::jsonb END) c
		         WHERE c->>'type' = 'text'), '')
		  FROM gateway.invocations
		 WHERE tenant_id = $1 AND key = ANY($2)`, tenantID, proposalIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var r Record
		if err := rows.Scan(&key, &r.State, &r.Message, &r.ToolError, &r.ToolOutcome, &r.At,
			&r.HasResult, &r.ResultError, &r.ResultText); err != nil {
			return nil, err
		}
		if o, ok := FromRecord(r); ok {
			out[key] = o
		}
	}
	return out, rows.Err()
}

// ArgumentsReader reads the arguments a held call will be sent with, so
// the person answering sees what they approve: the tool and
// these arguments, not KIFF's id for the call.
type ArgumentsReader interface {
	Arguments(ctx context.Context, tenantID string, proposalIDs []string) (map[string]json.RawMessage, error)
}

// Arguments reads the stored arguments of proposalIDs in tenantID, by
// proposal id, as the JSON object the gateway stored: what the agent
// sent, without kiff_operation_id. They are passed on as raw JSON, never
// decoded: a number such as 9007199254740993 must read as the tool will
// receive it. A call with no arguments is "{}"; one the
// gateway has no record of is absent. Where the gateway's tables do not
// exist, there are none.
func (p *Postgres) Arguments(ctx context.Context, tenantID string, proposalIDs []string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if p == nil || p.pool == nil || len(proposalIDs) == 0 {
		return out, nil
	}
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT to_regclass('gateway.invocations') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		return out, err
	}
	rows, err := p.pool.Query(ctx, `
		SELECT key, arguments::text FROM gateway.invocations
		 WHERE tenant_id = $1 AND key = ANY($2)`, tenantID, proposalIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, args string
		if err := rows.Scan(&key, &args); err != nil {
			return nil, err
		}
		out[key] = json.RawMessage(args)
	}
	return out, rows.Err()
}

// legacyMessage is a legacy error result's text as a message: one line,
// at most maxMessage characters.
func legacyMessage(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "The tool returned an error."
	}
	if r := []rune(text); len(r) > maxMessage {
		text = string(r[:maxMessage-1]) + "…"
	}
	return text
}

// ToolOwners reads who connected each of tenantID's gateway tools: the
// connecting key's id, or "" for a person (gateway.tools.created_by).
// A database without the gateway's tools, or with a gateway older
// than the column, is an error, so a connector key's writes fail closed.
func (p *Postgres) ToolOwners(ctx context.Context, tenantID string) (map[string]string, error) {
	if p == nil || p.pool == nil {
		return nil, errors.New("tooloutcome: no database")
	}
	rows, err := p.pool.Query(ctx, `SELECT name, created_by FROM gateway.tools WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([2]string, error) {
		var r [2]string
		err := row.Scan(&r[0], &r[1])
		return r, err
	})
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(out))
	for _, r := range out {
		m[r[0]] = r[1]
	}
	return m, nil
}

// ToolTitles reads the name people see for each of tenantID's gateway
// tools: the vendor's title, kept when the tool was connected
// (gateway.tools.title). Tools without one are left out.
func (p *Postgres) ToolTitles(ctx context.Context, tenantID string) (map[string]string, error) {
	if p == nil || p.pool == nil {
		return nil, errors.New("tooloutcome: no database")
	}
	rows, err := p.pool.Query(ctx, `SELECT name, title FROM gateway.tools WHERE tenant_id = $1 AND title <> ''`, tenantID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([2]string, error) {
		var r [2]string
		err := row.Scan(&r[0], &r[1])
		return r, err
	})
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(out))
	for _, r := range out {
		m[r[0]] = r[1]
	}
	return m, nil
}

// ReadOnlyTools reads which of tenantID's gateway tools are marked as
// only reading (gateway.tools.read_only), by name. Others are left out.
func (p *Postgres) ReadOnlyTools(ctx context.Context, tenantID string) (map[string]bool, error) {
	if p == nil || p.pool == nil {
		return nil, errors.New("tooloutcome: no database")
	}
	rows, err := p.pool.Query(ctx, `SELECT name FROM gateway.tools WHERE tenant_id = $1 AND read_only`, tenantID)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m, nil
}
