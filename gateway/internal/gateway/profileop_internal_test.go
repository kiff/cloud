package gateway

import "testing"

// A profile operation's marker reaches its verify read, so the read-back
// can find the record this call made; an operation without a marker reads
// its path as written.
func TestProfileOpCarriesTheMarkerToTheVerifyRead(t *testing.T) {
	profile := []byte(`{"test_proven":true,"operations":[
	 {"tool":"create_refund","effect":"moves_money",
	  "verify_read":{"tool":"list_refunds","args":{"payment_intent":"$args.payment_intent"},"amount_path":"data[0].amount"},
	  "marker":{"field":"metadata.kiff_op"}},
	 {"tool":"post_message","effect":"sends",
	  "verify_read":{"tool":"history","done_when":{"path":"messages[0].ts","equals":"$result.ts"}}}]}`)
	f, err := profileOp(profile, "create_refund")
	if v := f.Verify; err != nil || f.Probation || !f.Money || v == nil || v.Marker != "metadata.kiff_op" || v.AmountPath != "data[0].amount" {
		t.Fatalf("%+v %v", f, err)
	}
	if f, err := profileOp(profile, "post_message"); err != nil || f.Money || f.Verify == nil || f.Verify.Marker != "" {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestMarkedInReadsTheElementWithTheMarker(t *testing.T) {
	doc := map[string]any{"data": []any{
		map[string]any{"amount": 2000, "metadata": map[string]any{}},
		map[string]any{"amount": 500, "metadata": map[string]any{"kiff_op": "tc-1"}},
	}}
	for _, tc := range []struct {
		path, op   string
		val        any
		ok, listed bool
	}{
		{"data[0].amount", "tc-1", 500, true, true},
		{"data[0].amount", "tc-2", nil, false, true},
		{"data[0].missing", "tc-1", nil, false, true},
		{"amount", "tc-1", nil, false, false},
		{"other[0].amount", "tc-1", nil, false, false},
		{"data[0][1]", "tc-1", nil, false, false},
	} {
		val, ok, listed := markedIn([]any{doc}, tc.path, "metadata.kiff_op", tc.op)
		if ok != tc.ok || listed != tc.listed || (tc.ok && val != tc.val) {
			t.Errorf("%s %s: %v %v %v", tc.path, tc.op, val, ok, listed)
		}
	}
}

// Only a money operation no permit binds asks its owner on every call: a
// relay or verify tool's permit binds the call's arguments at the tool.
func TestOnlyAnUnboundMoneyToolAsksItsOwner(t *testing.T) {
	for _, tc := range []struct {
		mode            string
		money, readOnly bool
		asks            bool
	}{
		{ModeSealed, true, false, true},
		{"", true, false, true},
		{ModeRelay, true, false, false},
		{ModeVerify, true, false, false},
		{ModeSealed, false, false, false},
		{ModeSealed, true, true, false},
	} {
		tool := Tool{Mode: tc.mode, Money: tc.money, ReadOnly: tc.readOnly}
		p, err := tool.params(map[string]any{})
		if _, asks := p[OwnerAskParam]; err != nil || asks != tc.asks {
			t.Errorf("%+v: params %v, %v", tc, p, err)
		}
	}
}
