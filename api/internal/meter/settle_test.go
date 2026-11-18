package meter_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/tenant"
)

// Each counted call's place in its period, and settling it once.
func TestUnsettledPlacesAndSettles(t *testing.T) {
	ctx := context.Background()
	pool := fixture(t)
	id := tenant.NewID()
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.tenants VALUES ($1)`, id); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.billable_calls (tenant_id, key, agent_id, tool, unit, counted, free, at)
		SELECT $1, 'k' || lpad(g::text, 2, '0'), 'taf', 'refund', 'task', g <> 3, CASE WHEN g = 3 THEN 'failed' ELSE '' END, $2::timestamptz + g * interval '1 minute'
		  FROM generate_series(1, 5) g`, id, at); err != nil {
		t.Fatal(err)
	}
	m := meter.NewPostgres(pool)
	from, to := at, at.Add(time.Hour)
	got, err := m.Unsettled(ctx, id, meter.UnitTask, from, to)
	if err != nil || len(got) != 4 || got[0].Key != "k01" || got[2].Key != "k04" || got[2].Place != 3 {
		t.Fatalf("unsettled: %+v %v; the free call has no place", got, err)
	}
	if err := m.Settle(ctx, id, []string{"k01", "k02"}); err != nil {
		t.Fatal(err)
	}
	// Places count the settled calls too.
	if got, _ := m.Unsettled(ctx, id, meter.UnitTask, from, to); len(got) != 2 || got[0].Key != "k04" || got[0].Place != 3 {
		t.Fatalf("after settling two: %+v", got)
	}
	if ids, _ := m.Pending(ctx); !contains(ids, id) {
		t.Fatalf("pending: %v", ids)
	}
	if err := m.SettleBefore(ctx, id, to); err != nil {
		t.Fatal(err)
	}
	if ids, _ := m.Pending(ctx); contains(ids, id) {
		t.Fatalf("still pending after settling all: %v", ids)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
