package words

import (
	"strings"
	"testing"
)

// Each refusal names a place only as the key says: the stated country's by its
// name, where the person is never (an address's country is a guess).
func TestRegionNamesACountryOnlyForAStatedOne(t *testing.T) {
	for _, lang := range Languages {
		r := RegionIn(lang)
		if strings.Contains(r.Here, "{") {
			t.Errorf("%s: region.unsupported_here names a country: %q", lang, r.Here)
		}
		if strings.Count(r.CountryF, "{country}") != 1 {
			t.Errorf("%s: region.unsupported_country names {country} once: %q", lang, r.CountryF)
		}
		if got := r.Country("Ruritania"); !strings.Contains(got, "Ruritania") || strings.Contains(got, "{") {
			t.Errorf("%s: %q", lang, got)
		}
		// Both say why: Claude's availability, never a list of our own.
		for _, line := range []string{r.Here, r.CountryF} {
			if !strings.Contains(line, "Claude") || !strings.Contains(line, "Anthropic") {
				t.Errorf("%s: %q doesn't say it's where Anthropic's Claude is available", lang, line)
			}
		}
	}
	if RegionIn("xx") != RegionIn("en") {
		t.Error("a language without words isn't answered in English")
	}
}
