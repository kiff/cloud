package tooloutcome

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Probation: one of the first sent calls of an untried money operation.
const Probation Status = "probation"

// Unproven is a money call whose outcome KIFF can't prove: sent and its
// answer lost, or refused by the vendor with nothing that establishes no
// effect (connection profiles: room comes back only on proof); or one of the first sent calls of a money
// operation no test call proved (probation, §5). It stays counted until
// the owner answers "Did this happen?".
type Unproven struct {
	Proposal string    `json:"proposal"`
	Agent    string    `json:"agent"`
	Tool     string    `json:"tool"`
	Title    string    `json:"title"`
	Status   Status    `json:"status"`
	Amount   int64     `json:"amount"`
	Unit     string    `json:"unit"`
	Scale    int       `json:"scale"`
	At       time.Time `json:"at"`
	// Words is the call in a sentence, and StatusWords why KIFF asks, for
	// the apps to show as they are.
	Words       string `json:"words"`
	StatusWords string `json:"status_words"`
}

// HappenedAnswer is the owner's answer to one call's "Did this happen?".
type HappenedAnswer struct {
	Happened   bool      `json:"happened"`
	AnsweredBy string    `json:"answered_by"`
	AnsweredAt time.Time `json:"answered_at"`
}

// unprovenQuery lists a tenant's unproven money calls not answered yet:
// the gateway's own record of each call, with the amount the Card drew
// for it and no release of that draw. Its title, unit and scale are what
// the gateway fixed on the call when it was decided (measure), so a later
// change or removal of the connection never rewords it (connection profiles: a call keeps the profile it was decided under). A call from before that falls back to its profile revision, then to
// the connection as it is now.
const unprovenQuery = `SELECT i.key, i.agent_id, i.tool,
	COALESCE(NULLIF(i.measure->>'title', ''), NULLIF(op->>'title', ''), NULLIF(t.title, ''), i.tool), i.state, COALESCE(i.tool_outcome, ''), d.amount,
	COALESCE(i.measure->>'unit', op->'amount'->>'unit', t.amount_unit, ''),
	COALESCE((i.measure->>'scale')::int, (op->'amount'->>'scale')::int, t.amount_scale, 0), i.updated_at
	FROM gateway.invocations i
	JOIN cloud_meta.mandate_draws d ON d.tenant_id = i.tenant_id AND d.proposal_id = i.key AND d.quantity_key LIKE 'sum(%'
	LEFT JOIN gateway.tools t ON t.tenant_id = i.tenant_id AND t.name = i.tool
	LEFT JOIN gateway.profiles p ON p.tenant_id = i.tenant_id AND p.revision = i.profile_revision
	LEFT JOIN LATERAL (SELECT o FROM jsonb_array_elements(p.profile->'operations') o WHERE o->>'tool' = i.tool LIMIT 1) ops(op) ON true
	WHERE i.tenant_id = $1
	  AND (i.state = 'unknown' OR (i.state = 'forwarded' AND i.tool_outcome = 'error' AND i.no_effect_evidence IS NULL)
	       OR (i.state = 'forwarded' AND i.tool_outcome = 'done' AND i.probation))
	  AND ($3 OR (NOT EXISTS (SELECT 1 FROM cloud_meta.mandate_effect_releases r WHERE r.tenant_id = d.tenant_id AND r.mandate_id = d.mandate_id
	      AND r.proposal_id = d.proposal_id AND r.quantity_key = d.quantity_key)
	  AND NOT EXISTS (SELECT 1 FROM cloud_meta.happened_answers a WHERE a.tenant_id = i.tenant_id AND a.proposal_id = i.key)))
	  AND ($2 = '' OR i.key = $2)
	ORDER BY i.updated_at DESC
	LIMIT 100`

// Unproven lists the tenant's unanswered unproven money calls, newest
// first; proposal, when set, only that one.
func (p *Postgres) Unproven(ctx context.Context, tenant, proposal string) ([]Unproven, error) {
	return p.unproven(ctx, tenant, proposal, false)
}

// UnprovenCall is proposal as an unproven money call, answered or not: an
// answer that was kept and whose effect didn't finish is finished from it.
func (p *Postgres) UnprovenCall(ctx context.Context, tenant, proposal string) (Unproven, bool, error) {
	calls, err := p.unproven(ctx, tenant, proposal, true)
	if err != nil || len(calls) == 0 {
		return Unproven{}, false, err
	}
	return calls[0], true, nil
}

func (p *Postgres) unproven(ctx context.Context, tenant, proposal string, answered bool) ([]Unproven, error) {
	rows, err := p.pool.Query(ctx, unprovenQuery, tenant, proposal, answered)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Unproven{}
	seen := map[string]bool{}
	for rows.Next() {
		var u Unproven
		var state, outcome string
		if err := rows.Scan(&u.Proposal, &u.Agent, &u.Tool, &u.Title, &state, &outcome, &u.Amount, &u.Unit, &u.Scale, &u.At); err != nil {
			return nil, err
		}
		// A call drawn on several Cards is one question.
		if seen[u.Proposal] {
			continue
		}
		seen[u.Proposal] = true
		u.Status = RefusedByTool
		u.StatusWords = "The tool refused it, and nothing shows that no money moved."
		switch {
		case state == "unknown":
			u.Status = Unknown
			u.StatusWords = "It was sent, but its answer was lost, so KIFF can't tell whether it happened."
		case outcome == "done":
			u.Status = Probation
			u.StatusWords = "One of the first calls of a connection KIFF hasn't tried with a test account: confirm what happened."
		}
		u.Words = u.Title + ": " + formatAmount(u.Amount, u.Scale, u.Unit) + "."
		out = append(out, u)
	}
	return out, rows.Err()
}

// RecordHappened keeps the first answer to proposal's question; a second
// answers the first.
func (p *Postgres) RecordHappened(ctx context.Context, tenant, proposal string, a HappenedAnswer) (HappenedAnswer, error) {
	if _, err := p.pool.Exec(ctx, `INSERT INTO cloud_meta.happened_answers (tenant_id, proposal_id, happened, answered_by, answered_at)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`, tenant, proposal, a.Happened, a.AnsweredBy, a.AnsweredAt); err != nil {
		return HappenedAnswer{}, err
	}
	var got HappenedAnswer
	err := p.pool.QueryRow(ctx, `SELECT happened, answered_by, answered_at FROM cloud_meta.happened_answers WHERE tenant_id = $1 AND proposal_id = $2`,
		tenant, proposal).Scan(&got.Happened, &got.AnsweredBy, &got.AnsweredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return HappenedAnswer{}, errors.New("tooloutcome: the answer was not kept")
	}
	return got, err
}

// formatAmount writes minor units as the unit reads them: 4000 at scale 2
// in EUR is "40.00 EUR".
func formatAmount(n int64, scale int, unit string) string {
	digits := strconv.FormatInt(n, 10)
	if scale > 0 {
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		digits = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	}
	if unit == "" {
		return digits
	}
	return digits + " " + unit
}
