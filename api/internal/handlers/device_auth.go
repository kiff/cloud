package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/notify"
	"github.com/kiff/cloud/api/internal/personlang"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/words"
	"github.com/oklog/ulid/v2"
)

// DeviceAuthHandler implements the OAuth 2.0 Device Authorization
// Grant (RFC 8628) shape that backs `kiff auth login`:
//
//	POST /v1/auth/device/start     (unauthenticated) begin a flow
//	POST /v1/auth/device/token     (unauthenticated) poll for the token
//	POST /v1/auth/device/approve   (authenticated)   approve in the browser
//
// The CLI can't host a redirect, so the human approves in a browser
// where our identity provider already lives: `start` returns a short
// user_code the CLI prints and a verification URI; the human opens it,
// authenticates through the existing sign-in, matches the user_code
// (anti-phishing), and `approve` binds their subject+tenant and mints
// a developer session; the CLI's `token` poll then receives it once.
//
// Trust boundary: start/token are unauthenticated but only ever
// return a token AFTER a full authenticated approval in the browser —
// the CLI never sees the human's credentials. The developer session
// is minted server-side and stored (auth.DeveloperSessionStore) so it
// is per-device revocable.
//
// Slice-1 scope: pending state is in-memory. That is correct for the
// single-instance deploy today; a durable, cross-instance store and a
// per-IP rate cap on `start` (the CLI-auth design security posture) are tracked
// fast-follows before multi-instance.
type DeviceAuthHandler struct {
	sessions auth.DeveloperSessionStore
	// devices keeps the approval devices. Nil: a phone's key stays
	// on its session alone, as before.
	devices auth.DeviceStore
	// keys keeps the proof runner's test phone keys to the test tenants:
	// one is never signed in, made an approval key, or paired elsewhere.
	keys *auth.KeyRestriction
	// windows keeps the approval windows (kiff/tafs#494): a sign-out or a
	// removed device ends its windows. Nil: none to end.
	windows auth.WindowStore
	// emails and notices tell a person when one of their phones can
	// approve again with a new key (a re-link). Nil: no notice.
	emails  deviceEmails
	notices notify.MembershipNotifier
	tenants deviceTenants
	signIns deviceSignIns

	// verificationURI is the browser page where the human approves,
	// e.g. https://app.kiff.dev/auth/device. Cloud-side config; never
	// baked into the framework CLI.
	verificationURI string

	// stepUp asks for a recent sign-in before a device is approved: the
	// approval gives the device its own way to answer held calls.
	// Nil: no step-up (tests that don't exercise it).
	stepUp *auth.StepUp

	deviceTTL  time.Duration // how long a start code is valid (~10m)
	sessionTTL time.Duration // developer-session lifetime (~90d)
	interval   time.Duration // min poll interval advertised + enforced
	maxPending int           // bound on concurrent pending flows (memory guard)

	// Approve rate-cap (the CLI-auth design security posture / a review): an
	// authenticated caller iterating live user_codes could bind their
	// own tenant to a victim's pending flow (session fixation). Cap
	// failed approvals per subject in a window to make brute-forcing
	// the code space impractical and attributable.
	maxApproveFails int
	approveWindow   time.Duration

	clock  func() time.Time
	logger *slog.Logger

	// waiting keeps the sign-ins waiting for approval beyond this process,
	// so a deploy never loses one (nil: memory only). An approved one, with
	// the session it minted, stays only in pending until the device takes it.
	waiting auth.WaitingSignIns
	// pairings keeps the computers a phone approved (the desktop-client design,
	// §5). Nil: no pairing routes.
	pairings auth.PairingStore
	// pairingMembers says whether a pairing's person is still a member when its computer claims it.
	pairingMembers interface {
		Live(ctx context.Context, tenantID, subject string) (membership.Member, error)
	}
	// passkeys keeps a passkey computer's own credential, registered by its
	// pairing and removed with the device. Nil: passkey computers don't pair.
	passkeys auth.PasskeyStore
	// passkeyRP is who a registered passkey is for (WithDevicePasskeyRP).
	passkeyRP auth.RelyingParty

	mu           sync.Mutex
	pending      map[string]*pendingAuth       // key: device-code hash (hex)
	byUserCode   map[string]string             // user_code -> device-code hash
	approveFails map[string]*approveFailWindow // subject -> failed-approve counter
	claimPolls   map[string]time.Time          // pairing nonce hash (hex) -> last claim poll
}

// approveFailWindow counts a subject's failed approvals within a
// sliding window for the rate-cap.
type approveFailWindow struct {
	count int
	start time.Time
}

type pendingAuth struct {
	deviceHash string
	userCode   string
	status     string // "pending" | "approved"
	subject    string
	tenant     string
	tokenOnce  string // minted session plaintext, delivered once then cleared
	// approvalMode is the minted session's, as its device is told with the
	// token (auth.ShownApprovalMode); empty for a sign-in with no device key.
	approvalMode string
	// approvingSubject is who is approving it while status is "approving".
	approvingSubject string
	createdAt        time.Time
	expiresAt        time.Time
	lastPollAt       time.Time
	// deviceKey and deviceName come from start when the device has a
	// signing key; approval binds them to the minted session.
	deviceKey  []byte
	deviceName string
	// approvalKey is the phone's approval key, from start (kiff/tafs#233):
	// approval binds it to the session with the device key.
	approvalKey []byte
	// platform is the device's, from start: "" when a key came
	// without one, never a guess. A later sign-in that names it fills it in.
	platform string
	// platformSent: the device said its platform.
	platformSent bool
	// kind is what the device is (auth.DeviceKindOf), from start: its name
	// in the list is drawn from it in the reader's language.
	kind string
	// passkey: the device has no approval key and said "approval":
	// "passkey"; keyStore is auth.KeyStoreSoftware when its device key isn't
	// in hardware (the desktop-client design, §1.1).
	passkey  bool
	keyStore string
	// relinkSession is set for a re-link (kiff/tafs#233, case 2): the
	// session whose approval key approval replaces with approvalKey. Its
	// subject and tenant are the session's, set at the start: only that
	// person may approve it. relinked is set once the key is replaced.
	relinkSession string
	relinked      bool
}

// deviceEmails resolves a person's email for a re-link's notice.
type deviceEmails interface {
	EmailForSubject(ctx context.Context, subject string) (string, error)
}

// DeviceAuthOption configures a DeviceAuthHandler.
type DeviceAuthOption func(*DeviceAuthHandler)

// WithDeviceKeyRestriction refuses, with 403 proof_test_key, a sign-in,
// an approval key or a pairing that would put a restricted key (the proof
// runner's test phone) on a tenant it may not act on.
func WithDeviceKeyRestriction(k *auth.KeyRestriction) DeviceAuthOption {
	return func(h *DeviceAuthHandler) { h.keys = k }
}

// WithDeviceVerificationURI sets the browser approval URL.
func WithDeviceVerificationURI(uri string) DeviceAuthOption {
	return func(h *DeviceAuthHandler) {
		if s := strings.TrimSpace(uri); s != "" {
			h.verificationURI = strings.TrimRight(s, "/")
		}
	}
}

