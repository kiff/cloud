package receipt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"
)

// Pipeline turns the receipt building blocks (Build, Store, Attester)
// into a running production loop. Two responsibilities:
//
//  1. Reconcile a tenant's audit store into receipts. For every
//     action_executed record that does not yet have a receipt in the
//     store, build one and persist it. Synchronous, idempotent.
//
//  2. Attest unanchored receipts. Periodically (or on demand) read
//     the store's unanchored set, run Attester.Attest, and persist
//     the populated Attestation. Asynchronous; failures are logged
//     and the receipt stays unanchored for the next cycle.
//
// The pipeline does not own the runtime, the audit store, or the
// tenant store. Callers compose those (the HTTP path passes a
// resolved *kiffruntime.Runtime for inline reconcile; the background
// loop uses the tenant store + runtime resolver to walk active
// tenants).
//
// Concurrency: Pipeline is safe for concurrent calls into Reconcile
// and AttestPending. The underlying Store and Attester are expected
// to be goroutine-safe (the cloud's Postgres/Memory stores already
// are; LocalAttester and AnchorAttester are stateless after
// construction).
type Pipeline struct {
	store    Store
	attester Attester
	resolver AttesterResolver
	cursor   DecisionCursor
	// settlements lists settled holds for settled receipts; nil
	// leaves them off.
	settlements SettlementSource
	emails      AnswererEmails
	logger      *slog.Logger
	now         func() time.Time

	// inflight prevents two reconcile calls from racing on the same
	// (tenant, trace) and producing duplicate Save calls. Save is
	// itself idempotent (ON CONFLICT DO UPDATE), but skipping the
	// rebuild saves the audit query.
	inflightMu sync.Mutex
	inflight   map[traceKey]struct{}
}

// PipelineOption configures Pipeline.
type PipelineOption func(*Pipeline)

// WithLogger substitutes the logger. Defaults to a discard logger.
func WithLogger(l *slog.Logger) PipelineOption {
	return func(p *Pipeline) {
		if l != nil {
			p.logger = l
		}
	}
}

// WithClock substitutes the clock used for AttestPending's "now".
// The Attester carries its own clock for AnchoredAt; this clock is
// only for log timestamps.
func WithClock(now func() time.Time) PipelineOption {
	return func(p *Pipeline) {
		if now != nil {
			p.now = now
		}
	}
}

// WithDecisionCursor installs the per-tenant decision-reconcile watermark.
// When set, ReconcileDecisions skips proposal_decision records
// strictly before the tenant's mark instead of re-evaluating all history
// every sweep (records exactly at the mark are re-checked — an idempotent
// no-op via receipt dedup), and advances the mark after a clean pass. nil is
// ignored, leaving the full-scan behavior intact (tests, in-memory deploys).
func WithDecisionCursor(c DecisionCursor) PipelineOption {
	return func(p *Pipeline) {
		if c != nil {
			p.cursor = c
		}
	}
}

// WithAttesterResolver installs per-tenant attester routing.
// When set, AttestPending resolves the attester for each tenant
// (e.g. Sepolia for free tenants, mainnet for entitled ones) instead
// of using the single default attester. nil is ignored, leaving the
// single-attester behavior intact for tests and single-chain modes.
func WithAttesterResolver(r AttesterResolver) PipelineOption {
	return func(p *Pipeline) {
		if r != nil {
			p.resolver = r
		}
	}
}

