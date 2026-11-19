package usecases

import "testing"

// Six markets read as general-purpose, which is the answer a reader has
// to do the work to apply. Two read as a choice someone made. The
// discipline only holds if adding a third is a deliberate act, so the
// count is pinned: a market comes back when a customer in it has paid.
func TestTheListStaysTwo(t *testing.T) {
	got := All()
	if len(got) != 2 {
		names := make([]string, 0, len(got))
		for _, uc := range got {
			names = append(names, uc.Title)
		}
		t.Fatalf("use cases = %v; the homepage carries two, and a third needs a paying customer in it first", names)
	}
	want := []string{"Customer operations", "Cloud & infrastructure"}
	for i, uc := range got {
		if uc.Title != want[i] {
			t.Errorf("use case %d = %q, want %q", i, uc.Title, want[i])
		}
		if len(uc.Actions) == 0 {
			t.Errorf("%q has no actions; the chips are what make it concrete", uc.Title)
		}
	}
}
