package proposals

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Activity lists calls, not decisions: a call held for its owner
// is decided more than once under one proposal id (held, then allowed or
// refused once answered), and the owner reads it as one thing that
// happened. A call is its latest decision, plus whether any decision
// held it.

// Which calls a list shows.
const (
	CallsAll     = ""
	CallsAllowed = "allowed"
	CallsRefused = "refused"
	CallsHeld    = "held"
)

// heldOutcomes are the decisions that wait for a person: a Card's owner
// (owner_required, approved_no_room) or a domain approval
// (approval_required).
var heldOutcomes = []string{"owner_required", "approved_no_room", "approval_required"}

// CallQuery narrows the calls. Actor is one agent; Proposal is one call,
// by its proposal id; Trial is the calls a draft's trial made
// (kiff/tafs#173), and Since those made from then on; Show is one of the
// Calls* values. Limit is capped
// at MaxLimit. Offset is not capped: the counts cover every call, so
// every call must be reachable a page at a time.
type CallQuery struct {
	Actor    string
	Proposal string
	Trial    string
	Since    time.Time
	Show     string
	Limit    int
	Offset   int
}

// CallRow is one call: its latest decision, whether any of its decisions
// held it for a person, and how many decisions it had.
type CallRow struct {
	DecisionRow
	Held      bool `json:"held"`
	Decisions int  `json:"decisions"`
	// MadeAt is when the call was made: its first decision. CreatedAt is
	// its latest, so a held call answered later keeps its place by MadeAt
	// (kiff/tafs#138).
	MadeAt time.Time `json:"made_at"`
	// Reason is why it wasn't sent, in the reader's language when KIFF
	// writes it; "" when it was sent or KIFF has no words for it. Set by
	// the activity handler.
	Reason string `json:"reason,omitempty"`
	// Title is the call's tool as people read it (gateway.tools.title),
	// when it has one. Set by the activity handler, not stored.
	Title string `json:"title,omitempty"`
	// ReadOnly is true when the call's gateway tool is marked as only
	// reading, so a list can fold reads (kiff/tafs#167). Set by the
	// activity handler, like Title.
	ReadOnly bool `json:"read_only,omitempty"`
	// Answer is how the owner answered a held call; nil when it wasn't
	// held, or isn't answered (yet). Set by the activity handler.
	Answer *CallAnswer `json:"answer,omitempty"`
	// Receipt is the call's receipt and whether KIFF has signed it yet;
	// nil when it has none. Set by the activity handler.
	Receipt *CallReceipt `json:"receipt,omitempty"`
}

// CallAnswer is a person's answer to a held call: "approved_once" or
// "refused", who gave it (their role on the account), whether that was
// the person reading, whether a phone signed it, and that device's name.
type CallAnswer struct {
	Decision string `json:"decision"`
	By       string `json:"by,omitempty"`
	// You is true when the person reading the activity gave the answer
	// (kiff/tafs#170): a role alone can't say so, since an account's
	// owner may answer as an admin.
	You    bool      `json:"you,omitempty"`
	Signed bool      `json:"signed"`
	Device string    `json:"device,omitempty"`
	At     time.Time `json:"at"`
}

// CallReceipt is a call's receipt: its trace id, for GET
// /v1/receipts/{trace_id}, and "signed" or "pending" (signed within a
// sweep of being written).
type CallReceipt struct {
	TraceID string `json:"trace_id"`
	Status  string `json:"status"`
}

// CallCounts are how many calls each filter shows, for the same actor.
type CallCounts struct {
	All     int `json:"all"`
	Allowed int `json:"allowed"`
	Refused int `json:"refused"`
	Held    int `json:"held"`
}

// CallsPage is a page of calls, the counts for every filter, and every
// agent with a call in the account, whatever the filter, so a page can
// offer each one.
type CallsPage struct {
	Calls  []CallRow
	Counts CallCounts
	Agents []string
}

// MaxCallAgents caps the agents a page lists.
const MaxCallAgents = 500

