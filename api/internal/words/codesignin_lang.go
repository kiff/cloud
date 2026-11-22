package words

import (
	"strconv"
	"strings"
)

// The code sign-in emails speak the eight languages of Words and the more
// languages of CodeSignInEmailMore. A Lang the emails carry may therefore be
// one of the more ("ar"); Of is English for it, and CodeSignInEmailFor is the
// emails' own lines in it.

// CodeSignInEmailFor is the code emails' lines in lang: the eight's, the
// more languages', or English for one neither has.
func CodeSignInEmailFor(lang Lang) CodeSignInEmail {
	if _, ok := catalogs[lang]; ok {
		return Of(lang).CodeSignInEmail
	}
	if e, ok := CodeSignInEmailMore[string(lang)]; ok {
		return e
	}
	return Of(EN).CodeSignInEmail
}

// CodeLang is a language tag ("ar", "pt-PT", "ar_EG") as the Lang the code
// emails speak: the eight, or one of the more, else English.
func CodeLang(tag string) Lang {
	primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
	primary, _, _ = strings.Cut(primary, "_")
	if l, ok := known(primary); ok {
		return l
	}
	if _, ok := CodeSignInEmailMore[primary]; ok {
		return Lang(primary)
	}
	return EN
}

// CodeLangFromHeader is the code emails' language for an Accept-Language
// header: the tag with the highest q the emails speak, the earlier on a tie,
// English when none is. FromHeader's pick, with the more languages.
func CodeLangFromHeader(header string) Lang {
	best, bestQ := EN, 0.0
	for i, part := range strings.Split(header, ",") {
		if i >= 20 {
			break
		}
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f > 1 {
				continue
			}
			q = f
		}
		if q <= bestQ {
			continue
		}
		if l := CodeLang(tag); l != EN || strings.HasPrefix(strings.ToLower(strings.TrimSpace(tag)), "en") {
			best, bestQ = l, q
		}
	}
	return best
}
