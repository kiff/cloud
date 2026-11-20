// Package appsignin signs a person in inside the tafs apps: the authorization
// code flow with PKCE for one first-party client (README.md is the contract).
//
// GET /oauth/authorize hands a request naming ClientID to Authorize, which
// keeps it and sends the sheet to the sign-in page. Once the person is signed
// in there, the page calls POST /v1/auth/app/approve with their session, and
// the answer is the app's redirect with a code that lives CodeTTL and works
// once. The token route (next) redeems it with Redeem and the code verifier.
//
// Requests and codes are kept in memory: KIFF's API runs as one process, and
// a request lives minutes and a code a minute, so a restart costs a person
// one more tap on Sign in.
package appsignin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
)

const (
	// ClientID is the tafs apps' client: first party, so there's no consent page.
	ClientID = "tafs-app"
	// RedirectURI is the only redirect: a private-use scheme (RFC 8252 §7.1).
	RedirectURI = "dev.kiff.tafs:/oauth"
	// RequestTTL is how long the sign-in page may take.
	RequestTTL = 10 * time.Minute
	// CodeTTL is how long a code may wait to be redeemed.
	CodeTTL = 60 * time.Second
	// maxPending bounds the requests and codes kept at once.
	maxPending = 10000
)

// Locales are the sign-in page's languages (ui_locales); any other is English.
var Locales = []string{"en", "de", "fr", "es", "it", "pt", "nl", "pl"}

// Errors Redeem answers. Each is invalid_grant to the app except ErrOff.
var (
	ErrOff      = errors.New("appsignin: app sign-in is off")
	ErrBadGrant = errors.New("appsignin: the code is unknown, used, expired, or doesn't match")
)

// SpentTTL is how long a spent code is remembered, so a replay is known as one.
const SpentTTL = 10 * time.Minute

// Replay is a code used again after it was spent: invalid_grant to the app, and
// the session and device it made (Minted) are ended (RFC 6749 §4.1.2).
type Replay struct {
	SessionID string // "" when the first use minted nothing
	DeviceID  string // a device that first use made, else ""
}

func (*Replay) Error() string { return "appsignin: a spent code was used again" }

// Unwrap makes a Replay an ErrBadGrant too.
func (*Replay) Unwrap() error { return ErrBadGrant }

// Signed is who a redeemed code signed in.
type Signed struct {
	Subject string
	Tenant  string
	Email   string
}

type request struct {
	redirect, state, challenge string
	expires                    time.Time
}

type code struct {
	challenge, redirect string
	who                 Signed
	expires             time.Time
}

// Handler serves app sign-in. Off unless On is set.
type Handler struct {
	on      bool
	pageURL string
	now     func() time.Time
	// stepUp, when set, needs a recent sign-in to approve, as the device
	// flow's approve does: a code here mints a session that approves.
	stepUp *auth.StepUp

	mu       sync.Mutex
	requests map[string]request // by request id
	codes    map[string]code    // by sha256 of the code
	spent    map[string]spent   // by sha256 of the code, for SpentTTL
}

type spent struct {
	minted  Replay
	expires time.Time
}

// Option configures a Handler.
type Option func(*Handler)

// WithClock sets the clock (tests).
func WithClock(now func() time.Time) Option { return func(h *Handler) { h.now = now } }

// WithStepUp needs a recent sign-in (auth.StepUp) to approve.
func WithStepUp(s *auth.StepUp) Option { return func(h *Handler) { h.stepUp = s } }

// New returns a Handler. pageURL is the sign-in page the sheet opens on, such
// as https://app.kiff.dev/sign-in/app. on is the KIFF_APP_SIGNIN switch.
func New(on bool, pageURL string, opts ...Option) *Handler {
	h := &Handler{on: on, pageURL: pageURL, now: time.Now, requests: map[string]request{}, codes: map[string]code{}, spent: map[string]spent{}}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Authorize handles GET /oauth/authorize for ClientID. A wrong redirect is a
// 400 page, never a redirect (RFC 6749 §4.1.2.1); any other refusal goes back
// to the app as error and state.
func (h *Handler) Authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("redirect_uri") != RedirectURI {
		http.Error(w, "This redirect address is not registered for the tafs app.", http.StatusBadRequest)
		return
	}
	state := q.Get("state")
	back := func(e, desc string) {
		http.Redirect(w, r, errorRedirect(state, e, desc), http.StatusFound)
	}
	switch {
	case !h.on:
		back("unsupported_client", "signing in inside the app isn't on yet")
		return
	case q.Get("response_type") != "code":
		back("invalid_request", "response_type must be code")
		return
	case q.Get("code_challenge_method") != "S256" || !challengeShaped(q.Get("code_challenge")):
		back("invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	case len(state) < 16 || len(state) > 512:
		back("invalid_request", "state is required (16 to 512 characters)")
		return
	}
	id := "ar_" + randomToken()
	if !h.keepRequest(id, request{redirect: RedirectURI, state: state, challenge: q.Get("code_challenge"), expires: h.now().Add(RequestTTL)}) {
		back("temporarily_unavailable", "too many sign-ins at once; try again")
		return
	}
	page, _ := url.Parse(h.pageURL)
	pq := page.Query()
	pq.Set("request", id)
	if l := q.Get("ui_locales"); slices.Contains(Locales, l) {
		pq.Set("ui_locales", l)
	}
	if hint := q.Get("login_hint"); hint != "" && len(hint) <= 254 {
		if a, err := mail.ParseAddress(hint); err == nil && a.Address == hint {
			pq.Set("login_hint", hint)
		}
	}
	page.RawQuery = pq.Encode()
	http.Redirect(w, r, page.String(), http.StatusFound)
}

// RegisterAuthenticated mounts the routes the sign-in page calls with the
// person's session, behind the auth middleware.
func (h *Handler) RegisterAuthenticated(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/auth/app/approve", h.approve)
	mux.HandleFunc("POST /v1/auth/app/deny", h.deny)
}

// Response tells the sign-in page where to send the sheet.
type Response struct {
	RedirectTo string `json:"redirect_to"`
}

func (h *Handler) approve(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.APIKey || id.Method != auth.MethodSession || id.Subject == "" || id.Tenant == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "signing in to the app needs a signed-in person"})
		return
	}
	if h.stepUp != nil {
		if _, err := h.stepUp.Check(r.Context(), id); err != nil {
			auth.WriteReauthRequired(w)
			return
		}
	}
	req, ok := h.takeRequest(w, r)
	if !ok {
		return
	}
	raw := randomToken()
	if !h.keepCode(hashOf(raw), code{challenge: req.challenge, redirect: req.redirect,
		who: Signed{Subject: id.Subject, Tenant: id.Tenant, Email: id.Email}, expires: h.now().Add(CodeTTL)}) {
		writeJSON(w, http.StatusOK, Response{RedirectTo: errorRedirect(req.state, "temporarily_unavailable", "too many sign-ins at once; try again")})
		return
	}
	u, _ := url.Parse(req.redirect)
	q := u.Query()
	q.Set("code", raw)
	q.Set("state", req.state)
	u.RawQuery = q.Encode()
	writeJSON(w, http.StatusOK, Response{RedirectTo: u.String()})
}