// NewPipeline wires the dependencies. store is required; attester is
// required (pass NewLocalAttester(...) for environments without
// chain access).
func NewPipeline(store Store, attester Attester, opts ...PipelineOption) (*Pipeline, error) {
	if store == nil {
		return nil, errors.New("receipt: pipeline requires a store")
	}
	if attester == nil {
		return nil, ErrAttesterMissing
	}
	p := &Pipeline{
		store:    store,
		attester: attester,
		logger:   slog.New(discardHandler{}),
		now:      func() time.Time { return time.Now().UTC() },
		inflight: make(map[traceKey]struct{}),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
}

// traceKey identifies one in-flight reconcile so duplicate concurrent
// calls deduplicate.
type traceKey struct {
	TenantID string
	TraceID  string
}

// ReconcileTenant scans the tenant's audit store for action_executed
// records and persists a receipt for any that don't have one. The
// runtime's audit store is the source of truth; Pipeline never
// inspects the framework's internals beyond rt.Audit.
//
// Called inline from the HTTP path right after a successful execute
// or event-ingestion response. Latency budget: a few audit Query
// calls (one per recent trace) plus one Build per missing receipt.
//
// Errors building a single receipt are logged and skipped; one
// poison trace must not stall the loop. The function returns the
// count of receipts built (for log attribution) and an error only
// when the audit query itself fails irrecoverably.
func (p *Pipeline) ReconcileTenant(ctx context.Context, tenantID string, rt *kiffruntime.Runtime) (int, error) {
	if rt == nil {
		return 0, ErrRuntimeMissing
	}
	if rt.Audit == nil {
		return 0, ErrAuditStoreMissing
	}
	if tenantID == "" {
		return 0, ErrTenantIDRequired
	}

	// Pull every action_executed across the tenant's audit store.
	// This is unfiltered by trace because at the HTTP-hook point we
	// don't have a trace id available cheaply (the framework's
	// /events/raw and /execute responses don't include it). The
	// alternative — scanning all audit records and grouping by
	// trace — is more code for the same latency.
	executed, err := rt.Audit.Query(ctx, kiffaudit.Filter{Kind: kiffaudit.KindActionExecuted})
	if err != nil {
		return 0, fmt.Errorf("query action_executed: %w", err)
	}

	built := 0
	for _, rec := range executed {
		if rec.TraceID == "" {
			// A record without a trace id can't be receipted; skip.
			// The framework currently always sets one, but defence
			// in depth costs nothing.
			continue
		}

		key := traceKey{TenantID: tenantID, TraceID: rec.TraceID}
		if !p.acquire(key) {
			continue // another goroutine is already reconciling this trace
		}

		processed, perr := p.reconcileOne(ctx, tenantID, rec.TraceID, rt)
		p.release(key)

		if perr != nil {
			p.logger.WarnContext(ctx, "receipt: reconcile failed for trace; skipping",
				slog.String("tenant", tenantID),
				slog.String("trace_id", rec.TraceID),
				slog.String("err", perr.Error()),
			)
			continue
		}
		if processed {
			built++
		}
	}
	return built, nil
}

// ReconcileBlockedStateNotAllowed scans action_failed audit records
// and persists receipts for state_not_allowed decisions that do not
// already have one. This mirrors ReconcileTenant, but uses a distinct
// receipt id per failed audit record so a blocked retry can sit next
// to the successful receipt for the same entity trace.
func (p *Pipeline) ReconcileBlockedStateNotAllowed(ctx context.Context, tenantID string, rt *kiffruntime.Runtime) (int, error) {
	if rt == nil {
		return 0, ErrRuntimeMissing
	}
	if rt.Audit == nil {
		return 0, ErrAuditStoreMissing
	}
	if tenantID == "" {
		return 0, ErrTenantIDRequired
	}

	failed, err := rt.Audit.Query(ctx, kiffaudit.Filter{Kind: kiffaudit.KindActionFailed})
	if err != nil {
		return 0, fmt.Errorf("query action_failed: %w", err)
	}

	built := 0
	for _, rec := range failed {
		if !isStateNotAllowedRecord(rec) {
			continue
		}
		traceID := BlockedReceiptTraceID(rec)
		key := traceKey{TenantID: tenantID, TraceID: traceID}
		if !p.acquire(key) {
			continue
		}

		processed, perr := p.reconcileBlockedOne(ctx, tenantID, traceID, rt)
		p.release(key)

		if perr != nil {
			p.logger.WarnContext(ctx, "receipt: blocked reconcile failed for trace; skipping",
				slog.String("tenant", tenantID),
				slog.String("trace_id", traceID),
				slog.String("err", perr.Error()),
			)
			continue
		}
		if processed {
			built++
		}
	}
	return built, nil
}

// reconcileOne processes a single (tenant, trace). Returns true when
// a new receipt was built and persisted; false when the receipt
// already existed.
func (p *Pipeline) reconcileOne(ctx context.Context, tenantID, traceID string, rt *kiffruntime.Runtime) (bool, error) {
	if _, err := p.store.Get(ctx, tenantID, traceID); err == nil {
		return false, nil
	} else if !errors.Is(err, ErrReceiptNotFound) {
		return false, fmt.Errorf("store get: %w", err)
	}

	r, err := BuildFromRuntime(ctx, rt, BuildOptions{
		TenantID: tenantID,
		TraceID:  traceID,
	})
	if err != nil {
		// ErrNoActionExecuted shouldn't happen here (we just queried
		// for action_executed records) but a race with a parallel
		// audit truncation could surface it; treat as transient.
		return false, fmt.Errorf("build: %w", err)
	}

	if err := p.store.Save(ctx, r); err != nil {
		return false, fmt.Errorf("save: %w", err)
	}
	return true, nil
}

func (p *Pipeline) reconcileBlockedOne(ctx context.Context, tenantID, traceID string, rt *kiffruntime.Runtime) (bool, error) {
	if _, err := p.store.Get(ctx, tenantID, traceID); err == nil {
		return false, nil
	} else if !errors.Is(err, ErrReceiptNotFound) {
		return false, fmt.Errorf("store get: %w", err)
	}

	r, err := BuildBlocked(ctx, rt.Audit, BuildOptions{
		TenantID: tenantID,
		TraceID:  traceID,
	})
	if err != nil {
		return false, fmt.Errorf("build blocked: %w", err)
	}

	if err := p.store.Save(ctx, r); err != nil {
		return false, fmt.Errorf("save: %w", err)
	}
	return true, nil
}

// ReconcileDecisions scans the tenant's proposal_decision audit records
// and mints a decision receipt for every governed
// decision that does not already have a receipt. It is the background
// counterpart to ReconcileTenant: a connected kiff-guard runtime in
// enforce mode decides via POST /v1/proposals/decide without executing
// through the cloud, so there is no action_executed record — but the
// decide path does write a proposal_decision audit record, and this turns
// that record into a cloud-visible, signable receipt.
//
// Background-only by design: called from the anchor sweep, never inline on
// the decide hot path, so deciding pays no receipt latency (anchoring is
// asynchronous regardless). Idempotent and deduped — re-runs skip
// proposals that already have a receipt.
//
// Bounded by a per-tenant watermark when a DecisionCursor is wired:
// decisions strictly before the mark are skipped before any receipt-store
// lookup, so steady-state sweeps don't re-evaluate full history and a
// restart resumes instead of re-minting from scratch (which caused a
// first-sweep anchor thundering-herd). Records exactly AT the mark are
// re-checked each sweep (a cheap idempotent no-op) so a same-timestamp audit
// record is never missed; a future (created_at, record_id) compound cursor
// could do exact at-or-before skipping. The watermark is layered on top of
// the receipt-existence dedup, never the sole correctness mechanism: it only
// advances after a clean pass, and a missing/stale cursor degrades to a full
// (still-correct) pass.
//
// Dedup precedence (forward order only): when an execution receipt for the
// proposal already exists (cloud /execute path ran first), the sweep defers
// to it and mints no decision receipt — the richer execution receipt wins.
// It does NOT retroactively suppress a decision receipt if execution
// happens LATER: a decision receipt (trace "decision-<pid>") and an
// execution receipt (raw trace) are distinct artifacts linked by
// proposal_id, and the store has no delete. In pure Option A (vote-shape
// enforce, no cloud execution) the reverse order never occurs; suppressing
// a now-redundant decision receipt when a proposal later executes through
// the cloud is deferred to the Option B (execution-through-cloud) work.
func (p *Pipeline) ReconcileDecisions(ctx context.Context, tenantID string, rt *kiffruntime.Runtime) (int, error) {
	if rt == nil {
		return 0, ErrRuntimeMissing
	}
	if rt.Audit == nil {
		return 0, ErrAuditStoreMissing
	}
	if tenantID == "" {
		return 0, ErrTenantIDRequired
	}

	decisions, err := rt.Audit.Query(ctx, kiffaudit.Filter{Kind: decisionKind})
	if err != nil {
		return 0, fmt.Errorf("query proposal_decision: %w", err)
	}

	// Load the reconcile watermark: decisions strictly before
	// `through` are already reconciled, so we skip them before doing any
	// receipt-store lookups (records exactly at the mark are re-checked —
	// an idempotent no-op — so a same-timestamp record is never missed). A
	// cursor read error degrades to a full pass (the receipt-existence
	// dedup keeps that correct).
	var through time.Time
	if p.cursor != nil {
		if t, cerr := p.cursor.Through(ctx, tenantID); cerr != nil {
			p.logger.WarnContext(ctx, "receipt: decision cursor read failed; full pass",
				slog.String("tenant", tenantID), slog.String("err", cerr.Error()))
		} else {
			through = t
		}
	}

	built := 0
	seen := map[string]struct{}{}
	advanceOK := true // false once any in-window proposal can't be confirmed reconciled
	var maxSeen time.Time
	for _, rec := range decisions {
		// Skip decisions strictly before the watermark — already reconciled.
		// Records exactly AT the mark are intentionally re-checked every
		// sweep (a cheap idempotent no-op via the receipt dedup below) so a
		// later audit record sharing that exact CreatedAt can't be skipped.
		if !through.IsZero() && rec.CreatedAt.Before(through) {
			continue
		}
		if rec.Data == nil {
			continue
		}
		proposalID, _ := rec.Data["proposal_id"].(string)
		if proposalID == "" {
			continue
		}
		if rec.CreatedAt.After(maxSeen) {
			maxSeen = rec.CreatedAt
		}
		if _, dup := seen[proposalID]; dup {
			continue // one proposal can have multiple decision rows; reconcile once
		}
		seen[proposalID] = struct{}{}

		// BuildDecision (in reconcileDecisionOne) is the terminal-decision
		// authority and treats a non-receiptable or missing terminal
		// decision as a quiet no-op; only a real store/build error holds
		// the watermark.
		traceID := DecisionReceiptTraceID(proposalID)
		key := traceKey{TenantID: tenantID, TraceID: traceID}
		if !p.acquire(key) {
			advanceOK = false // in-flight elsewhere; don't advance past it
			continue
		}
		processed, perr := p.reconcileDecisionOne(ctx, tenantID, proposalID, traceID, rt)
		p.release(key)
		if perr != nil {
			p.logger.WarnContext(ctx, "receipt: decision reconcile failed for proposal; skipping",
				slog.String("tenant", tenantID),
				slog.String("proposal_id", proposalID),
				slog.String("err", perr.Error()),
			)
			advanceOK = false // a later sweep retries from the current mark
			continue
		}
		if processed {
			built++
		}
	}

	// Advance the watermark only after a clean in-window pass, so a
	// transient failure leaves the mark and the next sweep re-attempts
	// from there. Advance is monotonic (never moves backwards).
	if p.cursor != nil && advanceOK && maxSeen.After(through) {
		if cerr := p.cursor.Advance(ctx, tenantID, maxSeen); cerr != nil {
			p.logger.WarnContext(ctx, "receipt: advance decision cursor failed",
				slog.String("tenant", tenantID), slog.String("err", cerr.Error()))
		}
	}
	return built, nil
}

// reconcileDecisionOne mints the decision receipt for one proposal,
// returning true when a new receipt was persisted. Skips when a decision
// receipt already exists, or when any receipt already exists for the
// proposal (an execution receipt takes precedence). A non-receiptable or
// missing decision is skipped quietly (returns false, nil).
func (p *Pipeline) reconcileDecisionOne(ctx context.Context, tenantID, proposalID, traceID string, rt *kiffruntime.Runtime) (bool, error) {
	if _, err := p.store.Get(ctx, tenantID, traceID); err == nil {
		return false, nil
	} else if !errors.Is(err, ErrReceiptNotFound) {
		return false, fmt.Errorf("store get: %w", err)
	}
	if _, err := p.store.GetByProposalID(ctx, tenantID, proposalID); err == nil {
		return false, nil
	} else if !errors.Is(err, ErrReceiptNotFound) {
		return false, fmt.Errorf("store get by proposal: %w", err)
	}

	r, err := BuildDecision(ctx, rt.Audit, tenantID, proposalID)
	if err != nil {
		if errors.Is(err, ErrNoDecision) || errors.Is(err, ErrOutcomeNotReceiptable) {
			return false, nil
		}
		return false, fmt.Errorf("build decision: %w", err)
	}
	if err := p.store.Save(ctx, r); err != nil {
		return false, fmt.Errorf("save: %w", err)
	}
	return true, nil
}

// AttestPending walks the unanchored receipts for a tenant and runs
// Attester.Attest on each, persisting the populated Attestation back
// to the store. Called by the background anchor loop and at startup
// to pick up receipts left unanchored across a process restart.
//
// Failures are logged per receipt; the function returns the count
// attested and an error only when the underlying ListUnanchored
// call fails.
func (p *Pipeline) AttestPending(ctx context.Context, tenantID string) (int, error) {
	if tenantID == "" {
		return 0, ErrTenantIDRequired
	}
	pending, err := p.store.ListUnanchored(ctx, tenantID)
	if err != nil {
		return 0, fmt.Errorf("list unanchored: %w", err)
	}

	// Per-tenant chain routing: pick the attester for this
	// tenant's chain tier when a resolver is wired; otherwise use the
	// single default attester. The resolver never errors in practice
	// (it falls back to Sepolia), but a returned error or nil
	// attester degrades to the default rather than skipping anchoring.
	attester := p.attester
	if p.resolver != nil {
		if a, rerr := p.resolver.AttesterFor(ctx, tenantID); rerr != nil {
			p.logger.WarnContext(ctx, "receipt: attester resolve failed; using default attester",
				slog.String("tenant", tenantID),
				slog.String("err", rerr.Error()),
			)
		} else if a != nil {
			attester = a
		}
	}

	attested := 0
	for _, r := range pending {
		if err := ctx.Err(); err != nil {
			return attested, err
		}
		att, err := attester.Attest(ctx, r)
		if err != nil {
			p.logger.WarnContext(ctx, "receipt: attest failed; will retry",
				slog.String("tenant", tenantID),
				slog.String("trace_id", r.TraceID),
				slog.String("err", err.Error()),
			)
			continue
		}
		r.Attestation = att
		if err := p.store.Save(ctx, r); err != nil {
			p.logger.ErrorContext(ctx, "receipt: save attestation failed",
				slog.String("tenant", tenantID),
				slog.String("trace_id", r.TraceID),
				slog.String("err", err.Error()),
			)
			continue
		}
		attested++
		p.logger.InfoContext(ctx, "receipt: attested",
			slog.String("tenant", tenantID),
			slog.String("trace_id", r.TraceID),
			slog.String("anchor_tx", att.AnchorTxHash),
		)
	}
	return attested, nil
}

// TenantLister is the subset of tenant.Store the background loop
// needs. Defined here (not imported from the tenant package) so
// pipeline_test.go can stub it without dragging in the tenant
// store's full surface.
type TenantLister interface {
	ListActiveIDs(ctx context.Context) ([]string, error)
}

// RuntimeResolver is the subset of runtime.TenantRuntime the
// background loop needs.
type RuntimeResolver interface {
	For(ctx context.Context, tenantID string) (*kiffruntime.Runtime, error)
}

// RunAnchorLoop runs the periodic attest-pending loop until ctx is
// cancelled. interval is the tick between sweeps (e.g. 30 seconds).
// On each tick the loop pulls every active tenant, runs
// AttestPending against it, and logs a summary line.
//
// On startup, the loop runs one immediate sweep before sleeping so
// receipts left unanchored across a process restart get picked up
// without waiting a full interval.
//
// Returning from the loop is a clean shutdown signal; production
// callers run this in a goroutine and rely on ctx cancellation to
// stop it.
func (p *Pipeline) RunAnchorLoop(ctx context.Context, tenants TenantLister, runtimes RuntimeResolver, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if tenants == nil || runtimes == nil {
		p.logger.Warn("receipt: anchor loop disabled; tenants or runtimes nil")
		return
	}

	// Immediate first sweep so a process restart picks up receipts
	// left unanchored from the previous run.
	p.sweepOnce(ctx, tenants, runtimes)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sweepOnce(ctx, tenants, runtimes)
		}
	}
}

