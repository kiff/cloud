package mandate_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// sealedStore is a store that holds calls and keeps their releases.
type sealedStore interface {
	mandate.Store
	mandate.SealedReleases
}

func releaseAt(exp time.Time) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"chrome_id":"chr_1","op":"op_1.1","origin":"https://shop.example","nonce":"bm9uY2Utbm9uY2Utbm9uY2U","exp":%d,"sig":"MEUCIQ"}`, exp.UnixMilli()))
}

// A held sealed fill's release is kept only once the hold is approved, handed
// over once, and never after its exp, on both stores.
func checkSealedReleases(t *testing.T, store sealedStore, tenant string) {
	t.Helper()
	ctx := context.Background()
	if err := store.Issue(ctx, pgHomeCard(tenant, "card-1", 50000, 500000), "owner"); err != nil {
		t.Fatal(err)
	}
	now := base
	a := mandate.NewAuthorizer(store, nil, func() time.Time { return now })
	held, err := a.Authorize(ctx, tenant, "p-fill", pgRefund(150000))
	if err != nil || held.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("held = %+v, %v", held, err)
	}

	// Still held: nothing is kept.
	if err := store.KeepSealedRelease(ctx, tenant, held.ExceptionID, releaseAt(now.Add(time.Minute))); !errors.Is(err, mandate.ErrExceptionState) {
		t.Fatalf("a release kept on a held call: %v", err)
	}
	if err := store.KeepSealedRelease(ctx, tenant, "no-such-hold", releaseAt(now.Add(time.Minute))); !errors.Is(err, mandate.ErrExceptionNotFound) {
		t.Fatalf("a release kept on no hold: %v", err)
	}

	if _, err := store.ApproveOnce(ctx, tenant, held.ExceptionID, "owner", now); err != nil {
		t.Fatal(err)
	}
	if err := store.KeepSealedRelease(ctx, tenant, held.ExceptionID, releaseAt(now.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	// Another account reads nothing of it.
	if got, err := store.TakeSealedRelease(ctx, "t-other", "p-fill", now); err != nil || got != nil {
		t.Fatalf("another account took %s, %v", got, err)
	}
	got, err := store.TakeSealedRelease(ctx, tenant, "p-fill", now)
	if err != nil || len(got) == 0 {
		t.Fatalf("take: %s, %v", got, err)
	}
	if again, err := store.TakeSealedRelease(ctx, tenant, "p-fill", now); err != nil || again != nil {
		t.Fatalf("taken twice: %s, %v", again, err)
	}

	// Kept past its exp: taking it gives nothing, and forgets it.
	if err := store.KeepSealedRelease(ctx, tenant, held.ExceptionID, releaseAt(now.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if late, err := store.TakeSealedRelease(ctx, tenant, "p-fill", now.Add(2*time.Minute)); err != nil || late != nil {
		t.Fatalf("a late release was handed over: %s, %v", late, err)
	}
	if gone, _ := store.TakeSealedRelease(ctx, tenant, "p-fill", now); gone != nil {
		t.Fatalf("a late take kept it: %s", gone)
	}
}

func TestSealedReleasesInMemory(t *testing.T) {
	checkSealedReleases(t, mandate.NewMemoryStore(), "t-sealed")
}

func TestPostgres_SealedReleases(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-sealed")
	store := mandate.NewPostgresStore(pool)
	checkSealedReleases(t, store, "t-sealed")

	// The hold sweep drops a release nobody took once its exp is past.
	ctx := context.Background()
	now := base
	a := mandate.NewAuthorizer(store, nil, func() time.Time { return now })
	held, err := a.Authorize(ctx, "t-sealed", "p-swept", pgRefund(150000))
	if err != nil || held.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("held = %+v, %v", held, err)
	}
	if _, err := store.ApproveOnce(ctx, "t-sealed", held.ExceptionID, "owner", now); err != nil {
		t.Fatal(err)
	}
	if err := store.KeepSealedRelease(ctx, "t-sealed", held.ExceptionID, releaseAt(now.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExpireHeld(ctx, now.Add(2*time.Minute), 10); err != nil {
		t.Fatal(err)
	}
	var left bool
	if err := pool.QueryRow(ctx, `SELECT sealed_release IS NOT NULL FROM cloud_meta.mandate_exceptions WHERE tenant_id = 't-sealed' AND id = $1`, held.ExceptionID).Scan(&left); err != nil || left {
		t.Fatalf("the sweep left a release past its exp: %v %v", left, err)
	}
}
