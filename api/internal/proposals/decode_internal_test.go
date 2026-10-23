package proposals

import "testing"

// kiff/tafs#167: a decision row reads what its call moved and was on from
// the record; a record from before has none.
func TestDecodeDecisionRowReadsTheCall(t *testing.T) {
	data := []byte(`{"proposal_id":"tc-1","action":"stripe_refund","outcome":"allowed",
		"amount":{"value":"150.00","unit":"EUR"},"on_arg":"charge","on_hash":"sha256:ab"}`)
	row, err := decodeDecisionRow(DecisionRow{}, "proposal_decision", nil, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if row.Amount == nil || *row.Amount != (CallAmount{Value: "150.00", Unit: "EUR"}) || row.OnArg != "charge" || row.OnHash != "sha256:ab" {
		t.Fatalf("row = %+v", row)
	}
	old, err := decodeDecisionRow(DecisionRow{}, "proposal_decision", nil, []byte(`{"proposal_id":"tc-0","outcome":"allowed"}`), nil)
	if err != nil || old.Amount != nil || old.OnArg != "" || old.OnHash != "" {
		t.Fatalf("an earlier record = %+v, %v", old, err)
	}
}
