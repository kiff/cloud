// Package playstoretest is a fake Google for tests: the Play Developer API
// with its OAuth token endpoint, and the keys that sign Pub/Sub's push
// tokens. Nothing leaves the process.
package playstoretest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/playstore"
)

// Play is a fake Play Developer API for one app.
type Play struct {
	Server  *httptest.Server
	Package string
	// ServiceAccountJSON is a service account key the fake accepts.
	ServiceAccountJSON string

	key *rsa.PrivateKey
	mu  sync.Mutex
	// subs by purchase token; products by product, then token: the JSON
	// Play answers.
	subs     map[string]map[string]any
	products map[string]map[string]map[string]any
	acks     map[string]int
	access   string
	tokens   int // access tokens minted
}

// NewPlay starts a fake Play API for packageName, closed when t ends.
func NewPlay(t testing.TB, packageName string) *Play {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	sa, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "kiff-play@example.com",
		"private_key_id": "k1", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"token_uri": "https://attacker.example/token"})
	p := &Play{Package: packageName, ServiceAccountJSON: string(sa), key: key, subs: map[string]map[string]any{},
		products: map[string]map[string]map[string]any{}, acks: map[string]int{}, access: "access-" + rand.Text()}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.Server.Close)
	return p
}

// Client is a Play client for the fake.
func (p *Play) Client(t testing.TB) *playstore.Client {
	t.Helper()
	sa, err := playstore.ParseServiceAccount(p.ServiceAccountJSON)
	if err != nil {
		t.Fatal(err)
	}
	return playstore.NewClient(sa, p.Package).WithEndpoints(p.Server.URL+"/androidpublisher/v3", p.Server.URL+"/token")
}

// SetSubscription makes Play answer v for the subscription token.
func (p *Play) SetSubscription(token string, v map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subs[token] = v
}

// SetProduct makes Play answer v for product's purchase token.
func (p *Play) SetProduct(product, token string, v map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.products[product] == nil {
		p.products[product] = map[string]map[string]any{}
	}
	p.products[product][token] = v
}

// Acks is how many times the subscription token was acknowledged.
func (p *Play) Acks(token string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.acks[token]
}

// Subscription is a SubscriptionPurchaseV2 answer: product, in state,
// bound to accountToken, expiring at expires, acknowledgement pending.
func Subscription(product, state, accountToken string, start, expires time.Time) map[string]any {
	return map[string]any{
		"kind": "androidpublisher#subscriptionPurchaseV2", "startTime": start.UTC().Format(time.RFC3339Nano),
		"subscriptionState": state, "latestOrderId": "GPA.0000-0000-0000-00001",
		"acknowledgementState":       playstore.AckPending,
		"externalAccountIdentifiers": map[string]any{"obfuscatedExternalAccountId": accountToken},
		"lineItems": []any{map[string]any{"productId": product, "expiryTime": expires.UTC().Format(time.RFC3339Nano),
			"autoRenewingPlan": map[string]any{"autoRenewEnabled": true}}},
	}
}

// Product is a ProductPurchase answer: purchased, bound to accountToken.
func Product(accountToken string, at time.Time) map[string]any {
	return map[string]any{"kind": "androidpublisher#productPurchase", "purchaseTimeMillis": itoa(at.UnixMilli()),
		"purchaseState": 0, "consumptionState": 0, "orderId": "GPA.1111-1111-1111-11111",
		"obfuscatedExternalAccountId": accountToken, "acknowledgementState": 0, "quantity": 1}
}

func itoa(v int64) string { return big.NewInt(v).String() }

