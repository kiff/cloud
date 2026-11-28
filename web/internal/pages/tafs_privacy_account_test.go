package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// What a connection's record keeps about its account matches planque: for a
// bank (Qonto), the organisation's name, its country and the end of its id,
// read at sign-in, and used to show the account, to finish a sign-in started
// again only with that same account, and to keep an invoice to the fields its
// country takes.
func TestTafsPrivacyNamesWhatABankConnectionKeeps(t *testing.T) {
	var b bytes.Buffer
	if err := TafsPrivacy().Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(b.String(), "&#39;", "'")
	for _, want := range []string{
		"for a bank, the organisation's name, its country and the last characters of its id",
		"read from the provider when you sign in",
		"so a sign-in started again finishes only with that same account",
		"so an invoice carries only the fields its country takes",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("/tafs/privacy lacks %q", want)
		}
	}
	if strings.Contains(s, "(its name or email), so the app") {
		t.Error("/tafs/privacy still has the account sentence without what a bank connection keeps")
	}
}
