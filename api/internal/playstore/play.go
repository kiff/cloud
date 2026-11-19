// Package playstore is the tafs Android app's Google Play purchases: Solo's
// subscription and the task packs. Nothing the app or a notification sends
// is trusted for state: each purchase token is read back from the Google
// Play Developer API with KIFF's own service account, and applied with the
// same plan and pack rules as Apple's (appstore.Processor, with the Google
// Play store). Real-time Developer Notifications arrive as Pub/Sub pushes
// whose OIDC token, signed by Google, is verified first.
//
// Only the standard library is used: the service account's OAuth is a
// signed JWT assertion (RS256) to Google's token endpoint, and the push's
// token is checked against Google's published keys.
package playstore

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Google's endpoints, production's.
const (
	DefaultTokenURL = "https://oauth2.googleapis.com/token"
	DefaultAPIBase  = "https://androidpublisher.googleapis.com/androidpublisher/v3"
	scopePublisher  = "https://www.googleapis.com/auth/androidpublisher"
)

// ErrNoPurchase is a purchase token Google Play doesn't know for this app
// and product.
var ErrNoPurchase = errors.New("playstore: not a purchase Google Play knows for this app")

// ServiceAccount is the fields KIFF reads from a Google service account's
// JSON key.
type ServiceAccount struct {
	ClientEmail  string `json:"client_email"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	key          *rsa.PrivateKey
}

// ParseServiceAccount reads a service account's JSON key, as the Cloud
// console downloads it, or the same base64-encoded (one line, for an env
// file).
func ParseServiceAccount(raw string) (ServiceAccount, error) {
	raw = strings.TrimSpace(raw)
	if raw != "" && !strings.HasPrefix(raw, "{") {
		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return ServiceAccount{}, fmt.Errorf("playstore: service account: neither JSON nor base64 JSON")
		}
		raw = string(b)
	}
	var sa ServiceAccount
	if err := json.Unmarshal([]byte(raw), &sa); err != nil {
		return ServiceAccount{}, fmt.Errorf("playstore: service account: %w", err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return ServiceAccount{}, fmt.Errorf("playstore: service account: client_email and private_key are required")
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return ServiceAccount{}, fmt.Errorf("playstore: service account: private_key is not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if k1, err1 := x509.ParsePKCS1PrivateKey(block.Bytes); err1 == nil {
			k, err = k1, nil
		}
	}
	if err != nil {
		return ServiceAccount{}, fmt.Errorf("playstore: service account: private_key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return ServiceAccount{}, fmt.Errorf("playstore: service account: private_key is not RSA")
	}
	sa.key = rk
	return sa, nil
}

// Client reads purchases from the Google Play Developer API for one app,
// and acknowledges subscriptions.
type Client struct {
	sa          ServiceAccount
	packageName string
	tokenURL    string
	apiBase     string
	http        *http.Client
	now         func() time.Time

	mu      sync.Mutex
	access  string
	expires time.Time
}

// NewClient calls the Play API for packageName as sa.
func NewClient(sa ServiceAccount, packageName string) *Client {
	return &Client{sa: sa, packageName: packageName, tokenURL: DefaultTokenURL, apiBase: DefaultAPIBase,
		http: &http.Client{Timeout: 15 * time.Second}, now: time.Now}
}

// WithEndpoints points the client at other endpoints: a fake Play API, for
// tests. The key's own token_uri is never followed.
func (c *Client) WithEndpoints(apiBase, tokenURL string) *Client {
	c.apiBase, c.tokenURL = strings.TrimRight(apiBase, "/"), tokenURL
	return c
}

// PackageName is the app's.
func (c *Client) PackageName() string { return c.packageName }

// token is an access token for the Play API, minted from the service
// account and kept until a minute before it expires.
func (c *Client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.access != "" && now.Before(c.expires) {
		return c.access, nil
	}
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	if c.sa.PrivateKeyID != "" {
		header["kid"] = c.sa.PrivateKeyID
	}
	claims := map[string]any{"iss": c.sa.ClientEmail, "scope": scopePublisher, "aud": c.tokenURL,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
	assertion, err := signRS256(c.sa.key, header, claims)
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("playstore: token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("playstore: token: status %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
		return "", fmt.Errorf("playstore: token: no access token")
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 3600
	}
	c.access, c.expires = out.AccessToken, now.Add(time.Duration(out.ExpiresIn)*time.Second-time.Minute)
	return c.access, nil
}

func signRS256(key *rsa.PrivateKey, header map[string]string, claims map[string]any) (string, error) {
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cl, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(cl)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("playstore: sign: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// call sends one Play API request and decodes its answer into out. 404 and
// 410 are ErrNoPurchase; any other failure is an error to retry.
func (c *Client) call(ctx context.Context, method, path string, out any) error {
	access, err := c.token(ctx)
	if err != nil {
		return err
	}
	var body io.Reader
	if method == http.MethodPost {
		body = bytes.NewReader([]byte("{}"))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiBase+"/applications/"+url.PathEscape(c.packageName)+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("playstore: %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrNoPurchase
	case resp.StatusCode == http.StatusBadRequest:
		// A malformed or foreign token: Google answers 400 "invalid".
		return ErrNoPurchase
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("playstore: %s: status %d", strings.SplitN(path, "/tokens/", 2)[0], resp.StatusCode)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("playstore: decode: %w", err)
	}
	return nil
}

// Subscription states (SubscriptionPurchaseV2.subscriptionState).
const (
	StateActive          = "SUBSCRIPTION_STATE_ACTIVE"
	StateCanceled        = "SUBSCRIPTION_STATE_CANCELED" // renewal off; entitled until expiry
	StateInGrace         = "SUBSCRIPTION_STATE_IN_GRACE_PERIOD"
	StateOnHold          = "SUBSCRIPTION_STATE_ON_HOLD"
	StatePaused          = "SUBSCRIPTION_STATE_PAUSED"
	StateExpired         = "SUBSCRIPTION_STATE_EXPIRED"
	StatePending         = "SUBSCRIPTION_STATE_PENDING"
	StatePendingCanceled = "SUBSCRIPTION_STATE_PENDING_PURCHASE_CANCELED"
	AckPending           = "ACKNOWLEDGEMENT_STATE_PENDING"
	AckDone              = "ACKNOWLEDGEMENT_STATE_ACKNOWLEDGED"
)

// Subscription is the SubscriptionPurchaseV2 fields KIFF reads.
type Subscription struct {
	StartTime            time.Time `json:"startTime"`
	SubscriptionState    string    `json:"subscriptionState"`
	LatestOrderID        string    `json:"latestOrderId"`
	LinkedPurchaseToken  string    `json:"linkedPurchaseToken"`
	AcknowledgementState string    `json:"acknowledgementState"`
	// TestPurchase is present for a licence tester's purchase.
	TestPurchase               *struct{} `json:"testPurchase"`
	ExternalAccountIdentifiers struct {
		ObfuscatedExternalAccountID string `json:"obfuscatedExternalAccountId"`
	} `json:"externalAccountIdentifiers"`
	LineItems []struct {
		ProductID        string    `json:"productId"`
		ExpiryTime       time.Time `json:"expiryTime"`
		AutoRenewingPlan *struct {
			AutoRenewEnabled bool `json:"autoRenewEnabled"`
		} `json:"autoRenewingPlan"`
		OfferPhase *struct {
			FreeTrial *struct{} `json:"freeTrial"`
		} `json:"offerPhase"`
	} `json:"lineItems"`
}

// Product is the subscription's product (its first line item's).
func (s Subscription) Product() string {
	if len(s.LineItems) == 0 {
		return ""
	}
	return s.LineItems[0].ProductID
}

// Expires is when its current period ends.
func (s Subscription) Expires() time.Time {
	var at time.Time
	for _, li := range s.LineItems {
		if li.ExpiryTime.After(at) {
			at = li.ExpiryTime
		}
	}
	return at.UTC()
}

// FreeTrial reports that the current period is the free trial.
func (s Subscription) FreeTrial() bool {
	return len(s.LineItems) > 0 && s.LineItems[0].OfferPhase != nil && s.LineItems[0].OfferPhase.FreeTrial != nil
}

// Subscription reads a subscription purchase (purchases.subscriptionsv2.get).
func (c *Client) Subscription(ctx context.Context, token string) (Subscription, error) {
	var s Subscription
	err := c.call(ctx, http.MethodGet, "/purchases/subscriptionsv2/tokens/"+url.PathEscape(token), &s)
	return s, err
}

// Acknowledge acknowledges a subscription purchase
// (purchases.subscriptions.acknowledge). Play refunds one not acknowledged
// within three days.
func (c *Client) Acknowledge(ctx context.Context, product, token string) error {
	return c.call(ctx, http.MethodPost, "/purchases/subscriptions/"+url.PathEscape(product)+"/tokens/"+url.PathEscape(token)+":acknowledge", nil)
}

// One-time product purchase states (ProductPurchase.purchaseState).
const (
	ProductPurchased = 0
	ProductCanceled  = 1
	ProductPending   = 2
)

// ProductPurchase is the ProductPurchase fields KIFF reads.
type ProductPurchase struct {
	PurchaseTimeMillis          string `json:"purchaseTimeMillis"`
	PurchaseState               int    `json:"purchaseState"`
	OrderID                     string `json:"orderId"`
	ObfuscatedExternalAccountID string `json:"obfuscatedExternalAccountId"`
	// PurchaseType is absent for a real purchase; 0 is a licence tester's.
	PurchaseType *int `json:"purchaseType"`
	Quantity     int  `json:"quantity"`
	// ProductID is the product the purchase was read for (not Google's).
	ProductID string `json:"-"`
}

// Purchased is when it was bought.
func (p ProductPurchase) Purchased() time.Time {
	ms, _ := strconv.ParseInt(p.PurchaseTimeMillis, 10, 64)
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// Test reports a licence tester's purchase.
func (p ProductPurchase) Test() bool { return p.PurchaseType != nil && *p.PurchaseType == 0 }

// Product reads a one-time product purchase (purchases.products.get).
func (c *Client) Product(ctx context.Context, product, token string) (ProductPurchase, error) {
	var p ProductPurchase
	err := c.call(ctx, http.MethodGet, "/purchases/products/"+url.PathEscape(product)+"/tokens/"+url.PathEscape(token), &p)
	p.ProductID = product
	return p, err
}
