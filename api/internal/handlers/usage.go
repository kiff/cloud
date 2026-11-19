package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// UsageHandler exposes the cloud's tenant-facing usage surface:
//
//	GET /v1/me/usage         — current-period summary for the
//	                           authenticated tenant.
//
// The endpoint is the activation + plan-limit input. It returns
// the canonical cloud counters (kiff.cloud.proposals.submitted /
// allowed / approval_required / blocked / invalid) plus the
// framework-emitted counters that already land in
// cloud_meta.tenant_usage when the tenant exercises the runtime
// (kiff.events.ingested, kiff.decisions.recorded, etc).
//
// Trust boundary:
//
//   - tenant id always comes from auth.IdentityFrom; the response
//     never exposes another tenant's rows.
//   - parameters / API keys / secrets are not part of the counter
//     surface. The data here is governance-shape activity, not
//     payload content.
//
// Freshness: counters live in two places — the in-memory
// usage.Aggregator (≤30 s before flush) and the persisted Store.
// The handler runs the aggregator's FlushNow before reading the
// Store so the response reflects every increment up to the request
// instant. Without this, a tenant that posts a proposal and
// immediately reads /v1/me/usage would see zero until the next
// scheduled flush — which would also break plan-limit
// enforcement, where the gate must see the counter that the same
// request just incremented.
//
// Period semantics: by default the response covers the current
// calendar month in UTC. Callers that want a different range can
// pass ?from=<RFC3339>&to=<RFC3339> for explicit bounds. A future
// slice will surface "billing period" naming when a real plan/
// limit policy lands.
type UsageHandler struct {
	store       usage.Store
	tenantStore tenant.Store
	flusher     UsageFlusher
	logger      *slog.Logger
	now         func() time.Time
}

// UsageFlusher drains an in-memory counter aggregator into the
// persistent Store. Implementations:
//
//   - usage.Aggregator (production) buckets counters in-memory
//     and exposes FlushNow.
//   - tests can pass nil to skip flushing when they seed the
//     Store directly.
//
// The handler tolerates a nil flusher: it just skips the flush and
// reads whatever the Store already has. That is the right behavior
// for tests that seed counters directly through Store.Flush.
type UsageFlusher interface {
	FlushNow(ctx context.Context) error
}

// UsageOption configures UsageHandler.
type UsageOption func(*UsageHandler)

// WithUsageLogger substitutes the logger.
func WithUsageLogger(l *slog.Logger) UsageOption {
	return func(h *UsageHandler) {
		if l != nil {
			h.logger = l
		}
	}
}

// WithUsageClock substitutes the clock used to bound the default
// period. Tests pin time so output is reproducible.
func WithUsageClock(now func() time.Time) UsageOption {
	return func(h *UsageHandler) {
		if now != nil {
			h.now = now
		}
	}
}

// WithUsageFlusher installs a flusher that drains the in-memory
// aggregator before a snapshot read. Production wires
// usage.Aggregator; tests that seed the Store directly leave
// this nil.
func WithUsageFlusher(f UsageFlusher) UsageOption {
	return func(h *UsageHandler) {
		if f != nil {
			h.flusher = f
		}
	}
}

// WithUsageTenantStore installs the tenant store used to resolve
// plan metadata. The usage surface still works without it and
// falls back to the free/dev defaults; production wires this so
// /v1/me/usage is self-contained for dashboard activation and
// plan-limit copy.
func WithUsageTenantStore(store tenant.Store) UsageOption {
	return func(h *UsageHandler) {
		if store != nil {
			h.tenantStore = store
		}
	}
}

// NewUsageHandler wraps the cloud's usage store. Returns nil
// when store is nil, which the router treats as "usage surface
// disabled" (the binary still serves /v1 without it).
func NewUsageHandler(store usage.Store, opts ...UsageOption) *UsageHandler {
	if store == nil {
		return nil
	}
	h := &UsageHandler{
		store:  store,
		logger: slog.Default(),
		now:    func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Register attaches the handler's routes to mux.
func (h *UsageHandler) Register(mux *http.ServeMux) {
	// The decision counters, for the dashboard. GET /v1/me/usage is the
	// account's two meters (MeterUsageHandler).
	mux.HandleFunc("GET /v1/me/usage/counters", h.summary)
	// Per-domain consumption slice: the same
	// governed-operation counters, scoped to one domain via the
	// server-validated entity_type dimension the proposal gate now
	// stamps. Same UTC-month measure as the account-level summary.
	mux.HandleFunc("GET /v1/me/domains/{domain}/usage", h.domainSummary)
}

// usageSummaryResponse is the JSON returned to callers. The
// counters map is keyed by canonical counter name and carries
// total counts across the period. Per-entity-type and per-hour
// breakdowns are deliberately omitted at v0.1 — clients that need
// them can iterate later through richer snapshot calls. The shape
// here is what the dashboard / kiff-tenant inspect / future
// plan-limit logic actually need: "did this tenant cross the line".
type usageSummaryResponse struct {
	TenantID             string            `json:"tenant_id"`
	Plan                 string            `json:"plan"`
	MonthlyProposalLimit int               `json:"monthly_proposal_limit"`
	From                 time.Time         `json:"from"`
	To                   time.Time         `json:"to"`
	Counters             map[string]uint64 `json:"counters"`
}

func (h *UsageHandler) summary(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}

	from, to, err := parsePeriod(r, h.now)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Drain the in-memory aggregator so counters incremented in
	// the same request window (or by a peer request that has not
	// hit its scheduled flush yet) are visible in this read. A
	// flush failure is logged but does not fail the request:
	// stale-by-one-cycle is preferable to a 500 on the activation
	// path. The Snapshot read still surfaces every previously-
	// flushed row, so the response always represents at least the
	// last persisted state.
	if h.flusher != nil {
		if err := h.flusher.FlushNow(r.Context()); err != nil {
			h.logger.WarnContext(r.Context(), "usage: flush before snapshot failed; serving last-persisted state",
				slog.String("err", err.Error()),
			)
		}
	}

	// Pull every row in the window for this tenant. The Store's
	// Snapshot filter does not expose another tenant's data — the
	// TenantID predicate is server-controlled.
	rows, err := h.store.Snapshot(r.Context(), usage.Filter{
		TenantID: id.Tenant,
		HourFrom: from,
		HourTo:   to,
	})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "usage: snapshot failed",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "snapshot failed"})
		return
	}

	counters := make(map[string]uint64, len(usage.CloudCounterNames))
	// Seed the response with every cloud counter at zero so
	// callers can branch on "missing key" without nil-checking.
	// The framework counters are not seeded here — they show up
	// in the response only when the runtime actually emitted
	// them.
	for _, name := range usage.CloudCounterNames {
		counters[name] = 0
	}
	for _, row := range rows {
		counters[row.CounterName] += row.Count
	}
	limits := h.planLimits(r.Context(), id.Tenant)

	writeJSON(w, http.StatusOK, usageSummaryResponse{
		TenantID:             id.Tenant,
		Plan:                 string(limits.Plan),
		MonthlyProposalLimit: limits.MonthlyProposalLimit,
		From:                 from,
		To:                   to,
		Counters:             counters,
	})
}

