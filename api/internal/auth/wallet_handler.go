package auth

import (
	"encoding/json"
	"errors"
	"net/http"
)

// WalletHandler exposes the optional wallet-link surface:
//
//	POST   /v1/me/wallet/nonce   — issue a single-use nonce
//	POST   /v1/me/wallet/verify  — verify the user's signature, persist the link
//	DELETE /v1/me/wallet         — remove the link
//
// All routes assume the caller is authenticated; wrap with the
// auth.Middleware. The handler reads Identity from the context
// that the middleware attached and uses Identity.Subject as the
// stable user identifier.
type WalletHandler struct {
	service *WalletService
}

// NewWalletHandler returns a handler backed by service.
func NewWalletHandler(service *WalletService) *WalletHandler {
	return &WalletHandler{service: service}
}

// Register attaches the handler's routes to mux.
func (h *WalletHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/me/wallet/nonce", h.nonce)
	mux.HandleFunc("POST /v1/me/wallet/verify", h.verify)
	mux.HandleFunc("DELETE /v1/me/wallet", h.unlink)
	mux.HandleFunc("GET /v1/me/wallet", h.lookup)
}

type nonceRequest struct {
	Address string `json:"address"`
	ChainID uint64 `json:"chain_id"`
}

type nonceResponse struct {
	Nonce     string `json:"nonce"`
	Message   string `json:"message"`
	IssuedAt  string `json:"issued_at"`
	ExpiresAt string `json:"expires_at"`
}

func (h *WalletHandler) nonce(w http.ResponseWriter, r *http.Request) {
	id, err := IdentityFrom(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, err)
		return
	}
	var req nonceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	res, err := h.service.IssueNonce(r.Context(), id.Subject, req.Address, req.ChainID)
	if err != nil {
		switch {
		case errors.Is(err, ErrWalletAddressInvalid),
			errors.Is(err, ErrWalletChainInvalid):
			writeJSONError(w, http.StatusBadRequest, err)
		default:
			writeJSONError(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, nonceResponse{
		Nonce:     res.Nonce,
		Message:   res.Message,
		IssuedAt:  res.IssuedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		ExpiresAt: res.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
}

type verifyRequest struct {
	Nonce     string `json:"nonce"`
	Signature string `json:"signature"`
}

type verifyResponse struct {
	Address  string `json:"address"`
	ChainID  uint64 `json:"chain_id"`
	LinkedAt string `json:"linked_at"`
}

func (h *WalletHandler) verify(w http.ResponseWriter, r *http.Request) {
	id, err := IdentityFrom(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, err)
		return
	}
	var req verifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	if req.Nonce == "" || req.Signature == "" {
		writeJSONError(w, http.StatusBadRequest, errors.New("nonce and signature are required"))
		return
	}
	link, err := h.service.VerifyLink(r.Context(), id.Subject, req.Nonce, req.Signature)
	if err != nil {
		writeJSONError(w, walletStatusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, verifyResponse{
		Address:  link.Address,
		ChainID:  link.ChainID,
		LinkedAt: link.LinkedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
}

func (h *WalletHandler) unlink(w http.ResponseWriter, r *http.Request) {
	id, err := IdentityFrom(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, err)
		return
	}
	if err := h.service.Unlink(r.Context(), id.Subject); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *WalletHandler) lookup(w http.ResponseWriter, r *http.Request) {
	id, err := IdentityFrom(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, err)
		return
	}
	link, err := h.service.Lookup(r.Context(), id.Subject)
	if err != nil {
		if errors.Is(err, ErrWalletNotLinked) {
			writeJSON(w, http.StatusOK, map[string]any{"wallet": nil})
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"wallet": verifyResponse{
			Address:  link.Address,
			ChainID:  link.ChainID,
			LinkedAt: link.LinkedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		},
	})
}

// walletStatusFor maps wallet-related sentinels to HTTP statuses.
func walletStatusFor(err error) int {
	switch {
	case errors.Is(err, ErrWalletNonceNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrWalletNonceExpired),
		errors.Is(err, ErrWalletNonceReplayed),
		errors.Is(err, ErrWalletSignerMismatch),
		errors.Is(err, ErrWalletBadSignature),
		errors.Is(err, ErrWalletAddressInvalid),
		errors.Is(err, ErrWalletChainInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
