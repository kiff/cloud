// Package oauth is KIFF's authorization server for the gateway.
//
// Clerk signs the person in; KIFF authorizes an agent. On consent an
// admin or editor picks or creates an agent, and KIFF mints that
// agent's gateway_agent key. The OAuth tokens are handles to that key:
// they can only ask, every decision names the agent, and ending the
// grant revokes the key.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/oklog/ulid/v2"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
)

// Lifetimes.
const (
	AccessTTL  = time.Hour
	IdleLimit  = 90 * 24 * time.Hour
	CodeTTL    = 60 * time.Second
	RequestTTL = 10 * time.Minute
	// DeviceTTL and DeviceInterval: a device code lasts 10 minutes and is
	// polled at most every 5 seconds (RFC 8628).
	DeviceTTL      = 10 * time.Minute
	DeviceInterval = 5 * time.Second
	// DeviceGrantType is RFC 8628's grant type.
	DeviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	Scope           = "gateway"

	refreshPrefix = "kiff_ort_"
)

// Reasons a grant ends, recorded on it.
const (
	EndedRevoked  = "revoked"
	EndedIdle     = "idle"
	EndedRoleLost = "granter_lost_role"
	EndedReplaced = "replaced"
	EndedReplay   = "credential_replayed"
	// EndedFailedExchange: the code was presented and failed a check.
	EndedFailedExchange = "failed_exchange"
	// EndedLinkGone: the connect link was revoked, or its agent archived.
	EndedLinkGone = "link_gone"
)

// Config is where the parts live.
type Config struct {
	// Issuer is the authorization server, e.g. https://api.kiff.dev.
	Issuer string
	// Resource is the gateway, e.g. https://mcp.kiff.dev/mcp. Tokens are
	// valid for it only.
	Resource string
	// ConsentURL is the consent page, e.g. https://app.kiff.dev/oauth/consent.
	ConsentURL string
	// RedirectHosts are the https hosts a client may redirect to.
	// Loopback (127.0.0.1, [::1], localhost) on any port is always
	// allowed, for clients such as Claude Code (RFC 8252 7.3).
	RedirectHosts []string
	// MetadataHosts are the https hosts whose client ID metadata documents
	// KIFF accepts as a client id (CIMD), e.g. claude.ai. Empty: none, and
	// clients register (DCR).
	MetadataHosts []string
	// SharedNets are networks whose addresses many people share, such as
	// Anthropic's outbound range. Each address in them gets a larger
	// registration budget; every other address keeps the default.
	SharedNets []netip.Prefix
}

// Server serves the authorization server and the consent API.
type Server struct {
	store  *Store
	keys   auth.APIKeyStore
	cfg    Config
	logger *slog.Logger
	now    func() time.Time
	// granterActive reports whether the person who consented still holds
	// a role that may consent. Nil: always.
	granterActive func(ctx context.Context, tenantID, subject string) bool
	// agentKnown reports whether an agent is one of the account's.
	agentKnown func(ctx context.Context, tenantID, agentID string) (bool, error)
	// agentCreate creates an agent record (A5).
	agentCreate func(ctx context.Context, tenantID, agentID, createdBy string) error
	// afterPendingDevice is a test hook, called in deviceApprove once the
	// code is read as pending. Nil in production.
	afterPendingDevice func()
	// fetch is the HTTP client for metadata documents.
	fetch *http.Client

	mu       sync.Mutex
	register map[string][]time.Time  // per-IP registration times
	docs     map[string]cachedClient // metadata clients, by client id
}

// Option configures a Server.
type Option func(*Server)

// WithClock sets the clock.
func WithClock(now func() time.Time) Option { return func(s *Server) { s.now = now } }

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(s *Server) { s.logger = l } }

// WithHTTPClient sets the client that fetches metadata documents.
func WithHTTPClient(c *http.Client) Option { return func(s *Server) { s.fetch = c } }

// WithAgentCheck sets how consent checks that the chosen agent is one of
// the account's agents. Without it, consent is refused.
func WithAgentCheck(f func(ctx context.Context, tenantID, agentID string) (bool, error)) Option {
	return func(s *Server) { s.agentKnown = f }
}

// ErrAgentExists: the account already uses that agent name.
var ErrAgentExists = errors.New("oauth: agent already exists")

// WithAgentCreator sets how consent creates "a new agent for this app"
// on the generic URL (Amendment 1, A5). It returns ErrAgentExists for a
// name the account already uses. Without it, that option is refused.
func WithAgentCreator(f func(ctx context.Context, tenantID, agentID, createdBy string) error) Option {
	return func(s *Server) { s.agentCreate = f }
}

// WithGranterActive sets the granter check.
func WithGranterActive(f func(ctx context.Context, tenantID, subject string) bool) Option {
	return func(s *Server) { s.granterActive = f }
}

// MemberGranterActive is the granter check for accounts with
// memberships: active, and admin or editor.
func MemberGranterActive(latest func(ctx context.Context, tenantID, subject string) (membership.Member, error)) func(context.Context, string, string) bool {
	return func(ctx context.Context, tenantID, subject string) bool {
		m, err := latest(ctx, tenantID, subject)
		if errors.Is(err, membership.ErrNotFound) {
			// An account from before memberships: its one person.
			return true
		}
		return err == nil && m.Active() && (m.Role == membership.RoleAdmin || m.Role == membership.RoleEditor)
	}
}

// New returns a Server.
func New(store *Store, keys auth.APIKeyStore, cfg Config, opts ...Option) *Server {
	s := &Server{store: store, keys: keys, cfg: cfg, logger: slog.Default(),
		now: func() time.Time { return time.Now().UTC() }, register: map[string][]time.Time{},
		docs: map[string]cachedClient{},
		// A metadata document is served as is: a redirect is refused.
		fetch: &http.Client{Timeout: metadataTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// RegisterPublic mounts the unauthenticated endpoints.
func (s *Server) RegisterPublic(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.metadata)
	mux.HandleFunc("POST /oauth/register", s.registerClient)
	mux.HandleFunc("GET /oauth/authorize", s.authorize)
	mux.HandleFunc("POST /oauth/token", s.token)
	mux.HandleFunc("POST /oauth/revoke", s.revoke)
	mux.HandleFunc("POST /oauth/device_authorization", s.deviceAuthorization)
}

// Register mounts the consent API, behind the auth middleware.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/oauth/requests/{id}", s.requestInfo)
	mux.HandleFunc("POST /v1/oauth/consent", s.consent)
	mux.HandleFunc("POST /v1/oauth/deny", s.deny)
	mux.HandleFunc("POST /v1/me/agents/{id}/connect-links", s.createLink)
	mux.HandleFunc("GET /v1/me/agents/{id}/connect-links", s.listLinks)
	mux.HandleFunc("POST /v1/oauth/connect-links/{id}/revoke", s.revokeLink)
	mux.HandleFunc("GET /v1/oauth/device/{code}", s.deviceInfo)
	mux.HandleFunc("POST /v1/oauth/device/approve", s.deviceApprove)
	mux.HandleFunc("POST /v1/oauth/device/deny", s.deviceDeny)
	mux.HandleFunc("GET /v1/oauth/connections", s.connections)
	mux.HandleFunc("POST /v1/oauth/grants/{id}/revoke", s.revokeGrant)
}

func (s *Server) metadata(w http.ResponseWriter, _ *http.Request) {
	iss := strings.TrimRight(s.cfg.Issuer, "/")
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                iss,
		"authorization_endpoint":                iss + "/oauth/authorize",
		"token_endpoint":                        iss + "/oauth/token",
		"registration_endpoint":                 iss + "/oauth/register",
		"revocation_endpoint":                   iss + "/oauth/revoke",
		"device_authorization_endpoint":         iss + "/oauth/device_authorization",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token", DeviceGrantType},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{Scope},
		// Claude uses a metadata document only when this is true and
		// "none" is an auth method above; otherwise it registers.
		"client_id_metadata_document_supported": len(s.cfg.MetadataHosts) > 0,
	})
}

