package handlers

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
)

// DeviceVerifyHandler serves POST /v1/devices/verify: a service that
// takes a person's approval on their device, planque first (kiff/planque#10),
// asks KIFF whether that device signed it and whether its person may give
// it. KIFF keeps the devices, so planque keeps none.
//
// It answers which key signed (kiff/tafs#494): `key` is device (the
// device's own key) or critical (the approval key of one of the device's
// sessions, behind the person's fingerprint). A service whose route is
// routine passes `window`, the window id the phone put in the signed bytes:
// KIFF checks the id is a line of the message and answers `window: {id,
// open, expires}`, restarting the window when it is open. A critical route
// takes only `key: critical`.
type DeviceVerifyHandler struct {
	devices  auth.DeviceStore
	members  auth.Memberships
	sessions auth.DeveloperSessionStore
	windows  auth.WindowStore
	// passkeys finds the computer's own passkey that opened a window, for a
	// passkey session (auth.WindowOpenedByApprover).
	passkeys auth.PasskeyStore
	// checks keeps KIFF's record of a connection's approval verified inside
	// an open window, for Setup's read Card later (auth.ApprovalCheck).
	checks auth.ApprovalCheckStore
	now    func() time.Time
}

// NewDeviceVerifyHandler checks signatures against devices, and the
// person's role against members.
func NewDeviceVerifyHandler(devices auth.DeviceStore, members auth.Memberships) *DeviceVerifyHandler {
	return &DeviceVerifyHandler{devices: devices, members: members, now: time.Now}
}

// WithSessions lets the check find the approval keys of the device's
// sessions, so a signature by one answers `key: critical`.
func (h *DeviceVerifyHandler) WithSessions(sessions auth.DeveloperSessionStore) *DeviceVerifyHandler {
	h.sessions = sessions
	return h
}

// WithPasskeys lets a window opened by a computer's own passkey count, for
// a passkey session, while that passkey is still registered to the device.
func (h *DeviceVerifyHandler) WithPasskeys(passkeys auth.PasskeyStore) *DeviceVerifyHandler {
	h.passkeys = passkeys
	return h
}

// WithWindows reads approval windows from windows (kiff/tafs#494).
func (h *DeviceVerifyHandler) WithWindows(windows auth.WindowStore) *DeviceVerifyHandler {
	h.windows = windows
	return h
}

// WithApprovalChecks records each connection's approval it verifies inside
// an open window, so Setup's read Card may be issued on it once, within
// auth.ApprovalCheckLife (kiff/tafs window_required).
func (h *DeviceVerifyHandler) WithApprovalChecks(checks auth.ApprovalCheckStore) *DeviceVerifyHandler {
	h.checks = checks
	return h
}

// Register attaches the route to the authenticated mux.
func (h *DeviceVerifyHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/devices/verify", h.verify)
}

// The capabilities a service may ask about: what a person approves with a
// device outside KIFF. planque connects a vendor (tools.connect), exports
// the account's grants (account.export), and erases what it keeps when the
// account is deleted (account.delete, kiff/tafs#185), each only for those
// whose role may do it in KIFF too.
var verifiable = map[membership.Capability]bool{
	membership.CapToolsConnect: true, membership.CapHoldsAnswer: true,
	membership.CapExport: true, membership.CapDelete: true,
}

