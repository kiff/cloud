package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/tenant"
)

// MeHandler exposes GET /v1/me, returning the authenticated
// subject's tenant and identity attribution. The dashboard's
// "who am I" probe consumes this on every page load to render
// the user's tenant slug + id, replacing the operator-key
// shortcut that previously hard-coded one demo tenant.
//
// The response carries only data the caller could derive from
// their own token + a tenant lookup; nothing leaks across
// tenants. Returns 401 when no identity is on the context, or
// when identity has no tenant (defence in depth — the auth
// middleware already enforces this).
type MeHandler struct {
	tenants    tenant.Store
	emails     EmailLookup
	logger     *slog.Logger
	governance GovernanceReader
}

// GovernanceReader reads an account's governance policy (membership.Store
// is one).
type GovernanceReader interface {
	Governance(ctx context.Context, tenantID string) (membership.Governance, error)
}

// WithMeGovernance makes /v1/me's capabilities follow the account's
// separation of duties: holds.answer is listed only for a person who may
// answer under it (membership.AnswersHolds), as the hold gate decides, so a
// phone offers Approve only to whoever KIFF lets answer.
func WithMeGovernance(g GovernanceReader) MeOption {
	return func(h *MeHandler) { h.governance = g }
}

// MeOption configures a MeHandler.
type MeOption func(*MeHandler)

// WithMeEmails resolves the email of a phone's developer session, which
// carries none. The email is read from Clerk when asked, never
// stored (the operator-console design, owneremail). A failed lookup leaves it out; /v1/me still
// answers.
func WithMeEmails(e EmailLookup, logger *slog.Logger) MeOption {
	return func(h *MeHandler) { h.emails, h.logger = e, logger }
}

// NewMeHandler wraps the cloud's tenant store.
func NewMeHandler(tenants tenant.Store, opts ...MeOption) *MeHandler {
	h := &MeHandler{tenants: tenants}
	for _, o := range opts {
		o(h)
	}
	return h
}

// email is the caller's email: as their sign-in carries it, else, for a
// phone's developer session, as Clerk has it for their subject. Nothing
// else is looked up: the web dashboard calls /v1/me with a browser session
// on nearly every page and has the email from its own Clerk sign-in, and a
// key's caller is an agent, which has no need of it.
func (h *MeHandler) email(r *http.Request, id auth.Identity) string {
	if id.Email != "" || h.emails == nil || id.APIKey || id.Subject == "" ||
		id.Method != auth.MethodDeveloper {
		return id.Email
	}
	e, err := h.emails.EmailForSubject(r.Context(), id.Subject)
	if err != nil {
		if h.logger != nil {
			h.logger.WarnContext(r.Context(), "me: email lookup failed", slog.String("err", err.Error()))
		}
		return ""
	}
	return e
}

// Register attaches the route to mux. Idempotent if mux has not
// seen the pattern before.
func (h *MeHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me", h.get)
	mux.HandleFunc("PUT /v1/me/account-kind", h.setAccountKind)
	mux.HandleFunc("PUT /v1/me/time-zone", h.setTimeZone)
	mux.HandleFunc("PUT /v1/me/account-name", h.setAccountName)
	mux.HandleFunc("PUT /v1/me/chat-approvals", h.setChatApprovals)
}

// setChatApprovals records whether a held call is also offered in the
// agent's chat. Like the account kind, only the
// owner or an admin may set it, never an agent's key: an agent must not
// decide how its own holds reach the owner.
func (h *MeHandler) setChatApprovals(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a session or key is required"})
		return
	}
	if !can(id, membership.CapAccountSettings) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the account's owner or an admin can change this"})
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `the body must be {"enabled": true} or {"enabled": false}`})
		return
	}
	v := "off"
	if *body.Enabled {
		v = "on"
	}
	if err := h.tenants.SetMetadata(r.Context(), id.Tenant, tenant.MetadataKeyChatApprovals, v); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the choice could not be saved"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"chat_approvals": *body.Enabled})
}

