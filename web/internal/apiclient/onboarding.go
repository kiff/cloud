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
	"time"
)

// onboarding.go carries the apiclient methods that's
// onboarding handler needs and that the existing dashboard
// methods didn't already cover:
//
//   - GetDomainBearer       — does the tenant have a domain yet
//   - DecideProposalBearer  — call /v1/proposals/decide and
//                              surface the JSON body even on 4xx
//                              (because the body IS the answer
//                              for blocked/approval_required/
//                              invalid)
//   - GetUsageBearer        — read /v1/me/usage to show the
//                              tenant their first counter tick
//   - GetEntityTimelineBearer — pull the per-entity audit trail
//                                so the user can see their
//                                proposal_decision row land
//
// Everything below mirrors the *Bearer suffix convention and
// reuses the shared do() helper for plain 2xx-or-error calls;
// DecideProposalBearer goes around it because the gate needs
// non-2xx bodies preserved.

// Domain is the per-tenant domain summary returned by
// GET /v1/me/domain. The shape mirrors the apps/api
// validateResponse so the dashboard can render the active
// domain's slug, entity, action shape, and version without
// holding the YAML.
type Domain struct {
	Domain   string         `json:"domain"`
	Entity   string         `json:"entity"`
	Version  int            `json:"version,omitempty"`
	Events   []string       `json:"events"`
	States   []string       `json:"states"`
	Actions  []DomainAction `json:"actions"`
	Warnings []string       `json:"warnings,omitempty"`
}

// DomainAction is the per-action summary embedded in Domain.
// Only the fields the onboarding flow needs to compose a first
// proposal are mirrored here.
type DomainAction struct {
	Name               string   `json:"name"`
	AllowedStates      []string `json:"allowed_states,omitempty"`
	RequiredParameters []string `json:"required_parameters,omitempty"`
	// Parameters carries the declared types, absent on a domain that
	// declares none. A surface offering a quantity to count needs them:
	// only a numeric parameter can be summed by a mandate.
	Parameters []DomainParameter `json:"parameters,omitempty"`
	Risk       string            `json:"risk,omitempty"`
	Approval   string            `json:"approval,omitempty"`
	Executor   string            `json:"executor,omitempty"`
	// RequiresAuthority: an agent holding no card for this action is
	// refused rather than unaffected.
	RequiresAuthority bool `json:"requires_authority,omitempty"`
}

// DomainParameter is one typed parameter contract on an action.
type DomainParameter struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

// Summable reports whether a mandate could sum this parameter.
//
// It mirrors the api's isNumeric and is deliberately conservative in
// the same direction: an unrecognised type is not offered. The api
// remains the authority — this only decides what a form suggests, and
// suggesting a parameter that cannot be summed wastes a round trip,
// while omitting one costs a person nothing but typing.
func (p DomainParameter) Summable() bool {
	switch strings.ToLower(strings.TrimSpace(p.Type)) {
	case "integer", "int", "number":
		return true
	default:
		return false
	}
}

// ErrNoDomain signals that the tenant has not uploaded or
// applied a domain yet. Surfaced as 404 on /v1/me/domain. The
// onboarding handler treats this as "show the choose-a-template
// step", not as a hard error.
var ErrNoDomain = errors.New("apiclient: tenant has no active domain")

// DomainValidationIssue mirrors the apps/api validateIssueResponse
// shape: a single problem the YAML parser found, with an optional
// dotted path so the editor can highlight which action / state /
// transition tripped the rule.
type DomainValidationIssue struct {
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// DomainValidationError is returned by ValidateDomainBearer and
// PutDomainBearer when the YAML fails validation (HTTP 422). The
// error carries every issue the cloud reported so the textarea
// can render them next to the input. Use errors.As to extract
// it from a returned error.
type DomainValidationError struct {
	StatusCode int
	Message    string
	Issues     []DomainValidationIssue
}

func (e *DomainValidationError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return "domain validation failed"
	}
	return e.Message
}

