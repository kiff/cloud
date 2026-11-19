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
	"strconv"
	"strings"
	"time"
)

// Template mirrors the apps/api templates.Template wire shape.
// The dashboard's onboarding card consumes Slug + Name +
// Description + the SampleEvent JSON for the curl block.
type Template struct {
	Slug           string          `json:"slug"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	SampleEvent    json.RawMessage `json:"sample_event"`
	DecisionScript json.RawMessage `json:"decision_script,omitempty"`
}

// IssuesCard reports whether this template's demo issues a KIFF Card
// on its way through.
//
// It changes what the run is: the others decide against sandbox data
// and leave nothing behind, while this one issues authority in the
// reader's own workspace. A gallery button that read the same for both
// would be the product performing its central act without being asked,
// so the surface has to be able to tell them apart.
func (t Template) IssuesCard() bool {
	if len(t.DecisionScript) == 0 {
		return false
	}
	var script struct {
		Steps []struct {
			Kind string `json:"kind"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(t.DecisionScript, &script); err != nil {
		return false
	}
	for _, step := range script.Steps {
		if step.Kind == "issue_card" {
			return true
		}
	}
	return false
}

// ListTemplatesBearer fetches GET /v1/templates.
func (c *Client) ListTemplatesBearer(ctx context.Context, bearer string) ([]Template, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/templates", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Templates []Template `json:"templates"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apiclient: decode templates: %w", err)
	}
	return resp.Templates, nil
}

// ApplyTemplateResponse mirrors the apps/api applyResponse.
type ApplyTemplateResponse struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Tenant  string `json:"tenant"`
	Version int    `json:"version"`
}

// ApplyTemplateBearer applies a starter domain to the caller's
// tenant.
func (c *Client) ApplyTemplateBearer(ctx context.Context, bearer, slug string) (*ApplyTemplateResponse, error) {
	payload, _ := json.Marshal(map[string]string{"slug": slug})
	body, err := c.do(ctx, bearer, http.MethodPost, "/v1/me/domain/template", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	var resp ApplyTemplateResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apiclient: decode apply: %w", err)
	}
	return &resp, nil
}

// CreateKeyBearer mints a new API key for the tenant. The
// returned plaintext is the only time the key is visible; the
// dashboard reveals it once via session-flash storage.
type CreatedKey struct {
	ID          string   `json:"id"`
	Tenant      string   `json:"tenant"`
	Subject     string   `json:"subject"`
	Label       string   `json:"label"`
	Roles       []string `json:"roles,omitempty"`
	AgentID     string   `json:"agent_id,omitempty"`
	Environment string   `json:"environment,omitempty"`
	Plaintext   string   `json:"plaintext"`
	// ExpiresAt is when the key stops working (RFC 3339); absent when it
	// doesn't expire.
	ExpiresAt *string `json:"expires_at,omitempty"`
}

// CreateKeyBearer issues an API key for the tenant. label may be
// empty; the cloud accepts up to 200 chars. roles requests the
// role set the minted key carries; nil leaves the key role-less
// (the historical behaviour kept for the demo runner). The cloud
// enforces non-escalation: only an admin caller can request a
// non-subset role set.
//
// The dashboard's demo runner calls this with nil because its
// post-mint flow uses the session bearer for downstream calls,
// not the freshly-minted plaintext. The onboarding flow passes
// the caller's roles so the snippet credential can authorize
// starter proposals on the tenant's domain.
func (c *Client) CreateKeyBearer(ctx context.Context, bearer, label string, roles []string) (*CreatedKey, error) {
	return c.CreateAgentKeyBearer(ctx, bearer, label, roles, "")
}

// CreateAgentKeyBearer is CreateKeyBearer for a key bound to one agent,
// the holder of a card. The cloud then refuses any proposal from the
// key that names another actor, and an action requiring a card accepts
// only a key bound to its holder. An empty agentID mints an unbound key.
func (c *Client) CreateAgentKeyBearer(ctx context.Context, bearer, label string, roles []string, agentID string) (*CreatedKey, error) {
	return c.CreateBoundKeyBearer(ctx, bearer, label, roles, agentID, "")
}

