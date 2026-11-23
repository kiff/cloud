package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	kiffaction "github.com/kiff/kiff/pkg/kiff/action"
	kiffactor "github.com/kiff/kiff/pkg/kiff/actor"
	kiffapproval "github.com/kiff/kiff/pkg/kiff/approval"
	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
	kiffdecision "github.com/kiff/kiff/pkg/kiff/decision"
	kiffoutcome "github.com/kiff/kiff/pkg/kiff/outcome"
	kiffproposal "github.com/kiff/kiff/pkg/kiff/proposal"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/agentdrafts"
	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/callabout"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/halts"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/notify"
	"github.com/kiff/cloud/api/internal/permit"
	"github.com/kiff/cloud/api/internal/profile"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/runcontext"
	"github.com/kiff/cloud/api/internal/stream"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// ProposalsHandler exposes the cloud's pre-execution decision
// boundary:
//
//	POST /v1/proposals/decide   — receive a proposed action,
//	                              evaluate it against the
//	                              tenant's domain + state +
//	                              policy, return a stable
//	                              machine-readable outcome.
//
// This is the integration point the entire pivot is about. An
// AI system (agent, LLM tool-call layer, automation script) asks
// KIFF "I'm about to do X to entity Y for actor Z, is that
// allowed?" before it touches a real downstream system. KIFF
// answers with one of four outcomes:
//
//   - "allowed"            — go ahead; the contract was satisfied
//     and no approval is required.
//   - "approval_required"  — the contract requires a human in
//     the loop; the customer must drive
//     the approval flow before executing.
//   - "blocked"            — the contract was violated (state
//     not allowed, missing parameter,
//     permission denied). Do not execute.
//   - "invalid"            — the proposal itself is malformed
//     (empty action name, unknown action
//     for the tenant's domain, no entity).
//     Fix and retry.
//
// What this endpoint does NOT do:
//
//   - it does not execute the action. Execution remains
//     customer-owned. KIFF returns a decision; the customer
//     decides whether to call Stripe / their DB / wherever.
//   - it does not accept arbitrary executor code; the action
//     name must be one the tenant's uploaded YAML declares.
//   - it does not expose cross-tenant state. The handler resolves
//     the tenant from the auth context and routes through the
//     same TenantRuntimeAccessor every other route uses.
//   - it does not stream parameter values into a public log.
//     Parameters land in the framework's audit store
//     (per-tenant, search_path-pinned) and the cloud's decision
//     store (also per-tenant). They are never echoed in the
//     response error path.
//
// MetricsRecorderFactory returns a runtime.MetricsRecorder bound
// to a tenant id. Implementations:
//
//   - usage.Aggregator.RecorderFor (production) buckets cloud
//     counters into cloud_meta.tenant_usage on a fixed-interval
//     flush.
//   - tests pass a recording stub.
//
// Optional: the proposal handler tolerates a nil factory and
// emits no cloud counters in that case (the framework's own
// CounterDecisionsRecorded still fires through the runtime).
type MetricsRecorderFactory func(tenantID, domainID string) kiffruntime.MetricsRecorder

// Limiter is the contract the proposal handler uses to enforce
// per-tenant plan limits. Production wires
// usage.ProposalLimiter; tests pass a stub.
//
// Returning allowed=false signals the proposal must be rejected
// with outcome=limit_exceeded before the runtime is even resolved.
// This is the free-tier hard cap — a disclosed ceiling.
//
// Returning allowed=true with overage=true signals a paid tenant
// has crossed its included allowance: the proposal is still
// evaluated (a commercial allowance must not become a production
// outage on a consequential path — the consumption model) and the handler
// records the overage for billing.
//
// Returning allowed=true with a non-nil error means "allow when
// uncertain" — the limiter ran into trouble (Postgres blip, etc)
// but a 5xx would be a worse failure mode than a brief
// over-count, and a billing-subsystem failure must never change an
// otherwise valid runtime decision. The handler logs the error.
type Limiter interface {
	AllowProposal(ctx context.Context, tenantID string) (allowed bool, used uint64, limit int, overage bool, err error)
}

// MeterLimits refuses a call that would count past what the plan allows:
// a taf's at the tasks ceiling, an own agent's at the
// trial's actions, and every call once the trial ended. Production wires
// meter.Limits. reason is "" when the call may go on; an error allows it,
// and is logged.
type MeterLimits interface {
	Refusal(ctx context.Context, tenantID, agentID, tool string) (reason, message string, err error)
}

type ProposalsHandler struct {
	tenants     TenantRuntimeAccessor
	tenantStore tenant.Store        // optional; guards erasing tenants on decide
	drafts      agentdrafts.Store   // optional; a trial decided against a draft's Cards (kiff/tafs#107)
	halts       halts.Store         // optional; the owner's halts
	controls    domain.ControlStore // optional; stamps control identity on decisions
	metrics     MetricsRecorderFactory
	limiter     Limiter
	meterLimits MeterLimits
	// mandates is the cumulative-authority check. Nil means
	// this deployment issues no mandates and behaves as before.
	mandates MandateAuthorizer
	// sealedReleases hands a sealed fill's release to the decision that
	// sends it (handSealedRelease); nil hands none.
	sealedReleases mandate.SealedReleases
	// browserBlocks keeps a held sealed fill's signed block
	// (keepBrowserBlock); nil keeps none.
	browserBlocks mandate.BrowserBlocks
	// verifyBlock checks planque's signature on a block; nil is
	// verifyPlanque, the pinned key. Only tests set it (export_test.go).
	verifyBlock func(json.RawMessage, string) bool
	// holdOwners and holdNotifier email the owner about each newly held
	// action; appBaseURL makes its link absolute. dispatch
	// runs the send detached; tests make it synchronous.
	holdOwners   OwnerEmailResolver
	holdNotifier notify.Notifier
	// heldWords says the held call in people's words in that email; optional.
	heldWords  HeldWordsSource
	appBaseURL string
	dispatch   func(func())
	ledger     billing.UsageLedger
	reader     proposals.Reader
	locker     ProposalLocker
	alerter    alert.Notifier
	cardUnits  CardUnitAsks   // optional; the one ask per agent, action and unit (kiff/tafs#464 item 11)
	events     *stream.Broker // optional; pushes live decision events to SSE subscribers
	logger     *slog.Logger
	now        func() time.Time
	idemWindow time.Duration
	// permits mints execution permits; nil refuses any request
	// for one. permitTargets says which actions need one.
	permits *permit.Issuer
	// runGrants checks a person's run grant on each call of their run;
	// nil decides a call carrying one as before.
	runGrants     *RunGrants
	callAbout     callabout.Store // optional; what each call is about, kept beside its decision
	permitTargets permit.Targets
	// askedBy records whose run made each call, for usage by person (RFC
	// 050 step 8); nil records nothing.
	askedBy CallRequesters
	// toolProfiles reads each gateway tool's owner-approved profile, so a
	// held call's alert says whether it moves money and whether it may wait
	// (alert.Hold.MoneyOut, alert.Hold.CanWait); nil leaves both out.
	toolProfiles GatewayToolProfiles
}

// CallRequesters records the person whose run made a call; the meter is one.
type CallRequesters interface {
	AskedBy(ctx context.Context, tenantID, key, subject string) error
}

// WithProposalsCallRequesters records whose run made each call it decides.
func WithProposalsCallRequesters(r CallRequesters) ProposalsOption {
	return func(h *ProposalsHandler) { h.askedBy = r }
}

// ProposalLocker serializes the dedup+evaluate+write critical
// section per (tenant, idempotency key) so concurrent retries of
// one key produce exactly one decision. Acquire blocks
// until the lock is held and returns a release func; production
// wires idempotency.AdvisoryLocker (Postgres advisory locks).
// Optional — when nil the gate falls back to the sequential dedup,
// which collapses sequential retries but not concurrent ones.
type ProposalLocker interface {
	Acquire(ctx context.Context, key string) (release func(), err error)
}

// defaultIdempotencyWindow is how long a caller-supplied
// idempotency key (body id or Idempotency-Key header) dedupes a
// repeated proposal. Inside the window a re-submission returns the
// recorded decision verbatim and writes no new decision/audit row.
// After the window the proposal is re-evaluated against
// current state, because the entity may have moved on since the
// first decision. 24h comfortably covers the retry-storm scenario
// the dedup targets (an agent hammering the same action over
// seconds-to-minutes) without pinning a stale answer indefinitely.
const defaultIdempotencyWindow = 24 * time.Hour

// tenantStatusChecker is kept as a compile-time guard; see
// WithProposalsTenantStore below.

// WithProposalsTenantStore installs the optional tenant store for
// the erasing-tenant guard on POST /v1/proposals/decide. When set,
// the handler returns a stable 403 error (ErrTenantErasing) before
// evaluating a proposal for a tenant in StatusDeletedPending or
// StatusDeleted. Production wires this via the main dependency
// graph; tests that don't need the guard may omit it.
// WithProposalsRunGrants checks the run grant a person's run carries on
// each of its calls: the call, and any hold it opens, is theirs.
func WithProposalsRunGrants(g *RunGrants) ProposalsOption {
	return func(h *ProposalsHandler) { h.runGrants = g }
}

// WithProposalsCallAbout keeps what each call is about (the taf's
// kiff_about) beside its decision, never in the record a receipt hashes,
// so it can be erased and every receipt still verifies.
func WithProposalsCallAbout(s callabout.Store) ProposalsOption {
	return func(h *ProposalsHandler) { h.callAbout = s }
}

func WithProposalsTenantStore(s tenant.Store) ProposalsOption {
	return func(h *ProposalsHandler) {
		h.tenantStore = s
	}
}

// WithProposalsAgentDrafts lets a gateway trial be decided against a
// revision draft's Cards (kiff/tafs#107). Without it, such a trial is
// refused.
func WithProposalsAgentDrafts(s agentdrafts.Store) ProposalsOption {
	return func(h *ProposalsHandler) {
		h.drafts = s
	}
}

// WithProposalsEventPublisher installs the live-event broker so each
// recorded decision is pushed to the tenant's SSE subscribers (the
// dashboard updates without a reload). Optional and best-effort: a nil
// broker or a full subscriber buffer never affects the decide path.
func WithProposalsEventPublisher(b *stream.Broker) ProposalsOption {
	return func(h *ProposalsHandler) {
		h.events = b
	}
}

// WithProposalsControlStore installs the optional control store so
// the decide path can stamp the governing control's identity
// (control_id + environment) onto each decision.
// When nil, decisions record without a control reference (unchanged
// behavior). The control is resolved by FindActiveControl(tenant,
// action, "") — the single active control today; multi-environment
// resolution is the deferred follow-up.
func WithProposalsControlStore(s domain.ControlStore) ProposalsOption {
	return func(h *ProposalsHandler) {
		h.controls = s
	}
}

// WithProposalsHalts refuses the calls of a halted agent, or of any agent of
// a halted account, and after a resume the ones asked before it.
// WithProposalsMeterLimits refuses calls that would count past the plan.
func WithProposalsMeterLimits(l MeterLimits) ProposalsOption {
	return func(h *ProposalsHandler) { h.meterLimits = l }
}

func WithProposalsHalts(s halts.Store) ProposalsOption {
	return func(h *ProposalsHandler) { h.halts = s }
}

// ProposalsOption configures ProposalsHandler.
type ProposalsOption func(*ProposalsHandler)

// WithProposalsLogger substitutes the logger.
func WithProposalsLogger(l *slog.Logger) ProposalsOption {
	return func(h *ProposalsHandler) {
		if l != nil {
			h.logger = l
		}
	}
}

// WithProposalsClock substitutes the clock used for proposal
// CreatedAt defaults. Tests pin time so output is reproducible.
func WithProposalsClock(now func() time.Time) ProposalsOption {
	return func(h *ProposalsHandler) {
		if now != nil {
			h.now = now
		}
	}
}

// WithProposalsMetrics installs the per-tenant metrics recorder
// factory. Production wiring passes
// usage.Aggregator.RecorderFor; tests pass a recording stub.
// nil disables cloud-counter emission (the framework's own
// counters still flow through rt.RecordActionProposal).
func WithProposalsMetrics(factory MetricsRecorderFactory) ProposalsOption {
	return func(h *ProposalsHandler) {
		if factory != nil {
			h.metrics = factory
		}
	}
}

// WithProposalsUsageLedger installs the billable governed-operation
// ledger. When set, the handler records one idempotent ledger
// row per genuinely-new governed operation (allowed / blocked /
// approval_required), best-effort and off the decision path — a
// ledger failure is logged and never changes or blocks the decision.
// nil disables ledger recording (tests / dev).
func WithProposalsUsageLedger(l billing.UsageLedger) ProposalsOption {
	return func(h *ProposalsHandler) {
		if l != nil {
			h.ledger = l
		}
	}
}

// WithProposalsLimiter installs the per-tenant plan-limit check.
// Production wires usage.ProposalLimiter; tests can pass a stub
// or omit (nil disables limiting — the handler treats every
// tenant as unlimited).
func WithProposalsLimiter(l Limiter) ProposalsOption {
	return func(h *ProposalsHandler) {
		if l != nil {
			h.limiter = l
		}
	}
}

// WithProposalsAlerter installs the outbound alert notifier.
// When set, decide fires a best-effort alert on consequential
// outcomes (approval_required / blocked) via the notifier, which
// delivers asynchronously and never blocks the response. nil is
// ignored (alerts disabled).
func WithProposalsAlerter(n alert.Notifier) ProposalsOption {
	return func(h *ProposalsHandler) {
		if n != nil {
			h.alerter = n
		}
	}
}

// WithProposalsToolProfiles lets a held call's alert say whether its
// operation moves money and whether it may wait (alert.Hold.MoneyOut,
// alert.Hold.CanWait),
// from the tool's owner-approved profile. nil is ignored: the alert leaves
// both out.
func WithProposalsToolProfiles(p GatewayToolProfiles) ProposalsOption {
	return func(h *ProposalsHandler) {
		if p != nil {
			h.toolProfiles = p
		}
	}
}

