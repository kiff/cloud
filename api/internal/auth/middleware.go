package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/kiff/pkg/kiff/actor"
)

// Middleware is the HTTP entry point for cloud authentication.
// One Middleware instance is constructed at server startup and
// wraps the protected route tree. The Provider verifies session
// tokens; the APIKeys store verifies API keys; the ActorResolver
// turns the verified identity into a tenant + actor pair attached
// to the request context.
//
// Auth-failure modes (malformed token, unknown subject, revoked
// key) emit 401. Resolver infrastructure failures emit 503. The middleware does not
// distinguish 401 from 403 — that distinction belongs to per-route
// authorization, not the boundary.
//
// Tenant derivation is the ActorResolver's job; the middleware writes
// the resulting tenant id onto Identity.Tenant. Code further down the
// pipeline that needs the tenant.Tenant record looks it up via
// tenant.Store.Get.
type Middleware struct {
	provider    Provider
	apiKeys     APIKeyStore
	resolver    ActorResolver
	wallets     WalletStore
	devSessions DeveloperSessionStore // optional; enables kiff_dev_ tokens
	devices     DeviceStore           // optional; a session's approval device
	members     Memberships           // optional; membership resolution
	oauth       OAuthTokenResolver    // optional; enables kiff_oat_ tokens
	clock       func() time.Time
	logger      *slog.Logger
}

// MiddlewareOption configures Middleware. New defaults to
// time.Now().UTC() for the clock and a discard logger; tests
// override via WithClock and WithLogger.
type MiddlewareOption func(*Middleware)

// WithClock substitutes the time source. Tests use this to control
// LastUsedAt deterministically.
func WithClock(now func() time.Time) MiddlewareOption {
	return func(m *Middleware) { m.clock = now }
}

// WithLogger substitutes the logger. Production passes the API
// server's slog.Logger; tests pass slog.New(discardHandler{}).
func WithLogger(l *slog.Logger) MiddlewareOption {
	return func(m *Middleware) {
		if l != nil {
			m.logger = l
		}
	}
}

// WithWallets enables the per-request wallet enrichment hook.
// When set, the middleware calls wallets.Get with the
// verified subject and stamps Identity.Wallet on success. The
// lookup runs once per request — no cross-request cache, since
// wallet rotation invalidates a stale entry instantly. A
// transient store error logs and proceeds with Identity.Wallet
// nil; the request is not failed.
//
// Optional: callers that don't care about wallet attribution can
// omit this option. Identity.Wallet stays nil for those callers.
func WithWallets(store WalletStore) MiddlewareOption {
	return func(m *Middleware) { m.wallets = store }
}

// WithDeveloperSessions enables developer-session (kiff_dev_) tokens.
// When set, a bearer with the DevSessionPrefix is
// verified against the store and resolved to an owner-plane identity.
// When nil (the default), such a token is rejected like any unknown
// credential — the device-auth surface is simply not wired.
func WithDeveloperSessions(store DeveloperSessionStore) MiddlewareOption {
	return func(m *Middleware) { m.devSessions = store }
}

// WithDevices resolves a developer session's approval device: the
// session signs with that device's key, and a revoked device ends it.
func WithDevices(store DeviceStore) MiddlewareOption {
	return func(m *Middleware) { m.devices = store }
}