// domainUsageResponse is the GET /v1/me/domains/{domain}/usage body.
// The account summary's per-domain slice: the same cloud counters,
// summed only over rows attributed to this domain via the entity_type
// dimension the gate stamps from the tenant's active domain.
// MonthlyProposalLimit is the account-level allowance, carried for
// context — the allowance is per-account; this view shows the domain's
// share of usage against it.
type domainUsageResponse struct {
	Domain               string            `json:"domain"`
	From                 time.Time         `json:"from"`
	To                   time.Time         `json:"to"`
	MonthlyProposalLimit int               `json:"monthly_proposal_limit"`
	Counters             map[string]uint64 `json:"counters"`
}

// domainSummary handles GET /v1/me/domains/{domain}/usage. It mirrors
// summary() but sums only the rows whose entity_type matches the
// domain slug — the per-domain governed-operations slice. An
// unknown domain (or one with no usage yet) returns zeroed counters,
// not a 404: "no governed operations in this domain this period" is an
// honest answer, and the read never reveals whether the slug exists.
func (h *UsageHandler) domainSummary(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	slug := strings.TrimSpace(r.PathValue("domain"))
	if slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "domain is required"})
		return
	}

	from, to, err := parsePeriod(r, h.now)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if h.flusher != nil {
		if err := h.flusher.FlushNow(r.Context()); err != nil {
			h.logger.WarnContext(r.Context(), "usage: flush before domain snapshot failed; serving last-persisted state",
				slog.String("err", err.Error()),
			)
		}
	}

	rows, err := h.store.Snapshot(r.Context(), usage.Filter{
		TenantID: id.Tenant,
		DomainID: slug,
		HourFrom: from,
		HourTo:   to,
	})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "usage: domain snapshot failed",
			slog.String("tenant", id.Tenant),
			slog.String("domain", slug),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "snapshot failed"})
		return
	}

	counters := make(map[string]uint64, len(usage.CloudCounterNames))
	for _, name := range usage.CloudCounterNames {
		counters[name] = 0
	}
	// Rows are already scoped to this domain by the Snapshot filter
	// (the real domain_id dimension, the multi-domain design — replacing the
	// entity_type trick). Rows recorded before the dimension carry an
	// empty domain_id and so are not counted here; they appear only in
	// the account-level total, which is honest (they predate per-domain
	// attribution).
	for _, row := range rows {
		counters[row.CounterName] += row.Count
	}
	limits := h.planLimits(r.Context(), id.Tenant)

	writeJSON(w, http.StatusOK, domainUsageResponse{
		Domain:               slug,
		From:                 from,
		To:                   to,
		MonthlyProposalLimit: limits.MonthlyProposalLimit,
		Counters:             counters,
	})
}

func (h *UsageHandler) planLimits(ctx context.Context, tenantID string) tenant.PlanLimits {
	if h.tenantStore == nil {
		return tenant.PlanFor(tenant.Tenant{})
	}
	t, err := h.tenantStore.Get(ctx, tenantID)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			return tenant.PlanFor(tenant.Tenant{})
		}
		h.logger.WarnContext(ctx, "usage: tenant plan lookup failed; serving free/dev defaults",
			slog.String("tenant", tenantID),
			slog.String("err", err.Error()),
		)
		return tenant.PlanFor(tenant.Tenant{})
	}
	return tenant.PlanFor(t)
}

// parsePeriod resolves the [from, to] window from the request.
// Default: start of current calendar month UTC -> now. Optional
// ?from / ?to overrides accept RFC3339 timestamps and are
// validated for ordering.
//
// Period semantics keep deliberately simple at v0.1. The
// dashboard / activation tracker / plan-limit logic only need
// one window today; richer windowing arrives with billing.
func parsePeriod(r *http.Request, nowFn func() time.Time) (time.Time, time.Time, error) {
	now := nowFn().UTC()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := now

	if v := strings.TrimSpace(r.URL.Query().Get("from")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("from must be RFC3339")
		}
		from = t.UTC()
	}
	if v := strings.TrimSpace(r.URL.Query().Get("to")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("to must be RFC3339")
		}
		to = t.UTC()
	}
	if !to.After(from) {
		return time.Time{}, time.Time{}, errors.New("to must be after from")
	}
	return from, to, nil
}