// WithDeviceTTL overrides the start-code lifetime.
func WithDeviceTTL(d time.Duration) DeviceAuthOption {
	return func(h *DeviceAuthHandler) {
		if d > 0 {
			h.deviceTTL = d
		}
	}
}

// WithDeviceSessionTTL overrides the minted developer-session lifetime.
func WithDeviceSessionTTL(d time.Duration) DeviceAuthOption {
	return func(h *DeviceAuthHandler) {
		if d > 0 {
			h.sessionTTL = d
		}
	}
}

// WithDeviceStore keeps each signed-in device with a key as an approval
// device.
func WithDeviceStore(devices auth.DeviceStore) DeviceAuthOption {
	return func(h *DeviceAuthHandler) { h.devices = devices }
}

// WithDeviceNotices sends a person the account notice when one of their
// phones re-links its approval key.
func WithDeviceNotices(emails deviceEmails, notices notify.MembershipNotifier) DeviceAuthOption {
	return func(h *DeviceAuthHandler) { h.emails, h.notices = emails, notices }
}

// deviceSignIns hears which browser sign-in approved a device in the tafs
// app (newdevice.Service), so that sign-in's new-device email says "tafs
// by KIFF".
type deviceSignIns interface {
	Through(ctx context.Context, sessionID, subject string)
}

// WithDeviceSignIns tells s of each device approval's sign-in session.
func WithDeviceSignIns(s deviceSignIns) DeviceAuthOption {
	return func(h *DeviceAuthHandler) { h.signIns = s }
}

// throughTafs tells the sign-ins of the browser session that approved a
// device. Its id is the provider's session id, as the step-up reads it.
func (h *DeviceAuthHandler) throughTafs(ctx context.Context, id auth.Identity) {
	if h.signIns != nil {
		h.signIns.Through(ctx, strings.TrimSpace(id.Metadata["clerk_session_id"]), id.Subject)
	}
}

// WithDeviceTenants reads the account, so a re-link's email comes in the
// language the person's app last showed them (personlang). Without it,
// English.
func WithDeviceTenants(t deviceTenants) DeviceAuthOption {
	return func(h *DeviceAuthHandler) { h.tenants = t }
}

// deviceTenants reads an account.
type deviceTenants interface {
	Get(ctx context.Context, id string) (tenant.Tenant, error)
}

// WithDeviceStepUp asks for a recent sign-in (auth.StepUpWindow) before
// a device is approved. Refusing or ignoring one needs none.
func WithDeviceStepUp(s *auth.StepUp) DeviceAuthOption {
	return func(h *DeviceAuthHandler) { h.stepUp = s }
}

// WithDeviceClock substitutes the time source (tests).
// WithDeviceWindows ends a phone's approval windows when its session signs
// out or its device is removed (kiff/tafs#494).
func WithDeviceWindows(windows auth.WindowStore) DeviceAuthOption {
	return func(h *DeviceAuthHandler) { h.windows = windows }
}

// WithDeviceWaitingSignIns keeps the sign-ins waiting for approval in w,
// so they outlive the process (a deploy).
func WithDeviceWaitingSignIns(w auth.WaitingSignIns) DeviceAuthOption {
	return func(h *DeviceAuthHandler) { h.waiting = w }
}

func WithDeviceClock(now func() time.Time) DeviceAuthOption {
	return func(h *DeviceAuthHandler) {
		if now != nil {
			h.clock = now
		}
	}
}

// WithDeviceLogger substitutes the logger.
func WithDeviceLogger(l *slog.Logger) DeviceAuthOption {
	return func(h *DeviceAuthHandler) {
		if l != nil {
			h.logger = l
		}
	}
}