// CreateBoundKeyBearer is CreateAgentKeyBearer for a key also bound to
// one environment: the cloud decides its requests there and refuses one
// naming another. An empty environment leaves the key free to act in
// any. CreateExpiringKeyBearer is CreateKeyBearer with an expiry. A nil
// expiresAt leaves it to the cloud: an account key (the account roles,
// or viewer for a read-only one) then expires in 90 days, and any other
// key doesn't expire.
func (c *Client) CreateExpiringKeyBearer(ctx context.Context, bearer, label string, roles []string, expiresAt *time.Time) (*CreatedKey, error) {
	body := map[string]any{"label": label}
	if len(roles) > 0 {
		body["roles"] = roles
	}
	if expiresAt != nil {
		body["expires_at"] = expiresAt.UTC().Format(time.RFC3339)
	}
	return c.createKey(ctx, bearer, body)
}

func (c *Client) CreateBoundKeyBearer(ctx context.Context, bearer, label string, roles []string, agentID, environment string) (*CreatedKey, error) {
	body := map[string]any{"label": label}
	if len(roles) > 0 {
		body["roles"] = roles
	}
	if agentID != "" {
		body["agent_id"] = agentID
	}
	if environment != "" {
		body["environment"] = environment
	}
	return c.createKey(ctx, bearer, body)
}

func (c *Client) createKey(ctx context.Context, bearer string, body map[string]any) (*CreatedKey, error) {
	payload, _ := json.Marshal(body)
	respBody, err := c.do(ctx, bearer, http.MethodPost, "/v1/keys", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	var key CreatedKey
	if err := json.Unmarshal(respBody, &key); err != nil {
		return nil, fmt.Errorf("apiclient: decode key: %w", err)
	}
	return &key, nil
}

// KeyListItem mirrors the per-key shape returned by GET /v1/keys.
// Note no plaintext, no hash — only metadata for the listing.
type KeyListItem struct {
	ID          string   `json:"id"`
	Subject     string   `json:"subject"`
	Label       string   `json:"label"`
	Roles       []string `json:"roles,omitempty"`
	AgentID     string   `json:"agent_id,omitempty"`
	Environment string   `json:"environment,omitempty"`
	CreatedAt   string   `json:"created_at"`
	LastUsedAt  *string  `json:"last_used_at,omitempty"`
	// ExpiresAt is when the key stops working (RFC 3339); absent when it
	// doesn't expire.
	ExpiresAt *string `json:"expires_at,omitempty"`
}

// ListKeysBearer fetches the active API keys for the caller's
// tenant.
func (c *Client) ListKeysBearer(ctx context.Context, bearer string) ([]KeyListItem, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/keys", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Keys []KeyListItem `json:"keys"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apiclient: decode keys: %w", err)
	}
	return resp.Keys, nil
}

// AddKeyRolesBearer adds roles to an existing key (PATCH /v1/keys/{id})
// and returns the key as the list shows it. The cloud applies minting's
// rules: management callers only, and no role the caller does not hold.
func (c *Client) AddKeyRolesBearer(ctx context.Context, bearer, keyID string, roles []string) (*KeyListItem, error) {
	payload, _ := json.Marshal(map[string]any{"add_roles": roles})
	body, err := c.do(ctx, bearer, http.MethodPatch, "/v1/keys/"+url.PathEscape(keyID), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	var key KeyListItem
	if err := json.Unmarshal(body, &key); err != nil {
		return nil, fmt.Errorf("apiclient: decode key: %w", err)
	}
	return &key, nil
}

type GuardConnection struct {
	TenantID    string `json:"tenant_id"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	AgentID     string `json:"agent_id"`
	Workflow    string `json:"workflow"`
	Adapter     string `json:"adapter"`
	SDKVersion  string `json:"sdk_version,omitempty"`
	Mode        string `json:"mode"`
	// KeyID is the API key that most recently authenticated this runtime,
	// so the runtimes page can offer an inline "revoke this key" action —
	// the control lever for a runtime. Empty for JWT-authed runtimes.
	KeyID       string    `json:"key_id,omitempty"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	SeenCount   uint64    `json:"seen_count"`
}

type GuardToolObservation struct {
	Name              string          `json:"name"`
	Description       string          `json:"description,omitempty"`
	ParameterSchema   json.RawMessage `json:"parameter_schema,omitempty"`
	EntityArg         string          `json:"entity_arg,omitempty"`
	Action            string          `json:"action,omitempty"`
	EntityType        string          `json:"entity_type,omitempty"`
	Required          []string        `json:"required,omitempty"`
	ObservedCallCount uint64          `json:"observed_call_count,omitempty"`
}

type GuardObservation struct {
	TenantID    string                 `json:"tenant_id"`
	Project     string                 `json:"project"`
	Environment string                 `json:"environment"`
	AgentID     string                 `json:"agent_id"`
	Workflow    string                 `json:"workflow"`
	Tools       []GuardToolObservation `json:"tools"`
	ObservedAt  time.Time              `json:"observed_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
}

func (c *Client) ListGuardConnectionsBearer(ctx context.Context, bearer string) ([]GuardConnection, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/guard/connections", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Connections []GuardConnection `json:"connections"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apiclient: decode guard connections: %w", err)
	}
	return resp.Connections, nil
}

