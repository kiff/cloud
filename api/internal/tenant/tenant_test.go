package tenant_test

import (
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/tenant"
)

func TestNewIDIsLowerULID(t *testing.T) {
	t.Parallel()

	id := tenant.NewID()
	if len(id) != 26 {
		t.Fatalf("ulid length: got %d, want 26", len(id))
	}
	if id != strings.ToLower(id) {
		t.Fatalf("ulid is not lowercased: %q", id)
	}
}

func TestSchemaName(t *testing.T) {
	t.Parallel()

	id := tenant.NewID()
	want := "kiff_t_" + id
	got := tenant.SchemaNameFor(id)
	if got != want {
		t.Fatalf("SchemaNameFor: got %q, want %q", got, want)
	}
}

func TestTenantValidate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		t    tenant.Tenant
		ok   bool
	}{
		{"empty id", tenant.Tenant{Slug: "acme"}, false},
		{"non-ulid id", tenant.Tenant{ID: "not-a-ulid", Slug: "acme"}, false},
		{"empty slug", tenant.Tenant{ID: tenant.NewID()}, false},
		{"bad slug uppercase", tenant.Tenant{ID: tenant.NewID(), Slug: "Acme"}, false},
		{"bad slug starts with hyphen", tenant.Tenant{ID: tenant.NewID(), Slug: "-acme"}, false},
		{"valid", tenant.Tenant{ID: tenant.NewID(), Slug: "acme-corp", DisplayName: "Acme Corp"}, true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := c.t.Validate()
			if c.ok && err != nil {
				t.Fatalf("expected ok, got %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}
