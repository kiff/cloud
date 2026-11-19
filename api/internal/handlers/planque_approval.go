package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
)

// planqueApproval is the one check KIFF makes of an owner's approval that
// planque forwards (kiff/tafs#494): the profile it applies, and Setup's read
// Card from a connection's approval. Both routes call it, so the two can't
// drift.
//
// planque signs its request in one of two forms (kiff/planque
// internal/device.Message):
//   - planque.request.v1, seven lines, signed by the device's own key, as
//     before approval windows;
//   - planque.request.v2, the same seven lines and the approval window's id
//     as an eighth, signed by the same device key inside a window: it counts
//     only while that window is open for this device, its person and
//     account, and was opened by the approval key the window's session
//     holds now (a new enrolment or re-link ends it).
type planqueApproval struct {
	devices  auth.DeviceStore
	members  auth.Memberships
	sessions auth.DeveloperSessionStore
	windows  auth.WindowStore
	// passkeys finds a computer's own passkey that opened a window, for a
	// passkey session; nil, only an approval key's window counts.
	passkeys auth.PasskeyStore
}

// planqueLines splits a planque signed request into its seven common lines
// (kind, base URL, account, method, path, body SHA-256, signed at) and the
// window's id, "" for v1. ok is false for anything else.
func planqueLines(message string) (lines []string, window string, ok bool) {
	all := strings.Split(message, "\n")
	switch {
	case len(all) == 7 && all[0] == "planque.request.v1":
		return all, "", true
	case len(all) == 8 && all[0] == "planque.request.v2" && all[7] != "":
		return all[:7], all[7], true
	}
	return nil, "", false
}

// windowedApproveGrant is the grant a planque.request.v2 message approves on
// tenant (POST /v1/accounts/{tenant}/grants/{grant}/approve or …/key, signed
// inside an approval window), or "" for any other message. Only such an
// approval is recorded for Setup's read Card (auth.ApprovalCheck).
func windowedApproveGrant(message, tenant string) string {
	lines, window, ok := planqueLines(message)
	if !ok || window == "" || lines[2] != tenant || lines[3] != "POST" {
		return ""
	}
	prefix := "/v1/accounts/" + tenant + "/grants/"
	if !strings.HasPrefix(lines[4], prefix) {
		return ""
	}
	rest := strings.TrimPrefix(lines[4], prefix)
	grant, step, found := strings.Cut(rest, "/")
	if !found || (step != "approve" && step != "key") || !validGrantID(grant) {
		return ""
	}
	return grant
}

// pairedLines splits a kiff.requests.v1 message: one signature by the
// approval key over two requests (kiff/tafs/docs/secure-connection.md), as
// kind, account, signed at, and the two requests' SHA-256, lower first.
func pairedLines(message string) ([]string, bool) {
	all := strings.Split(message, "\n")
	if len(all) == 5 && all[0] == "kiff.requests.v1" {
		return all, true
	}
	return nil, false
}

// approvedRequest is the planque request an approval stands for: the
// message itself when it was signed alone, or, for a kiff.requests.v1
// message, the request given beside it, which the message names by its
// SHA-256 for the same account and time (planque sends its own message as
// approval.request). paired says which; why is a refusal ("" when it fits).
func approvedRequest(message, request, tenant string) (req string, paired bool, why string) {
	lines, ok := pairedLines(message)
	if !ok {
		if request != "" {
			return "", false, "request_without_pairing"
		}
		return message, false, ""
	}
	if request == "" {
		return "", true, "request_missing"
	}
	sum := sha256.Sum256([]byte(request))
	own := hex.EncodeToString(sum[:])
	if lines[1] != tenant || lines[3] == lines[4] || (lines[3] != own && lines[4] != own) {
		return "", true, "request_not_named"
	}
	// The approval key signs outside any window, so the request is a v1, at the same time as the pairing.
	planque, window, ok := planqueLines(request)
	if !ok || window != "" || planque[6] != lines[2] {
		return "", true, "not_an_approval"
	}
	return request, true, ""
}

// approved checks an approval planque forwards, signed alone or paired: it
// answers the planque request it stands for, once the signature checks. A
// paired message counts only from one of the device's sessions' approval
// keys, behind the fingerprint (never the device key or a window); one
// signed alone is check's. A refusal is a reason; err is a store that
// couldn't be read.
func (a planqueApproval) approved(ctx context.Context, tenant, deviceID, message, request, sig string,
	capability membership.Capability, now time.Time) (string, auth.ApprovalDevice, membership.Member, string, error) {
	req, paired, why := approvedRequest(message, request, tenant)
	if why != "" {
		return "", auth.ApprovalDevice{}, membership.Member{}, why, nil
	}
	var d auth.ApprovalDevice
	var m membership.Member
	var reason string
	var err error
	if paired {
		d, m, reason, err = a.checkApprovalKey(ctx, tenant, deviceID, []byte(message), sig, capability, now)
	} else {
		d, m, reason, err = a.check(ctx, tenant, deviceID, []byte(message), sig, capability, now)
	}
	if err != nil || reason != "" {
		return "", d, m, reason, err
	}
	return req, d, m, "", nil
}

