package region

import (
	"testing"

	"github.com/kiff/cloud/web/internal/words"
)

// The web's refused page (handlers.RegionRefused) says words.RegionIn's line;
// the API's JSON and this package's stand-in say Here. Both must read the
// same in every language.
func TestHereMatchesTheRefusedPage(t *testing.T) {
	for lang, line := range Here {
		if got := words.RegionIn(lang).Here; got != line {
			t.Errorf("%s: words.RegionIn says %q, region.Here says %q", lang, got, line)
		}
	}
}
