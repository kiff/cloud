// Package handlers — the cloud's HTTP-route layer. tenant_runtime.go
// dispatches an authenticated request into the tenant's KIFF
// runtime. The framework's httpapi.Handler exposes the protocol
// surface (events, entities, approvals); this wrapper picks the
// right tenant from the auth context, resolves a *kiffruntime.Runtime,
// and delegates.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	kiffhttpapi "github.com/kiff/kiff/pkg/kiff/httpapi"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/membership"
)

// TenantRuntimeAccessor is the contract the handler needs from
// runtime.TenantRuntime: resolve a tenant id to a *kiffruntime.Runtime.
// Pulled into its own interface so tests can stub the resolution
// path without dragging in the cache + pgxpool internals.
type TenantRuntimeAccessor interface {
	For(ctx context.Context, tenantID string) (*kiffruntime.Runtime, error)
	// ForDomain resolves the runtime for a specific domain of the
	// tenant. An empty domainID resolves the tenant's
	// default domain — identical to For — so callers without a domain
	// selector behave unchanged.
	ForDomain(ctx context.Context, tenantID, domainID string) (*kiffruntime.Runtime, error)
	// SpecForDomain resolves the cloud-side Spec for the same
	// (tenant, domain), from the same parse that produced the runtime.
	// The framework's ActionContract deliberately carries no
	// cloud-only fields, so an action's run-context dependency is only
	// readable here.
	//
	// It is part of this interface rather than an optional one a caller
	// type-asserts, because an accessor that silently could not answer
	// would leave an action that declares a run-context dependency
	// ungoverned while reading as governed — the failure mode the trusted-run-context design
	// exists to refuse. An implementation with nothing to say returns a
	// zero Spec, which declares no rules; it does not get to be absent.
	SpecForDomain(ctx context.Context, tenantID, domainID string) (domain.Spec, error)
	// DomainIDFor returns the immutable domain_id the selector resolves
	// to (the default domain's for ""), from the same resolution as the
	// runtime. Decisions are stamped and cards are matched with it, not
	// with the YAML's name, which a rename changes. Empty when
	// the implementation cannot say.
	DomainIDFor(ctx context.Context, tenantID, domainID string) (string, error)
}

// PostExecuteHook is called synchronously after a successful (2xx)
// framework response on routes that produce action_executed audit
// records (POST /v1/events/raw, POST /v1/entities/{id}/execute).
// The hook builds and persists the receipt before the response
// is flushed to the client, so a follow-up
// GET /v1/receipts/{trace_id} is guaranteed to find it — there is
// no race window where the client sees the execute succeed and
// the receipt is not yet in the store.
//
// Implementations must be cheap (target <50 ms). The handler
// invokes the hook on a request-scoped context with a short
// bounded timeout (default 2 seconds) so a hook that wedges does
// not stall the response indefinitely. A hook failure logs and
// the response flushes anyway: the receipt is durable evidence,
// not a precondition for the action.
//
// On-chain anchoring is deliberately NOT part of this hook.
// Anchoring happens asynchronously in a background sweep loop
// because chain confirmation is too slow (seconds) to block the
// hot path. The hook's job is build + sign + persist; anchor
// updates the same row later.
type PostExecuteHook func(ctx context.Context, tenantID string, rt *kiffruntime.Runtime)

// BlockedExecuteHook is called synchronously after the framework
// rejects an execute request with state_not_allowed. The action did
// not run, but the decision is still evidence: KIFF independently
// blocked the action before execution, and the cloud must persist a
// receipt for that fact.
type BlockedExecuteHook func(ctx context.Context, tenantID string, rt *kiffruntime.Runtime)

// TenantRuntimeHandler routes authenticated /v1 requests for the
// KIFF protocol surface (events / entities / approvals) into the
// per-tenant runtime.
//
// ApprovalDomainResolver reports which domain owns an approval, so a
// review addressed by id alone reaches the right runtime. Optional:
// nil keeps the earlier behaviour of resolving the default domain.
type ApprovalDomainResolver interface {
	DomainOf(ctx context.Context, tenantID, approvalID string) (string, bool, error)
}

