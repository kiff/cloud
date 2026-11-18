package auth_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
)

// KIFF's records of a connection's approval verified inside an open window
// (kiff/tafs window_required), in Postgres and in memory: kept as first
// written (never moved later), used once by a conditional write even by two
// parallel uses, and old rows deleted with the next record.
func TestApprovalCheckStores(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		approvalCheckStoreContract(t, auth.NewMemoryApprovalCheckStore(), "t1", "dev-1")
	})
	t.Run("postgres", func(t *testing.T) {
		pool := requirePostgres(t)
		ctx := context.Background()
		// The devices schema alters developer_sessions, so it comes after it, as at startup.
		if err := auth.ApplyDeveloperSessionsSchema(ctx, pool); err != nil {
			t.Fatal(err)
		}
		if err := auth.ApplyDevicesSchema(ctx, pool); err != nil {
			t.Fatal(err)
		}
		if err := auth.ApplyApprovalChecksSchema(ctx, pool); err != nil {
			t.Fatal(err)
		}
		if err := auth.ApplyApprovalChecksSchema(ctx, pool); err != nil {
			t.Fatalf("second ApplyApprovalChecksSchema: %v", err)
		}
		tenantID := tenant.NewID()
		seedTenant(t, pool, tenantID)
		phone := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: tenantID, Subject: "human-1", Name: "tafs on Pixel", Platform: "android",
			PublicKey: p256(t), CreatedAt: time.Now().UTC().Truncate(time.Second)}
		if err := auth.NewPostgresDeviceStore(pool).Insert(ctx, phone); err != nil {
			t.Fatal(err)
		}
		approvalCheckStoreContract(t, auth.NewPostgresApprovalCheckStore(pool), tenantID, phone.ID)
	})
}

func approvalCheckStoreContract(t *testing.T, store auth.ApprovalCheckStore, tenantID, device string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	id := auth.ApprovalCheckID([]byte("planque.request.v2\n…"), "sig-1")
	if id == auth.ApprovalCheckID([]byte("planque.request.v2\n…"), "sig-2") {
		t.Fatal("another signature gives the same id")
	}
	if _, err := store.Get(ctx, id); !errors.Is(err, auth.ErrApprovalCheckNotFound) {
		t.Fatalf("absent: %v", err)
	}
	if used, err := store.Use(ctx, id, now); err != nil || used {
		t.Fatalf("using an absent record: %v %v", used, err)
	}
	c := auth.ApprovalCheck{ID: id, Tenant: tenantID, DeviceID: device, Grant: "grt_1", Window: "win_1", CheckedAt: now}
	if err := store.Record(ctx, c); err != nil {
		t.Fatal(err)
	}
	// Recorded again later: it stays as first written.
	if err := store.Record(ctx, auth.ApprovalCheck{ID: id, Tenant: tenantID, DeviceID: device, Grant: "grt_2", Window: "win_2", CheckedAt: now.Add(10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, id)
	if err != nil || got.Grant != "grt_1" || got.Window != "win_1" || !got.CheckedAt.Equal(now) || got.UsedAt != nil {
		t.Fatalf("read back: %v %+v", err, got)
	}
	// Two parallel uses: one.
	results := make([]bool, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			used, err := store.Use(ctx, id, now.Add(time.Minute))
			if err != nil {
				t.Error(err)
			}
			results[i] = used
		}(i)
	}
	wg.Wait()
	if results[0] == results[1] {
		t.Fatalf("two parallel uses: %v, want exactly one", results)
	}
	if got, _ := store.Get(ctx, id); got.UsedAt == nil || !got.UsedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("used at: %+v", got.UsedAt)
	}
	// A record a day old goes with the next one.
	newer := auth.ApprovalCheck{ID: auth.ApprovalCheckID([]byte("later"), "sig"), Tenant: tenantID, DeviceID: device, Grant: "grt_3", Window: "win_3",
		CheckedAt: now.Add(25 * time.Hour)}
	if err := store.Record(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, id); !errors.Is(err, auth.ErrApprovalCheckNotFound) {
		t.Fatalf("a day-old record: %v, want deleted", err)
	}
	if _, err := store.Get(ctx, newer.ID); err != nil {
		t.Fatalf("the new record: %v", err)
	}
}
