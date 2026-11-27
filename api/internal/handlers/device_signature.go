package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
)

// DeviceSignatureHandler serves POST /v1/me/device-signature (kiff/tafs#206):
// another KIFF service that takes a person's act only when their phone signed
// it, the gateway first (connecting the account's control server), asks
// whether the caller's own phone signed that exact request. The caller is the
// phone's session, forwarded by the service; the request is signed as the
// phone signs a setup call to KIFF (kiff.request.v1: tenant, method, path,
// sha256 of the body, signed_at), and each is accepted once.
//
// The route's capability rule says what the person must be able to do
// (tools.connect): a phone's signature never adds to what its person may do.
// On acceptance it returns a device grant (auth.DeviceGrantRoutes): the
// service then saves the account's contract and permit targets with the
// phone's session, each once, within a minute.
//
// One signature over two requests (kiff.requests.v1, a v3 header) is checked
// against the approval key only, as on KIFF's own routes: the gateway's batch
// for Coach's first start takes it with Coach's agent draft and its Cards, one
// fingerprint for both.
//
// It answers which key signed (kiff/tafs#494): `key` is device or critical,
// and a signature made inside an approval window (a v2 header, the window's
// id in the signed message) comes with `window: {id, open, expires}`. The
// service refuses a kind short of its route's: a routine route takes the
// critical key, or the device key with its window open; a critical route
// only the critical key. KIFF restarts an open window on the check, as it
// does on its own routine routes.
type DeviceSignatureHandler struct {
	made    auth.SignedRequests
	windows auth.WindowStore
	// passkeys and rp check a computer's own passkey's assertion, sent in
	// place of the signature by a session that approves with one.
	passkeys auth.PasskeyStore
	rp       auth.RelyingParty
	now      func() time.Time
	// logger gets one line per answer (logAnswer): never a signature, an
	// assertion, a body or the grant.
	logger *slog.Logger
}

// NewDeviceSignatureHandler refuses a signed request already accepted, by
// made; nil keeps them in memory.
func NewDeviceSignatureHandler(made auth.SignedRequests) *DeviceSignatureHandler {
	if made == nil {
		made = auth.NewMemorySignedRequests()
	}
	return &DeviceSignatureHandler{made: made, logger: slog.Default()}
}

// WithLogger substitutes the logger each answer is logged to.
func (h *DeviceSignatureHandler) WithLogger(l *slog.Logger) *DeviceSignatureHandler {
	if l != nil {
		h.logger = l
	}
	return h
}

// WithWindows reads approval windows from windows (kiff/tafs#494). Without
// one, a signature made inside a window answers its window as not open.
func (h *DeviceSignatureHandler) WithWindows(windows auth.WindowStore) *DeviceSignatureHandler {
	h.windows = windows
	return h
}

// WithPasskeys checks the assertions of sessions that approve with their
// device's own passkey (the desktop client contract, §2.6): the request's
// `passkey`, a KIFF-Passkey-Assertion value, in place of `signature`.
func (h *DeviceSignatureHandler) WithPasskeys(store auth.PasskeyStore, rp auth.RelyingParty) *DeviceSignatureHandler {
	h.passkeys, h.rp = store, rp
	return h
}

// Register attaches the route to the authenticated mux.
func (h *DeviceSignatureHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/me/device-signature", h.check)
}

var (
	sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// The requests another service takes signed: a write to one of its /v1/ routes.
	signedMethod = map[string]bool{http.MethodPost: true, http.MethodPut: true, http.MethodDelete: true}
)

