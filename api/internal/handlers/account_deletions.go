package handlers

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/tenant"
)

// Deleting an account from the app, driven by the server.
// The owner's phone signs one intent with its approval key, behind the
// prompt; tafs cloud carries it to each service. Each accepts it first,
// deleting nothing, then finishes once every one has accepted, so a phone
// that dies between steps never leaves the account half-deleted. KIFF
// finishes last, so planque's connector key still works while planque
// erases. The intent's signature is the authority for finishing, so a
// retry needs no live session.
//
// The intent is one field a line, LF-separated, no trailing newline:
//
//	tafs.delete-account.v2
//	tenant=<tenant id>
//	device=<KIFF device id>
//	issued=<RFC 3339, UTC>
//	expires=<issued + at most 10 minutes>
//	nonce=<32 hex>
const (
	deleteIntentHead = "tafs.delete-account.v2"
	deleteIntentMax  = 10 * time.Minute

	// What KIFF keeps of an accepted intent, on the tenant: the intent, its
	// signature and the approval key that made it.
	metaDeleteIntent    = "delete_intent"
	metaDeleteSignature = "delete_intent_signature"
	metaDeleteKey       = "delete_intent_key"
	metaDeleteAccepted  = "delete_intent_accepted_at"
)

var deleteNonce = regexp.MustCompile(`^[0-9a-f]{32}$`)

type deleteIntent struct {
	Tenant, Device, Nonce string
	Issued, Expires       time.Time
}

// parseDeleteIntent reads an intent in exactly its signed form.
func parseDeleteIntent(text string) (deleteIntent, bool) {
	lines := strings.Split(text, "\n")
	if len(lines) != 6 || lines[0] != deleteIntentHead {
		return deleteIntent{}, false
	}
	want := []string{"tenant", "device", "issued", "expires", "nonce"}
	f := map[string]string{}
	for i, k := range want {
		v, ok := strings.CutPrefix(lines[i+1], k+"=")
		if !ok || v == "" {
			return deleteIntent{}, false
		}
		f[k] = v
	}
	issued, err1 := time.Parse(time.RFC3339, f["issued"])
	expires, err2 := time.Parse(time.RFC3339, f["expires"])
	if err1 != nil || err2 != nil || !expires.After(issued) || expires.Sub(issued) > deleteIntentMax || !deleteNonce.MatchString(f["nonce"]) {
		return deleteIntent{}, false
	}
	return deleteIntent{Tenant: f["tenant"], Device: f["device"], Nonce: f["nonce"], Issued: issued, Expires: expires}, true
}

type deletionBody struct {
	Intent    string `json:"intent"`
	Signature string `json:"signature"` // base64 DER ECDSA over the intent's SHA-256
}

// acceptDeletion serves POST /v1/account-deletions, from the person's phone
// session: the intent, signed by that session's approval key, by a person
// who may delete the account. It records the intent and deletes nothing.
func (h *AccountHandler) acceptDeletion(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if !id.IsPerson() || id.AgentID != "" || (id.MemberRole != "" && !can(id, membership.CapDelete)) || (id.MemberRole == "" && !id.HasRole("admin")) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": "deleting the account takes account.delete"})
		return
	}
	if len(id.ApprovalKey) == 0 || id.DeviceID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "approval_key_required",
			"detail": "deleting the account is signed with the phone's approval key, behind its screen lock"})
		return
	}
	var req deletionBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	in, ok := parseDeleteIntent(req.Intent)
	now := time.Now().UTC()
	switch {
	case !ok:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_intent", "detail": "the intent is tafs.delete-account.v2, six lines, expiring at most 10 minutes after it's issued"})
		return
	case in.Tenant != id.Tenant || in.Device != id.DeviceID:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not_this_account", "detail": "the intent names another account or device"})
		return
	case !now.Before(in.Expires) || now.Before(in.Issued.Add(-time.Minute)):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "intent_expired", "detail": "sign the deletion again"})
		return
	case !verifySignature(id.ApprovalKey, []byte(req.Intent), req.Signature):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "signature_invalid"})
		return
	}
	t, err := h.tenants.Get(r.Context(), id.Tenant)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "account unavailable"})
		return
	}
	if t.Status == tenant.StatusDeleted {
		writeJSON(w, http.StatusGone, map[string]string{"error": "the account is already deleted"})
		return
	}
	// The same intent again answers as the first time did. Another, once one
	// is accepted, is refused: tafs cloud finishes with the first, and the
	// account can't be used from then on anyway.
	if prev, _ := t.Metadata[metaDeleteIntent].(string); prev != "" {
		at, _ := t.Metadata[metaDeleteAccepted].(string)
		if prev == req.Intent {
			writeJSON(w, http.StatusAccepted, map[string]string{"accepted_at": at})
			return
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already_accepted", "accepted_at": at,
			"detail": "this account's deletion is already accepted; it finishes with that intent"})
		return
	}
	at := now.Format(time.RFC3339)
	for k, v := range map[string]string{metaDeleteIntent: req.Intent, metaDeleteSignature: req.Signature,
		metaDeleteKey: base64.StdEncoding.EncodeToString(id.ApprovalKey), metaDeleteAccepted: at} {
		if err := h.tenants.SetMetadata(r.Context(), id.Tenant, k, v); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the deletion could not be accepted; try again"})
			return
		}
	}
	// Once accepted, the account can't be used: every phone session is
	// signed out, and every key revoked, agents' included, before anything
	// is erased. planque's connector key is the one kept: planque erases
	// with it before KIFF finishes, last, and finishing revokes it too.
	if h.sessions != nil {
		if err := h.sessions.RevokeAllForTenant(r.Context(), id.Tenant, now); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the deletion could not be accepted; try again"})
			return
		}
	}
	if h.keys != nil {
		keys, err := h.keys.ListActive(r.Context(), id.Tenant)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the deletion could not be accepted; try again"})
			return
		}
		for _, k := range keys {
			if slices.Contains(k.Roles, auth.RoleConnector) {
				continue
			}
			if err := h.keys.Revoke(r.Context(), k.ID, now); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the deletion could not be accepted; try again"})
				return
			}
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"accepted_at": at})
}