// --- registration (RFC 7591) ---

// registerPerHour limits client registrations and device-code requests per
// IP, and device-code lookups per person (allowRegister, keyed by prefix).
// sharedRegisterPerHour is the registration budget of each address in a
// shared network (Config.SharedNets), where one address carries many
// people's connections. Limits stay per address, so one address running
// out never blocks another.
const (
	registerPerHour       = 20
	sharedRegisterPerHour = 600
)

func (s *Server) registerClient(w http.ResponseWriter, r *http.Request) {
	ip, limit := clientIP(r), registerPerHour
	if s.shared(ip) {
		limit = sharedRegisterPerHour
	}
	if !s.allowRegister(ip, limit) {
		oauthError(w, http.StatusTooManyRequests, "slow_down", "too many registrations from this address; try again later")
		return
	}
	var req struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
		GrantTypes   []string `json:"grant_types"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "the body must be JSON client metadata")
		return
	}
	name := strings.TrimSpace(req.ClientName)
	if name == "" {
		name = "MCP client"
	}
	if len(name) > 100 || strings.ContainsFunc(name, unicode.IsControl) {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "client_name must be at most 100 characters")
		return
	}
	// A client that only uses the device flow (D2b) has no redirect.
	deviceOnly := len(req.RedirectURIs) == 0 && slices.Contains(req.GrantTypes, DeviceGrantType)
	if !deviceOnly && (len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 5) {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "give one to five redirect_uris")
		return
	}
	if req.RedirectURIs == nil {
		req.RedirectURIs = []string{}
	}
	for _, u := range req.RedirectURIs {
		if !s.allowedRedirect(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect URI not allowed: "+u)
			return
		}
	}
	now := s.now()
	c := Client{ID: "kc_" + randomToken(), Name: name, RedirectURIs: req.RedirectURIs, CreatedAt: now}
	if err := s.store.CreateClient(r.Context(), c); err != nil {
		s.fail(w, r, "register client", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  c.ID,
		"client_id_issued_at":        now.Unix(),
		"client_name":                c.Name,
		"redirect_uris":              c.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token", DeviceGrantType},
		"response_types":             []string{"code"},
	})
}

// shared reports whether ip is in one of the shared networks.
func (s *Server) shared(ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	return slices.ContainsFunc(s.cfg.SharedNets, func(p netip.Prefix) bool { return p.Contains(a) })
}

func (s *Server) allowRegister(ip string, limit int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cut := s.now().Add(-time.Hour)
	for other, ts := range s.register {
		if other != ip && (len(ts) == 0 || ts[len(ts)-1].Before(cut)) {
			delete(s.register, other)
		}
	}
	recent := slices.DeleteFunc(s.register[ip], func(t time.Time) bool { return t.Before(cut) })
	if len(recent) >= limit {
		s.register[ip] = recent
		return false
	}
	s.register[ip] = append(recent, s.now())
	return true
}

// allowedRedirect: https to a configured host, or http to loopback on
// any port. No fragment, no credentials.
func (s *Server) allowedRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.User != nil || u.Host == "" {
		return false
	}
	host := u.Hostname()
	switch u.Scheme {
	case "https":
		return slices.Contains(s.cfg.RedirectHosts, strings.ToLower(host))
	case "http":
		return loopback(host)
	}
	return false
}

// redirectRegistered: the redirect is one the client registered or, for
// http loopback, one that differs from it only in the port (RFC 8252 7.3).
// Native clients such as Claude Code listen on a port chosen per session,
// and a metadata document lists loopback redirects without one.
func redirectRegistered(registered []string, raw string) bool {
	if slices.Contains(registered, raw) {
		return true
	}
	got, err := url.Parse(raw)
	if err != nil || got.Scheme != "http" || got.User != nil || got.Fragment != "" || !loopback(got.Hostname()) {
		return false
	}
	return slices.ContainsFunc(registered, func(r string) bool {
		u, err := url.Parse(r)
		return err == nil && u.Scheme == "http" && u.Hostname() == got.Hostname() &&
			u.Path == got.Path && u.RawQuery == got.RawQuery
	})
}

func loopback(host string) bool { return host == "127.0.0.1" || host == "::1" || host == "localhost" }

// --- client ID metadata documents (CIMD) ---

const (
	metadataTimeout = 5 * time.Second
	metadataMaxSize = 16 << 10
	// metadataTTL is how long a fetched document is reused.
	metadataTTL = 10 * time.Minute
)

// errBadMetadata: the client id is a URL KIFF does not accept, or its
// document is missing or invalid.
var errBadMetadata = errors.New("oauth: client metadata document not accepted")

type cachedClient struct {
	c  Client
	at time.Time
}

// client returns the client a request names: a registered one (DCR) or,
// when the id is an https URL on a host in MetadataHosts, the client its
// metadata document describes. A metadata client is then stored like a
// registered one, so consent, codes, tokens and grants treat both alike.
func (s *Server) client(ctx context.Context, id string) (Client, error) {
	if !strings.HasPrefix(id, "https://") {
		return s.store.Client(ctx, id)
	}
	s.mu.Lock()
	hit, ok := s.docs[id]
	s.mu.Unlock()
	if ok && s.now().Sub(hit.at) < metadataTTL {
		return hit.c, nil
	}
	c, err := s.fetchMetadata(ctx, id)
	if err != nil {
		return Client{}, err
	}
	if err := s.store.UpsertClient(ctx, c); err != nil {
		return Client{}, err
	}
	s.mu.Lock()
	s.docs[id] = cachedClient{c: c, at: s.now()}
	s.mu.Unlock()
	return c, nil
}

// fetchMetadata fetches and checks a client ID metadata document: its
// client_id must be the URL it was fetched from, it must be a public
// client, and every redirect must be one KIFF allows for any client.
func (s *Server) fetchMetadata(ctx context.Context, id string) (Client, error) {
	u, err := url.Parse(id)
	if err != nil || len(id) > 512 || u.Scheme != "https" || u.User != nil || u.Port() != "" ||
		u.RawQuery != "" || u.Fragment != "" || u.Path == "" || u.Path == "/" ||
		!slices.Contains(s.cfg.MetadataHosts, strings.ToLower(u.Hostname())) {
		return Client{}, errBadMetadata
	}
	ctx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, id, nil)
	if err != nil {
		return Client{}, errBadMetadata
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.fetch.Do(req)
	if err != nil {
		return Client{}, fmt.Errorf("%w: %v", errBadMetadata, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Client{}, fmt.Errorf("%w: status %d", errBadMetadata, resp.StatusCode)
	}
	var doc struct {
		ClientID     string   `json:"client_id"`
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, metadataMaxSize)).Decode(&doc); err != nil {
		return Client{}, fmt.Errorf("%w: %v", errBadMetadata, err)
	}
	name := strings.TrimSpace(doc.ClientName)
	if name == "" {
		name = u.Hostname()
	}
	switch {
	case doc.ClientID != id, doc.AuthMethod != "none",
		len(doc.RedirectURIs) == 0, len(doc.RedirectURIs) > 10,
		len(name) > 100, strings.ContainsFunc(name, unicode.IsControl):
		return Client{}, errBadMetadata
	}
	for _, r := range doc.RedirectURIs {
		if !s.allowedRedirect(r) {
			return Client{}, errBadMetadata
		}
	}
	return Client{ID: id, Name: name, RedirectURIs: doc.RedirectURIs, CreatedAt: s.now()}, nil
}

// --- authorization ---

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, err := s.client(r.Context(), q.Get("client_id"))
	if err != nil {
		if !errors.Is(err, ErrNotFound) && !errors.Is(err, errBadMetadata) {
			s.logger.Warn("oauth: client lookup failed", "err", err)
		}
		http.Error(w, "Unknown client. Connect again from your app.", http.StatusBadRequest)
		return
	}
	redirect := q.Get("redirect_uri")
	if redirect == "" && len(c.RedirectURIs) == 1 {
		redirect = c.RedirectURIs[0]
	}
	if !redirectRegistered(c.RedirectURIs, redirect) {
		// Never redirect to an address the client did not register.
		http.Error(w, "This redirect address is not registered for the client.", http.StatusBadRequest)
		return
	}
	state := q.Get("state")
	bad := func(code, desc string) {
		http.Redirect(w, r, errorRedirect(redirect, state, code, desc), http.StatusFound)
	}
	switch {
	case q.Get("response_type") != "code":
		bad("unsupported_response_type", "only response_type=code is supported")
		return
	case q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256":
		bad("invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	case q.Get("scope") != "" && q.Get("scope") != Scope:
		bad("invalid_scope", "the only scope is "+Scope)
		return
	}
	// The resource is the generic gateway URL, or a connect link's URL,
	// which names the agent (Amendment 1, A2).
	resource, linkID := q.Get("resource"), ""
	if resource == "" {
		resource = s.cfg.Resource
	}
	if resource != s.cfg.Resource {
		id, ok := s.linkFromResource(resource)
		if !ok {
			bad("invalid_target", "tokens are issued for KIFF gateway URLs only")
			return
		}
		if _, err := s.linkLive(r.Context(), id); err != nil {
			bad("invalid_target", "this connect link is no longer valid; ask for a new one")
			return
		}
		linkID = id
	}
	now := s.now()
	req := Request{ID: "kr_" + randomToken(), ClientID: c.ID, RedirectURI: redirect, State: state,
		CodeChallenge: q.Get("code_challenge"), Resource: resource, LinkID: linkID, CreatedAt: now, ExpiresAt: now.Add(RequestTTL)}
	if err := s.store.CreateRequest(r.Context(), req); err != nil {
		s.fail(w, r, "create request", err)
		return
	}
	http.Redirect(w, r, s.cfg.ConsentURL+"?request="+url.QueryEscape(req.ID), http.StatusFound)
}

// RequestInfo is what the consent page shows.
type RequestInfo struct {
	RequestID    string `json:"request_id"`
	ClientName   string `json:"client_name"`
	RedirectHost string `json:"redirect_host"`
	// Verified is always false in v1: KIFF verifies no client yet, so
	// the page marks every client as unverified.
	Verified        bool   `json:"verified"`
	PreviousAgentID string `json:"previous_agent_id,omitempty"`
	CanConsent      bool   `json:"can_consent"`
	// AgentID is set when the request came through a connect link: the
	// agent is fixed, and the page only asks whether to allow it (A3).
	AgentID string `json:"agent_id,omitempty"`
	// WrongAccount: the link belongs to another account than the
	// person's; the page refuses.
	WrongAccount bool `json:"wrong_account,omitempty"`
}

func (s *Server) requestInfo(w http.ResponseWriter, r *http.Request) {
	id, ok := person(w, r)
	if !ok {
		return
	}
	req, c, err := s.pending(r.Context(), r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this connection request is unknown or has expired; start again from your app"})
		return
	}
	info := RequestInfo{RequestID: req.ID, ClientName: c.Name, RedirectHost: redirectHost(req.RedirectURI), CanConsent: canConsent(id)}
	if req.LinkID != "" {
		l, err := s.linkLive(r.Context(), req.LinkID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "this connect link is no longer valid; ask for a new one"})
			return
		}
		if l.TenantID != id.Tenant {
			info.WrongAccount, info.CanConsent = true, false
		} else {
			info.AgentID = l.AgentID
		}
		writeJSON(w, http.StatusOK, info)
		return
	}
	if g, err := s.store.LatestGrant(r.Context(), id.Tenant, c.Name); err == nil {
		info.PreviousAgentID = g.AgentID
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) pending(ctx context.Context, requestID string) (Request, Client, error) {
	req, err := s.store.Request(ctx, requestID, s.now())
	if err != nil {
		return req, Client{}, err
	}
	c, err := s.store.Client(ctx, req.ClientID)
	return req, c, err
}

// ConsentResponse tells the page where to send the browser.
type ConsentResponse struct {
	RedirectTo string `json:"redirect_to"`
	GrantID    string `json:"grant_id,omitempty"`
}

func (s *Server) consent(w http.ResponseWriter, r *http.Request) {
	id, ok := person(w, r)
	if !ok {
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
		// AgentID is an existing agent; NewAgent asks to create one (A5).
		// Both are ignored for a request through a connect link.
		AgentID  string `json:"agent_id"`
		NewAgent string `json:"new_agent"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	ctx := r.Context()
	req, c, err := s.pending(ctx, body.RequestID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this connection request is unknown or has expired; start again from your app"})
		return
	}
	g, ok := s.approve(w, r, id, approval{ClientID: c.ID, ClientName: c.Name, Resource: req.Resource, LinkID: req.LinkID,
		AgentID: body.AgentID, NewAgent: body.NewAgent, Method: "sign-in"},
		func(now time.Time) (bool, error) { return s.store.UseRequest(ctx, req.ID, now) })
	if !ok {
		return
	}
	now := g.CreatedAt
	// A pending grant: its key is minted when the code is exchanged, so a
	// code that is never redeemed, or fails, leaves no key behind.
	code := randomToken()
	if err := s.store.CreateGrantWithCode(ctx, g, Code{Hash: hashOf(code), TenantID: id.Tenant, GrantID: g.ID,
		ClientID: c.ID, RedirectURI: req.RedirectURI, CodeChallenge: req.CodeChallenge, ExpiresAt: now.Add(CodeTTL)}); err != nil {
		s.fail(w, r, "create grant", err)
		return
	}
	u, _ := url.Parse(req.RedirectURI)
	q := u.Query()
	q.Set("code", code)
	if req.State != "" {
		q.Set("state", req.State)
	}
	q.Set("iss", strings.TrimRight(s.cfg.Issuer, "/"))
	u.RawQuery = q.Encode()
	writeJSON(w, http.StatusOK, ConsentResponse{RedirectTo: u.String(), GrantID: g.ID})
}

