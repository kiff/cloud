package tooloutcome

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kiff/cloud/api/internal/profile"
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
	// Vendor is the connection's name people read ("Stripe"), from the
	// profile the call was decided under; "" when it had none.
	Vendor string `json:"vendor,omitempty"`
	// Counterparty is what the call acted on, by the profile's resource;
	// nil when the operation names none (kiff/tafs#498).
	Counterparty *Counterparty `json:"counterparty,omitempty"`
	// RecordURL is where the owner looks at the vendor: the operation's
	// record_url filled with the resource's id; "" when the profile names
	// none, or the id gives no link.
	RecordURL string `json:"record_url,omitempty"`
	// HasStatusRead: the profile names a read that finds the operation at
	// the vendor without sending it again.
	HasStatusRead bool `json:"-"`
	// Checking is set while someone asked for help checking it (the
	// answer "not sure"): until then the ask is theirs.
	Checking *Checking `json:"checking,omitempty"`
}

// Counterparty is what a call acted on, in the profile's words: the
// resource's noun ("payment"), the vendor's id for it from the call's
// arguments, and its own name when the gateway read one (label_read).
type Counterparty struct {
	Noun  string `json:"noun"`
	ID    string `json:"id,omitempty"`
	Words string `json:"words,omitempty"`
}

// Checking says who asked for help checking a call, and until when the
// ask is theirs: after that it is everyone's again.
type Checking struct {
	By    string    `json:"by"`
	Until time.Time `json:"until"`
}

// CheckingFor is how long a "not sure" keeps the ask with the person who
// asked for help.
const CheckingFor = 24 * time.Hour

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
	COALESCE((i.measure->>'scale')::int, (op->'amount'->>'scale')::int, t.amount_scale, 0), i.updated_at,
	COALESCE(p.profile->>'vendor', ''), i.arguments, op->'resource', COALESCE(op->>'record_url', ''), COALESCE(op ? 'status_read', false),
	COALESCE(i.measure->>'counterparty', ''), COALESCE(c.checked_by, ''), c.until_at
	FROM gateway.invocations i
	JOIN cloud_meta.mandate_draws d ON d.tenant_id = i.tenant_id AND d.proposal_id = i.key AND d.quantity_key LIKE 'sum(%'
	LEFT JOIN gateway.tools t ON t.tenant_id = i.tenant_id AND t.name = i.tool
	LEFT JOIN gateway.profiles p ON p.tenant_id = i.tenant_id AND p.revision = i.profile_revision
	LEFT JOIN LATERAL (SELECT o FROM jsonb_array_elements(p.profile->'operations') o WHERE o->>'tool' = i.tool LIMIT 1) ops(op) ON true
	LEFT JOIN cloud_meta.happened_checking c ON c.tenant_id = i.tenant_id AND c.proposal_id = i.key AND c.until_at > $4
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
	rows, err := p.pool.Query(ctx, unprovenQuery, tenant, proposal, answered, p.clock())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Unproven{}
	seen := map[string]bool{}
	for rows.Next() {
		var u Unproven
		var state, outcome, recordURL, counterpartyWords, checkedBy string
		var args, resource []byte
		var until *time.Time
		if err := rows.Scan(&u.Proposal, &u.Agent, &u.Tool, &u.Title, &state, &outcome, &u.Amount, &u.Unit, &u.Scale, &u.At,
			&u.Vendor, &args, &resource, &recordURL, &u.HasStatusRead, &counterpartyWords, &checkedBy, &until); err != nil {
			return nil, err
		}
		// A call drawn on several Cards is one question.
		if seen[u.Proposal] {
			continue
		}
		seen[u.Proposal] = true
		u.Counterparty = counterpartyOf(resource, args, counterpartyWords)
		if u.Counterparty != nil && recordURL != "" {
			u.RecordURL = profile.RecordURL(recordURL, u.Counterparty.ID)
		}
		if checkedBy != "" && until != nil {
			u.Checking = &Checking{By: checkedBy, Until: *until}
		}
		u.Status = RefusedByTool
		u.StatusWords = "The app refused it. KIFF can't confirm that no money moved."
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

// counterpartyOf is what the call acted on: the profile's resource (its
// noun and where the call carries the vendor's id), read from the call's
// arguments, with the name the gateway read for it, if any. Nil without a
// resource.
func counterpartyOf(resource, args []byte, words string) *Counterparty {
	if len(resource) == 0 {
		return nil
	}
	var r profile.Resource
	if err := json.Unmarshal(resource, &r); err != nil || r.Noun == "" {
		return nil
	}
	c := &Counterparty{Noun: r.Noun, Words: cleanWords(words)}
	var a map[string]any
	if err := json.Unmarshal(args, &a); err != nil {
		return c
	}
	var v any
	var ok bool
	switch {
	case r.Path != "":
		v, ok = valueAt(a, r.Path)
	case r.Arg != "":
		v, ok = a[r.Arg]
	}
	if ok {
		switch id := v.(type) {
		case string:
			c.ID = cleanID(id)
		case float64:
			c.ID = cleanID(strconv.FormatFloat(id, 'f', -1, 64))
		case json.Number:
			c.ID = cleanID(id.String())
		}
	}
	return c
}

// MaxCounterpartyID bounds a vendor's id as the question carries it.
const MaxCounterpartyID = 128

// cleanID keeps a vendor's id only when it reads as one: one line of at most
// MaxCounterpartyID characters, letters, digits and _-.:/@. The call's
// arguments are the taf's model's words, so anything else (a newline, a
// bracket, a control character) is dropped rather than shown to the owner
// or handed to Coach as its task.
func cleanID(id string) string {
	if id == "" || len(id) > MaxCounterpartyID {
		return ""
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.', r == ':', r == '/', r == '@':
		default:
			return ""
		}
	}
	return id
}

