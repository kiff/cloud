package tenant_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/tenant"
)

func TestParseAccountName(t *testing.T) {
	for in, want := range map[string]string{
		"  Kiff Agents ":               "Kiff Agents",
		"Ünïcödé GmbH":                 "Ünïcödé GmbH",
		strings.Repeat("a", 80):        strings.Repeat("a", 80),
		"":                             "",
		"   ":                          "",
		"Tab\tin":                      "",
		"Line\nbreak":                  "",
		strings.Repeat("é", 81):        "",
		string([]byte{0xff, 'a', 'b'}): "",
	} {
		got, ok := tenant.ParseAccountName(in)
		if ok != (want != "") || got != want {
			t.Errorf("ParseAccountName(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

// An account made at a first sign-in is named by its sign-in id until an
// admin names it; NameFor says so, and which id it was.
func TestNameFor(t *testing.T) {
	store := tenant.NewMemoryStore()
	subject := "user_ana"
	made, err := tenant.EnsureForSubject(context.Background(), store, &fakeProvisioner{store: store}, tenant.NewMemorySubjectStore(), subject)
	if err != nil {
		t.Fatal(err)
	}
	if name, first := tenant.NameFor(made); name != "" || first != subject {
		t.Fatalf("a new account: NameFor = %q, %q; want no name and %q", name, first, subject)
	}

	if err := store.SetMetadata(context.Background(), made.ID, tenant.MetadataKeyAccountName, "Kiff Agents"); err != nil {
		t.Fatal(err)
	}
	named, _ := store.Get(context.Background(), made.ID)
	if name, first := tenant.NameFor(named); name != "Kiff Agents" || first != "" {
		t.Fatalf("named: NameFor = %q, %q; want Kiff Agents", name, first)
	}

	// An account an operator created with a name keeps it.
	ops := tenant.Tenant{ID: "t2", Slug: "acme", DisplayName: "Acme Ltd"}
	if name, first := tenant.NameFor(ops); name != "Acme Ltd" || first != "" {
		t.Fatalf("operator's: NameFor = %q, %q; want Acme Ltd", name, first)
	}
	// A display name that only looks like an id, on a slug not made from it,
	// is a name.
	odd := tenant.Tenant{ID: "t3", Slug: "user-0000000000", DisplayName: subject}
	if name, _ := tenant.NameFor(odd); name != subject {
		t.Fatalf("slug not from the name: NameFor = %q, want %q", name, subject)
	}
}