// approval is what a person is asked to allow: a client, the resource it
// asked for, and the agent (fixed by a connect link, or chosen).
type approval struct {
	ClientID, ClientName, Resource, LinkID string
	AgentID, NewAgent                      string
	Method                                 string // "sign-in" or "code"
}

// approve applies the consent rules shared by sign-in and the device flow
// (D2, Amendment 1): only an admin or editor; through a link the agent is
// the link's and the link must be live and in the person's account;
// otherwise an existing agent of the account, or a new one created only
// after claim wins. It returns the pending grant, not yet stored. On
// failure it has written the response.
func (s *Server) approve(w http.ResponseWriter, r *http.Request, id auth.Identity, a approval, claim func(time.Time) (bool, error)) (Grant, bool) {
	ctx := r.Context()
	if !canConsent(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only an admin or editor of this account can connect an agent"})
		return Grant{}, false
	}
	// The key is bound only to an agent the account has; the API checks
	// it, whatever page sent the request.
	if s.agentKnown == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agents cannot be checked on this deployment"})
		return Grant{}, false
	}
	var agentID, newAgent string
	switch {
	case a.LinkID != "":
		// Through a connect link the agent is the link's (A3). Checked
		// again here: the link may have been revoked since the page
		// loaded (A4).
		l, err := s.linkLive(ctx, a.LinkID)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "this connect link is no longer valid; ask for a new one"})
			return Grant{}, false
		}
		if l.TenantID != id.Tenant {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "this connect link belongs to another account"})
			return Grant{}, false
		}
		agentID = l.AgentID
	case strings.TrimSpace(a.NewAgent) != "":
		// A new agent for this app (A5). Checked here, created only after
		// the request is claimed below, so two submissions of one request
		// cannot both create an agent.
		newAgent = strings.TrimSpace(a.NewAgent)
		if len(newAgent) > 200 || strings.ContainsFunc(newAgent, unicode.IsControl) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the agent name must be 1 to 200 characters"})
			return Grant{}, false
		}
		if s.agentCreate == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agents cannot be created on this deployment"})
			return Grant{}, false
		}
		// A name already in use is refused before the request is claimed,
		// so the person can pick another without starting over.
		switch used, err := s.agentKnown(ctx, id.Tenant, newAgent); {
		case err != nil:
			s.fail(w, r, "check agent", err)
			return Grant{}, false
		case used:
			writeJSON(w, http.StatusConflict, map[string]string{"error": "this account already has an agent named " + newAgent + "; choose it from the list, or pick another name"})
			return Grant{}, false
		}
		agentID = newAgent
	default:
		agentID = strings.TrimSpace(a.AgentID)
		if agentID == "" || len(agentID) > 200 || strings.ContainsFunc(agentID, unicode.IsControl) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "choose an agent"})
			return Grant{}, false
		}
		switch known, err := s.agentKnown(ctx, id.Tenant, agentID); {
		case err != nil:
			s.fail(w, r, "check agent", err)
			return Grant{}, false
		case !known:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no agent named " + agentID + " on this account; add it first"})
			return Grant{}, false
		}
	}
	now := s.now()
	if won, err := claim(now); err != nil || !won {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this connection request was already answered"})
		return Grant{}, false
	}
	// Only the caller that claimed the request creates the agent (A5).
	// Not one transaction with the grant: a failure after this leaves an
	// agent with no key, never a key without its agent.
	if newAgent != "" {
		switch err := s.agentCreate(ctx, id.Tenant, newAgent, id.Subject); {
		case errors.Is(err, ErrAgentExists):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "this account already has an agent named " + newAgent + "; start again from your app and choose it"})
			return Grant{}, false
		case err != nil:
			s.fail(w, r, "create agent", err)
			return Grant{}, false
		}
	}
	return Grant{ID: "kg_" + randomToken(), TenantID: id.Tenant, AgentID: agentID, ClientID: a.ClientID, ClientName: a.ClientName,
		GrantedBy: id.Subject, Resource: a.Resource, LinkID: a.LinkID, Method: a.Method, CreatedAt: now, LastUsedAt: now}, true
}

