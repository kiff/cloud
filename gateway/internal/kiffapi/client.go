// Package kiffapi is the gateway's client for apps/api. Every call is
// made with the agent's own key, so apps/api applies that key's tenant,
// agent binding and roles exactly as for any other caller.
package kiffapi

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
	"time"

	"github.com/kiff/cloud/gateway/internal/requestlog"
)

// Client talks to apps/api.
type Client struct {
	base string
	http *http.Client
}

// New returns a client for the API at base (e.g. http://127.0.0.1:8080).
func New(base string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{base: strings.TrimRight(base, "/"), http: hc}
}

// ErrUnauthorized: apps/api refused the key.
var ErrUnauthorized = errors.New("kiffapi: key refused")

// ErrUnavailable: apps/api couldn't be reached, or couldn't say whether
// the key is valid (a 5xx, its database down). Not the key's fault.
var ErrUnavailable = errors.New("kiffapi: KIFF is unavailable")

// Me is who a key speaks for.
type Me struct {
	APIKey   bool     `json:"api_key"`
	TenantID string   `json:"tenant_id"`
	AgentID  string   `json:"agent_id"`
	Roles    []string `json:"roles"`
	// CredentialID is the key the caller authenticated with, also the
	// key behind an OAuth access token.
	CredentialID string `json:"credential_id"`
	// Resource is the gateway URL an OAuth token was issued for.
	Resource string `json:"resource"`
	// ChatApprovals: the account lets a held call also be offered in the
	// agent's chat.
	ChatApprovals bool `json:"chat_approvals"`
	// Capabilities is what the caller may do on the account,
	// e.g. "tools.connect" for an admin or editor. Absent from an API
	// that predates memberships.
	Capabilities []string `json:"capabilities"`
	// DeviceSession: the caller is a phone's session bound to its key.
	// It may read, but never change the account here.
	DeviceSession bool `json:"device_session"`
}

// IsConnector reports whether the caller is a connector key:
// it connects tools in relay or verify mode, and tests and removes
// only the tools it connected.
func (m Me) IsConnector() bool {
	if !m.APIKey {
		return false
	}
	for _, r := range m.Roles {
		if r == "connector" {
			return true
		}
	}
	return false
}

// CanRead reports whether the caller may list the account's connected
// tools: anyone who can manage them, and a phone's session of a person
// whose role could (a phone starting a taf lists the tools to give it).
func (m Me) CanRead() bool {
	if m.CanManage() {
		return true
	}
	if !m.DeviceSession || m.AgentID != "" {
		return false
	}
	for _, c := range m.Capabilities {
		if c == "tools.connect" {
			return true
		}
	}
	return false
}

// CanManage reports whether the caller may connect tools and change the
// account's domains: a person whose role holds tools.connect (an admin
// or editor), a connector key (which lets it write only the gateway's
// domain), or a key with tenant_owner or admin, and
// never a key bound to an agent.
func (m Me) CanManage() bool {
	if m.AgentID != "" || m.DeviceSession {
		return false
	}
	if m.Capabilities != nil {
		for _, c := range m.Capabilities {
			if c == "tools.connect" {
				return true
			}
		}
		return false
	}
	for _, r := range m.Roles {
		if r == "tenant_owner" || r == "admin" {
			return true
		}
	}
	return false
}

// Exception is the handle of a held decision.
type Exception struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	ReviewURL string `json:"review_url"`
	// ExpiresAt is when a held call stops waiting for the owner and is
	// refused.
	ExpiresAt *time.Time `json:"expires_at"`
}

// Decision is apps/api's answer to a proposal.
type Decision struct {
	Outcome   string     `json:"outcome"`
	Reasons   []string   `json:"reasons"`
	Message   string     `json:"message"`
	Exception *Exception `json:"exception"`
	// Permit is the call's execution permit, on the allowed
	// decision that minted it. PermitIssuedAt is when the operation's one
	// permit was issued, on a decision that did not mint it because another
	// request for the same operation already had.
	Permit         string     `json:"permit"`
	PermitIssuedAt *time.Time `json:"permit_issued_at"`
}

