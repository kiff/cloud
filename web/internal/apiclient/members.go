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

// the governance-roles design: a person can belong to several accounts.
// The dashboard names the one a request is for with the Kiff-Tenant
// header; the API checks the person's membership in it. The header is a
// selector, never an authority.

// TenantHeader is the header the API reads the selected account from.
const TenantHeader = "Kiff-Tenant"

type tenantKey struct{}

// WithTenant makes every session-aware request made with ctx name the
// account tenantID.
func WithTenant(ctx context.Context, tenantID string) context.Context {
	if strings.TrimSpace(tenantID) == "" {
		return ctx
	}
	return context.WithValue(ctx, tenantKey{}, strings.TrimSpace(tenantID))
}

// TenantFrom returns the account selected on ctx, or "".
func TenantFrom(ctx context.Context) string {
	t, _ := ctx.Value(tenantKey{}).(string)
	return t
}

// tenantTransport adds the selected account to each request, so every
// call the session-aware client makes carries it without each method
// knowing.
type tenantTransport struct{ next http.RoundTripper }

func (t tenantTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	if tenant := TenantFrom(req.Context()); tenant != "" && req.Header.Get(TenantHeader) == "" {
		req = req.Clone(req.Context())
		req.Header.Set(TenantHeader, tenant)
	}
	if lang := LanguageFrom(req.Context()); lang != "" && req.Header.Get("Accept-Language") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("Accept-Language", lang)
	}
	return next.RoundTrip(req)
}

type languageKey struct{}

// WithLanguage makes every session-aware request made with ctx ask the API
// for its words in lang (Accept-Language): a screen the apps open shows
// KIFF's own lines, as KIFF translates them.
func WithLanguage(ctx context.Context, lang string) context.Context {
	if strings.TrimSpace(lang) == "" {
		return ctx
	}
	return context.WithValue(ctx, languageKey{}, strings.TrimSpace(lang))
}

// LanguageFrom returns the language asked for on ctx, or "".
func LanguageFrom(ctx context.Context) string {
	l, _ := ctx.Value(languageKey{}).(string)
	return l
}

// APIError is a refusal from the API with its stable code (e.g.
// "last_admin", "reauth_required", "tenant_required").
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("apiclient: status %d: %s", e.Status, e.Code)
}

