package usage

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"
)

// Aggregator buckets runtime counter increments in-memory keyed by
// (tenant_id, counter_name, entity_type, hour_bucket) and flushes
// to a Store on a configurable interval. Flushes are idempotent;
// a flush failure leaves the in-memory state intact for the next
// attempt.
//
// The hot path is cheap: a map lookup + atomic add. No DB roundtrip.
//
// Lifecycle:
//
//	agg := usage.NewAggregator(store, usage.Options{Interval: 30*time.Second, Now: time.Now})
//	agg.Start(ctx)
//	defer agg.Stop(shutdownCtx)
//	rec := agg.RecorderFor(tenantID)  // pass to runtime.Config.Metrics
type Aggregator struct {
	store    Store
	interval time.Duration
	now      func() time.Time

	mu      sync.Mutex
	current map[aggKey]*atomic.Uint64

	stopCh chan struct{}
	doneCh chan struct{}

	started atomic.Bool
}

type aggKey struct {
	TenantID    string
	CounterName string
	EntityType  string
	DomainID    string
	HourBucket  time.Time
}

// Options configure an Aggregator. Zero values are sensible defaults
// for production wiring.
type Options struct {
	// Interval between flushes. Default 30 seconds.
	Interval time.Duration
	// Now returns the current time. Override in tests; defaults to
	// time.Now.
	Now func() time.Time
}

// NewAggregator constructs an Aggregator that writes through to
// store. Call Start to begin periodic flushes.
func NewAggregator(store Store, opts Options) *Aggregator {
	a := &Aggregator{
		store:    store,
		interval: opts.Interval,
		now:      opts.Now,
		current:  map[aggKey]*atomic.Uint64{},
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	if a.interval <= 0 {
		a.interval = 30 * time.Second
	}
	if a.now == nil {
		a.now = time.Now
	}
	return a
}

// Start launches the periodic flush goroutine. Calling Start more
// than once is a no-op.
func (a *Aggregator) Start(ctx context.Context) {
	if !a.started.CompareAndSwap(false, true) {
		return
	}
	go a.runLoop(ctx)
}

// Stop signals the flush goroutine to exit. It performs one final
// flush before returning so in-flight increments are not lost.
// The provided ctx bounds the final flush so a slow or unreachable
// store cannot hang shutdown indefinitely. Returns the error from
// the final flush, or nil. If ctx is already done, the final flush
// is skipped and ctx.Err() is returned.
func (a *Aggregator) Stop(ctx context.Context) error {
	if !a.started.Load() {
		if err := ctx.Err(); err != nil {
			return err
		}
		return a.flush(ctx)
	}
	close(a.stopCh)
	<-a.doneCh
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.flush(ctx)
}

// RecorderFor returns a runtime.MetricsRecorder bound to tenantID
// (and no domain). The returned value is small and safe to share
// across goroutines. Each call to its Inc method records into the
// Aggregator's in-memory state without blocking on a flush.
func (a *Aggregator) RecorderFor(tenantID string) kiffruntime.MetricsRecorder {
	return &Recorder{tenantID: tenantID, aggregator: a}
}

// RecorderForDomain returns a recorder bound to (tenantID, domainID)
// so every counter it records carries the domain dimension. The per-(tenant,domain) runtime wires one of these so
// framework counters are attributed to the domain that emitted them;
// the cloud proposal counters pass the resolved domain the same way.
// An empty domainID is the tenant-wide / no-domain case.
func (a *Aggregator) RecorderForDomain(tenantID, domainID string) kiffruntime.MetricsRecorder {
	return &Recorder{tenantID: tenantID, domainID: domainID, aggregator: a}
}

// Increment is the internal entrypoint Recorder calls.
func (a *Aggregator) increment(tenantID, counterName, entityType, domainID string, n uint64) {
	if entityType == "" {
		// The framework always passes EntityType. Empty falls back
		// to "unknown" so the (counter, entity) cardinality stays
		// bounded; it would otherwise create a wildcard row.
		entityType = "unknown"
	}
	key := aggKey{
		TenantID:    tenantID,
		CounterName: counterName,
		EntityType:  entityType,
		DomainID:    domainID,
		HourBucket:  HourBucket(a.now()),
	}
	a.mu.Lock()
	c, ok := a.current[key]
	if !ok {
		c = new(atomic.Uint64)
		a.current[key] = c
	}
	a.mu.Unlock()
	c.Add(n)
}

// flush drains the in-memory map into a slice and pushes it through
// the Store. On store error, the in-memory state is restored so the
// next flush retries.
func (a *Aggregator) flush(ctx context.Context) error {
	a.mu.Lock()
	if len(a.current) == 0 {
		a.mu.Unlock()
		return nil
	}
	drain := a.current
	a.current = make(map[aggKey]*atomic.Uint64, len(drain))
	a.mu.Unlock()

	counters := make([]Counter, 0, len(drain))
	for k, v := range drain {
		count := v.Load()
		if count == 0 {
			continue
		}
		counters = append(counters, Counter{
			TenantID:    k.TenantID,
			CounterName: k.CounterName,
			EntityType:  k.EntityType,
			DomainID:    k.DomainID,
			HourBucket:  k.HourBucket,
			Count:       count,
		})
	}

	if err := a.store.Flush(ctx, counters); err != nil {
		// Put the drained values back so the next flush retries.
		a.mu.Lock()
		for k, v := range drain {
			existing, ok := a.current[k]
			if !ok {
				a.current[k] = v
				continue
			}
			existing.Add(v.Load())
		}
		a.mu.Unlock()
		return err
	}
	return nil
}

// FlushNow runs a flush immediately. Useful for ops tooling and
// for tests that want a deterministic flush boundary.
func (a *Aggregator) FlushNow(ctx context.Context) error {
	return a.flush(ctx)
}

func (a *Aggregator) runLoop(ctx context.Context) {
	defer close(a.doneCh)
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		case <-ticker.C:
			_ = a.flush(ctx)
		}
	}
}

// Recorder implements runtime.MetricsRecorder for one tenant,
// optionally bound to one domain.
type Recorder struct {
	tenantID   string
	domainID   string
	aggregator *Aggregator
}

// Inc satisfies runtime.MetricsRecorder. The framework passes
// entity_type as the only Attr at v0.1; we ignore other attributes
// so the cardinality of cloud_meta.tenant_usage stays predictable.
// The domain dimension is carried by the recorder's binding, not an
// attr, so a caller cannot fan out rows by varying a body field.
func (r *Recorder) Inc(name string, n uint64, attrs ...kiffruntime.Attr) {
	if r == nil || r.aggregator == nil || n == 0 {
		return
	}
	entityType := ""
	for _, a := range attrs {
		if a.Key == "entity_type" {
			entityType = a.Value
		}
	}
	r.aggregator.increment(r.tenantID, name, entityType, r.domainID, n)
}