// The handler caches one *kiffhttpapi.Handler per resolved runtime
// to avoid the per-request NewHandler allocation. The framework's
// Handler is goroutine-safe.
type TenantRuntimeHandler struct {
	tenants         TenantRuntimeAccessor
	prefix          string
	logger          *slog.Logger
	hook            PostExecuteHook
	blocked         BlockedExecuteHook
	approvalDomains ApprovalDomainResolver
	// holds applies the governance-roles design's hold rules to approval reviews and records
	// who answered. Nil keeps the rules from before memberships.
	holds *HoldGate

	mu             sync.RWMutex
	frameworkCache map[*kiffruntime.Runtime]*kiffhttpapi.Handler
}

// NewTenantRuntimeHandler constructs a handler. prefix is the URL
// segment the cloud mounts the framework surface at (typically
// "/v1"); it is stripped before delegating so the framework
// continues to see canonical paths like "/events/raw".
//
// logger is optional; nil yields a discard logger.
func NewTenantRuntimeHandler(tenants TenantRuntimeAccessor, prefix string, logger *slog.Logger) *TenantRuntimeHandler {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &TenantRuntimeHandler{
		tenants:        tenants,
		prefix:         strings.TrimRight(prefix, "/"),
		logger:         logger,
		frameworkCache: make(map[*kiffruntime.Runtime]*kiffhttpapi.Handler),
	}
}

// WithPostExecuteHook installs hook as the post-success callback.
// Returns the same handler so callers can chain. Passing nil clears
// any previously-set hook. The hook fires only on 2xx responses
// from execute / events-raw paths; other framework routes (timeline,
// approvals listing, etc.) don't trigger it.
func (h *TenantRuntimeHandler) WithPostExecuteHook(hook PostExecuteHook) *TenantRuntimeHandler {
	h.hook = hook
	return h
}

// WithBlockedExecuteHook installs hook as the post-blocked callback.
// It fires only for execute routes where the framework returned a
// state_not_allowed error. Passing nil clears any previously-set hook.
func (h *TenantRuntimeHandler) WithBlockedExecuteHook(hook BlockedExecuteHook) *TenantRuntimeHandler {
	h.blocked = hook
	return h
}

// WithApprovalDomains installs the resolver that maps an approval id to
// its owning domain. Without it, a review from the tenant-wide
// queue resolves the default domain and fails for an approval owned by
// any other. Passing nil clears it.
func (h *TenantRuntimeHandler) WithApprovalDomains(r ApprovalDomainResolver) *TenantRuntimeHandler {
	h.approvalDomains = r
	return h
}

