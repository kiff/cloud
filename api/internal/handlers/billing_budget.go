package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/tenant"
)

// BillingBudgetHandler serves the tenant-facing monthly governed-
// operation budget (guardrails):
//
//	GET /v1/me/billing-budget   → { "budget_ops": <int> }   (0 = unset)
//	PUT /v1/me/billing-budget   { "budget_ops": <int> }     (0 clears it)
//
// The budget is **alert-only** — it never gates the runtime.
// When usage reaches it, the allowance alerter emails the owner; that
// is its entire effect. The value is read from and written to the
// caller's OWN tenant metadata (identity-scoped, like the billing
// portal), so a caller can only set their own budget.
type BillingBudgetHandler struct {
	tenants tenant.Store
	logger  *slog.Logger
}

// NewBillingBudgetHandler wires the tenant store. Returns nil when the
// store is nil so the route is simply unmounted.
func NewBillingBudgetHandler(tenants tenant.Store, logger *slog.Logger) *BillingBudgetHandler {
	if tenants == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &BillingBudgetHandler{tenants: tenants, logger: logger}
}

// Register mounts the GET/PUT routes. No-op on a nil handler.
func (h *BillingBudgetHandler) Register(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.HandleFunc("GET /v1/me/billing-budget", h.get)
	mux.HandleFunc("PUT /v1/me/billing-budget", h.put)
}

type billingBudgetBody struct {
	BudgetOps int `json:"budget_ops"`
}

func (h *BillingBudgetHandler) tenantID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no tenant identity"})
		return "", false
	}
	return id.Tenant, true
}

func (h *BillingBudgetHandler) get(w http.ResponseWriter, r *http.Request) {
	tid, ok := h.tenantID(w, r)
	if !ok {
		return
	}
	t, err := h.tenants.Get(r.Context(), tid)
	if err != nil {
		h.notFoundOr500(w, err)
		return
	}
	writeJSON(w, http.StatusOK, billingBudgetBody{BudgetOps: budgetFromMeta(t)})
}

func (h *BillingBudgetHandler) put(w http.ResponseWriter, r *http.Request) {
	tid, ok := h.tenantID(w, r)
	if !ok {
		return
	}
	var body billingBudgetBody
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
	}
	if body.BudgetOps < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "budget_ops must be >= 0"})
		return
	}

	// 0 clears the budget (delete the key); any positive value sets it.
	var value any
	if body.BudgetOps > 0 {
		value = strconv.Itoa(body.BudgetOps)
	} else {
		value = nil // SetMetadata deletes on nil
	}
	if err := h.tenants.SetMetadata(r.Context(), tid, billing.MetaBudgetOps, value); err != nil {
		h.notFoundOr500(w, err)
		return
	}
	writeJSON(w, http.StatusOK, billingBudgetBody{BudgetOps: body.BudgetOps})
}

func (h *BillingBudgetHandler) notFoundOr500(w http.ResponseWriter, err error) {
	if errors.Is(err, tenant.ErrNotFound) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "tenant not found"})
		return
	}
	h.logger.Error("billing budget: store error", slog.String("err", err.Error()))
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

// budgetFromMeta reads the tenant's stored budget (0 = unset).
func budgetFromMeta(t tenant.Tenant) int {
	v := metaString(t, billing.MetaBudgetOps)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
