package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/operatoraudit"
	"github.com/kiff/cloud/api/internal/tenant"
)

// Accounts as the operators see them: the label they give an account, its
// API keys by prefix (which answers whose a key is), and its runs and
// credits from the run ledger. Operator-only, and kept here rather than in
// tenant, which the public export carries.

// opsLabelKey is the Tenant.Metadata key of the operators' label. It is
// display only: no plan, limit or bill reads it.
const opsLabelKey = "ops_label"

// opsLabels are the labels an operator may give an account; "" is none.
var opsLabels = map[string]bool{"": true, "internal": true, "eval": true, "e2e": true, "demo": true}

// opsRunWindow is how far back the account list and the consumption table
// read the run ledger.
const opsRunWindow = 30 * 24 * time.Hour

// OpsRunDays reads the run ledger by account, agent and UTC day, every
// account's when tenantID is "". *meter.Postgres is one.
type OpsRunDays interface {
	RunDays(ctx context.Context, tenantID string, from, to time.Time) ([]meter.RunDay, error)
}

// WithOpsAccounts shows each account's API keys from keys, and its runs and
// credits from runs, and mounts the consumption table. Either may be nil,
// which leaves its part out.
func WithOpsAccounts(keys auth.APIKeyStore, runs OpsRunDays) OpsOption {
	return func(h *OpsHandler) {
		h.keys = keys
		h.runs = runs
	}
}

// opsLabel is t's operators' label, "" for none.
func opsLabel(t tenant.Tenant) string {
	s, _ := t.Metadata[opsLabelKey].(string)
	return s
}

// opsKeyPrefix is the start every API key of tenantID's has: the plaintext
// is the prefix, the tenant's id, "_", then the secret part.
func opsKeyPrefix(tenantID string) string { return auth.APIKeyPrefix + tenantID + "_" }

// opsRow is t's row in the account list and the detail's header.
func opsRow(t tenant.Tenant) OpsTenantRow {
	return OpsTenantRow{
		ID:          t.ID,
		Slug:        t.Slug,
		DisplayName: t.DisplayName,
		Status:      string(t.Status),
		Plan:        string(tenant.PlanFor(t).Plan),
		CreatedAt:   t.CreatedAt,
		Label:       opsLabel(t),
		BilledBy:    string(tenant.BillingSourceOf(t)),
		KeyPrefix:   opsKeyPrefix(t.ID),
	}
}

// addRuns sums days into row's runs, credits and last run.
func (row *OpsTenantRow) addRuns(days []meter.RunDay) {
	for _, d := range days {
		row.Runs += d.Runs
		row.Credits += d.Credits
		if row.LastRunAt == nil || d.Last.After(*row.LastRunAt) {
			last := d.Last
			row.LastRunAt = &last
		}
	}
}

