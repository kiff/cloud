package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kiff/cloud/api/internal/tenant"
)

// The v1 catalog's prices, by lookup key. The catalog
// script creates them, in test mode and
// live, and these keys are how KIFF finds them in either. v1 sells
// monthly only. The packs' one-time prices are named in the pack table
// (packs.Catalog), not here.
const (
	// PriceSoloMonthly is retired on the web: Solo is sold only in the app
	// stores. Kept to read an existing subscription's plan.
	PriceSoloMonthly   = "kiff_solo_monthly"
	PriceTeamMonthly   = "kiff_team_monthly" // per seat: what kiff.dev sells as Company
	PriceActionOverage = "kiff_action_overage"
	// PriceCreditOverage bills Company's credits past the period's
	// included ones (seats × meter.CompanyCreditsPerSeat), €0.01 each.
	PriceCreditOverage = "kiff_credit_overage"
	// PriceTaskOverage is retired: tasks past the included ones come from
	// packs. No new subscription carries it, and an existing one has it
	// dropped on its next update (DropTaskOverage).
	PriceTaskOverage = "kiff_task_overage"
)

// Checkout and payment metadata keys, set by KIFF on the pack checkouts it
// opens: the pack, and the member's request it answers with who answered.
const (
	MetaPack       = "kiff_pack"
	MetaRequest    = "kiff_request"
	MetaAnsweredBy = "kiff_answered_by"
)

// The meters' event names, which the overage prices are on.
const (
	MeterEventTask   = "kiff_task"
	MeterEventAction = "kiff_action"
	MeterEventCredit = "kiff_credit"
)

// MetaTenant is the subscription and checkout metadata key naming the
// KIFF account, set by KIFF on the sessions it creates, so every
// subscription event resolves its account.
const MetaTenant = "kiff_tenant"

// MetaAppleCredit marks a Team subscription whose first days are the
// unused days of the account's App Store Solo, credited when it moved to
// Team: it runs as Team, unbilled, until they end.
const MetaAppleCredit = "kiff_apple_credit"

// Stripe is the v1 offer's Stripe client: checkout for a plan, and Team's
// seats. Hand-rolled over the REST API like the rest of this package.
type Stripe struct {
	secretKey  string
	httpClient *http.Client
	apiBase    string

	mu     sync.Mutex
	prices map[string]string // lookup key → price id
}

// NewStripe returns a client for secretKey; nil when it's empty.
func NewStripe(secretKey string, opts ...PortalOption) *Stripe {
	if strings.TrimSpace(secretKey) == "" {
		return nil
	}
	p := &PortalClient{httpClient: &http.Client{Timeout: 15 * time.Second}, apiBase: stripeAPIBase}
	for _, o := range opts {
		o(p)
	}
	return &Stripe{secretKey: secretKey, httpClient: p.httpClient, apiBase: p.apiBase, prices: map[string]string{}}
}

