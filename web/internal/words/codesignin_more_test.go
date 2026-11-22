package words

import (
	"reflect"
	"strings"
	"testing"
)

// Every language in codeSignInMore has every line, takes the email (%s) where
// English does, and none is left as English's own.
func TestCodeSignInMoreHasEveryLineLikeEnglish(t *testing.T) {
	en := reflect.ValueOf(codeSignIn["en"])
	for lang, c := range codeSignInMore {
		v := reflect.ValueOf(c)
		for i := 0; i < v.NumField(); i++ {
			name := v.Type().Field(i).Name
			got, want := v.Field(i).String(), en.Field(i).String()
			if strings.TrimSpace(got) == "" {
				t.Errorf("%s %s: empty", lang, name)
			}
			if strings.Count(got, "%s") != strings.Count(want, "%s") {
				t.Errorf("%s %s: takes %d values, English %d", lang, name, strings.Count(got, "%s"), strings.Count(want, "%s"))
			}
			if got == want && name != "EmailLabel" && name != "CodeLabel" {
				t.Errorf("%s %s: left in English", lang, name)
			}
		}
	}
	if len(codeSignInMore) != 37 {
		t.Errorf("%d languages", len(codeSignInMore))
	}
}