func (h *Handler) deny(w http.ResponseWriter, r *http.Request) {
	req, ok := h.takeRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, Response{RedirectTo: errorRedirect(req.state, "access_denied", "the person closed or declined the sign-in")})
}

// takeRequest reads {"request": id} and removes the request, so it's used once.
func (h *Handler) takeRequest(w http.ResponseWriter, r *http.Request) (request, bool) {
	if !h.on {
		http.NotFound(w, r)
		return request{}, false
	}
	var body struct {
		Request string `json:"request"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Request == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the body is {request}"})
		return request{}, false
	}
	h.mu.Lock()
	req, ok := h.requests[body.Request]
	delete(h.requests, body.Request)
	h.mu.Unlock()
	if !ok || !h.now().Before(req.expires) {
		writeJSON(w, http.StatusGone, map[string]string{"error": "this sign-in has expired; start again from the app"})
		return request{}, false
	}
	return req, true
}

// Redeem spends a code: it's removed first, so a wrong verifier also ends it.
// The verifier must hash (S256) to the code's challenge, and the redirect and
// client must be the app's.
func (h *Handler) Redeem(_ context.Context, clientID, raw, verifier, redirect string) (Signed, error) {
	if !h.on {
		return Signed{}, ErrOff
	}
	if raw == "" {
		return Signed{}, ErrBadGrant
	}
	key := hashOf(raw)
	h.mu.Lock()
	c, ok := h.codes[key]
	delete(h.codes, key)
	if !ok {
		if sp, again := h.spent[key]; again && h.now().Before(sp.expires) {
			delete(h.spent, key)
			h.mu.Unlock()
			r := sp.minted
			return Signed{}, &r
		}
	} else {
		h.spent[key] = spent{expires: h.now().Add(SpentTTL)}
	}
	h.mu.Unlock()
	switch {
	case !ok, !h.now().Before(c.expires), clientID != ClientID, redirect != c.redirect, !verifierShaped(verifier):
		return Signed{}, ErrBadGrant
	}
	sum := sha256.Sum256([]byte(verifier))
	if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(c.challenge)) != 1 {
		return Signed{}, ErrBadGrant
	}
	return c.who, nil
}

// Minted records what a redeemed code made, so a replay of it can end them.
func (h *Handler) Minted(raw, sessionID, deviceID string) {
	key := hashOf(raw)
	h.mu.Lock()
	defer h.mu.Unlock()
	if sp, ok := h.spent[key]; ok {
		sp.minted = Replay{SessionID: sessionID, DeviceID: deviceID}
		h.spent[key] = sp
	}
}

func (h *Handler) keepRequest(id string, req request) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sweepLocked()
	if len(h.requests) >= maxPending {
		return false
	}
	h.requests[id] = req
	return true
}

func (h *Handler) keepCode(key string, c code) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sweepLocked()
	if len(h.codes) >= maxPending {
		return false
	}
	h.codes[key] = c
	return true
}

func (h *Handler) sweepLocked() {
	now := h.now()
	for k, v := range h.requests {
		if !now.Before(v.expires) {
			delete(h.requests, k)
		}
	}
	for k, v := range h.codes {
		if !now.Before(v.expires) {
			delete(h.codes, k)
		}
	}
	for k, v := range h.spent {
		if !now.Before(v.expires) {
			delete(h.spent, k)
		}
	}
}

// challengeShaped: BASE64URL(SHA256(...)) is 43 characters of the URL-safe alphabet.
func challengeShaped(s string) bool { return len(s) == 43 && urlSafe(s) }

// verifierShaped: 43 to 128 characters of [A-Za-z0-9-._~] (RFC 7636 §4.1).
func verifierShaped(s string) bool {
	if len(s) < 43 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~') {
			return false
		}
	}
	return true
}

func urlSafe(s string) bool {
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func errorRedirect(state, e, desc string) string {
	u, _ := url.Parse(RedirectURI)
	q := u.Query()
	q.Set("error", e)
	q.Set("error_description", desc)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashOf(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
