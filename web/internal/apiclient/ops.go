package apiclient

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

// Operator console client methods. These call the operator-only
// /v1/ops/* endpoints on apps/api, forwarding the signed-in
// operator's bearer exactly as the dashboard forwards a tenant
// bearer. The apps/api side re-checks the operator allow-list and
// returns 404 to non-operators, so a non-operator bearer surfaces
// here as ErrReceiptNotFound (the do helper's 404 mapping) — the web
// handler treats that as "not available."
//
// The response shapes mirror the apps/api handlers.Ops* types. We
// redefine them here rather than import apps/api so the web binary
// stays a separate module, the same convention the Receipt mirror
// follows.

// OpsOverview mirrors apps/api handlers.OpsOverview.
type OpsOverview struct {
	Tenants   OpsTenantStats   `json:"tenants"`
	Activity  OpsActivityStats `json:"activity"`
	Backlog   OpsBacklog       `json:"backlog"`
	Attention []OpsTenantRisk  `json:"attention"`
}

// OpsBacklog mirrors apps/api handlers.OpsBacklog.
type OpsBacklog struct {
	Pending          int   `json:"pending"`
	OldestAgeSeconds int64 `json:"oldest_age_seconds"`
}

// OpsRiskSignal mirrors apps/api handlers.OpsRiskSignal.
type OpsRiskSignal struct {
	Key    string `json:"key"`
	Detail string `json:"detail"`
	Level  string `json:"level"`
}

// OpsTenantRisk mirrors apps/api handlers.OpsTenantRisk.
type OpsTenantRisk struct {
	ID      string          `json:"id"`
	Slug    string          `json:"slug"`
	Status  string          `json:"status"`
	Plan    string          `json:"plan"`
	Score   int             `json:"score"`
	Level   string          `json:"level"`
	Signals []OpsRiskSignal `json:"signals"`
}

// OpsTenantStats mirrors apps/api handlers.OpsTenantStats.
type OpsTenantStats struct {
	Total    int            `json:"total"`
	ByStatus map[string]int `json:"by_status"`
	ByPlan   map[string]int `json:"by_plan"`
	New7d    int            `json:"new_7d"`
	New30d   int            `json:"new_30d"`
}

// OpsActivityStats mirrors apps/api handlers.OpsActivityStats.
type OpsActivityStats struct {
	WindowDays     int               `json:"window_days"`
	Counters       map[string]uint64 `json:"counters"`
	ReceiptsRecent int               `json:"receipts_recent"`
}

// OpsTenantRow mirrors apps/api handlers.OpsTenantRow.
type OpsTenantRow struct {
	ID             string     `json:"id"`
	Slug           string     `json:"slug"`
	DisplayName    string     `json:"display_name,omitempty"`
	Status         string     `json:"status"`
	Plan           string     `json:"plan"`
	CreatedAt      time.Time  `json:"created_at"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
	Label          string     `json:"label,omitempty"`
	BilledBy       string     `json:"billed_by,omitempty"`
	KeyPrefix      string     `json:"key_prefix"`
	Runs           int        `json:"runs"`
	Credits        int64      `json:"credits"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
}