// CallsReader lists calls. The Postgres and memory readers implement it;
// it is separate from Reader so other readers need not.
type CallsReader interface {
	ListCalls(ctx context.Context, tenantID string, q CallQuery) (CallsPage, error)
}

// callShown reports whether a call belongs under show. A call that was
// ever held is under Held however it ended; the others are Allowed or
// Refused by their latest decision.
func callShown(c CallRow, show string) bool {
	switch show {
	case CallsHeld:
		return c.Held
	case CallsAllowed:
		return !c.Held && c.Outcome == "allowed"
	case CallsRefused:
		return !c.Held && c.Outcome != "allowed"
	}
	return true
}

func (c *CallCounts) add(row CallRow) {
	c.All++
	switch {
	case row.Held:
		c.Held++
	case row.Outcome == "allowed":
		c.Allowed++
	default:
		c.Refused++
	}
}

func clampCalls(q CallQuery) CallQuery {
	if q.Limit <= 0 {
		q.Limit = DefaultLimit
	}
	if q.Limit > MaxLimit {
		q.Limit = MaxLimit
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	return q
}

// callsSQL keeps each call's latest decision (DISTINCT ON its proposal
// id, the id the scanner reads: data's proposal_id, else trace_id, else
// the row id) and whether any of its decisions held it. $1 is the actor,
// $3 the call and $4 the trial's draft; empty means any.
const callsSQL = `
	WITH d AS (
		SELECT id, kind, entity_id, entity_type, actor_id, data, trace_id, created_at,
		       COALESCE(NULLIF(data->>'proposal_id', ''), NULLIF(trace_id, ''), id::text) AS call_id
		FROM kiff_audit
		WHERE kind IN ('proposal_decision', 'proposal_rejected')
		  AND ($1 = '' OR actor_id = $1)
		  AND ($3 = '' OR COALESCE(NULLIF(data->>'proposal_id', ''), NULLIF(trace_id, ''), id::text) = $3)
		  AND ($4 = '' OR data->>'trial_draft' = $4)
	), latest AS (
		SELECT DISTINCT ON (call_id) id, kind, entity_id, entity_type, actor_id, data, trace_id, created_at, call_id
		FROM d
		ORDER BY call_id, created_at DESC, id DESC
	), held AS (
		SELECT call_id, bool_or(data->>'outcome' = ANY($2)) AS held, COUNT(*) AS decisions, MIN(created_at) AS made_at
		FROM d
		GROUP BY call_id
	)
	SELECT l.id, l.kind, l.entity_id, l.entity_type, l.actor_id, l.data, l.trace_id, l.created_at,
	       h.held, h.decisions, h.made_at
	FROM latest l JOIN held h USING (call_id)
	ORDER BY l.created_at DESC, l.id DESC
`

// ListCalls implements CallsReader. Calls are few next to decisions,
// so the filter and counts run over the actor's calls in Go, and every
// count is complete whatever the page.
func (r *PostgresReader) ListCalls(ctx context.Context, tenantID string, q CallQuery) (CallsPage, error) {
	q = clampCalls(q)
	conn, err := r.pool.Acquire(ctx, tenantID)
	if err != nil {
		return CallsPage{}, fmt.Errorf("proposals: acquire tenant pool: %w", err)
	}
	defer conn.Release()
	rows, err := conn.Query(ctx, callsSQL, q.Actor, heldOutcomes, q.Proposal, q.Trial)
	if err != nil {
		return CallsPage{}, fmt.Errorf("proposals: list calls: %w", err)
	}
	var all []CallRow
	for rows.Next() {
		var (
			kind      string
			actorID   *string
			dataBytes []byte
			traceID   *string
			c         CallRow
		)
		if err := rows.Scan(&c.ProposalID, &kind, &c.EntityID, &c.EntityType, &actorID, &dataBytes, &traceID, &c.CreatedAt, &c.Held, &c.Decisions, &c.MadeAt); err != nil {
			rows.Close()
			return CallsPage{}, fmt.Errorf("proposals: scan call: %w", err)
		}
		row, err := decodeDecisionRow(c.DecisionRow, kind, actorID, dataBytes, traceID)
		if err != nil {
			rows.Close()
			return CallsPage{}, err
		}
		c.DecisionRow = row
		if c.MadeAt.Before(q.Since) {
			continue
		}
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		return CallsPage{}, fmt.Errorf("proposals: list calls: %w", err)
	}
	page := CallsPage{}
	page.Calls, page.Counts = pageCalls(all, q)
	decisions := make([]DecisionRow, len(page.Calls))
	for i := range page.Calls {
		decisions[i] = page.Calls[i].DecisionRow
	}
	if err := r.enrichReceipts(ctx, tenantID, decisions); err != nil {
		return CallsPage{}, err
	}
	for i := range page.Calls {
		page.Calls[i].DecisionRow = decisions[i]
	}
	agents, err := conn.Query(ctx, callAgentsSQL, MaxCallAgents)
	if err != nil {
		return CallsPage{}, fmt.Errorf("proposals: list call agents: %w", err)
	}
	defer agents.Close()
	for agents.Next() {
		var a string
		if err := agents.Scan(&a); err != nil {
			return CallsPage{}, fmt.Errorf("proposals: scan call agent: %w", err)
		}
		page.Agents = append(page.Agents, a)
	}
	if err := agents.Err(); err != nil {
		return CallsPage{}, fmt.Errorf("proposals: list call agents: %w", err)
	}
	return page, nil
}

// callAgentsSQL is every agent with a call in the account, whatever the
// filter.
const callAgentsSQL = `
	SELECT DISTINCT actor_id FROM kiff_audit
	WHERE kind IN ('proposal_decision', 'proposal_rejected') AND actor_id IS NOT NULL AND actor_id <> ''
	ORDER BY actor_id
	LIMIT $1
`

// pageCalls counts every call, then keeps the page of those show
// selects. calls are newest first.
func pageCalls(calls []CallRow, q CallQuery) ([]CallRow, CallCounts) {
	var counts CallCounts
	out := make([]CallRow, 0, q.Limit)
	skipped := 0
	for _, c := range calls {
		counts.add(c)
		if !callShown(c, q.Show) {
			continue
		}
		if skipped < q.Offset {
			skipped++
			continue
		}
		if len(out) < q.Limit {
			out = append(out, c)
		}
	}
	return out, counts
}

// ListCalls implements CallsReader over the appended rows, by the same
// rules as the Postgres reader.
func (r *MemoryReader) ListCalls(_ context.Context, tenantID string, q CallQuery) (CallsPage, error) {
	q = clampCalls(q)
	r.mu.RLock()
	src := append([]DecisionRow(nil), r.rows[tenantID]...)
	r.mu.RUnlock()
	byCall := map[string]*CallRow{}
	agents := map[string]bool{}
	for _, d := range src {
		if d.ActorID != "" {
			agents[d.ActorID] = true
		}
		if (q.Actor != "" && d.ActorID != q.Actor) || (q.Proposal != "" && d.ProposalID != q.Proposal) ||
			(q.Trial != "" && d.TrialDraft != q.Trial) {
			continue
		}
		c, ok := byCall[d.ProposalID]
		if !ok {
			c = &CallRow{DecisionRow: d, MadeAt: d.CreatedAt}
			byCall[d.ProposalID] = c
		} else if d.CreatedAt.After(c.CreatedAt) {
			c.DecisionRow = d
		}
		if d.CreatedAt.Before(c.MadeAt) {
			c.MadeAt = d.CreatedAt
		}
		c.Decisions++
		for _, h := range heldOutcomes {
			if d.Outcome == h {
				c.Held = true
			}
		}
	}
	all := make([]CallRow, 0, len(byCall))
	for _, c := range byCall {
		if !c.MadeAt.Before(q.Since) {
			all = append(all, *c)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ProposalID > all[j].ProposalID
	})
	page := CallsPage{}
	page.Calls, page.Counts = pageCalls(all, q)
	for a := range agents {
		page.Agents = append(page.Agents, a)
	}
	sort.Strings(page.Agents)
	if len(page.Agents) > MaxCallAgents {
		page.Agents = page.Agents[:MaxCallAgents]
	}
	return page, nil
}
