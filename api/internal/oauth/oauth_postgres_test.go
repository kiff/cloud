package oauth_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/oauth"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/testdb"
)

const (
	redirect = "http://127.0.0.1:53682/callback"
	resource = "https://mcp.kiff.dev/mcp"
	verifier = "verifier-verifier-verifier-verifier-verifier-1"
)

type rig struct {
	t       *testing.T
	pool    *pgxpool.Pool
	srv     *oauth.Server
	keys    *auth.MemoryAPIKeyStore
	now     *time.Time
	granter *bool
	public  http.Handler
	authed  http.Handler
	// archived agents fail the agent check; created records A5 agents.
	archived map[string]bool
	created  map[string]bool
	mu       sync.Mutex
	// failCreate makes the agent creator fail, after the claim; onCreate
	// runs inside the creator, outside the lock.
	failCreate bool
	onCreate   func()
	// docs are the client metadata documents the fake web serves, by URL;
	// fetched counts the requests.
	docs    map[string]string
	fetched int
}

// RoundTrip serves r.docs: 200 with the document, 404 otherwise, and a
// redirect for any URL ending in /moved.
func (r *rig) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.fetched++
	doc, ok := r.docs[req.URL.String()]
	r.mu.Unlock()
	res := &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: http.NoBody, Request: req}
	switch {
	case strings.HasSuffix(req.URL.Path, "/moved"):
		res.StatusCode = http.StatusFound
		res.Header.Set("Location", "https://claude.ai/oauth/claude-code-client-metadata")
	case ok:
		res.StatusCode, res.Body = http.StatusOK, io.NopCloser(strings.NewReader(doc))
	}
	return res, nil
}

