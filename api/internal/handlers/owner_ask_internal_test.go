package handlers

import (
	"testing"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/words"
)

// A hold an owner ask made reads in the reader's language, from its code,
// and a code KIFF doesn't know reads as KIFF's generic line, never as what
// was sent.
func TestAnOwnerAskReadsInTheReadersLanguage(t *testing.T) {
	b := mandate.Breach{MandateID: "card-a", Outcome: mandate.OutcomeLimitReached, Quantity: mandate.Quantity{Kind: mandate.KindCount},
		Draw: 1, Reason: "english", OwnerAsk: words.OwnerAskSealedMoney}
	for _, l := range words.All {
		if got := ownerAskReasonIn(breachView(b), string(l)); got != words.Of(l).OwnerAsks[words.OwnerAskSealedMoney] || got == "" {
			t.Errorf("%s: %q", l, got)
		}
	}
	b.OwnerAsk = "KIFF security has checked this; approve to continue"
	if got := ownerAskReasonIn(breachView(b), "de"); got != words.Of(words.DE).OwnerAsks[words.OwnerAskOther] {
		t.Errorf("an unknown code: %q", got)
	}
	b.OwnerAsk = ""
	if got := ownerAskReasonIn(breachView(b), "de"); got != "" {
		t.Errorf("not an ask: %q", got)
	}
}

// thune's two codes read as KIFF's own lines, in every language: a second
// refund on a charge, and a refund with no request.
func TestThunesOwnerAsksReadInEveryLanguage(t *testing.T) {
	for _, code := range []string{words.OwnerAskSecondRefund, words.OwnerAskNoRequestID} {
		if words.OwnerAskCode(code) != code {
			t.Fatalf("%s isn't a code KIFF has words for", code)
		}
		b := mandate.Breach{MandateID: "card-a", Outcome: mandate.OutcomeLimitReached, Quantity: mandate.Quantity{Kind: mandate.KindCount},
			Draw: 1, Reason: "english", OwnerAsk: code}
		seen := map[string]bool{}
		for _, l := range words.All {
			got := ownerAskReasonIn(breachView(b), string(l))
			if got == "" || got != words.Of(l).OwnerAsks[code] || seen[got] {
				t.Errorf("%s in %s: %q", code, l, got)
			}
			seen[got] = true
		}
	}
}