// NewDeviceAuthHandler wires the handler. sessions is required.
func NewDeviceAuthHandler(sessions auth.DeveloperSessionStore, opts ...DeviceAuthOption) *DeviceAuthHandler {
	h := &DeviceAuthHandler{
		sessions:        sessions,
		verificationURI: "https://app.kiff.dev/auth/device",
		deviceTTL:       10 * time.Minute,
		sessionTTL:      90 * 24 * time.Hour,
		interval:        5 * time.Second,
		maxPending:      10000,
		maxApproveFails: 10,
		approveWindow:   10 * time.Minute,
		clock:           func() time.Time { return time.Now().UTC() },
		logger:          slog.Default(),
		pending:         make(map[string]*pendingAuth),
		byUserCode:      make(map[string]string),
		approveFails:    make(map[string]*approveFailWindow),
		claimPolls:      make(map[string]time.Time),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// RegisterPublic attaches the unauthenticated device routes. These
// must sit OUTSIDE the auth middleware — the CLI has no credential
// yet when it calls them.
func (h *DeviceAuthHandler) RegisterPublic(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/auth/device/start", h.start)
	mux.HandleFunc("POST /v1/auth/device/token", h.token)
	if h.pairings != nil && h.devices != nil {
		mux.HandleFunc("POST /v1/pair/claim", h.claimPairing)
	}
}

// RegisterAuthenticated attaches the routes that run behind the auth
// middleware: approve (from the browser) and logout (from the CLI,
// presenting its developer-session token).
func (h *DeviceAuthHandler) RegisterAuthenticated(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/auth/device/pending", h.pendingInfo)
	mux.HandleFunc("POST /v1/auth/device/approve", h.approve)
	mux.HandleFunc("POST /v1/auth/device/logout", h.logout)
	mux.HandleFunc("GET /v1/me/devices", h.listDevices)
	mux.HandleFunc("DELETE /v1/me/devices/{id}", h.deleteDevice)
	mux.HandleFunc("POST /v1/me/device/approval-key", h.approvalKey)
	mux.HandleFunc("POST /v1/me/device/relink", h.relink)
	if h.passkeys != nil {
		mux.HandleFunc("POST /v1/me/passkeys", h.registerPasskey)
		mux.HandleFunc("GET /v1/me/passkeys", h.listPasskeys)
		mux.HandleFunc("DELETE /v1/me/passkeys/{id}", h.removePasskey)
	}
	if h.pairings != nil && h.devices != nil {
		mux.HandleFunc("POST /v1/me/pairings", h.approvePairing)
	}
}

// --- start ---

type deviceStartResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func (h *DeviceAuthHandler) start(w http.ResponseWriter, r *http.Request) {
	// A phone may send the public half of its signing key, and its name.
	// The key is bound only when a person approves this flow in
	// the browser; nothing else can add a key to a session.
	var req struct {
		PublicKey  string `json:"public_key"`
		DeviceName string `json:"device_name"`
		// ApprovalPublicKey is the key that signs what grants authority
		// (kiff/tafs#233); it comes with public_key.
		ApprovalPublicKey string `json:"approval_public_key"`
		// Platform is ios, ipados, macos, android, windows or linux.
		Platform string `json:"platform"`
		// Kind is what the device is: iphone, ipad, android_phone,
		// android_tablet or other. Left out, or one KIFF doesn't know, its
		// name says it (auth.DeviceKindOf).
		Kind string `json:"kind"`
		// Approval is "passkey" for a computer with no approval key of its
		// own (Linux, a Mac without Touch ID), sent instead of
		// approval_public_key; KeyStore is "software" when its device key
		// isn't in hardware, sent only with it (the desktop-client design, §1.1).
		Approval string `json:"approval"`
		KeyStore string `json:"key_store"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
	}
	var deviceKey []byte
	if strings.TrimSpace(req.PublicKey) != "" {
		k, err := auth.ParseDeviceKey(req.PublicKey)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_public_key", "detail": err.Error()})
			return
		}
		deviceKey = k
	}
	var approvalKey []byte
	if strings.TrimSpace(req.ApprovalPublicKey) != "" {
		if deviceKey == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "approval_public_key comes with the device's public_key"})
			return
		}
		k, err := auth.ParseDeviceKey(req.ApprovalPublicKey)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_approval_public_key", "detail": err.Error()})
			return
		}
		if bytes.Equal(k, deviceKey) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_approval_public_key", "detail": "the approval key is a second key, not the device key"})
			return
		}
		approvalKey = k
	}
	// A computer approves with an approval key or a passkey, never with its
	// device key alone: it says which. key_store goes only with a passkey.
	passkey := req.Approval == "passkey"
	switch {
	case req.Approval != "" && !passkey:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "approval is \"passkey\", or left out"})
		return
	case passkey && (deviceKey == nil || approvalKey != nil):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "approval \"passkey\" comes with public_key, instead of approval_public_key"})
		return
	case req.KeyStore != "" && (req.KeyStore != auth.KeyStoreSoftware || !passkey):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "key_store is \"software\", sent only with approval \"passkey\""})
		return
	case deviceKey != nil && approvalKey == nil && !passkey && pairingPlatforms[strings.TrimSpace(req.Platform)]:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "approval_required",
			"detail": "a computer sends approval_public_key, or approval \"passkey\": it never approves with its device key"})
		return
	}
	platform := strings.TrimSpace(req.Platform)
	// A device that names no platform is stored with none, never as a phone:
	// a missing platform proves nothing, and a computer that left it out must
	// not pass as an iPhone where a phone is trusted more (planque's window
	// rule for computers). A later sign-in that names it fills it in.
	switch {
	case platform != "" && deviceKey == nil:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "platform comes with the device's public_key"})
		return
	case platform != "" && !auth.ValidDevicePlatform(platform):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_platform", "detail": "platform is ios, ipados, macos, android, windows or linux"})
		return
	}
	deviceName, ok := cleanDeviceName(req.DeviceName)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_device_name", "detail": "device_name is at most 64 characters, without control characters"})
		return
	}
	now := h.clock()
	// A soft cap: counted, then kept, not at once, so starts at the same
	// moment may go a few over it.
	if n, err := h.waitingCount(r.Context(), now); err != nil || n >= h.maxPending {
		if err != nil {
			h.logger.ErrorContext(r.Context(), "device start: waiting sign-ins unread", slog.String("err", err.Error()))
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "too many pending device authorizations; retry shortly"})
		return
	}

	deviceCode, err := randToken(32)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not start device authorization"})
		return
	}
	userCode, err := newUserCode()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not start device authorization"})
		return
	}
	hash := hashHex(deviceCode)

	// Extremely unlikely user_code collision among live pendings: retry-free,
	// just overwrite-guard by regenerating is overkill at 10k cap; keep it
	// simple and index the latest.
	p := &pendingAuth{
		deviceHash: hash,
		userCode:   userCode,
		status:     "pending",
		createdAt:  now,
		expiresAt:  now.Add(h.deviceTTL),
		deviceKey:  deviceKey,
		deviceName: deviceName,
		platform:   platform,
		kind:       auth.DeviceKindOf(strings.TrimSpace(req.Kind), deviceName),

		approvalKey: approvalKey,
		passkey:     passkey,
		keyStore:    req.KeyStore,

		platformSent: strings.TrimSpace(req.Platform) != "",
	}
	if err := h.keep(r.Context(), p); err != nil {
		h.logger.ErrorContext(r.Context(), "device start: sign-in not kept", slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not start device authorization"})
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

// --- token (poll) ---

type deviceTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Tenant      string `json:"tenant"`
	Subject     string `json:"subject"`
	// ApprovalMode is how the new session approves (auth.ShownApprovalMode),
	// for a device that signed in with a key; GET /v1/me says the same later.
	ApprovalMode string `json:"approval_mode,omitempty"`
}

func (h *DeviceAuthHandler) token(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
	}
	code := strings.TrimSpace(req.DeviceCode)
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	now := h.clock()
	hash := hashHex(code)

	h.adopt(r.Context(), hash, "", now)
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.pending[hash]
	if !ok || now.After(p.expiresAt) {
		if ok {
			h.deleteLocked(p)
		}
		// RFC 8628: unknown or expired device_code.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expired_token"})
		return
	}
	// slow_down: reject a poll faster than the advertised interval.
	if !p.lastPollAt.IsZero() && now.Sub(p.lastPollAt) < h.interval {
		p.lastPollAt = now
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slow_down"})
		return
	}
	p.lastPollAt = now

	if p.status != "approved" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "authorization_pending"})
		return
	}

	if p.relinked {
		// A re-link mints nothing: the session keeps going with its new key.
		fingerprint := auth.DeviceKeyFingerprint(p.approvalKey)
		h.deleteLocked(p)
		writeJSON(w, http.StatusOK, map[string]string{"status": "relinked", "fingerprint": fingerprint})
		return
	}
	// Approved: deliver the minted token exactly once, then retire the
	// pending record so the token can't be re-fetched.
	tok := p.tokenOnce
	subject, tenant, mode := p.subject, p.tenant, p.approvalMode
	h.deleteLocked(p)
	writeJSON(w, http.StatusOK, deviceTokenResponse{
		AccessToken:  tok,
		TokenType:    "Bearer",
		ExpiresIn:    int(h.sessionTTL.Seconds()),
		Tenant:       tenant,
		Subject:      subject,
		ApprovalMode: mode,
	})
}

// --- approve (authenticated, from the browser) ---

func (h *DeviceAuthHandler) approve(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	if id.APIKey {
		// Approving a device mints a developer session, which acts as
		// the account's owner. Only a person, signed in, may do that;
		// a key, and above all an agent's key, would otherwise turn
		// itself into an owner session.
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "approving a device sign-in requires a signed-in person; an API key cannot approve one"})
		return
	}
	if id.Method == auth.MethodDeveloper {
		// Nor may another device session: a stolen phone token would
		// otherwise mint itself a keyless session, or one bound to the
		// attacker's key, and approve held calls without the phone.
		// Approval happens in the browser.
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "approve a device sign-in in your browser; a device session cannot approve one"})
		return
	}
	// A device approved here answers held calls on its own, so approving
	// one needs a recent sign-in, checked here and not only in the page.
	// Nothing is read or approved without it.
	if h.stepUp != nil {
		if _, err := h.stepUp.Check(r.Context(), id); err != nil {
			auth.WriteReauthRequired(w)
			return
		}
	}
	var req struct {
		UserCode string `json:"user_code"`
		Label    string `json:"label"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return
		}
	}
	userCode := normalizeUserCode(req.UserCode)
	if userCode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_code is required"})
		return
	}
	now := h.clock()

	// Rate-cap failed approvals per subject: an attacker guessing live
	// user_codes to hijack a victim's flow is throttled and attributable.
	if h.approveRateLimited(id.Subject, now) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many approval attempts; wait a few minutes and retry"})
		return
	}

	h.adopt(r.Context(), "", userCode, now)
	h.mu.Lock()
	hash := h.byUserCode[userCode]
	p := h.pending[hash]
	if p == nil || now.After(p.expiresAt) {
		reason, age := missReason(p, now)
		if p != nil {
			h.deleteLocked(p)
		}
		h.mu.Unlock()
		h.recordApproveFail(id.Subject, now)
		h.logger.InfoContext(r.Context(), "device approve missed", slog.String("reason", reason), slog.Duration("age", age))
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown or expired code"})
		return
	}
	if p.status == "approved" || p.status == "approving" {
		// Approved, or being approved by this process now (a double
		// click): said once, to its approver.
		mine := p.subject == id.Subject || p.status == "approving" && p.approvingSubject == id.Subject
		h.mu.Unlock()
		if !mine {
			// As for a lookup: to anyone but its approver, unknown.
			h.recordApproveFail(id.Subject, now)
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown or expired code"})
			return
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this code was already approved"})
		return
	}
	if p.relinkSession != "" {
		h.approveRelink(w, r, id, p, now)
		return
	}
	// Taken for approval in this process, so a second approve of it here (a
	// double click) is told so and claims nothing.
	p.status, p.approvingSubject = "approving", id.Subject
	deviceKey, deviceName, platform, platformSent, approvalKey, kind, keyStore, passkey := p.deviceKey, p.deviceName, p.platform, p.platformSent, p.approvalKey, p.kind, p.keyStore, p.passkey
	h.mu.Unlock()

	// Claimed before anything is minted: of two processes approving one
	// code at once (a deploy's overlap), one goes on; the other's copy is
	// stale and goes. A failure before the approval is committed gives the
	// code back, here and in h.waiting, so the person can try again.
	committed, lost := false, false
	defer func() {
		if committed || lost {
			return
		}
		h.mu.Lock()
		if p.status == "approving" {
			p.status, p.approvingSubject = "pending", ""
		}
		h.mu.Unlock()
		h.rekeep(r.Context(), p)
	}()
	if h.waiting != nil {
		won, err := h.waiting.Claim(r.Context(), hash, now)
		if err != nil {
			h.logger.ErrorContext(r.Context(), "device approve: claim failed", slog.String("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not approve the device; retry"})
			return
		}
		if !won {
			lost = true
			h.mu.Lock()
			h.deleteLocked(p)
			h.mu.Unlock()
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown or expired code"})
			return
		}
	}

	// Once the person has a device that approves, active or removed in the
	// last 30 days, the browser enrols no approval key (the desktop-client design,
	// §1.1), whatever the device sent: it's view-only until a device that
	// approves pairs it (§5). Otherwise the web sign-in alone would get round
	// the phone. A first device with an approval key approves with it.
	// A new device's session never approves with its device key: it's held
	// to its approval key, or view-only (no approval key: a computer that
	// approves with a passkey, until passkey approvals; a phone without
	// one). Only sessions from before approval modes keep "".
	mode, alsoApproves := "", false
	if deviceKey != nil {
		mode = auth.ApprovalModeView
	}
	if deviceKey != nil && h.devices != nil {
		has, err := h.devices.HasApprovalDevice(r.Context(), id.Tenant, id.Subject, now.Add(-auth.ApprovalDeviceKept))
		if err != nil {
			h.logger.ErrorContext(r.Context(), "device approve: read approval devices failed", slog.String("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the devices"})
			return
		}
		// Until phones pair (the desktop-client design, §1.1, "(a)"), a phone or
		// tablet that sends an approval key still enrols it, so a second
		// iPhone or iPad isn't stranded; the person is told by email. A
		// computer pairs, and a passkey or software key never enrols here.
		switch {
		case has && phoneEnrols(platform, approvalKey, passkey, keyStore):
			alsoApproves = true
		case has:
			approvalKey, mode = nil, auth.ApprovalModeView
		}
	}
	if approvalKey != nil {
		mode = auth.ApprovalModeKey
	}

	// The proof runner's test phone signs in on a test tenant only.
	if !h.keys.Allows(r.Context(), id.Tenant, deviceKey, approvalKey) {
		auth.WriteKeyRestricted(w)
		return
	}
	// Mint the developer session bound to the approving human. Done
	// outside the lock (store call); re-check under lock before commit.
	gen, err := auth.NewDevSessionToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not mint developer session"})
		return
	}
	// A device that named itself keeps its name; the browser's label is
	// for flows that didn't (the CLI).
	label := deviceName
	if label == "" {
		label = strings.TrimSpace(req.Label)
	}
	if label == "" {
		label = "cli"
	}
	sess := auth.DeveloperSession{
		ID:        ulid.MustNew(ulid.Timestamp(now), ulid.DefaultEntropy()).String(),
		Subject:   id.Subject,
		Tenant:    id.Tenant,
		Label:     label,
		TokenHash: gen.Hash,
		CreatedAt: now,
		ExpiresAt: now.Add(h.sessionTTL),

		DevicePublicKey:   deviceKey,
		ApprovalPublicKey: approvalKey,
		ApprovalMode:      mode,
	}
	// A device with a key is an approval device: it's kept, with an
	// id KIFF makes, and the session signs with its key. The same key signing
	// in again (a new session on the same phone) is the same device.
	created := "" // a device this sign-in made, to undo if it doesn't finish
	// A new name and platform for a device this key already has, set only
	// once the sign-in finishes, so an unfinished one changes nothing.
	var relabel func()
	if deviceKey != nil && h.devices != nil {
		existing, err := h.devices.FindByKey(r.Context(), id.Tenant, id.Subject, deviceKey)
		if errors.Is(err, auth.ErrDeviceNotFound) {
			device := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: id.Tenant, Subject: id.Subject, Name: label,
				Platform: platform, Kind: kind, PublicKey: deviceKey, HasApprovalKey: approvalKey != nil, KeyStore: keyStore, CreatedAt: now}
			switch ierr := h.devices.Insert(r.Context(), device); {
			case ierr == nil:
				sess.DeviceID, created = device.ID, device.ID
			case errors.Is(ierr, auth.ErrDeviceDuplicate):
				// Another sign-in of this key kept it first: that's the device.
				existing, err = h.devices.FindByKey(r.Context(), id.Tenant, id.Subject, deviceKey)
			default:
				h.logger.ErrorContext(r.Context(), "device approve: insert device failed",
					slog.String("tenant", id.Tenant), slog.String("err", ierr.Error()))
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not persist the device"})
				return
			}
		}
		switch {
		case created != "":
		case err == nil:
			sess.DeviceID = existing.ID
			// Kept before the session, so a sign-in that fails after it errs
			// toward the device counting as one that approves.
			if approvalKey != nil {
				if merr := h.devices.MarkApprovalKey(r.Context(), existing.ID); merr != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not persist the device"})
					return
				}
			}
			// A later sign-in of the same key names the device as it is now,
			// such as an iPad that first signed in from the iPhone-only build.
			name, plat, knd := existing.Name, existing.Platform, existing.Kind
			if deviceName != "" {
				name = deviceName
			}
			if platformSent {
				plat = platform
			}
			if kind != "" && (kind != auth.DeviceOther || deviceName != "") {
				knd = kind
			}
			if name != existing.Name || plat != existing.Platform || knd != existing.Kind {
				devID := existing.ID
				relabel = func() {
					if rerr := h.devices.Relabel(r.Context(), devID, name, plat, knd); rerr != nil {
						h.logger.WarnContext(r.Context(), "device approve: relabel failed", slog.String("device", devID), slog.String("err", rerr.Error()))
					}
				}
			}
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the devices"})
			return
		}
	}
	if err := h.sessions.Insert(r.Context(), sess); err != nil {
		h.revokeDevice(r, created, now)
		h.logger.ErrorContext(r.Context(), "device approve: insert session failed",
			slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not persist developer session"})
		return
	}

	h.mu.Lock()
	// Re-fetch: the pending record may have expired/been consumed while
	// we minted. If so, revoke the just-minted session to avoid an orphan.
	if cur := h.pending[hash]; cur == p && cur.status == "approving" && !now.After(cur.expiresAt) {
		cur.status = "approved"
		cur.subject = id.Subject
		cur.tenant = id.Tenant
		cur.tokenOnce = gen.Plaintext
		if deviceKey != nil {
			cur.approvalMode = auth.ShownApprovalMode(sess.ApprovalMode, approvalKey != nil, keyStore)
		}
		cur.approvingSubject = ""
		committed = true
		h.mu.Unlock()
		// Approved, it's no longer waiting (claimed above): the session it
		// minted is handed to the device from this process's memory, never
		// kept.
	} else {
		h.mu.Unlock()
		_ = h.sessions.Revoke(r.Context(), sess.ID, now)
		h.revokeDevice(r, created, now)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown or expired code"})
		return
	}

	if relabel != nil {
		relabel()
	}
	h.clearApproveFails(id.Subject)
	h.throughTafs(r.Context(), id)
	if alsoApproves {
		h.newApproverNotice(r.Context(), id.Tenant, id.Subject, label)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "approved", "user_code": userCode})
}

