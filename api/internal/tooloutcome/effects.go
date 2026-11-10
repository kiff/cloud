package tooloutcome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kiff/cloud/api/internal/mandate"
)

// VerifiedReader reads the gateway's verify reads, as EvidenceReader reads
// its terminal outcomes.
type VerifiedReader interface {
	Verified(context.Context, string, string) (mandate.VerifiedEvidence, bool, error)
}

// EvidenceReader reads only evidence recorded by the gateway's trusted
// adapter, after the exact invocation has entered its durable terminal state.
type EvidenceReader interface {
	NoEffect(context.Context, string, string) (mandate.NoEffectEvidence, bool, error)
}

// SourceNotSent is the evidence of a call the gateway recorded as never
// sent: KIFF allowed it, and it ended `failed` before anything reached the
// tool (its permit wasn't issued, its credential couldn't be read, the tool
// couldn't be reached, or the gateway restarted while deciding). The
// gateway never moves a failed call on, so nothing can send it later.
const SourceNotSent = "gateway:not_sent"

// NoEffect is the trusted evidence that proposal's call had no effect:
// the vendor's terminal refusal under terminal_no_effect_v1, the
// gateway's own record that it was never sent, or a refund the processor
// says failed (refunds.go).
func (p *Postgres) NoEffect(ctx context.Context, tenant, proposal string) (mandate.NoEffectEvidence, bool, error) {
	e, found, err := p.terminalNoEffect(ctx, tenant, proposal)
	if found || err != nil {
		return e, found, err
	}
	if e, found, err = p.notSent(ctx, tenant, proposal); found || err != nil {
		return e, found, err
	}
	return p.refundFailed(ctx, tenant, proposal)
}

// notSent is the evidence of a call the gateway recorded as `failed`.
func (p *Postgres) notSent(ctx context.Context, tenant, proposal string) (mandate.NoEffectEvidence, bool, error) {
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='gateway' AND table_name='invocations')`).Scan(&exists); err != nil || !exists {
		return mandate.NoEffectEvidence{}, false, err
	}
	var agent, action string
	var at time.Time
	err := p.pool.QueryRow(ctx, `SELECT agent_id,tool,updated_at FROM gateway.invocations WHERE tenant_id=$1 AND key=$2 AND state='failed'`,
		tenant, proposal).Scan(&agent, &action, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return mandate.NoEffectEvidence{}, false, nil
	}
	if err != nil {
		return mandate.NoEffectEvidence{}, false, err
	}
	return mandate.NoEffectEvidence{TenantID: tenant, ProposalID: proposal, Subject: agent, Action: action, Source: SourceNotSent,
		EvidenceID: proposal, Reason: "the gateway recorded it as not sent", At: at}, true, nil
}

func (p *Postgres) terminalNoEffect(ctx context.Context, tenant, proposal string) (mandate.NoEffectEvidence, bool, error) {
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='gateway' AND table_name='invocations' AND column_name='no_effect_evidence')`).Scan(&exists); err != nil || !exists {
		return mandate.NoEffectEvidence{}, false, err
	}
	rows, err := p.pool.Query(ctx, `SELECT agent_id,tool,updated_at,no_effect_evidence FROM gateway.invocations WHERE tenant_id=$1 AND key=$2 AND state='forwarded' AND tool_outcome='error' AND no_effect_evidence IS NOT NULL`, tenant, proposal)
	if err != nil {
		return mandate.NoEffectEvidence{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return mandate.NoEffectEvidence{}, false, rows.Err()
	}
	var agent, action string
	var at time.Time
	var raw []byte
	if err := rows.Scan(&agent, &action, &at, &raw); err != nil {
		return mandate.NoEffectEvidence{}, false, err
	}
	var e struct {
		Contract  string `json:"contract"`
		Operation string `json:"operation_id"`
		Agent     string `json:"agent_id"`
		Action    string `json:"action"`
		Reason    string `json:"reason"`
		Source    string `json:"source"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return mandate.NoEffectEvidence{}, false, err
	}
	if e.Contract != "terminal_no_effect_v1" || e.Operation != proposal || e.Agent != agent || e.Action != action || strings.TrimSpace(e.Reason) == "" || (e.Source != "" && e.Source != "read_only_lookup") {
		return mandate.NoEffectEvidence{}, false, fmt.Errorf("invalid persisted terminal outcome binding")
	}
	source := "gateway:terminal_no_effect_v1"
	if e.Source == "read_only_lookup" {
		source += ":read_only_lookup"
	}
	return mandate.NoEffectEvidence{TenantID: tenant, ProposalID: proposal, Subject: agent, Action: action, Source: source, EvidenceID: proposal, Reason: e.Reason, At: at}, true, nil
}

// Verified is the gateway's verify read of a forwarded call, when it found
// the vendor used more than KIFF decided (connection profiles: the verify read). It reads
// only what the gateway recorded for that exact call.
func (p *Postgres) Verified(ctx context.Context, tenant, proposal string) (mandate.VerifiedEvidence, bool, error) {
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='gateway' AND table_name='invocations' AND column_name='verify_evidence')`).Scan(&exists); err != nil || !exists {
		return mandate.VerifiedEvidence{}, false, err
	}
	var agent, action string
	var raw []byte
	err := p.pool.QueryRow(ctx, `SELECT agent_id,tool,verify_evidence FROM gateway.invocations
		WHERE tenant_id=$1 AND key=$2 AND state='forwarded' AND tool_outcome='done' AND verify_evidence IS NOT NULL`, tenant, proposal).Scan(&agent, &action, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return mandate.VerifiedEvidence{}, false, nil
	}
	if err != nil {
		return mandate.VerifiedEvidence{}, false, err
	}
	var e struct {
		Check     string `json:"check"`
		Result    string `json:"result"`
		AmountArg string `json:"amount_argument"`
		Decided   *int64 `json:"decided_amount"`
		Vendor    *int64 `json:"vendor_amount"`
		At        string `json:"at"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return mandate.VerifiedEvidence{}, false, err
	}
	if e.Check != "amount" || e.Result != "mismatch" || e.Decided == nil || e.Vendor == nil || *e.Vendor <= *e.Decided || e.AmountArg == "" {
		return mandate.VerifiedEvidence{}, false, nil
	}
	at, err := time.Parse(time.RFC3339, e.At)
	if err != nil {
		return mandate.VerifiedEvidence{}, false, fmt.Errorf("invalid persisted verify read: %w", err)
	}
	return mandate.VerifiedEvidence{TenantID: tenant, ProposalID: proposal, Subject: agent, Action: action, AmountArg: e.AmountArg,
		Decided: *e.Decided, Vendor: *e.Vendor, At: at, Source: "gateway:verify_read"}, true, nil
}