// WithProposalsLocker installs the per-(tenant, key) locker that
// makes the idempotency dedup atomic under concurrent retries.
// Production wires idempotency.AdvisoryLocker; tests pass
// a stub or omit (nil — sequential dedup only). A nil locker is
// ignored so the option is always safe to pass.
func WithProposalsLocker(l ProposalLocker) ProposalsOption {
	return func(h *ProposalsHandler) {
		if l != nil {
			h.locker = l
		}
	}
}

// WithProposalsIdempotencyWindow overrides how long a
// caller-supplied idempotency key dedupes a repeated proposal.
// Production uses the default (24h); tests pin a short window (or
// a long one) to exercise both the in-window and expired paths
// deterministically. A non-positive duration is ignored so a
// misconfigured zero can't silently disable dedup.
func WithProposalsIdempotencyWindow(d time.Duration) ProposalsOption {
	return func(h *ProposalsHandler) {
		if d > 0 {
			h.idemWindow = d
		}
	}
}

// NewProposalsHandler returns a handler ready to register.
// tenants is the cloud's runtime accessor (typically
// runtime.TenantRuntime). The handler does not retain references
// to anything cross-tenant.
func NewProposalsHandler(tenants TenantRuntimeAccessor, opts ...ProposalsOption) *ProposalsHandler {
	h := &ProposalsHandler{
		tenants:    tenants,
		logger:     slog.Default(),
		now:        func() time.Time { return time.Now().UTC() },
		idemWindow: defaultIdempotencyWindow,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Register attaches the handler's routes to mux.
func (h *ProposalsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/proposals/decide", h.decide)
	mux.HandleFunc("POST /v1/gateway/trial", h.gatewayTrial)
	mux.HandleFunc("GET /v1/proposals", h.list)
}

// proposalRequest is the JSON body the integrator sends. Field
// names mirror the framework's proposal.ActionProposal so any
// future SDK can serialize directly. Currently no SDK exists;
// the JSON is the contract.
//
// Required: entity_id, entity_type, action_name, actor_id.
// Optional: id (auto-generated when empty so calls without an
//
//	idempotency key still produce a valid decision id),
//	parameters (flat map[string]any),
//	reasoning_summary, confidence,
//	created_at (defaults to server clock when zero).
//
// Roles are deliberately NOT accepted from the body. A caller
// must not be able to self-assert the authority that makes its
// own action allowed: that would turn KIFF into a pass-through
// validator instead of a governance boundary. Roles are derived
// from the authenticated identity (Identity.Roles, populated by
// the auth middleware from the API key record or upstream
// session claims) and merged into the action context server-side.
type proposalRequest struct {
	ID               string         `json:"id,omitempty"`
	EntityID         string         `json:"entity_id"`
	EntityType       string         `json:"entity_type"`
	ActionName       string         `json:"action_name"`
	ActorID          string         `json:"actor_id"`
	Parameters       map[string]any `json:"parameters,omitempty"`
	ReasoningSummary string         `json:"reasoning_summary,omitempty"`
	Confidence       float64        `json:"confidence,omitempty"`
	CreatedAt        time.Time      `json:"created_at,omitempty"`

	// ApprovalID lets a caller drive the approval round-trip:
	// on a re-decide after a human granted, the caller
	// passes the approval id it received on the first
	// approval_required so the gate can resolve the grant and
	// return "allowed". Optional — when empty the gate derives a
	// deterministic id from the proposal id, so a caller that just
	// retries the same proposal id also round-trips without
	// tracking the approval id separately. Never used to assert
	// authority: a granted approval only flips the outcome if the
	// approval store actually holds a matching granted record.
	ApprovalID string `json:"approval_id,omitempty"`

	// Permit asks for the call's execution permit: sent by the
	// gateway for a connection in verify or relay mode. The operation is
	// bound to its tool, arguments and audience at its first decision.
	Permit *permit.Request `json:"permit,omitempty"`

	// Environment scopes the decision to a specific runtime
	// environment (e.g. "aws", "dev") so control resolution stamps the
	// matching active control when a tenant runs the same action under
	// more than one control (minimal case). Optional: empty falls
	// back to the single-active-control resolution (current behavior).
	//
	// Authority-bearing for cards: it decides which environment-scoped
	// cards apply (cardEnvironmentFor). A card scoped to one
	// environment covers, and draws for, only a request in that
	// environment. When empty, the request is in the environment of the
	// active control governing the action in the card's domain; when
	// that is unknown or ambiguous, no environment-scoped card applies.
	// The value is the caller's to name, as an explicit domain selector
	// is for a tenant-wide key.
	Environment string `json:"environment,omitempty"`

	// Domain optionally selects which of the tenant's operational
	// domains the request routes to. Optional and
	// additive: empty resolves the tenant's default domain (current
	// behavior). When the API key is bound to a domain, a value here
	// that names a *different* domain is refused (the binding is an
	// isolation boundary, not a default the caller can override).
	Domain string `json:"domain,omitempty"`

	// RunContext carries what the harness knows about this agent run
	// and the model does not get to assert: a run id, and
	// whether untrusted content has entered the run. Optional and
	// additive, so /v1 stays backward compatible.
	//
	// It is a *sibling* of the proposal, never a field inside it. The
	// proposal is the model-supplied payload — including Parameters,
	// which is a map[string]any and would carry this today with no
	// schema change — and provenance the model can write is
	// provenance an injected instruction can clear. A caller that
	// puts run context in Parameters is refused rather than merged;
	// see paramsCarryRunContext.
	//
	// Recorded on the decision, not yet acted on: the enforcement
	// slice (a domain-grammar condition, fail-closed on an absent or
	// unverifiable assertion) is the next step in the trusted-run-context design, and until
	// it lands this field changes no outcome.
	RunContext *runcontext.RunContext `json:"run_context,omitempty"`

	// TrialDraft names the draft whose trial made this call
	// (kiff/tafs#173): sent by the gateway for a call made with KIFF-Trial
	// while its owner tries that draft. The call is decided on the trial's
	// Cards alone (mandate.TrialSubject), and refused once the trial ends.
	// Only a key bound to the draft's agent may name it.
	TrialDraft string `json:"trial_draft,omitempty"`

	// RunGrant is the run grant of the person whose run makes the call,
	// sent by the gateway from the run's KIFF-Run-Grant. KIFF checks it is
	// for this key's agent and that the person may still use it, and
	// refuses the call otherwise; the call, and any hold it opens, is
	// then theirs. Only a key bound to the agent may send one.
	RunGrant string `json:"run_grant,omitempty"`

	// TafAbout and TafReason are the taf's own words about the call, sent
	// by the gateway: kept with a hold for its owner, labelled as the
	// taf's, and never part of the decision (taf_words.go).
	TafAbout  tafText `json:"taf_about,omitempty"`
	TafReason tafText `json:"taf_reason,omitempty"`

	// BrowserBlock is planque's signed description of a sealed fill, sent
	// by the gateway on the decide that holds it: kept with the hold only
	// when planque's signature holds (browser_block.go).
	BrowserBlock json.RawMessage `json:"browser_block,omitempty"`
}

// proposalResponse is the JSON body returned on every call,
// successful or not. The outcome string is always one of the
// four stable values; reasons[] carries a list of machine-
// readable codes for "blocked" and "invalid" so callers can
// branch without parsing English.
type proposalResponse struct {
	ProposalID string   `json:"proposal_id"`
	Outcome    string   `json:"outcome"`
	Reasons    []string `json:"reasons,omitempty"`
	Message    string   `json:"message,omitempty"`

	// Deduplicated is true when this response was served from a
	// prior decision for the same idempotency key
	// instead of a fresh evaluation. The outcome/reasons are the
	// recorded ones; no new decision/audit row was written. Absent
	// (false, dropped by omitempty) on a normal first evaluation.
	Deduplicated bool `json:"deduplicated,omitempty"`

	// FirstDecidedAt is the CreatedAt of the original decision when
	// Deduplicated is true, so a caller (or the Playground panel)
	// can show "first seen at …". Nil on a normal evaluation.
	FirstDecidedAt *time.Time `json:"first_decided_at,omitempty"`

	// Approval is the actionable approval handle returned on an
	// approval_required outcome: the id the caller (and the
	// dashboard) correlate on, the current review status, and a
	// pointer to where a human reviews it. Nil for every other
	// outcome. The gate auto-creates the pending approval on the
	// first approval_required so the caller does not have to POST
	// /v1/entities/{id}/approvals itself.
	Approval *approvalHandle `json:"approval,omitempty"`

	// Mode is the tenant's governance posture for this decision:
	// "enforce" (the default — the outcome is binding and
	// the caller gates on it) or "observe" (the outcome is advisory;
	// the caller runs their existing flow and KIFF records what it
	// would have decided). Always present so a caller never has to
	// guess whether to gate.
	Mode string `json:"mode,omitempty"`

	// Advisory is true when Mode is "observe": the decision was
	// computed and recorded exactly as in enforce mode, but the
	// caller is explicitly NOT expected to gate execution on it.
	// Dropped (false) under enforce. Pairs with Mode so a client
	// can branch on a single boolean.
	Advisory bool `json:"advisory,omitempty"`

	// Exception is the held exception on an owner_required outcome
	// (the mandates design amendment): the action is outside the agent's Card and
	// waits for the Card's owner.
	Exception *exceptionHandle `json:"exception,omitempty"`

	// Permit is the operation's execution permit, on the
	// allowed decision that minted it; at most one per operation.
	Permit string `json:"permit,omitempty"`
	// PermitIssuedAt is when the operation's one permit was issued, on a
	// decision that did not mint it because another request already had:
	// the gateway waits on that request until the permit can no longer be
	// used.
	PermitIssuedAt *time.Time `json:"permit_issued_at,omitempty"`
	// SealedRelease is the phone's release for a sealed fill the owner
	// allowed once, on the allowed decision that sends it: handed to the
	// gateway once, which sends it to the tool beside the call
	// (mandate.SealedReleases). Never kept in the response's record.
	SealedRelease json.RawMessage `json:"sealed_release,omitempty"`
}

// approvalHandle is the approval pointer embedded in a
// proposalResponse when the outcome is approval_required. Status
// is one of pending | granted | denied. ReviewURL is the
// dashboard surface a human uses to grant or deny; it is a
// relative path so it works across app hosts.
// exceptionHandle points at the held exception on an owner_required
// outcome: the id the caller and the dashboard correlate on, its
// status, and where the owner answers it.
type exceptionHandle struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	ReviewURL string `json:"review_url,omitempty"`
	// ExpiresAt is when a held action stops waiting for its owner and is
	// refused.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// NextStep and Note are, on a rejection, what the owner asked the
	// agent to do instead.
	NextStep string `json:"next_step,omitempty"`
	Note     string `json:"note,omitempty"`
	// Why is, on a hold, each limit the call went over, in the words the
	// owner reads on the hold: "40.00 USD would go over the 250.00 USD
	// limit: 210.00 USD is used". The agent tells its user the same.
	Why []string `json:"why,omitempty"`
}

type approvalHandle struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	ReviewURL string `json:"review_url,omitempty"`
}

// outcome is the typed enumeration the handler maps to the stable
// wire strings. The four governed outcomes are the framework's
// outcome vocabulary (kiff/kiff outcome package, v0.7.0) — bound to
// the framework constants below so the cloud's wire values can never
// drift from the framework's canonical taxonomy. The cloud
// adds one value, limit_exceeded, that the framework does not model.
type outcome string

const (
	outcomeAllowed          = outcome(kiffoutcome.Allowed)
	outcomeApprovalRequired = outcome(kiffoutcome.ApprovalRequired)
	outcomeBlocked          = outcome(kiffoutcome.Blocked)
	outcomeInvalid          = outcome(kiffoutcome.Invalid)

	// outcomeOwnerRequired is the cloud's own value, like
	// limit_exceeded: the action is outside a Card set to come home
	// and is held for its owner (the mandates design amendment, "the work comes
	// home"). Nothing is authorized; the caller re-decides the same
	// proposal id later, as for approval_required. The framework does
	// not model Cards, so it does not model this outcome.
	outcomeOwnerRequired = outcome("owner_required")
	// A reviewed approval without current cumulative room is recoverable,
	// never a terminal block and never permission to execute.
	outcomeApprovedNoRoom = outcome("approved_no_room")

	// outcomeLimitExceeded is returned when the tenant has
	// exhausted their monthly proposal budget. The proposal is
	// not evaluated against the domain contract: the limit check
	// runs before the runtime resolution. Distinct from "blocked"
	// (a governance decision) so the dashboard / activation
	// tracker can surface it as an upgrade trigger. Cloud-only:
	// the framework outcome package does not model a usage limit.
	outcomeLimitExceeded outcome = "limit_exceeded"
)

