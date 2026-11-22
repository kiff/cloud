package words

import (
	"reflect"
	"strings"
	"testing"
)

// Every language in CodeSignInEmailMore has every line of the three emails,
// and none is left as English's own.
func TestCodeSignInEmailMoreHasEveryLineLikeEnglish(t *testing.T) {
	en := reflect.ValueOf(Of(EN).CodeSignInEmail)
	for lang, c := range CodeSignInEmailMore {
		v := reflect.ValueOf(c)
		for i := 0; i < v.NumField(); i++ {
			name := v.Type().Field(i).Name
			got, want := v.Field(i).String(), en.Field(i).String()
			if strings.TrimSpace(got) == "" {
				t.Errorf("%s %s: empty", lang, name)
			}
			if got == want && name != "CodeAskedBrowser" {
				t.Errorf("%s %s: left in English", lang, name)
			}
		}
	}
	if len(CodeSignInEmailMore) != 37 {
		t.Errorf("%d languages", len(CodeSignInEmailMore))
	}
}
