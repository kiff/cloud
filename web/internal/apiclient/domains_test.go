package apiclient

import "testing"

// AsDomain projects the /v1/me/domains apex DomainDetail onto the
// legacy Domain shape the shared dashboard view renders, so the
// Playground can source from the apex without churning the shared
// view type. This guards the field mapping.
func TestDomainDetail_AsDomain(t *testing.T) {
	t.Parallel()
	d := &DomainDetail{
		Name:    "duplicate-payment",
		Entity:  "Invoice",
		Version: 3,
		Status:  "active",
		Actions: []DomainAction{{Name: "PAY_INVOICE", AllowedStates: []string{"PENDING"}}},
		Lifecycle: DomainLifecycle{
			States: []string{"PENDING", "PAID"},
			Events: []string{"pay"},
		},
	}
	got := d.AsDomain()
	if got == nil {
		t.Fatal("AsDomain returned nil")
	}
	if got.Domain != "duplicate-payment" {
		t.Errorf("Domain = %q, want duplicate-payment (apex Name maps to legacy Domain)", got.Domain)
	}
	if got.Entity != "Invoice" || got.Version != 3 {
		t.Errorf("entity/version = %q/%d", got.Entity, got.Version)
	}
	if len(got.Actions) != 1 || got.Actions[0].Name != "PAY_INVOICE" {
		t.Errorf("actions = %+v", got.Actions)
	}
	if len(got.States) != 2 || got.States[0] != "PENDING" {
		t.Errorf("states = %v (from lifecycle)", got.States)
	}
	if len(got.Events) != 1 || got.Events[0] != "pay" {
		t.Errorf("events = %v (from lifecycle)", got.Events)
	}
}

// A nil DomainDetail maps to nil, so the playground's "no domain yet"
// path (Domain stays nil) is preserved.
func TestDomainDetail_AsDomain_Nil(t *testing.T) {
	t.Parallel()
	var d *DomainDetail
	if d.AsDomain() != nil {
		t.Error("nil DomainDetail must map to nil Domain")
	}
}