func newRig(t *testing.T) *rig {
	t.Helper()
	u := testdb.URL(t)
	if u == "" {
		t.Skip(testdb.EnvVar + " not set")
	}
	pool, err := pgxpool.New(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := oauth.ApplySchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `TRUNCATE cloud_meta.oauth_clients, cloud_meta.oauth_requests,
		cloud_meta.oauth_grants, cloud_meta.oauth_codes, cloud_meta.oauth_tokens, cloud_meta.oauth_connect_links, cloud_meta.oauth_device_codes`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	active := true
	r := &rig{t: t, pool: pool, keys: auth.NewMemoryAPIKeyStore(), now: &now, granter: &active,
		archived: map[string]bool{}, created: map[string]bool{}, docs: map[string]string{}}
	r.srv = oauth.New(oauth.NewStore(pool), r.keys, oauth.Config{Issuer: "https://api.kiff.dev", Resource: resource,
		ConsentURL: "https://app.kiff.dev/oauth/consent", RedirectHosts: []string{"claude.ai"},
		MetadataHosts: []string{"claude.ai"}, SharedNets: []netip.Prefix{netip.MustParsePrefix("160.79.104.0/21")}},
		oauth.WithClock(func() time.Time { return *r.now }),
		oauth.WithHTTPClient(&http.Client{Transport: r, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}),
		oauth.WithGranterActive(func(context.Context, string, string) bool { return *r.granter }),
		oauth.WithAgentCheck(func(_ context.Context, tenant, agent string) (bool, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.archived[agent] {
				return false, nil
			}
			if r.created[tenant+"/"+agent] {
				return true, nil
			}
			// Names the new-agent tests use do not exist until created.
			if strings.HasPrefix(agent, "agent-") || strings.Contains(agent, " ") {
				return false, nil
			}
			return tenant == "t1" && agent != "archived" && agent != "nobody", nil
		}),
		oauth.WithAgentCreator(func(_ context.Context, tenant, agent, by string) error {
			r.mu.Lock()
			hook := r.onCreate
			r.mu.Unlock()
			if hook != nil {
				hook()
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.failCreate {
				return fmt.Errorf("agent store unavailable")
			}
			if agent == "support" || r.created[tenant+"/"+agent] {
				return oauth.ErrAgentExists
			}
			r.created[tenant+"/"+agent] = true
			return nil
		}))
	pub, au := http.NewServeMux(), http.NewServeMux()
	r.srv.RegisterPublic(pub)
	r.srv.Register(au)
	r.public, r.authed = pub, au
	return r
}

func (r *rig) do(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func (r *rig) register(uris ...string) (int, string) {
	b, _ := json.Marshal(map[string]any{"client_name": "Claude", "redirect_uris": uris})
	rec := r.do(r.public, httptest.NewRequest("POST", "/oauth/register", bytes.NewReader(b)))
	var out struct {
		ClientID string `json:"client_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out.ClientID
}

func challenge() string {
	s := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(s[:])
}

// authorize returns the pending request id.
func (r *rig) authorize(client string) string {
	q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {redirect}, "state": {"xyz"},
		"code_challenge": {challenge()}, "code_challenge_method": {"S256"}, "resource": {resource}}
	rec := r.do(r.public, httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || loc.Host != "app.kiff.dev" {
		r.t.Fatalf("authorize: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	return loc.Query().Get("request")
}

var admin = auth.Identity{Subject: "user_admin", Tenant: "t1", Method: auth.MethodSession, MemberRole: membership.RoleAdmin}

// consent returns the status and the code from the redirect.
func (r *rig) consent(id auth.Identity, requestID, agent string) (int, string) {
	b, _ := json.Marshal(map[string]string{"request_id": requestID, "agent_id": agent})
	req := httptest.NewRequest("POST", "/v1/oauth/consent", bytes.NewReader(b))
	req = req.WithContext(auth.WithIdentity(req.Context(), id))
	rec := r.do(r.authed, req)
	var out struct {
		RedirectTo string `json:"redirect_to"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	u, _ := url.Parse(out.RedirectTo)
	if rec.Code == http.StatusOK && (u.Query().Get("state") != "xyz" || !strings.HasPrefix(out.RedirectTo, redirect)) {
		r.t.Fatalf("consent redirect: %s", out.RedirectTo)
	}
	return rec.Code, u.Query().Get("code")
}

type tokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Error   string `json:"error"`
}

func (r *rig) token(form url.Values) (int, tokens) {
	req := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := r.do(r.public, req)
	var out tokens
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (r *rig) exchange(client, code, v string) (int, tokens) {
	return r.exchangeFor(client, code, v, resource)
}

// exchangeFor sends res as the token request's resource, as RFC 8707
// clients do with the resource they authorized for.
func (r *rig) exchangeFor(client, code, v, res string) (int, tokens) {
	return r.token(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {client},
		"redirect_uri": {redirect}, "code_verifier": {v}, "resource": {res}})
}

// connect runs the whole flow and returns the tokens.
func (r *rig) connect(client, agent string) tokens {
	*r.now = r.now.Add(time.Second)
	_, code := r.consent(admin, r.authorize(client), agent)
	st, tk := r.exchange(client, code, verifier)
	if st != http.StatusOK || !strings.HasPrefix(tk.Access, auth.OAuthAccessTokenPrefix) {
		r.t.Fatalf("exchange: %d %+v", st, tk)
	}
	return tk
}

// keyOf resolves an access token to the key it stands for.
func (r *rig) keyOf(access string) (auth.APIKeyRecord, error) {
	g, err := r.srv.ResolveAccessToken(context.Background(), access)
	if err != nil {
		return auth.APIKeyRecord{}, err
	}
	return r.keys.GetByHash(context.Background(), g.KeyHash)
}

func TestRegistrationAllowsOnlyKnownRedirects(t *testing.T) {
	r := newRig(t)
	for _, ok := range []string{redirect, "http://localhost:1/cb", "http://[::1]:9/cb", "https://claude.ai/api/mcp/auth_callback"} {
		if code, _ := r.register(ok); code != http.StatusCreated {
			t.Fatalf("%s refused: %d", ok, code)
		}
	}
	for _, bad := range []string{"https://evil.example/cb", "http://claude.ai/cb", "https://claude.ai/cb#x", "javascript:alert(1)"} {
		if code, _ := r.register(bad); code != http.StatusBadRequest {
			t.Fatalf("%s accepted: %d", bad, code)
		}
	}
}

// The token is the agent's gateway key: agent-bound, gateway_agent only,
// minted for the person who consented.
func TestConsentMintsAnAgentKey(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	tk := r.connect(client, "claude-support")
	k, err := r.keyOf(tk.Access)
	if err != nil {
		t.Fatal(err)
	}
	if k.AgentID != "claude-support" || k.Tenant != "t1" || k.Subject != "user_admin" ||
		len(k.Roles) != 1 || k.Roles[0] != auth.RoleGatewayAgent {
		t.Fatalf("key %+v", k)
	}
	// A refresh token is not an access token.
	if _, err := r.keyOf(tk.Refresh); err == nil {
		t.Fatal("refresh token accepted as access token")
	}
}

func TestOnlyAdminsAndEditorsConsent(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	editor := admin
	editor.MemberRole = membership.RoleEditor
	if code, _ := r.consent(editor, r.authorize(client), "a"); code != http.StatusOK {
		t.Fatalf("editor: %d", code)
	}
	for _, role := range []membership.Role{membership.RoleApprover, membership.RoleViewer} {
		id := admin
		id.MemberRole = role
		if code, _ := r.consent(id, r.authorize(client), "a"); code != http.StatusForbidden {
			t.Fatalf("%s consented: %d", role, code)
		}
	}
	key := auth.Identity{Subject: "k", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{"admin"}}
	if code, _ := r.consent(key, r.authorize(client), "a"); code != http.StatusForbidden {
		t.Fatalf("an API key consented: %d", code)
	}
	// A request is answered once.
	req := r.authorize(client)
	r.consent(admin, req, "a")
	if code, _ := r.consent(admin, req, "a"); code != http.StatusNotFound && code != http.StatusConflict {
		t.Fatalf("request answered twice: %d", code)
	}
}

// activeKeys counts the tenant's live keys.
func (r *rig) activeKeys() int {
	ks, _ := r.keys.ListActive(context.Background(), "t1")
	return len(ks)
}

// Consent alone mints no key: a code that is never exchanged, or fails,
// leaves nothing usable behind, and does not end a working connection.
func TestKeyIsMintedOnlyOnExchange(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	working := r.connect(client, "a")
	if r.activeKeys() != 1 {
		t.Fatalf("keys after one connection: %d", r.activeKeys())
	}
	// Consent as the same agent, then the person closes the tab.
	if code, _ := r.consent(admin, r.authorize(client), "a"); code != http.StatusOK {
		t.Fatal(code)
	}
	if r.activeKeys() != 1 {
		t.Fatalf("consent minted a key: %d", r.activeKeys())
	}
	if _, err := r.keyOf(working.Access); err != nil {
		t.Fatal("an unredeemed consent ended the working connection")
	}
	// Each failed check ends the grant and leaves no key.
	for name, try := range map[string]func(client, code string) (int, tokens){
		"verifier": func(cl, c string) (int, tokens) { return r.exchange(cl, c, verifier+"x") },
		"client":   func(cl, c string) (int, tokens) { return r.exchange("kc_other", c, verifier) },
		"redirect_uri": func(cl, c string) (int, tokens) {
			return r.token(url.Values{"grant_type": {"authorization_code"}, "code": {c}, "client_id": {cl},
				"redirect_uri": {"http://127.0.0.1:1/other"}, "code_verifier": {verifier}})
		},
	} {
		_, code := r.consent(admin, r.authorize(client), "b")
		if st, _ := try(client, code); st != http.StatusBadRequest {
			t.Fatalf("%s: %d", name, st)
		}
		// The right verifier afterwards does not revive it.
		if st, _ := r.exchange(client, code, verifier); st != http.StatusBadRequest {
			t.Fatalf("%s: grant usable after a failed exchange", name)
		}
	}
	if r.activeKeys() != 1 {
		t.Fatalf("failed exchanges left keys: %d", r.activeKeys())
	}
}

// The API binds the key only to an agent the account has, whatever page
// sent the request.
func TestConsentNeedsAKnownAgent(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	for _, agent := range []string{"nobody", "archived"} {
		if code, _ := r.consent(admin, r.authorize(client), agent); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", agent, code)
		}
	}
	other := admin
	other.Tenant = "t2"
	if code, _ := r.consent(other, r.authorize(client), "a"); code != http.StatusBadRequest {
		t.Fatalf("another account's agent: %d", code)
	}
}

func TestCodeExchangeChecks(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	_, code := r.consent(admin, r.authorize(client), "a")
	if st, tk := r.exchange(client, code, verifier+"x"); st != http.StatusBadRequest || tk.Error != "invalid_grant" {
		t.Fatalf("bad verifier: %d %+v", st, tk)
	}
	// The failed attempt used the code; a replay ends the grant.
	if st, _ := r.exchange(client, code, verifier); st != http.StatusBadRequest {
		t.Fatalf("replayed code accepted: %d", st)
	}

	_, code = r.consent(admin, r.authorize(client), "b")
	*r.now = r.now.Add(2 * time.Minute)
	if st, _ := r.exchange(client, code, verifier); st != http.StatusBadRequest {
		t.Fatalf("expired code accepted: %d", st)
	}
}

func TestReplayedCodeRevokesTheKey(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	_, code := r.consent(admin, r.authorize(client), "a")
	_, tk := r.exchange(client, code, verifier)
	k, _ := r.keyOf(tk.Access)
	if st, _ := r.exchange(client, code, verifier); st != http.StatusBadRequest {
		t.Fatal("replay accepted")
	}
	if _, err := r.keyOf(tk.Access); err == nil {
		t.Fatal("token still works after a replayed code")
	}
	if _, err := r.keys.GetByHash(context.Background(), k.KeyHash); err == nil {
		t.Fatal("the grant's key was not revoked")
	}
}

func TestRefreshRotatesAndReuseEndsTheGrant(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	tk := r.connect(client, "a")
	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tk.Refresh}, "client_id": {client}}
	st, next := r.token(refresh)
	if st != http.StatusOK || next.Refresh == tk.Refresh {
		t.Fatalf("refresh: %d %+v", st, next)
	}
	k1, _ := r.keyOf(tk.Access)
	k2, err := r.keyOf(next.Access)
	if err != nil || k1.ID != k2.ID {
		t.Fatal("refreshed token stands for a different key")
	}
	// Reusing the rotated refresh token ends the grant and its key.
	if st, _ := r.token(refresh); st != http.StatusBadRequest {
		t.Fatalf("reused refresh accepted: %d", st)
	}
	if _, err := r.keyOf(next.Access); err == nil {
		t.Fatal("token works after refresh reuse")
	}
	if _, err := r.keys.GetByHash(context.Background(), k2.KeyHash); err == nil {
		t.Fatal("key not revoked after refresh reuse")
	}
	// The access token expires after an hour.
	tk = r.connect(client, "b")
	*r.now = r.now.Add(oauth.AccessTTL + time.Second)
	if _, err := r.keyOf(tk.Access); err == nil {
		t.Fatal("expired access token accepted")
	}
}