// phoneEnrolPlatforms are the platforms that still enrol an approval key from
// the browser while the person has another approval device: temporary, until
// a phone pairs from another the way a computer does.
var phoneEnrolPlatforms = map[string]bool{"ios": true, "ipados": true, "android": true}

// phoneEnrols reports whether a sign-in enrols its approval key though the
// person already has an approval device: a phone or tablet's, with an
// approval key of its own, never a passkey or a key in software.
func phoneEnrols(platform string, approvalKey []byte, passkey bool, keyStore string) bool {
	return phoneEnrolPlatforms[platform] && approvalKey != nil && !passkey && keyStore == ""
}

// newApproverNotice tells the person that another phone of theirs approves on
// KIFF now, enrolled from the browser while they had an approval device: the
// guard until phones pair. Best-effort, as relinkNotice.
func (h *DeviceAuthHandler) newApproverNotice(ctx context.Context, tenantID, subject, name string) {
	if h.emails == nil || h.notices == nil {
		return
	}
	to, err := h.emails.EmailForSubject(ctx, subject)
	if err != nil || strings.TrimSpace(to) == "" {
		h.logger.WarnContext(ctx, "device approve: no address for the new-approver notice", slog.String("subject", subject))
		return
	}
	lang := words.EN
	if h.tenants != nil {
		if t, err := h.tenants.Get(ctx, tenantID); err == nil {
			lang = personlang.Of(t, subject)
		}
	}
	w := words.Of(lang)
	phone := strings.TrimSpace(name)
	if phone == "" {
		phone = w.YourPhone
	}
	n := notify.AccountNotice{Subject: w.NewApproverSubject(phone), Body: w.NewApproverBody(phone), Lang: lang}
	if err := h.notices.AccountNotice(context.WithoutCancel(ctx), to, n); err != nil {
		h.logger.WarnContext(ctx, "device approve: new-approver notice failed", slog.String("err", err.Error()))
	}
}

