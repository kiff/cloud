package handlers

import "testing"

// planqueLines reads planque's own bytes: the vector is what kiff/planque's
// device.MessageWindow produces for an approve inside a window (and Message
// without one), so KIFF's parse and planque's layout can't drift apart
// unseen, the seam that refused every v2 approval (kiff/tafs#494).
func TestPlanqueLinesReadsPlanquesOwnLayout(t *testing.T) {
	const v2 = "planque.request.v2\nhttps://planque.kiff.dev\nt1\nPOST\n/v1/accounts/t1/grants/grt_1/approve\n778a7018a6902581dad36f0b176522db941cddb2f3b1e9a404a14a42bf414cdf\n2026-11-05T13:00:00Z\nwin_0123456789abcdef"
	lines, window, ok := planqueLines(v2)
	if !ok || window != "win_0123456789abcdef" || len(lines) != 7 || lines[0] != "planque.request.v2" ||
		lines[2] != "t1" || lines[4] != "/v1/accounts/t1/grants/grt_1/approve" || lines[6] != "2026-11-05T13:00:00Z" {
		t.Fatalf("v2: %q %q %v", lines, window, ok)
	}
	const v1 = "planque.request.v1\nhttps://planque.kiff.dev\nt1\nPOST\n/v1/accounts/t1/grants/grt_1/approve\n778a7018a6902581dad36f0b176522db941cddb2f3b1e9a404a14a42bf414cdf\n2026-11-05T13:00:00Z"
	if lines, window, ok := planqueLines(v1); !ok || window != "" || len(lines) != 7 {
		t.Fatalf("v1: %q %q %v", lines, window, ok)
	}
	for _, bad := range []string{v1 + "\nwin_x", v2 + "\nextra", "planque.request.v2\na\nb\nc\nd\ne\nf\n", "kiff.request.v2\na\nb\nc\nd\ne\nf\ng"} {
		if _, _, ok := planqueLines(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}