// Reconnecting as the same agent replaces the connection; another agent
// is a separate one.
func TestReconnectReplaces(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	first := r.connect(client, "a")
	other := r.connect(client, "b")
	k1, _ := r.keyOf(first.Access)

	// The consent page offers the agent used last.
	req := httptest.NewRequest("GET", "/v1/oauth/requests/"+r.authorize(client), nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), admin))
	var info oauth.RequestInfo
	_ = json.Unmarshal(r.do(r.authed, req).Body.Bytes(), &info)
	if info.PreviousAgentID != "b" || !info.CanConsent || info.ClientName != "Claude" || info.RedirectHost != "127.0.0.1:53682" {
		t.Fatalf("request info %+v", info)
	}

	again := r.connect(client, "a")
	if _, err := r.keyOf(first.Access); err == nil {
		t.Fatal("replaced connection still works")
	}
	if _, err := r.keys.GetByHash(context.Background(), k1.KeyHash); err == nil {
		t.Fatal("replaced connection's key not revoked")
	}
	if _, err := r.keyOf(again.Access); err != nil {
		t.Fatal(err)
	}
	if _, err := r.keyOf(other.Access); err != nil {
		t.Fatal("another agent's connection was ended")
	}
}

func TestGrantEnds(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)

	// Idle for 90 days.
	idle := r.connect(client, "idle")
	k, _ := r.keyOf(idle.Access)
	*r.now = r.now.Add(oauth.IdleLimit + time.Hour)
	st, _ := r.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {idle.Refresh}, "client_id": {client}})
	if st != http.StatusBadRequest {
		t.Fatalf("idle grant refreshed: %d", st)
	}
	if _, err := r.keys.GetByHash(context.Background(), k.KeyHash); err == nil {
		t.Fatal("idle grant's key not revoked")
	}

	// The granter loses the role.
	lost := r.connect(client, "lost")
	k, _ = r.keyOf(lost.Access)
	*r.granter = false
	if _, err := r.keyOf(lost.Access); err == nil {
		t.Fatal("token works after the granter lost the role")
	}
	if _, err := r.keys.GetByHash(context.Background(), k.KeyHash); err == nil {
		t.Fatal("key not revoked after the granter lost the role")
	}
	*r.granter = true

	// Revoked by the client (RFC 7009).
	rv := r.connect(client, "revoked")
	k, _ = r.keyOf(rv.Access)
	req := httptest.NewRequest("POST", "/oauth/revoke", strings.NewReader(url.Values{"token": {rv.Refresh}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := r.do(r.public, req); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	if _, err := r.keyOf(rv.Access); err == nil {
		t.Fatal("token works after revoke")
	}
	if _, err := r.keys.GetByHash(context.Background(), k.KeyHash); err == nil {
		t.Fatal("key not revoked after revoke")
	}

	// Revoking the key in KIFF Cloud ends what the token can do.
	direct := r.connect(client, "direct")
	k, _ = r.keyOf(direct.Access)
	_ = r.keys.Revoke(context.Background(), k.ID, *r.now)
	st, _ = r.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {direct.Refresh}, "client_id": {client}})
	if st != http.StatusBadRequest {
		t.Fatalf("grant refreshed after its key was revoked: %d", st)
	}
	if _, err := r.srv.ResolveAccessToken(context.Background(), direct.Access); err == nil {
		t.Fatal("token resolves after its key was revoked")
	}
}

func TestDenyAndAuthorizeErrors(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	b, _ := json.Marshal(map[string]string{"request_id": r.authorize(client)})
	req := httptest.NewRequest("POST", "/v1/oauth/deny", bytes.NewReader(b))
	viewer := admin
	viewer.MemberRole = membership.RoleViewer
	req = req.WithContext(auth.WithIdentity(req.Context(), viewer))
	rec := r.do(r.authed, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "access_denied") {
		t.Fatalf("deny: %d %s", rec.Code, rec.Body.String())
	}
	// An unregistered redirect is never redirected to.
	q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {"https://evil.example/cb"},
		"code_challenge": {challenge()}, "code_challenge_method": {"S256"}}
	if rec := r.do(r.public, httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil)); rec.Code != http.StatusBadRequest {
		t.Fatalf("unregistered redirect: %d", rec.Code)
	}
	// No PKCE, or another resource: an error back to the client.
	for _, bad := range []url.Values{{"code_challenge_method": {"plain"}}, {"resource": {"https://api.kiff.dev"}}} {
		q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {redirect},
			"code_challenge": {challenge()}, "code_challenge_method": {"S256"}}
		for k, v := range bad {
			q[k] = v
		}
		rec := r.do(r.public, httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil))
		if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, redirect) || !strings.Contains(loc, "error=") {
			t.Fatalf("%v: %d %s", bad, rec.Code, loc)
		}
	}
}

// --- connect links (Amendment 1) ---

// as runs an authed request as id.
func (r *rig) as(id auth.Identity, method, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req = req.WithContext(auth.WithIdentity(req.Context(), id))
	return r.do(r.authed, req)
}

func (r *rig) createLink(id auth.Identity, agent string) oauth.LinkView {
	rec := r.as(id, "POST", "/v1/me/agents/"+agent+"/connect-links", nil)
	if rec.Code != http.StatusCreated {
		r.t.Fatalf("create link: %d %s", rec.Code, rec.Body.String())
	}
	var l oauth.LinkView
	_ = json.Unmarshal(rec.Body.Bytes(), &l)
	return l
}

// authorizeFor starts a sign-in for resource; it returns the request id,
// or "" and the error redirect.
func (r *rig) authorizeFor(client, resource string) (string, string) {
	q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {redirect}, "state": {"xyz"},
		"code_challenge": {challenge()}, "code_challenge_method": {"S256"}, "resource": {resource}}
	rec := r.do(r.public, httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Host == "app.kiff.dev" {
		return loc.Query().Get("request"), ""
	}
	return "", rec.Header().Get("Location")
}

func (r *rig) info(id auth.Identity, requestID string) (int, oauth.RequestInfo) {
	rec := r.as(id, "GET", "/v1/oauth/requests/"+requestID, nil)
	var info oauth.RequestInfo
	_ = json.Unmarshal(rec.Body.Bytes(), &info)
	return rec.Code, info
}