// firstKeyAllowed reports whether a view-only session may register its own
// first approval key, and writes the refusal when not.
func (h *DeviceAuthHandler) firstKeyAllowed(w http.ResponseWriter, r *http.Request, id auth.Identity) bool {
	if id.ApprovalMode != auth.ApprovalModeView || id.KeyStore == auth.KeyStoreSoftware || h.devices == nil {
		writeJSON(w, http.StatusForbidden, auth.ViewOnlyRefusal())
		return false
	}
	has, err := h.devices.HasApprovalDevice(r.Context(), id.Tenant, id.Subject, h.clock().Add(-auth.ApprovalDeviceKept))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the devices"})
		return false
	}
	if has {
		writeJSON(w, http.StatusForbidden, auth.ViewOnlyRefusal())
		return false
	}
	return true
}

// approvalKey handles POST /v1/me/device/approval-key (kiff/tafs#233): a
// phone's session that signed in without an approval key registers one,
// once, signed by its device key (the route policy checks that). From then
// on, what grants authority through the session counts only with the
// approval key's signature. A session that has one answers 409 with that
// key's fingerprint, so the phone can tell whether it is its own:
// replacing it means signing in again.
func (h *DeviceAuthHandler) approvalKey(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if id.DeviceSessionID == "" || len(id.DeviceKey) == 0 || !auth.DeviceSigned(r.Context()) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "device_required",
			"detail": "a phone's session with a device key registers its approval key, signed by that key"})
		return
	}
	// A view-only session registers its own first approval key only where a
	// fresh browser sign-in with that key would enrol it: the person has no
	// approval device, active or removed in the last 30 days, and the device
	// key isn't in software (the desktop-client design, §1.1). Otherwise it gains
	// authority only by pairing (§5) or a new sign-in: a stolen token never
	// mints an approver.
	if id.ViewOnly() && !h.firstKeyAllowed(w, r, id) {
		return
	}
	var req struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	key, err := auth.ParseDeviceKey(req.PublicKey)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_public_key", "detail": err.Error()})
		return
	}
	if bytes.Equal(key, id.DeviceKey) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_public_key", "detail": "the approval key is a second key, not the device key"})
		return
	}
	if !h.keys.Allows(r.Context(), id.Tenant, key) {
		auth.WriteKeyRestricted(w)
		return
	}
	// The device holds an approval key from now on (HasApprovalDevice), kept
	// first so a registration that fails after it errs toward counting.
	if id.DeviceID != "" && h.devices != nil {
		if err := h.devices.MarkApprovalKey(r.Context(), id.DeviceID); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not register the approval key"})
			return
		}
	}
	sess, err := h.sessions.SetApprovalKey(r.Context(), id.DeviceSessionID, key, h.clock())
	if err == nil {
		// A window opened before this key was the session's counts no more (kiff/tafs#494).
		h.closeWindows(r, auth.WindowMatch{Session: id.DeviceSessionID}, auth.WindowClosedByKeyChange)
	}
	switch {
	case errors.Is(err, auth.ErrApprovalKeySet):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "approval_key_set",
			"detail": "this session has an approval key; sign in again to use another", "fingerprint": auth.DeviceKeyFingerprint(sess.ApprovalPublicKey)})
		return
	case errors.Is(err, auth.ErrDevSessionNotFound):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session_ended"})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not register the approval key"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"fingerprint": auth.DeviceKeyFingerprint(sess.ApprovalPublicKey)})
}

