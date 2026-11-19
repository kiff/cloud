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

// This file is the apps/web view of the domain-apex read projection
// served by apps/api:
//
//	GET /v1/me/domains            — list the tenant's domains (0 or 1 today)
//	GET /v1/me/domains/{domain}   — one domain's actions, lifecycle,
//	                                observed agents, and evidence entry point
//
// The shapes mirror the api wire contract deliberately rather than
// importing it, so the web binary stays a separate module.

// DomainListItem is one row of GET /v1/me/domains.
type DomainListItem struct {
	Name        string     `json:"name"`
	Entity      string     `json:"entity"`
	Version     int        `json:"version"`
	Status      string     `json:"status"`
	ActionCount int        `json:"action_count"`
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
	// Posture is the per-domain governance mode ("observe" | "enforce"),
	// empty when the control predates per-domain posture. Drives the
	// sidebar selector's posture pill.
	Posture string `json:"posture,omitempty"`
}

// DomainTransition is one lifecycle edge.
type DomainTransition struct {
	On   string `json:"on"`
	From string `json:"from"`
	To   string `json:"to"`
}

// DomainLifecycle is the domain's state-machine projection.
type DomainLifecycle struct {
	States      []string           `json:"states"`
	Events      []string           `json:"events"`
	Transitions []DomainTransition `json:"transitions"`
}

// DomainAgent is one observed proposing identity. It is derived from
// decisions seen against the domain's control over the observation
// window — an observed projection, not an authoritative roster.
type DomainAgent struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"` // "api_key" | "actor" | "subject"
	Operations int       `json:"operations"`
	LastSeen   time.Time `json:"last_seen"`
	// ViaKeys are the API keys an agent's requests came through, shown
	// beside the agent rather than as agents of their own.
	ViaKeys []string `json:"via_keys,omitempty"`
}

// DomainAgentWindow is the observation window the agents list covers.
type DomainAgentWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// DomainAgentsSection carries the observed-agents list with an explicit
// basis so the surface never reads it as authoritative membership.
// Status is "observed" when the api wired a decisions reader,
// "unavailable" otherwise.
type DomainAgentsSection struct {
	Status string             `json:"status"`
	Window *DomainAgentWindow `json:"window,omitempty"`
	Agents []DomainAgent      `json:"agents"`
}

// Observed reports whether the agents section is a live observed list.
func (s DomainAgentsSection) Observed() bool { return s.Status == "observed" }

// DomainEvidence is the entry point from a domain to its evidence view
// (the Control Room). ControlID is empty when no active control resolves.
type DomainEvidence struct {
	ControlID string `json:"control_id,omitempty"`
}

// DomainDetail is GET /v1/me/domains/{domain}.
type DomainDetail struct {
	Name        string              `json:"name"`
	Entity      string              `json:"entity"`
	Version     int                 `json:"version"`
	Status      string              `json:"status"`
	ActivatedAt *time.Time          `json:"activated_at,omitempty"`
	Posture     string              `json:"posture,omitempty"`
	Actions     []DomainAction      `json:"actions"`
	Lifecycle   DomainLifecycle     `json:"lifecycle"`
	Agents      DomainAgentsSection `json:"agents"`
	Evidence    DomainEvidence      `json:"evidence"`
}