func (h *DeviceVerifyHandler) verify(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || !id.IsConnector() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "connector_key_required",
			"detail": "a service's connector key verifies a device's signature"})
		return
	}
	var req struct {
		Device     string `json:"device_id"`
		Message    string `json:"message"`   // base64 of the exact bytes signed
		Signature  string `json:"signature"` // base64 DER ECDSA over the message's SHA-256
		Capability string `json:"capability"`
		// Window is the approval window the phone signed in, as its own line
		// of the signed message (kiff/tafs#494); "" outside one.
		Window string `json:"window"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	message, err := base64.StdEncoding.DecodeString(req.Message)
	if err != nil || len(message) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "message is the base64 of the bytes signed"})
		return
	}
	// KIFF's own messages are KIFF's to verify, where they're used: a service
	// signs its own (planque.request.v1), so one can't pass for the other.
	// One signature over two requests (kiff.requests.v1) names both requests'
	// hashes, so the other service checks it for its own request: the one
	// kiff.* message a service may bring.
	if strings.HasPrefix(string(message), "kiff.") && !strings.HasPrefix(string(message), "kiff.requests.v1\n") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "a kiff.* message is verified only by KIFF, where it's used"})
		return
	}
	capability := membership.Capability(req.Capability)
	if !verifiable[capability] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "capability is tools.connect, holds.answer, account.export or account.delete"})
		return
	}
	no := func(why string) {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false, "reason": why})
	}
	now := h.now()
	d, m, key, reason, err := h.checkDevice(r.Context(), id.Tenant, req.Device, message, req.Signature, capability, now)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	if reason != "" {
		no(reason)
		return
	}
	// The device's name, so a service shows the owner "tafs on iPad", not an id.
	out := map[string]any{"valid": true, "key": key, "device_id": d.ID, "name": d.Name, "subject": d.Subject,
		"platform": d.Platform, "role": string(m.Role), "attested": d.Attestation != ""}
	if req.Window != "" {
		// The window counts only as the signed bytes name it, as a line of
		// their own: a window passed beside the signature is nobody's word.
		if !strings.Contains("\n"+string(message)+"\n", "\n"+req.Window+"\n") {
			no("window_not_signed")
			return
		}
		window := map[string]any{"id": req.Window, "open": false}
		if h.windows != nil && key == auth.KeyDevice {
			open, werr := h.windows.OpenFor(r.Context(), d.ID, now)
			if werr != nil && !errors.Is(werr, auth.ErrWindowNotFound) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "window_check_unavailable"})
				return
			}
			if werr == nil && open.ID == req.Window && open.Tenant == d.Tenant && open.Subject == d.Subject && h.openedByCurrentKey(r.Context(), open, now) {
				_ = h.windows.Restart(r.Context(), open.ID, open.Restarted(now))
				window["open"], window["expires"] = true, open.Restarted(now).UTC().Format(time.RFC3339)
				h.recordApproval(r.Context(), id.Tenant, d.ID, open.ID, message, req.Signature, capability, now)
			}
		}
		out["window"] = window
	}
	writeJSON(w, http.StatusOK, out)
}

// recordApproval keeps KIFF's record that it verified a connection's
// approval inside window, open now: only after the device's signature
// checked and KIFF found the window open itself, only for tools.connect and
// a planque.request.v2 approve of a grant on tenant. A record that fails to
// be written leaves the answer as it is: Setup's read Card is then refused
// later, as before.
func (h *DeviceVerifyHandler) recordApproval(ctx context.Context, tenant, device, window string, message []byte, sig string,
	capability membership.Capability, now time.Time) {
	if h.checks == nil || capability != membership.CapToolsConnect {
		return
	}
	// The window is the message's own window line, never only somewhere in it.
	if _, named, ok := planqueLines(string(message)); !ok || named != window {
		return
	}
	grant := windowedApproveGrant(string(message), tenant)
	if grant == "" {
		return
	}
	if err := h.checks.Record(ctx, auth.ApprovalCheck{ID: auth.ApprovalCheckID(message, sig), Tenant: tenant, DeviceID: device,
		Grant: grant, Window: window, CheckedAt: now}); err != nil {
		slog.WarnContext(ctx, "approval check not recorded", slog.String("tenant", tenant), slog.String("grant", grant), slog.String("err", err.Error()))
	}
}

// openedByCurrentKey is windowOpenedByCurrentKey with the handler's sessions.
func (h *DeviceVerifyHandler) openedByCurrentKey(ctx context.Context, open auth.ApprovalWindow, now time.Time) bool {
	return windowOpenedByCurrentKey(ctx, h.sessions, h.passkeys, open, now)
}

// checkDevice is the package's checkDevice, then, when the device's own key
// didn't sign, the approval keys of the device's active sessions: the key
// that did is KeyCritical.
func (h *DeviceVerifyHandler) checkDevice(ctx context.Context, tenant, deviceID string, message []byte, sig string,
	capability membership.Capability, now time.Time) (auth.ApprovalDevice, membership.Member, string, string, error) {
	d, m, reason, err := checkDevice(ctx, h.devices, h.members, tenant, deviceID, message, sig, capability)
	if err != nil || reason != "signature_invalid" || h.sessions == nil {
		return d, m, auth.KeyDevice, reason, err
	}
	// The device is known and the person is theirs to find: which of its
	// sessions' approval keys signed?
	dev, derr := h.devices.Get(ctx, deviceID)
	if derr != nil {
		return d, m, auth.KeyDevice, reason, nil
	}
	sessions, serr := h.sessions.ListActiveForSubject(ctx, dev.Subject, now)
	if serr != nil {
		return auth.ApprovalDevice{}, membership.Member{}, "", "", errors.New("device_check_unavailable")
	}
	for _, s := range sessions {
		if s.DeviceID != dev.ID || len(s.ApprovalPublicKey) == 0 || !verifySignature(s.ApprovalPublicKey, message, sig) {
			continue
		}
		// The device's key is known to have signed nothing here; check the
		// person as checkDevice would.
		d, m, reason, err = checkDeviceWithKey(ctx, h.devices, h.members, tenant, deviceID, s.ApprovalPublicKey, message, sig, capability)
		return d, m, auth.KeyCritical, reason, err
	}
	return d, m, auth.KeyDevice, reason, nil
}

// checkDevice says whether the device deviceID of tenant signed message
// (sig, base64 DER ECDSA over its SHA-256), and whether its person is an
// active member who may give capability. A refusal is a reason (unknown_device,
// signature_invalid, not_a_member, not_allowed); err is a store that couldn't
// be read, named as the error to answer with.
func checkDevice(ctx context.Context, devices auth.DeviceStore, members auth.Memberships, tenant, deviceID string,
	message []byte, sig string, capability membership.Capability) (auth.ApprovalDevice, membership.Member, string, error) {
	return checkDeviceWithKey(ctx, devices, members, tenant, deviceID, nil, message, sig, capability)
}

// checkDeviceWithKey is checkDevice with the signature checked against key
// (SPKI DER) instead of the device's own, when key is given: a session's
// approval key (kiff/tafs#494).
func checkDeviceWithKey(ctx context.Context, devices auth.DeviceStore, members auth.Memberships, tenant, deviceID string, key,
	message []byte, sig string, capability membership.Capability) (auth.ApprovalDevice, membership.Member, string, error) {
	d, err := devices.Get(ctx, deviceID)
	if errors.Is(err, auth.ErrDeviceNotFound) || (err == nil && (d.Tenant != tenant || d.IsRevoked())) {
		// Another account's device reads as unknown.
		return auth.ApprovalDevice{}, membership.Member{}, "unknown_device", nil
	}
	if err != nil {
		return auth.ApprovalDevice{}, membership.Member{}, "", errors.New("device_check_unavailable")
	}
	if key == nil {
		key = d.PublicKey
	}
	if !verifySignature(key, message, sig) {
		return auth.ApprovalDevice{}, membership.Member{}, "signature_invalid", nil
	}
	if members == nil {
		return auth.ApprovalDevice{}, membership.Member{}, "", errors.New("membership_check_unavailable")
	}
	m, err := members.Latest(ctx, d.Tenant, d.Subject)
	if errors.Is(err, membership.ErrNotMember) || (err == nil && !m.Active()) {
		return auth.ApprovalDevice{}, membership.Member{}, "not_a_member", nil
	}
	if err != nil {
		return auth.ApprovalDevice{}, membership.Member{}, "", errors.New("membership_check_unavailable")
	}
	if !membership.Can(m.Role, capability) {
		return auth.ApprovalDevice{}, membership.Member{}, "not_allowed", nil
	}
	return d, m, "", nil
}

// verifySignature checks a base64 DER ECDSA P-256 signature over message's
// SHA-256 with key (SPKI DER), as KIFF verifies a phone's own messages.
func verifySignature(keyDER, message []byte, sigB64 string) bool {
	pub, err := x509.ParsePKIXPublicKey(keyDER)
	if err != nil {
		return false
	}
	k, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil || len(sig) == 0 {
		return false
	}
	digest := sha256.Sum256(message)
	return ecdsa.VerifyASN1(k, digest[:], sig)
}