// decide handles POST /v1/proposals/decide. The bulk of the
// logic is in evaluate(); this method is the HTTP shell:
// authenticate, parse, evaluate, audit, render.
func (h *ProposalsHandler) decide(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}

	// Erasing-tenant guard + governance mode. We fetch the tenant
	// once here and reuse it for both: the erasing check (reject
	// deleted tenants) and the observe/enforce mode that
	// annotates the decision as binding or advisory. Capturing the
	// mode from this same Get keeps observe mode free of an extra
	// round-trip on the hot path.
	mode := tenant.ModeEnforce
	if h.tenantStore != nil {
		t, err := h.tenantStore.Get(r.Context(), id.Tenant)
		if err == nil {
			if t.IsErasing() {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"error": ErrTenantErasing.Error(),
				})
				return
			}
			// Suspended tenants are held offline by an operator.
			// Reject with a clear, stable 403 here rather
			// than letting the call fall through to the runtime
			// resolver's misleading "runtime unavailable" path.
			if t.Status == tenant.StatusSuspended {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"error": ErrTenantSuspended.Error(),
				})
				return
			}
			mode = tenant.ModeFor(t)
		}
		// A missing tenant or store error falls through: the
		// runtime resolution step below will surface the same
		// failure with its own error response. Mode stays enforce
		// (fail-closed) when the tenant can't be read.
	}

	// The plan-limit check used to run here, before body parsing.
	// It now runs *after* the idempotency dedup (further down) so a
	// retry storm sharing one key does not burn quota on duplicates
	// of a single logical decision — see the dedup + limiter blocks
	// below. The cost is that an over-budget tenant's body is parsed
	// and its runtime resolved before the limit is enforced; both
	// are cheap (the runtime is cached per tenant) and the limiter
	// still gates the expensive evaluate().

	var req proposalRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// Malformed body still counts as a proposal attempt
			// against the tenant: it occupies the gate, it lands
			// in the audit log via recordRejection, and it should
			// be visible in the activation/limit picture. We bump
			// submitted + invalid before responding so the
			// counters match the audit row's claim of "the tenant
			// asked KIFF to decide."
			h.incCounter(r.Context(), id.Tenant, usage.CloudCounterProposalsSubmitted)
			h.incCounter(r.Context(), id.Tenant, usage.CloudCounterProposalsInvalid)
			writeJSON(w, http.StatusBadRequest, proposalResponse{
				Outcome: string(outcomeInvalid),
				Reasons: []string{"invalid_json"},
				Message: "request body is not valid JSON",
			})
			return
		}
	}

	prop, idempotent := h.normalizeProposal(req, r.Header.Get("Idempotency-Key"))
	// the agent-as-unit design: a key bound to an agent carries the agent's identity,
	// so the proposal need not repeat it. An empty actor_id becomes the
	// key's agent; one that names someone else is refused by
	// holderRefusal below. Unbound keys and sessions still name an actor.
	if id.APIKey && id.AgentID != "" && prop.ActorID == "" {
		prop.ActorID = id.AgentID
	}
	// Carry the request environment for control resolution (minimal).
	// Used by recordOutcome/recordRejection → resolveControlIdentity
	// below; empty preserves single-active-control behavior. A key bound
	// to an environment decides in it; a mismatch is refused below,
	// once the domain is known, so the refusal is recorded there.
	requestEnv, envMatches := resolveRequestEnvironment(id, strings.TrimSpace(req.Environment))
	ctx := withControlEnvironment(r.Context(), requestEnv)
	// Resolve the tenant's active domain once and thread it so cloud
	// proposal counters are attributed per domain. Server-
	// validated (the active control's domain), bounded; empty falls
	// back to the fixed CloudEntityProposal label.
	ctx = withUsageDomain(ctx, h.activeDomainFor(ctx, id.Tenant))
	ctx = withDecideMode(ctx, mode)
	// Per-domain posture: the governing control's mode wins over the
	// tenant-wide default. Now that the action is known we can resolve
	// the control; an unset control mode falls back to the tenant mode.
	mode = h.effectiveMode(ctx, id.Tenant, prop.ActionName, mode)

	// Run context. Resolved before the shape check so a
	// rejection recorded below still carries the run it happened in:
	// a malformed proposal inside a tainted run is evidence too.
	//
	// Refusing run context smuggled through Parameters is the
	// enforceable half of "asserted by the harness, never by the
	// model". The code never *reads* run context from the proposal,
	// so this check changes no decision — it turns an integrator
	// mistake into a loud error instead of a value that is silently
	// ignored while the integrator believes it is governing
	// something. The cost is that a domain wanting a literal
	// "run_context" action parameter cannot have one, which is a
	// trade worth making for a name this load-bearing.
	if key, found := paramsCarryRunContext(req.Parameters); found {
		h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsSubmitted)
		h.recordRejection(ctx, id, prop, outcomeInvalid, []string{"run_context_in_parameters"})
		resp := proposalResponse{
			ProposalID: prop.ID,
			Outcome:    string(outcomeInvalid),
			Reasons:    []string{"run_context_in_parameters"},
			Message: "run context must be sent as a sibling of the proposal, not inside parameters " +
				"(offending parameter: " + key + ")",
		}
		h.applyMode(ctx, id.Tenant, mode, &resp)
		writeJSON(w, http.StatusBadRequest, resp)
		return
	}
	if req.RunContext != nil {
		if err := req.RunContext.Validate(); err != nil {
			h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsSubmitted)
			h.recordRejection(ctx, id, prop, outcomeInvalid, []string{"invalid_run_context"})
			resp := proposalResponse{
				ProposalID: prop.ID,
				Outcome:    string(outcomeInvalid),
				Reasons:    []string{"invalid_run_context"},
				Message:    "run_context is malformed: " + err.Error(),
			}
			h.applyMode(ctx, id.Tenant, mode, &resp)
			writeJSON(w, http.StatusBadRequest, resp)
			return
		}
		ctx = runcontext.NewContext(ctx, *req.RunContext)
	}

	if invalid, reasons := basicProposalShapeIssues(prop); invalid {
		// A shape-malformed proposal is a real (non-duplicate)
		// arrival: count it as submitted + invalid and audit the
		// rejection. The customer asked us to decide on something
		// we couldn't parse, and that fact is itself a decision.
		h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsSubmitted)
		h.recordRejection(ctx, id, prop, outcomeInvalid, reasons)
		resp := proposalResponse{
			ProposalID: prop.ID,
			Outcome:    string(outcomeInvalid),
			Reasons:    reasons,
			Message:    "proposal is missing required fields",
		}
		h.applyMode(ctx, id.Tenant, mode, &resp)
		writeJSON(w, http.StatusBadRequest, resp)
		return
	}

	// Resolve which of the tenant's domains this request routes to.
	// The API key's binding is an isolation
	// boundary: a domain-scoped key presented with a conflicting
	// explicit selector is refused (403), never redirected. A
	// tenant-wide key passes the selector through; empty resolves the
	// tenant's default domain. The slug is normalized so a human name
	// and its slug both match.
	resolvedDomain, derr := domain.ResolveRequestDomain(id.DomainID, domain.Slugify(strings.TrimSpace(req.Domain)))
	if derr != nil {
		h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsSubmitted)
		h.recordRejection(ctx, id, prop, outcomeInvalid, []string{"domain_selector_mismatch"})
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "the API key is bound to a different domain than the request selected",
		})
		return
	}
	ctx = withRuntimeDomain(ctx, resolvedDomain)
	if !envMatches {
		h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsSubmitted)
		h.recordRejection(ctx, id, prop, outcomeInvalid, []string{"environment_selector_mismatch"})
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "the API key is bound to a different environment than the request selected",
		})
		return
	}

	rt, err := h.runtimeFor(ctx, id.Tenant)
	// the trusted-run-context design: resolve the cloud-side spec HERE, beside the runtime,
	// rather than lazily inside the run-context gate. Both come from
	// one cache entry and one parse, so resolving them together means
	// there is exactly one place this can fail — and it is a place that
	// already refuses the proposal. A lazy lookup inside the gate had a
	// second failure mode whose only honest options were "fail the
	// tenant's unrelated traffic" or "let a declaring action through
	// ungoverned". Removing the second failure point is better than
	// choosing between those two.
	var spec domain.Spec
	var answeredBy string
	if err == nil {
		spec, err = h.tenants.SpecForDomain(ctx, id.Tenant, runtimeDomainFrom(ctx))
	}
	if err == nil {
		answeredBy, err = h.tenants.DomainIDFor(ctx, id.Tenant, runtimeDomainFrom(ctx))
	}
	if err != nil {
		h.logger.ErrorContext(r.Context(), "proposals: tenant runtime resolution failed",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
		// Record the rejection so the audit trail captures that
		// the tenant tried to decide before they had a domain
		// uploaded. This is a real signal the integrator is
		// missing setup step.
		h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsSubmitted)
		h.recordRejection(ctx, id, prop, outcomeInvalid, []string{"tenant_runtime_unavailable"})
		resp := proposalResponse{
			ProposalID: prop.ID,
			Outcome:    string(outcomeInvalid),
			Reasons:    []string{"tenant_runtime_unavailable"},
			Message:    "tenant runtime is not ready; upload a domain (PUT /v1/me/domain) before deciding",
		}
		h.applyMode(ctx, id.Tenant, mode, &resp)
		writeJSON(w, statusForResponse(resp), resp)
		return
	}
	// Stamp the decision with the immutable domain_id of the domain whose
	// runtime answered it, scoped or not. This is identity for the
	// record only. Cards are matched by the domain's current slug
	// (cardDomainFor), the name they are issued under and explicit
	// selectors use, so an unscoped request finds exactly the cards a
	// request naming the default domain would: a domain replaced in place
	// keeps its first id, and matching cards by that id refused every
	// unscoped card decision on such a tenant.
	if answeredBy != "" {
		ctx = withDecidingDomain(ctx, answeredBy)
	}

	// A trial's call (kiff/tafs#173): only the draft's agent names it, and
	// it is part of the proposal's identity from here on, so the cache
	// never answers it with a live call's decision, or the other way.
	if draft := strings.TrimSpace(req.TrialDraft); draft != "" {
		if !id.APIKey || id.AgentID == "" || prop.ActorID != id.AgentID {
			h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsSubmitted)
			h.recordRejection(ctx, id, prop, outcomeInvalid, []string{"trial_needs_agent_key"})
			resp := proposalResponse{ProposalID: prop.ID, Outcome: string(outcomeInvalid), Reasons: []string{"trial_needs_agent_key"},
				Message: "a trial's call is made with the key of the agent being tried, for itself"}
			writeJSON(w, http.StatusBadRequest, resp)
			return
		}
		ctx = withTrialDraft(ctx, draft)
	}

	ctx = withTafWords(ctx, string(req.TafAbout), string(req.TafReason))

	// A person's run: the call is theirs if their run grant says so and
	// they may still use the agent; a grant that fails refuses the call.
	if grant := strings.TrimSpace(req.RunGrant); grant != "" && h.runGrants != nil {
		reason := ""
		if !id.APIKey || id.AgentID == "" || prop.ActorID != id.AgentID {
			reason = "run_grant_needs_agent_key"
		} else if sub, err := h.runGrants.Check(ctx, id.Tenant, id.AgentID, grant); err != nil {
			reason = "run_grant"
			if !errors.Is(err, permit.ErrInvalidToken) && !errors.Is(err, errNoUse) {
				h.logger.ErrorContext(ctx, "proposals: run grant not checked", slog.String("err", err.Error()))
			}
		} else {
			ctx = withRunGrantSubject(ctx, sub)
		}
		if reason != "" {
			h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsSubmitted)
			h.recordRejection(ctx, id, prop, outcomeInvalid, []string{reason})
			resp := proposalResponse{ProposalID: prop.ID, Outcome: string(outcomeInvalid), Reasons: []string{reason},
				Message: "this call's run grant isn't valid for this agent, or the person who asked may no longer use it; nothing was sent"}
			writeJSON(w, http.StatusForbidden, resp)
			return
		}
	}

	// Card-holder identity, before the idempotency cache and before any
	// state, approval or authority is considered. Ahead of the cache
	// because a replayed proposal id must not return a decision made
	// for a credential that could speak for its actor to one that
	// cannot; ahead of evaluation so it never opens an approval or
	// draws on a card in another agent's name. Recorded as a rejection,
	// not a decision, so it cannot become the cached answer for the
	// rightful agent's retries of the same id.
	if refusal, refused := h.holderRefusal(ctx, id, prop, spec); refused {
		h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsSubmitted)
		h.recordRejection(ctx, id, prop, outcomeBlocked, refusal.Reasons)
		h.applyMode(ctx, id.Tenant, mode, &refusal)
		writeJSON(w, statusForResponse(refusal), refusal)
		return
	}

	// The owner's halt, ahead of the idempotency
	// cache, the permit gate and any approval: a halted agent's call is
	// refused whatever was decided for it before, a held one included, so
	// nothing is sent, and no permit is issued. Once resumed, a call first
	// asked before the halt stays refused: only new calls go through.
	// The owner's halt binds in observe mode too: the mode is labelled, the
	// refusal isn't advisory (like limit_exceeded in applyMode).
	if refusal, refused := h.haltRefusal(ctx, id, prop, rt); refused {
		h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsSubmitted)
		refusal.Mode = string(mode)
		writeJSON(w, statusForResponse(refusal), refusal)
		return
	}

	// Idempotency dedup runs BEFORE the plan-limit check so a
	// retry storm sharing one key is free: a duplicate neither
	// consumes quota nor double-counts. Generated timestamp ids are
	// unique per call, so the lookup is skipped for them.
	//
	// For idempotent requests, the dedup + evaluate + write run
	// inside a per-(tenant, key) advisory lock so concurrent
	// retries serialize: the first writes the decision, and the
	// others observe it in maybeDedupe and dedupe instead of
	// re-evaluating — exactly one decision row. The lock is held
	// until the handler returns (defer release). When no locker is
	// wired the dedup still collapses sequential retries.
	var releaseLock func()
	if req.Permit != nil && h.locker == nil {
		// the execution-permits design: a permit request is bound and checked under the
		// per-operation lock. With no locker installed (an in-memory
		// or local run), refuse it, as when the lock fails.
		h.logger.ErrorContext(r.Context(), "proposals: no idempotency locker; refusing a permit request",
			slog.String("tenant", id.Tenant), slog.String("proposal_id", prop.ID))
		h.refuseUnserialized(ctx, w, id, mode, prop)
		return
	}
	if idempotent && h.locker != nil {
		release, lerr := h.locker.Acquire(ctx, id.Tenant+"\x00"+prop.ID)
		switch {
		case lerr != nil && req.Permit != nil:
			// the execution-permits design: a permit request is bound and checked under this
			// lock, so the binding, the hold and the release of one
			// operation are serialized. Without it, refuse.
			h.logger.ErrorContext(r.Context(), "proposals: idempotency lock failed; refusing a permit request",
				slog.String("tenant", id.Tenant), slog.String("proposal_id", prop.ID), slog.String("err", lerr.Error()))
			h.refuseUnserialized(ctx, w, id, mode, prop)
			return
		case lerr != nil:
			// A lock failure must not drop the request: log and
			// proceed unlocked (degrades to sequential dedup).
			h.logger.WarnContext(r.Context(), "proposals: idempotency lock failed; proceeding unlocked",
				slog.String("tenant", id.Tenant),
				slog.String("proposal_id", prop.ID),
				slog.String("err", lerr.Error()),
			)
		default:
			releaseLock = release
			// Safety net: always release by the time decide
			// returns. release is idempotent, so the explicit
			// release on the dedup-hit path below is fine too.
			defer releaseLock()
		}
	}

	// the execution-permits design, under the operation's lock: bind the operation to the exact
	// call before the dedup, so
	// a reused id naming another call is refused even where the
	// amount-only fingerprint matches, and a held call's release must be
	// the call that was held.
	var binding permit.Binding
	if req.Permit != nil {
		b, resp, stop := h.permitGate(ctx, id, prop, spec, idempotent, *req.Permit)
		if stop {
			if !resp.Deduplicated {
				h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsSubmitted)
			}
			h.applyMode(ctx, id.Tenant, mode, &resp)
			writeJSON(w, statusForResponse(resp), resp)
			return
		}
		binding = b
		ctx = withCoveringSlot(ctx)
	} else if resp, stop := h.permitRequired(ctx, id, prop, spec); stop {
		h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsSubmitted)
		h.applyMode(ctx, id.Tenant, mode, &resp)
		writeJSON(w, statusForResponse(resp), resp)
		return
	}

	if idempotent {
		// The decision was recorded with the gateway's own parameters taken
		// out (the observed unit, an unread amount), so the retry is
		// compared the same way: with them in, every retry of a call that
		// carries them read as another request (idempotency_key_reuse).
		_, recorded := takeObservedUnit(spec, prop)
		_, recorded = takeAmountUnverified(spec, recorded)
		_, recorded = takeOwnerAsk(spec, recorded)
		if dup, ok := h.maybeDedupe(ctx, id, recorded, rt); ok {
			h.applyMode(ctx, id.Tenant, mode, &dup)
			// Release the lock (and its parked connection) before
			// the network write — the critical section is over once
			// we've read the prior decision.
			if releaseLock != nil {
				releaseLock()
			}
			writeJSON(w, statusForResponse(dup), dup)
			return
		}
	}

	// A trial's call is decided only while its trial is open: once it
	// ended, this call and any held under it are refused (kiff/tafs#173).
	if refusal, refused := h.trialRefusal(ctx, id, prop); refused {
		if releaseLock != nil {
			releaseLock()
		}
		h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsSubmitted)
		h.applyMode(ctx, id.Tenant, mode, &refusal)
		writeJSON(w, statusForResponse(refusal), refusal)
		return
	}

	// Plan-limit check for a genuinely new (non-duplicate) proposal.
	// allowed-when-uncertain: a flush or store error logs and the
	// request proceeds. Submitted is bumped only after allowance, so
	// request N+1 (N=limit) is the rejected one — and the deduped
	// retries handled above never reach this counter.
	//
	// Free tenants hard-stop at their disclosed ceiling. Paid
	// tenants do not: crossing the included allowance is metered
	// overage, not an outage — a commercial limit must never stop a
	// valid production decision on a consequential path (the consumption model).
	// overLimit carries that paid-overage signal to the
	// billing counter below.
	overLimit := false
	if h.limiter != nil {
		allowed, used, limit, overage, lerr := h.limiter.AllowProposal(ctx, id.Tenant)
		if lerr != nil {
			h.logger.WarnContext(r.Context(), "proposals: limit check failed; allowing",
				slog.String("tenant", id.Tenant),
				slog.String("err", lerr.Error()),
			)
		}
		if !allowed {
			h.recordRejection(ctx, id, prop, outcomeLimitExceeded, []string{"plan_limit_reached"})
			resp := proposalResponse{
				ProposalID: prop.ID,
				Outcome:    string(outcomeLimitExceeded),
				Reasons:    []string{"plan_limit_reached"},
				Message: fmt.Sprintf(
					"monthly proposal budget exhausted (used=%d limit=%d); upgrade your plan or wait for the next period",
					used, limit,
				),
			}
			h.applyMode(ctx, id.Tenant, mode, &resp)
			writeJSON(w, statusForResponse(resp), resp)
			return
		}
		overLimit = overage
	}

	// The meters: a call that would count past what the
	// plan allows is refused before anything is sent. A trial decides and
	// sends nothing, so it never counts and is never stopped here.
	if h.meterLimits != nil && trialDraftFrom(ctx) == "" {
		reason, msg, merr := h.meterLimits.Refusal(ctx, id.Tenant, prop.ActorID, prop.ActionName)
		if merr != nil {
			h.logger.WarnContext(r.Context(), "proposals: meter limit check failed; allowing",
				slog.String("tenant", id.Tenant), slog.String("err", merr.Error()))
		}
		if reason != "" {
			if releaseLock != nil {
				releaseLock()
			}
			h.recordRejection(ctx, id, prop, outcomeLimitExceeded, []string{reason})
			resp := proposalResponse{ProposalID: prop.ID, Outcome: string(outcomeLimitExceeded), Reasons: []string{reason}, Message: msg}
			h.applyMode(ctx, id.Tenant, mode, &resp)
			writeJSON(w, statusForResponse(resp), resp)
			return
		}
	}

	// Submitted counter fires for a genuinely new proposal we are
	// about to evaluate (not duplicates, which returned above, and
	// not limit-exceeded rejections, which carry their own counter).
	h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsSubmitted)
	if overLimit {
		// Paid tenant past its included allowance. Record the
		// overage for billing and continue to evaluate — the
		// decision path is never stopped by a commercial limit.
		// Runs after dedup, so retries of one
		// logical action never double-count overage.
		h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsOverage)
		h.logger.WarnContext(r.Context(), "proposals: paid allowance exceeded; recording overage and continuing",
			slog.String("tenant", id.Tenant),
		)
	}
	resp := h.evaluate(ctx, id, prop, req.ApprovalID, idempotent, rt, spec)
	if req.Permit != nil {
		h.mintPermit(ctx, id, prop, binding, &resp)
	}
	h.applyMode(ctx, id.Tenant, mode, &resp)
	h.recordBillableOperation(ctx, id.Tenant, prop.ID, resp.Outcome)
	h.recordAskedBy(ctx, id.Tenant, prop.ID)
	h.maybeAlert(ctx, id, prop, resp)
	h.keepBrowserBlock(ctx, id.Tenant, prop, spec, resp, req.BrowserBlock)
	// Last, so nothing above reads or records it.
	h.handSealedRelease(ctx, id.Tenant, prop, spec, &resp)
	writeJSON(w, statusForResponse(resp), resp)
}

