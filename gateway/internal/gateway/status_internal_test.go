package gateway

import "testing"

// A match through a list looks at every element for this call's
// operation id, never only the first, and never by any other field.
func TestMarkedRecordFindsOnlyThisCallsOperationID(t *testing.T) {
	list := []any{map[string]any{"data": []any{
		map[string]any{"id": "re_new", "amount": 500, "metadata": map[string]any{"kiff_op": "other"}},
		map[string]any{"id": "re_mine", "amount": 500, "metadata": map[string]any{"kiff_op": "k1"}},
	}}}
	rec, ok := markedRecord(list, "data[0].metadata.kiff_op", "k1")
	if !ok || rec.(map[string]any)["id"] != "re_mine" {
		t.Fatalf("got %v %v", rec, ok)
	}
	if _, ok := markedRecord(list, "data[0].metadata.kiff_op", "k2"); ok {
		t.Fatal("found a record without this call's operation id")
	}
	single := []any{map[string]any{"id": "inv_1", "reference": "k1"}}
	if rec, ok := markedRecord(single, "reference", "k1"); !ok || rec.(map[string]any)["id"] != "inv_1" {
		t.Fatalf("single record: %v %v", rec, ok)
	}
	if _, ok := markedRecord(single, "reference", "k9"); ok {
		t.Fatal("single record with another id")
	}
	blank := []any{map[string]any{"data": []any{map[string]any{"id": "re_blank", "metadata": map[string]any{"kiff_op": ""}}}}}
	if _, ok := markedRecord(blank, "data[0].metadata.kiff_op", ""); ok {
		t.Fatal("an empty operation id matched an empty marker")
	}
	if _, ok := markedRecord(list, "data[0]", "k1"); ok {
		t.Fatal("a list path names a field of its elements")
	}
}
