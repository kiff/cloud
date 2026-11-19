package handlers

import "testing"

func TestApproverAmountConversion(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		scale int
		want  int64
		ok    bool
	}{{"100.50", 2, 10050, true}, {"100", 0, 100, true}, {"-1", 2, 0, false}, {"1.001", 2, 0, false}, {"9223372036854775808", 0, 0, false}, {"NaN", 0, 0, false}} {
		n, err := parseApproverAmount(tc.raw, tc.scale)
		if (err == nil) != tc.ok || (tc.ok && n != tc.want) {
			t.Errorf("%s scale %d = %d,%v", tc.raw, tc.scale, n, err)
		}
	}
}
