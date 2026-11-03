package mandate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/mandate"
)

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"":                      "",
		"  Refunds on Stripe  ": "Refunds on Stripe",
		strings.Repeat("é", 60): strings.Repeat("é", 60),
	} {
		got, err := mandate.CleanTitle(in)
		if err != nil || got != want {
			t.Errorf("CleanTitle(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{strings.Repeat("é", 61), "two\nlines", "tab\there", "\x00"} {
		if _, err := mandate.CleanTitle(in); !errors.Is(err, mandate.ErrInvalidMandate) {
			t.Errorf("CleanTitle(%q) err = %v, want ErrInvalidMandate", in, err)
		}
	}
}

// The title round-trips through Postgres, survives a reissue that names
// none, is replaced by one that does, and changes without a revision.
// Only SQL proves the upsert keeps it.
func TestPostgres_CardTitle(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "pg-title")
	s := mandate.NewPostgresStore(pool)
	ctx := context.Background()

	m := pgMandate("pg-title", "mdt-title")
	m.Title = "Refunds on Stripe"
	if err := s.Issue(ctx, m, "tester"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	title := func() string {
		t.Helper()
		ms, err := s.ForSubject(ctx, "pg-title", "agent://a")
		if err != nil || len(ms) != 1 {
			t.Fatalf("ForSubject: %v, %d", err, len(ms))
		}
		return ms[0].Title
	}
	if got := title(); got != "Refunds on Stripe" {
		t.Fatalf("issued title = %q", got)
	}

	m.Title = ""
	if err := s.Issue(ctx, m, "tester"); err != nil {
		t.Fatalf("reissue: %v", err)
	}
	if got := title(); got != "Refunds on Stripe" {
		t.Errorf("a reissue without a title changed it to %q", got)
	}
	m.Title = "Read Stripe"
	if err := s.Issue(ctx, m, "tester"); err != nil {
		t.Fatalf("reissue: %v", err)
	}
	if got := title(); got != "Read Stripe" {
		t.Errorf("a reissue's title = %q, want Read Stripe", got)
	}

	before, err := s.Revisions(ctx, "pg-title", "mdt-title")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTitle(ctx, "pg-title", "mdt-title", "Stripe"); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}
	if got := title(); got != "Stripe" {
		t.Errorf("set title = %q", got)
	}
	if err := s.SetTitle(ctx, "pg-title", "mdt-title", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := title(); got != "" {
		t.Errorf("cleared title = %q", got)
	}
	after, err := s.Revisions(ctx, "pg-title", "mdt-title")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("renaming wrote revisions: %d → %d", len(before), len(after))
	}
	if err := s.SetTitle(ctx, "pg-title", "nope", "x"); !errors.Is(err, mandate.ErrNotFound) {
		t.Errorf("unknown Card: err = %v, want ErrNotFound", err)
	}
}
