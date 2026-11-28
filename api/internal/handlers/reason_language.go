package handlers

import (
	"net/http"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/words"
)

// A person's lines in their language (kiff/tafs#75, and the eight of
// kiff/tafs/docs/languages.md). The phone sends its app's languages in
// Accept-Language; KIFF writes the lines it can compose (a held call's
// reason, why a call wasn't sent, the approval window, "did this happen")
// in the best of the eight (internal/words), else English. Any reason KIFF
// can't compose stays as KIFF wrote it, in English. Receipts are never
// translated: their words are signed.

const (
	langEN = string(words.EN)
	langES = string(words.ES)
)

// replyLanguage is the language KIFF writes a person's lines in: the
// Accept-Language tag with the highest q whose primary language is one of
// the eight, the earlier one on a tie. English when none is.
func replyLanguage(r *http.Request) string {
	return string(words.FromHeader(r.Header.Get("Accept-Language")))
}

// wordsIn is lang's words.
func wordsIn(lang string) *words.Words { return words.Of(words.Lang(lang)) }

// resourceReasonIn is a resource breach's reason in lang, from the breach's
// resource and value as the view carries them; "" keeps KIFF's own, in
// English.
func resourceReasonIn(b map[string]any, lang string) string {
	if lang == langEN || b["outcome"] != string(mandate.OutcomeResourceOutside) {
		return ""
	}
	param, _ := b["resource"].(string)
	value, _ := b["value"].(string)
	if param == "" {
		return ""
	}
	if value == "" {
		return wordsIn(lang).ResourceMissing(param)
	}
	return wordsIn(lang).ResourceNotOnCard(param, value)
}

// ownerAskReasonIn is an owner ask's reason in lang, from its code as the
// view carries it (words.OwnerAsks); "" when the breach isn't one.
func ownerAskReasonIn(b map[string]any, lang string) string {
	code, _ := b["owner_ask"].(string)
	if code == "" {
		return ""
	}
	return wordsIn(lang).OwnerAsks[words.OwnerAskCode(code)]
}

// callReasons are the reason codes KIFF has words for, why a call wasn't
// sent in the Record (kiff/tafs#138): "mandate_" and a Card's outcome
// (mandate.Outcome*), or one of KIFF's own. Their words are in
// internal/words, by the same code. A code not here has no words: its line
// says only that it wasn't sent.
var callReasons = []string{
	"mandate_" + string(mandate.OutcomeOwnerRejected),
	"mandate_" + string(mandate.OutcomeOwnerExpired),
	"mandate_" + string(mandate.OutcomeWithdrawn),
	"mandate_" + string(mandate.OutcomePerActionExceeded),
	"mandate_" + string(mandate.OutcomeLimitReached),
	"mandate_" + string(mandate.OutcomeResourceOutside),
	"mandate_" + string(mandate.OutcomeUnreadableResource),
	"mandate_" + string(mandate.OutcomeNotCovered),
	"mandate_" + string(mandate.OutcomeCategoryClosed),
	"mandate_" + string(mandate.OutcomeExpired),
	reasonNoMandateIssued,
	reasonUnitMismatch,
	reasonUnitUnverified,
	reasonAmountUnverified,
	reasonHalted,
	reasonHaltUnavailable,
}

// callReasonIn is why a call wasn't sent, in lang: the first of its
// reasons KIFF has words for; "" when it was sent or none has.
func callReasonIn(outcome string, reasons []string, lang string) string {
	if outcome == "allowed" {
		return ""
	}
	w := wordsIn(lang)
	for _, r := range reasons {
		if line, ok := w.Reasons[r]; ok {
			return line
		}
	}
	return ""
}