// logout handles POST /v1/auth/device/logout: the CLI presents its
// developer-session bearer and this revokes exactly that session
// (per-device logout, the CLI-auth design). Idempotent — an unknown or
// already-revoked token still returns 204 so `kiff auth logout` never
// errors on a stale credential.
func (h *DeviceAuthHandler) logout(w http.ResponseWriter, r *http.Request) {
	if _, err := auth.IdentityFrom(r.Context()); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	// `?all=true` (or body {"all":true}) is "log out everywhere": revoke
	// every active session for the authenticated subject, not just the
	// one presented. Used by `kiff auth logout --all`.
	if wantsAll(r) {
		id, _ := auth.IdentityFrom(r.Context())
		if strings.TrimSpace(id.Subject) == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if _, err := h.sessions.RevokeAllForSubject(r.Context(), id.Subject, h.clock()); err != nil {
			h.logger.WarnContext(r.Context(), "device logout: revoke-all failed", slog.String("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not revoke sessions"})
			return
		}
		h.closeWindows(r, auth.WindowMatch{Subject: id.Subject}, auth.WindowClosedBySignOut)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	parsed, perr := auth.ParseDevSessionToken(raw)
	if perr != nil {
		// Not a developer-session bearer (e.g. a Clerk session). Nothing
		// device-scoped to revoke; treat as a no-op success.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := h.sessions.RevokeByHash(r.Context(), parsed.Hash, h.clock()); err != nil && err != auth.ErrDevSessionNotFound {
		h.logger.WarnContext(r.Context(), "device logout: revoke failed", slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not revoke session"})
		return
	}
	// The session's approval windows end with it (kiff/tafs#494).
	if id, err := auth.IdentityFrom(r.Context()); err == nil && id.DeviceSessionID != "" {
		h.closeWindows(r, auth.WindowMatch{Session: id.DeviceSessionID}, auth.WindowClosedBySignOut)
	}
	w.WriteHeader(http.StatusNoContent)
}

// closeWindows ends the approval windows match names, for why. Best effort:
// the sign-out or removal that asked for it already holds.
func (h *DeviceAuthHandler) closeWindows(r *http.Request, match auth.WindowMatch, why string) {
	if h.windows == nil {
		return
	}
	if _, err := h.windows.CloseAll(r.Context(), match, h.clock(), why); err != nil {
		h.logger.WarnContext(r.Context(), "device: closing approval windows failed", slog.String("why", why), slog.String("err", err.Error()))
	}
}

// wantsAll reports whether the logout request asked to revoke every
// session for the subject, via ?all=true or a JSON body {"all":true}.
func wantsAll(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("all")), "true") {
		return true
	}
	if r.Body != nil && r.ContentLength != 0 {
		var body struct {
			All bool `json:"all"`
		}
		if json.NewDecoder(r.Body).Decode(&body) == nil {
			return body.All
		}
	}
	return false
}

// approveRateLimited reports whether the subject has exceeded the
// failed-approval cap within the current window.
func (h *DeviceAuthHandler) approveRateLimited(subject string, now time.Time) bool {
	if subject == "" || h.maxApproveFails <= 0 {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	fw := h.approveFails[subject]
	if fw == nil || now.Sub(fw.start) > h.approveWindow {
		return false // no window, or the window has rolled over
	}
	return fw.count >= h.maxApproveFails
}

// recordApproveFail increments the subject's failed-approval counter,
// starting a fresh window when none is active or the last has expired.
func (h *DeviceAuthHandler) recordApproveFail(subject string, now time.Time) {
	if subject == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	fw := h.approveFails[subject]
	if fw == nil || now.Sub(fw.start) > h.approveWindow {
		h.approveFails[subject] = &approveFailWindow{count: 1, start: now}
		return
	}
	fw.count++
}

// clearApproveFails resets a subject's counter after a success.
func (h *DeviceAuthHandler) clearApproveFails(subject string) {
	if subject == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.approveFails, subject)
}

// --- internal helpers ---

func (h *DeviceAuthHandler) evictExpiredLocked(now time.Time) {
	for hash, p := range h.pending {
		if now.After(p.expiresAt) {
			delete(h.pending, hash)
			delete(h.byUserCode, p.userCode)
		}
	}
}

func (h *DeviceAuthHandler) deleteLocked(p *pendingAuth) {
	delete(h.pending, p.deviceHash)
	delete(h.byUserCode, p.userCode)
	h.unkeep(context.Background(), p.deviceHash)
}

// keep records a sign-in waiting for approval: in h.waiting first, so a
// restart keeps it, then in this process. h.mu must not be held.
func (h *DeviceAuthHandler) keep(ctx context.Context, p *pendingAuth) error {
	if h.waiting != nil {
		if err := h.waiting.Put(ctx, waitingOf(p)); err != nil {
			return err
		}
	}
	h.mu.Lock()
	h.pending[p.deviceHash] = p
	h.byUserCode[p.userCode] = p.deviceHash
	h.mu.Unlock()
	return nil
}

// rekeep puts back a sign-in claimed for an approval that failed before
// it was committed, so the person can try again. h.mu must not be held.
func (h *DeviceAuthHandler) rekeep(ctx context.Context, p *pendingAuth) {
	if h.waiting == nil {
		return
	}
	h.mu.Lock()
	cur := h.pending[p.deviceHash]
	h.mu.Unlock()
	if cur == nil || cur.status != "pending" || h.clock().After(cur.expiresAt) {
		return
	}
	if err := h.waiting.Put(ctx, waitingOf(cur)); err != nil {
		h.logger.WarnContext(ctx, "device: claimed sign-in not put back", slog.String("err", err.Error()))
	}
}

func waitingOf(p *pendingAuth) auth.WaitingSignIn {
	return auth.WaitingSignIn{DeviceHash: p.deviceHash, UserCode: p.userCode, Subject: p.subject, Tenant: p.tenant,
		RelinkSession: p.relinkSession, DeviceKey: p.deviceKey, ApprovalKey: p.approvalKey, DeviceName: p.deviceName,
		Platform: p.platform, PlatformSent: p.platformSent, Kind: p.kind, Passkey: p.passkey, KeyStore: p.keyStore,
		CreatedAt: p.createdAt, ExpiresAt: p.expiresAt}
}

