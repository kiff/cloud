package tenant_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/tenant"
)

func TestMemorySubjectStore_GetReturnsErrSubjectNotMapped(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemorySubjectStore()
	if _, err := store.Get(context.Background(), "subj-1"); !errors.Is(err, tenant.ErrSubjectNotMapped) {
		t.Fatalf("err = %v, want ErrSubjectNotMapped", err)
	}
}

func TestMemorySubjectStore_SetAndGet(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemorySubjectStore()
	now := time.Now().UTC()
	if err := store.Set(context.Background(), "subj-1", "ten-1", now); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get(context.Background(), "subj-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Subject != "subj-1" || got.TenantID != "ten-1" {
		t.Fatalf("got %+v, want subject=subj-1 tenant=ten-1", got)
	}
}

func TestMemorySubjectStore_SetIsIdempotent(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemorySubjectStore()
	now := time.Now().UTC()
	if err := store.Set(context.Background(), "subj-1", "ten-1", now); err != nil {
		t.Fatalf("first Set: %v", err)
	}
	if err := store.Set(context.Background(), "subj-1", "ten-1", now.Add(time.Hour)); err != nil {
		t.Fatalf("second Set with same tenant: %v", err)
	}
}

func TestMemorySubjectStore_SetReturnsErrAlreadyMappedOnDifferentTenant(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemorySubjectStore()
	now := time.Now().UTC()
	if err := store.Set(context.Background(), "subj-1", "ten-1", now); err != nil {
		t.Fatalf("first Set: %v", err)
	}
	err := store.Set(context.Background(), "subj-1", "ten-2", now)
	if !errors.Is(err, tenant.ErrSubjectAlreadyMapped) {
		t.Fatalf("err = %v, want ErrSubjectAlreadyMapped", err)
	}
}

func TestMemorySubjectStore_SetRejectsEmptyArguments(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemorySubjectStore()
	now := time.Now().UTC()
	if err := store.Set(context.Background(), "", "ten-1", now); err == nil {
		t.Fatalf("expected error for empty subject")
	}
	if err := store.Set(context.Background(), "subj-1", "", now); err == nil {
		t.Fatalf("expected error for empty tenant")
	}
}
