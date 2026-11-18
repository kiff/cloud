package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MeterEventClient reports usage to Stripe Billing Meters via
// POST /v1/billing/meter_events. Stripe aggregates these events
// against a meter and invoices the subscription's metered price
// automatically — KIFF reports events, it does not compute the bill.
// Legacy usage records are deprecated; meter events are the
// current usage-based path.
//
// Holds the Stripe secret key (api-side only, like PortalClient) and
// never exposes it. nil when no key is configured so the reporter
// stays disabled.
type MeterEventClient struct {
	secretKey  string
	eventName  string
	httpClient *http.Client
	apiBase    string
}

// NewMeterEventClient returns a client, or nil when secretKey or
// eventName is empty so the caller can leave the reporter disabled in
// environments without metered billing configured.
func NewMeterEventClient(secretKey, eventName string, opts ...MeterEventOption) *MeterEventClient {
	if strings.TrimSpace(secretKey) == "" || strings.TrimSpace(eventName) == "" {
		return nil
	}
	c := &MeterEventClient{
		secretKey:  secretKey,
		eventName:  eventName,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		apiBase:    stripeAPIBase,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// MeterEventOption configures a MeterEventClient.
type MeterEventOption func(*MeterEventClient)

// WithMeterHTTPClient overrides the HTTP client (tests).
func WithMeterHTTPClient(h *http.Client) MeterEventOption {
	return func(c *MeterEventClient) { c.httpClient = h }
}

// WithMeterAPIBase overrides the Stripe API base (tests).
func WithMeterAPIBase(base string) MeterEventOption {
	return func(c *MeterEventClient) { c.apiBase = base }
}

// Send reports a single meter event for the customer. identifier is
// Stripe's dedup key (we pass the ledger operation id) so a retried
// send within Stripe's dedup window never double-counts. value is the
// number of governed operations (1 per operation). timestamp is the
// operation's occurrence time.
func (c *MeterEventClient) Send(ctx context.Context, customerID, identifier string, value int, at time.Time) error {
	if strings.TrimSpace(customerID) == "" {
		return fmt.Errorf("meter event: empty customer id")
	}
	form := url.Values{}
	form.Set("event_name", c.eventName)
	form.Set("identifier", identifier)
	form.Set("timestamp", strconv.FormatInt(at.Unix(), 10))
	form.Set("payload[stripe_customer_id]", customerID)
	form.Set("payload[value]", strconv.Itoa(value))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.apiBase+"/v1/billing/meter_events", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.secretKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("meter event: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var body struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	msg := "stripe error"
	if body.Error != nil && body.Error.Message != "" {
		msg = body.Error.Message
	}
	return fmt.Errorf("meter event: %s (status %d)", msg, resp.StatusCode)
}
