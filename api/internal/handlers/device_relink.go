package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/notify"
	"github.com/kiff/cloud/api/internal/personlang"
	"github.com/kiff/cloud/api/internal/words"
)

// Re-linking a phone's approval key (kiff/tafs#233, case 2). A face or
// fingerprint added on the phone ends its approval key for good, and the
// phone alone can't vouch for a new one: its device key signs with no one
// at the screen, and the new key's prompt accepts the newly enrolled
// finger. So the person approves the new key in their browser, as for a
// sign-in, but the session goes on: its alerts, its list and its device
// stay, and only the approval key changes.
//
//	POST /v1/me/device/relink     (device-signed) ask, with the new key's proof
//	GET  /v1/auth/device/pending  the page says it's a re-link, and which phone
//	POST /v1/auth/device/approve  the session's own person approves it
//	POST /v1/auth/device/token    the phone polls: {"status": "relinked"}

// relink handles POST /v1/me/device/relink: a phone's session, signed by
// its device key, asks to approve with a new approval key. The new key
// signs auth.RelinkMessage under its biometric prompt, so KIFF knows it
// works before anything changes; nothing is stored until the browser
// approves. One re-link at a time per session: a new one cancels the last.
func (h *DeviceAuthHandler) relink(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if id.DeviceSessionID == "" || len(id.DeviceKey) == 0 || !auth.DeviceSigned(r.Context()) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "device_required",
			"detail": "a phone's session with a device key re-links its approval key, signed by that key"})
		return
	}
	// A view-only session has no approval key to re-link, and gets one only
	// by pairing (the desktop-client design, §5), never from the browser.
	if id.ViewOnly() {
		writeJSON(w, http.StatusForbidden, auth.ViewOnlyRefusal())
		return
	}
	// A software-keyed computer never holds an approval key: its security key
	// approves.
	if id.SoftwareKeyPasskey() {
		writeJSON(w, http.StatusForbidden, auth.SecurityKeyOnlyRefusal())
		return
	}
	var req struct {
		ApprovalPublicKey string `json:"approval_public_key"`
		SignedAt          string `json:"signed_at"`
		Signature         string `json:"signature"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	key, err := auth.ParseDeviceKey(req.ApprovalPublicKey)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_approval_public_key", "detail": err.Error()})
		return
	}
	if string(key) == string(id.DeviceKey) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_approval_public_key", "detail": "the approval key is a second key, not the device key"})
		return
	}
	now := h.clock()
	if err := auth.VerifyRelink(key, id.Tenant, id.DeviceKey, strings.TrimSpace(req.SignedAt), req.Signature, now); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_proof",
			"detail": "the new approval key signs kiff.approval-key-relink.v1, within 5 minutes of now"})
		return
	}
	// The page names the phone as its person knows it.
	name := ""
	if h.devices != nil {
		if d, err := h.devices.FindByKey(r.Context(), id.Tenant, id.Subject, id.DeviceKey); err == nil {
			name = d.Name
		}
	}

	deviceCode, err := randToken(32)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not start the re-link"})
		return
	}
	userCode, err := newUserCode()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not start the re-link"})
		return
	}
	hash := hashHex(deviceCode)

	// One kept by a process before a restart goes too, and the cap counts
	// what waits: both read outside the lock.
	if h.waiting != nil {
		if err := h.waiting.DropRelinks(r.Context(), id.DeviceSessionID); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "could not start the re-link; retry shortly"})
			return
		}
	}
	if n, err := h.waitingCount(r.Context(), now); err != nil || n >= h.maxPending {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "too many pending device authorizations; retry shortly"})
		return
	}
	h.mu.Lock()
	for _, p := range h.pending {
		if p.relinkSession != id.DeviceSessionID {
			continue
		}
		if p.status == "approving" {
			// Its key is being written now: cancelling it could leave KIFF
			// holding a key the phone has let go of.
			h.mu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]string{"error": "relink_in_progress", "detail": "a re-link of this phone is being approved; poll it"})
			return
		}
		// One at a time: polling the last one's device_code now says expired.
		h.deleteLocked(p)
	}
	h.mu.Unlock()
	p := &pendingAuth{
		deviceHash:    hash,
		userCode:      userCode,
		status:        "pending",
		subject:       id.Subject,
		tenant:        id.Tenant,
		createdAt:     now,
		expiresAt:     now.Add(h.deviceTTL),
		deviceKey:     id.DeviceKey,
		deviceName:    name,
		approvalKey:   key,
		relinkSession: id.DeviceSessionID,
	}
	if err := h.keep(r.Context(), p); err != nil {
		h.logger.ErrorContext(r.Context(), "device relink: sign-in not kept", slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not start the re-link"})
		return
	}

	writeJSON(w, http.StatusOK, deviceStartResponse{
		DeviceCode:              deviceCode,
		UserCode:                userCode,
		VerificationURI:         h.verificationURI,
		VerificationURIComplete: h.verificationURI + "?code=" + userCode,
		ExpiresIn:               int(h.deviceTTL.Seconds()),
		Interval:                int(h.interval.Seconds()),
	})
}

// approveRelink approves a re-link from the browser. Called by approve
// with h.mu held and p pending; it releases the lock. Only the session's
// own person, signed in on the same account, may approve it: to anyone
// else the code is unknown, and the miss counts toward their rate cap.
func (h *DeviceAuthHandler) approveRelink(w http.ResponseWriter, r *http.Request, id auth.Identity, p *pendingAuth, now time.Time) {
	if p.subject != id.Subject || p.tenant != id.Tenant {
		h.mu.Unlock()
		h.recordApproveFail(id.Subject, now)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown or expired code"})
		return
	}
	if p.status != "pending" {
		h.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this code was already approved"})
		return
	}
	// Claimed before the write, so a new re-link can't cancel it midway,
	// and claimed in h.waiting, so another process can't approve it too.
	p.status = "approving"
	sessionID, key, name, deviceKey, userCode := p.relinkSession, p.approvalKey, p.deviceName, p.deviceKey, p.userCode
	h.mu.Unlock()
	if h.waiting != nil {
		won, err := h.waiting.Claim(r.Context(), p.deviceHash, now)
		if err != nil {
			// Not claimed: it stays waiting, here and in h.waiting, for a retry.
			h.mu.Lock()
			if p.status == "approving" {
				p.status = "pending"
			}
			h.mu.Unlock()
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not re-link the approval key; retry"})
			return
		}
		if !won {
			h.mu.Lock()
			h.deleteLocked(p)
			h.mu.Unlock()
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown or expired code"})
			return
		}
	}

	// The device holds an approval key (HasApprovalDevice); a session from
	// before approval keys may get its first one this way. Best effort: the
	// devices kept before this was recorded take it from their sessions at
	// startup.
	if h.devices != nil {
		if d, derr := h.devices.FindByKey(r.Context(), id.Tenant, id.Subject, deviceKey); derr == nil {
			if merr := h.devices.MarkApprovalKey(r.Context(), d.ID); merr != nil {
				h.logger.WarnContext(r.Context(), "device relink: device not marked", slog.String("device", d.ID), slog.String("err", merr.Error()))
			}
		}
	}
	old, err := h.sessions.ReplaceApprovalKey(r.Context(), sessionID, key, now)
	if err == nil {
		// The windows the old key opened end with it (kiff/tafs#494).
		h.closeWindows(r, auth.WindowMatch{Session: sessionID}, auth.WindowClosedByKeyChange)
	}
	h.mu.Lock()
	if err != nil {
		h.deleteLocked(p)
		h.mu.Unlock()
		if errors.Is(err, auth.ErrDevSessionNotFound) {
			// The phone signed out, or its session ended, since it asked.
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown or expired code"})
			return
		}
		h.logger.ErrorContext(r.Context(), "device relink: replace approval key failed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not re-link the approval key"})
		return
	}
	// The key is replaced whether or not the phone polls again in time: it
	// answers relinked until the record expires.
	p.status, p.relinked = "approved", true
	if p.expiresAt.Before(now.Add(h.interval * 4)) {
		p.expiresAt = now.Add(h.interval * 4)
	}
	// Evicted while it was written, at the very end of its time: put back,
	// so the phone still learns its new key counts.
	h.pending[p.deviceHash], h.byUserCode[p.userCode] = p, p.deviceHash
	h.mu.Unlock()

	oldFP := ""
	if len(old) > 0 {
		oldFP = auth.DeviceKeyFingerprint(old)
	}
	// The device's record of it: which key it was, which it is, who said so.
	h.logger.InfoContext(r.Context(), "device approval key re-linked",
		slog.String("tenant", id.Tenant), slog.String("subject", id.Subject), slog.String("session", sessionID),
		slog.String("device_key", auth.DeviceKeyFingerprint(deviceKey)),
		slog.String("old_approval_key", oldFP), slog.String("new_approval_key", auth.DeviceKeyFingerprint(key)))
	h.clearApproveFails(id.Subject)
	h.relinkNotice(r.Context(), id.Tenant, id.Subject, name)
	h.throughTafs(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "approved", "user_code": userCode})
}

// relinkNotice tells the person, as for a change to who can approve on
// their account, that one of their phones approves again with a new key.
// Best-effort: the re-link stands whether or not the email goes.
func (h *DeviceAuthHandler) relinkNotice(ctx context.Context, tenantID, subject, name string) {
	if h.emails == nil || h.notices == nil {
		return
	}
	to, err := h.emails.EmailForSubject(ctx, subject)
	if err != nil || strings.TrimSpace(to) == "" {
		return
	}
	// The browser that approved it sends no app language: the one the
	// person's app last showed them.
	lang := words.EN
	if h.tenants != nil {
		if t, err := h.tenants.Get(ctx, tenantID); err == nil {
			lang = personlang.Of(t, subject)
		}
	}
	w := words.OfNotice(lang)
	phone := strings.TrimSpace(name)
	if phone == "" {
		phone = w.YourPhone
	}
	n := notify.AccountNotice{Subject: w.RelinkSubject(phone), Body: w.RelinkBody(phone), Lang: lang}
	if err := h.notices.AccountNotice(context.WithoutCancel(ctx), to, n); err != nil {
		h.logger.WarnContext(ctx, "device relink: notice failed", slog.String("err", err.Error()))
	}
}