// OpsKey mirrors apps/api handlers.OpsKey: never the key, only its prefix.
type OpsKey struct {
	ID         string     `json:"id"`
	Prefix     string     `json:"prefix"`
	Label      string     `json:"label,omitempty"`
	Agent      string     `json:"agent,omitempty"`
	Roles      []string   `json:"roles,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// OpsAgentRuns mirrors apps/api handlers.OpsAgentRuns.
type OpsAgentRuns struct {
	Agent        string    `json:"agent"`
	RuntimeAgent string    `json:"runtime_agent"`
	Setup        bool      `json:"setup"`
	Runs         int       `json:"runs"`
	Counted      int       `json:"counted"`
	Credits      int64     `json:"credits"`
	CostMicroUSD int64     `json:"cost_micro_usd"`
	Last         time.Time `json:"last"`
}

// OpsConsumptionRow mirrors apps/api handlers.OpsConsumptionRow.
type OpsConsumptionRow struct {
	Day          time.Time `json:"day"`
	TenantID     string    `json:"tenant_id"`
	Slug         string    `json:"slug"`
	Label        string    `json:"label,omitempty"`
	Agent        string    `json:"agent"`
	RuntimeAgent string    `json:"runtime_agent"`
	Setup        bool      `json:"setup"`
	Runs         int       `json:"runs"`
	Counted      int       `json:"counted"`
	Credits      int64     `json:"credits"`
	Over         int64     `json:"over"`
	CostMicroUSD int64     `json:"cost_micro_usd"`
}

// OpsConsumption mirrors apps/api handlers.OpsConsumption.
type OpsConsumption struct {
	From  time.Time           `json:"from"`
	To    time.Time           `json:"to"`
	Rows  []OpsConsumptionRow `json:"rows"`
	Total OpsConsumptionRow   `json:"total"`
}

// OpsTenantDetail mirrors apps/api handlers.OpsTenantDetail.
type OpsTenantDetail struct {
	Tenant    OpsTenantRow           `json:"tenant"`
	Usage     []OpsUsageCounter      `json:"usage"`
	Receipts  []Receipt              `json:"receipts"`
	Proposals []OpsDecisionRow       `json:"proposals"`
	Posture   []OpsPostureTransition `json:"posture"`
	Alerts    OpsAlertHealth         `json:"alerts"`
	Keys      []OpsKey               `json:"keys,omitempty"`
	Agents    []OpsAgentRuns         `json:"agents,omitempty"`
}

// OpsUsageCounter mirrors apps/api usage.Counter.
type OpsUsageCounter struct {
	CounterName string    `json:"CounterName"`
	EntityType  string    `json:"EntityType"`
	HourBucket  time.Time `json:"HourBucket"`
	Count       uint64    `json:"Count"`
}

// OpsDecisionRow is the minimal subset of apps/api proposals.DecisionRow
// the console renders.
type OpsDecisionRow struct {
	ProposalID string    `json:"proposal_id"`
	ActionName string    `json:"action_name"`
	Outcome    string    `json:"outcome"`
	CreatedAt  time.Time `json:"created_at"`
}

// OpsPostureTransition mirrors apps/api posture.Transition.
type OpsPostureTransition struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	ChangedBy string    `json:"changed_by,omitempty"`
	ChangedAt time.Time `json:"changed_at"`
}

// OpsAlertHealth mirrors apps/api handlers.OpsAlertHealth.
type OpsAlertHealth struct {
	Delivered int `json:"delivered"`
	Failed    int `json:"failed"`
	Blocked   int `json:"blocked"`
}

// OpsActivityRow mirrors apps/api handlers.OpsActivityRow.
type OpsActivityRow struct {
	TenantID   string    `json:"tenant_id"`
	TraceID    string    `json:"trace_id"`
	ProposalID string    `json:"proposal_id,omitempty"`
	Action     string    `json:"action_name"`
	Outcome    string    `json:"outcome,omitempty"`
	IssuedAt   time.Time `json:"issued_at"`
	Anchored   bool      `json:"anchored"`
}

// OpsOverviewBearer fetches GET /v1/ops/overview.
func (c *Client) OpsOverviewBearer(ctx context.Context, bearer string) (*OpsOverview, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/ops/overview", nil)
	if err != nil {
		return nil, err
	}
	var out OpsOverview
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode ops overview: %w", err)
	}
	return &out, nil
}

// OpsTenantsBearer fetches GET /v1/ops/tenants.
func (c *Client) OpsTenantsBearer(ctx context.Context, bearer string) ([]OpsTenantRow, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/ops/tenants", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tenants []OpsTenantRow `json:"tenants"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode ops tenants: %w", err)
	}
	return out.Tenants, nil
}

// OpsTenantDetailBearer fetches GET /v1/ops/tenants/{id}.
func (c *Client) OpsTenantDetailBearer(ctx context.Context, bearer, id string) (*OpsTenantDetail, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/ops/tenants/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	var out OpsTenantDetail
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode ops tenant detail: %w", err)
	}
	return &out, nil
}