// checkApprovalKey says whether one of deviceID's active sessions' approval
// keys signed message, and its person may give capability. Without the
// session store the approval key can't be found, so it doesn't count.
func (a planqueApproval) checkApprovalKey(ctx context.Context, tenant, deviceID string, message []byte, sig string,
	capability membership.Capability, now time.Time) (auth.ApprovalDevice, membership.Member, string, error) {
	if a.sessions == nil {
		return auth.ApprovalDevice{}, membership.Member{}, "approval_key_required", nil
	}
	dev, err := a.devices.Get(ctx, deviceID)
	if errors.Is(err, auth.ErrDeviceNotFound) || (err == nil && (dev.Tenant != tenant || dev.IsRevoked())) {
		return auth.ApprovalDevice{}, membership.Member{}, "unknown_device", nil
	}
	if err != nil {
		return auth.ApprovalDevice{}, membership.Member{}, "", errors.New("device_check_unavailable")
	}
	sessions, err := a.sessions.ListActiveForSubject(ctx, dev.Subject, now)
	if err != nil {
		return auth.ApprovalDevice{}, membership.Member{}, "", errors.New("device_check_unavailable")
	}
	for _, s := range sessions {
		if s.DeviceID != dev.ID || len(s.ApprovalPublicKey) == 0 || !verifySignature(s.ApprovalPublicKey, message, sig) {
			continue
		}
		return checkDeviceWithKey(ctx, a.devices, a.members, tenant, deviceID, s.ApprovalPublicKey, message, sig, capability)
	}
	return auth.ApprovalDevice{}, membership.Member{}, "approval_key_required", nil
}

// check says whether deviceID of tenant signed message (sig) and may give
// capability, as checkDevice does, and, for a v2 message, whether the
// window it names is open as above. A refusal is a reason; err is a store
// that couldn't be read.
func (a planqueApproval) check(ctx context.Context, tenant, deviceID string, message []byte, sig string,
	capability membership.Capability, now time.Time) (auth.ApprovalDevice, membership.Member, string, error) {
	_, window, ok := planqueLines(string(message))
	if !ok {
		return auth.ApprovalDevice{}, membership.Member{}, "not_an_approval", nil
	}
	d, m, reason, err := checkDevice(ctx, a.devices, a.members, tenant, deviceID, message, sig, capability)
	if err != nil || reason != "" || window == "" {
		return d, m, reason, err
	}
	if a.windows == nil || a.sessions == nil {
		// Without the stores a window can't be checked, so it doesn't count.
		return auth.ApprovalDevice{}, membership.Member{}, "window_required", nil
	}
	open, err := a.windows.OpenFor(ctx, d.ID, now)
	if errors.Is(err, auth.ErrWindowNotFound) {
		return auth.ApprovalDevice{}, membership.Member{}, "window_required", nil
	}
	if err != nil {
		return auth.ApprovalDevice{}, membership.Member{}, "", errors.New("window_check_unavailable")
	}
	if open.ID != window || open.Tenant != d.Tenant || open.Subject != d.Subject || !windowOpenedByCurrentKey(ctx, a.sessions, a.passkeys, open, now) {
		return auth.ApprovalDevice{}, membership.Member{}, "window_required", nil
	}
	return d, m, "", nil
}

// windowOpenedByCurrentKey reports whether the session that opened the
// window is still active on the window's device, and what opened it is still
// that session's approval authority (auth.WindowOpenedByApprover): its
// approval key, or for a passkey session its device's own passkey, still
// registered and not synced. The window is that session's only: once it
// ends, another session on the same device gets nothing from it. A new
// enrolment, a re-link, signing out or removing the passkey ends a window.
// Without a session store, it can't be checked, so the window doesn't count.
func windowOpenedByCurrentKey(ctx context.Context, sessions auth.DeveloperSessionStore, passkeys auth.PasskeyStore, open auth.ApprovalWindow, now time.Time) bool {
	if sessions == nil {
		return false
	}
	active, err := sessions.ListActiveForSubject(ctx, open.Subject, now)
	if err != nil {
		return false
	}
	for _, s := range active {
		if s.ID == open.SessionID {
			if s.DeviceID != open.DeviceID {
				return false
			}
			return auth.WindowOpenedByApprover(ctx, passkeys, open, s.ID, s.ApprovalPublicKey, s.ApprovalMode)
		}
	}
	return false
}