// setAccountKind records whether the account is personal or a company's.
// Only the account's owner or an admin may set it, never
// an agent's key: it changes what the account's people see, not what
// any agent may do.
func (h *MeHandler) setAccountKind(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a session or key is required"})
		return
	}
	if !can(id, membership.CapAccountSettings) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the account's owner or an admin can choose what the account is for"})
		return
	}
	var body struct {
		Kind string `json:"kind"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `the body must be {"kind": "personal"} or {"kind": "company"}`})
		return
	}
	kind, ok := tenant.ParseAccountKind(body.Kind)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `kind must be "personal" or "company"`})
		return
	}
	if err := h.tenants.SetMetadata(r.Context(), id.Tenant, tenant.MetadataKeyAccountKind, string(kind)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the choice could not be saved"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"account_kind": string(kind)})
}

// MeResponse is the body of GET /v1/me. The shape is intentionally
// minimal; clients (the dashboard, future SDKs) should treat
// unknown fields as forward-compatible additions.
type MeResponse struct {
	TimeZone    string   `json:"time_zone"`
	Subject     string   `json:"subject"`
	Email       string   `json:"email,omitempty"`
	TenantID    string   `json:"tenant_id"`
	TenantSlug  string   `json:"tenant_slug"`
	DisplayName string   `json:"tenant_display_name,omitempty"`
	APIKey      bool     `json:"api_key"`
	Roles       []string `json:"roles,omitempty"`
	// AgentID is the agent the authenticating key is bound to, so a
	// caller holding only that key (the KIFF gateway, the cards-for-any-agent design) can name
	// the agent in the proposals it sends. Empty for unbound keys and
	// sessions.
	AgentID string `json:"agent_id,omitempty"`
	// CredentialID is the id of the key the caller authenticated with,
	// including the key behind an OAuth access token. The
	// gateway keys retries on it, because access tokens rotate hourly.
	// Empty for sessions.
	CredentialID string `json:"credential_id,omitempty"`
	// Resource is the gateway URL an OAuth access token was issued for;
	// the gateway refuses the token at any other
	// URL. Empty for keys and sessions.
	Resource string `json:"resource,omitempty"`

	// MemberRole is the person's role on this account; empty
	// for keys. Capabilities is what the caller may do here, for keys
	// too, so the dashboard and the gateway ask one question instead of
	// reading role names.
	MemberRole   string   `json:"member_role,omitempty"`
	Capabilities []string `json:"capabilities"`
	// DeviceSession is true for a phone's session bound to its key.
	DeviceSession bool `json:"device_session,omitempty"`
	// ApprovalMode is how a device's session approves
	// (auth.ShownApprovalMode): view, key, passkey or device_key. Empty for
	// other callers.
	ApprovalMode string `json:"approval_mode,omitempty"`
	// SeparationOfDuties is the account's policy, for a person: with it
	// on, only approvers answer held calls (Capabilities says already
	// whether this person may). Absent for keys, or when it can't be read.
	SeparationOfDuties *bool `json:"separation_of_duties,omitempty"`

	// Plan and MonthlyProposalLimit surface the tenant's effective
	// plan state so the dashboard can render "you are on the Free
	// / Dev plan" + an upgrade CTA without a second round-trip.
	// Resolved via tenant.PlanFor; defaults to free_dev when the
	// metadata key is absent. MonthlyProposalLimit is zero-or-less
	// for unlimited tiers (no such tier in v0.1; reserved for the
	// founder-led pilot path documented in the offer doc).
	//
	// surfaces the same fields on /v1/me/usage so a dashboard
	// can render plan + counters in a single call. Carrying them
	// here too keeps /v1/me self-contained for the "who am I"
	// probe; the two endpoints stay consistent because both read
	// from tenant.PlanFor.
	Plan                 string `json:"plan,omitempty"`
	MonthlyProposalLimit int    `json:"monthly_proposal_limit,omitempty"`

	// MaxDomains is the tenant's plan cap on domain count (the multi-domain design
	// D4); zero-or-less is unlimited. The dashboard renders
	// "N of M domains" and gates the "New domain" affordance from it.
	MaxDomains int `json:"max_domains,omitempty"`

	// CanonicalPlan is the consumption model canonical plan
	// (developer/growth/scale/enterprise) the tenant resolves to,
	// independent of whether Plan carries a legacy alias. The web
	// reads this directly for plan display instead of re-deriving
	// the canonical plan from the legacy alias table.
	CanonicalPlan string `json:"canonical_plan,omitempty"`

	// BillingStatus is the Stripe-synced subscription state written
	// by the billing webhook: "active", "past_due"
	// (grace — the runtime keeps serving), or "canceled".
	// Empty when the tenant has never carried a subscription. The
	// dashboard renders a non-alarming past_due banner from it.
	BillingStatus string `json:"billing_status,omitempty"`

	// HasBilling reports whether the tenant has a linked Stripe
	// customer (a subscription was activated at some point). The
	// dashboard gates the "Manage billing" (Customer Portal) action
	// on this — a tenant with no Stripe customer has no portal.
	HasBilling bool `json:"has_billing,omitempty"`

	// AnchorChain is the public chain this tenant's receipts anchor on:
	// "sepolia" on every plan today ("mainnet" is supported in code but
	// not offered). The signed receipt is the core beneath either; this
	// field never means "no anchoring."
	AnchorChain string `json:"anchor_chain,omitempty"`
	// AccountKind is "personal" or "company", or empty
	// while the account's owner has not chosen yet.
	AccountKind string `json:"account_kind,omitempty"`
	// ChatApprovals is whether a held call is also offered in the agent's
	// chat: the owner's choice, else on for a personal account.
	ChatApprovals bool `json:"chat_approvals"`
}

func (h *MeHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	t, err := h.tenants.Get(r.Context(), id.Tenant)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			// The tenant was provisioned then deleted. The
			// dashboard treats this as "session is no longer
			// usable; please sign in again."
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "tenant not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	limits := tenant.PlanFor(t)
	email := h.email(r, id)
	caps, sod := h.capabilities(r.Context(), id, t.ID)
	writeJSON(w, http.StatusOK, MeResponse{
		Subject:     id.Subject,
		Email:       email,
		TenantID:    t.ID,
		TenantSlug:  t.Slug,
		DisplayName: h.accountName(r.Context(), t, id.Subject, email),
		APIKey:      id.APIKey,
		// Roles are surfaced so the dashboard's onboarding mint
		// can request a key that matches the caller's authority.
		// The cloud's keys handler refuses any role escalation
		// (rolesAreSubsetOf), so exposing the read here is safe:
		// the caller already knows what they hold.
		Roles:        append([]string(nil), id.Roles...),
		AgentID:      id.AgentID,
		CredentialID: id.Metadata["api_key_id"],
		Resource:     id.Metadata["oauth_resource"],
		MemberRole:   string(id.MemberRole),
		Capabilities: caps,
		// A phone's session: other KIFF services, such as the
		// gateway, let it read but not change the account.
		DeviceSession:      len(id.DeviceKey) > 0,
		ApprovalMode:       id.ShownApprovalMode(),
		SeparationOfDuties: sod,
		// Plan + limit so the dashboard can render plan state
		// without a second round-trip. See struct doc.
		Plan:                 string(limits.Plan),
		MonthlyProposalLimit: limits.MonthlyProposalLimit,
		MaxDomains:           limits.MaxDomains,
		CanonicalPlan:        string(limits.Canonical),
		BillingStatus:        metaString(t, billing.MetaBillingStatus),
		HasBilling:           metaString(t, billing.MetaStripeCustomerID) != "",
		AnchorChain:          string(limits.AnchorChain),
		AccountKind:          string(tenant.AccountKindFor(t)),
		ChatApprovals:        tenant.ChatApprovalsFor(t),
		TimeZone:             tenant.TimeZoneFor(t),
	})
}

// accountName is the account's name for people. When the account
// is named by the caller's own email, the email already read is used: the
// dashboard asks /v1/me on nearly every page, and its owner shouldn't cost a
// Clerk lookup each time.
func (h *MeHandler) accountName(ctx context.Context, t tenant.Tenant, subject, email string) string {
	if name, first := tenant.NameFor(t); name == "" && first != "" && first == subject && email != "" {
		return email
	}
	return accountLabel(ctx, t, h.emails, subject)
}

// metaString reads a string value from a tenant's metadata bag,
// returning "" when the key is absent or not a string. The billing
// webhook writes billing_status / stripe_customer_id as
// strings via tenant.SetMetadata.
func metaString(t tenant.Tenant, key string) string {
	if t.Metadata == nil {
		return ""
	}
	if v, ok := t.Metadata[key].(string); ok {
		return v
	}
	return ""
}

// capabilities is what the caller may do here. For a person, holds.answer
// follows the account's separation of duties, as the hold gate does; sod is
// that policy, nil for a key or when it can't be read (the role alone then
// decides, and the hold gate still checks each answer).
func (h *MeHandler) capabilities(ctx context.Context, id auth.Identity, tenantID string) ([]string, *bool) {
	caps := capabilitiesOf(id)
	if h.governance == nil || id.APIKey || id.MemberRole == "" {
		return caps, nil
	}
	gov, err := h.governance.Governance(ctx, tenantID)
	if err != nil {
		// Safe, as the hold gate still checks each answer, but the app may then
		// offer Approve that the gate refuses: worth seeing.
		if h.logger != nil {
			h.logger.WarnContext(ctx, "me: governance unreadable; holds.answer follows the role alone",
				slog.String("tenant", tenantID), slog.String("err", err.Error()))
		}
		return caps, nil
	}
	sod := gov.SeparationOfDuties
	if membership.AnswersHolds(id.MemberRole, sod) {
		return caps, &sod
	}
	out := caps[:0]
	for _, c := range caps {
		if c != string(membership.CapHoldsAnswer) {
			out = append(out, c)
		}
	}
	return out, &sod
}

// capHoldsAllow says KIFF offers "Allow this from now on" on a held call
// (POST /v1/me/card-exceptions/{id}/allow-draft and the replay), to whoever
// manages the account's Cards. The apps show the button only with it, so it
// goes out in the same change that makes the routes live.
const capHoldsAllow = "holds.allow"

// capabilitiesOf lists what id may do on its account (auth.Identity.Can).
func capabilitiesOf(id auth.Identity) []string {
	all := []membership.Capability{membership.CapRead, membership.CapPersonal, membership.CapCardsManage,
		membership.CapDomainsManage, membership.CapToolsConnect, membership.CapActionsPropose,
		membership.CapHoldsAnswer, membership.CapKeysManage, membership.CapBilling, membership.CapMembersManage,
		membership.CapGovernance, membership.CapAccountSettings, membership.CapExport, membership.CapDelete}
	out := []string{}
	for _, c := range all {
		if id.Can(c) {
			out = append(out, string(c))
		}
	}
	if id.Can(membership.CapCardsManage) {
		out = append(out, capHoldsAllow)
	}
	return out
}