// GetDomainBearer fetches the active domain summary for the
// caller's tenant. Returns ErrNoDomain when the tenant has not
// yet applied a starter or uploaded a YAML.
func (c *Client) GetDomainBearer(ctx context.Context, bearer string) (*Domain, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/me/domain", nil)
	if err != nil {
		// The shared do() collapses any 404 to ErrReceiptNotFound;
		// for the onboarding surface that read is "no domain yet".
		if errors.Is(err, ErrReceiptNotFound) {
			return nil, ErrNoDomain
		}
		return nil, err
	}
	var d Domain
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("apiclient: decode domain: %w", err)
	}
	return &d, nil
}

// Decision is the apps/web view of /v1/proposals/decide's
// response shape. Mirrors the apps/api proposalResponse so the
// onboarding page can render the live JSON the user receives.
type Decision struct {
	ProposalID string   `json:"proposal_id"`
	Outcome    string   `json:"outcome"`
	Reasons    []string `json:"reasons,omitempty"`
	Message    string   `json:"message,omitempty"`
}

// ProposalRequest is the body apps/web sends to
// /v1/proposals/decide. Field names match the apps/api
// proposalRequest exactly so the JSON can be forwarded without
// translation.
type ProposalRequest struct {
	ID               string         `json:"id,omitempty"`
	EntityID         string         `json:"entity_id"`
	EntityType       string         `json:"entity_type"`
	ActionName       string         `json:"action_name"`
	ActorID          string         `json:"actor_id"`
	Parameters       map[string]any `json:"parameters,omitempty"`
	ReasoningSummary string         `json:"reasoning_summary,omitempty"`
	Confidence       float64        `json:"confidence,omitempty"`
	CreatedAt        time.Time      `json:"created_at,omitempty"`
}

// DecideProposalBearer calls POST /v1/proposals/decide and
// returns the decoded decision plus the HTTP status code,
// regardless of the response status. The gate intentionally
// returns the same JSON shape on 200 (allowed/approval_required/
// blocked), 400 (invalid), 429 (limit_exceeded once ships), and
// 502 (state_store_unavailable / tenant_runtime_unavailable /
// actions_unavailable), so callers branch on Decision.Outcome
// rather than HTTP status.
//
// The status is surfaced so the onboarding handler (and future
// dashboard surfaces) can log status alongside the outcome on
// non-2xx — without it, a tenant who hits their plan limit
// would silently flash "limit_exceeded" with no operator-visible
// signal.
//
// The only error this method returns is a transport-level
// failure (network, JSON decode, missing bearer); anything the
// gate decided to answer comes back as a Decision + status.
func (c *Client) DecideProposalBearer(ctx context.Context, bearer string, prop ProposalRequest) (*Decision, int, error) {
	if c == nil {
		return nil, 0, errors.New("apiclient: nil client")
	}
	if strings.TrimSpace(bearer) == "" {
		return nil, 0, errors.New("apiclient: empty bearer")
	}
	body, err := json.Marshal(prop)
	if err != nil {
		return nil, 0, fmt.Errorf("apiclient: encode proposal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/proposals/decide", bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("apiclient: build proposal request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("apiclient: post proposal: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, resp.StatusCode, ErrUnauthorized
	}
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return nil, resp.StatusCode, fmt.Errorf("apiclient: read proposal response: %w", readErr)
	}
	var d Decision
	if err := json.Unmarshal(respBody, &d); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("apiclient: decode proposal: %w (status %d, body %q)",
			err, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return &d, resp.StatusCode, nil
}

// Usage mirrors the apps/api usageSummaryResponse shape. The
// onboarding page reads this once: if the tenant has any
// `kiff.cloud.proposals.submitted` ticks, the activation has
// happened.
type Usage struct {
	TenantID             string            `json:"tenant_id"`
	Plan                 string            `json:"plan"`
	MonthlyProposalLimit int               `json:"monthly_proposal_limit"`
	From                 time.Time         `json:"from"`
	To                   time.Time         `json:"to"`
	Counters             map[string]uint64 `json:"counters"`
}

// GetUsageBearer fetches GET /v1/me/usage for the current
// period (current calendar month UTC by default). Used by the
// onboarding page as the "did the user cross the line yet"
// signal.
func (c *Client) GetUsageBearer(ctx context.Context, bearer string) (*Usage, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/me/usage/counters", nil)
	if err != nil {
		return nil, err
	}
	var u Usage
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("apiclient: decode usage: %w", err)
	}
	return &u, nil
}

// TimelineEntry is the apps/web view of one entry in a per-
// entity timeline. The apps/api framework wraps the underlying
// audit.Record; we surface only the fields the onboarding page
// needs to confirm "your decision was recorded" (kind, message,
// trace_id, occurred_at) plus the per-kind Data map so callers
// can read structured fields like a state_changed record's
// "to" field for the new state.
type TimelineEntry struct {
	Kind       string         `json:"kind"`
	OccurredAt time.Time      `json:"occurred_at"`
	TraceID    string         `json:"trace_id,omitempty"`
	Message    string         `json:"message,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

// GetEntityTimelineBearer fetches the audit timeline for an
// entity. The framework's HTTP surface mounts this at
// /v1/entities/{entity_id}/timeline; the response wraps the
// records under "timeline".
func (c *Client) GetEntityTimelineBearer(ctx context.Context, bearer, entityID string) ([]TimelineEntry, error) {
	if strings.TrimSpace(entityID) == "" {
		return nil, errors.New("apiclient: empty entity id")
	}
	path := "/v1/entities/" + url.PathEscape(entityID) + "/timeline"
	body, err := c.do(ctx, bearer, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Timeline []TimelineEntry `json:"timeline"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apiclient: decode timeline: %w", err)
	}
	return resp.Timeline, nil
}