// deny declines a request. Anyone signed in may decline.
func (s *Server) deny(w http.ResponseWriter, r *http.Request) {
	if _, ok := person(w, r); !ok {
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	req, _, err := s.pending(r.Context(), body.RequestID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this connection request is unknown or has expired"})
		return
	}
	if won, err := s.store.UseRequest(r.Context(), req.ID, s.now()); err != nil || !won {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this connection request was already answered"})
		return
	}
	writeJSON(w, http.StatusOK, ConsentResponse{RedirectTo: errorRedirect(req.RedirectURI, req.State, "access_denied", "the person declined")})
}

// person is a signed-in person (not a key) on an account.
func person(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.APIKey || id.Method != auth.MethodSession || id.Tenant == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "connecting an agent needs a signed-in person"})
		return id, false
	}
	return id, true
}

// canConsent: admin or editor. Without memberships, the
// account's own manager.
func canConsent(id auth.Identity) bool {
	if id.MemberRole != "" {
		return id.MemberRole == membership.RoleAdmin || id.MemberRole == membership.RoleEditor
	}
	return id.ManagesAccount()
}

// --- tokens ---

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "the body must be form-encoded")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.exchangeCode(w, r)
	case "refresh_token":
		s.refresh(w, r)
	case DeviceGrantType:
		s.deviceToken(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
	}
}