// ServeHTTP resolves the tenant from the auth context, fetches its
// runtime, strips the cloud prefix, and delegates. Failure modes:
//
//   - identity missing or has empty tenant: 401
//   - tenant runtime resolution fails: 502 (the credential is
//     valid; the tenant data plane is broken). The response body
//     is intentionally terse; the cause lands in the logs.
func (h *TenantRuntimeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		// Should not happen when the auth middleware sits in front.
		// Defence in depth.
		writeJSONStatus(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		h.logger.WarnContext(r.Context(), "auth: identity has empty tenant",
			slog.String("subject", id.Subject),
			slog.String("path", r.URL.Path),
		)
		writeJSONStatus(w, http.StatusUnauthorized, "identity has no tenant")
		return
	}

	// Resolve an optional ?domain=<slug> selector so a
	// multi-domain tenant can address a NON-default domain on the
	// framework-proxied surfaces (events/raw, execute, timeline,
	// approvals). This mirrors the decide path (POST /v1/proposals/
	// decide reads the same selector from its body): wired the
	// selector into decide but not into these surfaces, so a
	// non-default domain could be decided against but never seeded
	// with state or executed. An empty selector resolves the tenant's
	// default domain — unchanged behavior — and a domain-scoped key
	// refuses a conflicting selector (the multi-domain design isolation wall).
	domainID, derr := domain.ResolveRequestDomain(id.DomainID, domain.Slugify(strings.TrimSpace(r.URL.Query().Get("domain"))))
	if derr != nil {
		h.logger.InfoContext(r.Context(), "domain selector rejected",
			slog.String("tenant", id.Tenant),
			slog.String("subject", id.Subject),
			slog.String("err", derr.Error()),
		)
		writeJSONStatus(w, http.StatusForbidden, "domain selector conflicts with the key's domain binding")
		return
	}

	// A review addresses an approval by id alone, with no domain in the
	// path. For a management caller reading the tenant-wide queue, the
	// default domain is therefore the wrong runtime for any approval
	// owned by another domain. Resolve the owner from the
	// stamped column instead of guessing, when the caller did not
	// supply one and is not bound to a domain.
	// The prefix strip happens further down, so derive the framework-side
	// path here rather than reaching for the rewritten request.
	frameworkPath := strings.TrimPrefix(r.URL.Path, h.prefix)
	if domainID == "" && h.approvalDomains != nil &&
		r.Method == http.MethodPost && isApprovalReviewSuffix(frameworkPath) {
		owner, found, aerr := h.approvalDomains.DomainOf(r.Context(), id.Tenant, approvalIDFromReviewPath(frameworkPath))
		switch {
		case aerr != nil:
			h.logger.WarnContext(r.Context(), "approval domain lookup failed",
				slog.String("tenant", id.Tenant),
				slog.String("err", aerr.Error()),
			)
			writeJSONStatus(w, http.StatusBadGateway, "approval lookup unavailable")
			return
		case !found:
			writeJSONStatus(w, http.StatusNotFound, "approval not found")
			return
		case owner == "":
			// A legacy unattributed row. No domain runtime owns it, so
			// no domain runtime can review it. Say so rather than
			// letting the default domain answer for it.
			writeJSONStatus(w, http.StatusConflict,
				"approval predates domain attribution and cannot be reviewed until an operator attributes it")
			return
		default:
			domainID = owner
		}
	}

	rt, err := h.tenants.ForDomain(r.Context(), id.Tenant, domainID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "tenant runtime resolution failed",
			slog.String("tenant", id.Tenant),
			slog.String("domain", domainID),
			slog.String("err", err.Error()),
		)
		writeJSONStatus(w, http.StatusBadGateway, "tenant runtime unavailable")
		return
	}

	frameworkHandler := h.handlerFor(rt)

	// Strip the cloud's prefix so the framework sees its canonical
	// paths. Shallow-copy the request so we do not mutate the
	// caller's value.
	rr := *r
	url := *r.URL
	rr.URL = &url
	rr.URL.Path = strings.TrimPrefix(rr.URL.Path, h.prefix)
	if rr.URL.Path == "" {
		rr.URL.Path = "/"
	}
	if rr.URL.RawPath != "" {
		rr.URL.RawPath = strings.TrimPrefix(rr.URL.RawPath, h.prefix)
	}
	// The ?domain= selector is a cloud-layer concern; strip it so the
	// framework sees its canonical query (it never reads "domain").
	if rr.URL.RawQuery != "" && rr.URL.Query().Has("domain") {
		q := rr.URL.Query()
		q.Del("domain")
		rr.URL.RawQuery = q.Encode()
	}

	// The blanket 403 that used to stand here — a domain-bound key could
	// not read or review any approval — is no longer needed. Approvals
	// now carry domain_id and the runtime resolved for this
	// request is wired with an approval store bound to (tenant, domain),
	// so scoping is enforced at the store rather than by refusing the
	// route: another domain's approval reads as not-found, and a write
	// that targets one is rejected.
	//
	// Legacy rows written before that migration carry no domain and are
	// therefore invisible here by the same predicate — they remain
	// reachable only through the management surface, which is the
	// intended fail-closed behavior rather than an omission.

	// Cloud-side gate on the approval-review surface (/ RFC
	// 011 M1.2b). The framework's runtime.ReviewApproval trusts
	// the caller string; the cloud knows the tenant's roles, so
	// the permission check belongs here, before delegation. The
	// reviewer permission is read from the cloud-side spec, from the
	// same parse as the runtime. An action that declares none cannot
	// be reviewed here: fail closed.
	if isApprovalReviewPath(&rr) {
		spec, specErr := h.tenants.SpecForDomain(r.Context(), id.Tenant, domainID)
		if err := enforceApprovalReviewPermission(r.Context(), rt, spec, specErr, id, rr.URL.Path); err != nil {
			h.logger.InfoContext(r.Context(), "approval-review denied",
				slog.String("tenant", id.Tenant),
				slog.String("subject", id.Subject),
				slog.String("path", rr.URL.Path),
				slog.String("err", err.Error()),
			)
			writeApprovalReviewError(w, err)
			return
		}
		if h.holds != nil {
			approvalID := approvalIDFromReviewPath(rr.URL.Path)
			gov, err := h.holds.Check(r.Context(), id, id.Tenant, approvalHold(r.Context(), rt, approvalID), false)
			if err != nil {
				writeHoldGateError(w, err)
				return
			}
			answer := "deny"
			if strings.HasSuffix(rr.URL.Path, "/grant") {
				answer = "grant"
			}
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			defer func() {
				if rec.status >= 200 && rec.status < 300 {
					if err := h.holds.Record(context.WithoutCancel(r.Context()), id, id.Tenant, approvalID, answer, gov); err != nil {
						h.logger.ErrorContext(r.Context(), "approval answer attribution not recorded",
							slog.String("tenant", id.Tenant), slog.String("approval", approvalID), slog.String("err", err.Error()))
					}
				}
			}()
			w = rec
		}
	}

	// The framework accepts an actor object in several request bodies.
	// Cloud is the authentication authority, so replace that complete
	// object with the resolved principal before any permission check or
	// audit write. Oversized and unreadable bodies fail closed rather
	// than bypassing the rewrite.
	if shape := authenticatedActorShape(&rr); shape != actorShapeNone {
		rewritten, rewriteErr := rewriteAuthenticatedActor(&rr, id.ActorID(), id.Roles, shape)
		if rewriteErr != nil {
			status := http.StatusBadRequest
			message := "invalid request body"
			if errors.Is(rewriteErr, errActorBodyTooLarge) {
				status = http.StatusRequestEntityTooLarge
				message = "request body is too large"
			}
			h.logger.InfoContext(r.Context(), "authenticated actor rewrite rejected",
				slog.String("tenant", id.Tenant),
				slog.String("subject", id.Subject),
				slog.String("path", rr.URL.Path),
				slog.String("err", rewriteErr.Error()),
			)
			writeJSONStatus(w, status, message)
			return
		}
		rr = *rewritten
	}

	// Decide whether this request should trigger the post-execute
	// hook. Only execute and event-ingest paths produce
	// action_executed audit records; other routes (timeline,
	// approvals listing, validate) don't, so wrapping the response
	// writer only on those paths keeps the hot path narrow.
	shouldHook := (h.hook != nil && isExecuteOrIngest(&rr)) || (h.blocked != nil && isExecutePath(&rr))
	if !shouldHook {
		frameworkHandler.ServeHTTP(w, &rr)
		return
	}

	// Buffer the framework's response. We need to know its status
	// code (so the hook only fires on 2xx) before we flush, and on
	// success we want the receipt to be persisted before the
	// client observes the execute as done. A `GET /v1/receipts/...`
	// issued the moment after the execute returns must hit a
	// populated row — anything less and the dashboard polling has
	// to retry, which is the bug this slice exists to prevent.
	buf := newBufferedWriter(w)
	frameworkPanicked := false
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				frameworkPanicked = true
				h.logger.ErrorContext(r.Context(), "framework handler panicked",
					slog.String("tenant", id.Tenant),
					slog.String("path", rr.URL.Path),
					slog.Any("panic", rec),
				)
				// A panic in the framework is a 500 to the client.
				// Reset the buffer so we don't leak a half-written
				// body; the framework's own panic-handler would have
				// done this with the real ResponseWriter.
				buf.body = nil
				buf.status = http.StatusInternalServerError
				buf.written = true
			}
		}()
		frameworkHandler.ServeHTTP(buf, &rr)
	}()

	if !frameworkPanicked && h.hook != nil && buf.status >= 200 && buf.status < 300 {
		// 2-second hook budget. Long enough for an audit query +
		// receipt build + Postgres insert; short enough that a
		// stuck hook does not visibly delay the response.
		hookCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		func() {
			defer cancel()
			defer func() {
				if rec := recover(); rec != nil {
					h.logger.ErrorContext(hookCtx, "post-execute hook panicked",
						slog.String("tenant", id.Tenant),
						slog.Any("panic", rec),
					)
				}
			}()
			h.hook(hookCtx, id.Tenant, rt)
		}()
	}
	if !frameworkPanicked && h.blocked != nil && isExecutePath(&rr) && isStateNotAllowedResponse(buf) {
		hookCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		func() {
			defer cancel()
			defer func() {
				if rec := recover(); rec != nil {
					h.logger.ErrorContext(hookCtx, "blocked-execute hook panicked",
						slog.String("tenant", id.Tenant),
						slog.Any("panic", rec),
					)
				}
			}()
			h.blocked(hookCtx, id.Tenant, rt)
		}()
	}

	// Flush the buffered response regardless of hook outcome. The
	// execute has already happened in the framework runtime; the
	// hook is durability/visibility, not authorization. Failing to
	// build a receipt must never turn a successful execute into a
	// 5xx for the caller.
	if err := buf.Flush(); err != nil {
		h.logger.WarnContext(r.Context(), "tenant runtime: flush buffered response failed",
			slog.String("err", err.Error()),
		)
	}
}