// OpsActivityBearer fetches GET /v1/ops/activity.
func (c *Client) OpsActivityBearer(ctx context.Context, bearer string, limit int) ([]OpsActivityRow, error) {
	path := "/v1/ops/activity"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	body, err := c.do(ctx, bearer, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Activity []OpsActivityRow `json:"activity"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode ops activity: %w", err)
	}
	return out.Activity, nil
}

// OpsAuditEntry mirrors apps/api operatoraudit.Entry.
type OpsAuditEntry struct {
	ID              string         `json:"id"`
	OperatorSubject string         `json:"operator_subject"`
	Action          string         `json:"action"`
	TargetTenantID  string         `json:"target_tenant_id,omitempty"`
	Reason          string         `json:"reason,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
}

// OpsSuspendBearer suspends a tenant. reason is recorded in the
// operator audit trail.
func (c *Client) OpsSuspendBearer(ctx context.Context, bearer, tenantID, reason string) error {
	return c.opsTenantAction(ctx, bearer, tenantID, "suspend", reason)
}

// OpsUnsuspendBearer reverses a suspension.
func (c *Client) OpsUnsuspendBearer(ctx context.Context, bearer, tenantID, reason string) error {
	return c.opsTenantAction(ctx, bearer, tenantID, "unsuspend", reason)
}

func (c *Client) opsTenantAction(ctx context.Context, bearer, tenantID, action, reason string) error {
	path := "/v1/ops/tenants/" + url.PathEscape(tenantID) + "/" + action
	body := strings.NewReader(`{"reason":` + jsonString(reason) + `}`)
	_, err := c.do(ctx, bearer, http.MethodPost, path, body)
	return err
}

// OpsAuditBearer fetches the operator audit log. tenantID scopes to
// one tenant when non-empty.
func (c *Client) OpsAuditBearer(ctx context.Context, bearer, tenantID string) ([]OpsAuditEntry, error) {
	path := "/v1/ops/audit"
	if tenantID != "" {
		path += "?tenant=" + url.QueryEscape(tenantID)
	}
	body, err := c.do(ctx, bearer, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Entries []OpsAuditEntry `json:"entries"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode ops audit: %w", err)
	}
	return out.Entries, nil
}

// jsonString returns s as a JSON string literal (quoted, escaped).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// OpsAlert mirrors apps/api operatoralerts.Alert.
type OpsAlert struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenant_id"`
	TenantSlug string    `json:"tenant_slug,omitempty"`
	Kind       string    `json:"kind"`
	Severity   string    `json:"severity"`
	Detail     string    `json:"detail"`
	Status     string    `json:"status"`
	Count      int       `json:"count"`
	FirstSeen  time.Time `json:"first_seen_at"`
	LastSeen   time.Time `json:"last_seen_at"`
	AckedBy    string    `json:"acked_by,omitempty"`
	Note       string    `json:"note,omitempty"`
}

// OpsAlertsBearer lists operator alerts. all includes resolved.
func (c *Client) OpsAlertsBearer(ctx context.Context, bearer string, all bool) ([]OpsAlert, error) {
	path := "/v1/ops/alerts"
	if all {
		path += "?all=1"
	}
	return c.opsDecodeAlerts(ctx, bearer, http.MethodGet, path)
}

// OpsSyncAlertsBearer recomputes risk signals into the alert queue.
func (c *Client) OpsSyncAlertsBearer(ctx context.Context, bearer string) ([]OpsAlert, error) {
	return c.opsDecodeAlerts(ctx, bearer, http.MethodPost, "/v1/ops/alerts/sync")
}

func (c *Client) opsDecodeAlerts(ctx context.Context, bearer, method, path string) ([]OpsAlert, error) {
	raw, err := c.do(ctx, bearer, method, path, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Alerts []OpsAlert `json:"alerts"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode ops alerts: %w", err)
	}
	return out.Alerts, nil
}

// OpsAlertActionBearer acks or resolves an alert (action = "ack" |
// "resolve"); note is recorded on the alert and the audit log.
func (c *Client) OpsAlertActionBearer(ctx context.Context, bearer, id, action, note string) error {
	path := "/v1/ops/alerts/" + url.PathEscape(id) + "/" + action
	_, err := c.do(ctx, bearer, http.MethodPost, path, strings.NewReader(`{"reason":`+jsonString(note)+`}`))
	return err
}

// OpsChangePlanBearer sets a tenant's plan (audited on the API side).
func (c *Client) OpsChangePlanBearer(ctx context.Context, bearer, tenantID, plan, reason string) error {
	path := "/v1/ops/tenants/" + url.PathEscape(tenantID) + "/plan"
	body := `{"plan":` + jsonString(plan) + `,"reason":` + jsonString(reason) + `}`
	_, err := c.do(ctx, bearer, http.MethodPost, path, strings.NewReader(body))
	return err
}

// OpsStoreReset is an account's billing as the API reads it back after
// Reset store billing.
type OpsStoreReset struct {
	ID                 string `json:"id"`
	Plan               string `json:"plan"`
	BillingSource      string `json:"billing_source"`
	BillingStatus      string `json:"billing_status"`
	AppleSubscription  bool   `json:"apple_subscription"`
	GoogleSubscription bool   `json:"google_subscription"`
	AppleReset         bool   `json:"apple_reset"`
	GoogleReset        bool   `json:"google_reset"`
	ResetAt            string `json:"reset_at"`
	// Trial is "not_started" (as a new sign-up), "running" or "ended";
	// NoPlanAllowanceFrom, when Coach's no-plan allowance counts from.
	Trial               string `json:"trial"`
	NoPlanAllowanceFrom string `json:"no_plan_allowance_from"`
	From                string `json:"from"`
}

// OpsResetStoreBillingBearer puts a review or test account back to no plan
// after a store purchase: confirm is the account's id typed back, reason is
// required; audited on the API side. It answers the account read back.
func (c *Client) OpsResetStoreBillingBearer(ctx context.Context, bearer, tenantID, confirm, reason string) (*OpsStoreReset, error) {
	path := "/v1/ops/tenants/" + url.PathEscape(tenantID) + "/store-billing/reset"
	body := `{"confirm":` + jsonString(confirm) + `,"reason":` + jsonString(reason) + `}`
	raw, err := c.do(ctx, bearer, http.MethodPost, path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	var out OpsStoreReset
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode store billing reset: %w", err)
	}
	return &out, nil
}

// OpsSetLabelBearer sets the operators' label on a tenant ("" for none);
// audited on the API side, display only.
func (c *Client) OpsSetLabelBearer(ctx context.Context, bearer, tenantID, label, reason string) error {
	path := "/v1/ops/tenants/" + url.PathEscape(tenantID) + "/label"
	body := `{"label":` + jsonString(label) + `,"reason":` + jsonString(reason) + `}`
	_, err := c.do(ctx, bearer, http.MethodPost, path, strings.NewReader(body))
	return err
}

// OpsConsumptionBearer reads the run ledger's last 30 days by day,
// account and agent.
func (c *Client) OpsConsumptionBearer(ctx context.Context, bearer string) (*OpsConsumption, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/ops/consumption", nil)
	if err != nil {
		return nil, err
	}
	var out OpsConsumption
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode ops consumption: %w", err)
	}
	return &out, nil
}

// OpsGrantBonusBearer gives a tenant bonus credits, a pack of credits of
// their own that never expires; audited on the API side, reason required.
func (c *Client) OpsGrantBonusBearer(ctx context.Context, bearer, tenantID string, credits int, reason string) error {
	path := "/v1/ops/tenants/" + url.PathEscape(tenantID) + "/bonus"
	body := `{"credits":` + strconv.Itoa(credits) + `,"reason":` + jsonString(reason) + `}`
	_, err := c.do(ctx, bearer, http.MethodPost, path, strings.NewReader(body))
	return err
}

// Operator support-ticket triage. These call the operator-only
// /v1/ops/support* endpoints. Operators see every tenant's tickets, so
// the shapes carry tenant_id (unlike the tenant-facing SupportTicket).

// OpsSupportTicket mirrors apps/api support.Ticket on the operator
// surface — cross-tenant, so tenant_id and author_email are included.
type OpsSupportTicket struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	AuthorSubject string    `json:"author_subject"`
	AuthorEmail   string    `json:"author_email,omitempty"`
	Title         string    `json:"title"`
	Body          string    `json:"body"`
	Category      string    `json:"category"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// OpsSupportMessage mirrors apps/api support.Message.
type OpsSupportMessage struct {
	ID            string    `json:"id"`
	TicketID      string    `json:"ticket_id"`
	AuthorKind    string    `json:"author_kind"` // "user" | "operator"
	AuthorSubject string    `json:"author_subject,omitempty"`
	Body          string    `json:"body"`
	CreatedAt     time.Time `json:"created_at"`
}

// OpsSupportDetail mirrors the operator ticket-detail response.
type OpsSupportDetail struct {
	Ticket   OpsSupportTicket    `json:"ticket"`
	Messages []OpsSupportMessage `json:"messages"`
}

// OpsSupportListBearer fetches GET /v1/ops/support[?status=], the
// cross-tenant triage queue, optionally filtered by status.
func (c *Client) OpsSupportListBearer(ctx context.Context, bearer, status string) ([]OpsSupportTicket, error) {
	path := "/v1/ops/support"
	if status != "" {
		path += "?status=" + url.QueryEscape(status)
	}
	body, err := c.do(ctx, bearer, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tickets []OpsSupportTicket `json:"tickets"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode ops support list: %w", err)
	}
	return out.Tickets, nil
}

// OpsSupportDetailBearer fetches GET /v1/ops/support/{id}: one ticket +
// its thread. A 404 surfaces as ErrReceiptNotFound via do().
func (c *Client) OpsSupportDetailBearer(ctx context.Context, bearer, id string) (*OpsSupportDetail, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/ops/support/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	var out OpsSupportDetail
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode ops support detail: %w", err)
	}
	return &out, nil
}

// OpsSupportReplyBearer posts an operator reply (POST .../{id}/reply);
// the API moves the ticket to "answered" and emails the submitter.
func (c *Client) OpsSupportReplyBearer(ctx context.Context, bearer, id, replyBody string) error {
	path := "/v1/ops/support/" + url.PathEscape(id) + "/reply"
	_, err := c.do(ctx, bearer, http.MethodPost, path, strings.NewReader(`{"body":`+jsonString(replyBody)+`}`))
	return err
}

// OpsSupportStatusBearer sets a ticket's status (POST .../{id}/status).
func (c *Client) OpsSupportStatusBearer(ctx context.Context, bearer, id, status string) error {
	path := "/v1/ops/support/" + url.PathEscape(id) + "/status"
	_, err := c.do(ctx, bearer, http.MethodPost, path, strings.NewReader(`{"status":`+jsonString(status)+`}`))
	return err
}