func (p *Play) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		p.token(w, r)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+p.access {
		http.Error(w, `{"error":{"code":401}}`, http.StatusUnauthorized)
		return
	}
	segs := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/androidpublisher/v3/applications/"), "/")
	for i := range segs {
		segs[i], _ = url.PathUnescape(segs[i])
	}
	if len(segs) < 2 || segs[0] != p.Package || segs[1] != "purchases" {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.Method == http.MethodGet && len(segs) == 5 && segs[2] == "subscriptionsv2" && segs[3] == "tokens":
		if v, ok := p.subs[segs[4]]; ok {
			_ = json.NewEncoder(w).Encode(v)
			return
		}
	case r.Method == http.MethodGet && len(segs) == 6 && segs[2] == "products" && segs[4] == "tokens":
		if v, ok := p.products[segs[3]][segs[5]]; ok {
			_ = json.NewEncoder(w).Encode(v)
			return
		}
	case r.Method == http.MethodPost && len(segs) == 6 && segs[2] == "subscriptions" && segs[4] == "tokens" && strings.HasSuffix(segs[5], ":acknowledge"):
		token := strings.TrimSuffix(segs[5], ":acknowledge")
		v, ok := p.subs[token]
		if !ok || v["lineItems"].([]any)[0].(map[string]any)["productId"] != segs[3] {
			break
		}
		if v["acknowledgementState"] == playstore.AckDone {
			http.Error(w, `{"error":{"code":400,"message":"already acknowledged"}}`, http.StatusBadRequest)
			return
		}
		v["acknowledgementState"] = playstore.AckDone
		p.acks[token]++
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Error(w, `{"error":{"code":404}}`, http.StatusNotFound)
}

// token is Google's token endpoint: a JWT assertion signed by the service
// account's key, for the Play API's scope and this endpoint.
func (p *Play) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
		return
	}
	parts := strings.Split(r.Form.Get("assertion"), ".")
	if len(parts) != 3 {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&p.key.PublicKey, crypto.SHA256, digest[:], sig) != nil {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Iss, Scope, Aud string
		Exp             int64
	}
	_ = json.Unmarshal(raw, &c)
	if c.Iss != "kiff-play@example.com" || c.Scope != "https://www.googleapis.com/auth/androidpublisher" ||
		c.Aud != "http://"+r.Host+"/token" || c.Exp < time.Now().Unix() {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.tokens++
	p.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": p.access, "expires_in": 3599, "token_type": "Bearer"})
}

// Google is a fake of the keys Google signs its OIDC tokens with.
type Google struct {
	Server *httptest.Server
	key    *rsa.PrivateKey
	Kid    string
}

// NewGoogle serves a JWKS of one key, closed when t ends.
func NewGoogle(t testing.TB) *Google {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	g := &Google{key: key, Kid: "g-" + rand.Text()[:8]}
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": g.Kid,
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(jwks)
	}))
	t.Cleanup(g.Server.Close)
	return g
}

// JWKSURL is where the fake serves its keys.
func (g *Google) JWKSURL() string { return g.Server.URL }

// Sign is an RS256 JWT of claims under the fake's key.
func (g *Google) Sign(claims map[string]any) string { return SignWith(g.key, g.Kid, claims) }

// PushToken is the token Pub/Sub sends: for audience, as email, valid at now.
func (g *Google) PushToken(audience, email string, now time.Time) string {
	return g.Sign(map[string]any{"iss": "https://accounts.google.com", "aud": audience, "email": email, "email_verified": true,
		"sub": "1234567890", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
}

// SignWith signs claims with key, as kid.
func SignWith(key *rsa.PrivateKey, kid string, claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	c, _ := json.Marshal(claims)
	signing := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	digest := sha256.Sum256([]byte(signing))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// Push is a Pub/Sub push body carrying notification n.
func Push(messageID string, n map[string]any) []byte {
	raw, _ := json.Marshal(n)
	body, _ := json.Marshal(map[string]any{"message": map[string]any{"data": base64.StdEncoding.EncodeToString(raw),
		"messageId": messageID, "publishTime": time.Now().UTC().Format(time.RFC3339)},
		"subscription": "projects/kiff/subscriptions/play-rtdn"})
	return body
}
