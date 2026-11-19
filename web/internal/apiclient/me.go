package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Me mirrors the apps/api MeResponse shape. The dashboard's
// /dashboard handler hits GetMeBearer on every page load to
// resolve the signed-in user's tenant — this is the source of
// truth that "you're signed in as alice@example.com, working in
// tenant user-abc123".
type Me struct {
	Subject     string `json:"subject"`
	Email       string `json:"email,omitempty"`
	TenantID    string `json:"tenant_id"`
	TenantSlug  string `json:"tenant_slug"`
	DisplayName string `json:"tenant_display_name,omitempty"`
	APIKey      bool   `json:"api_key"`
	// Roles is the authenticated identity's role set, populated
	// by the apps/api auth middleware (Clerk org role in
	// production, KIFF_STUB_SUBJECTS seed in stub mode). The
	// onboarding mint reads it so the freshly-minted key carries
	// the same authority as the session that minted it; the
	// cloud's keys handler refuses any role escalation, so this
	// is a read-back of what the caller already holds.
	Roles []string `json:"roles,omitempty"`
	// Plan is the tenant's effective plan identifier. Canonical
	// the consumption model values are "developer", "growth",
	// "scale", and "enterprise"; legacy aliases ("free_dev",
	// "launch_pilot", "team", "builder", "agency_team") may still
	// appear for tenants whose metadata predates the migration.
	// Defaults to the free Developer plan when the underlying
	// metadata is absent. Surfaced so the dashboard's plan card +
	// upgrade CTA can render plan state without a second
	// round-trip.
	Plan string `json:"plan,omitempty"`
	// MonthlyProposalLimit is the period ceiling for governed
	// proposals on this plan. Zero-or-less means unlimited
	// (reserved for high-touch lanes; not used in v0.1
	// self-service tiers).
	MonthlyProposalLimit int `json:"monthly_proposal_limit,omitempty"`
	// MaxDomains is the plan's cap on domain count; zero-or-less
	// is unlimited. The domains apex renders "N of M" and gates
	// the "New domain" affordance from it.
	MaxDomains int `json:"max_domains,omitempty"`
	// CanonicalPlan is the consumption model canonical plan
	// (developer/growth/scale/enterprise) the tenant resolves to,
	// independent of any legacy alias in Plan. The plan card reads
	// this directly rather than re-deriving from the alias table.
	CanonicalPlan string `json:"canonical_plan,omitempty"`
	// BillingStatus is the Stripe-synced subscription state:
	// "active", "past_due" (grace — service unaffected), or
	// "canceled". Empty when the tenant never carried a
	// subscription. Drives the settings billing banner.
	BillingStatus string `json:"billing_status,omitempty"`
	// AccountKind is "personal" or "company", "" until the account's
	// owner chooses.
	AccountKind string `json:"account_kind,omitempty"`
	TimeZone    string `json:"time_zone"`
	// ChatApprovals: a held call is also offered in the agent's chat, the
	// owner's choice or the account kind's default.
	ChatApprovals bool `json:"chat_approvals"`
	// MemberRole is the signed-in person's role on this account: admin,
	// editor, approver or viewer. Capabilities is what the caller may do
	// here; the dashboard hides controls a role cannot use (the API
	// refuses them anyway).
	MemberRole   string   `json:"member_role,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	// HasBilling reports whether the tenant has a linked Stripe
	// customer. Gates the "Manage billing" (Customer Portal) action.
	HasBilling bool `json:"has_billing,omitempty"`
}

// GetMeBearer fetches /v1/me using the supplied bearer (either a
// Clerk session JWT or a stub subject). The bearer is forwarded
// verbatim as the Authorization header value (without the
// "Bearer " prefix; callers pass the credential, this method
// adds the prefix).
//
// Errors:
//   - bearer is empty: returns an error without a request.
//   - upstream returns 401: ErrUnauthorized so the dashboard can
//     redirect to /sign-in instead of swallowing.
//   - any other non-2xx: a wrapping error with the body snippet.
func (c *Client) GetMeBearer(ctx context.Context, bearer string) (*Me, error) {
	if c == nil {
		return nil, errors.New("apiclient: nil client")
	}
	if strings.TrimSpace(bearer) == "" {
		return nil, errors.New("apiclient: empty bearer")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/me", nil)
	if err != nil {
		return nil, fmt.Errorf("apiclient: build me request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: get me: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var m Me
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			return nil, fmt.Errorf("apiclient: decode me: %w", err)
		}
		return &m, nil
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("apiclient: get me unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// ErrNoBilling is returned by BillingPortalBearer when the tenant
// has no linked Stripe customer (the API 404s). The dashboard gates
// the action on Me.HasBilling, so this is defence in depth.
var ErrNoBilling = errors.New("apiclient: tenant has no billing account")

// BillingPortalBearer mints a Stripe Customer Portal session for the
// caller's tenant via POST /v1/me/billing-portal and returns the
// one-time URL the dashboard should redirect to. The Stripe secret
// stays api-side; the web only ever sees the resulting portal URL.
//
// Errors:
//   - 401 → ErrUnauthorized (bounce to /sign-in).
//   - 404 → ErrNoBilling (no Stripe customer; hide the action).
//   - other non-2xx → a wrapping error with the body snippet.
func (c *Client) BillingPortalBearer(ctx context.Context, bearer string) (string, error) {
	if c == nil {
		return "", errors.New("apiclient: nil client")
	}
	if strings.TrimSpace(bearer) == "" {
		return "", errors.New("apiclient: empty bearer")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/me/billing-portal", nil)
	if err != nil {
		return "", fmt.Errorf("apiclient: build billing-portal request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("apiclient: billing portal: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return "", fmt.Errorf("apiclient: decode billing portal: %w", err)
		}
		if strings.TrimSpace(body.URL) == "" {
			return "", errors.New("apiclient: billing portal returned empty url")
		}
		return body.URL, nil
	case http.StatusUnauthorized:
		return "", ErrUnauthorized
	case http.StatusNotFound:
		return "", ErrNoBilling
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("apiclient: billing portal unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
}

// SetAccountKindBearer records what the account is for: "personal" or
// "company". Only its owner or an admin may; the API answers anyone
// else with 403 (ErrForbidden). SetChatApprovalsBearer turns offering
// held calls in the agent's chat on or off for the account (owner or
// admin only).
func (c *Client) SetChatApprovalsBearer(ctx context.Context, bearer string, enabled bool) error {
	payload, _ := json.Marshal(map[string]bool{"enabled": enabled})
	_, err := c.do(ctx, bearer, http.MethodPut, "/v1/me/chat-approvals", bytes.NewReader(payload))
	return err
}

func (c *Client) SetTimeZoneBearer(ctx context.Context, bearer, zone string) error {
	payload, _ := json.Marshal(map[string]string{"time_zone": zone})
	_, err := c.do(ctx, bearer, http.MethodPut, "/v1/me/time-zone", bytes.NewReader(payload))
	return err
}

// SetAccountNameBearer names the account.
func (c *Client) SetAccountNameBearer(ctx context.Context, bearer, name string) error {
	payload, _ := json.Marshal(map[string]string{"name": name})
	_, err := c.do(ctx, bearer, http.MethodPut, "/v1/me/account-name", bytes.NewReader(payload))
	return err
}

func (c *Client) SetAccountKindBearer(ctx context.Context, bearer, kind string) error {
	payload, _ := json.Marshal(map[string]string{"kind": kind})
	_, err := c.do(ctx, bearer, http.MethodPut, "/v1/me/account-kind", bytes.NewReader(payload))
	return err
}

// Can reports whether the caller holds capability c. An API that sends
// no capabilities (one older than the governance-roles design) is read
// as allowing it, as before.
func (m *Me) Can(c string) bool {
	if m == nil || m.Capabilities == nil {
		return true
	}
	for _, x := range m.Capabilities {
		if x == c {
			return true
		}
	}
	return false
}

// ErrAlreadySubscribed is CheckoutBearer's answer for an account that has
// a plan already: it changes it in the billing portal.
var ErrAlreadySubscribed = errors.New("apiclient: the account already has a plan")

// ErrNoCheckout is CheckoutBearer's answer when the person can't choose
// the plan (not the billing admin), or the account's plan is arranged
// with KIFF.
var ErrNoCheckout = errors.New("apiclient: no checkout for this account")

// CheckoutBearer opens the v1 checkout (POST /v1/me/billing/checkout)
// for plan ("solo" or "team") and returns Stripe's URL.
func (c *Client) CheckoutBearer(ctx context.Context, bearer, plan string) (string, error) {
	if c == nil {
		return "", errors.New("apiclient: nil client")
	}
	if strings.TrimSpace(bearer) == "" {
		return "", errors.New("apiclient: empty bearer")
	}
	body, _ := json.Marshal(map[string]string{"plan": plan})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/me/billing/checkout", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("apiclient: build checkout request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("apiclient: checkout: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 8<<10)).Decode(&out)
	switch {
	case resp.StatusCode == http.StatusOK && strings.TrimSpace(out.URL) != "":
		return out.URL, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return "", ErrUnauthorized
	case resp.StatusCode == http.StatusConflict && out.Error == "already_subscribed":
		return "", ErrAlreadySubscribed
	case resp.StatusCode == http.StatusConflict && out.Error == "billed_by_apple":
		return "", ErrBilledByApple
	case resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusForbidden:
		return "", ErrNoCheckout
	default:
		return "", fmt.Errorf("apiclient: checkout unexpected status %d: %s", resp.StatusCode, out.Error)
	}
}

// ErrBilledByApple is CheckoutBearer's answer for an account whose plan
// Apple bills: it is managed with Apple, never sold here.
var ErrBilledByApple = errors.New("apiclient: Apple bills the account's plan")

// PackCheckoutBearer opens a pack's one-time checkout (POST
// /v1/me/billing/checkout {"pack": …}) and returns Stripe's URL. With a
// member's request id, the pack answers it.
func (c *Client) PackCheckoutBearer(ctx context.Context, bearer, pack, requestID string) (string, error) {
	if c == nil {
		return "", errors.New("apiclient: nil client")
	}
	payload, _ := json.Marshal(map[string]string{"pack": pack, "request_id": requestID})
	body, err := c.do(ctx, bearer, http.MethodPost, "/v1/me/billing/checkout", bytes.NewReader(payload))
	switch {
	case errors.Is(err, ErrConflict), errors.Is(err, ErrForbidden), errors.Is(err, ErrReceiptNotFound):
		return "", ErrNoCheckout
	case err != nil:
		return "", err
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &out); err != nil || strings.TrimSpace(out.URL) == "" {
		return "", fmt.Errorf("apiclient: pack checkout without a url")
	}
	return out.URL, nil
}

// DeclinePackRequestBearer declines a member's request for a pack, with an
// optional note.
func (c *Client) DeclinePackRequestBearer(ctx context.Context, bearer, id, note string) error {
	payload, _ := json.Marshal(map[string]string{"note": note})
	_, err := c.do(ctx, bearer, http.MethodPost, "/v1/me/pack-requests/"+url.PathEscape(id)+"/decline", bytes.NewReader(payload))
	return err
}