// ErrorCode returns the API's error code for err, or "".
func ErrorCode(err error) string {
	var e *APIError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// membersCall sends body as JSON and decodes a 2xx reply into out.
// Refusals come back as *APIError; 401 as ErrUnauthorized.
func (c *Client) membersCall(ctx context.Context, bearer, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	resp, err := c.doRaw(ctx, bearer, method, path, r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(raw))
		}
		return &APIError{Status: resp.StatusCode, Code: e.Error, Message: e.Message}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Membership is one account the signed-in person belongs to.
type Membership struct {
	TenantID string `json:"tenant_id"`
	Account  string `json:"account"`
	Role     string `json:"role"`
	Status   string `json:"status"`
}

// MembershipsBearer lists the person's accounts. It runs without a
// selected account (the API resolves none for it).
func (c *Client) MembershipsBearer(ctx context.Context, bearer string) ([]Membership, error) {
	var out struct {
		Memberships []Membership `json:"memberships"`
	}
	err := c.membersCall(context.WithValue(ctx, tenantKey{}, ""), bearer, http.MethodGet, "/v1/me/memberships", nil, &out)
	return out.Memberships, err
}

// Member is a person on the account.
type Member struct {
	Subject      string    `json:"subject"`
	Email        string    `json:"email"`
	Role         string    `json:"role"`
	Status       string    `json:"status"`
	You          bool      `json:"you"`
	InvitedBy    string    `json:"invited_by"`
	CreatedAt    time.Time `json:"created_at"`
	Capabilities []string  `json:"capabilities"`
}

// MembersBearer lists the account's people.
func (c *Client) MembersBearer(ctx context.Context, bearer string) ([]Member, error) {
	var out struct {
		Members []Member `json:"members"`
	}
	err := c.membersCall(ctx, bearer, http.MethodGet, "/v1/tenant/members", nil, &out)
	return out.Members, err
}

// UsageCount is what one person's or group's runs counted in a period.
type UsageCount struct {
	Subject string `json:"subject,omitempty"`
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	People  int    `json:"people,omitempty"`
	Tasks   int    `json:"tasks"`
	Actions int    `json:"actions"`
}

// UsagePeople is usage by person and group: a cost and fair-use figure,
// never a view of how people work.
type UsagePeople struct {
	Period struct {
		Start time.Time `json:"start"`
		End   time.Time `json:"end"`
	} `json:"period"`
	People   []UsageCount `json:"people"`
	NoPerson *UsageCount  `json:"no_person"`
	Groups   []UsageCount `json:"groups"`
	// PeopleSince is when KIFF began recording who asked, while the period
	// started before it.
	PeopleSince *time.Time `json:"people_since"`
}

// UsagePeopleBearer reads the period's usage by person and group: the
// billing admin's reads everyone's, anyone else's only their own.
func (c *Client) UsagePeopleBearer(ctx context.Context, bearer string) (UsagePeople, error) {
	var out UsagePeople
	err := c.membersCall(ctx, bearer, http.MethodGet, "/v1/me/usage/people", nil, &out)
	return out, err
}

// Invite is an invitation to the account. AcceptURL is only present on
// the reply that created it.
type Invite struct {
	ID         string    `json:"id"`
	Email      string    `json:"email"`
	Role       string    `json:"role"`
	InvitedBy  string    `json:"invited_by"`
	State      string    `json:"state"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	AcceptedBy string    `json:"accepted_by"`
	AcceptURL  string    `json:"accept_url"`
}

// InvitesBearer lists the account's invitations, newest first.
func (c *Client) InvitesBearer(ctx context.Context, bearer string) ([]Invite, error) {
	var out struct {
		Invites []Invite `json:"invites"`
	}
	err := c.membersCall(ctx, bearer, http.MethodGet, "/v1/tenant/invites", nil, &out)
	return out.Invites, err
}

// CreateInviteBearer invites email as role; lifetimeHours 0 is the
// role's default.
func (c *Client) CreateInviteBearer(ctx context.Context, bearer, email, role string, lifetimeHours int) (Invite, error) {
	body := map[string]any{"email": email, "role": role}
	if lifetimeHours > 0 {
		body["lifetime_hours"] = lifetimeHours
	}
	var out Invite
	err := c.membersCall(ctx, bearer, http.MethodPost, "/v1/tenant/invites", body, &out)
	return out, err
}

// RevokeInviteBearer withdraws a pending invitation.
func (c *Client) RevokeInviteBearer(ctx context.Context, bearer, inviteID string) error {
	return c.membersCall(ctx, bearer, http.MethodPost, "/v1/tenant/invites/"+url.PathEscape(inviteID)+"/revoke", nil, nil)
}

// ChangeRoleBearer sets a member's role.
func (c *Client) ChangeRoleBearer(ctx context.Context, bearer, subject, role string) error {
	return c.membersCall(ctx, bearer, http.MethodPut, "/v1/tenant/members/"+url.PathEscape(subject)+"/role",
		map[string]string{"role": role}, nil)
}

// MemberActionBearer suspends, reactivates or removes a member.
func (c *Client) MemberActionBearer(ctx context.Context, bearer, subject, action string) error {
	switch action {
	case "suspend", "reactivate", "remove":
	default:
		return fmt.Errorf("apiclient: unknown member action %q", action)
	}
	return c.membersCall(ctx, bearer, http.MethodPost, "/v1/tenant/members/"+url.PathEscape(subject)+"/"+action, nil, nil)
}

// LeaveBearer removes the signed-in person from the account.
func (c *Client) LeaveBearer(ctx context.Context, bearer string) error {
	return c.membersCall(ctx, bearer, http.MethodPost, "/v1/tenant/leave", nil, nil)
}

// Governance is the account's separation-of-duties policy.
type Governance struct {
	SeparationOfDuties bool      `json:"separation_of_duties"`
	UpdatedBy          string    `json:"updated_by"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// GovernanceBearer reads the policy.
func (c *Client) GovernanceBearer(ctx context.Context, bearer string) (Governance, error) {
	var out Governance
	err := c.membersCall(ctx, bearer, http.MethodGet, "/v1/tenant/governance", nil, &out)
	return out, err
}

// SetSeparationOfDutiesBearer turns the policy on or off.
func (c *Client) SetSeparationOfDutiesBearer(ctx context.Context, bearer string, on bool) error {
	return c.membersCall(ctx, bearer, http.MethodPut, "/v1/tenant/governance", map[string]bool{"separation_of_duties": on}, nil)
}

// MembershipEvent is one row of the account's people trail.
type MembershipEvent struct {
	Actor    string    `json:"actor"`
	Action   string    `json:"action"`
	Target   string    `json:"target"`
	OldValue string    `json:"old_value"`
	NewValue string    `json:"new_value"`
	At       time.Time `json:"at"`
}

// MembershipAuditBearer reads the trail (admins).
func (c *Client) MembershipAuditBearer(ctx context.Context, bearer string) ([]MembershipEvent, error) {
	var out struct {
		Events []MembershipEvent `json:"events"`
	}
	err := c.membersCall(ctx, bearer, http.MethodGet, "/v1/tenant/audit", nil, &out)
	return out.Events, err
}

// InvitePreview is what an invitation offers, shown before accepting.
type InvitePreview struct {
	TenantID  string    `json:"tenant_id"`
	Account   string    `json:"account"`
	Role      string    `json:"role"`
	InvitedBy string    `json:"invited_by"`
	ExpiresAt time.Time `json:"expires_at"`
}

// PreviewInviteBearer reads an invitation by its token.
func (c *Client) PreviewInviteBearer(ctx context.Context, bearer, token string) (InvitePreview, error) {
	var out InvitePreview
	err := c.membersCall(context.WithValue(ctx, tenantKey{}, ""), bearer, http.MethodPost, "/v1/invites/preview", map[string]string{"token": token}, &out)
	return out, err
}

// AcceptInviteBearer accepts an invitation for the signed-in person and
// returns the account they joined.
func (c *Client) AcceptInviteBearer(ctx context.Context, bearer, token string) (Membership, error) {
	var out Membership
	err := c.membersCall(context.WithValue(ctx, tenantKey{}, ""), bearer, http.MethodPost, "/v1/invites/accept", map[string]string{"token": token}, &out)
	return out, err
}