func (s *Server) exchangeCode(w http.ResponseWriter, r *http.Request) {
	ctx, f, now := r.Context(), r.PostForm, s.now()
	c, first, err := s.store.UseCode(ctx, hashOf(f.Get("code")), now)
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown code")
		return
	}
	g, gerr := s.store.Grant(ctx, c.GrantID)
	if !first {
		// A replayed code: end the grant it produced (RFC 6749 4.1.2).
		if gerr == nil {
			s.endGrant(ctx, g, EndedReplay)
		}
		oauthError(w, http.StatusBadRequest, "invalid_grant", "this code was already used")
		return
	}
	if gerr != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown code")
		return
	}
	// The code is used now. Any failure below ends its grant: a wrong
	// verifier is what a stolen code looks like, and the person can
	// connect again.
	refuse := func(code, desc string) {
		s.endGrant(ctx, g, EndedFailedExchange)
		oauthError(w, http.StatusBadRequest, code, desc)
	}
	switch {
	case now.After(c.ExpiresAt):
		refuse("invalid_grant", "the code has expired")
		return
	case f.Get("client_id") != c.ClientID, f.Get("redirect_uri") != c.RedirectURI:
		refuse("invalid_grant", "client_id or redirect_uri does not match")
		return
	case !verifyPKCE(f.Get("code_verifier"), c.CodeChallenge):
		refuse("invalid_grant", "code_verifier does not match")
		return
	case f.Get("resource") != "" && f.Get("resource") != g.Resource:
		// The grant's own resource: the generic URL, or its connect link's.
		refuse("invalid_target", "this code was issued for "+g.Resource)
		return
	case g.EndedAt != nil || g.RedeemedAt != nil:
		refuse("invalid_grant", "this connection has ended")
		return
	}
	s.redeem(w, r, g)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	ctx, f, now := r.Context(), r.PostForm, s.now()
	raw := f.Get("refresh_token")
	if !strings.HasPrefix(raw, refreshPrefix) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown refresh token")
		return
	}
	t, used, err := s.store.Token(ctx, hashOf(raw))
	if err != nil || t.Kind != "refresh" {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown refresh token")
		return
	}
	g, err := s.store.Grant(ctx, t.GrantID)
	if err != nil || g.ClientID != f.Get("client_id") {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown refresh token")
		return
	}
	if f.Get("resource") != "" && f.Get("resource") != g.Resource {
		oauthError(w, http.StatusBadRequest, "invalid_target", "this connection was issued for "+g.Resource)
		return
	}
	if g.EndedAt != nil {
		// Ending a connection marks its tokens used; say what happened
		// rather than reporting reuse.
		oauthError(w, http.StatusBadRequest, "invalid_grant", "this connection has ended; connect again")
		return
	}
	won := false
	if !used {
		won, err = s.store.UseToken(ctx, t.Hash, now)
		if err != nil {
			s.fail(w, r, "use refresh token", err)
			return
		}
	}
	if !won {
		// Reuse of a rotated refresh token: someone else may hold it.
		s.endGrant(ctx, g, EndedReplay)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "this refresh token was already used; connect again")
		return
	}
	if err := s.live(ctx, g); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "this connection has ended; connect again")
		return
	}
	s.issue(w, r, g)
}

// keyLabel names how a grant's key was made, for the Keys tab.
func keyLabel(method string) string {
	if method == "code" {
		return " (connected with a code)"
	}
	return " (connected with sign-in)"
}

// redeem connects a pending grant on its first exchange: it mints the
// grant's own key (agent-bound, gateway_agent only), records it, replaces
// that client's previous connection for the agent, and issues tokens.
func (s *Server) redeem(w http.ResponseWriter, r *http.Request, g Grant) {
	ctx, now := r.Context(), s.now()
	// Mint the grant's own key now: agent-bound, gateway_agent only.
	gen, err := auth.NewAPIKey(g.TenantID, now)
	if err != nil {
		s.fail(w, r, "mint key", err)
		return
	}
	key := auth.APIKeyRecord{
		ID: ulid.MustNew(ulid.Timestamp(now), ulid.DefaultEntropy()).String(), Tenant: g.TenantID, Subject: g.GrantedBy,
		Label: g.ClientName + keyLabel(g.Method), Roles: []string{auth.RoleGatewayAgent}, AgentID: g.AgentID,
		KeyHash: gen.Hash, CreatedAt: now,
	}
	if err := s.keys.Insert(ctx, key); err != nil {
		s.fail(w, r, "insert key", err)
		return
	}
	won, err := s.store.RedeemGrant(ctx, g.ID, key.ID, gen.Hash, now)
	if err != nil || !won {
		_ = s.keys.Revoke(ctx, key.ID, now)
		if err != nil {
			s.fail(w, r, "redeem grant", err)
			return
		}
		oauthError(w, http.StatusBadRequest, "invalid_grant", "this connection has ended")
		return
	}
	g.KeyID, g.KeyHash, g.RedeemedAt, g.LastUsedAt = key.ID, gen.Hash, &now, now
	// Reconnecting as the same agent replaces that client's connection,
	// once the new one is actually connected.
	if old, err := s.store.LiveGrants(ctx, g.TenantID, g.ClientName, g.AgentID); err == nil {
		for _, o := range old {
			if o.ID != g.ID {
				s.endGrant(ctx, o, EndedReplaced)
			}
		}
	}
	if err := s.live(ctx, g); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "this connection has ended")
		return
	}
	s.issue(w, r, g)
}

func (s *Server) issue(w http.ResponseWriter, r *http.Request, g Grant) {
	now := s.now()
	access := auth.OAuthAccessTokenPrefix + randomToken()
	refresh := refreshPrefix + randomToken()
	exp := now.Add(AccessTTL)
	if err := s.store.InsertTokens(r.Context(),
		Token{Hash: hashOf(access), TenantID: g.TenantID, GrantID: g.ID, Kind: "access", ExpiresAt: &exp},
		Token{Hash: hashOf(refresh), TenantID: g.TenantID, GrantID: g.ID, Kind: "refresh"}); err != nil {
		s.fail(w, r, "insert tokens", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(AccessTTL.Seconds()),
		"refresh_token": refresh,
		"scope":         Scope,
	})
}

// revoke (RFC 7009) ends the grant behind either token. Always 200.
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err == nil {
		if t, _, err := s.store.Token(r.Context(), hashOf(r.PostForm.Get("token"))); err == nil {
			if g, err := s.store.Grant(r.Context(), t.GrantID); err == nil {
				s.endGrant(r.Context(), g, EndedRevoked)
			}
		}
	}
	w.WriteHeader(http.StatusOK)
}

// ResolveAccessToken implements auth.OAuthTokenResolver.
func (s *Server) ResolveAccessToken(ctx context.Context, raw string) (auth.OAuthGrant, error) {
	t, _, err := s.store.Token(ctx, hashOf(raw))
	if err != nil || t.Kind != "access" || t.ExpiresAt == nil || !s.now().Before(*t.ExpiresAt) {
		return auth.OAuthGrant{}, errors.New("oauth: unknown or expired access token")
	}
	g, err := s.store.Grant(ctx, t.GrantID)
	if err != nil || !s.grantResourceValid(g) {
		return auth.OAuthGrant{}, errors.New("oauth: token is not for this resource")
	}
	if err := s.live(ctx, g); err != nil {
		return auth.OAuthGrant{}, err
	}
	if err := s.store.TouchGrant(ctx, g.ID, s.now()); err != nil {
		s.logger.WarnContext(ctx, "oauth: grant touch failed", slog.String("grant", g.ID), slog.String("err", err.Error()))
	}
	return auth.OAuthGrant{GrantID: g.ID, KeyHash: g.KeyHash, Resource: g.Resource}, nil
}

