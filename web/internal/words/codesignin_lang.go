package words

import (
	"strconv"
	"strings"
)

// CodeSignInTag is a language tag ("ar", "ar-EG", "pt_PT") as the language the
// code sign-in screens have words in: one of the eight and English, or one of
// the more languages; "" when it is none of them.
func CodeSignInTag(tag string) string {
	primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
	primary, _, _ = strings.Cut(primary, "_")
	if _, ok := codeSignIn[primary]; ok {
		return primary
	}
	if _, ok := codeSignInMore[primary]; ok {
		return primary
	}
	return ""
}

// CodeSignInFromHeader is the code sign-in screens' language for an
// Accept-Language header: the tag with the highest q they have words in, the
// earlier on a tie, English when none; q=0 is refused.
func CodeSignInFromHeader(header string) string {
	best, bestQ := "en", 0.0
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
		if l := CodeSignInTag(tag); l != "" && q > bestQ {
			best, bestQ = l, q
		}
	}
	return best
}
