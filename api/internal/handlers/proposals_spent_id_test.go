package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/mandate"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/templates"
	"github.com/kiff/cloud/api/internal/tenant"
)

// flakyQueryAudit delegates to the runtime's audit store but fails
// Query while failing is set, which is how the decide path's
// idempotency cache misses: maybeDedupe falls through to a fresh
// evaluation when its lookup errors.
type flakyQueryAudit struct {
	kiffaudit.Store
	failing *atomic.Bool
}

func (a flakyQueryAudit) Query(ctx context.Context, f kiffaudit.Filter) ([]kiffaudit.Record, error) {
	if a.failing.Load() {
		return nil, errors.New("audit store unavailable")
	}
	return a.Store.Query(ctx, f)
}

// regression on the decide path: once an id has drawn on a card,
// a request under that id is never allowed from a fresh evaluation —
// not the same request, and not a larger one for another entity — when
// the cached decision cannot be read. The cache, when it can answer,
// still returns the recorded decision.
func TestSpentID_CacheMissFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpl, err := templates.Get("card-refund")
	if err != nil {
		t.Fatal(err)
	}
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-spent-id", Status: tenant.StatusActive}
	if err := tenantStore.Create(ctx, tnt); err != nil {
		t.Fatal(err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, tmpl.YAML); err != nil {
		t.Fatal(err)
	}
	rt := cloudruntime.New(cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry()))
	resolved, err := rt.ForDomain(ctx, tnt.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, order := range []string{"o1", "o2"} {
		if _, err := resolved.IngestRaw(ctx, kiffadapter.RawInput{
			ID: "paid-" + order, Adapter: "cloud", Type: "ORDER_PAID", Source: "test",
			ReceivedAt: time.Now().UTC(), EntityID: order, EntityType: "Order",
			ActorID: "a", Metadata: kiffevent.Metadata{TraceID: "t-" + order}, Payload: map[string]any{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	failing := &atomic.Bool{}
	resolved.Audit = flakyQueryAudit{Store: resolved.Audit, failing: failing}

	store := mandate.NewMemoryStore()
	if err := store.Issue(ctx, mandate.Mandate{
		ID: "refund-agent-refunds", TenantID: tnt.ID, Subject: "refund-agent", Domain: "card-refund",
		Grants: []mandate.Grant{{Action: "REFUND_ORDER"}},
		Aggregates: []mandate.Aggregate{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"},
			Limit:    10000, Window: mandate.WindowCalendarDay,
		}},
		ValidFrom: time.Now().Add(-time.Hour),
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	h := handlers.NewProposalsHandler(rt, handlers.WithProposalsMandates(mandate.NewAuthorizer(store, nil, nil)))
	inner := http.NewServeMux()
	h.Register(inner)
	id := runtimeKey("refund-agent")
	id.Tenant = tnt.ID
	serve := withFakeIdentity(inner, id)

	send := func(entity string, amount int) string {
		rec := drivePost(t, serve, "/v1/proposals/decide", map[string]any{
			"id": "refund-stable", "entity_id": entity, "entity_type": "Order", "action_name": "REFUND_ORDER",
			"actor_id": "refund-agent", "parameters": map[string]any{"amount_cents": amount},
		})
		return rec.Body.String()
	}

	if body := send("o1", 500); !strings.Contains(body, `"outcome":"allowed"`) {
		t.Fatalf("first request: %s", body)
	}

	failing.Store(true)
	for _, c := range []struct {
		entity string
		amount int
	}{{"o1", 500}, {"o2", 5000}} {
		body := send(c.entity, c.amount)
		if strings.Contains(body, `"outcome":"allowed"`) || !strings.Contains(body, "mandate_proposal_reused") {
			t.Errorf("cache miss, %s €%d under the spent id: %s", c.entity, c.amount/100, body)
		}
	}
	draws, err := store.Draws(ctx, tnt.ID, "refund-agent-refunds", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(draws) != 1 || draws[0].Amount != 500 || draws[0].EntityID != "o1" {
		t.Fatalf("the ledger changed under cache misses: %+v", draws)
	}
}

// Cross-actor, no-card replay: an id spent by the card holder is reused
// by another agent that holds no cards, on ISSUE_CREDIT (no card
// required). Neither past the idempotency window nor during a cache
// failure may it get a fresh allowed.
func TestSpentID_ReusedByAnAgentWithNoCards(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		window     time.Duration
		cacheFails bool
	}{
		{"after the idempotency window", time.Nanosecond, false},
		{"during a cache failure", 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			rig := newSpentRig(t, c.window)
			if body := rig.send("refund-agent", "REFUND_ORDER", "o1", 500); !strings.Contains(body, `"outcome":"allowed"`) {
				t.Fatalf("holder's request: %s", body)
			}
			rig.failing.Store(c.cacheFails)
			body := rig.send("other-agent", "ISSUE_CREDIT", "o2", 5000)
			if strings.Contains(body, `"outcome":"allowed"`) || !strings.Contains(body, "mandate_proposal_reused") {
				t.Errorf("no-card agent reusing the spent id: %s", body)
			}
		})
	}
}

type spentRig struct {
	serve   func(agent string) http.Handler
	failing *atomic.Bool
	store   *mandate.MemoryStore
	tenant  string
	t       *testing.T
}

func (r spentRig) draws(t *testing.T) []mandate.Draw {
	t.Helper()
	d, err := r.store.Draws(context.Background(), r.tenant, "refund-agent-refunds", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (r spentRig) send(agent, action, entity string, amount int) string {
	r.t.Helper()
	return drivePost(r.t, r.serve(agent), "/v1/proposals/decide", map[string]any{
		"id": "refund-stable", "entity_id": entity, "entity_type": "Order", "action_name": action,
		"actor_id": agent, "parameters": map[string]any{"amount_cents": amount},
	}).Body.String()
}

// newSpentRig is the card-refund tenant of TestSpentID_CacheMissFailsClosed
// with a flaky audit query and, when window > 0, a short idempotency
// window. refund-agent holds a REFUND_ORDER card; nobody else holds any.
func newSpentRig(t *testing.T, window time.Duration) spentRig {
	t.Helper()
	ctx := context.Background()
	tmpl, err := templates.Get("card-refund")
	if err != nil {
		t.Fatal(err)
	}
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-spent-cross", Status: tenant.StatusActive}
	if err := tenantStore.Create(ctx, tnt); err != nil {
		t.Fatal(err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, tmpl.YAML); err != nil {
		t.Fatal(err)
	}
	rt := cloudruntime.New(cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry()))
	resolved, err := rt.ForDomain(ctx, tnt.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, order := range []string{"o1", "o2"} {
		if _, err := resolved.IngestRaw(ctx, kiffadapter.RawInput{
			ID: "paid-" + order, Adapter: "cloud", Type: "ORDER_PAID", Source: "test",
			ReceivedAt: time.Now().UTC(), EntityID: order, EntityType: "Order",
			ActorID: "a", Metadata: kiffevent.Metadata{TraceID: "t-" + order}, Payload: map[string]any{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	failing := &atomic.Bool{}
	resolved.Audit = flakyQueryAudit{Store: resolved.Audit, failing: failing}

	store := mandate.NewMemoryStore()
	if err := store.Issue(ctx, mandate.Mandate{
		ID: "refund-agent-refunds", TenantID: tnt.ID, Subject: "refund-agent", Domain: "card-refund",
		Grants: []mandate.Grant{{Action: "REFUND_ORDER"}},
		Aggregates: []mandate.Aggregate{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"},
			Limit:    10000, Window: mandate.WindowCalendarDay,
		}},
		ValidFrom: time.Now().Add(-time.Hour),
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	opts := []handlers.ProposalsOption{handlers.WithProposalsMandates(mandate.NewAuthorizer(store, nil, nil))}
	if window > 0 {
		opts = append(opts, handlers.WithProposalsIdempotencyWindow(window))
	}
	h := handlers.NewProposalsHandler(rt, opts...)
	inner := http.NewServeMux()
	h.Register(inner)
	return spentRig{
		serve: func(agent string) http.Handler {
			id := runtimeKey(agent)
			id.Tenant = tnt.ID
			return withFakeIdentity(inner, id)
		},
		failing: failing,
		store:   store,
		tenant:  tnt.ID,
		t:       t,
	}
}

// Concurrent decide requests under one new proposal id, with no outer
// idempotency lock wired and the cache unable to answer: exactly one
// fresh allowed, one draw; the rest are refused as a reused id.
func TestSpentID_ConcurrentSameIDWithoutOuterLock(t *testing.T) {
	t.Parallel()
	rig := newSpentRig(t, 0)
	rig.failing.Store(true)

	const n = 12
	var wg sync.WaitGroup
	bodies := make([]string, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			bodies[i] = rig.send("refund-agent", "REFUND_ORDER", "o1", 500)
		}(i)
	}
	close(start)
	wg.Wait()

	allowed := 0
	for i, b := range bodies {
		switch {
		case strings.Contains(b, `"outcome":"allowed"`):
			allowed++
		case strings.Contains(b, "mandate_proposal_reused"):
		default:
			t.Errorf("request %d: %s", i, b)
		}
	}
	if allowed != 1 {
		t.Errorf("%d concurrent requests allowed under one id, want 1", allowed)
	}
	if n := len(rig.draws(t)); n != 1 {
		t.Errorf("draws = %d, want 1", n)
	}
}