func (c *Client) GetGuardObservationBearer(ctx context.Context, bearer string, conn GuardConnection) (*GuardObservation, error) {
	q := url.Values{}
	q.Set("project", conn.Project)
	q.Set("environment", conn.Environment)
	q.Set("agent_id", conn.AgentID)
	q.Set("workflow", conn.Workflow)
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/guard/observations?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Observation GuardObservation `json:"observation"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apiclient: decode guard observation: %w", err)
	}
	return &resp.Observation, nil
}

// GuardCandidateField is one parameter of a candidate control, derived from
// the observed tool's parameter schema. Required mirrors the schema exactly.
type GuardCandidateField struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

// GuardCandidateEvidence cites the connected runtime a candidate was derived
// from. Source is always "kiff-guard runtime".
type GuardCandidateEvidence struct {
	Source      string `json:"source"`
	Tool        string `json:"tool"`
	Adapter     string `json:"adapter"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Workflow    string `json:"workflow"`
	Mode        string `json:"mode"`
}

// GuardCandidate is one runtime-grounded candidate control. PolicyThresholds
// and States are always null on the wire (KIFF never derives them); they are
// kept as json.RawMessage so the consumer sees the literal null rather than
// a defaulted value.
type GuardCandidate struct {
	Tool              string                 `json:"tool"`
	Action            string                 `json:"action"`
	EntityType        string                 `json:"entity_type"`
	EntityArg         string                 `json:"entity_arg"`
	Fields            []GuardCandidateField  `json:"fields"`
	Risk              string                 `json:"risk"`
	RiskReasons       []string               `json:"risk_reasons"`
	Evidence          GuardCandidateEvidence `json:"evidence"`
	ObservedCallCount uint64                 `json:"observed_call_count,omitempty"`
	PolicyThresholds  json.RawMessage        `json:"policy_thresholds"`
	States            json.RawMessage        `json:"states"`
}

// GuardCandidateCheck is one grounding assertion's result.
type GuardCandidateCheck struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// GuardGroundingReport is the structured grounding verdict for a runtime's
// candidate set. OK is the AND of every check.
type GuardGroundingReport struct {
	OK     bool                  `json:"ok"`
	Checks []GuardCandidateCheck `json:"checks"`
}

// GuardRuntimeRef is the runtime coordinate a candidate set was derived from.
type GuardRuntimeRef struct {
	AgentID     string `json:"agent_id"`
	Adapter     string `json:"adapter"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Workflow    string `json:"workflow"`
	Mode        string `json:"mode"`
}

// GuardRuntimeCandidates is the derived, grounded candidate set for one
// connected runtime.
type GuardRuntimeCandidates struct {
	Runtime    GuardRuntimeRef      `json:"runtime"`
	Candidates []GuardCandidate     `json:"candidates"`
	Grounding  GuardGroundingReport `json:"grounding"`
}

// ListGuardCandidatesBearer fetches GET /v1/guard/candidates: runtime-grounded
// candidate controls derived from the tenant's stored guard observations. This
// is the action-first onboarding source: the candidates the Review screen
// renders, traced to the same runtime boundary used for enforcement, never an
// uploaded manifest. The four scope params are optional; pass empty strings to
// derive across every connected runtime.
func (c *Client) ListGuardCandidatesBearer(ctx context.Context, bearer, project, environment, agentID, workflow string) ([]GuardRuntimeCandidates, error) {
	path := "/v1/guard/candidates"
	q := url.Values{}
	if project != "" {
		q.Set("project", project)
	}
	if environment != "" {
		q.Set("environment", environment)
	}
	if agentID != "" {
		q.Set("agent_id", agentID)
	}
	if workflow != "" {
		q.Set("workflow", workflow)
	}
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	body, err := c.do(ctx, bearer, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Source   string                   `json:"source"`
		Runtimes []GuardRuntimeCandidates `json:"runtimes"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apiclient: decode guard candidates: %w", err)
	}
	return resp.Runtimes, nil
}

