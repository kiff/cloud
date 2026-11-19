package handlers

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/callabout"
	"github.com/kiff/cloud/api/internal/mandate"
)

// CallWordsHandler erases the taf's own words about one call: what it's
// about (callabout) and, on a hold, its kiff_about and kiff_reason. They
// may hold what a customer wrote, so an admin can take them out of the
// record; the call, its decision and its receipt stay exactly as they were,
// and the receipt still verifies, since none of them carries the words.
type CallWordsHandler struct {
	about  callabout.Store
	holds  mandate.TafWordsEraser
	logger *slog.Logger
}

// NewCallWordsHandler erases from about and, when given, from holds.
func NewCallWordsHandler(about callabout.Store, holds mandate.TafWordsEraser, logger *slog.Logger) *CallWordsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &CallWordsHandler{about: about, holds: holds, logger: logger}
}

// Register adds DELETE /v1/me/calls/{proposal_id}/words: the account's
// admins only (auth/policy.go), a person signed in.
func (h *CallWordsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("DELETE /v1/me/calls/{proposal_id}/words", h.erase)
}

func (h *CallWordsHandler) erase(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	if id.APIKey || id.Method == auth.MethodAPIKey {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "human session required"})
		return
	}
	proposal := strings.TrimSpace(r.PathValue("proposal_id"))
	if proposal == "" || len(proposal) > 256 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid call id"})
		return
	}
	about, err := h.about.Erase(r.Context(), id.Tenant, proposal)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "call words: about not erased", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the words could not be erased; try again"})
		return
	}
	held := false
	if h.holds != nil {
		if held, err = h.holds.EraseTafWords(r.Context(), id.Tenant, proposal); err != nil {
			h.logger.ErrorContext(r.Context(), "call words: hold's words not erased", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the words could not be erased; try again"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposal_id": proposal, "erased": about || held})
}