// validateDomainResponse mirrors apps/api validateResponse. We
// reuse Domain for the success shape, so this is only used to
// confirm a 2xx parses.
type validateDomainResponse = Domain

// rawValidateErrorBody is the apps/api validateErrorResponse
// shape. Decoded so we can surface issues to the editor.
type rawValidateErrorBody struct {
	Error  string                  `json:"error"`
	Issues []DomainValidationIssue `json:"issues,omitempty"`
}

// ValidateDomainBearer dry-runs YAML against the cloud's
// registry. Returns the parsed Domain shape on 200; returns a
// *DomainValidationError on 422 carrying every issue the cloud
// reported.
//
// Goes around the shared do() helper because the body is raw
// YAML, not JSON, and because the 422 body carries information
// the textarea needs to render.
func (c *Client) ValidateDomainBearer(ctx context.Context, bearer string, yaml []byte) (*Domain, error) {
	return c.sendDomainYAML(ctx, bearer, http.MethodPost, "/v1/me/domain/validate", yaml)
}

// PutDomainBearer validates + uploads YAML and activates it for
// the tenant. Returns the parsed Domain on 200; returns a
// *DomainValidationError on 422 with the issues the cloud
// surfaced. Storage is left untouched on validation failure (the
// apps/api handler enforces that).
func (c *Client) PutDomainBearer(ctx context.Context, bearer string, yaml []byte) (*Domain, error) {
	return c.sendDomainYAML(ctx, bearer, http.MethodPut, "/v1/me/domain", yaml)
}

