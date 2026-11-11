package auth

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
)

// OAuthAccessTokenPrefix marks an access token issued by KIFF's
// authorization server. The token is a handle to one grant,
// and the grant owns exactly one agent-bound gateway_agent key.
const OAuthAccessTokenPrefix = "kiff_oat_"

// OAuthGrant is what an access token resolves to: the grant and the
// key it owns.
type OAuthGrant struct {
	GrantID string
	KeyHash [APIKeyHashSize]byte
	// Resource is the gateway URL the token was issued for: the generic
	// URL or a connect link's. The gateway checks
	// it against the URL it was called on.
	Resource string
}

// OAuthTokenResolver resolves an access token. It refuses an unknown or
// expired token, and a grant that has ended; it also ends a grant that
// has idled out or whose granter no longer holds the role.
type OAuthTokenResolver interface {
	ResolveAccessToken(ctx context.Context, token string) (OAuthGrant, error)
}

// WithOAuthTokens accepts kiff_oat_ access tokens. Without it they are
// refused.
func WithOAuthTokens(r OAuthTokenResolver) MiddlewareOption {
	return func(m *Middleware) { m.oauth = r }
}

// oauthRoutes are the only routes an OAuth access token or an agent's
// gateway key reaches: the ones the gateway calls with an agent's
// credential. Every other route refuses it.
var oauthRoutes = []string{
	"GET /v1/me",
	"GET /v1/me/card",
	"GET /v1/me/holds",
	"POST /v1/events/raw",
	"POST /v1/proposals/decide",
	// A trial's call (kiff/tafs#107): decided for this agent, nothing
	// recorded, read-only to the account.
	"POST /v1/gateway/trial",
}

// runtimeRoutes are the routes an agent's gateway key reaches besides
// oauthRoutes, and an OAuth access token never does: the runtime running
// a taf holds only its gateway key, and asks what a person may do with its
// own agent before their run and at each turn, and resolves the run grant
// their run carries. Both handlers answer only for the key's own agent;
// resolving needs a grant KIFF signed. A client signed in over OAuth runs
// no one's runs, so it needs neither.
var runtimeRoutes = []string{
	"POST /v1/me/access",
	"POST /v1/me/run-grants/resolve",
	// A read grant, before it serves one person's work to another: the
	// handler answers only for the key's own agent.
	"POST /v1/me/read-grants/resolve",
	// Whether a new run may start, before each one: the balance, for the
	// key's own agent. It grants nothing.
	"GET /v1/me/credits/allowance",
	// A removed taf's runtime archives the key's own agent, its last act
	// with the key: it only takes authority away, and revokes the key.
	"POST /v1/agents/self/archive",
	// A taf hands a step it may never take to its owner ("Your step"), and
	// lists its own that wait. The handler keeps both to the key's agent.
	"POST /v1/me/handoffs",
	"GET /v1/me/handoffs",
}

// OAuthRouteAllowed reports whether r may be served to an OAuth access
// token.
func OAuthRouteAllowed(r *http.Request) bool {
	path := strings.TrimRight(r.URL.Path, "/")
	// Reconciliation only reads gateway-owned terminal evidence for this
	// agent's operation. Its handler checks ownership before any release;
	// it never accepts an outcome or a budget revision from the caller.
	if r.Method == http.MethodPost && postOnOne(path, "/v1/me/card-effects/", "/reconcile") {
		return true
	}
	// A surcharge reads the gateway's own verify read of the agent's call
	// the same way, and counts only what the vendor said it used.
	if r.Method == http.MethodPost && postOnOne(path, "/v1/me/card-effects/", "/surcharge") {
		return true
	}
	// An agent withdraws one of its own held calls when its run is
	// cancelled. The handler checks the call is the agent's, and a
	// withdrawal only stops a call from being sent: it reads nothing and
	// grants nothing. The agent's runtime holds only its gateway key.
	if r.Method == http.MethodPost && postOnOne(path, "/v1/me/holds/", "/withdraw") {
		return true
	}
	// A taf's key reads its own agent's drafts, so tafs cloud can finish a
	// version switch once the owner confirmed its Cards (kiff/tafs#107).
	// The handler serves only drafts for the key's agent.
	if r.Method == http.MethodGet && postOnOne(path, "/v1/me/agent-drafts/", "") {
		return true
	}
	// It reads whether its own draft is being tried, which the gateway asks
	// before sending a trial's call, and its runtime ends its own idle trial
	// (kiff/tafs#173). The handler serves only the key's agent's drafts, and
	// ending a trial only takes authority away.
	if (r.Method == http.MethodGet || r.Method == http.MethodDelete) && postOnOne(path, "/v1/me/agent-drafts/", "/trial") {
		return true
	}
	return slices.Contains(oauthRoutes, r.Method+" "+path)
}