// PermitRequest asks apps/api for the call's execution permit: the
// audience the tool checks, the upstream tool name, and the exact
// arguments forwarded, which apps/api hashes into the permit.
type PermitRequest struct {
	Audience  string          `json:"audience"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

// Proposal is a decide request for one tool call.
type Proposal struct {
	ID         string         `json:"id"`
	Domain     string         `json:"domain"`
	EntityID   string         `json:"entity_id"`
	EntityType string         `json:"entity_type"`
	ActionName string         `json:"action_name"`
	ActorID    string         `json:"actor_id"`
	Parameters map[string]any `json:"parameters"`
	Permit     *PermitRequest `json:"permit,omitempty"`
	// TrialDraft is the draft whose trial makes the call (kiff/tafs#173):
	// sent, and decided on the trial's own Cards.
	TrialDraft string `json:"trial_draft,omitempty"`
}

func (c *Client) do(ctx context.Context, key, method, path string, body any) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	if id := requestlog.ID(ctx); id != "" {
		req.Header.Set(requestlog.Header, id)
	}
	if t := TenantFrom(ctx); t != "" {
		req.Header.Set(TenantHeader, t)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return resp.StatusCode, raw, ErrUnauthorized
	}
	return resp.StatusCode, raw, nil
}

func apiError(status int, raw []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	if e.Error == "" {
		e.Error = strings.TrimSpace(string(raw))
	}
	return fmt.Errorf("kiffapi: %d: %s", status, e.Error)
}

// Me resolves the key.
func (c *Client) Me(ctx context.Context, key string) (Me, error) {
	status, raw, err := c.do(ctx, key, http.MethodGet, "/v1/me", nil)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			return Me{}, err
		}
		return Me{}, errors.Join(ErrUnavailable, err)
	}
	if status != http.StatusOK {
		if status >= 500 {
			return Me{}, errors.Join(ErrUnavailable, apiError(status, raw))
		}
		return Me{}, apiError(status, raw)
	}
	var m Me
	if err := json.Unmarshal(raw, &m); err != nil {
		return Me{}, err
	}
	// Only the verified API answer can enrich the request log. Never
	// classify an unverified token from its prefix or caller headers.
	kind := "person"
	if m.APIKey {
		kind = "account_key"
	}
	if m.AgentID != "" {
		kind = "agent"
	}
	if strings.HasPrefix(key, "kiff_oat_") {
		kind = "oauth"
	}
	requestlog.Authenticated(ctx, kind, m.TenantID, m.AgentID)
	return m, nil
}

// AgentCard is the caller's own read of the Cards that apply to its
// gateway calls (GET /v1/me/card). The numbers are as of the read;
// every call is still decided when it is made.
type AgentCard struct {
	AgentID string `json:"agent_id"`
	Cards   []Card `json:"cards"`
}

// Card is one Card in an AgentCard.
type Card struct {
	ID         string      `json:"id"`
	Grants     []CardGrant `json:"grants"`
	Limits     []CardLimit `json:"limits"`
	OnExceed   string      `json:"on_exceed"`
	ValidUntil *time.Time  `json:"valid_until,omitempty"`
}

// CardGrant is an action a Card allows, with its per-call limits.
type CardGrant struct {
	Action       string         `json:"action"`
	PerActionMax []PerActionMax `json:"per_action_max,omitempty"`
	// Resources limits the grant to named resources.
	Resources *CardResources `json:"resources,omitempty"`
}

// CardResources is a grant's resource scope: the calls it covers name
// one of Values in Parameter.
type CardResources struct {
	Parameter string   `json:"parameter"`
	Values    []string `json:"values"`
}

// PerActionMax caps one call: quantity is "sum(<argument>)".
type PerActionMax struct {
	Quantity string `json:"quantity"`
	Max      int64  `json:"max"`
}

// CardLimit is a total over a window, with what is used and left.
// Status is "unavailable" when the use could not be read.
type CardForecast struct {
	State              string     `json:"state"`
	ObservationSeconds int64      `json:"observation_seconds"`
	ObservedAt         time.Time  `json:"observed_at"`
	RunsOutAt          *time.Time `json:"runs_out_at,omitempty"`
	ResetsAt           *time.Time `json:"resets_at,omitempty"`
	NextCapacityAt     *time.Time `json:"next_capacity_at,omitempty"`
	Rate               float64    `json:"quantity_per_second,omitempty"`
}

type CardLimit struct {
	Forecast        *CardForecast   `json:"forecast,omitempty"`
	Measure         string          `json:"measure"`
	BaseLimit       int64           `json:"base_limit"`
	ExtraRoom       int64           `json:"extra_room"`
	RoomGrants      json.RawMessage `json:"room_grants"`
	GrossAuthorized int64           `json:"gross_authorized"`
	Released        int64           `json:"released"`
	NetCounted      int64           `json:"net_counted"`
	Quantity        string          `json:"quantity"`
	Limit           int64           `json:"limit"`
	Window          string          `json:"window"`
	Used            int64           `json:"used"`
	Remaining       int64           `json:"remaining"`
	Status          string          `json:"status"`
	// Unit and Scale say what a summed amount means (unit EUR, scale 2:
	// 8000 is 80.00 EUR), when the domain declares it. Empty unit: bare.
	Unit  string `json:"unit,omitempty"`
	Scale int    `json:"scale,omitempty"`
}

// AgentCard reads the caller's own Cards. During a trial against a draft
// (trial is its id, kiff/tafs#107), the Cards that draft would leave; "" or
// "live", the Cards in force.
func (c *Client) AgentCard(ctx context.Context, key, trial string) (AgentCard, error) {
	path := "/v1/me/card"
	if strings.HasPrefix(trial, "drf_") {
		path += "?trial=" + url.QueryEscape(trial)
	}
	status, raw, err := c.do(ctx, key, http.MethodGet, path, nil)
	if err != nil {
		return AgentCard{}, err
	}
	if status != http.StatusOK {
		return AgentCard{}, apiError(status, raw)
	}
	var card AgentCard
	if err := json.Unmarshal(raw, &card); err != nil {
		return AgentCard{}, err
	}
	return card, nil
}

// RequestToolCall records TOOL_CALL_REQUESTED for a new ToolCall entity.
func (c *Client) RequestToolCall(ctx context.Context, key, domain, entityID, agentID string, at time.Time) error {
	ev := map[string]any{
		"id": "evt-" + entityID, "adapter": "cloud", "type": "TOOL_CALL_REQUESTED",
		"source": "kiff-gateway", "received_at": at.UTC().Format(time.RFC3339),
		"entity_id": entityID, "entity_type": "ToolCall", "actor_id": agentID,
		"payload": map[string]any{},
	}
	status, raw, err := c.do(ctx, key, http.MethodPost, "/v1/events/raw?domain="+url.QueryEscape(domain), ev)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated && status != http.StatusAccepted {
		return apiError(status, raw)
	}
	return nil
}

// Decide asks for a decision. A response that carries an outcome is a
// decision whatever its status; anything else is an error.
func (c *Client) Decide(ctx context.Context, key string, p Proposal) (Decision, error) {
	status, raw, err := c.do(ctx, key, http.MethodPost, "/v1/proposals/decide", p)
	if err != nil {
		return Decision{}, err
	}
	var d Decision
	if json.Unmarshal(raw, &d) == nil && d.Outcome != "" {
		return d, nil
	}
	return Decision{}, apiError(status, raw)
}

// TrialCall is one call a trial of an agent's next version makes
// (kiff/tafs#107), asked of POST /v1/gateway/trial. Draft is whose Cards
// decide it: "live", or a draft's id.
type TrialCall struct {
	ActionName string         `json:"action_name"`
	Parameters map[string]any `json:"parameters"`
	Draft      string         `json:"draft"`
	// Run is the trial's run: its earlier allowed writes count toward
	// the Cards' limits for this one.
	Run string `json:"run"`
}

// TrialDecision is KIFF's answer to a trial call: "allowed", "held" or
// "refused". KIFF records nothing for it.
type TrialDecision struct {
	Decision string   `json:"decision"`
	Reasons  []string `json:"reasons"`
	Message  string   `json:"message"`
}

// Trial asks how the key's agent's Cards would decide c. A response
// without a decision is an error.
func (c *Client) Trial(ctx context.Context, key string, t TrialCall) (TrialDecision, error) {
	status, raw, err := c.do(ctx, key, http.MethodPost, "/v1/gateway/trial", t)
	if err != nil {
		return TrialDecision{}, err
	}
	var d TrialDecision
	if status == http.StatusOK && json.Unmarshal(raw, &d) == nil && d.Decision != "" {
		return d, nil
	}
	return TrialDecision{}, apiError(status, raw)
}

// TrialOpen reports whether draft is being tried (kiff/tafs#173), as
// GET /v1/me/agent-drafts/{id}/trial says to the draft's agent: then its
// trial's calls are sent. A draft never tried, another agent's, or any other
// 4xx is not; a 5xx or no answer is an error, and then nothing is sent.
func (c *Client) TrialOpen(ctx context.Context, key, draft string) (bool, error) {
	status, raw, err := c.do(ctx, key, http.MethodGet, "/v1/me/agent-drafts/"+url.PathEscape(draft)+"/trial", nil)
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		var t struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return false, err
		}
		return t.Status == "trying", nil
	case http.StatusNotFound:
		return false, nil
	}
	// Any other answer KIFF gave about the request (refused, unknown, not
	// the agent's) is not a trial it may send for: decided only, as before.
	// Only KIFF failing to answer is an error, and then nothing is sent.
	if status >= 400 && status < 500 {
		return false, nil
	}
	return false, apiError(status, raw)
}

// PermitTarget is where an action's execution permit goes.
type PermitTarget struct {
	// Domain is the domain whose Cards authorize the action: the gateway's.
	Domain    string `json:"domain"`
	Action    string `json:"action"`
	Tool      string `json:"tool"`
	Audience  string `json:"audience"`
	AmountArg string `json:"amount_argument,omitempty"`
	// ResourceArg is bound like the amount: the resource the Card read is
	// the one in the signed arguments.
	ResourceArg string `json:"resource_argument,omitempty"`
}

// SetPermitTargets replaces the account's permit targets. It needs the
// same authority as ApplyDomain (domains.manage), so only the owner's or
// an admin's key can say where an action's permit goes.
func (c *Client) SetPermitTargets(ctx context.Context, key string, ts []PermitTarget) error {
	if ts == nil {
		ts = []PermitTarget{}
	}
	status, raw, err := c.do(ctx, key, http.MethodPut, "/v1/me/permit-targets", map[string]any{"targets": ts})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return apiError(status, raw)
	}
	return nil
}

// ApplyDomain replaces the named domain's contract, or creates it the
// first time. Going through the API (never Postgres) makes the running
// API drop its cached copy.
func (c *Client) ApplyDomain(ctx context.Context, key, domain, yaml string) error {
	put, err := c.yaml(ctx, key, http.MethodPut, "/v1/me/domains/"+url.PathEscape(domain), yaml)
	if err != nil {
		return err
	}
	if put.status == http.StatusOK || put.status == http.StatusCreated {
		return nil
	}
	if put.status != http.StatusNotFound {
		return apiError(put.status, put.body)
	}
	post, err := c.yaml(ctx, key, http.MethodPost, "/v1/me/domains", yaml)
	if err != nil {
		return err
	}
	if post.status == http.StatusOK || post.status == http.StatusCreated {
		return nil
	}
	return apiError(post.status, post.body)
}

type rawResp struct {
	status int
	body   []byte
}

func (c *Client) yaml(ctx context.Context, key, method, path, body string) (rawResp, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, strings.NewReader(body))
	if err != nil {
		return rawResp{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/yaml")
	if id := requestlog.ID(ctx); id != "" {
		req.Header.Set(requestlog.Header, id)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return rawResp{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return rawResp{}, ErrUnauthorized
	}
	return rawResp{status: resp.StatusCode, body: b}, err
}

// TenantHeader selects which of a person's accounts a session request
// operates on. The gateway forwards it from the dashboard's
// request; it is a selector, and apps/api checks the membership.
const TenantHeader = "Kiff-Tenant"

type tenantKey struct{}

// WithTenant carries the selected account to the calls made with ctx.
func WithTenant(ctx context.Context, tenant string) context.Context {
	if tenant == "" {
		return ctx
	}
	return context.WithValue(ctx, tenantKey{}, tenant)
}

// TenantFrom returns the selected account, or "".
func TenantFrom(ctx context.Context) string {
	t, _ := ctx.Value(tenantKey{}).(string)
	return t
}

// Hold is where one of the agent's held calls stands, by the exception id
// the gateway recorded with it (GET /v1/me/holds). Status is the
// exception's as of the read: held, approved, changed, consumed, rejected,
// expired or invalidated.
type Hold struct {
	ExceptionID string `json:"exception_id"`
	Action      string `json:"action"`
	Status      string `json:"status"`
	// Withdrawn: the agent withdrew this hold when its run was cancelled,
	// so it is invalidated by the agent's own act, not by the
	// owner or the Card.
	Withdrawn  bool       `json:"withdrawn,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	// NextStep and Note are, for a rejected hold, what the owner asked the
	// agent to do instead; empty for a plain refusal.
	NextStep string `json:"next_step,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Holds reads where the caller's own held calls stand. Read only: nothing
// is decided or redeemed.
func (c *Client) Holds(ctx context.Context, key string, ids []string) ([]Hold, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	status, raw, err := c.do(ctx, key, http.MethodGet, "/v1/me/holds?ids="+url.QueryEscape(strings.Join(ids, ",")), nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, apiError(status, raw)
	}
	var out struct {
		Holds []Hold `json:"holds"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Holds, nil
}