func (c *Client) sendDomainYAML(ctx context.Context, bearer, method, path string, yaml []byte) (*Domain, error) {
	if c == nil {
		return nil, errors.New("apiclient: nil client")
	}
	if strings.TrimSpace(bearer) == "" {
		return nil, errors.New("apiclient: empty bearer")
	}
	if len(yaml) == 0 {
		return nil, errors.New("apiclient: empty yaml")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(yaml))
	if err != nil {
		return nil, fmt.Errorf("apiclient: build %s request: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/x-yaml")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		var d Domain
		if err := json.Unmarshal(body, &d); err != nil {
			return nil, fmt.Errorf("apiclient: decode domain response: %w", err)
		}
		return &d, nil
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case http.StatusUnprocessableEntity:
		var verr rawValidateErrorBody
		_ = json.Unmarshal(body, &verr) // best-effort; struct stays zero on bad JSON
		return nil, &DomainValidationError{
			StatusCode: resp.StatusCode,
			Message:    firstNonEmptyString(verr.Error, "domain validation failed"),
			Issues:     verr.Issues,
		}
	case http.StatusRequestEntityTooLarge:
		return nil, fmt.Errorf("apiclient: %s %s: yaml exceeds the cloud's per-request limit", method, path)
	default:
		return nil, fmt.Errorf("apiclient: %s %s unexpected status %d: %s",
			method, path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

func firstNonEmptyString(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// MeterUsage is the account's two meters for a billing period, as
// GET /v1/me/usage gives them: counts only.
type MeterUsage struct {
	Plan   string `json:"plan"`
	Seats  int    `json:"seats"`
	Period struct {
		Start time.Time `json:"start"`
		End   time.Time `json:"end"`
	} `json:"period"`
	// Tasks are the period's tafs tasks: what the plan includes, the pack
	// tasks for the period, used and left (nil: no limit).
	Tasks struct {
		Counted        int            `json:"counted"`
		Included       int            `json:"included"`
		Packs          int            `json:"packs"`
		Used           int            `json:"used"`
		Left           *int           `json:"left"`
		Ceiling        int            `json:"ceiling"`
		CeilingReached bool           `json:"ceiling_reached"`
		Free           map[string]int `json:"free"`
	} `json:"tasks"`
	Actions struct {
		Counted  int            `json:"counted"`
		Included int            `json:"included"`
		Free     map[string]int `json:"free"`
	} `json:"actions"`
	Trial *struct {
		EndsAt   *time.Time `json:"ends_at"`
		DaysLeft int        `json:"days_left"`
	} `json:"trial"`
	// Billing is who bills the plan ("stripe", "apple", "contract" or ""),
	// and where it's managed.
	Billing struct {
		Source    string  `json:"source"`
		ManageURL *string `json:"manage_url"`
		// AppleSoloUntil: Team is billed here now, and the App Store Solo it
		// replaced still renews with Apple on this date unless cancelled.
		AppleSoloUntil *time.Time `json:"apple_solo_until,omitempty"`
	} `json:"billing"`
	// PackList is the account's packs with tasks left, oldest first.
	PackList []UsagePack `json:"pack_list"`
	// CanAddTasks: the caller may buy tasks (Solo, or packs where
	// CanAddPacks).
	CanAddTasks bool `json:"can_add_tasks"`
	// CanAddPacks: packs are offered. Not on a trial not started or
	// ended, where packs wait for a plan.
	CanAddPacks bool `json:"can_add_packs"`
	// Store is the packs on sale: names, tasks and how long they last.
	Store *struct {
		Packs []StorePack `json:"packs"`
	} `json:"store"`
	// Requests are members' requests for packs: every open one for
	// someone who may add tasks, the caller's own otherwise.
	Requests []PackRequest `json:"requests"`
}

// UsagePack is one of the account's packs.
type UsagePack struct {
	Name      string    `json:"name"`
	Tasks     int       `json:"tasks"`
	TasksLeft int       `json:"tasks_left"`
	Kind      string    `json:"kind"`
	ExpiresAt time.Time `json:"expires_at"`
}

// StorePack is a pack on sale.
type StorePack struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Line   string `json:"line"`
	Tasks  int    `json:"tasks"`
	Months int    `json:"months"`
	// StripePrice is its Stripe price; empty for a pack sold in the app
	// stores only (every pack of credits).
	StripePrice string `json:"stripe_price"`
}

// PackRequest is a member's request for a pack.
type PackRequest struct {
	ID        string    `json:"id"`
	AskedBy   string    `json:"asked_by"`
	Pack      string    `json:"pack"`
	Reason    string    `json:"reason"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Note      string    `json:"note"`
}

// GetMeterUsageBearer fetches GET /v1/me/usage: the account's tasks and
// actions this period.
func (c *Client) GetMeterUsageBearer(ctx context.Context, bearer string) (*MeterUsage, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/me/usage", nil)
	if err != nil {
		return nil, err
	}
	var u MeterUsage
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("apiclient: decode usage: %w", err)
	}
	return &u, nil
}