// recordAskedBy notes the person whose run made the call, when its run
// grant named one: usage reads by person from it. Off the
// decision path: a failure is logged, never surfaced.
func (h *ProposalsHandler) recordAskedBy(ctx context.Context, tenantID, proposalID string) {
	subject := runGrantSubjectOf(ctx)
	if h.askedBy == nil || subject == "" {
		return
	}
	if err := h.askedBy.AskedBy(ctx, tenantID, proposalID, subject); err != nil {
		h.logger.WarnContext(ctx, "proposals: who asked not recorded", slog.String("tenant", tenantID),
			slog.String("proposal_id", proposalID), slog.String("err", err.Error()))
	}
}

// recordBillableOperation writes one idempotent ledger row for a
// genuinely-new governed operation (allowed / blocked /
// approval_required). It runs after dedup and after evaluate, so a
// retry storm sharing one idempotency key never reaches it and a
// malformed (invalid) or budget (limit_exceeded) outcome is excluded
// — those are not governed operations KIFF performed against the
// contract. The write is best-effort and off the
// decision path: a ledger failure is logged, never surfaced, and
// never changes the decision already returned to the caller.
// The operation id is the decision-record id (<proposal id>:<outcome>)
// so one recorded decision maps to exactly one billable row.
func (h *ProposalsHandler) recordBillableOperation(ctx context.Context, tenantID, proposalID, oc string) {
	if h.ledger == nil || !isGovernedOperation(oc) {
		return
	}
	recorded, err := h.ledger.Record(ctx, billing.UsageEvent{
		OperationID: proposalID + ":" + oc,
		TenantID:    tenantID,
		Outcome:     oc,
		OccurredAt:  h.now(),
	})
	if err != nil {
		// Conservative on failure: log and continue. Under-counting a
		// billable op is acceptable; turning billing into a runtime
		// failure is not.
		h.logger.WarnContext(ctx, "proposals: billable usage ledger write failed",
			slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return
	}
	if !recorded {
		// Already recorded (idempotent re-record). Not expected on the
		// decide path since retries dedupe upstream; harmless if it
		// happens.
		h.logger.DebugContext(ctx, "proposals: billable usage op already recorded",
			slog.String("tenant", tenantID))
	}
}

// isGovernedOperation reports whether an outcome is a billable governed
// operation: the runtime evaluated a proposal against the contract and
// returned a terminal governance answer. allowed / blocked /
// approval_required qualify; invalid (malformed) and limit_exceeded
// (budget rejection before evaluation) do not.
func isGovernedOperation(oc string) bool {
	switch outcome(oc) {
	case outcomeAllowed, outcomeBlocked, outcomeApprovalRequired:
		return true
	default:
		return false
	}
}

// heldEffectRead bounds the profile read a held call's alert makes: past
// it, the alert goes without MoneyOut and CanWait rather than waiting.
const heldEffectRead = 500 * time.Millisecond

// heldEffect is the held call's operation's effect when approved, from the
// tool's owner-approved profile, and whether KIFF knows it: not when there's
// no profile, no operation by that name, or the read fails
// (alert.Hold.MoneyOut, alert.Hold.CanWait).
func (h *ProposalsHandler) heldEffect(ctx context.Context, tenantID, action string) (string, bool) {
	if h.toolProfiles == nil || action == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, heldEffectRead)
	defer cancel()
	profiles, err := h.toolProfiles.ToolProfiles(ctx, tenantID)
	if err != nil {
		h.logger.WarnContext(ctx, "proposals: a held call's profile unread", slog.String("err", err.Error()))
		return "", false
	}
	raw, ok := profiles[action]
	if !ok {
		return "", false
	}
	p, err := profile.Parse(raw)
	if err != nil {
		return "", false
	}
	for _, op := range p.Operations {
		if op.Tool == action {
			return op.Effect, true
		}
	}
	return "", false
}

// moneyOut is whether a known effect moves money, nil when it isn't known.
func moneyOut(effect string, known bool) *bool {
	if !known {
		return nil
	}
	out := effect == profile.EffectMovesMoney
	return &out
}

// canWait is whether a held call with a known effect may wait: only a read
// (no effect) or an effect named here. Sending, moving money or an effect
// added later alerts at once. nil when the effect isn't known.
func canWait(effect string, known bool) *bool {
	if !known {
		return nil
	}
	out := false
	switch effect {
	case "", profile.EffectDraft, profile.EffectWrites, profile.EffectRecordsMoney:
		out = true
	}
	return &out
}

// maybeAlert fires a best-effort outbound alert on a
// consequential outcome (approval_required / blocked) for a freshly
// evaluated proposal. No-op when no alerter is wired or the outcome
// isn't consequential. The notifier delivers asynchronously, so this
// never blocks the response. It runs after applyMode, so an observe-
// mode decision still alerts — telling the human what the gate would
// have done. Only the fresh-evaluation path calls this; deduped
// retries (a retry storm) do not re-alert. The event carries only
// decision essentials — no request parameters — so redaction
// holds by construction.
func (h *ProposalsHandler) maybeAlert(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal, resp proposalResponse) {
	if h.alerter == nil {
		return
	}
	if resp.Outcome != string(outcomeBlocked) && resp.Outcome != string(outcomeApprovalRequired) &&
		resp.Outcome != string(outcomeOwnerRequired) {
		return
	}
	ev := alert.Event{
		TenantID:   id.Tenant,
		ProposalID: prop.ID,
		Action:     prop.ActionName,
		EntityID:   prop.EntityID,
		EntityType: prop.EntityType,
		Outcome:    resp.Outcome,
		Reasons:    resp.Reasons,
		DecidedAt:  h.now(),
	}
	if resp.Approval != nil {
		ev.Approval = &alert.Approval{
			ID:        resp.Approval.ID,
			Status:    resp.Approval.Status,
			ReviewURL: resp.Approval.ReviewURL,
		}
	}
	// A Card hold names the held call, where it is answered and when it
	// stops waiting, so a receiver can show it and count down.
	if x := resp.Exception; x != nil && resp.Outcome == string(outcomeOwnerRequired) {
		effect, known := h.heldEffect(ctx, id.Tenant, prop.ActionName)
		ev.Hold = &alert.Hold{ID: x.ID, Status: x.Status, ReviewURL: x.ReviewURL, ExpiresAt: x.ExpiresAt,
			MoneyOut: moneyOut(effect, known), CanWait: canWait(effect, known)}
		// A person's run asked for it: it goes to those who may answer it,
		// never to them.
		if asked := runGrantSubjectOf(ctx); asked != "" && h.runGrants != nil {
			if people, err := h.runGrants.Answerers(ctx, id.Tenant, asked); err != nil {
				h.logger.WarnContext(ctx, "proposals: who to tell about a hold", slog.String("err", err.Error()))
			} else {
				ev.Notify = &people
			}
		}
	}
	h.alerter.Notify(ev)
}

// applyMode stamps the tenant's governance posture onto a response.
// It always records the mode label so every reply carries
// it. In observe mode it additionally marks genuine governance
// outcomes (allowed / blocked / approval_required) advisory and
// fires the shadowed counter, so the dashboard can show what the
// gate *would* have done without the caller having gated on it.
// The outcome itself is never changed — observe is a contract
// annotation, not a different decision.
//
// invalid and limit_exceeded are deliberately NOT marked advisory
// or counted as shadowed: a malformed proposal or a budget ceiling
// is not a governed decision, and counting them would pollute the
// observe→enforce readiness signal (which is "what would enforcing
// have allowed/blocked/held").
// effectiveMode prefers the governing control's per-domain posture over
// the tenant-wide mode. An unset control mode (a control created before
// per-domain posture, or no active control declaring the action) falls
// back to the tenant mode passed in — so existing tenants keep their
// behavior until a domain gets its own posture.
func (h *ProposalsHandler) effectiveMode(ctx context.Context, tenantID, action string, fallback tenant.Mode) tenant.Mode {
	if h.controls == nil || action == "" {
		return fallback
	}
	ctrl, err := domain.FindActiveControl(ctx, h.controls, tenantID, action, controlEnvironmentFrom(ctx))
	if err != nil {
		return fallback
	}
	// Normalize first: controls activated before hold the legacy
	// "shadow" spelling, and must keep resolving to observe rather
	// than falling through to the tenant-wide fallback.
	switch domain.NormalizeControlMode(ctrl.Mode) {
	case domain.ControlModeObserve:
		return tenant.ModeObserve
	case domain.ControlModeEnforce:
		return tenant.ModeEnforce
	default:
		return fallback
	}
}

