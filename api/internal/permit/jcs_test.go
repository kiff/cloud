package permit

import (
	"encoding/json"
	"os"
	"testing"
)

// The vectors are shared with the Python verifier in kiff/kiff-guard: both
// sides must produce the same bytes, or every permit is refused.
func TestCanonicalizeVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/jcs_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct{ Name, Input, Canonical string }
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		got, err := Canonicalize(json.RawMessage(v.Input))
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		if string(got) != v.Canonical {
			t.Errorf("%s:\n got %s\nwant %s", v.Name, got, v.Canonical)
		}
	}
}

func TestArgsSHA256IgnoresOrderAndWhitespace(t *testing.T) {
	a, err := ArgsSHA256(json.RawMessage(`{"amount_eur": 80, "order_number": "1042"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ArgsSHA256(json.RawMessage(`{"order_number":"1042","amount_eur":80.0}`))
	if a != b {
		t.Fatalf("same arguments hashed differently: %s %s", a, b)
	}
	c, _ := ArgsSHA256(json.RawMessage(`{"order_number":"1043","amount_eur":80}`))
	if a == c {
		t.Fatal("different arguments hashed the same")
	}
}

func TestCanonicalizeRefusesTrailingData(t *testing.T) {
	if _, err := Canonicalize(json.RawMessage(`{} {}`)); err == nil {
		t.Fatal("two values accepted")
	}
}

// Integers beyond 2^53 - 1 are refused: as doubles, 2^53 and 2^53 + 1 would
// hash the same while the tool received either (review on kiff-guard#66).
func TestCanonicalizeRefusesUnsafeIntegers(t *testing.T) {
	for _, in := range []string{`{"id":9007199254740992}`, `{"id":9007199254740993}`, `{"a":[{"id":-9007199254740993}]}`} {
		if _, err := Canonicalize(json.RawMessage(in)); err == nil {
			t.Errorf("accepted %s", in)
		}
	}
	if _, err := Canonicalize(json.RawMessage(`{"id":9007199254740991,"f":1e300}`)); err != nil {
		t.Fatal(err)
	}
}
