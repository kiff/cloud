package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/testdb"
)

type sweepProv struct {
	store  tenant.Store
	called []string
	fail   string
}

func (p *sweepProv) Deprovision(ctx context.Context, id string) error {
	p.called = append(p.called, id)
	if id == p.fail {
		return errors.New("schema drop failed")
	}
	return p.store.SetStatus(ctx, id, tenant.StatusDeleted, nil)
}

func pendingTenant(t *testing.T, store tenant.Store, slug string, graceEnds time.Time) string {
	t.Helper()
	ctx := context.Background()
	id := tenant.NewID()
	if err := store.Create(ctx, tenant.Tenant{ID: id, Slug: slug, Status: tenant.StatusActive}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatus(ctx, id, tenant.StatusDeletedPending, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMetadata(ctx, id, tenant.MetadataKeyGraceWindowEnds, graceEnds.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	return id
}

// The API erases every deleted account whose grace window has ended, on its
// own, as the privacy policy promises: one inside its window is kept, an
// active one is never touched, and a scrub that fails is logged and left
// deleted_pending for the next day's sweep.
func TestSweepExpiredErasesWhatThePolicyPromises(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := tenant.NewMemoryStore()
	ended := pendingTenant(t, store, "ended", now.Add(-time.Hour))
	inside := pendingTenant(t, store, "inside", now.Add(24*time.Hour))
	broken := pendingTenant(t, store, "broken", now.Add(-48*time.Hour))
	if err := store.Create(ctx, tenant.Tenant{ID: tenant.NewID(), Slug: "active", Status: tenant.StatusActive}); err != nil {
		t.Fatal(err)
	}
	prov := &sweepProv{store: store, fail: broken}
	var logs bytes.Buffer
	got := sweepExpiredOnce(ctx, store, prov, now, slog.New(slog.NewTextHandler(&logs, nil)))

	if got.Scrubbed != 1 || got.Failed != 1 || got.Skipped != 1 {
		t.Fatalf("summary: %+v", got)
	}
	for id, want := range map[string]tenant.Status{ended: tenant.StatusDeleted, inside: tenant.StatusDeletedPending, broken: tenant.StatusDeletedPending} {
		if tn, err := store.Get(ctx, id); err != nil || tn.Status != want {
			t.Errorf("tenant %s: %v %v, want %s", id, tn.Status, err, want)
		}
	}
	// The erased account keeps a record of when, past the logs' retention.
	if tn, _ := store.Get(ctx, ended); tn.Metadata[tenant.MetadataKeyErasedAt] != now.Format(time.RFC3339) {
		t.Errorf("erased_at: %v, want %s", tn.Metadata[tenant.MetadataKeyErasedAt], now.Format(time.RFC3339))
	}
	if tn, _ := store.Get(ctx, broken); tn.Metadata[tenant.MetadataKeyErasedAt] != nil {
		t.Errorf("a failed scrub stamped erased_at: %v", tn.Metadata[tenant.MetadataKeyErasedAt])
	}
	for _, want := range []string{"sweep: erased a deleted account", "tenant=" + ended, "sweep: a deleted account wasn't erased", "tenant=" + broken, "schema drop failed"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs missing %q:\n%s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), inside) {
		t.Errorf("an account inside its window was logged:\n%s", logs.String())
	}
}

// A job that scrubs or prunes runs on one API host at a time: while another
// holds its advisory lock, onceAcross skips the run; once released, it runs.
func TestOnceAcrossRunsOnOneHost(t *testing.T) {
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	other, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('kiff.test-once', 0))`); err != nil {
		t.Fatal(err)
	}
	ran := 0
	onceAcross(ctx, pool, "kiff.test-once", logger, func() { ran++ })
	if ran != 0 {
		t.Fatal("ran while another host held the lock")
	}
	if _, err := other.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended('kiff.test-once', 0))`); err != nil {
		t.Fatal(err)
	}
	other.Release()
	onceAcross(ctx, pool, "kiff.test-once", logger, func() { ran++ })
	onceAcross(ctx, pool, "kiff.test-once", logger, func() { ran++ })
	if ran != 2 {
		t.Fatalf("ran %d times with the lock free, want 2 (released after each run)", ran)
	}
}
