package handlers

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
)

// A person's passkeys (the desktop-client design, §2.6): a phone's passkey through
// a QR code, or a security key, registered for the account so a computer that
// holds no approval key of its own (Linux, a Mac without Touch ID) approves
// with it, an assertion each time.
//
//	POST   /v1/me/passkeys        register one, signed by an approval device's approval key
//	GET    /v1/me/passkeys        the person's passkeys
//	DELETE /v1/me/passkeys/{id}   remove one: an approval (the approval key, or a passkey's assertion)

// registerPasskey serves POST /v1/me/passkeys. Registering a passkey gives
// approval authority, so only an existing approval device's approval key
// signs it (never a passkey, a window or a device key alone), and only an
// account's owners and admins register one.
func (h *DeviceAuthHandler) registerPasskey(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if with, signed := auth.SignedWithFrom(r.Context()); !signed || !with.Critical() || with.Passkey != nil || len(id.ApprovalKey) == 0 || id.DeviceID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "approval_key_required",
			"message": "registering a passkey gives approval authority: sign it with a phone's approval key, behind its fingerprint"})
		return
	}
	if !id.ManagesAccount() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admins_only",
			"message": "only the account's owners and admins register a passkey"})
		return
	}
	// The registration's body: the creation's response, which proves the key
	// (§2.6), or, until both apps send that, the credential id and key alone.
	var req struct {
		Name              string `json:"name"`
		IssuedAt          string `json:"issued_at"`
		ClientDataJSON    string `json:"client_data_json"`
		AttestationObject string `json:"attestation_object"`
		CredentialID      string `json:"credential_id"`
		PublicKey         string `json:"public_key"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	name, nameOK := cleanDeviceName(req.Name)
	if !nameOK || name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_name", "message": "name is 1 to 64 characters, without control characters"})
		return
	}
	p := auth.StoredPasskey{Passkey: auth.Passkey{Tenant: id.Tenant}, Subject: id.Subject, KeyID: auth.NewPasskeyID(), Name: name, CreatedAt: h.clock()}
	proof := req.ClientDataJSON != "" || req.AttestationObject != "" || req.IssuedAt != ""
	switch {
	case proof && (req.CredentialID != "" || req.PublicKey != ""):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request",
			"message": "send the creation's response (issued_at, client_data_json, attestation_object) or credential_id and public_key, not both"})
		return
	case proof:
		client, err1 := base64.RawURLEncoding.DecodeString(strings.TrimRight(req.ClientDataJSON, "="))
		object, err2 := base64.RawURLEncoding.DecodeString(strings.TrimRight(req.AttestationObject, "="))
		rp := h.passkeyRP
		if rp.ID == "" {
			rp = auth.KiffRelyingParty
		}
		var reg auth.Registered
		err := errors.Join(err1, err2)
		if err == nil {
			reg, err = auth.VerifyRegistration(rp, id.Tenant, id.Subject, req.IssuedAt, client, object, h.clock())
		} else {
			err = auth.ErrRegistrationShape
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "registration_invalid", "reason": registrationReason(err), "message": err.Error()})
			return
		}
		synced := reg.Synced
		p.ID, p.PublicKey, p.SignCount, p.Synced = reg.CredentialID, reg.PublicKey, reg.SignCount, &synced
	default:
		credential, ok := auth.ParsePairingCredentialID(req.CredentialID)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_credential_id",
				"message": "credential_id is the WebAuthn credential id, base64url without padding, at most 1023 bytes"})
			return
		}
		key, err := auth.ParseDeviceKey(req.PublicKey)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_public_key",
				"message": "public_key is the browser's getPublicKey(): base64 SPKI DER, P-256 (ES256)"})
			return
		}
		p.ID, p.PublicKey = credential, key
	}
	switch err := h.passkeys.Insert(r.Context(), p); {
	case errors.Is(err, auth.ErrPasskeyDuplicate):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "passkey_registered", "message": "this passkey is already registered on the account"})
		return
	case errors.Is(err, auth.ErrTestTenantKey):
		writeTestTenantKey(w, err)
		return
	case err != nil:
		h.logger.ErrorContext(r.Context(), "passkeys: register failed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not register the passkey"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"passkey": passkeyView(p)})
}

// registrationReason is which of a registration's checks refused it, as the
// answer's reason says (§2.6).
func registrationReason(err error) string {
	for _, r := range []struct {
		err    error
		reason string
	}{
		{auth.ErrRegistrationIssued, "issued_at"},
		{auth.ErrRegistrationType, "type"},
		{auth.ErrRegistrationOrigin, "origin"},
		{auth.ErrRegistrationChallenge, "challenge"},
		{auth.ErrRegistrationAttestation, "attestation"},
		{auth.ErrRegistrationUser, "user_verification"},
		{auth.ErrRegistrationKey, "key"},
	} {
		if errors.Is(err, r.err) {
			return r.reason
		}
	}
	return "malformed"
}

// passkeyView is a passkey as the apps list it. synced stays null until an
// assertion said.
func passkeyView(p auth.StoredPasskey) map[string]any {
	v := map[string]any{"id": p.KeyID, "name": p.Name, "synced": p.Synced, "created_at": p.CreatedAt.UTC().Format(time.RFC3339)}
	if p.DeviceID != "" {
		v["device_id"] = p.DeviceID
	}
	return v
}

// listPasskeys serves GET /v1/me/passkeys: the person's passkeys on the
// account, a computer's own included (with its device_id; it goes with the
// device, never on its own).
func (h *DeviceAuthHandler) listPasskeys(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.APIKey {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "passkeys belong to a signed-in person"})
		return
	}
	keys, err := h.passkeys.ListForPerson(r.Context(), id.Tenant, id.Subject)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list the passkeys"})
		return
	}
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, passkeyView(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"passkeys": out})
}

// removePasskey serves DELETE /v1/me/passkeys/{id}. Removing one is an
// approval: from a device's session, its approval key or a passkey's
// assertion signs it (the route policy checks which), never its device key
// alone. A browser session removes it as it removes a lost phone.
func (h *DeviceAuthHandler) removePasskey(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.APIKey {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "passkeys belong to a signed-in person"})
		return
	}
	if len(id.DeviceKey) > 0 {
		if with, signed := auth.SignedWithFrom(r.Context()); !signed || !with.Critical() {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "approval_key_required",
				"message": "removing a passkey is an approval: sign it with your approval key, or approve it with a passkey"})
			return
		}
	}
	switch err := h.passkeys.Revoke(r.Context(), id.Tenant, id.Subject, r.PathValue("id"), h.clock()); {
	case errors.Is(err, auth.ErrPasskeyNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such passkey"})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not remove the passkey"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