func (h *DeviceSignatureHandler) check(w http.ResponseWriter, r *http.Request) {
	// Each answer is logged (the Mac's refused approval, 15 Nov: two 403s here
	// left no line to read their reason from).
	rec := &answerRecorder{ResponseWriter: w}
	w = rec
	defer h.logAnswer(r, rec)
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || len(id.DeviceKey) == 0 || id.AgentID != "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "device_session_required",
			"detail": "only a phone's own session asks whether that phone signed a request"})
		return
	}
	// A session that approves nothing never answers for another service
	// either (§2.6): a view device's signature grants nothing anywhere.
	if id.ViewOnly() {
		writeJSON(w, http.StatusForbidden, auth.ViewOnlyRefusal())
		return
	}
	var req struct {
		Method     string `json:"method"`
		URI        string `json:"uri"`         // the path and query, as sent to the service
		BodySHA256 string `json:"body_sha256"` // lowercase hex
		Signature  string `json:"signature"`   // the KIFF-Device-Signature header, as sent
		// Passkey is the KIFF-Passkey-Assertion header, as sent, from a session
		// that approves with its device's own passkey, in place of Signature.
		Passkey string `json:"passkey"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || !signedMethod[req.Method] || !strings.HasPrefix(req.URI, "/v1/") ||
		!sha256Hex.MatchString(req.BodySHA256) || (req.Signature == "") == (req.Passkey == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request",
			"detail": "method (POST, PUT or DELETE), uri (/v1/…), body_sha256 (lowercase hex), and signature or passkey (one of them) are required"})
		return
	}
	now := time.Now()
	if h.now != nil {
		now = h.now()
	}
	if req.Passkey != "" {
		h.checkPasskey(w, r, id, req.Method, req.URI, req.BodySHA256, req.Passkey, now)
		return
	}
	// The device key first, then the approval key: which one signed is the
	// answer's `key`.
	key := auth.KeyDevice
	var replayID []byte
	var named string
	if auth.SignedTogether(req.Signature) {
		// One signature over two requests (kiff.requests.v1) is the approval
		// key's alone, as on KIFF's own routes (SignedWith): it carries another
		// request too, so a window's tap never makes one.
		key = auth.KeyCritical
		replayID, named, err = auth.VerifyRequestDigestWindow(id.ApprovalKey, id.Tenant, req.Method, req.URI, req.BodySHA256, req.Signature, now)
	} else {
		replayID, named, err = auth.VerifyRequestDigestWindow(id.DeviceKey, id.Tenant, req.Method, req.URI, req.BodySHA256, req.Signature, now)
		if errors.Is(err, auth.ErrRequestSignature) && len(id.ApprovalKey) > 0 {
			key = auth.KeyCritical
			replayID, named, err = auth.VerifyRequestDigestWindow(id.ApprovalKey, id.Tenant, req.Method, req.URI, req.BodySHA256, req.Signature, now)
		}
	}
	switch {
	case errors.Is(err, auth.ErrAnswerSignedAt):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "signed_at_out_of_range",
			"detail": "the signature's time must be RFC 3339, within 5 minutes of KIFF's clock"})
		return
	case err != nil:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "signature_invalid",
			"detail": "this phone didn't sign that request"})
		return
	}
	// The window the device key signed in, as the signed message names it:
	// open for this phone's session that opened it, or not. A critical
	// signature names no window.
	var window map[string]any
	if named != "" && key == auth.KeyDevice {
		window = map[string]any{"id": named, "open": false}
		if h.windows != nil {
			open, werr := h.windows.OpenFor(r.Context(), id.DeviceID, now)
			if werr != nil && !errors.Is(werr, auth.ErrWindowNotFound) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "signature_check_unavailable"})
				return
			}
			if werr == nil && open.ID == named && open.Tenant == id.Tenant && open.Subject == id.Subject && auth.WindowOpenedByApprover(r.Context(), h.passkeys, open, id.DeviceSessionID, id.ApprovalKey, id.ApprovalMode) {
				window["open"], window["expires"] = true, open.Restarted(now).UTC().Format(time.RFC3339)
			}
		}
	}
	h.accept(w, r, id, replayID, key, window, now)
}

// accept records the request's use, once, and answers with the device grant
// for the service's writes that follow.
func (h *DeviceSignatureHandler) accept(w http.ResponseWriter, r *http.Request, id auth.Identity, replayID []byte, key string, window map[string]any, now time.Time) {
	fresh, err := h.made.Use(r.Context(), replayID, now, now.Add(2*auth.MaxAnswerSkew))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "signature_check_unavailable"})
		return
	}
	if !fresh {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "signature_replayed",
			"detail": "this signed request was already made; sign it again to repeat it"})
		return
	}
	// The writes the service then makes for this phone: once each, for a
	// minute (auth.DeviceGrantRoutes).
	grant, until, err := auth.IssueDeviceGrant(r.Context(), h.made, id, now)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "signature_check_unavailable"})
		return
	}
	out := map[string]any{"signed": true, "key": key, "grant": grant, "grant_until": until.UTC().Format(time.RFC3339)}
	if window != nil {
		out["window"] = window
	}
	writeJSON(w, http.StatusOK, out)
}

// checkPasskey checks a computer's own passkey's assertion over the
// service's request (auth.VerifyRequestPasskey: one request, or two with
// with=), for a session that approves with it. It answers `key: critical`,
// as the approval key's signature would, and never names a window.
func (h *DeviceSignatureHandler) checkPasskey(w http.ResponseWriter, r *http.Request, id auth.Identity, method, uri, bodySHA256, header string, now time.Time) {
	if id.ApprovalMode != auth.ApprovalModePasskey || h.passkeys == nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "passkey_not_this_session",
			"detail": "only a session that approves with its device's own passkey answers with one"})
		return
	}
	_, replayID, err := auth.VerifyRequestPasskey(r.Context(), h.passkeys, h.rp, id, method, uri, bodySHA256, header, now)
	switch {
	case errors.Is(err, auth.ErrAnswerSignedAt):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "signed_at_out_of_range",
			"detail": "the assertion's t= must be RFC 3339, within 5 minutes of KIFF's clock"})
		return
	case err != nil:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "signature_invalid",
			"detail": "this computer's passkey didn't approve that request"})
		return
	}
	h.accept(w, r, id, replayID, auth.KeyCritical, nil, now)
}

// answerRecorder keeps an answer's status and its first bytes, for its log line.
type answerRecorder struct {
	http.ResponseWriter
	status int
	head   []byte
}

func (a *answerRecorder) WriteHeader(code int) {
	if a.status == 0 {
		a.status = code
	}
	a.ResponseWriter.WriteHeader(code)
}

func (a *answerRecorder) Write(b []byte) (int, error) {
	if a.status == 0 {
		a.status = http.StatusOK
	}
	if room := 1024 - len(a.head); room > 0 {
		a.head = append(a.head, b[:min(room, len(b))]...)
	}
	return a.ResponseWriter.Write(b)
}

func (a *answerRecorder) Unwrap() http.ResponseWriter { return a.ResponseWriter }

// logAnswer writes one line per answer: its status, its error code or the
// key that signed, and how the asking session approves. Only those fields are
// read from the answer, so a grant is never logged, and nothing of the request.
func (h *DeviceSignatureHandler) logAnswer(r *http.Request, rec *answerRecorder) {
	if h.logger == nil {
		return
	}
	var answer struct {
		Error string `json:"error"`
		Key   string `json:"key"`
	}
	_ = json.Unmarshal(rec.head, &answer)
	attrs := []slog.Attr{slog.Int("status", rec.status)}
	if answer.Error != "" {
		attrs = append(attrs, slog.String("error", answer.Error))
	}
	if answer.Key != "" {
		attrs = append(attrs, slog.String("key", answer.Key))
	}
	if id, err := auth.IdentityFrom(r.Context()); err == nil {
		attrs = append(attrs, slog.String("tenant", id.Tenant), slog.String("device_session", id.DeviceSessionID),
			slog.String("approval_mode", id.ShownApprovalMode()), slog.Bool("view_only", id.ViewOnly()))
	}
	level := slog.LevelInfo
	if rec.status >= 400 {
		level = slog.LevelWarn
	}
	h.logger.LogAttrs(r.Context(), level, "device signature", attrs...)
}