// NewMiddleware constructs a Middleware. provider, apiKeys, and
// resolver are required.
func NewMiddleware(provider Provider, apiKeys APIKeyStore, resolver ActorResolver, opts ...MiddlewareOption) *Middleware {
	m := &Middleware{
		provider: provider,
		apiKeys:  apiKeys,
		resolver: resolver,
		clock:    func() time.Time { return time.Now().UTC() },
		logger:   slog.New(discardHandler{}),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Wrap returns a new http.Handler that authenticates inbound
// requests before delegating to next. Unauthenticated requests
// receive 401 with a small JSON body; authenticated requests have
// Identity and actor.Actor attached to the context.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := m.authenticate(r)
		if err != nil {
			m.refuse(w, r, err)
			return
		}
		if id.gatewayOnly() && !OAuthRouteAllowed(r) {
			oauthForbidden(w)
			return
		}
		if id.IsConnector() && !ConnectorRouteAllowed(r) {
			connectorForbidden(w)
			return
		}

		// Enrich Identity.Wallet from WalletStore so
		// the resolver's display-name annotation and downstream
		// audit code see the wallet without each call having to
		// hit the store. Optional — when m.wallets is nil, skip.
		// A transient lookup error must not fail the request.
		m.enrichWallet(r.Context(), &id)

		// Resolution rule:
		//   - Session path: the resolver is the source of truth
		//     for (Actor, Tenant). Identity.Tenant is empty here.
		//   - API-key path: the key record self-identifies the
		//     tenant; the resolver is consulted opportunistically
		//     to enrich Actor metadata (display name, roles), but
		//     a missing entry must NOT fail the request — API
		//     keys exist precisely so machines without a session
		//     can call the cloud.
		var resolved actor.Actor
		var tenantID string
		if m.members != nil {
			m.serveWithMemberships(w, r, id, next)
			return
		}
		if id.APIKey {
			tenantID = id.Tenant
			if maybe, _, lookupErr := m.resolver.Resolve(r.Context(), id); lookupErr == nil {
				resolved = maybe
			} else {
				resolved = actor.Actor{
					ID:          "actor-" + id.Subject,
					Type:        actor.TypeService,
					DisplayName: id.Subject,
					Roles:       append([]string(nil), id.Roles...),
				}
			}
		} else if id.Method == MethodDeveloper {
			// A developer session carries its tenant on the record (bound
			// at approval), like an API key — the resolver is not the
			// source of truth for it, and a missing resolver entry must
			// not fail the request. But it represents a human owner, so
			// the owner-role widening below (the !id.APIKey branch) still
			// applies and gives it tenant_owner.
			tenantID = id.Tenant
			if maybe, _, lookupErr := m.resolver.Resolve(r.Context(), id); lookupErr == nil {
				resolved = maybe
			} else {
				resolved = actor.Actor{
					ID:          "actor-" + id.Subject,
					Type:        actor.TypeHuman,
					DisplayName: id.Subject,
					Roles:       append([]string(nil), id.Roles...),
				}
			}
		} else {
			var err error
			resolved, tenantID, err = m.resolver.Resolve(r.Context(), id)
			if err != nil {
				if errors.Is(err, ErrResolverUnavailable) {
					m.unavailable(w, r, err)
					return
				}
				m.unauthorized(w, r, err)
				return
			}
		}
		id.Tenant = tenantID

		// A session-method identity that the resolver
		// successfully attributed to a tenant is, by the
		// auto-provisioning contract (one Clerk subject = one
		// tenant they own, see tenant.EnsureForSubject), the
		// owner of that tenant. The owner must satisfy the role
		// names emitted by the cloud's generated domains:
		// `operator` for the guided-builder era and `tenant_owner`
		// for Studio/guard-derived domains. Clerk solo users also
		// arrive with `admin`, so keeping only the provider role
		// causes generated domains to deny the first proof action.
		//
		// Scope of the default:
		//   - Session-method only. API-key paths carry their
		//     own roles from the key record and must not be
		//     widened here — the key minter explicitly chose
		//     the role set.
		//   - The owner aliases are additive. Existing provider
		//     roles such as `admin` are preserved, and missing
		//     generated-domain aliases are appended.
		//   - The normalized roles flow into both Identity.Roles
		//     (which the proposal gate reads) and Actor.Roles
		//     (which downstream attribution reads), so the two
		//     stay consistent.
		if !id.APIKey {
			id.Roles = sessionOwnerRoles(id.Roles)
			resolved.Roles = append([]string(nil), id.Roles...)
		}

		ctx := WithIdentity(r.Context(), id)
		ctx = WithActor(ctx, resolved)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authenticate verifies the bearer: an API key, a developer session or
// a provider session.
func (m *Middleware) authenticate(r *http.Request) (Identity, error) {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if raw == "" {
		return Identity{}, errors.New("missing Authorization header")
	}
	token := trimBearer(raw)
	if token == "" {
		return Identity{}, errors.New("empty bearer token")
	}
	switch {
	case strings.HasPrefix(token, APIKeyPrefix):
		return m.verifyAPIKey(r.Context(), token)
	case strings.HasPrefix(token, DevSessionPrefix):
		return m.verifyDevSession(r.Context(), token)
	case strings.HasPrefix(token, OAuthAccessTokenPrefix):
		return m.verifyOAuthToken(r.Context(), token)
	}
	return m.provider.VerifySession(r.Context(), token)
}

// WrapPerson authenticates a person (a dashboard or developer session)
// without resolving a tenant, for the few routes a person uses before
// or across accounts: listing their memberships, and previewing and
// accepting an invitation. Keys are refused: they belong to
// one account and have no memberships. The identity carries no tenant
// and no roles, so nothing tenant-scoped can run behind it.
func (m *Middleware) WrapPerson(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := m.authenticate(r)
		if err != nil {
			m.refuse(w, r, err)
			return
		}
		if id.gatewayOnly() && !OAuthRouteAllowed(r) {
			oauthForbidden(w)
			return
		}
		if id.APIKey {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "people_only",
				"message": "this route is for a person's session, not a key"})
			return
		}
		id.Tenant, id.Roles = "", nil
		ctx := WithIdentity(r.Context(), id)
		ctx = WithActor(ctx, humanActor(id))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// refuse answers a request that couldn't be authenticated: 503 when a
// store couldn't say whether its credential is valid (the database down),
// so a client retries instead of taking its key for a bad one; else 401.
func (m *Middleware) refuse(w http.ResponseWriter, r *http.Request, cause error) {
	if errors.Is(cause, ErrResolverUnavailable) {
		m.unavailable(w, r, cause)
		return
	}
	m.unauthorized(w, r, cause)
}

func (m *Middleware) unavailable(w http.ResponseWriter, r *http.Request, cause error) {
	m.logger.ErrorContext(r.Context(), "auth: identity resolver unavailable",
		slog.String("path", r.URL.Path),
		slog.String("method", r.Method),
		slog.String("err", cause.Error()),
	)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "5")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "service unavailable"})
}