// The link names the agent: consent only grants, whatever agent the body
// names, and the token is for the link's URL only.
func TestConnectLinkFixesTheAgent(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	l := r.createLink(admin, "support")
	if l.URL != "https://mcp.kiff.dev/a/"+l.ID+"/mcp" || l.AgentID != "support" {
		t.Fatalf("link %+v", l)
	}
	req, _ := r.authorizeFor(client, l.URL)
	if code, info := r.info(admin, req); code != http.StatusOK || info.AgentID != "support" || !info.CanConsent {
		t.Fatalf("info %d %+v", code, info)
	}
	_, code := r.consent(admin, req, "someone-else")
	st, tk := r.exchangeFor(client, code, verifier, l.URL)
	if st != http.StatusOK {
		t.Fatalf("exchange with the link URL: %d", st)
	}
	// Refreshing names the same resource.
	if st, _ := r.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tk.Refresh}, "client_id": {client},
		"resource": {resource}}); st != http.StatusBadRequest {
		t.Fatalf("refresh for the generic URL accepted on a link grant: %d", st)
	}
	g, err := r.srv.ResolveAccessToken(context.Background(), tk.Access)
	if err != nil || g.Resource != l.URL {
		t.Fatalf("resolve %+v %v", g, err)
	}
	k, _ := r.keys.GetByHash(context.Background(), g.KeyHash)
	if k.AgentID != "support" {
		t.Fatalf("key bound to %q, not the link's agent", k.AgentID)
	}
}

func TestConnectLinkOfAnotherAccount(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	l := r.createLink(admin, "support")
	req, _ := r.authorizeFor(client, l.URL)
	other := admin
	other.Tenant = "t2"
	if _, info := r.info(other, req); !info.WrongAccount || info.CanConsent || info.AgentID != "" {
		t.Fatalf("info for another account %+v", info)
	}
	if code, _ := r.consent(other, req, ""); code != http.StatusForbidden {
		t.Fatalf("another account consented: %d", code)
	}
}

// Revoking a link ends what was made through it, including a sign-in
// started before the revoke (A4).
func TestRevokedLinkStopsEverything(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	l := r.createLink(admin, "support")

	req, _ := r.authorizeFor(client, l.URL)
	_, code := r.consent(admin, req, "")
	_, tk := r.exchangeFor(client, code, verifier, l.URL)
	k, _ := r.keyOf(tk.Access)

	pendingReq, _ := r.authorizeFor(client, l.URL)
	_, pendingCode := r.consent(admin, pendingReq, "")
	raceReq, _ := r.authorizeFor(client, l.URL)

	if rec := r.as(admin, "POST", "/v1/oauth/connect-links/"+l.ID+"/revoke", nil); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := r.keyOf(tk.Access); err == nil {
		t.Fatal("token works after its link was revoked")
	}
	if _, err := r.keys.GetByHash(context.Background(), k.KeyHash); err == nil {
		t.Fatal("key not revoked with its link")
	}
	if st, _ := r.exchangeFor(client, pendingCode, verifier, l.URL); st != http.StatusBadRequest {
		t.Fatalf("pending grant redeemed after revoke: %d", st)
	}
	if code, _ := r.consent(admin, raceReq, ""); code == http.StatusOK {
		t.Fatal("consent through a revoked link")
	}
	if _, loc := r.authorizeFor(client, l.URL); !strings.Contains(loc, "error=invalid_target") {
		t.Fatalf("new sign-in through a revoked link: %q", loc)
	}
	if rec := r.as(admin, "POST", "/v1/oauth/connect-links/"+l.ID+"/revoke", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("second revoke: %d", rec.Code)
	}
}

// Archiving the agent stops its links (A1).
func TestArchivedAgentStopsItsLinks(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	l := r.createLink(admin, "billing")
	req, _ := r.authorizeFor(client, l.URL)
	_, code := r.consent(admin, req, "")
	_, tk := r.exchangeFor(client, code, verifier, l.URL)
	r.archived["billing"] = true
	if _, err := r.keyOf(tk.Access); err == nil {
		t.Fatal("token works after its agent was archived")
	}
	if _, loc := r.authorizeFor(client, l.URL); !strings.Contains(loc, "error=invalid_target") {
		t.Fatalf("sign-in through an archived agent's link: %q", loc)
	}
}

func TestLinkRulesAndRoles(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	approver := admin
	approver.MemberRole = membership.RoleApprover
	if rec := r.as(approver, "POST", "/v1/me/agents/support/connect-links", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("approver created a link: %d", rec.Code)
	}
	if rec := r.as(admin, "POST", "/v1/me/agents/nobody/connect-links", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("link for an unknown agent: %d", rec.Code)
	}
	for _, bad := range []string{"https://mcp.kiff.dev/a/cl_nope/mcp", "https://mcp.kiff.dev/a/../mcp", "https://evil.example/a/cl_abcdefghij/mcp"} {
		if _, loc := r.authorizeFor(client, bad); !strings.Contains(loc, "error=invalid_target") {
			t.Fatalf("%s: %q", bad, loc)
		}
	}
	// A person on the account who is not an admin or editor sees Decline only.
	l := r.createLink(admin, "support")
	req, _ := r.authorizeFor(client, l.URL)
	if _, info := r.info(approver, req); info.CanConsent || info.AgentID != "support" {
		t.Fatalf("approver info %+v", info)
	}
	if code, _ := r.consent(approver, req, ""); code != http.StatusForbidden {
		t.Fatalf("approver consented: %d", code)
	}
	// Listing.
	rec := r.as(admin, "GET", "/v1/me/agents/support/connect-links", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), l.ID) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
}