func (h *ProposalsHandler) applyMode(ctx context.Context, tenantID string, mode tenant.Mode, resp *proposalResponse) {
	resp.Mode = string(mode)
	if mode != tenant.ModeObserve {
		return
	}
	switch outcome(resp.Outcome) {
	case outcomeAllowed, outcomeBlocked, outcomeApprovalRequired, outcomeOwnerRequired:
		resp.Advisory = true
		h.incCounter(ctx, tenantID, usage.CloudCounterProposalsShadowed)
	default:
		// invalid / limit_exceeded: label the mode but keep binding.
	}
}

// maybeDedupe returns a recorded prior decision for prop.ID when
// one exists within the idempotency window, so a retried proposal
// collapses to one decision + N withholds instead of N
// decision/audit rows. ok=false means "no usable prior; evaluate
// fresh" — covering no prior, an expired prior, a malformed
// record, or a store-read blip. It never writes; the decision
// already exists from the first evaluation.
//
// Scope and known gaps:
//
//   - Atomicity is provided by the caller (decide) holding a
//     per-(tenant, key) advisory lock around the dedup + evaluate
//   - write when a ProposalLocker is wired. Without a
//     locker this is a read-then-write race — two concurrent
//     retries can both observe "no prior" and both write — so the
//     guarantee degrades to "sequential retries collapse to one
//     decision."
//   - Payload match. A dedup hit is gated on the idempotency key
//     AND a request fingerprint (entity + action + parameters).
//     Reusing one key for a *different* request returns
//     "invalid"/idempotency_key_reuse rather than silently echoing
//     the prior answer — the decision is the artifact we sell, so
//     a stale answer for a different request would be a footgun.
func (h *ProposalsHandler) maybeDedupe(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal, rt *kiffruntime.Runtime) (proposalResponse, bool) {
	if rt == nil || rt.Audit == nil {
		return proposalResponse{}, false
	}
	// The decision rows the gate writes carry TraceID = prop.ID
	// and Kind "proposal_decision" (see recordOutcome). Query by
	// the idempotency key to find a prior decision for the same
	// logical action.
	records, err := rt.Audit.Query(ctx, kiffaudit.Filter{
		TraceID: prop.ID,
		Kind:    kiffaudit.Kind("proposal_decision"),
	})
	if err != nil {
		// A query failure must not block the request: fall
		// through to a normal evaluation. Logging at warn keeps
		// the signal without turning a read blip into a 5xx.
		h.logger.WarnContext(ctx, "proposals: dedup lookup failed; evaluating fresh",
			slog.String("tenant", id.Tenant),
			slog.String("proposal_id", prop.ID),
			slog.String("err", err.Error()),
		)
		return proposalResponse{}, false
	}
	if len(records) == 0 {
		return proposalResponse{}, false
	}
	// Pick the most recent prior decision. The audit Query returns
	// chronological order, so the last element is newest.
	prior := records[len(records)-1]
	if h.now().Sub(prior.CreatedAt) > h.idemWindow {
		// Outside the window: the entity may have moved on, so
		// re-evaluate against current state. The re-evaluation
		// records a *distinct* decision row (decisionRecordID
		// appends a unique sequence), so there is no PK collision
		// and no swallowed insert error — the prior decision and
		// the fresh one both persist, at their respective times.
		return proposalResponse{}, false
	}

	outcomeStr, _ := prior.Data["outcome"].(string)
	if outcomeStr == "" {
		// A decision record with no recorded outcome is
		// malformed for dedup purposes; evaluate fresh rather
		// than echo an empty answer.
		return proposalResponse{}, false
	}
	if outcomeStr == string(outcomeOwnerRequired) || outcomeStr == string(outcomeApprovedNoRoom) {
		// Like approval_required below: a hold, not a settled decision.
		// The owner's answer must be visible on the next call, so it is
		// always re-evaluated (the mandates design amendment, "Retries").
		return proposalResponse{}, false
	}
	if outcomeStr == string(outcomeApprovalRequired) {
		// approval_required is not a settled decision: it is a
		// hold waiting for a human to grant or deny. Returning it
		// from the dedup cache would mask a grant that landed
		// after the first decision, so the same key could never
		// progress to "allowed". Always re-evaluate so the
		// round-trip can resolve the now-granted approval.
		return proposalResponse{}, false
	}

	// Payload-match guard (#4): if the prior decision recorded a
	// request fingerprint and it differs from this request's, the
	// caller reused the key for a different request. Returning the
	// prior answer would be wrong, so reject with a stable invalid
	// outcome instead of deduping. (Older rows without a recorded
	// fingerprint fall through and dedupe as before — backward
	// compatible.)
	if priorFP, _ := prior.Data["request_fingerprint"].(string); priorFP != "" {
		if priorFP != proposalFingerprint(ctx, prop) {
			h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsSubmitted)
			h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsInvalid)
			return proposalResponse{
				ProposalID: prop.ID,
				Outcome:    string(outcomeInvalid),
				Reasons:    []string{"idempotency_key_reuse"},
				Message:    "this idempotency key was already used for a different request (entity, action, parameters, or asserted run context differ); use a new key",
			}, true
		}
	}

	// The actor is part of whose decision this was. The fingerprint
	// leaves it out (it also derives approval ids, which must not shift
	// for existing callers), so compare the recorded actor directly: a
	// retry naming another agent must not receive a decision drawn on
	// the first agent's card. Rows recorded without an actor fall
	// through, as with the fingerprint.
	if priorActor, _ := prior.Data["proposed_actor_id"].(string); priorActor != "" && priorActor != prop.ActorID {
		h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsSubmitted)
		h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsInvalid)
		return proposalResponse{
			ProposalID: prop.ID,
			Outcome:    string(outcomeInvalid),
			Reasons:    []string{"idempotency_key_reuse"},
			Message:    "this idempotency key was already used for a proposal by a different actor; use a new key",
		}, true
	}

	reasons := stringSliceFromData(prior.Data["reasons"])

	// Dedup hit: count it and return the recorded decision without
	// re-evaluating or writing a new row. The outcome counters are
	// intentionally NOT re-incremented — only the first evaluation
	// counts toward allowed/blocked/etc.
	h.incCounter(ctx, id.Tenant, usage.CloudCounterProposalsDeduped)
	firstAt := prior.CreatedAt
	return proposalResponse{
		ProposalID:     prop.ID,
		Outcome:        outcomeStr,
		Reasons:        reasons,
		Message:        "duplicate of an earlier decision for this idempotency key; returning the recorded outcome",
		Deduplicated:   true,
		FirstDecidedAt: &firstAt,
	}, true
}

