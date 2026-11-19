package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// draftHold is the held install a draft's confirm also answers: Coach's
// call that installs the version whose Card the draft is. The phone's
// signature covers the confirm's body, so it binds this hold at this
// accounting version, as a signed answer does.
type draftHold struct {
	ID                string `json:"id"`
	AccountingVersion int    `json:"accounting_version"`
}

// installAction is the only held call a confirm answers: installing a
// version, which lets Coach read how the install stands and grants
// nothing itself. Any other held call, a refund of the same taf among
// them, is answered on its own, behind its own fingerprint.
//
// This is safe only while version_install grants nothing. KIFF can't
// check that the hold names this draft's taf and version (the hold's
// taf is tafs' id or name, not the draft's KIFF agent), so a confirm
// may approve another version's install hold. If version_install ever
// installs or grants anything by itself, bind the hold to the draft's
// taf and version first.
const installAction = "version_install"

// Why a confirm didn't take its hold. The app then answers it the usual
// way; the draft is confirmed either way.
const (
	holdApproved            = "approved"
	holdNotFound            = "not_found"
	holdNotThisInstall      = "not_this_install"
	holdAnswered            = "answered"
	holdExpired             = "expired"
	holdApprovalKeyRequired = "approval_key_required"
	holdRefused             = "refused"
	holdFailed              = "failed"
)

// answerDraftHold approves once the held install a confirmed draft named,
// under the confirm's own signature. Only a phone's approval key counts,
// behind its fingerprint: a confirm signed inside an approval window
// takes no hold, as a window never answers one. The hold must be the
// account's, still held at the version the phone signed, and an install;
// the same gate as any approve-once applies (the role, separation of
// duties). The receipt's proof is the confirm's signature.
func (h *MandatesHandler) answerDraftHold(r *http.Request, ident auth.Identity, hold draftHold, body []byte) map[string]any {
	ctx, tenantID, now := r.Context(), ident.Tenant, h.now()
	out := func(status, detail string) map[string]any {
		v := map[string]any{"id": hold.ID, "status": status}
		if detail != "" {
			v["detail"] = detail
		}
		return v
	}
	signed, ok := auth.SignedWithFrom(ctx)
	if !ok || !signed.Critical() || len(ident.ApprovalKey) == 0 && signed.Passkey == nil {
		return out(holdApprovalKeyRequired, "only a confirm the phone's approval key signed, behind its fingerprint, answers a hold: answer it on its own")
	}
	allowed := ident.ManagesAccount()
	if ident.MemberRole != "" {
		allowed = membership.Can(ident.MemberRole, membership.CapHoldsAnswer)
	}
	if !allowed {
		return out(holdRefused, "answering held calls isn't this role's")
	}
	e, err := h.store.Exception(ctx, tenantID, hold.ID)
	switch {
	case errors.Is(err, mandate.ErrExceptionNotFound):
		return out(holdNotFound, "")
	case err != nil:
		h.logger.ErrorContext(ctx, "draft confirm: load hold failed", slog.String("tenant", tenantID), slog.String("exception", hold.ID), slog.String("err", err.Error()))
		return out(holdFailed, "KIFF couldn't read the hold: answer it on its own")
	}
	if e.KindOf() != mandate.KindHold || e.Action != installAction {
		return out(holdNotThisInstall, "a confirm answers only the held call that installs a version")
	}
	if e.Status != mandate.ExceptionHeld || e.AccountingVersion != hold.AccountingVersion {
		return out(holdAnswered, "this hold was already answered, or its Card changed: reload it")
	}
	gate := Hold{Proposers: []string{e.Subject}, AskedBy: e.Request.AskedBy}
	for _, b := range e.Breaches {
		gate.MandateIDs = append(gate.MandateIDs, b.MandateID)
	}
	gov, err := h.holds.Check(ctx, ident, tenantID, gate, false)
	if err != nil {
		return out(holdRefused, err.Error())
	}
	e, err = h.store.ApproveOnce(ctx, tenantID, hold.ID, issuerOf(ident), now)
	switch {
	case errors.Is(err, mandate.ErrExceptionExpired):
		return out(holdExpired, "this hold expired before it was answered: Coach has to ask again")
	case errors.Is(err, mandate.ErrExceptionState), errors.Is(err, mandate.ErrCumulativeApproval):
		return out(holdAnswered, "this hold was already answered, or its Card changed: reload it")
	case errors.Is(err, mandate.ErrExceptionNotFound):
		return out(holdNotFound, "")
	case err != nil:
		h.logger.ErrorContext(ctx, "draft confirm: approve hold failed", slog.String("tenant", tenantID), slog.String("exception", hold.ID), slog.String("err", err.Error()))
		return out(holdFailed, "KIFF couldn't answer the hold: answer it on its own")
	}
	answeredAt := now
	if e.ResolvedAt != nil {
		answeredAt = *e.ResolvedAt
	}
	if rerr := h.holds.RecordSigned(ctx, ident, tenantID, e.ID, "approve-once", gov, answeredAt, confirmProof(r, ident, body)); rerr != nil {
		h.logger.ErrorContext(ctx, "draft confirm: hold attribution not recorded",
			slog.String("tenant", tenantID), slog.String("exception", e.ID), slog.String("err", rerr.Error()))
	}
	h.alertAnswered(e, now)
	return out(holdApproved, "")
}

// confirmProof is the receipt's proof for a hold a confirm answered: the
// confirm's own signed message (kiff.request.v1, its body's digest
// naming the hold) and the approval key's signature over it.
func confirmProof(r *http.Request, ident auth.Identity, body []byte) *membership.DeviceProof {
	header := r.Header.Get(auth.DeviceSignatureHeader)
	// Approved by a computer's own passkey: its key, and the assertion.
	if with, ok := auth.SignedWithFrom(r.Context()); ok && with.Passkey != nil {
		header = r.Header.Get(auth.PasskeyAssertionHeader)
	}
	var at, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			at = v
		case "v1":
			sig = v
		}
	}
	keyID := ident.DeviceSessionID
	if ident.DeviceID != "" {
		keyID = ident.DeviceID
	}
	key := ident.ApprovalKey
	if with, ok := auth.SignedWithFrom(r.Context()); ok && with.Passkey != nil {
		key, sig = with.Passkey, with.Assertion
	}
	return &membership.DeviceProof{Name: ident.DeviceName, KeyID: keyID, PublicKey: key,
		Message: string(auth.RequestMessage(ident.Tenant, r.Method, r.URL.RequestURI(), body, at)), Signature: sig}
}