// On the generic URL the consent API creates "a new agent for this app"
// (A5), and refuses a name the account already uses.
func TestNewAgentOnTheGenericURL(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	consentNew := func(name string) (int, string) {
		req := r.authorize(client)
		rec := r.as(admin, "POST", "/v1/oauth/consent", map[string]string{"request_id": req, "new_agent": name})
		var out struct {
			RedirectTo string `json:"redirect_to"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		u, _ := url.Parse(out.RedirectTo)
		return rec.Code, u.Query().Get("code")
	}
	st, code := consentNew("Claude Code")
	if st != http.StatusOK || !r.created["t1/Claude Code"] {
		t.Fatalf("new agent: %d created=%v", st, r.created)
	}
	_, tk := r.exchange(client, code, verifier)
	if k, err := r.keyOf(tk.Access); err != nil || k.AgentID != "Claude Code" {
		t.Fatalf("key %+v %v", k, err)
	}
	if st, _ := consentNew("support"); st != http.StatusConflict {
		t.Fatalf("existing name accepted as new: %d", st)
	}
	// A generic-URL token is not a link token.
	if g, _ := r.srv.ResolveAccessToken(context.Background(), tk.Access); g.Resource != resource {
		t.Fatalf("generic token resource %q", g.Resource)
	}
}

// A link grant's code is exchanged for the link's URL; the generic URL is
// a different resource and ends the grant.
func TestLinkCodeIsExchangedForTheLinkURL(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	l := r.createLink(admin, "support")
	req, _ := r.authorizeFor(client, l.URL)
	_, code := r.consent(admin, req, "")
	st, tk := r.exchangeFor(client, code, verifier, resource)
	if st != http.StatusBadRequest || tk.Error != "invalid_target" {
		t.Fatalf("link code exchanged for the generic URL: %d %+v", st, tk)
	}
	// Without a resource the grant's own is implied.
	req, _ = r.authorizeFor(client, l.URL)
	_, code = r.consent(admin, req, "")
	if st, _ := r.exchangeFor(client, code, verifier, ""); st != http.StatusOK {
		t.Fatalf("link code without resource: %d", st)
	}
}

// Two submissions of one request create one agent: the request is
// claimed before the agent is created.
func TestConcurrentNewAgentConsentCreatesOneAgent(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	req := r.authorize(client)
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := r.as(admin, "POST", "/v1/oauth/consent", map[string]string{"request_id": req, "new_agent": fmt.Sprintf("agent-%d", i)})
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == http.StatusOK {
			ok++
		}
	}
	r.mu.Lock()
	created := len(r.created)
	r.mu.Unlock()
	if ok != 1 || created != 1 {
		t.Fatalf("successes %d, agents created %d (codes %v)", ok, created, codes)
	}
}

// --- device flow (D2b) and Connections (D2c) ---

func (r *rig) registerDevice() string {
	b, _ := json.Marshal(map[string]any{"client_name": "kiff-cli", "grant_types": []string{oauth.DeviceGrantType}})
	rec := r.do(r.public, httptest.NewRequest("POST", "/oauth/register", bytes.NewReader(b)))
	if rec.Code != http.StatusCreated {
		r.t.Fatalf("register device client: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out.ClientID
}

type deviceStart struct {
	DeviceCode string `json:"device_code"`
	UserCode   string `json:"user_code"`
	VerifyURI  string `json:"verification_uri"`
	Complete   string `json:"verification_uri_complete"`
	Error      string `json:"error"`
}

func (r *rig) startDevice(client, res string) (int, deviceStart) {
	form := url.Values{"client_id": {client}}
	if res != "" {
		form.Set("resource", res)
	}
	req := httptest.NewRequest("POST", "/oauth/device_authorization", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := r.do(r.public, req)
	var d deviceStart
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	return rec.Code, d
}

func (r *rig) poll(client, deviceCode string) (int, tokens) {
	return r.token(url.Values{"grant_type": {oauth.DeviceGrantType}, "device_code": {deviceCode}, "client_id": {client}})
}

func TestDeviceFlowNewAgent(t *testing.T) {
	r := newRig(t)
	client := r.registerDevice()
	st, d := r.startDevice(client, "")
	if st != http.StatusOK || d.VerifyURI != "https://app.kiff.dev/oauth/device" || len(d.UserCode) != 9 || d.UserCode[4] != '-' {
		t.Fatalf("start: %d %+v", st, d)
	}
	if _, tk := r.poll(client, d.DeviceCode); tk.Error != "authorization_pending" {
		t.Fatalf("first poll: %+v", tk)
	}
	if _, tk := r.poll(client, d.DeviceCode); tk.Error != "slow_down" {
		t.Fatalf("immediate second poll: %+v", tk)
	}
	// The code is accepted typed loosely.
	loose := strings.ToLower(strings.ReplaceAll(d.UserCode, "-", " "))
	rec := r.as(admin, "GET", "/v1/oauth/device/"+url.PathEscape(loose), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"client_name":"kiff-cli"`) {
		t.Fatalf("info: %d %s", rec.Code, rec.Body.String())
	}
	rec = r.as(admin, "POST", "/v1/oauth/device/approve", map[string]string{"user_code": d.UserCode, "new_agent": "agent-cli"})
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	*r.now = r.now.Add(6 * time.Second)
	st, tk := r.poll(client, d.DeviceCode)
	if st != http.StatusOK || !strings.HasPrefix(tk.Access, auth.OAuthAccessTokenPrefix) {
		t.Fatalf("poll after approve: %d %+v", st, tk)
	}
	k, err := r.keyOf(tk.Access)
	if err != nil || k.AgentID != "agent-cli" || !strings.HasSuffix(k.Label, "(connected with a code)") {
		t.Fatalf("key %+v %v", k, err)
	}
	// A second redemption ends the connection.
	if _, tk2 := r.poll(client, d.DeviceCode); tk2.Error != "invalid_grant" {
		t.Fatalf("replayed device code: %+v", tk2)
	}
	if _, err := r.keyOf(tk.Access); err == nil {
		t.Fatal("token works after a replayed device code")
	}
}

func TestDeviceFlowThroughALink(t *testing.T) {
	r := newRig(t)
	client := r.registerDevice()
	l := r.createLink(admin, "support")
	_, d := r.startDevice(client, l.URL)
	rec := r.as(admin, "GET", "/v1/oauth/device/"+d.UserCode, nil)
	if !strings.Contains(rec.Body.String(), `"agent_id":"support"`) {
		t.Fatalf("info: %s", rec.Body.String())
	}
	other := admin
	other.Tenant = "t2"
	if rec := r.as(other, "POST", "/v1/oauth/device/approve", map[string]string{"user_code": d.UserCode}); rec.Code != http.StatusForbidden {
		t.Fatalf("another account approved: %d", rec.Code)
	}
	approver := admin
	approver.MemberRole = membership.RoleApprover
	if rec := r.as(approver, "POST", "/v1/oauth/device/approve", map[string]string{"user_code": d.UserCode}); rec.Code != http.StatusForbidden {
		t.Fatalf("approver approved: %d", rec.Code)
	}
	if rec := r.as(admin, "POST", "/v1/oauth/device/approve", map[string]string{"user_code": d.UserCode, "agent_id": "billing"}); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	_, tk := r.poll(client, d.DeviceCode)
	g, err := r.srv.ResolveAccessToken(context.Background(), tk.Access)
	if err != nil || g.Resource != l.URL {
		t.Fatalf("resolve %+v %v", g, err)
	}
	if k, _ := r.keys.GetByHash(context.Background(), g.KeyHash); k.AgentID != "support" {
		t.Fatalf("key bound to %q, not the link's agent", k.AgentID)
	}
}

func TestDeviceFlowDenyAndExpiry(t *testing.T) {
	r := newRig(t)
	client := r.registerDevice()
	_, d := r.startDevice(client, "")
	viewer := admin
	viewer.MemberRole = membership.RoleViewer
	if rec := r.as(viewer, "POST", "/v1/oauth/device/deny", map[string]string{"user_code": d.UserCode}); rec.Code != http.StatusOK {
		t.Fatalf("deny: %d", rec.Code)
	}
	if _, tk := r.poll(client, d.DeviceCode); tk.Error != "access_denied" {
		t.Fatalf("after deny: %+v", tk)
	}
	_, d = r.startDevice(client, "")
	*r.now = r.now.Add(oauth.DeviceTTL + time.Second)
	if _, tk := r.poll(client, d.DeviceCode); tk.Error != "expired_token" {
		t.Fatalf("after expiry: %+v", tk)
	}
	if rec := r.as(admin, "POST", "/v1/oauth/device/approve", map[string]string{"user_code": d.UserCode, "agent_id": "support"}); rec.Code != http.StatusNotFound {
		t.Fatalf("expired code approved: %d", rec.Code)
	}
	if _, d := r.startDevice(client, "https://mcp.kiff.dev/a/cl_nopenopenope/mcp"); d.Error != "invalid_target" {
		t.Fatalf("unknown link: %+v", d)
	}
}

func TestConnectionsListAndRevoke(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	tk := r.connect(client, "support")
	rec := r.as(admin, "GET", "/v1/oauth/connections", nil)
	var out struct {
		Connections []oauth.ConnectionView `json:"connections"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Connections) != 1 || out.Connections[0].AgentID != "support" || out.Connections[0].Method != "sign-in" ||
		out.Connections[0].Status != "live" || out.Connections[0].KeyID == "" {
		t.Fatalf("connections %+v", out.Connections)
	}
	id := out.Connections[0].ID
	approver := admin
	approver.MemberRole = membership.RoleApprover
	if rec := r.as(approver, "POST", "/v1/oauth/grants/"+id+"/revoke", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("approver revoked: %d", rec.Code)
	}
	other := admin
	other.Tenant = "t2"
	if rec := r.as(other, "POST", "/v1/oauth/grants/"+id+"/revoke", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("another account revoked: %d", rec.Code)
	}
	if rec := r.as(admin, "POST", "/v1/oauth/grants/"+id+"/revoke", nil); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d", rec.Code)
	}
	if _, err := r.keyOf(tk.Access); err == nil {
		t.Fatal("token works after revoke")
	}
	// The refresh says the connection ended, not that the token was reused.
	_, rt := r.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tk.Refresh}, "client_id": {client}})
	if rt.Error != "invalid_grant" {
		t.Fatalf("refresh after revoke: %+v", rt)
	}
	rec = r.as(admin, "GET", "/v1/oauth/connections", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Connections) != 1 || out.Connections[0].Status != "ended" || out.Connections[0].EndedReason != oauth.EndedRevoked {
		t.Fatalf("after revoke %+v", out.Connections)
	}
	agentKey := auth.Identity{Subject: "k", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true, AgentID: "support"}
	if rec := r.as(agentKey, "GET", "/v1/oauth/connections", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("agent key listed connections: %d", rec.Code)
	}
}

// A device code whose approval fails after the claim tells the terminal at
// once, instead of polling until the code expires.
func TestDeviceApprovalFailureEndsTheCode(t *testing.T) {
	r := newRig(t)
	client := r.registerDevice()
	_, d := r.startDevice(client, "")
	r.mu.Lock()
	r.failCreate = true
	r.mu.Unlock()
	rec := r.as(admin, "POST", "/v1/oauth/device/approve", map[string]string{"user_code": d.UserCode, "new_agent": "agent-boom"})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("approve with a failing create: %d %s", rec.Code, rec.Body.String())
	}
	if _, tk := r.poll(client, d.DeviceCode); tk.Error != "access_denied" {
		t.Fatalf("poll after a failed approval: %+v", tk)
	}
}

func TestUserCodesAreWellFormedAndUnbiased(t *testing.T) {
	counts := map[rune]int{}
	for i := 0; i < 4000; i++ {
		c := oauth.NormalizeUserCode(oauth.NewUserCodeForTest())
		if c == "" {
			t.Fatal("malformed user code")
		}
		for _, r := range strings.ReplaceAll(c, "-", "") {
			counts[r]++
		}
	}
	// 32000 letters over 20: about 1600 each. The old modulo gave the first
	// 16 letters 13/256 and the last 4 12/256: about 8% apart.
	for r, n := range counts {
		if n < 1400 || n > 1800 {
			t.Errorf("letter %c drawn %d times", r, n)
		}
	}
}

func TestDeviceCodeLookupsAreLimited(t *testing.T) {
	// View, approve and deny all count: guessing cannot go around the
	// limit through approve or deny.
	for _, try := range []func(r *rig) int{
		func(r *rig) int { return r.as(admin, "GET", "/v1/oauth/device/BCDF-GHJK", nil).Code },
		func(r *rig) int {
			return r.as(admin, "POST", "/v1/oauth/device/approve", map[string]string{"user_code": "BCDF-GHJK", "agent_id": "support"}).Code
		},
		func(r *rig) int {
			return r.as(admin, "POST", "/v1/oauth/device/deny", map[string]string{"user_code": "BCDF-GHJK"}).Code
		},
	} {
		r := newRig(t)
		limited := false
		for i := 0; i < 25; i++ {
			if try(r) == http.StatusTooManyRequests {
				limited = true
				break
			}
		}
		if !limited {
			t.Fatal("25 guesses by one person were not limited")
		}
	}
}

// Two approvals of one code: the one that loses the claim must not end the
// winner's approval. The interleaving is forced: B reads
// the code as pending, A claims it and pauses inside agent creation, B loses
// the claim and runs its failure path, then A finishes.
func TestConcurrentDeviceApprovalsOneWins(t *testing.T) {
	r := newRig(t)
	client := r.registerDevice()
	_, d := r.startDevice(client, "")

	bReady, aClaimed, bDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls int32
	oauth.SetAfterPendingDeviceForTest(r.srv, func() {
		if atomic.AddInt32(&calls, 1) == 1 { // B, started first
			close(bReady)
			<-aClaimed
			return
		}
		// A: let B read "pending" first.
		<-bReady
	})
	r.mu.Lock()
	r.onCreate = func() { close(aClaimed); <-bDone } // A pauses after its claim
	r.mu.Unlock()

	var bCode, aCode int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(bDone)
		bCode = r.as(admin, "POST", "/v1/oauth/device/approve", map[string]string{"user_code": d.UserCode, "agent_id": "support"}).Code
	}()
	<-bReady
	aCode = r.as(admin, "POST", "/v1/oauth/device/approve", map[string]string{"user_code": d.UserCode, "new_agent": "agent-a"}).Code
	wg.Wait()
	if aCode != http.StatusOK || bCode == http.StatusOK {
		t.Fatalf("A (winner) %d, B (loser) %d", aCode, bCode)
	}
	st, tk := r.poll(client, d.DeviceCode)
	if st != http.StatusOK || !strings.HasPrefix(tk.Access, auth.OAuthAccessTokenPrefix) {
		t.Fatalf("the winning approval was ended by the losing one: %d %+v", st, tk)
	}
}

// --- Claude as a connector: metadata documents, redirects, registration ---

const claudeCode = "https://claude.ai/oauth/claude-code-client-metadata"

// claudeCodeDoc is the document Claude Code serves, as published.
func claudeCodeDoc(id string) string {
	return `{"client_id":"` + id + `","client_name":"Claude Code","client_uri":"https://claude.ai",` +
		`"redirect_uris":["http://localhost/callback","http://127.0.0.1/callback"],` +
		`"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none"}`
}

// authorizeStatus runs /oauth/authorize and returns the status and Location.
func (r *rig) authorizeStatus(client, redirectURI string) (int, string) {
	q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {redirectURI}, "state": {"xyz"},
		"code_challenge": {challenge()}, "code_challenge_method": {"S256"}, "resource": {resource}}
	rec := r.do(r.public, httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil))
	return rec.Code, rec.Header().Get("Location")
}

func TestMetadataDocumentIsAdvertised(t *testing.T) {
	r := newRig(t)
	rec := r.do(r.public, httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil))
	var md map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &md)
	// Claude uses a metadata document only when both are present.
	if md["client_id_metadata_document_supported"] != true {
		t.Fatalf("metadata: %v", md)
	}
	if m, _ := md["token_endpoint_auth_methods_supported"].([]any); len(m) != 1 || m[0] != "none" {
		t.Fatalf("auth methods: %v", md["token_endpoint_auth_methods_supported"])
	}
}

// Claude Code connects with its metadata document instead of registering,
// on a port it picks per session, and the token is still one agent's key.
func TestMetadataDocumentClientConnectsAsOneAgent(t *testing.T) {
	r := newRig(t)
	r.docs[claudeCode] = claudeCodeDoc(claudeCode)
	tk := r.connect(claudeCode, "claude-support")
	k, err := r.keyOf(tk.Access)
	if err != nil {
		t.Fatal(err)
	}
	if k.AgentID != "claude-support" || len(k.Roles) != 1 || k.Roles[0] != auth.RoleGatewayAgent {
		t.Fatalf("key %+v", k)
	}
	// The consent page names the client from its document.
	_, req := r.authorizeStatus(claudeCode, "http://localhost:3118/callback")
	loc, _ := url.Parse(req)
	if st, info := r.info(admin, loc.Query().Get("request")); st != http.StatusOK || info.ClientName != "Claude Code" {
		t.Fatalf("info: %d %+v", st, info)
	}
	// The document is reused, not fetched for every sign-in.
	if r.fetched != 1 {
		t.Fatalf("fetched %d times", r.fetched)
	}
	// Refresh works for the URL client id, and only for it.
	if st, out := r.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tk.Refresh}, "client_id": {"kc_other"}}); st == http.StatusOK {
		t.Fatalf("refresh with another client id: %+v", out)
	}
	if st, out := r.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tk.Refresh}, "client_id": {claudeCode}}); st != http.StatusOK {
		t.Fatalf("refresh: %d %+v", st, out)
	}
}

func TestMetadataDocumentRules(t *testing.T) {
	r := newRig(t)
	bad := map[string]string{
		// The document names another client.
		"https://claude.ai/oauth/a": claudeCodeDoc("https://claude.ai/oauth/b"),
		// A confidential client.
		"https://claude.ai/oauth/c": strings.Replace(claudeCodeDoc("https://claude.ai/oauth/c"), `"none"`, `"client_secret_basic"`, 1),
		// A redirect KIFF does not allow for any client.
		"https://claude.ai/oauth/d": strings.Replace(claudeCodeDoc("https://claude.ai/oauth/d"), "http://localhost/callback", "https://evil.example/cb", 1),
		// Not JSON.
		"https://claude.ai/oauth/e": "<html>",
		// A host that is not trusted, even with a valid document.
		"https://evil.example/oauth/f": claudeCodeDoc("https://evil.example/oauth/f"),
	}
	for u, d := range bad {
		r.docs[u] = d
	}
	ids := []string{"https://claude.ai/oauth/a", "https://claude.ai/oauth/c", "https://claude.ai/oauth/d",
		"https://claude.ai/oauth/e", "https://evil.example/oauth/f", "https://claude.ai/oauth/missing",
		"https://claude.ai/oauth/moved", "https://claude.ai:8443/oauth/x", "https://claude.ai/oauth/x?y=1", "https://claude.ai/"}
	for _, id := range ids {
		if st, loc := r.authorizeStatus(id, redirect); st != http.StatusBadRequest {
			t.Errorf("%s: %d %s", id, st, loc)
		}
	}
	// Ids on other hosts, or not well formed, are never fetched.
	if r.fetched != 6 {
		t.Fatalf("fetched %d documents, want 6 (the claude.ai ones only)", r.fetched)
	}
	// A refused document leaves no client behind.
	if st, _ := r.authorizeStatus("https://claude.ai/oauth/a", redirect); st != http.StatusBadRequest {
		t.Fatal("a refused document was accepted on retry")
	}
}

func TestLoopbackRedirectOnAnyPort(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect, "https://claude.ai/api/mcp/auth_callback")
	for _, ok := range []string{redirect, "http://127.0.0.1:1234/callback", "https://claude.ai/api/mcp/auth_callback"} {
		if st, loc := r.authorizeStatus(client, ok); st != http.StatusFound || !strings.HasPrefix(loc, "https://app.kiff.dev/") {
			t.Errorf("%s refused: %d %s", ok, st, loc)
		}
	}
	// Only the port may differ, and only for loopback.
	for _, bad := range []string{"http://127.0.0.1:1234/other", "http://localhost:53682/callback",
		"https://claude.ai:8443/api/mcp/auth_callback", "http://127.0.0.1:1234/callback#x", "http://u@127.0.0.1:1234/callback"} {
		if st, _ := r.authorizeStatus(client, bad); st != http.StatusBadRequest {
			t.Errorf("%s accepted: %d", bad, st)
		}
	}
}

// registerFrom registers a client as if from ip, behind Caddy.
func (r *rig) registerFrom(ip string) int {
	b, _ := json.Marshal(map[string]any{"client_name": "Claude", "redirect_uris": []string{redirect}})
	req := httptest.NewRequest("POST", "/oauth/register", bytes.NewReader(b))
	req.Header.Set("X-Forwarded-For", ip)
	return r.do(r.public, req).Code
}

// Registration is limited per address: one address running out never
// blocks another, and an address in a shared network (Anthropic's) gets a
// larger budget of its own.
func TestRegistrationLimitIsPerAddress(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 20; i++ {
		if st := r.registerFrom("203.0.113.7"); st != http.StatusCreated {
			t.Fatalf("registration %d: %d", i, st)
		}
	}
	if st := r.registerFrom("203.0.113.7"); st != http.StatusTooManyRequests {
		t.Fatalf("21st registration from one address: %d", st)
	}
	if st := r.registerFrom("203.0.113.8"); st != http.StatusCreated {
		t.Fatalf("another address was blocked: %d", st)
	}
	for i := 0; i < 100; i++ {
		if st := r.registerFrom("160.79.104.10"); st != http.StatusCreated {
			t.Fatalf("shared address, registration %d: %d", i, st)
		}
	}
	// A client cannot claim the shared budget by sending its own header:
	// only the address Caddy appended counts.
	for i := 0; i < 20; i++ {
		r.registerFrom("160.79.104.11, 198.51.100.9")
	}
	if st := r.registerFrom("160.79.104.11, 198.51.100.9"); st != http.StatusTooManyRequests {
		t.Fatalf("spoofed shared address got the larger budget: %d", st)
	}
}

// count returns how many rows of table match the condition.
func (r *rig) count(table, where string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM cloud_meta.`+table+` WHERE `+where, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// Ended sign-in connections, with their tokens and codes, are deleted
// once they are older than the cutoff; so are connections never redeemed
// and revoked links nothing names. Live connections and links stay.
func TestPurgeEndedKeepsLiveConnections(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	live := r.connect(client, "support")
	liveGrant, err := r.srv.ResolveAccessToken(context.Background(), live.Access)
	if err != nil {
		t.Fatal(err)
	}

	// Ended: revoked by the client.
	ended := r.connect(client, "billing")
	endedGrant, _ := r.srv.ResolveAccessToken(context.Background(), ended.Access)
	req := httptest.NewRequest("POST", "/oauth/revoke", strings.NewReader(url.Values{"token": {ended.Refresh}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := r.do(r.public, req); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	// Never redeemed: consent given, the code never exchanged.
	*r.now = r.now.Add(time.Second)
	if st, _ := r.consent(admin, r.authorize(client), "claude-desk"); st != http.StatusOK {
		t.Fatalf("consent: %d", st)
	}
	// A revoked link nothing names, and a live link.
	gone, kept := r.createLink(admin, "support"), r.createLink(admin, "support")
	if rec := r.as(admin, "POST", "/v1/oauth/connect-links/"+gone.ID+"/revoke", nil); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	if n := r.count("oauth_grants", "true"); n != 3 {
		t.Fatalf("grants before = %d, want 3", n)
	}
	// Each redeemed grant owns one api_keys row (the rig keeps keys in
	// memory, so put the two rows in Postgres here): the ended grant's is
	// revoked, the live grant's is not.
	if err := tenant.ApplyCloudMeta(context.Background(), r.pool); err != nil {
		t.Fatal(err)
	}
	if err := auth.ApplyAPIKeysSchema(context.Background(), r.pool); err != nil {
		t.Fatal(err)
	}
	keyTenant := ulid.Make().String()
	if err := tenant.NewPostgresStore(r.pool).Create(context.Background(), tenant.Tenant{ID: keyTenant, Slug: "purge-" + strings.ToLower(keyTenant),
		DisplayName: "purge test", Status: tenant.StatusActive, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	keyOfGrant := func(grantID string) string {
		var id string
		if err := r.pool.QueryRow(context.Background(), `SELECT key_id FROM cloud_meta.oauth_grants WHERE id = $1`, grantID).Scan(&id); err != nil || id == "" {
			t.Fatalf("key of %s: %q %v", grantID, id, err)
		}
		return id
	}
	pgKeys := auth.NewPostgresAPIKeyStore(r.pool)
	endedKey, liveKey := keyOfGrant(endedGrant.GrantID), keyOfGrant(liveGrant.GrantID)
	for i, id := range []string{endedKey, liveKey} {
		if err := pgKeys.Insert(context.Background(), auth.APIKeyRecord{ID: id, Tenant: keyTenant, Subject: "user_admin",
			Roles: []string{auth.RoleGatewayAgent}, AgentID: "agent", KeyHash: sha256.Sum256([]byte(id + fmt.Sprint(i))), CreatedAt: *r.now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := pgKeys.Revoke(context.Background(), endedKey, *r.now); err != nil {
		t.Fatal(err)
	}

	store := oauth.NewStore(r.pool)
	// Not old enough yet: nothing goes.
	if p, err := store.PurgeEnded(context.Background(), r.now.Add(-time.Hour)); err != nil || p != (oauth.Purged{}) {
		t.Fatalf("early purge: %+v %v", p, err)
	}
	p, err := store.PurgeEnded(context.Background(), r.now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if p.Grants != 2 || p.Links != 1 || p.Keys != 1 || p.Tokens == 0 {
		t.Fatalf("purged %+v, want 2 grants, their tokens, 1 key, 1 link", p)
	}
	if r.count("api_keys", "id = $1", endedKey) != 0 || r.count("api_keys", "id = $1", liveKey) != 1 {
		t.Fatal("wrong api_keys rows purged: the ended grant's revoked key must go, the live grant's stay")
	}
	if r.count("oauth_grants", "id = $1", endedGrant.GrantID) != 0 || r.count("oauth_tokens", "grant_id = $1", endedGrant.GrantID) != 0 {
		t.Fatal("the ended connection or its tokens remain")
	}
	// Only the live connection's (used) code is left; expired codes of live
	// connections are a separate cleanup.
	if r.count("oauth_grants", "redeemed_at IS NULL") != 0 || r.count("oauth_codes", "grant_id <> $1", liveGrant.GrantID) != 0 {
		t.Fatal("the never-redeemed connection or its code remain")
	}
	if r.count("oauth_connect_links", "id = $1", gone.ID) != 0 || r.count("oauth_connect_links", "id = $1", kept.ID) != 1 {
		t.Fatal("wrong links purged")
	}
	// The live connection is untouched and its token still works.
	if r.count("oauth_grants", "id = $1", liveGrant.GrantID) != 1 {
		t.Fatal("the live connection was purged")
	}
	if _, err := r.keyOf(live.Access); err != nil {
		t.Fatalf("live token after purge: %v", err)
	}
}

// Sign-in requests, authorization codes and device codes that never
// became a connection are deleted once they have expired, and nothing inside
// its window is touched. A live connection keeps working without its code.
func TestPurgeExpiredDeletesWhatCanNeverBeUsed(t *testing.T) {
	r := newRig(t)
	_, client := r.register(redirect)
	live := r.connect(client, "support") // a used code
	_ = r.authorize(client)              // a sign-in request never finished
	if st, _ := r.startDevice(client, ""); st != http.StatusOK {
		t.Fatalf("device start: %d", st)
	}
	if r.count("oauth_requests", "true") == 0 || r.count("oauth_codes", "true") == 0 || r.count("oauth_device_codes", "grant_id = ''") == 0 {
		t.Fatal("the rig made no request, code or pending device code")
	}
	store := oauth.NewStore(r.pool)

	// Everything is still inside its window: nothing goes.
	if e, err := store.PurgeExpired(context.Background(), *r.now); err != nil || e != (oauth.Expired{}) {
		t.Fatalf("early purge: %+v %v", e, err)
	}
	// Past every window.
	e, err := store.PurgeExpired(context.Background(), r.now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if e.Requests == 0 || e.Codes == 0 || e.Devices == 0 {
		t.Fatalf("purged %+v, want requests, codes and device codes", e)
	}
	if r.count("oauth_requests", "true") != 0 || r.count("oauth_codes", "true") != 0 || r.count("oauth_device_codes", "grant_id = ''") != 0 {
		t.Fatal("expired rows remain")
	}
	// The connection itself is untouched: its token still works.
	if _, err := r.keyOf(live.Access); err != nil {
		t.Fatalf("live token after purge: %v", err)
	}
}