// finishDeletion serves POST /v1/account-deletions/finish, with no session.
// Its 403 not_accepted is stable: tafs cloud reads it as "KIFF never took
// this intent" and drops a deletion whose accept answer it lost:
// the accepted intent and its signature, verified again with the approval
// key that made it. Not its expiry: finishing has no deadline. It does what
// POST /v1/me/delete does, idempotently.
func (h *AccountHandler) finishDeletion(w http.ResponseWriter, r *http.Request) {
	t, in, ok := h.acceptedDeletion(w, r)
	if !ok {
		return
	}
	switch t.Status {
	case tenant.StatusDeleted:
		writeJSON(w, http.StatusOK, map[string]string{"status": string(t.Status)})
		return
	case tenant.StatusDeletedPending:
		ends, _ := t.Metadata[tenant.MetadataKeyGraceWindowEnds].(string)
		writeJSON(w, http.StatusOK, map[string]string{"status": string(t.Status), "grace_window_ends": ends})
		return
	}
	resp, err := softDeleteTenant(r.Context(), h.tenants, h.keys, in.Tenant, tenant.DeletedViaApp)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the deletion could not finish; try again"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": resp.Status, "grace_window_ends": resp.GraceWindowEnds})
}

// deletionStatus serves POST /v1/account-deletions/status, with no session:
// whether KIFF accepted this intent, checked as finish checks it, deleting
// nothing. tafs cloud asks it when it lost the answer to accept, to finish
// a deletion KIFF took or drop one it didn't: 200 {accepted_at, status}, or
// the same stable 403 not_accepted.
func (h *AccountHandler) deletionStatus(w http.ResponseWriter, r *http.Request) {
	t, _, ok := h.acceptedDeletion(w, r)
	if !ok {
		return
	}
	at, _ := t.Metadata[metaDeleteAccepted].(string)
	writeJSON(w, http.StatusOK, map[string]string{"accepted_at": at, "status": string(t.Status)})
}

// acceptedDeletion reads a request's intent and the account it names, and
// whether KIFF accepted exactly that intent, verified with the approval key
// stored at accept. Not its expiry: an accepted deletion has no deadline.
// When not, it has answered: 401 without an intent, 403 not_accepted.
func (h *AccountHandler) acceptedDeletion(w http.ResponseWriter, r *http.Request) (tenant.Tenant, deleteIntent, bool) {
	// The signed intent is these routes' only credential: without one, the
	// request is unauthenticated, whatever else it carries.
	var req deletionBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "intent_required", "detail": "this takes the intent the account accepted, and its signature"})
		return tenant.Tenant{}, deleteIntent{}, false
	}
	in, ok := parseDeleteIntent(req.Intent)
	if !ok || req.Signature == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "intent_required", "detail": "this takes the intent the account accepted, and its signature"})
		return tenant.Tenant{}, deleteIntent{}, false
	}
	t, err := h.tenants.Get(r.Context(), in.Tenant)
	if errors.Is(err, tenant.ErrNotFound) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not_accepted"})
		return tenant.Tenant{}, deleteIntent{}, false
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "account unavailable"})
		return tenant.Tenant{}, deleteIntent{}, false
	}
	accepted, _ := t.Metadata[metaDeleteIntent].(string)
	keyB64, _ := t.Metadata[metaDeleteKey].(string)
	key, kerr := base64.StdEncoding.DecodeString(keyB64)
	if accepted != req.Intent || kerr != nil || !verifySignature(key, []byte(req.Intent), req.Signature) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not_accepted", "detail": "KIFF didn't accept this deletion, with this signature"})
		return tenant.Tenant{}, deleteIntent{}, false
	}
	return t, in, true
}
