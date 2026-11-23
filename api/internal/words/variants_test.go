package words

import (
	"strings"
	"testing"
)

// pt-BR is its own language for the emails, drawn over pt: Brazil's tag reads Brazil's lines, Portugal's and the bare
// tag read pt's, and a line pt-BR doesn't write reads as pt's, never as English.
func TestABrazilianReaderGetsBrazilsLinesOverPortugals(t *testing.T) {
	for tag, want := range map[string]Lang{"pt-BR": "pt-BR", "pt_br": "pt-BR", "PT-br": "pt-BR", "pt": PT, "pt-PT": PT, "pt-AO": PT} {
		if got := CodeLang(tag); got != want {
			t.Errorf("CodeLang(%q) = %q, want %q", tag, got, want)
		}
	}
	if got := CodeLangFromHeader("pt-BR, pt;q=0.9, en;q=0.8"); got != "pt-BR" {
		t.Errorf("a Brazilian phone's header reads %q", got)
	}
	br, pt := Of("pt-BR"), Of(PT)
	if br == pt || br.NewDeviceSubject == pt.NewDeviceSubject || !strings.Contains(br.NewDevicePassword, "senha") {
		t.Errorf("pt-BR's own lines: %q, %q", br.NewDeviceSubject, br.NewDevicePassword)
	}
	// A line pt-BR doesn't write is pt's: the months, the decimal comma, and every line beyond the emails.
	if br.Months != pt.Months || br.DecimalComma != pt.DecimalComma || br.NewDeviceWhen != pt.NewDeviceWhen {
		t.Errorf("pt-BR lost a line of pt's")
	}
	if en := Of(EN); br.NewDeviceWhen == en.NewDeviceWhen {
		t.Errorf("a line pt-BR doesn't write read as English")
	}
	// Every email reads pt-BR, and what isn't in the table reads pt's, never English.
	for name, got := range map[string]Lang{"EmailLangFor": EmailLangFor("pt-BR"), "NoticeLangFor": NoticeLangFor("pt-BR"),
		"CardLangFor": CardLangFor("pt-BR"), "AccountEmailLang": AccountEmailLang("pt-BR")} {
		if got != "pt-BR" {
			t.Errorf("%s(pt-BR) = %q", name, got)
		}
	}
	if TextLang("pt-BR") != PT {
		t.Errorf("TextLang(pt-BR) = %q, want pt", TextLang("pt-BR"))
	}
	if f, ok := EmailFooterFor("pt-BR"); !ok || !strings.Contains(f.NeverAsk, "senha") {
		t.Errorf("pt-BR's footer: %+v", f)
	}
	if e := CodeSignInEmailFor("pt-BR"); !strings.Contains(e.CodeSubject, "entrar") {
		t.Errorf("pt-BR's code email: %q", e.CodeSubject)
	}
	if got := ReceiptStepsFor("pt-BR, pt;q=0.9").ReceiptStepLines["handoff.handed"]; got != ReceiptStepsMore["pt-BR"].ReceiptStepLines["handoff.handed"] {
		t.Errorf("pt-BR's receipt step: %q", got)
	}
	// Brazil counts 0 as singular, and writes its amounts R$ 1.234,56.
	if got := br.CallCount(1); got != "1 chamada" {
		t.Errorf("CallCount(1) = %q", got)
	}
	if br.Money.Thousands != "." || br.Money.CurrencyAfter {
		t.Errorf("pt-BR's money: %+v", br.Money)
	}
}

// A variant's lines are only names the emails read, with the parts English has: never a stray key or a lost {n}.
func TestAVariantsLinesAreEmailLinesWithTheirParts(t *testing.T) {
	english := EmailLinesEnglish()
	for tag, lines := range EmailVariants {
		if _, ok := variantBase[Lang(tag)]; !ok {
			t.Errorf("%s has variant lines but isn't a variant", tag)
		}
		for k, v := range lines {
			en, ok := english[k]
			if !ok {
				t.Errorf("%s: %s isn't a line the emails read", tag, k)
				continue
			}
			if _, plural := pluralLines[k]; plural {
				continue
			}
			if strings.Join(placeholders(v), " ") != strings.Join(placeholders(en), " ") {
				t.Errorf("%s %s: parts %v, English has %v", tag, k, placeholders(v), placeholders(en))
			}
			if p := lineValueProblem(k, v); p != "" {
				t.Errorf("%s %s: %s", tag, k, p)
			}
		}
	}
}