// OpsKey is one of an account's active API keys: never the key or its
// hash, only the prefix every key of the account shares and what the key
// was made for.
type OpsKey struct {
	ID         string     `json:"id"`
	Prefix     string     `json:"prefix"`
	Label      string     `json:"label,omitempty"`
	Agent      string     `json:"agent,omitempty"`
	Roles      []string   `json:"roles,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// OpsAgentRuns are one agent's runs in the window.
type OpsAgentRuns struct {
	Agent        string    `json:"agent"`
	RuntimeAgent string    `json:"runtime_agent"`
	Setup        bool      `json:"setup"`
	Runs         int       `json:"runs"`
	Counted      int       `json:"counted"`
	Credits      int64     `json:"credits"`
	CostMicroUSD int64     `json:"cost_micro_usd"`
	Last         time.Time `json:"last"`
}

// opsKeys are tenantID's active keys, oldest first.
func (h *OpsHandler) opsKeys(ctx context.Context, tenantID string) ([]OpsKey, error) {
	recs, err := h.keys.ListActive(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]OpsKey, 0, len(recs))
	for _, r := range recs {
		out = append(out, OpsKey{
			ID: r.ID, Prefix: opsKeyPrefix(r.Tenant), Label: r.Label, Agent: r.AgentID, Roles: r.Roles,
			CreatedAt: r.CreatedAt, LastUsedAt: r.LastUsedAt, ExpiresAt: r.ExpiresAt,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// byAgent sums days per agent, most credits first, then the latest run.
func byAgent(days []meter.RunDay) []OpsAgentRuns {
	m := map[[3]string]*OpsAgentRuns{}
	for _, d := range days {
		setup := ""
		if d.Setup {
			setup = "setup"
		}
		k := [3]string{d.Agent, d.RuntimeAgent, setup}
		a := m[k]
		if a == nil {
			a = &OpsAgentRuns{Agent: d.Agent, RuntimeAgent: d.RuntimeAgent, Setup: d.Setup}
			m[k] = a
		}
		a.Runs += d.Runs
		a.Counted += d.Counted
		a.Credits += d.Credits
		a.CostMicroUSD += d.CostMicroUSD
		if d.Last.After(a.Last) {
			a.Last = d.Last
		}
	}
	out := make([]OpsAgentRuns, 0, len(m))
	for _, a := range m {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Credits != out[j].Credits {
			return out[i].Credits > out[j].Credits
		}
		return out[i].Last.After(out[j].Last)
	})
	return out
}

// opsLabelRequest is the body of POST /v1/ops/tenants/{id}/label.
type opsLabelRequest struct {
	Label  string `json:"label"`
	Reason string `json:"reason"`
}

// setLabel serves POST /v1/ops/tenants/{id}/label: the operators' label
// on the account, with its audit entry in the same unit. Display only.
func (h *OpsHandler) setLabel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	op := auth.MustIdentity(ctx).Subject

	var body opsLabelRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if r.FormValue("label") != "" { // tolerate form posts from the console
		body.Label, body.Reason = r.FormValue("label"), r.FormValue("reason")
	}
	label := strings.ToLower(strings.TrimSpace(body.Label))
	if !opsLabels[label] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown label: " + label})
		return
	}
	t, err := h.tenants.Get(ctx, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	from := opsLabel(t)
	if from == label {
		writeJSON(w, http.StatusOK, map[string]string{"id": t.ID, "label": label, "note": "no change"})
		return
	}
	entry := operatoraudit.Entry{
		OperatorSubject: op, Action: operatoraudit.ActionLabel, TargetTenantID: t.ID,
		Reason: strings.TrimSpace(body.Reason), Metadata: map[string]any{"from": from, "to": label},
	}
	if err := h.mutator.SetTenantLabel(ctx, t.ID, label, entry); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "mutation failed; label unchanged: " + err.Error(), "id": t.ID, "label": from,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": t.ID, "label": label, "from": from})
}

// OpsConsumptionRow is one account's runs of one agent on one UTC day.
type OpsConsumptionRow struct {
	Day          time.Time `json:"day"`
	TenantID     string    `json:"tenant_id"`
	Slug         string    `json:"slug"`
	Label        string    `json:"label,omitempty"`
	Agent        string    `json:"agent"`
	RuntimeAgent string    `json:"runtime_agent"`
	Setup        bool      `json:"setup"`
	Runs         int       `json:"runs"`
	Counted      int       `json:"counted"`
	Credits      int64     `json:"credits"`
	Over         int64     `json:"over"`
	CostMicroUSD int64     `json:"cost_micro_usd"`
}

// OpsConsumption is the body of GET /v1/ops/consumption.
type OpsConsumption struct {
	From time.Time           `json:"from"`
	To   time.Time           `json:"to"`
	Rows []OpsConsumptionRow `json:"rows"`
	// Total sums the rows.
	Total OpsConsumptionRow `json:"total"`
}

// consumption serves GET /v1/ops/consumption: the run ledger's last 30
// days by day, account and agent, newest day first, with their total.
func (h *OpsHandler) consumption(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	to := h.now().UTC()
	from := to.Add(-opsRunWindow)
	days, err := h.runs.RunDays(ctx, "", from, to)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	tenants, err := h.tenants.List(ctx, tenant.ListFilter{})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	byID := make(map[string]tenant.Tenant, len(tenants))
	for _, t := range tenants {
		byID[t.ID] = t
	}
	out := OpsConsumption{From: from, To: to, Rows: make([]OpsConsumptionRow, 0, len(days))}
	for _, d := range days {
		t := byID[d.Tenant]
		row := OpsConsumptionRow{
			Day: d.Day, TenantID: d.Tenant, Slug: t.Slug, Label: opsLabel(t), Agent: d.Agent, RuntimeAgent: d.RuntimeAgent,
			Setup: d.Setup, Runs: d.Runs, Counted: d.Counted, Credits: d.Credits, Over: d.Over, CostMicroUSD: d.CostMicroUSD,
		}
		out.Rows = append(out.Rows, row)
		out.Total.Runs += d.Runs
		out.Total.Counted += d.Counted
		out.Total.Credits += d.Credits
		out.Total.Over += d.Over
		out.Total.CostMicroUSD += d.CostMicroUSD
	}
	sort.SliceStable(out.Rows, func(i, j int) bool { return out.Rows[i].Day.After(out.Rows[j].Day) })
	writeJSON(w, http.StatusOK, out)
}