func isStateNotAllowedResponse(buf *bufferedWriter) bool {
	if buf == nil || buf.status != http.StatusBadRequest {
		return false
	}
	// The framework's typed reason (its outcome envelope). A runtime on a
	// framework from before the envelope sends only `error`: its text is
	// read then, and only then.
	var body struct {
		Reason *string `json:"reason"`
		Error  string  `json:"error"`
	}
	if err := json.Unmarshal(buf.body, &body); err != nil {
		return false
	}
	if body.Reason != nil {
		return *body.Reason == "state_not_allowed"
	}
	return strings.Contains(strings.ToLower(body.Error), "action state not allowed")
}

// isExecuteOrIngest reports whether the (already-prefix-stripped)
// request is one that the framework would record as an
// action_executed audit event. Limits the hook to just those routes
// so timeline reads, approvals listing, and validation calls
// don't waste a goroutine + audit query each.
//
// The two paths matched correspond to httpapi.handleIngestRaw
// (POST /events/raw) and httpapi.handleExecuteAction
// (POST /entities/{id}/execute).
func isExecuteOrIngest(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	path := r.URL.Path
	if path == "/events/raw" {
		return true
	}
	if strings.HasPrefix(path, "/entities/") && strings.HasSuffix(path, "/execute") {
		return true
	}
	return false
}

