package callabout_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiff/cloud/api/internal/callabout"
	"github.com/kiff/cloud/api/internal/testdb"
)

// Keep, read and erase what calls are about, by account, in both stores:
// asked again, the latest words stand; erased, they're gone.
func TestKeepReadErase(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	stores := map[string]callabout.Store{"memory": callabout.NewMemory()}
	if url := testdb.URL(t); url != "" {
		pool, err := pgxpool.New(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		if err := callabout.ApplySchema(ctx, pool); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM cloud_meta.call_about WHERE tenant_id IN ('t-a', 't-b')`); err != nil {
			t.Fatal(err)
		}
		stores["postgres"] = callabout.NewPostgres(pool)
	}
	for name, s := range stores {
		for _, k := range [][3]string{{"t-a", "p-1", "Order 1042"}, {"t-a", "p-2", "INV-77"}, {"t-b", "p-1", "Order 9"}, {"t-a", "p-1", "Order no. 1042"}} {
			if err := s.Keep(ctx, k[0], k[1], k[2], at); err != nil {
				t.Fatalf("%s: keep: %v", name, err)
			}
		}
		got, err := s.About(ctx, "t-a", []string{"p-1", "p-2", "p-3"})
		if err != nil || !reflect.DeepEqual(got, map[string]string{"p-1": "Order no. 1042", "p-2": "INV-77"}) {
			t.Fatalf("%s: %v %v", name, got, err)
		}
		if ok, err := s.Erase(ctx, "t-a", "p-1"); err != nil || !ok {
			t.Fatalf("%s: erase: %v %v", name, ok, err)
		}
		if ok, _ := s.Erase(ctx, "t-a", "p-1"); ok {
			t.Fatalf("%s: erased twice", name)
		}
		got, _ = s.About(ctx, "t-a", []string{"p-1", "p-2"})
		other, _ := s.About(ctx, "t-b", []string{"p-1"})
		if !reflect.DeepEqual(got, map[string]string{"p-2": "INV-77"}) || other["p-1"] != "Order 9" {
			t.Fatalf("%s: after erasing: %v, other account %v", name, got, other)
		}
	}
}