func sessionOwnerRoles(roles []string) []string {
	out := append([]string(nil), roles...)
	out = appendRole(out, "operator")
	out = appendRole(out, "tenant_owner")
	return out
}

func appendRole(roles []string, role string) []string {
	role = strings.TrimSpace(role)
	if role == "" {
		return roles
	}
	for _, existing := range roles {
		if strings.EqualFold(strings.TrimSpace(existing), role) {
			return roles
		}
	}
	return append(roles, role)
}

// verifyDevSession parses a developer-session token, looks it up in
// the store (which enforces revoke + expiry), constructs an
// owner-plane Identity, and best-effort-touches LastSeenAt. When no
// developer-session store is wired the token is rejected.
func (m *Middleware) verifyDevSession(ctx context.Context, raw string) (Identity, error) {
	if m.devSessions == nil {
		return Identity{}, errors.New("auth: developer sessions are not enabled")
	}
	parsed, err := ParseDevSessionToken(raw)
	if err != nil {
		return Identity{}, err
	}
	sess, err := m.devSessions.GetByHash(ctx, parsed.Hash, m.clock())
	if err != nil {
		if !errors.Is(err, ErrDevSessionNotFound) && !errors.Is(err, ErrDevSessionRevoked) && !errors.Is(err, ErrDevSessionExpired) {
			return Identity{}, errors.Join(ErrResolverUnavailable, err)
		}
		return Identity{}, err
	}
	// Best-effort last-seen update; a failure does not fail the
	// request (the session is already verified).
	if terr := m.devSessions.Touch(ctx, sess.ID, m.clock()); terr != nil {
		m.logger.WarnContext(ctx, "auth: developer session touch failed",
			slog.String("session_id", sess.ID), slog.String("err", terr.Error()))
	}
	id := Identity{
		Subject: sess.Subject,
		Tenant:  sess.Tenant,
		Method:  MethodDeveloper,
	}
	if sess.DeviceID != "" {
		// The device decides: its key signs, and once it's removed
		// the session it signed in is over, whatever the session says.
		if m.devices == nil {
			return Identity{}, errors.New("auth: this session's device can't be checked")
		}
		d, err := m.devices.Get(ctx, sess.DeviceID)
		if err != nil {
			return Identity{}, fmt.Errorf("auth: this session's device: %w", err)
		}
		if d.IsRevoked() || d.Tenant != sess.Tenant || d.Subject != sess.Subject {
			return Identity{}, ErrDevSessionRevoked
		}
		id.DeviceKey, id.DeviceSessionID, id.DeviceName, id.DeviceID = d.PublicKey, sess.ID, d.Name, d.ID
		if terr := m.devices.Touch(ctx, d.ID, m.clock()); terr != nil {
			m.logger.WarnContext(ctx, "auth: device touch failed", slog.String("device", d.ID), slog.String("err", terr.Error()))
		}
		return id, nil
	}
	if len(sess.DevicePublicKey) > 0 {
		id.DeviceKey, id.DeviceSessionID, id.DeviceName = sess.DevicePublicKey, sess.ID, sess.Label
	}
	return id, nil
}