// unkeep drops a sign-in from h.waiting: refused, replaced or expired. A
// failure is logged; the row expires on its own.
func (h *DeviceAuthHandler) unkeep(ctx context.Context, hash string) {
	if h.waiting == nil {
		return
	}
	if err := h.waiting.Delete(ctx, hash); err != nil {
		h.logger.WarnContext(ctx, "device: waiting sign-in not dropped", slog.String("err", err.Error()))
	}
}

// adopt brings a sign-in another process started (before a restart) into
// this one, by its device-code hash or its user code, when this process
// doesn't have it. It reads h.waiting outside the lock; h.mu must not be
// held.
func (h *DeviceAuthHandler) adopt(ctx context.Context, hash, code string, now time.Time) {
	if h.waiting == nil {
		return
	}
	known := func() bool {
		if code != "" {
			_, ok := h.byUserCode[code]
			return ok
		}
		_, ok := h.pending[hash]
		return ok
	}
	h.mu.Lock()
	has := known()
	h.mu.Unlock()
	if has {
		return
	}
	var w *auth.WaitingSignIn
	var err error
	if code != "" {
		w, err = h.waiting.ByUserCode(ctx, code, now)
	} else {
		w, err = h.waiting.ByHash(ctx, hash, now)
	}
	if err != nil {
		h.logger.WarnContext(ctx, "device: waiting sign-in unread", slog.String("err", err.Error()))
	}
	if w == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if known() {
		return
	}
	p := &pendingAuth{deviceHash: w.DeviceHash, userCode: w.UserCode, status: "pending", subject: w.Subject, tenant: w.Tenant,
		createdAt: w.CreatedAt, expiresAt: w.ExpiresAt, deviceKey: w.DeviceKey, deviceName: w.DeviceName, approvalKey: w.ApprovalKey,
		platform: w.Platform, platformSent: w.PlatformSent, kind: w.Kind, relinkSession: w.RelinkSession, passkey: w.Passkey, keyStore: w.KeyStore}
	h.pending[p.deviceHash] = p
	h.byUserCode[p.userCode] = p.deviceHash
}

// waitingCount is how many sign-ins wait, for the cap: h.waiting's, once
// its expired ones are gone, or this process's. h.mu must not be held.
func (h *DeviceAuthHandler) waitingCount(ctx context.Context, now time.Time) (int, error) {
	if h.waiting == nil {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.evictExpiredLocked(now)
		return len(h.pending), nil
	}
	h.mu.Lock()
	h.evictExpiredLocked(now)
	h.mu.Unlock()
	return h.waiting.Sweep(ctx, now)
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// randToken returns n bytes of crypto/rand as base32 (no padding).
func randToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

// userCodeAlphabet excludes easily-confused characters (no 0/O, 1/I).
const userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ23456789"

// newUserCode returns a short, human-typable code like "BCDF-GHJK".
func newUserCode() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, 0, 9)
	for i, b := range buf {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, userCodeAlphabet[int(b)%len(userCodeAlphabet)])
	}
	return string(out), nil
}

// missReason says why a code isn't one to approve, and how old it is:
// unknown (never started here, or already used and cleared), expired, or
// its status (approved). The age is zero for an unknown code.
func missReason(p *pendingAuth, now time.Time) (string, time.Duration) {
	switch {
	case p == nil:
		return "unknown", 0
	case now.After(p.expiresAt):
		return "expired", now.Sub(p.createdAt)
	default:
		return p.status, now.Sub(p.createdAt)
	}
}

// normalizeUserCode upper-cases and strips spaces so a human who
// typed "bcdf-ghjk" or "BCDF GHJK" still matches.
func normalizeUserCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "")
	return s
}

// cleanDeviceName trims a device's name and refuses one too long or
// with control characters, since it is shown on the approval page.
func cleanDeviceName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if len([]rune(name)) > 64 {
		return "", false
	}
	for _, c := range name {
		if c < 0x20 || c == 0x7f {
			return "", false
		}
	}
	return name, true
}

// pendingInfo serves GET /v1/auth/device/pending?user_code=: what the
// person is about to approve, so the page can show the device's name and
// its key's fingerprint before they do. A miss counts toward the
// same rate cap as a failed approval, so it cannot enumerate codes.
func (h *DeviceAuthHandler) pendingInfo(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || strings.TrimSpace(id.Tenant) == "" || id.APIKey || id.Method == auth.MethodDeveloper {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only a person signed in in the browser can look up a device sign-in"})
		return
	}
	now := h.clock()
	if h.approveRateLimited(id.Subject, now) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many approval attempts; wait a few minutes and retry"})
		return
	}
	userCode := normalizeUserCode(r.URL.Query().Get("user_code"))
	if userCode != "" {
		h.adopt(r.Context(), "", userCode, now)
	}
	h.mu.Lock()
	var p *pendingAuth
	if hash, ok := h.byUserCode[userCode]; ok && userCode != "" {
		p = h.pending[hash]
	}
	if p == nil || now.After(p.expiresAt) || p.status != "pending" {
		reason, age := missReason(p, now)
		// Only the person who approved it learns that a code was approved;
		// to anyone else it is as unknown as any other code.
		mine := p != nil && p.subject == id.Subject
		h.mu.Unlock()
		h.recordApproveFail(id.Subject, now)
		// Why a lookup missed, never the code: "unknown or expired" on the
		// page covered all of these, so a report of a fresh code shown as
		// expired couldn't be told apart (kiff/tafs#58).
		h.logger.InfoContext(r.Context(), "device lookup missed", slog.String("reason", reason), slog.Duration("age", age))
		if reason == "approved" && mine {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "this code was already approved"})
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown or expired code"})
		return
	}
	if p.relinkSession != "" && p.subject != id.Subject {
		// A re-link is its own person's to see, as it is theirs to approve;
		// the page then approves it on the phone's account, whichever one is
		// in use in the browser.
		h.mu.Unlock()
		h.recordApproveFail(id.Subject, now)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown or expired code"})
		return
	}
	out := map[string]any{"user_code": userCode, "device_name": p.deviceName, "has_key": len(p.deviceKey) > 0}
	if len(p.deviceKey) > 0 {
		out["key_fingerprint"] = auth.DeviceKeyFingerprint(p.deviceKey)
	}
	if p.relinkSession != "" {
		// A phone already signed in, asking to approve again with a new key:
		// the page says which, and that it should be in the person's hands.
		out["relink"] = true
		out["approval_key_fingerprint"] = auth.DeviceKeyFingerprint(p.approvalKey)
		out["tenant"] = p.tenant
	}
	h.mu.Unlock()
	// Whether approving would pass the recent sign-in check, so the
	// page signs the person in again before it offers Approve, not after.
	// Approve checks it again; this only says it.
	recent := true
	if h.stepUp != nil {
		_, err := h.stepUp.Check(r.Context(), id)
		recent = err == nil
	}
	out["recent_sign_in"] = recent
	writeJSON(w, http.StatusOK, out)
}

