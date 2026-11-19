package runtime_test

import (
	"context"
	"github.com/kiff/cloud/api/internal/testdb"
	"strings"
	"testing"
	"time"

	kiffevent "github.com/kiff/kiff/pkg/kiff/event"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
)

// TestE2E_EntityStateSurvivesRestart is a regression test. Entity state
// lives in the runtime's memory; a restart (a new resolver and cache)
// must rebuild it from the stored events of the runtime's own domain,
// never from another domain's events for the same entity id, and never
// from events whose domain was not recorded.
func TestE2E_EntityStateSurvivesRestart(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	dropTenantSchemas(t, pool)
	defer func() {
		dropTenantSchemas(t, pool)
		resetCloudMeta(t, pool)
	}()
	ctx := context.Background()
	databaseURL := testdb.URL(t)
	store := tenant.NewPostgresStore(pool)
	source := domain.NewPostgresSource(pool)

	tt := tenant.Tenant{ID: tenant.NewID(), Slug: "replay", DisplayName: "Replay", Status: tenant.StatusProvisioning}
	if err := store.Create(ctx, tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if err := tenant.NewProvisioner(pool, store).Provision(ctx, tt.ID); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if err := source.Save(ctx, tt.ID, []byte(e2eYAML)); err != nil {
		t.Fatalf("save default: %v", err)
	}
	if err := source.Create(ctx, tt.ID, []byte(strings.Replace(e2eYAML, "domain: refund", "domain: refund-eu", 1))); err != nil {
		t.Fatalf("create second domain: %v", err)
	}

	boot := func() *cloudruntime.TenantRuntime {
		r := cloudruntime.NewCloudResolver(databaseURL, store, source, executors.DefaultRegistry(), nil)
		t.Cleanup(r.Close)
		return cloudruntime.New(r)
	}
	resolve := func(tr *cloudruntime.TenantRuntime, d string) *kiffruntime.Runtime {
		rt, err := tr.ForDomain(ctx, tt.ID, d)
		if err != nil {
			t.Fatalf("resolve %q: %v", d, err)
		}
		return rt
	}
	ingest := func(rt *kiffruntime.Runtime, id, typ string) {
		t.Helper()
		if err := rt.IngestEvent(ctx, kiffevent.Event{ID: id, Type: typ, EntityID: "order-1", EntityType: "Order",
			Source: "test", ActorID: "a", OccurredAt: time.Now().UTC()}); err != nil {
			t.Fatalf("ingest %s: %v", id, err)
		}
	}
	stateOf := func(rt *kiffruntime.Runtime, entity string) (string, int, bool) {
		t.Helper()
		st, ok, err := rt.States.Current(ctx, entity)
		if err != nil {
			t.Fatalf("state of %s: %v", entity, err)
		}
		return st.Value, st.Version, ok
	}

	// Before the restart: order-1 is PAID in the default domain and only
	// CREATED in refund-eu, which uses the same entity id.
	first := boot()
	ingest(resolve(first, ""), "ev-1", "ORDER_PLACED")
	ingest(resolve(first, ""), "ev-2", "ORDER_PAID")
	ingest(resolve(first, "refund-eu"), "ev-eu-1", "ORDER_PLACED")
	// An event stored before event domains were recorded.
	schema := tenant.SchemaNameFor(tt.ID)
	if _, err := pool.Exec(ctx, `INSERT INTO `+schema+`.kiff_events
		(id, type, entity_id, entity_type, source, actor_id, occurred_at)
		VALUES ('ev-legacy', 'ORDER_PLACED', 'order-legacy', 'Order', 'test', 'a', NOW())`); err != nil {
		t.Fatalf("insert legacy event: %v", err)
	}

	// The restart.
	second := boot()
	def, eu := resolve(second, ""), resolve(second, "refund-eu")
	if v, ver, ok := stateOf(def, "order-1"); !ok || v != "PAID" || ver != 2 {
		t.Fatalf("default domain after restart: %s v%d ok=%v, want PAID v2", v, ver, ok)
	}
	if v, _, ok := stateOf(eu, "order-1"); !ok || v != "CREATED" {
		t.Fatalf("refund-eu after restart: %s ok=%v, want CREATED (its own history only)", v, ok)
	}
	if _, _, ok := stateOf(def, "order-legacy"); ok {
		t.Fatal("an event with no recorded domain must not be replayed into a domain")
	}

	// The first event after the restart applies once, on top of history.
	ingest(def, "ev-3", "ORDER_REFUNDED")
	if v, ver, _ := stateOf(def, "order-1"); v != "REFUNDED" || ver != 3 {
		t.Fatalf("after refund: %s v%d, want REFUNDED v3", v, ver)
	}
	// Reads are unchanged: the tenant-wide event list still has every event.
	all, err := def.Events.List(ctx, "order-1")
	if err != nil || len(all) != 4 {
		t.Fatalf("event list = %d events (err %v), want 4 across both domains", len(all), err)
	}
}