// live ends a grant that has idled out or whose granter lost the role,
// and refuses an ended one.
func (s *Server) live(ctx context.Context, g Grant) error {
	if g.EndedAt != nil {
		return errors.New("oauth: grant has ended")
	}
	if g.RedeemedAt == nil || g.KeyID == "" {
		return errors.New("oauth: grant is not connected")
	}
	// A grant made through a connect link ends with the link: revoked, or
	// its agent archived (Amendment 1, A1, A4).
	if g.LinkID != "" {
		if _, err := s.linkLive(ctx, g.LinkID); err != nil {
			s.endGrant(ctx, g, EndedLinkGone)
			return errors.New("oauth: the connect link is no longer valid")
		}
	}
	if s.now().Sub(g.LastUsedAt) > IdleLimit {
		s.endGrant(ctx, g, EndedIdle)
		return errors.New("oauth: grant idled out")
	}
	// Revoking the key in KIFF Cloud ends the grant too.
	if rec, err := s.keys.GetByHash(ctx, g.KeyHash); errors.Is(err, auth.ErrAPIKeyRevoked) || errors.Is(err, auth.ErrAPIKeyNotFound) {
		s.endGrant(ctx, g, EndedRevoked)
		return errors.New("oauth: the agent's key was revoked")
	} else if err == nil && rec.IsExpired(s.now()) {
		// An expired key ends the grant as a revoked one does.
		s.endGrant(ctx, g, EndedRevoked)
		return errors.New("oauth: the agent's key has expired")
	}
	if s.granterActive != nil && !s.granterActive(ctx, g.TenantID, g.GrantedBy) {
		s.endGrant(ctx, g, EndedRoleLost)
		return errors.New("oauth: the person who connected this agent no longer may")
	}
	return nil
}

// endGrant ends the grant and revokes the key it owns. Every way a grant
// ends comes through here.
func (s *Server) endGrant(ctx context.Context, g Grant, reason string) {
	now := s.now()
	if _, err := s.store.EndGrant(ctx, g.ID, reason, now); err != nil {
		s.logger.ErrorContext(ctx, "oauth: end grant failed", slog.String("grant", g.ID), slog.String("err", err.Error()))
	}
	if g.KeyID == "" {
		return // pending: no key was minted
	}
	if err := s.keys.Revoke(ctx, g.KeyID, now); err != nil && !errors.Is(err, auth.ErrAPIKeyNotFound) && !errors.Is(err, auth.ErrAPIKeyRevoked) {
		s.logger.ErrorContext(ctx, "oauth: revoke grant key failed", slog.String("grant", g.ID), slog.String("err", err.Error()))
	}
}

// --- helpers ---

