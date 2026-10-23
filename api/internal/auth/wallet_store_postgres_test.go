package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
)

// requirePostgresWithWallets is the wallet-test analogue of
// requirePostgres in apikey_store_postgres_test.go. It applies the
// cloud-meta + wallets DDL.
func requirePostgresWithWallets(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := requirePostgres(t)
	if err := auth.ApplyWalletsSchema(context.Background(), pool); err != nil {
		t.Fatalf("ApplyWalletsSchema: %v", err)
	}
	return pool
}

func truncateWallets(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for _, table := range []string{"cloud_meta.wallet_nonces", "cloud_meta.user_wallets"} {
		if _, err := pool.Exec(context.Background(), "TRUNCATE "+table); err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}
}

func TestPostgresWalletNonceStore_Roundtrip(t *testing.T) {
	pool := requirePostgresWithWallets(t)
	truncateWallets(t, pool)
	store := auth.NewPostgresWalletNonceStore(pool)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	rec := auth.WalletNonce{
		Nonce:     "nonce-roundtrip",
		Subject:   "user_postgres",
		Address:   "0x1111111111111111111111111111111111111111",
		ChainID:   8453,
		IssuedAt:  now,
		ExpiresAt: now.Add(10 * time.Minute),
	}
	if err := store.Insert(ctx, rec); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := store.Get(ctx, rec.Nonce)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Subject != rec.Subject || got.Address != rec.Address || got.ChainID != rec.ChainID {
		t.Errorf("retrieved nonce mismatch: got %+v", got)
	}

	// MarkUsed is compare-and-swap: first call wins, second returns
	// ErrWalletNonceReplayed.
	at := now.Add(time.Minute).Truncate(time.Microsecond)
	if err := store.MarkUsed(ctx, rec.Nonce, at); err != nil {
		t.Fatalf("MarkUsed: %v", err)
	}
	if err := store.MarkUsed(ctx, rec.Nonce, at); !errors.Is(err, auth.ErrWalletNonceReplayed) {
		t.Fatalf("second MarkUsed should return ErrWalletNonceReplayed, got %v", err)
	}

	got, _ = store.Get(ctx, rec.Nonce)
	if got.UsedAt == nil || !got.UsedAt.Equal(at) {
		t.Errorf("UsedAt: got %v, want %v", got.UsedAt, at)
	}
}

func TestPostgresWalletNonceStore_NotFound(t *testing.T) {
	pool := requirePostgresWithWallets(t)
	truncateWallets(t, pool)
	store := auth.NewPostgresWalletNonceStore(pool)
	if _, err := store.Get(context.Background(), "missing"); !errors.Is(err, auth.ErrWalletNonceNotFound) {
		t.Fatalf("expected ErrWalletNonceNotFound, got %v", err)
	}
	if err := store.MarkUsed(context.Background(), "missing", time.Now().UTC()); !errors.Is(err, auth.ErrWalletNonceNotFound) {
		t.Fatalf("MarkUsed missing should return ErrWalletNonceNotFound, got %v", err)
	}
}

func TestPostgresWalletStore_Roundtrip(t *testing.T) {
	pool := requirePostgresWithWallets(t)
	truncateWallets(t, pool)
	store := auth.NewPostgresWalletStore(pool)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	link := auth.WalletLink{
		Address:  "0x2222222222222222222222222222222222222222",
		ChainID:  8453,
		LinkedAt: now,
	}
	if err := store.Upsert(ctx, "user_a", link); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.Get(ctx, "user_a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Address != link.Address || got.ChainID != link.ChainID {
		t.Errorf("link mismatch: %+v", got)
	}

	// Upsert is idempotent on the (subject) key — replacing the link
	// with a new address overwrites cleanly.
	link2 := auth.WalletLink{
		Address:  "0x3333333333333333333333333333333333333333",
		ChainID:  1,
		LinkedAt: now.Add(time.Minute),
	}
	if err := store.Upsert(ctx, "user_a", link2); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	got, _ = store.Get(ctx, "user_a")
	if got.Address != link2.Address || got.ChainID != link2.ChainID {
		t.Errorf("upsert did not replace: %+v", got)
	}

	// Delete is idempotent.
	if err := store.Delete(ctx, "user_a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(ctx, "user_a"); err != nil {
		t.Fatalf("second Delete should be idempotent: %v", err)
	}
	if _, err := store.Get(ctx, "user_a"); !errors.Is(err, auth.ErrWalletNotLinked) {
		t.Fatalf("after Delete, Get should return ErrWalletNotLinked; got %v", err)
	}
}

// silence unused-import warning from tenant — we don't use it directly
// in this file but the helper depends on it through requirePostgres.
var _ = tenant.NewPostgresStore
