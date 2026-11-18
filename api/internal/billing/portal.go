package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// stripeAPIBase is the Stripe REST API base. A field on the client
// (set via WithAPIBase) keeps it overridable in tests without a
// package-level global.
const stripeAPIBase = "https://api.stripe.com"

// PortalClient mints Stripe Customer Portal sessions. It holds the
// Stripe secret key and never exposes it — only the returned portal
// URL is handed back. This lives api-side by design: the web tier
// must never hold the secret key. The portal lets a
// customer update their card, view invoices, and cancel without
// KIFF ever touching card data (Stripe hosts the portal).
type PortalClient struct {
	secretKey  string
	httpClient *http.Client
	apiBase    string
}

// PortalOption configures a PortalClient.
type PortalOption func(*PortalClient)

// WithHTTPClient overrides the HTTP client (tests).
func WithHTTPClient(c *http.Client) PortalOption {
	return func(p *PortalClient) { p.httpClient = c }
}

// WithAPIBase overrides the Stripe API base (tests).
func WithAPIBase(base string) PortalOption {
	return func(p *PortalClient) { p.apiBase = base }
}

// NewPortalClient returns a client, or nil when secretKey is empty so
// callers can leave the portal route disabled in environments without
// Stripe configured (mirrors the webhook's unmounted-when-unset).
func NewPortalClient(secretKey string, opts ...PortalOption) *PortalClient {
	if strings.TrimSpace(secretKey) == "" {
		return nil
	}
	p := &PortalClient{
		secretKey:  secretKey,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		apiBase:    stripeAPIBase,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// CreateSession creates a billing-portal session for the Stripe
// customer and returns the one-time portal URL to redirect to.
// returnURL is where Stripe sends the user when they leave the portal.
func (c *PortalClient) CreateSession(ctx context.Context, customerID, returnURL string) (string, error) {
	if strings.TrimSpace(customerID) == "" {
		return "", fmt.Errorf("billing portal: empty customer id")
	}
	form := url.Values{}
	form.Set("customer", customerID)
	if returnURL != "" {
		form.Set("return_url", returnURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.apiBase+"/v1/billing_portal/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.secretKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("billing portal: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		URL   string `json:"url"`
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("billing portal decode: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := "stripe error"
		if body.Error != nil && body.Error.Message != "" {
			msg = body.Error.Message
		}
		return "", fmt.Errorf("billing portal: %s (status %d)", msg, resp.StatusCode)
	}
	if body.URL == "" {
		return "", fmt.Errorf("billing portal: stripe returned empty url")
	}
	return body.URL, nil
}