// call sends a form request and decodes the JSON answer into out.
func (s *Stripe) call(ctx context.Context, method, path string, form url.Values, out any) error {
	var body *strings.Reader
	target := s.apiBase + path
	if method == http.MethodGet {
		if len(form) > 0 {
			target += "?" + form.Encode()
		}
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.secretKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("stripe %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		msg := "stripe error"
		if e.Error != nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		return fmt.Errorf("stripe %s: %s (status %d)", path, msg, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Price is the price id for a lookup key, read once and kept.
func (s *Stripe) Price(ctx context.Context, lookupKey string) (string, error) {
	s.mu.Lock()
	id, ok := s.prices[lookupKey]
	s.mu.Unlock()
	if ok {
		return id, nil
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	form := url.Values{"lookup_keys[]": {lookupKey}, "active": {"true"}}
	if err := s.call(ctx, http.MethodGet, "/v1/prices", form, &out); err != nil {
		return "", err
	}
	if len(out.Data) != 1 {
		return "", fmt.Errorf("stripe: no active price %s: run the catalog script", lookupKey)
	}
	s.mu.Lock()
	s.prices[lookupKey] = out.Data[0].ID
	s.mu.Unlock()
	return out.Data[0].ID, nil
}

// Checkout is a checkout for one account.
type Checkout struct {
	TenantID string
	// CustomerID is the account's Stripe customer, once it has one; else
	// Stripe makes one, from Email.
	CustomerID, Email string
	// Plan is "team" (Company): Solo is sold only in the app stores.
	Plan string
	// Seats is Team's people who run things; at least one.
	Seats int
	// TrialEnd starts the subscription on a trial that ends then: 7 days
	// for a new account, what's left of it for one already on the trial.
	// Zero: no trial.
	TrialEnd time.Time
	// AppleCredit marks TrialEnd as the end of the App Store Solo days the
	// account already paid for, credited as Team days, not a trial.
	AppleCredit bool
	// SuccessURL and CancelURL are where Stripe sends the person back.
	SuccessURL, CancelURL string
}

// CreateCheckout opens a Stripe Checkout session for c and returns its URL.
// The subscription carries Company's seats and its two metered prices, KIFF
// actions and credits past the included ones; Stripe Tax computes VAT, the
// company's VAT id is collected, and a card is taken. A price Stripe doesn't
// have fails the checkout: Company is never sold without its overage.
func (s *Stripe) CreateCheckout(ctx context.Context, c Checkout) (string, error) {
	if c.Plan != "team" {
		return "", fmt.Errorf("stripe: no plan %q to check out: Solo is sold in the app stores", c.Plan)
	}
	form := url.Values{}
	for i, item := range []struct {
		key      string
		quantity int
	}{{PriceTeamMonthly, max(c.Seats, tenant.TeamMinSeats)}, {PriceActionOverage, 0}, {PriceCreditOverage, 0}} {
		id, err := s.Price(ctx, item.key)
		if err != nil {
			return "", err
		}
		form.Set(fmt.Sprintf("line_items[%d][price]", i), id)
		if item.quantity > 0 {
			form.Set(fmt.Sprintf("line_items[%d][quantity]", i), strconv.Itoa(item.quantity))
		}
	}
	form.Set("mode", "subscription")
	form.Set("client_reference_id", c.TenantID)
	form.Set("metadata["+MetaTenant+"]", c.TenantID)
	form.Set("subscription_data[metadata]["+MetaTenant+"]", c.TenantID)
	form.Set("automatic_tax[enabled]", "true")
	form.Set("tax_id_collection[enabled]", "true")
	form.Set("billing_address_collection", "required")
	form.Set("payment_method_collection", "always")
	form.Set("success_url", c.SuccessURL)
	form.Set("cancel_url", c.CancelURL)
	if c.CustomerID != "" {
		form.Set("customer", c.CustomerID)
		// Tax ids and the billing address are kept on the customer.
		form.Set("customer_update[address]", "auto")
		form.Set("customer_update[name]", "auto")
	} else if c.Email != "" {
		form.Set("customer_email", c.Email)
	}
	if !c.TrialEnd.IsZero() {
		form.Set("subscription_data[trial_end]", strconv.FormatInt(c.TrialEnd.Unix(), 10))
	}
	if c.AppleCredit && !c.TrialEnd.IsZero() {
		form.Set("subscription_data[metadata]["+MetaAppleCredit+"]", "1")
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := s.call(ctx, http.MethodPost, "/v1/checkout/sessions", form, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", fmt.Errorf("stripe: checkout session without a url")
	}
	return out.URL, nil
}

// PackCheckout is a checkout for one pack.
type PackCheckout struct {
	TenantID string
	// CustomerID is the account's Stripe customer, once it has one; else
	// Stripe makes one, from Email.
	CustomerID, Email string
	// Pack is the pack's id in the table, and PriceKey its one-time
	// price's lookup key.
	Pack, PriceKey string
	// RequestID is the member's request this pack answers, and AnsweredBy
	// who is buying it; both "" when it answers none.
	RequestID, AnsweredBy string
	// SuccessURL and CancelURL are where Stripe sends the person back.
	SuccessURL, CancelURL string
}

// CreatePackCheckout opens a one-time Stripe Checkout payment for a pack
// and returns its URL. The session and its payment carry the account and
// the pack, so the completed payment records the pack (Processor), and a
// refund finds it.
func (s *Stripe) CreatePackCheckout(ctx context.Context, c PackCheckout) (string, error) {
	if c.TenantID == "" || c.Pack == "" || c.PriceKey == "" {
		return "", fmt.Errorf("stripe: a pack checkout needs its account and pack")
	}
	price, err := s.Price(ctx, c.PriceKey)
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("line_items[0][price]", price)
	form.Set("line_items[0][quantity]", "1")
	form.Set("client_reference_id", c.TenantID)
	meta := map[string]string{MetaTenant: c.TenantID, MetaPack: c.Pack}
	if c.RequestID != "" {
		meta[MetaRequest] = c.RequestID
		meta[MetaAnsweredBy] = c.AnsweredBy
	}
	for k, v := range meta {
		form.Set("metadata["+k+"]", v)
		form.Set("payment_intent_data[metadata]["+k+"]", v)
	}
	form.Set("automatic_tax[enabled]", "true")
	form.Set("tax_id_collection[enabled]", "true")
	form.Set("billing_address_collection", "required")
	form.Set("success_url", c.SuccessURL)
	form.Set("cancel_url", c.CancelURL)
	if c.CustomerID != "" {
		form.Set("customer", c.CustomerID)
		form.Set("customer_update[address]", "auto")
		form.Set("customer_update[name]", "auto")
	} else {
		// A customer, so a later plan or pack is billed to the same one.
		form.Set("customer_creation", "always")
		if c.Email != "" {
			form.Set("customer_email", c.Email)
		}
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := s.call(ctx, http.MethodPost, "/v1/checkout/sessions", form, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", fmt.Errorf("stripe: checkout session without a url")
	}
	return out.URL, nil
}

// DropTaskOverage brings sub's metered prices in line with the catalog,
// with nothing prorated: the retired task overage price goes (tasks are
// no longer metered, so it would bill nothing), and a Team (Company)
// subscription gains the credit overage price if it lacks it, so its
// credits past the included ones are billed. True when it changed one.
func (s *Stripe) DropTaskOverage(ctx context.Context, sub Subscription) (bool, error) {
	changed, team, credits := false, false, false
	for _, it := range sub.Items.Data {
		switch it.Price.LookupKey {
		case PriceTeamMonthly:
			team = true
		case PriceCreditOverage:
			credits = true
		case PriceTaskOverage:
			if it.ID == "" {
				continue
			}
			form := url.Values{"proration_behavior": {"none"}}
			if err := s.call(ctx, http.MethodDelete, "/v1/subscription_items/"+url.PathEscape(it.ID), form, nil); err != nil {
				return changed, err
			}
			changed = true
		}
	}
	if team && !credits && sub.ID != "" {
		price, err := s.Price(ctx, PriceCreditOverage)
		if err != nil {
			return changed, err
		}
		form := url.Values{"subscription": {sub.ID}, "price": {price}, "proration_behavior": {"none"}}
		if err := s.call(ctx, http.MethodPost, "/v1/subscription_items", form, nil); err != nil {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}

// SetSeats sets the Team subscription's seats, at least one, from the next
// invoice: no proration, so a seat added mid-period is billed from the
// next one. Its metered prices are brought in line with the update
// (DropTaskOverage).
func (s *Stripe) SetSeats(ctx context.Context, subscriptionID string, seats int) error {
	sub, err := s.Subscription(ctx, subscriptionID)
	if err != nil {
		return err
	}
	seatsErr := s.setSeats(ctx, sub, seats)
	// After the seats, so a failure to drop the retired price never holds
	// a seat change back; the next update tries again.
	if _, err := s.DropTaskOverage(ctx, sub); err != nil && seatsErr == nil {
		return fmt.Errorf("stripe: the seats are set, but the metered prices stay as they were until the next update: %w", err)
	}
	return seatsErr
}

func (s *Stripe) setSeats(ctx context.Context, sub Subscription, seats int) error {
	seats = max(seats, tenant.TeamMinSeats)
	for _, it := range sub.Items.Data {
		if it.Price.LookupKey != PriceTeamMonthly {
			continue
		}
		if it.Quantity == seats {
			return nil
		}
		form := url.Values{"quantity": {strconv.Itoa(seats)}, "proration_behavior": {"none"}}
		return s.call(ctx, http.MethodPost, "/v1/subscription_items/"+url.PathEscape(it.ID), form, nil)
	}
	return fmt.Errorf("stripe: subscription %s has no Team seats", sub.ID)
}

// Subscription is what KIFF reads of a Stripe subscription.
type Subscription struct {
	ID         string            `json:"id"`
	Customer   string            `json:"customer"`
	Status     string            `json:"status"`
	Metadata   map[string]string `json:"metadata"`
	TrialStart int64             `json:"trial_start"`
	TrialEnd   int64             `json:"trial_end"`
	// The period is on the subscription in older API versions and on
	// each item in newer ones; Period reads either.
	CurrentPeriodStart int64 `json:"current_period_start"`
	CurrentPeriodEnd   int64 `json:"current_period_end"`
	Items              struct {
		Data []SubscriptionItem `json:"data"`
	} `json:"items"`
}

// SubscriptionItem is one price on a subscription.
type SubscriptionItem struct {
	ID                 string `json:"id"`
	Quantity           int    `json:"quantity"`
	CurrentPeriodStart int64  `json:"current_period_start"`
	CurrentPeriodEnd   int64  `json:"current_period_end"`
	Price              struct {
		LookupKey string `json:"lookup_key"`
	} `json:"price"`
}

// Plan is the subscription's plan ("solo" or "team") and its seats, from
// its base price; "" when it has neither.
func (s Subscription) Plan() (plan string, seats int, item SubscriptionItem) {
	for _, it := range s.Items.Data {
		switch it.Price.LookupKey {
		case PriceSoloMonthly:
			return "solo", 1, it
		case PriceTeamMonthly:
			return "team", it.Quantity, it
		}
	}
	return "", 0, SubscriptionItem{}
}

// Period is the subscription's current billing period.
func (s Subscription) Period() (from, to time.Time, ok bool) {
	start, end := s.CurrentPeriodStart, s.CurrentPeriodEnd
	if start == 0 || end == 0 {
		_, _, it := s.Plan()
		start, end = it.CurrentPeriodStart, it.CurrentPeriodEnd
	}
	if start == 0 || end == 0 {
		return time.Time{}, time.Time{}, false
	}
	return time.Unix(start, 0).UTC(), time.Unix(end, 0).UTC(), true
}

// Subscription reads subscription id as Stripe has it now.
func (s *Stripe) Subscription(ctx context.Context, id string) (Subscription, error) {
	var sub Subscription
	err := s.call(ctx, http.MethodGet, "/v1/subscriptions/"+url.PathEscape(id), nil, &sub)
	return sub, err
}
