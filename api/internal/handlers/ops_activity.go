package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kiff/cloud/api/internal/receipt"
)

// OpsActivityRow is one entry in GET /v1/ops/activity — a receipt
// minted somewhere on the platform.
type OpsActivityRow struct {
	TenantID   string    `json:"tenant_id"`
	TraceID    string    `json:"trace_id"`
	ProposalID string    `json:"proposal_id,omitempty"`
	Action     string    `json:"action_name"`
	Outcome    string    `json:"outcome,omitempty"`
	IssuedAt   time.Time `json:"issued_at"`
	Anchored   bool      `json:"anchored"`
}

// activity serves GET /v1/ops/activity: the most recent receipts
// across all tenants, newest first — the at-a-glance "is the system
// healthy" feed. Sourced from the single sanctioned cross-tenant
// receipt read, bounded by receipt.OpsActivityLimit.
func (h *OpsHandler) activity(w http.ResponseWriter, r *http.Request) {
	limit := receipt.OpsActivityLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			if n > receipt.OpsActivityLimit {
				n = receipt.OpsActivityLimit
			}
			limit = n
		}
	}
	receipts, err := h.receipts.ListRecentAcrossTenants(r.Context(), limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	rows := make([]OpsActivityRow, 0, len(receipts))
	for _, rc := range receipts {
		rows = append(rows, OpsActivityRow{
			TenantID:   rc.TenantID,
			TraceID:    rc.TraceID,
			ProposalID: rc.ProposalID,
			Action:     rc.ActionName,
			Outcome:    rc.Outcome,
			IssuedAt:   rc.IssuedAt,
			Anchored:   rc.Attestation != nil && rc.Attestation.AnchorTxHash != "",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"activity": rows})
}
