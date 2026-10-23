package domain

import (
	"context"
	"testing"
)

func TestSlugify(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "refunds", "refunds"},
		{"spaces", "Refund Flow", "refund-flow"},
		{"underscores", "Vendor_Payments", "vendor-payments"},
		{"mixed case", "PayoutsV2", "payoutsv2"},
		{"slash", "ops/access", "ops-access"},
		{"dotted", "vendor.payments", "vendor-payments"},
		{"collapse runs", "a   b__c", "a-b-c"},
		{"trim separators", "  --refunds--  ", "refunds"},
		{"strip punctuation", "Refunds! (prod)", "refunds-prod"},
		{"digits", "tier1 billing", "tier1-billing"},
		{"empty", "", ""},
		{"only separators", "  __  ", ""},
		{"non-ascii dropped", "réfund", "rfund"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Slugify(tc.in); got != tc.want {
				t.Errorf("Slugify(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDomainNameFromYAML(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"reads domain", "domain: refunds\nentity: Order\n", "refunds"},
		{"trims whitespace", "domain:   Refund Flow  \n", "Refund Flow"},
		{"missing field", "entity: Order\n", ""},
		{"blank field", "domain: \"\"\n", ""},
		{"malformed yaml", "domain: [unterminated", ""},
		{"empty input", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DomainNameFromYAML([]byte(tc.yaml)); got != tc.want {
				t.Errorf("DomainNameFromYAML(%q) = %q, want %q", tc.yaml, got, tc.want)
			}
		})
	}
}

func TestSlugFromYAML(t *testing.T) {
	t.Parallel()
	if got := SlugFromYAML([]byte("domain: Refund Flow\n")); got != "refund-flow" {
		t.Errorf("SlugFromYAML = %q, want %q", got, "refund-flow")
	}
	if got := SlugFromYAML([]byte("entity: Order\n")); got != "" {
		t.Errorf("SlugFromYAML with no domain = %q, want empty", got)
	}
}

// TestMemorySourceLoadDerivesIdentity confirms Load populates the
// DomainID/Slug identity from the stored YAML (the same behavior
// PostgresSource.Load provides), so every layer reading a
// TenantDomain gets a domain identity without a schema change.
func TestMemorySourceLoadDerivesIdentity(t *testing.T) {
	t.Parallel()
	src := NewMemorySource()
	ctx := context.Background()
	if err := src.Save(ctx, "ten-1", []byte("domain: Refund Flow\nentity: Order\n")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	td, err := src.Load(ctx, "ten-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if td.Slug != "refund-flow" {
		t.Errorf("Slug = %q, want %q", td.Slug, "refund-flow")
	}
	if td.DomainID != "refund-flow" {
		t.Errorf("DomainID = %q, want %q (== slug at migration)", td.DomainID, "refund-flow")
	}
	if td.Version != 1 {
		t.Errorf("Version = %d, want 1", td.Version)
	}
}

// TestMemorySourceLoadIdentityDefaultWhenUnparseable confirms a
// nameless / unreadable domain falls back to DefaultDomainID rather
// than an empty key — the composite storage key must never be empty.
func TestMemorySourceLoadIdentityDefaultWhenUnparseable(t *testing.T) {
	t.Parallel()
	src := NewMemorySource()
	ctx := context.Background()
	if err := src.Save(ctx, "ten-2", []byte("entity: Order\n")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	td, err := src.Load(ctx, "ten-2")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if td.DomainID != DefaultDomainID || td.Slug != DefaultDomainID {
		t.Errorf("expected %q identity, got DomainID=%q Slug=%q", DefaultDomainID, td.DomainID, td.Slug)
	}
}

// TestMemorySourceDomainIDImmutableSlugFollows confirms the
// immutable-id / mutable-slug contract: re-saving with a renamed
// domain keeps the original domain_id but moves the slug.
func TestMemorySourceDomainIDImmutableSlugFollows(t *testing.T) {
	t.Parallel()
	src := NewMemorySource()
	ctx := context.Background()
	if err := src.Save(ctx, "ten-3", []byte("domain: Refunds\n")); err != nil {
		t.Fatalf("Save 1: %v", err)
	}
	if err := src.Save(ctx, "ten-3", []byte("domain: Refunds V2\n")); err != nil {
		t.Fatalf("Save 2: %v", err)
	}
	td, err := src.Load(ctx, "ten-3")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if td.DomainID != "refunds" {
		t.Errorf("DomainID = %q, want %q (immutable across rename)", td.DomainID, "refunds")
	}
	if td.Slug != "refunds-v2" {
		t.Errorf("Slug = %q, want %q (follows the rename)", td.Slug, "refunds-v2")
	}
	if td.Version != 2 {
		t.Errorf("Version = %d, want 2", td.Version)
	}

	bySlug, err := src.LoadByDomain(ctx, "ten-3", "refunds-v2")
	if err != nil {
		t.Fatalf("LoadByDomain by renamed slug: %v", err)
	}
	if bySlug.DomainID != "refunds" || bySlug.Slug != "refunds-v2" {
		t.Errorf("renamed slug resolved %+v, want immutable id refunds and slug refunds-v2", bySlug)
	}
}