// stringSliceFromData coerces an audit data value (decoded from
// JSONB as []any) back into a []string for the reasons field.
// Tolerates the in-memory store's native []string too.
func stringSliceFromData(v any) []string {
	switch vv := v.(type) {
	case []string:
		return append([]string(nil), vv...)
	case []any:
		out := make([]string, 0, len(vv))
		for _, e := range vv {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// resolveApproval handles the approval_required path end-to-end.
// It is reached only when the contract requires approval
// and the proposal is otherwise valid. The cloud resolves the
// round-trip itself rather than depending on the framework
// threading an approval id through the proposal (v0.2.0 does not):
//
//   - approvalID is built from a base (the caller-supplied
//     approval_id when present, else a deterministic id derived
//     from the proposal id) plus a fingerprint of the request
//     parameters, so re-deciding with different parameters cannot
//     reuse an approval a human granted for the original
//     parameters — for caller-supplied ids as much as derived ones.
//   - If a matching approval is already GRANTED, return "allowed".
//   - If it is DENIED, return "blocked" (reason approval_denied).
//   - If it is PENDING, return "approval_required" with the handle.
//   - If none exists, auto-create a pending approval and return
//     the handle so the caller never has to create one itself.
//
// idempotent reports whether prop.ID is a caller-supplied stable
// key. The round-trip can only resolve to "allowed" if the caller
// can reproduce the approval id on re-decide — i.e. they supplied a
// stable proposal id or an explicit approval_id. Without either,
// the derived id changes every call (prop.ID is a fresh timestamp),
// so the message tells the caller to re-decide with the returned
// approval_id (a review point 2).
//
// The grant is authority the store holds, not something the caller
// asserts: a granted outcome requires an actual granted record
// matching this entity + action + parameter fingerprint, so
// passing an approval_id can never by itself make an action
// allowed, nor authorize a different payload than the one a human
// reviewed.
func (h *ProposalsHandler) resolveApproval(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal, spec domain.Spec, callerApprovalID, currentState string, idempotent bool, proposalActor kiffactor.Actor, contract kiffaction.ActionContract, rt *kiffruntime.Runtime, gateReasons ...string) proposalResponse {
	const reviewURL = "/dashboard/approvals"

	// gateReasons carry why a caller who did not ask for an approval
	// ended up here — today, a trusted-run-context design run-context hold. They are
	// prepended to every reason slice below so the decision record
	// says which gate produced the hold, not just that approval was
	// required. Empty for the ordinary contract-driven path.
	withGate := func(rs ...string) []string {
		if len(gateReasons) == 0 {
			return rs
		}
		return append(append([]string{}, gateReasons...), rs...)
	}

	callerSuppliedID := strings.TrimSpace(callerApprovalID)
	base := callerSuppliedID
	if base == "" {
		// Deterministic base from the proposal id.
		base = "apr_" + prop.ID
	}
	// Bind the request parameters into the approval id — for a
	// caller-supplied approval_id as much as a derived one.
	// A re-decide that changes any param yields a
	// different id, so a human's grant of the original request can
	// never authorize the changed one. The handle returns this
	// bound id; a caller may re-decide with either their original
	// base (we re-append the fingerprint) or the returned bound id
	// (already suffixed — the HasSuffix guard keeps it idempotent
	// so we don't double-append).
	fpSuffix := "_" + proposalFingerprint(ctx, prop)[:16]
	approvalID := base
	if !strings.HasSuffix(base, fpSuffix) {
		approvalID = base + fpSuffix
	}

	// roundTripStable is true when the caller can reproduce the
	// approval id on a later re-decide: either they passed an
	// explicit approval_id, or prop.ID is a stable idempotency key
	// so the derived id is reproducible. When false, re-deciding the
	// "same" proposal mints a new id, so the caller must pass back
	// the approval_id from this response to complete the trip.
	roundTripStable := idempotent || callerSuppliedID != ""

	pendingMessage := "this action requires human approval; a pending approval was created — grant it in the dashboard, then re-decide with the same proposal"
	if !roundTripStable {
		pendingMessage = "this action requires human approval; a pending approval was created — grant it in the dashboard, then re-decide passing this approval_id (or send a stable proposal id so the round-trip is reproducible)"
	}

	// Defensive: the resolver always wires an approval store, but
	// the framework runtime tolerates a nil one. Fall back to the
	// legacy "create it yourself" answer rather than panicking.
	if rt.Approvals == nil {
		reasons := withGate("approval_required")
		h.recordOutcome(ctx, id, prop, outcomeApprovalRequired, reasons)
		return proposalResponse{
			ProposalID: prop.ID,
			Outcome:    string(outcomeApprovalRequired),
			Reasons:    reasons,
			Message:    "this action requires human approval; request one via /v1/entities/{id}/approvals before executing",
		}
	}

	existing, found, err := rt.Approvals.Get(ctx, approvalID)
	if err != nil {
		// Read blip: don't crash and don't reset state. Treat as
		// "still pending" and return the handle so the caller can
		// re-decide; the dashboard list is the source of truth.
		h.logger.WarnContext(ctx, "proposals: approval lookup failed; returning pending",
			slog.String("tenant", id.Tenant),
			slog.String("approval_id", approvalID),
			slog.String("err", err.Error()),
		)
		reasons := withGate("approval_required")
		h.recordOutcome(ctx, id, prop, outcomeApprovalRequired, reasons)
		return proposalResponse{
			ProposalID: prop.ID,
			Outcome:    string(outcomeApprovalRequired),
			Reasons:    reasons,
			Message:    "this action requires human approval; review it in the dashboard, then re-decide",
			Approval:   &approvalHandle{ID: approvalID, Status: string(kiffapproval.StatusPending), ReviewURL: reviewURL},
		}
	}

	if found {
		switch existing.Status {
		case kiffapproval.StatusGranted:
			if existing.EntityID == prop.EntityID && existing.ActionName == contract.Name {
				// A granted approval satisfies the contract; it does
				// not replenish authority. A human saying yes to one
				// action cannot spend past a limit the same human set.
				if refusal, refused := h.mandateRefusal(ctx, id, prop, spec, currentState); refused {
					return refusal
				}
				h.recordOutcome(ctx, id, prop, outcomeAllowed, nil)
				return proposalResponse{
					ProposalID: prop.ID,
					Outcome:    string(outcomeAllowed),
					Message:    "human approval granted; action contract satisfied; safe to execute",
					Approval:   &approvalHandle{ID: approvalID, Status: string(kiffapproval.StatusGranted), ReviewURL: reviewURL},
				}
			}
			// A granted approval that doesn't match this entity +
			// action must not authorize the action. Treat as a
			// blocked governance answer rather than silently
			// allowing.
			reasons := withGate("approval_mismatch")
			h.recordOutcome(ctx, id, prop, outcomeBlocked, reasons)
			return proposalResponse{
				ProposalID: prop.ID,
				Outcome:    string(outcomeBlocked),
				Reasons:    reasons,
				Message:    "the referenced approval does not match this entity and action",
			}
		case kiffapproval.StatusDenied:
			reasons := withGate("approval_denied")
			h.recordOutcome(ctx, id, prop, outcomeBlocked, reasons)
			return proposalResponse{
				ProposalID: prop.ID,
				Outcome:    string(outcomeBlocked),
				Reasons:    reasons,
				Message:    "a human denied this action; do not execute",
				Approval:   &approvalHandle{ID: approvalID, Status: string(kiffapproval.StatusDenied), ReviewURL: reviewURL},
			}
		default: // pending
			reasons := withGate("approval_required")
			h.recordOutcome(ctx, id, prop, outcomeApprovalRequired, reasons)
			return proposalResponse{
				ProposalID: prop.ID,
				Outcome:    string(outcomeApprovalRequired),
				Reasons:    reasons,
				Message:    "this action is waiting on a human reviewer; grant it in the dashboard, then re-decide",
				Approval:   &approvalHandle{ID: approvalID, Status: string(kiffapproval.StatusPending), ReviewURL: reviewURL},
			}
		}
	}

	// No approval exists yet: auto-create a pending one so the
	// caller doesn't have to. RequestApproval validates the
	// contract requires approval — true on the contract-driven branch
	// by the action's own requirement, and true on a trusted-run-context design
	// run-context hold because the gate hands in a contract marked
	// required for that proposal — and stamps RequestedBy from the actor.
	//
	// Concurrency: two concurrent first-time calls can both Get
	// (not found) and both RequestApproval. The Postgres approval
	// store saves via ON CONFLICT (id) DO UPDATE, so they collapse
	// to one row; the in-memory store upserts by id likewise.
	// Correctness here rests on that upsert.
	reasons := withGate("approval_required")
	actionCtx, acErr := prop.ActionContext(currentState, proposalActor)
	if acErr == nil {
		if _, reqErr := rt.RequestApproval(ctx, approvalID, actionCtx, contract,
			approvalReason(ctx, prop, gateReasons)); reqErr != nil {
			h.logger.WarnContext(ctx, "proposals: auto-create approval failed; returning pending handle",
				slog.String("tenant", id.Tenant),
				slog.String("approval_id", approvalID),
				slog.String("err", reqErr.Error()),
			)
		}
	}
	h.recordOutcome(ctx, id, prop, outcomeApprovalRequired, reasons)
	return proposalResponse{
		ProposalID: prop.ID,
		Outcome:    string(outcomeApprovalRequired),
		Reasons:    reasons,
		Message:    pendingMessage,
		Approval:   &approvalHandle{ID: approvalID, Status: string(kiffapproval.StatusPending), ReviewURL: reviewURL},
	}
}

// proposalFingerprint is a stable hash of the parts of a proposal
// that define "the same request" for idempotency: entity, type,
// action, and parameters. Reusing an idempotency key with any of
// these changed yields a different fingerprint, which the dedup
// path uses to reject key reuse (#4 in the review) instead of
// returning a stale decision, and which the approval round-trip
// bakes into the derived approval id so a grant is bound to the
// parameters the human saw. json.Marshal sorts map keys, so the
// parameters serialization is deterministic for string-keyed maps.
// On the rare marshal error we fall back to a non-empty sentinel
// so two un-fingerprintable requests never compare equal by
// accident.
// approvalReason is the text a human sees on the approval record.
//
// The reviewer's decision is the control, so what they read has to be
// the trusted account of why the hold exists — not only the model's.
// prop.ReasoningSummary is written by the agent being governed; used
// alone (as it was) a reviewer could be asked to clear an
// untrusted-run hold while reading a perfectly reasonable explanation
// that never mentions it, which turns the human step into theatre.
//
// So the gate's own reason leads, with the asserted run context beside
// it, and the agent's explanation follows clearly labelled as a claim.
// Order matters: a surface that truncates keeps the trusted half.
//
// A string is the v0 shape because the framework's RequestApproval
// takes one. Structured fields on the approval record are the better
// answer and need a framework change, which lands as its own public PR
// rather than being smuggled in here (#kiff/kiff).
func approvalReason(ctx context.Context, p kiffproposal.ActionProposal, gateReasons []string) string {
	if len(gateReasons) == 0 {
		return p.ReasoningSummary
	}
	var b strings.Builder
	b.WriteString("KIFF held this: ")
	b.WriteString(strings.Join(gateReasons, ", "))
	if rc, ok := runcontext.FromContext(ctx); ok {
		if rc.UntrustedInput {
			b.WriteString(" (run reported untrusted input")
		} else {
			b.WriteString(" (run reported no untrusted input")
		}
		if rc.RunID != "" {
			b.WriteString("; run_id=")
			b.WriteString(rc.RunID)
		}
		b.WriteString(")")
	} else {
		b.WriteString(" (no run context was asserted)")
	}
	if s := strings.TrimSpace(p.ReasoningSummary); s != "" {
		b.WriteString(" — agent's stated reason: ")
		b.WriteString(s)
	}
	return b.String()
}

func proposalFingerprint(ctx context.Context, p kiffproposal.ActionProposal) string {
	paramsJSON, err := json.Marshal(p.Parameters)
	if err != nil {
		paramsJSON = []byte("\x00unmarshalable")
	}
	h := sha256.New()
	h.Write([]byte(p.EntityID))
	h.Write([]byte{0})
	h.Write([]byte(p.EntityType))
	h.Write([]byte{0})
	h.Write([]byte(p.ActionName))
	h.Write([]byte{0})
	h.Write(paramsJSON)
	// the trusted-run-context design: the asserted run context is part of what is being asked,
	// so it belongs in the identity of the request.
	//
	// Without this, run context is invisible to both things built on the
	// fingerprint. A caller could get `allowed` under an asserted-clean
	// context and then replay the same proposal id under a tainted one:
	// maybeDedupe matches on the fingerprint and returns the recorded
	// `allowed` before the gate ever runs. The same omission lets an
	// approval granted in one run satisfy an otherwise identical proposal
	// in another, which is precisely the "fresh approval" the RFC claimed
	// and did not have.
	//
	// Absent context writes nothing, so a proposal that asserts none
	// hashes byte-identically to before this change. That keeps every
	// idempotency key and approval id minted before the deploy matching
	// afterwards — a fingerprint change for the status-quo path would
	// invalidate in-flight approvals on the way out.
	// A trial's call is decided on other Cards than the agent's own, so a
	// proposal id can't move between the two (kiff/tafs#173). None writes
	// nothing, as for run context.
	if draft := trialDraftFrom(ctx); draft != "" {
		h.Write([]byte{0})
		h.Write([]byte("trial_draft"))
		h.Write([]byte{0})
		h.Write([]byte(draft))
	}
	if rc, ok := runcontext.FromContext(ctx); ok {
		h.Write([]byte{0})
		h.Write([]byte("run_context"))
		h.Write([]byte{0})
		if rc.UntrustedInput {
			h.Write([]byte("untrusted"))
		} else {
			h.Write([]byte("clean"))
		}
		h.Write([]byte{0})
		h.Write([]byte(rc.RunID))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// evaluate runs the contract validation against the proposal and
// produces a proposalResponse. Side-effect: records the proposal
// + decision via the runtime so the audit trail covers both the
// asked and the answered.
//
// id is the authenticated identity. id.Roles is the **trusted**
// source of authority for the permission check; the request body
// has no influence here. id.Tenant routes the runtime, id.Subject
// is recorded in the audit trail as the principal that made the
// call (distinct from prop.ActorID, which is attribution metadata
// for which agent/tool inside the customer's system did the work).
func (h *ProposalsHandler) evaluate(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal, callerApprovalID string, idempotent bool, rt *kiffruntime.Runtime, spec domain.Spec) proposalResponse {
	tenantID := id.Tenant
	// The gateway's read of the call's unit is a fact about the call, not
	// one of its parameters.
	observedUnit, prop := takeObservedUnit(spec, prop)
	amountUnverified, prop := takeAmountUnverified(spec, prop)
	ownerAsk, prop := takeOwnerAsk(spec, prop)
	ctx = withOwnerAsk(ctx, ownerAsk)
	// Record the proposal regardless of outcome so the audit
	// trail captures what the caller asked us to decide on. A
	// proposal that's later blocked is still evidence the agent
	// considered the action.
	if rt.Decisions != nil {
		if err := rt.RecordActionProposal(ctx, prop); err != nil && !alreadyRecorded(err) {
			h.logger.WarnContext(ctx, "proposals: record proposal failed (continuing)",
				slog.String("tenant", tenantID),
				slog.String("proposal_id", prop.ID),
				slog.String("err", err.Error()),
			)
		}
	}

	if rt.Actions == nil {
		h.recordOutcome(ctx, id, prop, outcomeInvalid, []string{"actions_unavailable"})
		return proposalResponse{
			ProposalID: prop.ID,
			Outcome:    string(outcomeInvalid),
			Reasons:    []string{"actions_unavailable"},
			Message:    "tenant runtime has no action catalog",
		}
	}
	contract, ok := rt.Actions.Get(prop.ActionName)
	if !ok {
		reasons := []string{"unknown_action"}
		h.recordOutcome(ctx, id, prop, outcomeInvalid, reasons)
		return proposalResponse{
			ProposalID: prop.ID,
			Outcome:    string(outcomeInvalid),
			Reasons:    reasons,
			Message:    fmt.Sprintf("action %q is not declared by the tenant's domain", prop.ActionName),
		}
	}

	currentState := ""
	if rt.States != nil {
		st, ok, err := rt.States.Current(ctx, prop.EntityID)
		if err != nil {
			// State store failures must NOT degrade silently into
			// validation. A blocked outcome there would hide an
			// infrastructure problem behind a governance denial,
			// which is exactly the wrong default for a system
			// whose product is "trustworthy answers". Surface a
			// 502 so the caller knows to retry rather than treat
			// it as a no.
			h.logger.ErrorContext(ctx, "proposals: lookup current state failed",
				slog.String("tenant", tenantID),
				slog.String("entity_id", prop.EntityID),
				slog.String("err", err.Error()),
			)
			h.recordOutcome(ctx, id, prop, outcomeInvalid, []string{"state_store_unavailable"})
			return proposalResponse{
				ProposalID: prop.ID,
				Outcome:    string(outcomeInvalid),
				Reasons:    []string{"state_store_unavailable"},
				Message:    "could not look up entity state; retry shortly",
			}
		} else if ok {
			currentState = st.Value
		}
	}

	proposalActor := kiffactor.Actor{
		ID: prop.ActorID,
		// Attribution only. Since the authority-resolution design the permission check resolves
		// authority from the trusted identity on the context
		// (domain.ContextPolicy reads auth.IdentityFrom, not actor.Roles),
		// so these roles no longer gate anything — they are retained as
		// audit/display metadata and as defense-in-depth.
		Roles: append([]string(nil), id.Roles...),
	}

	// Make the reconstructed state-at-decision and the action's
	// allowed states available to recordOutcome so the decision
	// record carries them as explicit fields (the authenticated-surface design guarantee
	// #1). Threaded via context to avoid changing
	// recordOutcome's many call sites (mirrors the control-environment
	// pattern). Set here, after the entity state is resolved and the
	// contract is known, so every governance outcome below (allowed,
	// blocked-on-state, approval-required) records it; the earlier
	// pre-state outcomes (actions_unavailable, unknown_action) record
	// no state, which is honest.
	ctx = withDecisionState(ctx, currentState, contract.AllowedStates)
	// And what the call moves and is on (kiff/tafs#167), for the same
	// outcomes.
	ctx = withCallFacts(ctx, spec, prop, observedUnit)
	// A call in another unit than its Card counts is blocked, recorded,
	// and never counted.
	if refusal, refused := h.unitRefusal(ctx, id, prop, spec, observedUnit); refused {
		return refusal
	}
	// So is one whose amount the gateway couldn't read.
	if refusal, refused := h.amountRefusal(ctx, id, prop, amountUnverified); refused {
		return refusal
	}

	err := rt.ValidateActionProposal(ctx, prop, currentState, proposalActor, contract)
	if err == nil {
		// the trusted-run-context design: the action may declare that its answer depends on
		// what the harness reported about this run. Runs before the
		// mandate draw so a proposal on its way to a human hold does
		// not spend cumulative authority first.
		if refusal, refused := h.runContextRefusal(ctx, id, prop, spec, callerApprovalID,
			currentState, idempotent, proposalActor, contract, rt); refused {
			return refusal
		}
		// Last check, and the only one whose answer depends on other
		// requests: has this subject any authority left today.
		if refusal, refused := h.mandateRefusal(ctx, id, prop, spec, currentState); refused {
			return refusal
		}
		h.recordOutcome(ctx, id, prop, outcomeAllowed, nil)
		return proposalResponse{
			ProposalID: prop.ID,
			Outcome:    string(outcomeAllowed),
			Message:    "action contract satisfied; safe to execute",
		}
	}

	// The approval-required path is resolved cloud-side: the
	// round-trip, the parameter-bound approval id, and auto-creating a
	// pending approval all live in resolveApproval, so keep it out of
	// the generic classifier.
	if errors.Is(err, kiffaction.ErrApprovalRequired) {
		return h.resolveApproval(ctx, id, prop, spec, callerApprovalID, currentState, idempotent, proposalActor, contract, rt)
	}

	// Every other validation failure is classified with the framework's
	// outcome vocabulary (Slice 3) instead of a hand-rolled
	// error→string switch. outcome.Classify is the single source of
	// truth for the taxonomy: a missing/invalid parameter is `invalid`
	// (a malformed proposal, which the cloud already treats as a 400,
	// non-billable, non-alerting request — the correct handling for a
	// broken request rather than a governance "no"); state/permission
	// failures are `blocked` (a governance denial); any unrecognized
	// error fails safe to `blocked`. The cloud outcome constants are
	// bound to the framework's, so outcome(oc) is exact.
	oc, reason := kiffoutcome.Classify(err)
	cloudOutcome := outcome(oc)
	reasons := []string{string(reason)}
	if reason == kiffoutcome.ReasonError {
		// Unrecognized framework error: preserve the operational
		// signal the old default branch emitted so an unmapped error
		// stays visible in logs rather than silently classified.
		h.logger.WarnContext(ctx, "proposals: unclassified validation error",
			slog.String("tenant", tenantID),
			slog.String("proposal_id", prop.ID),
			slog.String("err", err.Error()),
		)
	}
	h.recordOutcome(ctx, id, prop, cloudOutcome, reasons)
	return proposalResponse{
		ProposalID: prop.ID,
		Outcome:    string(cloudOutcome),
		Reasons:    reasons,
		Message:    classifyMessage(reason),
	}
}

// classifyMessage returns the human-readable message for a classified
// validation reason. Preserves the curated wording the hand-rolled
// switch used for the reasons the cloud already surfaced, and adds
// wording for the reasons outcome.Classify distinguishes that the old
// switch lumped into a generic "validation failed".
func classifyMessage(reason kiffoutcome.Reason) string {
	switch reason {
	case kiffoutcome.ReasonStateNotAllowed:
		return "entity is not in a state that permits this action"
	case kiffoutcome.ReasonMissingParameter:
		return "proposal is missing a parameter the contract requires"
	case kiffoutcome.ReasonInvalidParameter:
		return "a proposal parameter is invalid for the contract"
	case kiffoutcome.ReasonPermissionDenied:
		return "actor does not hold a permission the contract requires"
	case kiffoutcome.ReasonApprovalPolicy:
		return "the approval policy could not decide; the action is stopped"
	case kiffoutcome.ReasonExecutorMissing:
		return "the action has no executor configured"
	case kiffoutcome.ReasonInvalidContract:
		return "the action contract is invalid"
	default:
		return "the proposal failed contract validation"
	}
}

// normalizeProposal copies the request fields into a
// proposal.ActionProposal, defaulting id and created_at when
// the caller left them empty. Trims whitespace on string fields
// so accidental " action " inputs don't pretend to be valid.
//
// Idempotency key precedence: an explicit body `id` wins,
// then the Idempotency-Key header, then a generated timestamp id.
// idempotent reports whether the id came from the caller (body or
// header) — only caller-supplied ids are stable enough to dedupe;
// a generated timestamp id is unique per call by construction, so
// the dedup lookup is skipped for it.
func (h *ProposalsHandler) normalizeProposal(req proposalRequest, headerKey string) (prop kiffproposal.ActionProposal, idempotent bool) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		id = strings.TrimSpace(headerKey)
	}
	idempotent = id != ""

	prop = kiffproposal.ActionProposal{
		ID:               id,
		EntityID:         strings.TrimSpace(req.EntityID),
		EntityType:       strings.TrimSpace(req.EntityType),
		ActionName:       strings.TrimSpace(req.ActionName),
		ActorID:          strings.TrimSpace(req.ActorID),
		Parameters:       req.Parameters,
		ReasoningSummary: req.ReasoningSummary,
		Confidence:       req.Confidence,
		CreatedAt:        req.CreatedAt,
	}
	if prop.ID == "" {
		// No caller-supplied idempotency key. Generate a
		// timestamp id so the call still produces a valid,
		// unique decision id. Callers that want retries to
		// dedupe must pass their own `id` (a tool-call id, an
		// orchestration trace id, etc.) or the Idempotency-Key
		// header so duplicate decisions collapse in the
		// decision/audit stores within the idempotency window.
		prop.ID = "prop_" + h.now().UTC().Format("20060102T150405.000000000")
	}
	if prop.CreatedAt.IsZero() {
		prop.CreatedAt = h.now().UTC()
	}
	if prop.Parameters == nil {
		prop.Parameters = map[string]any{}
	}
	return prop, idempotent
}

// (deprecated) anyifyStringSlice was used to thread role strings
// through Parameters when roles still came from the body. The
// trusted-roles refactor removed that path; the helper is gone.

// basicProposalShapeIssues runs a fast pre-runtime check for
// the required fields. Returns (true, reasons) when the
// proposal is malformed; the handler short-circuits to
// "invalid" without going to the runtime.
func basicProposalShapeIssues(p kiffproposal.ActionProposal) (bool, []string) {
	var reasons []string
	if p.EntityID == "" {
		reasons = append(reasons, "missing_entity_id")
	}
	if p.EntityType == "" {
		reasons = append(reasons, "missing_entity_type")
	}
	if p.ActionName == "" {
		reasons = append(reasons, "missing_action_name")
	}
	if p.ActorID == "" {
		reasons = append(reasons, "missing_actor_id")
	}
	return len(reasons) > 0, reasons
}

// decisionRecordSeq guarantees a unique suffix per recorded
// decision/rejection row, independent of the (test-pinnable)
// clock. See decisionRecordID.
var decisionRecordSeq atomic.Uint64

// decisionRecordID builds the store id for a recorded decision.
// It appends a process-unique sequence so a post-window
// re-evaluation records a *distinct* row rather than
// colliding on "prop.ID:outcome" and relying on the audit store's
// plain INSERT to fail-and-swallow. The canonical
// proposal id stays in TraceID and data.proposal_id, so the dedup
// query (by TraceID) and the GET /v1/proposals reader (which reads
// data.proposal_id) are unaffected by the suffix.
// alreadyRecorded: the decision store already holds this proposal. A held
// call is decided again once the owner answers, and an approval is decided
// again when it's used; the proposal is the same one, recorded the first
// time, so its second record is no failure.
func alreadyRecorded(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "kiff_decisions_pkey"
}

func decisionRecordID(propID string, oc outcome) string {
	return propID + ":" + string(oc) + ":" + strconv.FormatUint(decisionRecordSeq.Add(1), 36)
}

// recordOutcome appends the outcome to the cloud's decision
// store + audit log so the request leaves a reviewable trail.
// Never blocks the response path on a recording failure.
//
// id is the authenticated identity. The audit data field captures
// id.Subject and id.APIKey alongside the caller-supplied
// prop.ActorID, so a reviewer can distinguish the trusted
// credential that crossed the boundary (id.Subject + the API key
// id, server-controlled) from the attribution label the caller
// chose for the agent (prop.ActorID, untrusted).
func (h *ProposalsHandler) recordOutcome(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal, oc outcome, reasons []string) {
	// Cloud counter for this outcome. Fires regardless of whether
	// the runtime is reachable so we count even when the rest of
	// the recording path can't open the per-tenant runtime.
	h.incCounter(ctx, id.Tenant, counterForOutcome(oc))

	rt, err := h.runtimeFor(ctx, id.Tenant)
	if err != nil || rt == nil {
		return
	}
	// Append a decision-store entry capturing the answered side.
	if rt.Decisions != nil {
		recID := decisionRecordID(prop.ID, oc)
		dec := kiffdecision.Decision{
			ID:               recID,
			EntityID:         prop.EntityID,
			EntityType:       prop.EntityType,
			Kind:             kiffdecision.KindActionProposal,
			ProposedAction:   prop.ActionName,
			ReasoningSummary: prop.ReasoningSummary,
			Confidence:       prop.Confidence,
			ActorID:          prop.ActorID,
			CreatedAt:        h.now(),
		}
		if err := dec.Validate(); err == nil {
			_ = rt.Decisions.Append(ctx, dec)
		}
	}
	// And drop a richer record in the audit store so the
	// timeline + receipt path can pick it up.
	if rt.Audit != nil {
		controlID, environment, controlVersion := h.resolveControlIdentity(ctx, id.Tenant, prop.ActionName)
		data := attributionData(ctx, id, prop, oc, reasons, controlID, environment, controlVersion)
		// Stamp the authoritative domain_id at write
		// time so the decision — and the receipt built from it — carry
		// the domain that produced them, rather than the web re-deriving
		// it from domains[0]. Resolved domain selector first
		// (explicit request / domain-scoped key), else the active
		// control's domain slug for the default-domain path. Omitted
		// when neither is known.
		if did := resolvedDomainID(ctx); did != "" {
			data["domain_id"] = did
		}
		// State-at-decision + allowed states (the authenticated-surface design
		// guarantee #1). Present only for outcomes recorded after the
		// entity state was resolved; absent (and omitted) otherwise.
		if st, allowed := decisionStateFrom(ctx); st != "" || len(allowed) > 0 {
			if st != "" {
				data["state_at_decision"] = st
			}
			if len(allowed) > 0 {
				data["allowed_states"] = allowed
			}
		}
		for k, v := range callFactsFrom(ctx) {
			data[k] = v
		}
		// What the call is about, in the taf's words (taf_words.go): a
		// business reference, kept for every decided call so the owner's
		// Record can be searched by it. Beside the decision, never in its
		// record, which a receipt's chain hashes: it can be erased and the
		// receipt still verifies. Best effort, as the record is.
		if about := tafWordsFrom(ctx).about; about != "" && h.callAbout != nil {
			if err := h.callAbout.Keep(ctx, id.Tenant, prop.ID, about, h.now()); err != nil {
				h.logger.WarnContext(ctx, "proposals: what the call is about not kept", slog.String("tenant", id.Tenant),
					slog.String("proposal_id", prop.ID), slog.String("err", err.Error()))
			}
		}
		_ = rt.Audit.Append(ctx, kiffaudit.Record{
			ID:         decisionRecordID(prop.ID, oc),
			Kind:       kiffaudit.Kind("proposal_decision"),
			EntityID:   prop.EntityID,
			EntityType: prop.EntityType,
			TraceID:    prop.ID,
			ActorID:    prop.ActorID,
			CreatedAt:  h.now(),
			Message:    "proposal decision recorded",
			Data:       data,
		})
	}
	// Push a live event so the dashboard reflects this decision without
	// a reload. Best-effort: nil broker / full buffer is a no-op.
	h.events.Publish(id.Tenant, stream.Event{
		Type: "decision",
		Data: map[string]any{
			"outcome":     string(oc),
			"action_name": prop.ActionName,
			"entity_id":   prop.EntityID,
			"reasons":     reasons,
		},
	})
}

// controlEnvKey is the context key carrying the decide request's
// environment so the control-resolution path can scope to the matching
// active control without threading the value through evaluate's many
// recordOutcome call sites. Request-scoped only.
type controlEnvKey struct{}

// resolveRequestEnvironment is the environment a request is decided in,
// as domain.ResolveRequestDomain is for domains. A key bound to
// an environment decides in it: the request may repeat it, and one
// naming a different environment does not match. Otherwise the request's
// own environment, possibly empty, stands.
func resolveRequestEnvironment(id auth.Identity, requested string) (string, bool) {
	if !id.APIKey || id.Environment == "" {
		return requested, true
	}
	if requested != "" && !strings.EqualFold(requested, id.Environment) {
		return id.Environment, false
	}
	return id.Environment, true
}

// withControlEnvironment returns ctx carrying env for control resolution.
// An empty env is stored as-is and read back as "" (single-active path).
func withControlEnvironment(ctx context.Context, env string) context.Context {
	return context.WithValue(ctx, controlEnvKey{}, env)
}

// controlEnvironmentFrom returns the decide request's environment, or ""
// when none was provided (the default single-active-control resolution).
func controlEnvironmentFrom(ctx context.Context) string {
	env, _ := ctx.Value(controlEnvKey{}).(string)
	return env
}

// decisionStateKey carries the entity's reconstructed lifecycle state
// at decision time and the action's allowed states, so recordOutcome
// can stamp them onto the decision record without threading them
// through evaluate's many call sites (mirrors controlEnvKey).
// These make the "decided against reconstructed state" guarantee
// (the authenticated-surface design guarantee #1) legible: e.g. a retry refused because the
// entity was already PAID, shown against the action's allowed states.
// Request-scoped only.
type decisionStateKey struct{}

type decisionStateDetail struct {
	stateAtDecision string
	allowedStates   []string
}

// withDecisionState returns ctx carrying the state-at-decision and the
// action's allowed states for the decision about to be recorded. A
// copy of allowed is taken so a later mutation of the contract slice
// cannot change what was recorded.
func withDecisionState(ctx context.Context, state string, allowed []string) context.Context {
	return context.WithValue(ctx, decisionStateKey{}, decisionStateDetail{
		stateAtDecision: state,
		allowedStates:   append([]string(nil), allowed...),
	})
}

// decisionStateFrom returns the recorded state-at-decision and allowed
// states, or zero values when none were threaded (outcomes recorded
// before state resolution, e.g. unknown_action / actions_unavailable).
func decisionStateFrom(ctx context.Context) (string, []string) {
	d, _ := ctx.Value(decisionStateKey{}).(decisionStateDetail)
	return d.stateAtDecision, d.allowedStates
}

// usageDomainKey carries the tenant's server-validated active domain
// name so incCounter can attribute cloud proposal counters per domain.
// The domain is NOT caller-supplied — it is the
// tenant's active control's domain — so using it as the usage
// entity_type dimension keeps cardinality bounded (a tenant has 0-or-1
// active domain today), preserving the cardinality-DoS protection the
// fixed CloudEntityProposal label provided. Empty (no active domain /
// no control store) falls back to the fixed label.
type usageDomainKey struct{}

func withUsageDomain(ctx context.Context, domainName string) context.Context {
	return context.WithValue(ctx, usageDomainKey{}, domainName)
}

func usageDomainFrom(ctx context.Context) string {
	d, _ := ctx.Value(usageDomainKey{}).(string)
	return d
}

// runtimeDomainKey carries the domain_id the request resolved to,
// so every runtime lookup in the decide path —
// the gate, recordOutcome, the limit-exceeded recorder — addresses
// the same domain's runtime. Set once at the top of decide after the
// key binding + request selector are resolved. Empty means the
// tenant's default domain (the resolver loads it). Request-scoped.
type runtimeDomainKey struct{}

func withRuntimeDomain(ctx context.Context, domainID string) context.Context {
	return context.WithValue(ctx, runtimeDomainKey{}, domainID)
}

func runtimeDomainFrom(ctx context.Context) string {
	d, _ := ctx.Value(runtimeDomainKey{}).(string)
	return d
}

// decidingDomainKey carries the immutable domain_id of the domain whose
// runtime answered the request. Request-scoped; stamping only.
type decidingDomainKey struct{}

func withDecidingDomain(ctx context.Context, domainID string) context.Context {
	return context.WithValue(ctx, decidingDomainKey{}, domainID)
}

func decidingDomainFrom(ctx context.Context) string {
	d, _ := ctx.Value(decidingDomainKey{}).(string)
	return d
}

// resolvedDomainID returns the authoritative domain_id for the request,
// for stamping onto the decision/receipt. The domain
// whose runtime answered wins (its immutable id); then the
// resolved domain selector (an explicit request domain or a
// domain-scoped key, already a slug); otherwise the active control's
// domain name is slugified to the default domain's id. Empty when
// neither is known (no selector and no active control).
func resolvedDomainID(ctx context.Context) string {
	if d := decidingDomainFrom(ctx); d != "" {
		return d
	}
	if d := runtimeDomainFrom(ctx); d != "" {
		return d
	}
	return domain.Slugify(usageDomainFrom(ctx))
}

// runtimeFor resolves the tenant runtime for the domain the request
// routed to (runtimeDomainFrom), defaulting to the tenant's default
// domain when none was threaded. All decide-path runtime lookups go
// through it so the gate and the recording paths share one domain.
func (h *ProposalsHandler) runtimeFor(ctx context.Context, tenantID string) (*kiffruntime.Runtime, error) {
	return h.tenants.ForDomain(ctx, tenantID, runtimeDomainFrom(ctx))
}

// activeDomainFor returns the tenant's active domain name (the active
// control's Domain), or "" when no control store is wired or no active
// control exists. Server-controlled, so safe as a bounded usage
// dimension. One List call; resolved once per decide and threaded via
// withUsageDomain.
func (h *ProposalsHandler) activeDomainFor(ctx context.Context, tenantID string) string {
	if h.controls == nil {
		return ""
	}
	list, err := domain.ActiveControls(ctx, h.controls, tenantID)
	if err != nil {
		return ""
	}
	for _, c := range list {
		if strings.TrimSpace(c.Domain) != "" {
			return c.Domain
		}
	}
	return ""
}

// resolveControlIdentity returns the (control_id, environment, version)
// of the active control that governs an action for a tenant, so the
// decision can be scoped to its control and stamped with the governing
// control version. Best-effort: a nil store, a
// lookup error, or no active control for the action yields zero values,
// and the decision records as "not yet attributable to a control"
// rather than failing. Resolution uses the single-active-control
// back-compat path (empty environment); multi-environment resolution
// from the connection identity is the deferred follow-up.
func (h *ProposalsHandler) resolveControlIdentity(ctx context.Context, tenantID, action string) (string, string, int) {
	if h.controls == nil {
		return "", "", 0
	}
	env := controlEnvironmentFrom(ctx)
	// Prefer the active control matching the request's environment.
	// Fall back to the single-active resolution (env "")
	// when none matches, so threading an environment can never REGRESS
	// stamping for today's one-active-control-per-tenant model — it only
	// sharpens resolution once per-environment active controls exist.
	c, err := domain.FindActiveControl(ctx, h.controls, tenantID, action, env)
	if err != nil && env != "" {
		c, err = domain.FindActiveControl(ctx, h.controls, tenantID, action, "")
	}
	if err != nil {
		return "", "", 0
	}
	return c.ID, c.Environment, c.Version
}

// recordRejection is the variant for shape-malformed proposals
// where the runtime hasn't been resolved yet. The dropped
// payload is intentionally narrow but still records the trusted
// principal (id.Subject + id.APIKey) so the audit can show who
// tried.
func (h *ProposalsHandler) recordRejection(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal, oc outcome, reasons []string) {
	// Cloud counter for the rejection (almost always "invalid").
	// Fires before the runtime resolution because rejections
	// happen specifically when the runtime is unavailable or the
	// proposal shape is broken — exactly the cases where the
	// metrics path needs to keep working.
	h.incCounter(ctx, id.Tenant, counterForOutcome(oc))

	rt, err := h.runtimeFor(ctx, id.Tenant)
	if err != nil || rt == nil || rt.Audit == nil {
		return
	}
	// Attribute limit_exceeded to its governing control: it has a
	// valid action and the tenant usually has an active control, and
	// the evidence-timeline design counts it among a control's decisions-by-outcome.
	// The other rejections (invalid JSON, missing fields,
	// runtime unavailable) have no resolvable control and stay empty.
	var controlID, environment string
	var controlVersion int
	if oc == outcomeLimitExceeded {
		controlID, environment, controlVersion = h.resolveControlIdentity(ctx, id.Tenant, prop.ActionName)
	}
	_ = rt.Audit.Append(ctx, kiffaudit.Record{
		ID:         decisionRecordID(prop.ID, oc),
		Kind:       kiffaudit.Kind("proposal_rejected"),
		EntityID:   firstNonEmpty(prop.EntityID, "<unknown>"),
		EntityType: firstNonEmpty(prop.EntityType, "<unknown>"),
		TraceID:    prop.ID,
		ActorID:    firstNonEmpty(prop.ActorID, "<unknown>"),
		CreatedAt:  h.now(),
		Message:    "proposal rejected before runtime evaluation",
		Data:       attributionData(ctx, id, prop, oc, reasons, controlID, environment, controlVersion),
	})
}

// attributionData assembles the audit payload that captures both
// the trusted principal (authenticated subject + API key id +
// auth method + roles) and the caller-supplied attribution
// (proposed_actor_id, proposed_action). A receipt or timeline
// review can read this and tell which credential crossed the
// boundary, regardless of how the proposing system labelled the
// agent. When the call authenticated via an API key, the
// principal_api_key_id captures the specific key id from
// Identity.Metadata so a subject that mints multiple keys can
// still be disambiguated per request.
// paramsCarryRunContext reports whether the caller tried to assert
// KIFF run context through the model-supplied side of the request,
// and which key it was.
//
// Only `kiff_`-prefixed spellings match. An earlier version also
// refused a bare `run_context` parameter, which caught the mistake an
// integrator actually makes (copying the top-level field name into
// parameters) at the cost of taking a plausible name out of every
// customer's domain vocabulary — `parameters` is the *customer's*
// namespace, and a governance boundary should not quietly confiscate
// words from it. The `kiff_` prefix is unambiguously ours, so
// refusing it costs them nothing.
//
// What actually catches a misplaced assertion is not this check: it
// is fail-closed enforcement. An action that declares a run-context
// dependency and receives no assertion holds or refuses (see
// runContextGate), so an integrator who put the value in the wrong
// place watches their traffic stop rather than believing they are
// governed while being silently ignored. This helper is the cheap
// belt-and-braces on top of that, and it is worth keeping only
// because it costs nothing now that it is namespaced.
func paramsCarryRunContext(params map[string]any) (string, bool) {
	for k := range params {
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "kiff_run_context", "kiff_cloud_run_context", "kiffruncontext", "kiff-run-context":
			return k, true
		}
	}
	return "", false
}

func attributionData(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal, oc outcome, reasons []string, controlID, environment string, controlVersion int) map[string]any {
	data := map[string]any{
		"proposal_id":       prop.ID,
		"action":            prop.ActionName,
		"outcome":           string(oc),
		"reasons":           reasons,
		"proposed_actor_id": prop.ActorID,
		"principal_subject": id.Subject,
		// request_fingerprint lets the idempotency dedup
		// detect key reuse with a different payload: a later
		// proposal that reuses this id but changes entity/action/
		// parameters has a different fingerprint and is rejected
		// rather than served this decision. Cheap to store, and
		// never carries the raw parameters (it is a hash).
		"request_fingerprint": proposalFingerprint(ctx, prop),
	}
	// The trial that made the call (kiff/tafs#173), for the owner's record
	// and the trial's counts.
	if draft := trialDraftFrom(ctx); draft != "" {
		data["trial_draft"] = draft
	}
	// Run context, stamped only when the harness actually
	// asserted one. The three states a reader must be able to tell
	// apart are absent / asserted-clean / asserted-tainted, so a run
	// with no assertion gets no keys rather than untrusted_input=false:
	// silence must never render as a clean-run claim. The mandates design shipped
	// the inverse of this (an empty mandate set read as "nothing to
	// check", which meant allowed) and it is the same shape.
	if rc, ok := runcontext.FromContext(ctx); ok {
		for k, v := range rc.AuditFields() {
			data[k] = v
		}
	}
	// Control identity: scope the decision to the control that
	// governs the action, so the windowed query and the evidence
	// object can group by control. Empty when no active control matches.
	if controlID != "" {
		data["control_id"] = controlID
	}
	if environment != "" {
		data["environment"] = environment
	}
	// Governing control version: which version of the control
	// governed this decision. Stamped only when a control resolved.
	if controlVersion > 0 {
		data["control_version"] = controlVersion
	}
	if id.APIKey {
		// The middleware sets APIKey true when the request
		// authenticated via an API key; capture the boolean so
		// the audit row records whether the caller used a key
		// vs an upstream session JWT.
		data["principal_method"] = "api_key"
		// verifyAPIKey stamps the key id into Identity.Metadata.
		// Surface it on the audit row so a subject that mints
		// multiple keys can still be disambiguated per request.
		if keyID := id.Metadata["api_key_id"]; keyID != "" {
			data["principal_api_key_id"] = keyID
		}
		// The actor is verified only when the credential itself is the
		// agent's: a key bound to exactly this actor. Anything
		// else — an unbound key, a session, a legacy row with no stamp —
		// is the caller's claim, and readers must not present it as the
		// agent's own activity. Stamped only when true, so absence reads
		// as unverified.
		if id.AgentID != "" && id.AgentID == prop.ActorID {
			data["actor_verified"] = true
		}
	} else if id.Subject != "" {
		data["principal_method"] = "session"
	}
	if len(id.Roles) > 0 {
		data["principal_roles"] = append([]string(nil), id.Roles...)
	}
	return data
}

func firstNonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// incCounter fires a cloud-side counter. Safe with a nil factory:
// no-ops in that case, so tests and dev binaries that don't wire
// usage still work. Cloud proposal counters use a fixed bounded
// entity_type (usage.CloudEntityProposal) instead of
// caller-supplied prop.EntityType because the request body is
// untrusted: aggregating on a free-form caller string would let
// any authenticated tenant fan out cloud_meta.tenant_usage rows
// by varying the body.
//
// Per-domain attribution rides on the real domain_id dimension:
// the recorder is bound to the request's
// resolved domain, so consumption can be read per domain off the
// same counters without overloading entity_type (the trick).
// The domain is server-resolved (never caller-supplied), so
// cardinality stays bounded by the tenant's authoritative domains.
func (h *ProposalsHandler) incCounter(ctx context.Context, tenantID, name string) {
	if h.metrics == nil || tenantID == "" || name == "" {
		return
	}
	rec := h.metrics(tenantID, resolvedDomainID(ctx))
	if rec == nil {
		return
	}
	rec.Inc(name, 1, kiffruntime.EntityType(usage.CloudEntityProposal))
}

// counterForOutcome maps a wire outcome string to the cloud
// counter name. Centralized so the audit row + the metrics row
// agree on the same taxonomy.
func counterForOutcome(oc outcome) string {
	switch oc {
	case outcomeAllowed:
		return usage.CloudCounterProposalsAllowed
	case outcomeApprovalRequired:
		return usage.CloudCounterProposalsApprovalRequired
	case outcomeOwnerRequired, outcomeApprovedNoRoom:
		return usage.CloudCounterProposalsOwnerRequired
	case outcomeBlocked:
		return usage.CloudCounterProposalsBlocked
	case outcomeInvalid:
		return usage.CloudCounterProposalsInvalid
	case outcomeLimitExceeded:
		return usage.CloudCounterProposalsLimitExceeded
	}
	return ""
}

// statusFor maps an outcome to the HTTP status the handler
// returns. The mapping is conservative: the actual outcome
// always travels in the JSON body, the status is a hint.
//
//   - allowed            → 200
//   - approval_required  → 200 (the call succeeded; the answer
//     is "ask a human")
//   - blocked            → 200 (the call succeeded; the answer
//     is "do not execute")
//   - invalid            → 400 in most cases; 502 when the
//     reason is an infra blip we don't want
//     to encode as a governance denial
//     (state store unreachable, runtime
//     unavailable). statusForResponse below
//     uses the response reasons to pick.
//
// Returning 200 for blocked/approval_required keeps idempotent
// retries safe; a 4xx would imply the customer should change
// the request, but a state-not-allowed proposal is a valid
// question with a "no" answer.
func statusFor(out string) int {
	switch outcome(out) {
	case outcomeInvalid:
		return http.StatusBadRequest
	default:
		return http.StatusOK
	}
}

// statusForResponse picks the HTTP status from the full response
// (not just the outcome string) so we can map infrastructure
// failures to 502 even when the outcome is "invalid". Keeps the
// outcome string stable (clients still match on the four
// outcomes) while letting status code carry the retry hint.
func statusForResponse(resp proposalResponse) int {
	if outcome(resp.Outcome) == outcomeLimitExceeded {
		return http.StatusTooManyRequests
	}
	if resp.Outcome != string(outcomeInvalid) {
		return http.StatusOK
	}
	for _, reason := range resp.Reasons {
		switch reason {
		case "tenant_runtime_unavailable",
			"state_store_unavailable",
			"actions_unavailable":
			return http.StatusBadGateway
		}
	}
	return http.StatusBadRequest
}

type decideModeKey struct{}

// withDecideMode carries the tenant's mode to the Card check, which must
// not hold an exception in observe mode: the caller executes anyway, so
// there is nothing for an owner to answer.
func withDecideMode(ctx context.Context, mode tenant.Mode) context.Context {
	return context.WithValue(ctx, decideModeKey{}, mode)
}

func observing(ctx context.Context) bool {
	m, _ := ctx.Value(decideModeKey{}).(tenant.Mode)
	return m == tenant.ModeObserve
}

// refuseUnserialized answers a permit request that cannot take the
// per-operation lock: nothing is bound or authorized.
func (h *ProposalsHandler) refuseUnserialized(ctx context.Context, w http.ResponseWriter, id auth.Identity, mode tenant.Mode, prop kiffproposal.ActionProposal) {
	resp := proposalResponse{ProposalID: prop.ID, Outcome: string(outcomeBlocked), Reasons: []string{reasonPermitsUnavailable},
		Message: "KIFF could not serialize this operation, so it was not authorized; retry the same call"}
	h.applyMode(ctx, id.Tenant, mode, &resp)
	writeJSON(w, statusForResponse(resp), resp)
}