// bufferedWriter captures the framework handler's status, headers,
// and body so the cloud's post-execute hook can run before the
// bytes reach the client. Flush copies the buffered state to the
// underlying ResponseWriter in the right order (header values,
// then status, then body).
//
// Why buffer instead of stream-and-fire-async: the receipt must
// exist in the store before the client sees the execute as
// complete. Streaming the response while the hook runs in a
// goroutine creates a race window where the dashboard's next
// fetch returns 404. Synchronous buffering is the simpler answer
// and the response sizes on the protocol surface are small (KB,
// not MB).
type bufferedWriter struct {
	target  http.ResponseWriter
	header  http.Header
	body    []byte
	status  int
	written bool
}

func newBufferedWriter(target http.ResponseWriter) *bufferedWriter {
	return &bufferedWriter{
		target: target,
		header: http.Header{},
		status: http.StatusOK,
	}
}

// Header returns the buffered header map. The framework's handler
// writes Content-Type and friends through this; Flush copies them
// onto the real ResponseWriter before WriteHeader runs.
func (b *bufferedWriter) Header() http.Header { return b.header }

// WriteHeader records the status without writing anything to the
// underlying response. Idempotent on first call (Go's net/http
// contract).
func (b *bufferedWriter) WriteHeader(code int) {
	if b.written {
		return
	}
	b.status = code
	b.written = true
}