// verifyAPIKey parses the raw key, looks it up in the store,
// constructs an Identity, and best-effort-touches LastUsedAt.
func (m *Middleware) verifyAPIKey(ctx context.Context, raw string) (Identity, error) {
	parsed, err := ParseAPIKey(raw)
	if err != nil {
		return Identity{}, err
	}
	rec, err := m.apiKeys.GetByHash(ctx, parsed.Hash)
	if err != nil {
		if !errors.Is(err, ErrAPIKeyNotFound) && !errors.Is(err, ErrAPIKeyRevoked) {
			// The store couldn't say: not the key's fault (refuse, below).
			return Identity{}, errors.Join(ErrResolverUnavailable, err)
		}
		return Identity{}, err
	}
	if rec.Tenant != parsed.Tenant {
		// Defensive: the embedded tenant in the key must match the
		// stored record. A mismatch is either a bug in Insert or
		// tampering; either way, refuse.
		return Identity{}, errors.New("auth: api key tenant mismatch with stored record")
	}

	// Best-effort LastUsedAt update. A failure is logged but does
	// not fail the request; the key is already verified.
	if err := m.apiKeys.Touch(ctx, rec.ID, m.clock()); err != nil {
		m.logger.WarnContext(ctx, "auth: api key touch failed",
			slog.String("key_id", rec.ID),
			slog.String("err", err.Error()),
		)
	}

	roles := rolesInEffect(rec)
	return Identity{
		Subject:  rec.Subject,
		Tenant:   rec.Tenant, // API keys self-identify their tenant.
		Method:   MethodAPIKey,
		APIKey:   true,
		Roles:    roles,
		DomainID: rec.DomainID, // "" = tenant-wide; non-empty binds the key to one domain
		AgentID:  rec.AgentID,  // "" = unbound; non-empty: the key may only act as this agent
		// "" = any environment; non-empty: requests are decided in it
		Environment: rec.Environment,
		Metadata:    map[string]string{"api_key_id": rec.ID},
	}, nil
}

// enrichWallet looks up the user's wallet link and stamps
// id.Wallet on success. ErrWalletNotLinked is the no-op case;
// other errors are logged and the request proceeds with
// id.Wallet nil. A wallet lookup failure must not turn an
// otherwise-authenticated request into a 401 — the user's auth
// already passed; the wallet is decoration.
func (m *Middleware) enrichWallet(ctx context.Context, id *Identity) {
	if m.wallets == nil || id == nil || id.Subject == "" {
		return
	}
	link, err := m.wallets.Get(ctx, id.Subject)
	if err != nil {
		if errors.Is(err, ErrWalletNotLinked) {
			return
		}
		m.logger.WarnContext(ctx, "auth: wallet lookup failed; proceeding without wallet",
			slog.String("subject", id.Subject),
			slog.String("err", err.Error()),
		)
		return
	}
	id.Wallet = &link
}

// unauthorized writes a 401 JSON body and logs the cause. The body
// is intentionally terse: real auth-failure detail belongs in the
// server logs, not the response.
func (m *Middleware) unauthorized(w http.ResponseWriter, r *http.Request, cause error) {
	m.logger.InfoContext(r.Context(), "auth: unauthorized request",
		slog.String("path", r.URL.Path),
		slog.String("method", r.Method),
		slog.String("err", cause.Error()),
	)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="kiff-cloud"`)
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
}

// discardHandler is a slog.Handler that drops every record. Used
// as the default logger so middleware does not write to stdout in
// tests. Production callers pass a real logger via WithLogger.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (discardHandler) WithAttrs([]slog.Attr) slog.Handler        { return discardHandler{} }
func (discardHandler) WithGroup(string) slog.Handler             { return discardHandler{} }

// Compile-time interface satisfaction. If MapResolver stops
// implementing ActorResolver the build breaks here, before
// downstream code that depends on the assumption.
var _ ActorResolver = (*MapResolver)(nil)
