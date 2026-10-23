package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

func newRecord(t *testing.T, tenant, id, subject string) APIKeyRecord {
	t.Helper()
	hash := sha256.Sum256([]byte(id + ":" + tenant + ":" + subject))
	return APIKeyRecord{
		ID:        id,
		Tenant:    tenant,
		Subject:   subject,
		Label:     "test key " + id,
		Roles:     []string{"operator"},
		KeyHash:   hash,
		CreatedAt: time.Now().UTC(),
	}
}

func TestMemoryAPIKeyStore_InsertAndGet(t *testing.T) {
	t.Parallel()
	store := NewMemoryAPIKeyStore()
	ctx := context.Background()
	rec := newRecord(t, "tenant-acme", "key-1", "actor-alice")

	if err := store.Insert(ctx, rec); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := store.GetByHash(ctx, rec.KeyHash)
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if got.ID != rec.ID || got.Tenant != rec.Tenant || got.Subject != rec.Subject {
		t.Errorf("retrieved record mismatch: got %+v, want %+v", got, rec)
	}
	// Mutation isolation.
	got.Roles[0] = "tampered"
	got, _ = store.GetByHash(ctx, rec.KeyHash)
	if got.Roles[0] != "operator" {
		t.Errorf("roles must not be shared between caller and store")
	}
}

func TestMemoryAPIKeyStore_DuplicateID(t *testing.T) {
	t.Parallel()
	store := NewMemoryAPIKeyStore()
	ctx := context.Background()
	rec := newRecord(t, "tenant-acme", "key-1", "actor-alice")
	_ = store.Insert(ctx, rec)
	err := store.Insert(ctx, rec)
	if !errors.Is(err, ErrAPIKeyDuplicate) {
		t.Fatalf("expected ErrAPIKeyDuplicate, got %v", err)
	}
}

func TestMemoryAPIKeyStore_DuplicateHash(t *testing.T) {
	t.Parallel()
	store := NewMemoryAPIKeyStore()
	ctx := context.Background()
	rec := newRecord(t, "tenant-acme", "key-1", "actor-alice")
	_ = store.Insert(ctx, rec)
	clone := rec
	clone.ID = "key-2"
	err := store.Insert(ctx, clone)
	if !errors.Is(err, ErrAPIKeyDuplicate) {
		t.Fatalf("expected ErrAPIKeyDuplicate on hash collision, got %v", err)
	}
}

func TestMemoryAPIKeyStore_GetByHash_NotFound(t *testing.T) {
	t.Parallel()
	store := NewMemoryAPIKeyStore()
	ctx := context.Background()
	var zero [APIKeyHashSize]byte
	zero[0] = 1 // any non-zero hash that wasn't inserted
	_, err := store.GetByHash(ctx, zero)
	if !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("expected ErrAPIKeyNotFound, got %v", err)
	}
}

func TestMemoryAPIKeyStore_RevokedReturnsErrRevoked(t *testing.T) {
	t.Parallel()
	store := NewMemoryAPIKeyStore()
	ctx := context.Background()
	rec := newRecord(t, "tenant-acme", "key-1", "actor-alice")
	if err := store.Insert(ctx, rec); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := store.Revoke(ctx, rec.ID, time.Now().UTC()); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := store.GetByHash(ctx, rec.KeyHash); !errors.Is(err, ErrAPIKeyRevoked) {
		t.Fatalf("after revoke, GetByHash should return ErrAPIKeyRevoked; got %v", err)
	}
	// Idempotent: revoke again is a no-op error.
	if err := store.Revoke(ctx, rec.ID, time.Now().UTC()); err != nil {
		t.Fatalf("second Revoke must be idempotent; got %v", err)
	}
}

func TestMemoryAPIKeyStore_Revoke_NotFound(t *testing.T) {
	t.Parallel()
	store := NewMemoryAPIKeyStore()
	ctx := context.Background()
	err := store.Revoke(ctx, "missing", time.Now().UTC())
	if !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("expected ErrAPIKeyNotFound, got %v", err)
	}
}

func TestMemoryAPIKeyStore_Touch(t *testing.T) {
	t.Parallel()
	store := NewMemoryAPIKeyStore()
	ctx := context.Background()
	rec := newRecord(t, "tenant-acme", "key-1", "actor-alice")
	_ = store.Insert(ctx, rec)
	at := time.Now().UTC()
	if err := store.Touch(ctx, rec.ID, at); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got, _ := store.GetByHash(ctx, rec.KeyHash)
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(at) {
		t.Errorf("LastUsedAt: got %v, want %v", got.LastUsedAt, at)
	}
}

func TestMemoryAPIKeyStore_ListActive_FiltersAndOrders(t *testing.T) {
	t.Parallel()
	store := NewMemoryAPIKeyStore()
	ctx := context.Background()

	t1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	t3 := t2.Add(time.Hour)

	a := newRecord(t, "tenant-a", "key-a1", "actor-1")
	a.CreatedAt = t1
	b := newRecord(t, "tenant-a", "key-a2", "actor-2")
	b.CreatedAt = t2
	c := newRecord(t, "tenant-a", "key-a3", "actor-3")
	c.CreatedAt = t3
	d := newRecord(t, "tenant-b", "key-b1", "actor-1") // wrong tenant — must be filtered

	for _, r := range []APIKeyRecord{a, b, c, d} {
		if err := store.Insert(ctx, r); err != nil {
			t.Fatalf("Insert %s: %v", r.ID, err)
		}
	}
	if err := store.Revoke(ctx, c.ID, t3); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	got, err := store.ListActive(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 active keys for tenant-a, got %d (%v)", len(got), got)
	}
	// Newest first, so b before a.
	if got[0].ID != "key-a2" || got[1].ID != "key-a1" {
		t.Errorf("unexpected order: got %s, %s; want key-a2, key-a1", got[0].ID, got[1].ID)
	}
}

func TestValidateRecordForInsert(t *testing.T) {
	t.Parallel()
	rec := newRecord(t, "tenant-acme", "key-1", "actor-alice")
	if err := validateRecordForInsert(rec); err != nil {
		t.Errorf("happy-path record should validate: %v", err)
	}
	var zeroHash [APIKeyHashSize]byte
	cases := []struct {
		name string
		mut  func(*APIKeyRecord)
	}{
		{"missing ID", func(r *APIKeyRecord) { r.ID = "" }},
		{"missing Tenant", func(r *APIKeyRecord) { r.Tenant = "" }},
		{"missing Subject", func(r *APIKeyRecord) { r.Subject = "" }},
		{"zero CreatedAt", func(r *APIKeyRecord) { r.CreatedAt = time.Time{} }},
		{"zero KeyHash", func(r *APIKeyRecord) { r.KeyHash = zeroHash }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRecord(t, "tenant-acme", "key-1", "actor-alice")
			c.mut(&r)
			if err := validateRecordForInsert(r); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestAPIKeyStore_Interfaces(t *testing.T) {
	t.Parallel()
	var _ APIKeyStore = (*MemoryAPIKeyStore)(nil)
	var _ APIKeyStore = (*PostgresAPIKeyStore)(nil)
}
