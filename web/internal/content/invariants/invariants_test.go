package invariants

import "testing"

func TestArtifactParsesAndIsNotEmpty(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatalf("parse embedded invariants.json: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no invariants embedded — the page would render an empty promise")
	}
	if Count() != len(all) {
		t.Errorf("Count() = %d, len(All()) = %d", Count(), len(all))
	}
	if Source() == "" {
		t.Error("Source() is empty — the page cannot state the scope of the claim")
	}
	for _, inv := range all {
		if inv.Test == "" || inv.File == "" || inv.Line == 0 || inv.Statement == "" {
			t.Errorf("incomplete invariant: %+v", inv)
		}
	}
}

// Every invariant must land in a rendered group. A category typo would
// otherwise drop a published guarantee off the page silently, which is
// the failure mode this whole artifact exists to prevent.
func TestGroupingLosesNothing(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	groups, err := Grouped()
	if err != nil {
		t.Fatalf("Grouped: %v", err)
	}
	n := 0
	for _, g := range groups {
		if g.Label() == "" {
			t.Errorf("group %q has no label", g.Category)
		}
		n += len(g.Invariants)
	}
	if n != len(all) {
		t.Errorf("grouping dropped invariants: %d grouped, %d total", n, len(all))
	}
}