func (p *Pipeline) sweepOnce(ctx context.Context, tenants TenantLister, runtimes RuntimeResolver) {
	ids, err := tenants.ListActiveIDs(ctx)
	if err != nil {
		p.logger.WarnContext(ctx, "receipt: anchor sweep — list tenants failed",
			slog.String("err", err.Error()),
		)
		return
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return
		}
		// Resolve the runtime: it warms the cache, verifies the tenant
		// is healthy, and backs the decision-receipt
		// reconcile below. Skip silently on resolve errors.
		rt, err := runtimes.For(ctx, id)
		if err != nil {
			p.logger.DebugContext(ctx, "receipt: anchor sweep — skip tenant (runtime unavailable)",
				slog.String("tenant", id),
				slog.String("err", err.Error()),
			)
			continue
		}
		// Mint decision receipts for governed cloud decisions that
		// don't have one yet, before attesting — so a decision receipt
		// minted this sweep gets anchored in the same pass.
		if minted, derr := p.ReconcileDecisions(ctx, id, rt); derr != nil {
			p.logger.WarnContext(ctx, "receipt: decision sweep — reconcile failed",
				slog.String("tenant", id),
				slog.String("err", derr.Error()),
			)
		} else if minted > 0 {
			p.logger.InfoContext(ctx, "receipt: decision sweep — minted decision receipts",
				slog.String("tenant", id),
				slog.Int("count", minted),
			)
		}
		if minted, serr := p.ReconcileSettlements(ctx, id, rt); serr != nil {
			p.logger.WarnContext(ctx, "receipt: settlement sweep — reconcile failed",
				slog.String("tenant", id),
				slog.String("err", serr.Error()),
			)
		} else if minted > 0 {
			p.logger.InfoContext(ctx, "receipt: settlement sweep — minted settled receipts",
				slog.String("tenant", id),
				slog.Int("count", minted),
			)
		}
		count, err := p.AttestPending(ctx, id)
		if err != nil {
			p.logger.WarnContext(ctx, "receipt: anchor sweep — attest pending failed",
				slog.String("tenant", id),
				slog.String("err", err.Error()),
			)
			continue
		}
		if count > 0 {
			p.logger.InfoContext(ctx, "receipt: anchor sweep — attested",
				slog.String("tenant", id),
				slog.Int("count", count),
			)
		}
	}
}

func (p *Pipeline) acquire(k traceKey) bool {
	p.inflightMu.Lock()
	defer p.inflightMu.Unlock()
	if _, ok := p.inflight[k]; ok {
		return false
	}
	p.inflight[k] = struct{}{}
	return true
}

func (p *Pipeline) release(k traceKey) {
	p.inflightMu.Lock()
	defer p.inflightMu.Unlock()
	delete(p.inflight, k)
}

// discardHandler is a slog.Handler that drops every record. Mirrors
// the one in middleware.go; redeclared here so the receipt package
// has no internal dependency on auth.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (discardHandler) WithAttrs([]slog.Attr) slog.Handler        { return discardHandler{} }
func (discardHandler) WithGroup(string) slog.Handler             { return discardHandler{} }
