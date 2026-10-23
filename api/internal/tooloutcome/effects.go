package tooloutcome

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// EvidenceReader reads only evidence recorded by the gateway's trusted
// adapter, after the exact invocation has entered its durable terminal state.
type EvidenceReader interface {
	NoEffect(context.Context, string, string) (mandate.NoEffectEvidence, bool, error)
}

func (p *Postgres) NoEffect(ctx context.Context, tenant, proposal string) (mandate.NoEffectEvidence, bool, error) {
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