// RevokeKeyBearer revokes one active API key for the caller's
// tenant. apps/api enforces tenant ownership; a key id from
// another tenant returns 404.
func (c *Client) RevokeKeyBearer(ctx context.Context, bearer, keyID string) error {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return errors.New("apiclient: empty key id")
	}
	_, err := c.do(ctx, bearer, http.MethodDelete, "/v1/keys/"+url.PathEscape(keyID), nil)
	return err
}

// ListReceiptsBearer fetches the most recent receipts for the
// caller's tenant. limit <= 0 uses the server default.
func (c *Client) ListReceiptsBearer(ctx context.Context, bearer string, limit int) ([]Receipt, error) {
	path := "/v1/receipts"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	body, err := c.do(ctx, bearer, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Receipts []Receipt `json:"receipts"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apiclient: decode receipts: %w", err)
	}
	return resp.Receipts, nil
}

// IngestEventBearer posts an event payload to /v1/events/raw on
// behalf of the caller's tenant. payload is the wire-format
// adapter.RawInput JSON; the dashboard's "send sample event"
// button hands the template's SampleEvent bytes verbatim.
//
// Returns the raw response body so the caller can echo it on the
// page (handy for dev / debugging).
func (c *Client) IngestEventBearer(ctx context.Context, bearer string, payload []byte) ([]byte, error) {
	return c.do(ctx, bearer, http.MethodPost, "/v1/events/raw", bytes.NewReader(payload))
}

// IngestEventForDomainBearer posts an event to one of the tenant's
// domains (/v1/events/raw?domain=). Without the selector the event
// lands on the tenant's default domain, not the one the caller means.
func (c *Client) IngestEventForDomainBearer(ctx context.Context, bearer, domain string, payload []byte) ([]byte, error) {
	return c.do(ctx, bearer, http.MethodPost, "/v1/events/raw?domain="+url.QueryEscape(domain), bytes.NewReader(payload))
}

// ExecuteActionBearer runs an action against an entity. Body is
// the framework's actionRequest shape:
//
//	{ "actor": {"id": "..."}, "parameters": {...}, "entity_type": "..." }
//
// Returns the raw response body so the caller can stash the
// trace_id reported by the framework.
func (c *Client) ExecuteActionBearer(ctx context.Context, bearer, entityID, actionName string, body []byte) ([]byte, error) {
	path := "/v1/entities/" + url.PathEscape(entityID) + "/actions/" + url.PathEscape(actionName) + "/execute"
	resp, err := c.doRaw(ctx, bearer, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return respBody, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("apiclient: execute action target not found: %s", strings.TrimSpace(string(respBody)))
	default:
		return nil, fmt.Errorf("apiclient: %s %s unexpected status %d: %s",
			http.MethodPost, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
}

// GetReceiptBearer fetches a receipt by trace id using the caller's
// session bearer (not the operator key). This is the session-aware
// counterpart to GetReceipt: it lets a signed-in user open their
// own receipts without the deploy needing KIFF_DEMO_API_KEY. The
// shared do() helper maps 404 -> ErrReceiptNotFound and 401 ->
// ErrUnauthorized, matching the operator-key path's contract.
func (c *Client) GetReceiptBearer(ctx context.Context, bearer, traceID string) (*Receipt, error) {
	if strings.TrimSpace(traceID) == "" {
		return nil, errors.New("apiclient: empty trace id")
	}
	raw, err := c.do(ctx, bearer, http.MethodGet, "/v1/receipts/"+url.PathEscape(traceID), nil)
	if err != nil {
		return nil, err
	}
	var rcpt Receipt
	if err := json.Unmarshal(raw, &rcpt); err != nil {
		return nil, fmt.Errorf("apiclient: decode receipt: %w", err)
	}
	return &rcpt, nil
}

// ReceiptDownloadMax bounds a downloaded receipt; a receipt is a few
// kilobytes.
const ReceiptDownloadMax = 8 << 20

// ErrReceiptIncomplete is a receipt body that did not arrive whole: cut
// short, over ReceiptDownloadMax, or not JSON.
var ErrReceiptIncomplete = errors.New("apiclient: receipt body incomplete")

// GetReceiptRawBearer is the receipt as the API holds it, byte for byte:
// what KIFF signed, for downloading. It never returns part of a receipt:
// a body cut short, over the bound, or not JSON is an error.
func (c *Client) GetReceiptRawBearer(ctx context.Context, bearer, traceID string) ([]byte, error) {
	if strings.TrimSpace(traceID) == "" {
		return nil, errors.New("apiclient: empty trace id")
	}
	resp, err := c.doRaw(ctx, bearer, http.MethodGet, "/v1/receipts/"+url.PathEscape(traceID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrReceiptNotFound
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("apiclient: GET receipt %s unexpected status %d", traceID, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, ReceiptDownloadMax+1))
	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: %v", ErrReceiptIncomplete, err)
	case len(body) > ReceiptDownloadMax:
		return nil, fmt.Errorf("%w: larger than %d bytes", ErrReceiptIncomplete, ReceiptDownloadMax)
	case !json.Valid(body):
		return nil, fmt.Errorf("%w: not complete JSON", ErrReceiptIncomplete)
	}
	return body, nil
}

// GrantApprovalBearer approves a pending action.
func (c *Client) GrantApprovalBearer(ctx context.Context, bearer, approvalID, actorID, reason string) error {
	return c.reviewApproval(ctx, bearer, approvalID, "grant", actorID, reason)
}

// DenyApprovalBearer denies a pending action.
func (c *Client) DenyApprovalBearer(ctx context.Context, bearer, approvalID, actorID, reason string) error {
	return c.reviewApproval(ctx, bearer, approvalID, "deny", actorID, reason)
}

func (c *Client) reviewApproval(ctx context.Context, bearer, approvalID, decision, actorID, reason string) error {
	payload := map[string]any{
		"actor": map[string]any{"id": actorID},
	}
	if reason != "" {
		payload["reason"] = reason
	}
	body, _ := json.Marshal(payload)
	if _, err := c.do(ctx, bearer, http.MethodPost,
		"/v1/approvals/"+url.PathEscape(approvalID)+"/"+decision, bytes.NewReader(body)); err != nil {
		return err
	}
	return nil
}

// do is the shared bearer-forwarding HTTP path. Handles 401 ->
// ErrUnauthorized, non-2xx -> wrapped error, and propagates the
// 200 body to the caller. Used by every *Bearer method above so
// the auth + error contract is uniform.
func (c *Client) do(ctx context.Context, bearer, method, path string, body io.Reader) ([]byte, error) {
	resp, err := c.doRaw(ctx, bearer, method, path, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return respBody, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrReceiptNotFound // reuse for 404 across read resources
	case resp.StatusCode == http.StatusConflict:
		return nil, fmt.Errorf("%w: %s %s: %s", ErrConflict,
			method, path, strings.TrimSpace(string(respBody)))
	case resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: %s %s: %s", ErrForbidden,
			method, path, strings.TrimSpace(string(respBody)))
	default:
		return nil, fmt.Errorf("apiclient: %s %s unexpected status %d: %s",
			method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
}

func (c *Client) doRaw(ctx context.Context, bearer, method, path string, body io.Reader) (*http.Response, error) {
	if c == nil {
		return nil, errors.New("apiclient: nil client")
	}
	if strings.TrimSpace(bearer) == "" {
		return nil, errors.New("apiclient: empty bearer")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("apiclient: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: %s %s: %w", method, path, err)
	}
	return resp, nil
}

// Convenience methods that use the embedded API key. These are
// wrappers around the *Bearer methods for clients created with
// New that have an operator API key. Used by the Playground
// demo tenant client.

// ListTemplates calls ListTemplatesBearer with the embedded API key.
func (c *Client) ListTemplates(ctx context.Context) ([]Template, error) {
	if c.apiKey == "" {
		return nil, errors.New("apiclient: no API key configured (use ListTemplatesBearer for session-aware calls)")
	}
	return c.ListTemplatesBearer(ctx, c.apiKey)
}

// ApplyTemplate calls ApplyTemplateBearer with the embedded API key.
func (c *Client) ApplyTemplate(ctx context.Context, slug string) (*ApplyTemplateResponse, error) {
	if c.apiKey == "" {
		return nil, errors.New("apiclient: no API key configured (use ApplyTemplateBearer for session-aware calls)")
	}
	return c.ApplyTemplateBearer(ctx, c.apiKey, slug)
}

// ListReceipts calls ListReceiptsBearer with the embedded API key.
func (c *Client) ListReceipts(ctx context.Context, limit int) ([]Receipt, error) {
	if c.apiKey == "" {
		return nil, errors.New("apiclient: no API key configured (use ListReceiptsBearer for session-aware calls)")
	}
	return c.ListReceiptsBearer(ctx, c.apiKey, limit)
}

// IngestEvent calls IngestEventBearer with the embedded API key.
func (c *Client) IngestEvent(ctx context.Context, payload []byte) ([]byte, error) {
	if c.apiKey == "" {
		return nil, errors.New("apiclient: no API key configured (use IngestEventBearer for session-aware calls)")
	}
	return c.IngestEventBearer(ctx, c.apiKey, payload)
}

// GetDomain calls GetDomainBearer with the embedded API key.
func (c *Client) GetDomain(ctx context.Context) (*Domain, error) {
	if c.apiKey == "" {
		return nil, errors.New("apiclient: no API key configured (use GetDomainBearer for session-aware calls)")
	}
	return c.GetDomainBearer(ctx, c.apiKey)
}

// ListApprovals calls ListApprovalsBearer with the embedded API key.
func (c *Client) ListApprovals(ctx context.Context, opts ApprovalsListOptions) (*ApprovalsList, error) {
	if c.apiKey == "" {
		return nil, errors.New("apiclient: no API key configured (use ListApprovalsBearer for session-aware calls)")
	}
	return c.ListApprovalsBearer(ctx, c.apiKey, opts)
}

// ExecuteAction calls ExecuteActionBearer with the embedded API key.
func (c *Client) ExecuteAction(ctx context.Context, entityID, actionName string, body []byte) ([]byte, error) {
	if c.apiKey == "" {
		return nil, errors.New("apiclient: no API key configured (use ExecuteActionBearer for session-aware calls)")
	}
	return c.ExecuteActionBearer(ctx, c.apiKey, entityID, actionName, body)
}

// DecisionOutcome is the decision returned by POST /v1/proposals/decide
// — the cloud's client-facing gate. Outcome is one of allowed | blocked |
// approval_required | invalid | limit_exceeded; allowed/blocked/
// approval_required all come back 200 (a refusal is a valid decision, not
// a transport error), so the runner reads the verdict from the body.
type DecisionOutcome struct {
	ProposalID string   `json:"proposal_id"`
	Outcome    string   `json:"outcome"`
	Reasons    []string `json:"reasons,omitempty"`
	Message    string   `json:"message,omitempty"`
	Mode       string   `json:"mode,omitempty"`
	// Exception is set on owner_required: the action is held for the
	// card's owner, answered at ReviewURL.
	Exception *struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		ReviewURL string `json:"review_url,omitempty"`
	} `json:"exception,omitempty"`
}

// DecideBearer posts a proposal to the gate and returns its decision.
// Body is the proposalRequest shape:
//
//	{ "id": "...", "entity_id": "...", "entity_type": "...",
//	  "action_name": "...", "actor_id": "...", "parameters": {...} }
//
// Roles are NOT sent in the body — the gate derives them from the
// authenticated identity (the cloud rejects body roles on decide). A
// blocked decision is a 200 with outcome=blocked, so this is not an error.
func (c *Client) DecideBearer(ctx context.Context, bearer string, body []byte) (*DecisionOutcome, error) {
	raw, err := c.do(ctx, bearer, http.MethodPost, "/v1/proposals/decide", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var out DecisionOutcome
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode decision: %w", err)
	}
	return &out, nil
}

// Decide calls DecideBearer with the embedded API key (demo-tenant /
// playground path).
func (c *Client) Decide(ctx context.Context, body []byte) (*DecisionOutcome, error) {
	if c.apiKey == "" {
		return nil, errors.New("apiclient: no API key configured (use DecideBearer for session-aware calls)")
	}
	return c.DecideBearer(ctx, c.apiKey, body)
}

// GetMe calls GetMeBearer with the embedded API key.
func (c *Client) GetMe(ctx context.Context) (*Me, error) {
	if c.apiKey == "" {
		return nil, errors.New("apiclient: no API key configured (use GetMeBearer for session-aware calls)")
	}
	return c.GetMeBearer(ctx, c.apiKey)
}
