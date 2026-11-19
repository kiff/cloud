package handlers

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/profile"
)

// ProfilesHandler serves connection profiles (the connection-profiles design Appendix A): the
// words KIFF renders for a profile, and the check that the owner's
// approval covers a profile and those words, which the gateway asks
// before it applies one.
type ProfilesHandler struct {
	approvals planqueApproval
	clock     func() time.Time
}

// NewProfilesHandler checks approvals against devices and members.
func NewProfilesHandler(devices auth.DeviceStore, members auth.Memberships) *ProfilesHandler {
	return &ProfilesHandler{approvals: planqueApproval{devices: devices, members: members}, clock: time.Now}
}

// WithWindows lets an approval signed inside an approval window count
// (kiff/tafs#494): sessions find the key that opened it, windows whether
// it's open. Without them, only a v1 approval counts.
func (h *ProfilesHandler) WithWindows(sessions auth.DeveloperSessionStore, windows auth.WindowStore) *ProfilesHandler {
	h.approvals.sessions, h.approvals.windows = sessions, windows
	return h
}

// WithPasskeys lets a window opened by a computer's own passkey count, for
// a passkey session, while that passkey is still registered to the device.
func (h *ProfilesHandler) WithPasskeys(passkeys auth.PasskeyStore) *ProfilesHandler {
	h.approvals.passkeys = passkeys
	return h
}

// Register attaches the routes to the authenticated mux.
func (h *ProfilesHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/profiles/words", h.words)
	mux.HandleFunc("POST /v1/me/profile-approvals", h.approval)
}

// ApprovalMaxAge is how long after the owner signed an approval it may
// still apply its profile.
const ApprovalMaxAge = time.Hour

type wordsAnswer struct {
	ProfileSHA256 string        `json:"profile_sha256"`
	Words         profile.Words `json:"words"`
	WordsSHA256   string        `json:"words_sha256"`
}

// render parses raw and answers its words, in kiff.words.v1, and both digests.
func render(raw []byte) (wordsAnswer, error) { return renderVersion(raw, profile.WordsVersion) }

// renderVersion is render in a words version, one of profile.WordsVersions.
func renderVersion(raw []byte, version string) (wordsAnswer, error) {
	p, err := profile.Parse(raw)
	if err != nil {
		return wordsAnswer{}, err
	}
	words, ok := profile.RenderVersion(p, version)
	if !ok {
		return wordsAnswer{}, fmt.Errorf("unknown words version %q", version)
	}
	a := wordsAnswer{Words: words}
	if a.ProfileSHA256, err = profile.Digest(raw); err != nil {
		return wordsAnswer{}, err
	}
	if a.WordsSHA256, err = profile.WordsDigest(a.Words); err != nil {
		return wordsAnswer{}, err
	}
	return a, nil
}

func (h *ProfilesHandler) words(w http.ResponseWriter, r *http.Request) {
	if _, err := auth.IdentityFrom(r.Context()); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, profile.MaxBytes))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "invalid_profile", "detail": "a profile is at most 256 KB"})
		return
	}
	// ?v=2 answers kiff.words.v2, the words without KIFF's bookkeeping; with
	// no v, kiff.words.v1, as every app read them before.
	version := profile.WordsVersion
	switch r.URL.Query().Get("v") {
	case "", "1":
	case "2":
		version = profile.WordsVersion2
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "v is 1 or 2"})
		return
	}
	a, err := renderVersion(raw, version)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_profile", "detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// planque's two routes whose signature applies a profile: approving a
// grant's tools, and giving a key-based grant its key with its tools, POST
// /v1/accounts/{tenant}/grants/{id}/approve or /key.
var approveURI = regexp.MustCompile(`^/v1/accounts/([^/?]+)/grants/[^/?]+/(approve|key)$`)

func (h *ProfilesHandler) approval(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || !id.IsConnector() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "connector_key_required",
			"detail": "the gateway checks an approval with the connector key that applies the profile"})
		return
	}
	var req struct {
		Profile  json.RawMessage `json:"profile"`
		Approval struct {
			Message   string `json:"message"`
			Body      string `json:"body"`
			Signature string `json:"signature"`
			Device    string `json:"device_id"`
			// Request is base64 of the approve request's own message when
			// message is a kiff.requests.v1 one, signed over it and the Card.
			Request string `json:"request"`
		} `json:"approval"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, profile.MaxBytes+64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	a, err := render(req.Profile)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_profile", "detail": err.Error()})
		return
	}
	no := func(why string) {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false, "reason": why})
	}
	message, err1 := base64.StdEncoding.DecodeString(req.Approval.Message)
	body, err2 := base64.StdEncoding.DecodeString(req.Approval.Body)
	request, err3 := base64.StdEncoding.DecodeString(req.Approval.Request)
	if err1 != nil || err2 != nil || err3 != nil || len(message) == 0 || len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "approval.message, approval.body and approval.request are base64 of the bytes signed, sent and paired"})
		return
	}
	// The request the approval stands for: the message signed alone, or the
	// request a kiff.requests.v1 message names (planqueApproval).
	approved, _, why := approvedRequest(string(message), string(request), id.Tenant)
	if why != "" {
		no(why)
		return
	}
	// planque.request.v1 or v2: base, tenant, method, uri, sha256(body),
	// signed_at, and in v2 the approval window's id (planqueApproval).
	lines, _, ok := planqueLines(approved)
	if !ok {
		no("not_an_approval")
		return
	}
	m := approveURI.FindStringSubmatch(lines[4])
	if lines[3] != http.MethodPost || m == nil || m[1] != id.Tenant || lines[2] != id.Tenant {
		no("not_an_approval")
		return
	}
	sum := sha256.Sum256(body)
	if lines[5] != hex.EncodeToString(sum[:]) {
		no("body_mismatch")
		return
	}
	at, err := time.Parse(time.RFC3339, lines[6])
	if now := h.clock(); err != nil || at.After(now.Add(5*time.Minute)) || now.Sub(at) > ApprovalMaxAge {
		no("approval_expired")
		return
	}
	var signed struct {
		KiffProfile struct {
			ProfileSHA256 string `json:"profile_sha256"`
			WordsSHA256   string `json:"words_sha256"`
		} `json:"kiff_profile"`
	}
	if json.Unmarshal(body, &signed) != nil || signed.KiffProfile.ProfileSHA256 != a.ProfileSHA256 {
		no("profile_not_approved")
		return
	}
	// The owner approved the words their app showed, in whichever version
	// it read: each one KIFF answers is checked, v1 first.
	if signed.KiffProfile.WordsSHA256 != a.WordsSHA256 {
		shown := false
		for _, v := range profile.WordsVersions[1:] {
			if b, err := renderVersion(req.Profile, v); err == nil && b.WordsSHA256 == signed.KiffProfile.WordsSHA256 {
				a, shown = b, true
				break
			}
		}
		if !shown {
			no("words_not_approved")
			return
		}
	}
	_, d, _, reason, err := h.approvals.approved(r.Context(), id.Tenant, req.Approval.Device, string(message), string(request), req.Approval.Signature, membership.CapToolsConnect, h.clock())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	if reason != "" {
		no(reason)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "profile_sha256": a.ProfileSHA256,
		"words_sha256": a.WordsSHA256, "device_id": d.ID, "signed_at": at.UTC().Format(time.RFC3339)})
}
