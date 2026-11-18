package words

import "github.com/kiff/cloud/api/internal/mandate"

// WarningReason is a Card warning's reason in l, composed from its kind,
// its share of the total and its window. In English it is the reason KIFF
// stored, word for word. It lives here, not in mandate, so the exported
// core holds no catalogs.
func WarningReason(w mandate.CardWarning, l Lang) string {
	pct := -1.0
	if w.Ceiling > 0 {
		pct = 100 * float64(w.Used) / float64(w.Ceiling)
	}
	return Of(l).WarningReason(w.Kind, w.Ratio, pct, string(w.Window))
}
