// Package apiclient is the apps/web side of the cross-binary
// boundary: it fetches receipts (and, later, other tenant
// resources) from the apps/api binary on behalf of the dashboard.
//
// v0.1 scope is intentionally minimal:
//
//   - one configured base URL (KIFF_API_BASE_URL)
//   - one configured operator API key (KIFF_DEMO_API_KEY)
//   - read-only GET /v1/receipts/{trace_id}
//
// This is not the production cross-binary auth story. Real
// production wiring forwards the end-user's Clerk session from
// apps/web to apps/api so each user sees only their tenant's
// data. The operator-API-key shape exists because the issue this
// slice closes needs a demoable dashboard surface for the
// prospect calls, not a multi-tenant logged-in dashboard.
//
// When the configured key is the demo tenant's, anyone hitting
// /dashboard/receipts/<trace_id> sees that tenant's receipts.
// Production deploys must not expose this surface; the handler
// guards against accidental enable by refusing to render when
// the env vars are unset.
package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Errors returned by Client. Distinct types so handlers can branch
// on the failure mode (404 vs unreachable vs misconfigured).
var (
	ErrNotConfigured   = errors.New("apiclient: not configured (set KIFF_API_BASE_URL and KIFF_DEMO_API_KEY)")
	ErrReceiptNotFound = errors.New("apiclient: receipt not found")
	ErrUnauthorized    = errors.New("apiclient: unauthorized (check KIFF_DEMO_API_KEY)")

	// ErrForbidden maps a 403: the caller is authenticated but lacks
	// the role for the operation (e.g. a runtime key trying to flip a
	// domain's governance posture, which is management-gated).
	ErrForbidden = errors.New("apiclient: forbidden (management role required)")
	// ErrDomainExists maps the create endpoint's 409: the tenant
	// already has a domain with this name. Surfaced so the create
	// flow can show a precise "pick another name" message rather than
	// a generic failure.
	ErrDomainExists = errors.New("apiclient: a domain with this name already exists")
	// ErrDomainIsDefault maps the delete endpoint's 409: the addressed
	// domain is the tenant's default and backs the singular routes, so
	// it can't be deleted. The delete flow shows a precise message.
	ErrDomainIsDefault = errors.New("apiclient: the default domain cannot be deleted")
	// ErrDomainLimitReached maps the create endpoint's 403: the tenant
	// is at its plan's domain cap. The create flow shows an
	// upgrade/delete message rather than a generic failure.
	ErrDomainLimitReached = errors.New("apiclient: domain limit reached for this plan")
	// ErrDomainRenameUnsupported maps the update endpoint's 409: the
	// edited YAML changed the domain name. In-place edit keeps the
	// identity; rename is a separate operation. The edit flow shows a
	// precise message.
	ErrDomainRenameUnsupported = errors.New("apiclient: a domain's name can't change on edit (rename is separate)")
	// ErrTicketNotFound maps the support endpoints' 404: the ticket
	// does not exist, or belongs to another tenant (indistinguishable
	// by design). The support flow shows a "no longer exists" message.
	ErrTicketNotFound = errors.New("apiclient: support ticket not found")
)

// Client is the apps/web HTTP client for apps/api. Built once at
// startup, reused per request. Concurrent-safe.
//
// Two auth modes coexist:
//
//   - operator-key mode (legacy): the dashboard binary holds one
//     API key and uses it on every call. Wired by New() when
//     APIKey is set. Used by the public-receipt page.
//   - per-session mode: the dashboard forwards the signed-in
//     user's bearer (Clerk JWT or stub-mode subject) on every
//     call. Wired by NewSessionAware() and consumed by the *Bearer
//     methods. Used by /dashboard/* once the auth slice lands.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// Config configures a Client. baseURL points at the apps/api root
// (e.g. https://api.kiff.dev or http://localhost:8082); apiKey is
// the operator key the dashboard uses on every fetched call.
type Config struct {
	BaseURL string
	APIKey  string
	// Timeout bounds every outbound call. Defaults to 10 seconds
	// when zero.
	Timeout time.Duration
}

// New returns a Client. Returns ErrNotConfigured when either
// BaseURL or APIKey is empty — the dashboard handler treats that
// as "no dashboard surface" and renders an explanatory page.
func New(cfg Config) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	key := strings.TrimSpace(cfg.APIKey)
	if base == "" || key == "" {
		return nil, ErrNotConfigured
	}
	if _, err := url.Parse(base); err != nil {
		return nil, fmt.Errorf("apiclient: parse base url: %w", err)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		baseURL: base,
		apiKey:  key,
		http:    &http.Client{Timeout: timeout},
	}, nil
}

// NewSessionAware returns a Client that has no operator API key.
// Suitable for the /dashboard/* surface where every call carries
// the signed-in user's bearer. Returns an error only when the
// base URL is missing or unparsable; an empty APIKey is allowed
// and disables the legacy operator-key methods.
//
// Methods on the returned client that require a bearer (the
// *Bearer suffix variants) accept it per-call. The legacy
// methods (GetReceipt, etc.) refuse to run when apiKey is empty
// to prevent accidental anonymous calls.
func NewSessionAware(baseURL string, timeout time.Duration) (*Client, error) {
	return NewSessionAwareWithTransport(baseURL, timeout, nil)
}

