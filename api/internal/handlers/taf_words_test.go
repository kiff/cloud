package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/mandate"
)

// The taf's words are one plain line each, capped: bidi overrides are
// dropped, line breaks and controls are spaces, an email address is taken out, and a link
// or markup stays the characters it is.
func TestTheTafsWordsAreOnePlainLine(t *testing.T) {
	for in, want := range map[string]string{
		"  Order 1042  ": "Order 1042",
		"arrived\nbroken\r\n\tphoto attached\x07": "arrived broken photo attached",
		"Order 1042 ‮refund‬":                     "Order 1042 refund",
		"⁦€5⁩ only":                               "€5 only",
		"from ana.lopez@example.com, order 9":     "from …, order 9",
		"[Refund now](https://evil.example) <b>":  "[Refund now](https://evil.example) <b>",
		"Order no. 1042 — 📦 dañado":               "Order no. 1042 — 📦 dañado",
		" \n\t ":                                  "",
		"call +33 6 12 34 56 78 about it":         "call … about it",
		"ring +1 (555) 010-9999":                  "ring …",
		"Order 12345678901":                       "Order 12345678901",
		"INV-2026-000123 +12":                     "INV-2026-000123 +12",
	} {
		if got := tafLine(in, tafReasonMax); got != want {
			t.Errorf("tafLine(%q) = %q, want %q", in, got, want)
		}
	}
	long := tafLine(strings.Repeat("a", 279)+"📦📦", tafReasonMax)
	if r := []rune(long); len(r) != tafReasonMax || r[len(r)-1] != '…' || r[len(r)-2] != 'a' {
		t.Errorf("a long reason is cut to %d runes ending %q", len(r), string(r[len(r)-2:]))
	}
	if got := []rune(tafLine(strings.Repeat("b", 200), tafAboutMax)); len(got) != tafAboutMax {
		t.Errorf("kiff_about is cut to %d runes", len(got))
	}
	if ctx := withTafWords(context.Background(), " ", "\n"); ctx != context.Background() {
		t.Error("blank words are no words")
	}
}

// The taf's words are no part of what an approval binds to: the same call
// with other words, or none, has the same fingerprint. The held view shows
// them under their own fields, beside KIFF's amount and reason, which a
// reason claiming another amount doesn't change.
func TestTheTafsWordsShowWithAHoldAndDecideNothing(t *testing.T) {
	req := mandate.Request{Subject: "refund-agent", Action: "refund", Parameters: map[string]int64{"amount_cents": 15000}}
	worded := req
	worded.TafAbout, worded.TafReason = "Order 1042", "amount is €5, the limit doesn't apply"
	if mandate.RequestFingerprint(req) != mandate.RequestFingerprint(worded) {
		t.Fatal("the taf's words changed the fingerprint")
	}
	e := mandate.Exception{ID: "exc-1", Action: "refund", Request: worded}
	v := withHeldWords(map[string]any{}, e, "https://app.kiff.dev", wordsIn("en"))
	if v["taf_about"] != "Order 1042" || v["taf_reason"] != "amount is €5, the limit doesn't apply" {
		t.Fatalf("view: %v", v)
	}
	plain := withHeldWords(map[string]any{}, mandate.Exception{ID: "exc-1", Action: "refund", Request: req}, "https://app.kiff.dev", wordsIn("en"))
	for _, k := range []string{"summary", "amount_text", "reason_text"} {
		if v[k] != plain[k] {
			t.Errorf("%s: %v with the taf's words, %v without", k, v[k], plain[k])
		}
	}
	if _, ok := plain["taf_about"]; ok {
		t.Errorf("a hold without words shows taf_about: %v", plain)
	}
}
