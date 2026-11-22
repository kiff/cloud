package codesignin_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/codesignin"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/testdb"
)

// The Postgres store: a code is used once even by racing verifies, counts
// add up per window, a new code for the same browser replaces the old one,
// and the sweep and erasure take what they should.
func TestPostgresStore(t *testing.T) {
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping Postgres-backed test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := codesignin.ApplySchema(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	s := codesignin.NewPostgres(pool)
	now := time.Now().UTC().Truncate(time.Second)
	who := "user_" + tenant.NewID()

	if set, err := s.Setting(ctx, who); err != nil || set.EmailCode {
		t.Fatalf("the default is off: %+v %v", set, err)
	}
	if err := s.SetSetting(ctx, who, codesignin.Setting{EmailCode: true, ChangedAt: now}); err != nil {
		t.Fatal(err)
	}
	if set, _ := s.Setting(ctx, who); !set.EmailCode || !set.ChangedAt.Equal(now) {
		t.Fatalf("setting: %+v", set)
	}

	key := "test-" + who
	for i := 1; i <= 3; i++ {
		if n, err := s.Add(ctx, key, now.Truncate(time.Hour)); err != nil || n != i {
			t.Fatalf("add %d: %d %v", i, n, err)
		}
	}
	if n, _ := s.Count(ctx, key, now.Truncate(time.Hour).Add(-time.Hour)); n != 0 {
		t.Errorf("another window: %d", n)
	}

	flow, addr := "flow-"+who, "addr-"+who
	if err := s.PutCode(ctx, codesignin.Code{ID: "c1-" + who, Flow: flow, Address: addr, Subject: who, MAC: []byte{1}, ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutCode(ctx, codesignin.Code{ID: "c2-" + who, Flow: flow, Address: addr, Subject: who, MAC: []byte{2}, ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	c, ok, err := s.FindCode(ctx, flow, addr, now)
	if err != nil || !ok || c.ID != "c2-"+who || c.MAC[0] != 2 {
		t.Fatalf("the newer code replaces the older: %+v %v %v", c, ok, err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if used, err := s.UseCode(ctx, c.ID, now); err == nil && used {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("a code was used %d times", wins)
	}

	if err := s.PutCode(ctx, codesignin.Code{ID: "c3-" + who, Flow: flow, Address: addr, Subject: who, MAC: []byte{3}, ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := s.NoteSignIn(ctx, who, now); err != nil {
		t.Fatal(err)
	}
	if at, ok, _ := s.LastSignIn(ctx, who); !ok || !at.Equal(now) {
		t.Fatalf("code sign-in: %v %v", at, ok)
	}
	if err := s.Sweep(ctx, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.FindCode(ctx, flow, addr, now); ok {
		t.Error("a lapsed code survived the sweep")
	}
	if err := s.Sweep(ctx, now.Add(codesignin.CountsKept+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Count(ctx, key, now.Truncate(time.Hour)); n != 0 {
		t.Errorf("a count older than a day survived: %d", n)
	}
	if _, ok, _ := s.LastSignIn(ctx, who); ok {
		t.Error("an old code sign-in survived the sweep")
	}
	if err := s.EraseSetting(ctx, who); err != nil {
		t.Fatal(err)
	}
	if set, _ := s.Setting(ctx, who); set.EmailCode {
		t.Error("the setting survived erasure")
	}
}
