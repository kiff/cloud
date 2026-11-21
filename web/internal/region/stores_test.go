package region

import "testing"

// The web's copy of the store lists is held to the same rule as the API's:
// every country a store offers tafs in is on the pinned list, once.
func TestEveryStoreCountryIsSupported(t *testing.T) {
	for store, codes := range StoreCountries {
		seen := map[string]bool{}
		for _, c := range codes {
			if seen[c] {
				t.Errorf("%s lists %s twice", store, c)
			}
			seen[c] = true
			if !IsSupported(c) {
				t.Errorf("%s offers tafs in %s, which isn't on the list effective %s (%s)", store, c, ListEffective, ListSource)
			}
		}
	}
}
