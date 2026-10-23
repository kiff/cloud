package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/gateway/internal/kiffapi"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

// Access tokens rotate hourly, so retries are keyed by the key behind
// them: a retry after a refresh is the same caller. A gateway
// key keeps the id it always had, so calls in flight across a deploy
// keep their keys.
func TestCredentialIDIsStableAcrossTokenRefresh(t *testing.T) {
	me := kiffapi.Me{CredentialID: "key-1"}
	a, b := credentialID(OAuthTokenPrefix+"first", me), credentialID(OAuthTokenPrefix+"second", me)
	if a != b {
		t.Fatalf("two tokens of one grant: %s != %s", a, b)
	}
	if credentialID(OAuthTokenPrefix+"first", kiffapi.Me{CredentialID: "key-2"}) == a {
		t.Fatal("two grants share a credential id")
	}
	key := "kiff_live_t1_abc"
	if credentialID(key, me) == credentialID("kiff_live_t1_xyz", me) {
		t.Fatal("two gateway keys share a credential id")
	}
	if got := credentialID(key, kiffapi.Me{}); got != credentialID(key, me) {
		t.Fatal("a gateway key's id changed with /v1/me")
	}
}

func TestOAuthDiscovery(t *testing.T) {
	g := &Gateway{Resource: "https://mcp.kiff.dev/mcp", Issuer: "https://api.kiff.dev"}
	if got := g.ResourceMetadataURL(); got != "https://mcp.kiff.dev/.well-known/oauth-protected-resource/mcp" {
		t.Fatalf("metadata url %q", got)
	}
	rec := httptest.NewRecorder()
	g.ResourceMetadata(rec, httptest.NewRequest("GET", "/.well-known/oauth-protected-resource/mcp", nil))
	var md struct {
		Resource string   `json:"resource"`
		Servers  []string `json:"authorization_servers"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &md)
	if md.Resource != g.Resource || len(md.Servers) != 1 || md.Servers[0] != g.Issuer {
		t.Fatalf("metadata %+v", md)
	}

	srv := &mcp.Server{ResourceMetadata: g.ResourceMetadataURL(),
		Auth: func(*http.Request) (mcp.Handler, error) { return nil, errors.New("no key") }}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/mcp", strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized ||
		!strings.Contains(rec.Header().Get("WWW-Authenticate"), `resource_metadata="https://mcp.kiff.dev/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatalf("challenge: %d %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
	if (&Gateway{}).ResourceMetadataURL() != "" {
		t.Fatal("discovery on without a resource")
	}
}

// meAPI answers Me from a table; the gateway's other calls are unused.
type meAPI map[string]kiffapi.Me

func (m meAPI) Me(_ context.Context, key string) (kiffapi.Me, error) {
	if me, ok := m[key]; ok {
		return me, nil
	}
	return kiffapi.Me{}, errors.New("unknown key")
}
func (meAPI) Holds(context.Context, string, []string) ([]kiffapi.Hold, error) { return nil, nil }

func (meAPI) AgentCard(context.Context, string, string) (kiffapi.AgentCard, error) {
	return kiffapi.AgentCard{}, nil
}
func (meAPI) RequestToolCall(context.Context, string, string, string, string, time.Time) error {
	return nil
}
func (meAPI) Decide(context.Context, string, kiffapi.Proposal) (kiffapi.Decision, error) {
	return kiffapi.Decision{}, nil
}

func (meAPI) Trial(context.Context, string, kiffapi.TrialCall) (kiffapi.TrialDecision, error) {
	return kiffapi.TrialDecision{}, nil
}

func (meAPI) TrialOpen(context.Context, string, string) (bool, error) { return false, nil }

// A link URL accepts only a token issued for that URL; the generic URL
// refuses a link's token.
// Invariant: a sign-in token issued through one connect link works only at
// that link's URL.
// Category: authority
// Attack: use a token issued through a connect link at the general gateway URL
// Refused: the general URL refuses it
func TestLinkURLAcceptsOnlyItsOwnTokens(t *testing.T) {
	const link = "cl_abcdefghijklmnop"
	g := &Gateway{Resource: "https://mcp.kiff.dev/mcp", Issuer: "https://api.kiff.dev"}
	linkURL := g.LinkResource(link)
	g.API = meAPI{
		"kiff_live_t1_key":     {TenantID: "t1", AgentID: "support", Roles: []string{Role}},
		OAuthTokenPrefix + "l": {TenantID: "t1", AgentID: "support", Roles: []string{Role}, CredentialID: "k1", Resource: linkURL},
		OAuthTokenPrefix + "o": {TenantID: "t1", AgentID: "support", Roles: []string{Role}, CredentialID: "k2", Resource: g.LinkResource("cl_otherotherother")},
		OAuthTokenPrefix + "g": {TenantID: "t1", AgentID: "support", Roles: []string{Role}, CredentialID: "k3", Resource: g.Resource},
	}
	try := func(bearer, resource string, isLink bool) error {
		r := httptest.NewRequest("POST", "/", nil)
		r.Header.Set("Authorization", "Bearer "+bearer)
		_, err := g.authenticate(r, resource, isLink)
		return err
	}
	cases := []struct {
		bearer, resource string
		link, ok         bool
	}{
		{OAuthTokenPrefix + "l", linkURL, true, true},
		{"kiff_live_t1_key", linkURL, true, false},         // a key at a link URL
		{OAuthTokenPrefix + "o", linkURL, true, false},     // another link's token
		{OAuthTokenPrefix + "g", linkURL, true, false},     // a generic token
		{OAuthTokenPrefix + "l", g.Resource, false, false}, // a link token at the generic URL
		{OAuthTokenPrefix + "g", g.Resource, false, true},
		{"kiff_live_t1_key", g.Resource, false, true},
	}
	for _, c := range cases {
		if err := try(c.bearer, c.resource, c.link); (err == nil) != c.ok {
			t.Errorf("%s at %s: err=%v, want ok=%v", c.bearer, c.resource, err, c.ok)
		}
	}
	if !ValidLinkID(link) || ValidLinkID("cl_../x") || ValidLinkID("abc") {
		t.Fatal("ValidLinkID")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/.well-known/oauth-protected-resource/a/"+link+"/mcp", nil)
	req.SetPathValue("id", link)
	g.LinkResourceMetadata(rec, req)
	if !strings.Contains(rec.Body.String(), `"resource":"`+linkURL+`"`) {
		t.Fatalf("link metadata: %s", rec.Body.String())
	}
	if got := g.LinkServer("test", link).ResourceMetadata; got != "https://mcp.kiff.dev/.well-known/oauth-protected-resource/a/"+link+"/mcp" {
		t.Fatalf("link challenge metadata %q", got)
	}
}