// postOnOne reports whether path is prefix, one id segment, then suffix.
func postOnOne(path, prefix, suffix string) bool {
	tail, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return false
	}
	id, ok := strings.CutSuffix(tail, suffix)
	return ok && id != "" && !strings.Contains(id, "/") && id != "." && id != ".."
}

// GatewayRouteAllowed reports whether r may be served to id, a gateway
// credential: an OAuth access token reaches oauthRoutes alone, and an
// agent's gateway key those and runtimeRoutes.
func GatewayRouteAllowed(r *http.Request, id Identity) bool {
	if OAuthRouteAllowed(r) {
		return true
	}
	if id.isOAuth() {
		return false
	}
	path := strings.TrimRight(r.URL.Path, "/")
	// The runtime running a taf reports each of its runs that ended having
	// used AI, once, for its credits. The handler keeps it only for the
	// key's own agent, and it grants nothing.
	if r.Method == http.MethodPut && postOnOne(path, "/v1/me/runs/", "/usage") {
		return true
	}
	// "Your step": the runtime reads a step its taf handed to the owner,
	// and names its read of the vendor after "Done". The handler serves
	// only the key's own agent's; a hand-off grants nothing.
	if (r.Method == http.MethodGet && postOnOne(path, "/v1/me/handoffs/", "")) ||
		(r.Method == http.MethodPost && postOnOne(path, "/v1/me/handoffs/", "/check")) {
		return true
	}
	return slices.Contains(runtimeRoutes, r.Method+" "+path)
}

// isOAuth reports whether id came from an OAuth access token.
func (id Identity) isOAuth() bool { return id.Metadata["oauth_grant_id"] != "" }

// gatewayOnly reports whether id may reach only oauthRoutes: an OAuth
// access token, or a gateway key pasted into a client. Both are the same
// credential to the gateway, so they reach the same routes; a pasted key
// that could read the rest of the account (every agent's Cards, held
// work, the event stream) would make it worth more than the sign-in
// connection for the same agent. It turns on the role alone: a
// gateway key is always bound to an agent when it is created, and one
// that somehow is not is still only a gateway key.
func (id Identity) gatewayOnly() bool {
	return id.isOAuth() || slices.Contains(id.Roles, RoleGatewayAgent)
}

// verifyOAuthToken resolves the token to its grant's key and builds the
// key's identity, so everything downstream treats it exactly as that
// agent's gateway key.
func (m *Middleware) verifyOAuthToken(ctx context.Context, raw string) (Identity, error) {
	if m.oauth == nil {
		return Identity{}, errors.New("auth: oauth tokens are not enabled")
	}
	g, err := m.oauth.ResolveAccessToken(ctx, raw)
	if err != nil {
		return Identity{}, err
	}
	rec, err := m.apiKeys.GetByHash(ctx, g.KeyHash)
	if err != nil {
		return Identity{}, err
	}
	if rec.IsExpired(m.clock()) {
		return Identity{}, ErrAPIKeyExpired
	}
	// Defensive: a grant only ever owns an agent-bound gateway_agent key.
	if rec.AgentID == "" || !slices.Equal(rec.Roles, []string{RoleGatewayAgent}) {
		return Identity{}, errors.New("auth: oauth grant key is not an agent-bound gateway key")
	}
	if err := m.apiKeys.Touch(ctx, rec.ID, m.clock()); err != nil {
		m.logger.WarnContext(ctx, "auth: api key touch failed", "key_id", rec.ID, "err", err.Error())
	}
	return Identity{
		Subject:     rec.Subject,
		Tenant:      rec.Tenant,
		Method:      MethodAPIKey,
		APIKey:      true,
		Roles:       rolesInEffect(rec),
		DomainID:    rec.DomainID,
		AgentID:     rec.AgentID,
		Environment: rec.Environment,
		Metadata:    map[string]string{"api_key_id": rec.ID, "oauth_grant_id": g.GrantID, "oauth_resource": g.Resource},
	}, nil
}

// RoleGatewayAgent is the only role an agent's gateway credential holds.
const RoleGatewayAgent = "gateway_agent"

// RoleSetupReader is the role of an agent that holds no tools (Setup,
// kiff/agents#91): the gateway's contract grants it the read-only tools'
// permissions, so it reads a tool the owner approved for it (kiff/tafs#138),
// and the tools of the account's own control server, so as Coach it acts
// there (kiff/tafs#206); never a vendor's tool that writes. Each call is
// decided on a Card of its own. Unlike RoleGatewayAgent it doesn't keep the
// key to the gateway's routes. Its calls are never billed (meter.FreeSetup).
const RoleSetupReader = "setup_reader"

func oauthForbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"an agent's gateway credential reaches only the gateway's routes"}` + "\n"))
}