// NewSessionAwareWithTransport returns a session-aware client that sends
// requests through transport. A nil transport uses http.DefaultTransport.
// The public demo uses this seam to run the real dashboard against an
// in-process fixture API without opening a socket or contacting apps/api.
func NewSessionAwareWithTransport(baseURL string, timeout time.Duration, transport http.RoundTripper) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, errors.New("apiclient: BaseURL is required")
	}
	if _, err := url.Parse(base); err != nil {
		return nil, fmt.Errorf("apiclient: parse base url: %w", err)
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		baseURL: base,
		http:    &http.Client{Timeout: timeout, Transport: tenantTransport{next: transport}},
	}, nil
}

// Receipt is the apps/web view of an Attested Action Receipt.
//
// We define the shape here rather than depending on the apps/api
// receipt package directly so the web binary stays a separate Go
// module. The fields are the v0.1 contract from the receipts
// design §Decision; if apps/api ever bumps the schema, the dash
// surface gets explicit version-aware code rather than a silent
// type error.
type Receipt struct {
	SchemaVersion string `json:"schema_version"`
	TraceID       string `json:"trace_id"`
	ProposalID    string `json:"proposal_id,omitempty"`
	TenantID      string `json:"tenant_id"`
	EntityID      string `json:"entity_id"`
	EntityType    string `json:"entity_type"`
	ActionName    string `json:"action_name"`
	// DomainID is the operational domain the decision was made
	// against, stamped on the receipt by apps/api at build time. Empty
	// for receipts built before domain stamping; the receipt-reveal
	// resolver falls back to the tenant's first domain in that case.
	DomainID        string              `json:"domain_id,omitempty"`
	Outcome         string              `json:"outcome,omitempty"`
	IssuedAt        time.Time           `json:"issued_at"`
	CausalChain     []ChainStep         `json:"causal_chain"`
	Attestation     *Attestation        `json:"attestation"`
	RiskScore       *RiskScore          `json:"risk_score"`
	ReplayLink      string              `json:"replay_link,omitempty"`
	ComplianceMap   []ComplianceMapping `json:"compliance_map"`
	InsurerEnvelope *InsurerEnvelope    `json:"insurer_envelope"`
}

// ChainStep mirrors apps/api receipt.ChainStep.
type ChainStep struct {
	Sequence   int       `json:"sequence"`
	Kind       string    `json:"kind"`
	ActorID    string    `json:"actor_id,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
	Message    string    `json:"message,omitempty"`
	DataHash   string    `json:"data_hash"`
	// Data is a step's own signed values when it is not an audit record:
	// a settled receipt's answer and tool report.
	Data map[string]any `json:"data,omitempty"`
}

// Attestation mirrors apps/api receipt.Attestation.
type Attestation struct {
	Algorithm     string    `json:"algorithm"`
	Signature     string    `json:"signature"`
	WalletAddress string    `json:"wallet_address"`
	AnchorTxHash  string    `json:"anchor_tx_hash"`
	AnchoredAt    time.Time `json:"anchored_at"`
	// Network is the anchor chain ("base-sepolia" / "base-mainnet"),
	// empty when signed-but-unanchored. Drives the tier label and
	// the block-explorer link on the receipt surface.
	Network string `json:"network,omitempty"`
}

// RiskScore mirrors apps/api receipt.RiskScore.
type RiskScore struct {
	Prior          float64 `json:"prior"`
	Instance       float64 `json:"instance"`
	DatasetVersion string  `json:"dataset_version"`
	SampleSize     int     `json:"sample_size"`
}

// ComplianceMapping mirrors apps/api receipt.ComplianceMapping.
type ComplianceMapping struct {
	Framework    string `json:"framework"`
	ControlID    string `json:"control_id"`
	EvidenceKind string `json:"evidence_kind"`
}

// InsurerEnvelope mirrors apps/api receipt.InsurerEnvelope.
type InsurerEnvelope struct {
	PartnerID string `json:"partner_id"`
}

// GetReceipt fetches the receipt for the given trace id. Returns
// ErrReceiptNotFound on 404, ErrUnauthorized on 401, or a wrapping
// error for any other failure.
func (c *Client) GetReceipt(ctx context.Context, traceID string) (*Receipt, error) {
	if traceID == "" {
		return nil, errors.New("apiclient: empty trace id")
	}
	endpoint := c.baseURL + "/v1/receipts/" + url.PathEscape(traceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("apiclient: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: get receipt: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var r Receipt
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, fmt.Errorf("apiclient: decode receipt: %w", err)
		}
		return &r, nil
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case http.StatusNotFound:
		return nil, ErrReceiptNotFound
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("apiclient: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// Configured reports whether the client has a non-empty base URL
// and key. Cheap helper for handlers that want to render a "not
// configured" page rather than a request that will obviously fail.
func (c *Client) Configured() bool {
	return c != nil && c.baseURL != "" && c.apiKey != ""
}

// BaseURL returns the configured API base URL. Exposed so the
// dashboard footer can render where the data came from.
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.baseURL
}
