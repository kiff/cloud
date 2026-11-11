package mandate_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/testdb"
)

// Erasing a hold's taf words takes them out of its request and leaves the
// hold, its fingerprint and its status as they were, in both stores.
func TestEraseTafWordsLeavesTheHold(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	stores := map[string]interface {
		mandate.Store
		mandate.TafWordsEraser
	}{"memory": mandate.NewMemoryStore()}
	if testdb.URL(t) != "" {
		pool := requirePostgres(t)
		seedTenant(t, pool, "t-words")
		stores["postgres"] = mandate.NewPostgresStore(pool)
	}
	for name, s := range stores {
		tenant := "t-words"
		e := mandate.Exception{ID: mandate.ExceptionID(tenant, "p-1"), TenantID: tenant, ProposalID: "p-1", Subject: "refunds", Action: "refund",
			Fingerprint: "v2:abc", Status: mandate.ExceptionHeld, CreatedAt: at,
			Request: mandate.Request{Subject: "refunds", Action: "refund", TafAbout: "Order 1042", TafReason: "arrived broken"}}
		if err := s.SaveException(ctx, e); err != nil {
			t.Fatalf("%s: save: %v", name, err)
		}
		if ok, err := s.EraseTafWords(ctx, tenant, "p-1"); err != nil || !ok {
			t.Fatalf("%s: erase: %v %v", name, ok, err)
		}
		got, found, err := s.GetException(ctx, tenant, "p-1")
		if err != nil || !found || got.Request.TafAbout != "" || got.Request.TafReason != "" || got.Fingerprint != "v2:abc" ||
			got.Status != mandate.ExceptionHeld || got.Request.Subject != "refunds" {
			t.Fatalf("%s: after erasing: %+v %v %v", name, got, found, err)
		}
		if ok, _ := s.EraseTafWords(ctx, tenant, "p-1"); ok {
			t.Fatalf("%s: erased twice", name)
		}
		if ok, _ := s.EraseTafWords(ctx, tenant, "p-none"); ok {
			t.Fatalf("%s: erased a hold that isn't", name)
		}
	}
}