// Write appends to the body buffer. WriteHeader is implicitly
// called with 200 on the first Write, matching the standard
// ResponseWriter behaviour.
func (b *bufferedWriter) Write(p []byte) (int, error) {
	if !b.written {
		b.WriteHeader(http.StatusOK)
	}
	b.body = append(b.body, p...)
	return len(p), nil
}

// Flush copies the buffered state to the underlying ResponseWriter
// in the correct order: headers → status → body. Returns the
// error from the body Write (header copying does not return one).
//
// Callers invoke Flush exactly once after the hook has either
// completed or been skipped.
func (b *bufferedWriter) Flush() error {
	dst := b.target.Header()
	for k, vs := range b.header {
		dst[k] = vs
	}
	b.target.WriteHeader(b.status)
	if len(b.body) == 0 {
		return nil
	}
	_, err := b.target.Write(b.body)
	return err
}

// handlerFor returns a cached *kiffhttpapi.Handler for the runtime,
// constructing one on first use. The framework handler is
// stateless w.r.t. the request and safe to share across goroutines.
func (h *TenantRuntimeHandler) handlerFor(rt *kiffruntime.Runtime) *kiffhttpapi.Handler {
	h.mu.RLock()
	cached, ok := h.frameworkCache[rt]
	h.mu.RUnlock()
	if ok {
		return cached
	}
	// Unauthenticated by construction, and correct here: Cloud is the
	// authentication authority. ServeHTTP above has already validated the
	// caller's identity, enforced the approval-review permission gate, and
	// rewritten the actor in the request body to the resolved principal — so
	// the framework handler receives a request whose actor is already a fact
	// rather than a claim. Handing it an Authenticator would authenticate the
	// same request twice against a weaker source of truth.
	//
	// The framework refuses to serve when neither an Authenticator nor this
	// opt-out is set, which is why the choice is named explicitly.
	built := kiffhttpapi.NewUnauthenticatedHandler(rt)
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.frameworkCache[rt]; ok {
		return existing
	}
	h.frameworkCache[rt] = built
	return built
}

func writeJSONStatus(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// ErrNoTenantOnIdentity is returned in contexts where a caller
// wants to distinguish "identity present but tenant missing" from
// other 401 causes. The HTTP path emits the same 401; this
// sentinel exists for non-HTTP callers if any land later.
var ErrNoTenantOnIdentity = errors.New("handlers: identity has no tenant")

// WithHoldGate applies the governance-roles design's hold rules to approval reviews.
func (h *TenantRuntimeHandler) WithHoldGate(m membership.Store) *TenantRuntimeHandler {
	if m != nil {
		h.holds = &HoldGate{Memberships: m}
	}
	return h
}

// WithHoldGateAlert is told when an answer's attribution could not be
// written (see HoldGate.OnRecordError). Call after WithHoldGate.
func (h *TenantRuntimeHandler) WithHoldGateAlert(f func(ctx context.Context, tenantID, holdID string, err error)) *TenantRuntimeHandler {
	if h.holds != nil {
		h.holds.OnRecordError = f
	}
	return h
}

// approvalHold is what the hold gate needs about a runtime approval:
// who asked for it. A store error leaves the proposers empty; the
// framework's own review path still refuses a missing approval.
func approvalHold(ctx context.Context, rt *kiffruntime.Runtime, approvalID string) Hold {
	var h Hold
	if rt == nil || rt.Approvals == nil || approvalID == "" {
		return h
	}
	if rec, ok, err := rt.Approvals.Get(ctx, approvalID); err == nil && ok {
		h.Proposers = append(h.Proposers, rec.RequestedBy)
	}
	if rk, ok := rt.Approvals.(requesterKeyReader); ok {
		if k, err := rk.RequesterKey(ctx, approvalID); err == nil {
			h.Proposers = append(h.Proposers, k)
		}
	}
	return h
}

// statusRecorder remembers the status a response was written with.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