func verifyPKCE(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

func hashOf(s string) [32]byte { return sha256.Sum256([]byte(s)) }

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func errorRedirect(redirect, state, code, desc string) string {
	u, _ := url.Parse(redirect)
	q := u.Query()
	q.Set("error", code)
	q.Set("error_description", desc)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func redirectHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

func clientIP(r *http.Request) string {
	// The last entry is the one our proxy (Caddy) appended; earlier ones
	// are whatever the client sent.
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		parts := strings.Split(f, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.logger.ErrorContext(r.Context(), "oauth: "+what, slog.String("err", err.Error()))
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
}

// --- connect links ---

// LinkView is a connect link as the dashboard shows it.
type LinkView struct {
	ID        string     `json:"id"`
	AgentID   string     `json:"agent_id"`
	URL       string     `json:"url"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// gatewayOrigin is the scheme and host of the gateway resource.
func (s *Server) gatewayOrigin() string {
	u, err := url.Parse(s.cfg.Resource)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// LinkURL is a link's gateway URL, the OAuth resource that names its
// agent.
func (s *Server) LinkURL(id string) string { return s.gatewayOrigin() + "/a/" + id + "/mcp" }

// linkFromResource reads the link id from a link's URL.
func (s *Server) linkFromResource(resource string) (string, bool) {
	prefix := s.gatewayOrigin() + "/a/"
	if !strings.HasPrefix(resource, prefix) || !strings.HasSuffix(resource, "/mcp") {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(resource, prefix), "/mcp")
	return id, ValidLinkID(id)
}

// ValidLinkID: "cl_" and base64url characters.
func ValidLinkID(id string) bool {
	if !strings.HasPrefix(id, "cl_") || len(id) < 10 || len(id) > 64 {
		return false
	}
	for _, r := range id[3:] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// linkLive returns the link when it is not revoked and its agent is still
// one of the account's (not archived, within the naming rule).
func (s *Server) linkLive(ctx context.Context, id string) (Link, error) {
	l, err := s.store.Link(ctx, id)
	if err != nil {
		return l, err
	}
	if l.RevokedAt != nil {
		return l, errors.New("oauth: connect link revoked")
	}
	if s.agentKnown == nil {
		return l, errors.New("oauth: agents cannot be checked")
	}
	known, err := s.agentKnown(ctx, l.TenantID, l.AgentID)
	if err != nil {
		return l, err
	}
	if !known {
		return l, errors.New("oauth: the link's agent is archived or unknown")
	}
	return l, nil
}

// grantResourceValid: the generic URL, or the URL of the grant's own link.
func (s *Server) grantResourceValid(g Grant) bool {
	if g.LinkID == "" {
		return g.Resource == s.cfg.Resource
	}
	return g.Resource == s.LinkURL(g.LinkID)
}

func (s *Server) linkView(l Link) LinkView {
	return LinkView{ID: l.ID, AgentID: l.AgentID, URL: s.LinkURL(l.ID), CreatedBy: l.CreatedBy, CreatedAt: l.CreatedAt, RevokedAt: l.RevokedAt}
}

func (s *Server) createLink(w http.ResponseWriter, r *http.Request) {
	id, ok := person(w, r)
	if !ok {
		return
	}
	if !canConsent(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only an admin or editor of this account can create a connect link"})
		return
	}
	agentID := strings.TrimSpace(r.PathValue("id"))
	if s.agentKnown == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agents cannot be checked on this deployment"})
		return
	}
	switch known, err := s.agentKnown(r.Context(), id.Tenant, agentID); {
	case err != nil:
		s.fail(w, r, "check agent", err)
		return
	case !known:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no agent named " + agentID + " on this account"})
		return
	}
	l := Link{ID: "cl_" + randomToken()[:22], TenantID: id.Tenant, AgentID: agentID, CreatedBy: id.Subject, CreatedAt: s.now()}
	if err := s.store.CreateLink(r.Context(), l); err != nil {
		s.fail(w, r, "create link", err)
		return
	}
	writeJSON(w, http.StatusCreated, s.linkView(l))
}

func (s *Server) listLinks(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" || id.AgentID != "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "listing connect links needs a person or account credential"})
		return
	}
	links, err := s.store.Links(r.Context(), id.Tenant, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		s.fail(w, r, "list links", err)
		return
	}
	out := make([]LinkView, 0, len(links))
	for _, l := range links {
		out = append(out, s.linkView(l))
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out})
}

// revokeLink revokes a link and ends every connection made through it,
// pending ones included, revoking their keys.
func (s *Server) revokeLink(w http.ResponseWriter, r *http.Request) {
	id, ok := person(w, r)
	if !ok {
		return
	}
	if !canConsent(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only an admin or editor of this account can revoke a connect link"})
		return
	}
	ctx, linkID := r.Context(), r.PathValue("id")
	won, err := s.store.RevokeLink(ctx, id.Tenant, linkID, id.Subject, s.now())
	if err != nil {
		s.fail(w, r, "revoke link", err)
		return
	}
	if !won {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no live connect link with that id on this account"})
		return
	}
	grants, err := s.store.LinkGrants(ctx, linkID)
	if err != nil {
		// The link is revoked; its connections end on their next use
		// (live() checks the link), so say that rather than a bare error.
		s.logger.ErrorContext(ctx, "oauth: list link grants", slog.String("link", linkID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusOK, map[string]any{"revoked": linkID,
			"note": "the link is revoked; its connections end on their next use"})
		return
	}
	for _, g := range grants {
		s.endGrant(ctx, g, EndedLinkGone)
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": linkID, "connections_ended": len(grants)})
}

// --- device flow (RFC 8628) ---

// resolveResource reads a requested resource: the generic gateway URL
// (also when empty), or a live connect link's URL. It returns the
// resource and the link id ("" for generic).
func (s *Server) resolveResource(ctx context.Context, requested string) (string, string, error) {
	if requested == "" || requested == s.cfg.Resource {
		return s.cfg.Resource, "", nil
	}
	id, ok := s.linkFromResource(requested)
	if !ok {
		return "", "", errors.New("tokens are issued for KIFF gateway URLs only")
	}
	if _, err := s.linkLive(ctx, id); err != nil {
		return "", "", errors.New("this connect link is no longer valid; ask for a new one")
	}
	return requested, id, nil
}

// userCodeAlphabet has no vowels or look-alikes, so codes are easy to
// read aloud and cannot spell words.
const userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"

func newUserCode() string {
	// Rejection sampling: bytes from 240 up would favour the first letters
	// (256 is not a multiple of 20), so they are drawn again.
	limit := byte(256 / len(userCodeAlphabet) * len(userCodeAlphabet))
	out := make([]byte, 0, 9)
	buf := make([]byte, 1)
	for len(out) < 9 {
		if len(out) == 4 {
			out = append(out, '-')
			continue
		}
		if _, err := rand.Read(buf); err != nil {
			panic(err)
		}
		if buf[0] >= limit {
			continue
		}
		out = append(out, userCodeAlphabet[int(buf[0])%len(userCodeAlphabet)])
	}
	return string(out)
}

// NormalizeUserCode accepts a code typed with or without the dash, in any
// case, with spaces.
func NormalizeUserCode(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(raw) {
		if strings.ContainsRune(userCodeAlphabet, r) {
			b.WriteRune(r)
		}
	}
	c := b.String()
	if len(c) != 8 {
		return ""
	}
	return c[:4] + "-" + c[4:]
}

// deviceAuthorization starts a device flow for a registered client.
func (s *Server) deviceAuthorization(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.allowRegister("device:"+clientIP(r), registerPerHour) {
		oauthError(w, http.StatusTooManyRequests, "slow_down", "too many device requests from this address; try again later")
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "the body must be form-encoded")
		return
	}
	f := r.PostForm
	c, err := s.store.Client(r.Context(), f.Get("client_id"))
	if err != nil {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "unknown client; register first")
		return
	}
	if f.Get("scope") != "" && f.Get("scope") != Scope {
		oauthError(w, http.StatusBadRequest, "invalid_scope", "the only scope is "+Scope)
		return
	}
	resource, linkID, err := s.resolveResource(r.Context(), f.Get("resource"))
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_target", err.Error())
		return
	}
	now := s.now()
	deviceCode := randomToken()
	d := Device{Hash: hashOf(deviceCode), UserCode: newUserCode(), ClientID: c.ID, Resource: resource, LinkID: linkID,
		CreatedAt: now, ExpiresAt: now.Add(DeviceTTL)}
	if err := s.store.CreateDevice(r.Context(), d); err != nil {
		// A user code collision is astronomically rare; one retry.
		d.UserCode = newUserCode()
		if err := s.store.CreateDevice(r.Context(), d); err != nil {
			s.fail(w, r, "create device", err)
			return
		}
	}
	verify := strings.TrimSuffix(s.cfg.ConsentURL, "/oauth/consent") + "/oauth/device"
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               deviceCode,
		"user_code":                 d.UserCode,
		"verification_uri":          verify,
		"verification_uri_complete": verify + "?code=" + url.QueryEscape(d.UserCode),
		"expires_in":                int(DeviceTTL.Seconds()),
		"interval":                  int(DeviceInterval.Seconds()),
	})
}

// allowDeviceLookup is a per-person limit on looking up device codes, so
// codes cannot be guessed by trying many (the space is about 2.6e10 and
// codes live 10 minutes). Every handler that reads a user code from the
// person calls it first: view, approve and deny.
func (s *Server) allowDeviceLookup(w http.ResponseWriter, id auth.Identity) bool {
	if s.allowRegister("device-lookup:"+id.Subject, registerPerHour) {
		return true
	}
	writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many code lookups; wait a little and try again"})
	return false
}

// pendingDevice is a device code waiting for a person, with its client.
func (s *Server) pendingDevice(ctx context.Context, raw string) (Device, Client, error) {
	code := NormalizeUserCode(raw)
	if code == "" {
		return Device{}, Client{}, ErrNotFound
	}
	d, err := s.store.DeviceByUserCode(ctx, code)
	if err != nil {
		return d, Client{}, err
	}
	if d.Status != "pending" || !s.now().Before(d.ExpiresAt) {
		return d, Client{}, ErrNotFound
	}
	c, err := s.store.Client(ctx, d.ClientID)
	return d, c, err
}

// DeviceInfo is what the device approval page shows.
type DeviceInfo struct {
	UserCode        string `json:"user_code"`
	ClientName      string `json:"client_name"`
	CanConsent      bool   `json:"can_consent"`
	AgentID         string `json:"agent_id,omitempty"`
	WrongAccount    bool   `json:"wrong_account,omitempty"`
	PreviousAgentID string `json:"previous_agent_id,omitempty"`
}

func (s *Server) deviceInfo(w http.ResponseWriter, r *http.Request) {
	id, ok := person(w, r)
	if !ok {
		return
	}
	if !s.allowDeviceLookup(w, id) {
		return
	}
	d, c, err := s.pendingDevice(r.Context(), r.PathValue("code"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this code is unknown, already used or expired; start again in your terminal"})
		return
	}
	info := DeviceInfo{UserCode: d.UserCode, ClientName: c.Name, CanConsent: canConsent(id)}
	if d.LinkID != "" {
		l, err := s.linkLive(r.Context(), d.LinkID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "this connect link is no longer valid; ask for a new one"})
			return
		}
		if l.TenantID != id.Tenant {
			info.WrongAccount, info.CanConsent = true, false
		} else {
			info.AgentID = l.AgentID
		}
	} else if g, err := s.store.LatestGrant(r.Context(), id.Tenant, c.Name); err == nil {
		info.PreviousAgentID = g.AgentID
	}
	writeJSON(w, http.StatusOK, info)
}

// deviceApprove applies the same consent rules as sign-in (approve), then
// stores the pending grant on the device code. The client's next poll
// redeems it.
func (s *Server) deviceApprove(w http.ResponseWriter, r *http.Request) {
	id, ok := person(w, r)
	if !ok {
		return
	}
	var body struct {
		UserCode string `json:"user_code"`
		AgentID  string `json:"agent_id"`
		NewAgent string `json:"new_agent"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if !s.allowDeviceLookup(w, id) {
		return
	}
	ctx := r.Context()
	d, c, err := s.pendingDevice(ctx, body.UserCode)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this code is unknown, already used or expired; start again in your terminal"})
		return
	}
	if s.afterPendingDevice != nil {
		s.afterPendingDevice()
	}
	// claimed records whether this request won pending → claimed: only
	// then may a failure end the code, never another admin's approval.
	claimed := false
	// The cleanup must run even if the browser disconnected.
	cleanup := context.WithoutCancel(ctx)
	g, ok := s.approve(w, r, id, approval{ClientID: c.ID, ClientName: c.Name, Resource: d.Resource, LinkID: d.LinkID,
		AgentID: body.AgentID, NewAgent: body.NewAgent, Method: "code"},
		func(now time.Time) (bool, error) {
			won, err := s.store.SetDeviceStatus(ctx, d.Hash, "pending", "claimed", now)
			claimed = won && err == nil
			return won, err
		})
	if !ok {
		// Only the request that claimed the code ends it, so the terminal
		// is told at once instead of polling until it expires. A request
		// that lost the claim leaves the winner's approval alone.
		if claimed {
			_, _ = s.store.SetDeviceStatus(cleanup, d.Hash, "claimed", "failed", s.now())
		}
		return
	}
	if err := s.store.ApproveDevice(ctx, d.Hash, g); err != nil {
		_, _ = s.store.SetDeviceStatus(cleanup, d.Hash, "claimed", "failed", s.now())
		s.fail(w, r, "approve device", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "approved", "agent_id": g.AgentID})
}

// deviceDeny declines a device code. Anyone signed in may decline.
func (s *Server) deviceDeny(w http.ResponseWriter, r *http.Request) {
	id, ok := person(w, r)
	if !ok {
		return
	}
	if !s.allowDeviceLookup(w, id) {
		return
	}
	var body struct {
		UserCode string `json:"user_code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	d, _, err := s.pendingDevice(r.Context(), body.UserCode)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this code is unknown, already used or expired"})
		return
	}
	if won, err := s.store.SetDeviceStatus(r.Context(), d.Hash, "pending", "denied", s.now()); err != nil || !won {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this code was already answered"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "denied"})
}

// deviceToken is the client's poll (RFC 8628 3.4, 3.5).
func (s *Server) deviceToken(w http.ResponseWriter, r *http.Request) {
	ctx, f, now := r.Context(), r.PostForm, s.now()
	d, err := s.store.DeviceByHash(ctx, hashOf(f.Get("device_code")))
	if err != nil || d.ClientID != f.Get("client_id") {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown device code")
		return
	}
	if !now.Before(d.ExpiresAt) {
		oauthError(w, http.StatusBadRequest, "expired_token", "the code expired; start again")
		return
	}
	if f.Get("resource") != "" && f.Get("resource") != d.Resource {
		oauthError(w, http.StatusBadRequest, "invalid_target", "this code was issued for "+d.Resource)
		return
	}
	switch d.Status {
	case "pending", "claimed":
		prev, err := s.store.PollDevice(ctx, d.Hash, now)
		if err == nil && prev != nil && now.Sub(*prev) < DeviceInterval {
			oauthError(w, http.StatusBadRequest, "slow_down", "poll at most every 5 seconds")
			return
		}
		oauthError(w, http.StatusBadRequest, "authorization_pending", "waiting for an admin or editor to allow it")
		return
	case "denied":
		oauthError(w, http.StatusBadRequest, "access_denied", "the person declined")
		return
	case "failed":
		oauthError(w, http.StatusBadRequest, "access_denied", "approving this code failed; start again")
		return
	case "redeemed":
		// A second redemption: someone else may hold the device code.
		if g, err := s.store.Grant(ctx, d.GrantID); err == nil {
			s.endGrant(ctx, g, EndedReplay)
		}
		oauthError(w, http.StatusBadRequest, "invalid_grant", "this code was already used")
		return
	}
	// approved: redeem once.
	if won, err := s.store.SetDeviceStatus(ctx, d.Hash, "approved", "redeemed", now); err != nil || !won {
		if err != nil {
			s.fail(w, r, "redeem device", err)
			return
		}
		oauthError(w, http.StatusBadRequest, "invalid_grant", "this code was already used")
		return
	}
	g, err := s.store.Grant(ctx, d.GrantID)
	if err != nil || g.EndedAt != nil || g.RedeemedAt != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "this connection has ended")
		return
	}
	s.redeem(w, r, g)
}

// --- Connections ---

// ConnectionView is one OAuth connection as the Connections view shows it.
type ConnectionView struct {
	ID          string     `json:"id"`
	AgentID     string     `json:"agent_id"`
	ClientName  string     `json:"client_name"`
	Method      string     `json:"method"` // sign-in | code
	LinkID      string     `json:"link_id,omitempty"`
	KeyID       string     `json:"key_id"`
	GrantedBy   string     `json:"granted_by"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  time.Time  `json:"last_used_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	EndedReason string     `json:"ended_reason,omitempty"`
	Status      string     `json:"status"` // live | idle | ended
}

// IdleAfter: a live connection with no call this long shows as idle.
const IdleAfter = 30 * 24 * time.Hour

func (s *Server) connections(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" || id.AgentID != "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "listing connections needs a person or account credential"})
		return
	}
	now := s.now()
	grants, err := s.store.TenantGrants(r.Context(), id.Tenant, now.Add(-IdleAfter))
	if err != nil {
		s.fail(w, r, "list connections", err)
		return
	}
	out := make([]ConnectionView, 0, len(grants))
	for _, g := range grants {
		v := ConnectionView{ID: g.ID, AgentID: g.AgentID, ClientName: g.ClientName, Method: g.Method, LinkID: g.LinkID,
			KeyID: g.KeyID, GrantedBy: g.GrantedBy, CreatedAt: g.CreatedAt, LastUsedAt: g.LastUsedAt,
			EndedAt: g.EndedAt, EndedReason: g.EndedReason, Status: "live"}
		switch {
		case g.EndedAt != nil:
			v.Status = "ended"
		case now.Sub(g.LastUsedAt) > IdleAfter:
			v.Status = "idle"
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
}

// revokeGrant ends one connection and revokes its key.
func (s *Server) revokeGrant(w http.ResponseWriter, r *http.Request) {
	id, ok := person(w, r)
	if !ok {
		return
	}
	if !canConsent(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only an admin or editor of this account can revoke a connection"})
		return
	}
	g, err := s.store.Grant(r.Context(), r.PathValue("id"))
	if err != nil || g.TenantID != id.Tenant || g.EndedAt != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no live connection with that id on this account"})
		return
	}
	s.endGrant(r.Context(), g, EndedRevoked)
	writeJSON(w, http.StatusOK, map[string]string{"revoked": g.ID})
}