// ListDomainsBearer fetches GET /v1/me/domains for the caller's tenant.
// Returns an empty slice (not an error) when the tenant has no domain.
func (c *Client) ListDomainsBearer(ctx context.Context, bearer string) ([]DomainListItem, error) {
	resp, err := c.doRaw(ctx, bearer, http.MethodGet, "/v1/me/domains", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		var out struct {
			Domains []DomainListItem `json:"domains"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("apiclient: decode domains: %w", err)
		}
		return out.Domains, nil
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	default:
		return nil, fmt.Errorf("apiclient: list domains unexpected status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// GetDomainDetailBearer fetches GET /v1/me/domains/{slug}. Returns
// ErrNoDomain when the tenant has no domain or the slug does not match
// the tenant's active domain (the api 404s both cases).
func (c *Client) GetDomainDetailBearer(ctx context.Context, bearer, slug string) (*DomainDetail, error) {
	if strings.TrimSpace(slug) == "" {
		return nil, errors.New("apiclient: empty domain slug")
	}
	resp, err := c.doRaw(ctx, bearer, http.MethodGet, "/v1/me/domains/"+url.PathEscape(slug), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		var d DomainDetail
		if err := json.Unmarshal(body, &d); err != nil {
			return nil, fmt.Errorf("apiclient: decode domain detail: %w", err)
		}
		return &d, nil
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case http.StatusNotFound:
		return nil, ErrNoDomain
	default:
		return nil, fmt.Errorf("apiclient: get domain %q unexpected status %d: %s",
			slug, resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// GetDomainUsageBearer fetches GET /v1/me/domains/{slug}/usage — the
// per-domain governed-operations slice: the same cloud counters as the
// account summary, but summed only over rows attributed to this domain
// via the gate-stamped entity_type. Used by the domain home's activity
// glance so the counts match the domain the page shows rather than
// tenant-wide totals. An unknown/empty domain returns zeroed counters
// (the api never 404s this read). Decodes into Usage; the extra
// "domain" field in the response is ignored.
func (c *Client) GetDomainUsageBearer(ctx context.Context, bearer, slug string) (*Usage, error) {
	if strings.TrimSpace(slug) == "" {
		return nil, errors.New("apiclient: empty domain slug")
	}
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/me/domains/"+url.PathEscape(slug)+"/usage", nil)
	if err != nil {
		return nil, err
	}
	var u Usage
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("apiclient: decode domain usage: %w", err)
	}
	return &u, nil
}

// GetDomainEvidenceBearer fetches GET /v1/me/domains/{slug}/evidence —
// the Control Room rendered as a domain's evidence view. The api
// resolves the domain's active control and returns the *same* payload
// as the control_id decisions route, so this reuses
// ControlDecisionsResponse verbatim. Returns ErrNoDomain when the
// tenant has no matching domain (the api 404s).
func (c *Client) GetDomainEvidenceBearer(ctx context.Context, bearer, slug string, opts ControlDecisionsOptions) (*ControlDecisionsResponse, error) {
	if strings.TrimSpace(slug) == "" {
		return nil, errors.New("apiclient: empty domain slug")
	}
	path := "/v1/me/domains/" + url.PathEscape(slug) + "/evidence"
	q := url.Values{}
	if !opts.From.IsZero() {
		q.Set("from", opts.From.UTC().Format(time.RFC3339))
	}
	if !opts.To.IsZero() {
		q.Set("to", opts.To.UTC().Format(time.RFC3339))
	}
	for _, outcome := range opts.Outcomes {
		q.Add("outcome", outcome)
	}
	if opts.Actor != "" {
		q.Set("actor", opts.Actor)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Offset > 0 {
		q.Set("offset", strconv.Itoa(opts.Offset))
	}
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}

	resp, err := c.doRaw(ctx, bearer, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		var out ControlDecisionsResponse
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("apiclient: decode domain evidence: %w", err)
		}
		if out.Decisions.Counts == nil {
			out.Decisions.Counts = map[string]int{}
		}
		if out.Decisions.Rows == nil {
			out.Decisions.Rows = []ProposalDecision{}
		}
		return &out, nil
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case http.StatusNotFound:
		return nil, ErrNoDomain
	default:
		return nil, fmt.Errorf("apiclient: get domain evidence %q unexpected status %d: %s",
			slug, resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// ListDomains calls ListDomainsBearer with the embedded API key. Used
// by the Playground's demo-tenant client so it consumes the
// /v1/me/domains apex like the rest of the dashboard, not the singular
// /v1/me/domain.
func (c *Client) ListDomains(ctx context.Context) ([]DomainListItem, error) {
	if c.apiKey == "" {
		return nil, errors.New("apiclient: no API key configured (use ListDomainsBearer for session-aware calls)")
	}
	return c.ListDomainsBearer(ctx, c.apiKey)
}

// GetDomainDetail calls GetDomainDetailBearer with the embedded API
// key (Playground demo-tenant client).
func (c *Client) GetDomainDetail(ctx context.Context, slug string) (*DomainDetail, error) {
	if c.apiKey == "" {
		return nil, errors.New("apiclient: no API key configured (use GetDomainDetailBearer for session-aware calls)")
	}
	return c.GetDomainDetailBearer(ctx, c.apiKey, slug)
}

// AsDomain projects the apex DomainDetail onto the legacy Domain shape
// the shared dashboard view renders (Overview, Playground), so callers
// can source from the /v1/me/domains apex without churning the shared
// view type. Lossy by design: the apex-only facets (status, agents,
// evidence) are dropped — the legacy shape only needs the domain name,
// entity, lifecycle, and actions.
func (d *DomainDetail) AsDomain() *Domain {
	if d == nil {
		return nil
	}
	return &Domain{
		Domain:  d.Name,
		Entity:  d.Entity,
		Version: d.Version,
		Events:  append([]string(nil), d.Lifecycle.Events...),
		States:  append([]string(nil), d.Lifecycle.States...),
		Actions: append([]DomainAction(nil), d.Actions...),
	}
}

// CreateDomainFromTemplateBearer creates a domain from one of the
// cloud's starter templates via POST /v1/me/domains?template=<slug>.
// It becomes the tenant's default when it is the first domain; it never
// replaces an existing one. Errors follow CreateDomainBearer's contract:
// 409 → ErrDomainExists, 403 → ErrDomainLimitReached, 401 →
// ErrUnauthorized.
func (c *Client) CreateDomainFromTemplateBearer(ctx context.Context, bearer, slug string) (*Domain, error) {
	resp, err := c.doRaw(ctx, bearer, http.MethodPost, "/v1/me/domains?template="+url.QueryEscape(slug), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		var d Domain
		if err := json.Unmarshal(body, &d); err != nil {
			return nil, fmt.Errorf("apiclient: decode create domain response: %w", err)
		}
		return &d, nil
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case http.StatusForbidden:
		return nil, ErrDomainLimitReached
	case http.StatusConflict:
		return nil, ErrDomainExists
	default:
		return nil, fmt.Errorf("apiclient: POST /v1/me/domains?template=%s unexpected status %d: %s",
			slug, resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// CreateDomainBearer creates an additional, non-default domain for
// the tenant from YAML via POST /v1/me/domains. Returns the created
// domain's summary on 201.
//
// Error contract, so the create flow can show a precise message:
//   - 401 → ErrUnauthorized (bounce to /sign-in)
//   - 409 → ErrDomainExists (the name is taken; pick another)
//   - 422 → *DomainValidationError carrying the cloud's issues
//   - 413 → a "too large" wrapping error
//
// Goes around the shared do() helper because the body is raw YAML,
// not JSON, mirroring sendDomainYAML on the singular upload path.
func (c *Client) CreateDomainBearer(ctx context.Context, bearer string, yaml []byte) (*Domain, error) {
	if c == nil {
		return nil, errors.New("apiclient: nil client")
	}
	if strings.TrimSpace(bearer) == "" {
		return nil, errors.New("apiclient: empty bearer")
	}
	if len(yaml) == 0 {
		return nil, errors.New("apiclient: empty yaml")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/me/domains", bytes.NewReader(yaml))
	if err != nil {
		return nil, fmt.Errorf("apiclient: build create domain request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/x-yaml")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: POST /v1/me/domains: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		var d Domain
		if err := json.Unmarshal(body, &d); err != nil {
			return nil, fmt.Errorf("apiclient: decode create domain response: %w", err)
		}
		return &d, nil
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case http.StatusForbidden:
		return nil, ErrDomainLimitReached
	case http.StatusConflict:
		return nil, ErrDomainExists
	case http.StatusUnprocessableEntity:
		var verr rawValidateErrorBody
		_ = json.Unmarshal(body, &verr) // best-effort; struct stays zero on bad JSON
		return nil, &DomainValidationError{
			StatusCode: resp.StatusCode,
			Message:    firstNonEmptyString(verr.Error, "domain validation failed"),
			Issues:     verr.Issues,
		}
	case http.StatusRequestEntityTooLarge:
		return nil, fmt.Errorf("apiclient: POST /v1/me/domains: yaml exceeds the cloud's per-request limit")
	default:
		return nil, fmt.Errorf("apiclient: POST /v1/me/domains unexpected status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// DeleteDomainBearer deletes a domain by slug via DELETE
// /v1/me/domains/{slug}. Maps the endpoint's contract to typed
// errors so the delete flow can show a precise message:
//   - 204/200 → nil (deleted)
//   - 401 → ErrUnauthorized (bounce to /sign-in)
//   - 404 → ErrNoDomain (unknown/stale slug)
//   - 409 → ErrDomainIsDefault (the default can't be deleted)
func (c *Client) DeleteDomainBearer(ctx context.Context, bearer, slug string) error {
	if strings.TrimSpace(slug) == "" {
		return errors.New("apiclient: empty domain slug")
	}
	resp, err := c.doRaw(ctx, bearer, http.MethodDelete, "/v1/me/domains/"+url.PathEscape(slug), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusNotFound:
		return ErrNoDomain
	case http.StatusConflict:
		return ErrDomainIsDefault
	default:
		return fmt.Errorf("apiclient: DELETE /v1/me/domains/%s unexpected status %d: %s",
			slug, resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// SetDomainModeBearer flips a domain's governance posture
// (observe ↔ enforce) via PUT /v1/me/domains/{slug}/mode — the
// per-domain posture the decide path honors. Management-gated by the
// api; a runtime key gets 403. Maps the contract to typed errors:
//   - 200 → nil (persisted)
//   - 401 → ErrUnauthorized
//   - 403 → ErrForbidden (the caller can't change enforcement)
//   - 404 → ErrNoDomain (unknown slug / no active control)
func (c *Client) SetDomainModeBearer(ctx context.Context, bearer, slug, mode string) error {
	if strings.TrimSpace(slug) == "" {
		return errors.New("apiclient: empty domain slug")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	// "shadow" is the earlier spelling of "observe"; normalized here so
	// the dashboard only ever PUTs the canonical value.
	if mode == "shadow" {
		mode = "observe"
	}
	if mode != "observe" && mode != "enforce" {
		return errors.New("apiclient: mode must be 'observe' or 'enforce'")
	}
	payload, err := json.Marshal(map[string]string{"mode": mode})
	if err != nil {
		return fmt.Errorf("apiclient: encode mode request: %w", err)
	}
	resp, err := c.doRaw(ctx, bearer, http.MethodPut, "/v1/me/domains/"+url.PathEscape(slug)+"/mode", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusNotFound:
		return ErrNoDomain
	default:
		return fmt.Errorf("apiclient: PUT /v1/me/domains/%s/mode unexpected status %d: %s",
			slug, resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// GetDomainYAMLBearer fetches GET /v1/me/domains/{slug}/yaml — the
// domain's stored YAML, so the edit surface can pre-fill the editor.
// 401 → ErrUnauthorized, 404 → ErrNoDomain.
func (c *Client) GetDomainYAMLBearer(ctx context.Context, bearer, slug string) (string, error) {
	if strings.TrimSpace(slug) == "" {
		return "", errors.New("apiclient: empty domain slug")
	}
	resp, err := c.doRaw(ctx, bearer, http.MethodGet, "/v1/me/domains/"+url.PathEscape(slug)+"/yaml", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		return string(body), nil
	case http.StatusUnauthorized:
		return "", ErrUnauthorized
	case http.StatusNotFound:
		return "", ErrNoDomain
	default:
		return "", fmt.Errorf("apiclient: GET /v1/me/domains/%s/yaml unexpected status %d: %s",
			slug, resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// UpdateDomainBearer re-authors a domain in place via PUT
// /v1/me/domains/{slug}. Maps the endpoint's contract to
// typed errors:
//   - 200 → the updated domain summary
//   - 401 → ErrUnauthorized
//   - 404 → ErrNoDomain (unknown/stale slug)
//   - 409 → ErrDomainRenameUnsupported (the YAML changed the name)
//   - 422 → *DomainValidationError with the cloud's issues
func (c *Client) UpdateDomainBearer(ctx context.Context, bearer, slug string, yaml []byte) (*Domain, error) {
	if strings.TrimSpace(slug) == "" {
		return nil, errors.New("apiclient: empty domain slug")
	}
	if len(yaml) == 0 {
		return nil, errors.New("apiclient: empty yaml")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/v1/me/domains/"+url.PathEscape(slug), bytes.NewReader(yaml))
	if err != nil {
		return nil, fmt.Errorf("apiclient: build update domain request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/x-yaml")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: PUT /v1/me/domains/%s: %w", slug, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		var d Domain
		if err := json.Unmarshal(body, &d); err != nil {
			return nil, fmt.Errorf("apiclient: decode update domain response: %w", err)
		}
		return &d, nil
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case http.StatusNotFound:
		return nil, ErrNoDomain
	case http.StatusConflict:
		return nil, ErrDomainRenameUnsupported
	case http.StatusUnprocessableEntity:
		var verr rawValidateErrorBody
		_ = json.Unmarshal(body, &verr)
		return nil, &DomainValidationError{
			StatusCode: resp.StatusCode,
			Message:    firstNonEmptyString(verr.Error, "domain validation failed"),
			Issues:     verr.Issues,
		}
	default:
		return nil, fmt.Errorf("apiclient: PUT /v1/me/domains/%s unexpected status %d: %s",
			slug, resp.StatusCode, strings.TrimSpace(string(body)))
	}
}