// listDevices serves GET /v1/me/devices: the person's devices on this
// account that approve. A device's id is KIFF's (dev_…); a
// phone signed in before and not yet linked shows by its session.
// Each comes with its kind and display_name, its name by its kind in the
// request's language (Accept-Language): never the language the app had at
// sign-in. name stays what the device gave, and display_name is it for a
// device of another kind.
func (h *DeviceAuthHandler) listDevices(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.APIKey {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "devices belong to a signed-in person"})
		return
	}
	now := h.clock()
	lang := words.Of(words.Lang(replyLanguage(r)))
	named := func(v map[string]any, kind, name string) {
		kind = auth.DeviceKindOf(kind, name)
		v["kind"] = kind
		if dn := lang.DeviceName(kind, name); dn != "" {
			v["display_name"] = dn
		}
	}
	out := make([]map[string]any, 0)
	if h.devices != nil {
		devices, err := h.devices.ListActive(r.Context(), id.Tenant, id.Subject)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list devices failed"})
			return
		}
		for _, d := range devices {
			v := map[string]any{"id": d.ID, "name": d.Name, "platform": d.Platform, "key_fingerprint": auth.DeviceKeyFingerprint(d.PublicKey),
				"attested": d.Attestation != "", "created_at": d.CreatedAt, "this_device": d.ID == id.DeviceID}
			named(v, d.Kind, d.Name)
			if d.LastUsedAt != nil {
				v["last_used_at"] = *d.LastUsedAt
			}
			out = append(out, v)
		}
	}
	sessions, err := h.sessions.ListActiveForSubject(r.Context(), id.Subject, now)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list devices failed"})
		return
	}
	for _, s := range sessions {
		if s.Tenant != id.Tenant || len(s.DevicePublicKey) == 0 || (s.DeviceID != "" && h.devices != nil) {
			continue
		}
		d := map[string]any{"id": s.ID, "name": s.Label, "platform": "ios", "key_fingerprint": auth.DeviceKeyFingerprint(s.DevicePublicKey),
			"attested": false, "created_at": s.CreatedAt, "expires_at": s.ExpiresAt, "this_device": s.ID == id.DeviceSessionID}
		named(d, "", s.Label)
		if s.LastSeenAt != nil {
			d["last_seen_at"] = *s.LastSeenAt
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// deleteDevice serves DELETE /v1/me/devices/{id}: the device no longer
// approves, and every session it signed in is over. A phone from
// before not yet linked is removed by its session. From a device's
// session, removing another device that holds an approval key takes an
// approval (removalApproved); removing its own device takes none.
func (h *DeviceAuthHandler) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.APIKey {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "devices belong to a signed-in person"})
		return
	}
	now := h.clock()
	target := r.PathValue("id")
	sessions, err := h.sessions.ListActiveForSubject(r.Context(), id.Subject, now)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load devices failed"})
		return
	}
	if strings.HasPrefix(target, "dev_") && h.devices != nil {
		d, err := h.devices.Get(r.Context(), target)
		if errors.Is(err, auth.ErrDeviceNotFound) || (err == nil && (d.Tenant != id.Tenant || d.Subject != id.Subject || d.IsRevoked())) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such device"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load devices failed"})
			return
		}
		if d.ID != id.DeviceID && d.HasApprovalKey && !removalApproved(w, r, id) {
			return
		}
		// The device first: from then on no session of it verifies, even one
		// this request doesn't see.
		if err := h.devices.Revoke(r.Context(), d.ID, now); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "remove device failed"})
			return
		}
		// Its own passkey goes with it. Should this fail, the passkey still
		// counts only for this device, whose sessions no longer verify.
		h.revokePasskeys(r, d.ID, now)
		// The computers it approved and that haven't claimed yet are taken back
		// too. The claim also refuses a removed approver, should this fail.
		if h.pairings != nil {
			if err := h.pairings.DropUnclaimedBy(r.Context(), d.ID); err != nil {
				h.logger.WarnContext(r.Context(), "remove device: unclaimed pairings kept", slog.String("device", d.ID), slog.String("err", err.Error()))
			}
		}
		for _, s := range sessions {
			if s.DeviceID == d.ID {
				_ = h.sessions.Revoke(r.Context(), s.ID, now)
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	for _, s := range sessions {
		if s.ID == target && s.Tenant == id.Tenant && len(s.DevicePublicKey) > 0 {
			if s.ID != id.DeviceSessionID && len(s.ApprovalPublicKey) > 0 && !removalApproved(w, r, id) {
				return
			}
			if err := h.sessions.Revoke(r.Context(), s.ID, now); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "remove device failed"})
				return
			}
			h.revokeDevice(r, s.DeviceID, now)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such device"})
}

// removalApproved reports whether id may remove another of the person's
// devices that holds an approval key, and writes the refusal when not: a
// device's session approves it as it approves anything (PhoneMaySignRules),
// so a view-only session can't remove the phone that approves. A browser
// session removes it as before: that's how a lost phone is removed.
func removalApproved(w http.ResponseWriter, r *http.Request, id auth.Identity) bool {
	switch with, signed := auth.SignedWithFrom(r.Context()); {
	case len(id.DeviceKey) == 0:
		return true
	case id.ViewOnly():
		writeJSON(w, http.StatusForbidden, auth.ViewOnlyRefusal())
		return false
	case id.ApprovalMode == "":
		// A session from before approval modes removes it unsigned, as before:
		// the phones signed in then don't sign it.
		return true
	case !signed || !with.Critical():
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "approval_key_required",
			"detail": "removing another device that approves takes an approval: sign it with this device's approval key"})
		return false
	}
	return true
}

// revokeDevice removes a device a failed or undone sign-in created, or one
// whose session was removed. Best-effort: "" or no store does nothing.
func (h *DeviceAuthHandler) revokeDevice(r *http.Request, deviceID string, now time.Time) {
	if deviceID == "" || h.devices == nil {
		return
	}
	if err := h.devices.Revoke(r.Context(), deviceID, now); err != nil && !errors.Is(err, auth.ErrDeviceNotFound) {
		h.logger.WarnContext(r.Context(), "device: revoke failed", slog.String("device", deviceID), slog.String("err", err.Error()))
	}
	h.revokePasskeys(r, deviceID, now)
	h.closeWindows(r, auth.WindowMatch{Device: deviceID}, auth.WindowClosedByRevoked)
}

// revokePasskeys removes the passkeys registered with a device (a passkey
// computer's own). Best effort: each counts only for its device anyway.
func (h *DeviceAuthHandler) revokePasskeys(r *http.Request, deviceID string, now time.Time) {
	if deviceID == "" || h.passkeys == nil {
		return
	}
	if err := h.passkeys.RevokeForDevice(r.Context(), deviceID, now); err != nil {
		h.logger.WarnContext(r.Context(), "device: its passkeys not removed", slog.String("device", deviceID), slog.String("err", err.Error()))
	}
}