// MaxCounterpartyWords bounds the resource's own name as the question carries it.
const MaxCounterpartyWords = 200

// cleanWords keeps the resource's name (a customer's, an invoice's number)
// read from a vendor only as one line of printable text, at most
// MaxCounterpartyWords characters, with no bracket that could pass for one
// of tafs cloud's own markers; anything else is dropped.
func cleanWords(words string) string {
	words = strings.TrimSpace(words)
	if words == "" || len(words) > MaxCounterpartyWords {
		return ""
	}
	for _, r := range words {
		if r < 0x20 || r == 0x7f || r == '[' || r == ']' {
			return ""
		}
	}
	return words
}

// valueAt reads the value at a dotted path in the call's arguments, as the
// gateway reads a profile's paths.
func valueAt(args map[string]any, path string) (any, bool) {
	var cur any = args
	for _, s := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[s]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// MarkChecking keeps that by asked for help checking proposal, until
// until: the ask is theirs till then, and a second ask while it holds
// keeps the first. It answers the mark in force.
func (p *Postgres) MarkChecking(ctx context.Context, tenant, proposal, by string, until time.Time) (Checking, error) {
	if _, err := p.pool.Exec(ctx, `INSERT INTO cloud_meta.happened_checking (tenant_id, proposal_id, checked_by, until_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, proposal_id) DO UPDATE SET checked_by = EXCLUDED.checked_by, until_at = EXCLUDED.until_at
		WHERE happened_checking.until_at <= $5`, tenant, proposal, by, until, p.clock()); err != nil {
		return Checking{}, err
	}
	var c Checking
	err := p.pool.QueryRow(ctx, `SELECT checked_by, until_at FROM cloud_meta.happened_checking WHERE tenant_id = $1 AND proposal_id = $2`,
		tenant, proposal).Scan(&c.By, &c.Until)
	if errors.Is(err, pgx.ErrNoRows) {
		return Checking{}, errors.New("tooloutcome: the mark was not kept")
	}
	return c, err
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
