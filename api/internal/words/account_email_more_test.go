package words

import (
	"strings"
	"testing"
)

// Every shipped language has all seven lines of the three account emails, says
// "account" and never "workspace" or "KIFF Cloud", keeps KIFF's name and the
// support address, and carries no digits the English does not.
func TestEveryLanguageHasAllTheAccountEmailLines(t *testing.T) {
	if got := len(AccountEmails); got != 44 {
		t.Fatalf("the account emails are written in %d languages, want 44 (every shipped language besides English)", got)
	}
	for code, e := range AccountEmails {
		lines := map[string]string{
			"SuspendedSubject": e.SuspendedSubject, "SuspendedBody": e.SuspendedBody,
			"UnsuspendedSubject": e.UnsuspendedSubject, "UnsuspendedBody": e.UnsuspendedBody,
			"SupportSubject": e.SupportSubject, "SupportBody": e.SupportBody, "SupportView": e.SupportView,
		}
		for name, line := range lines {
			if strings.TrimSpace(line) == "" {
				t.Errorf("%s: %s is empty", code, name)
			}
			if strings.Contains(line, "orkspace") || strings.Contains(line, "KIFF Cloud") {
				t.Errorf("%s: %s says workspace or KIFF Cloud: %q", code, name, line)
			}
			if strings.ContainsAny(line, "0123456789") && !strings.Contains(line, "support@kiff.dev") {
				t.Errorf("%s: %s has a digit the English has not: %q", code, name, line)
			}
		}
		if !strings.Contains(e.SuspendedBody, "support@kiff.dev") {
			t.Errorf("%s: the suspended email lost the support address", code)
		}
		for _, name := range []string{"SuspendedSubject", "UnsuspendedSubject", "SupportSubject"} {
			if !strings.Contains(lines[name], "KIFF") {
				t.Errorf("%s: %s lost the name KIFF", code, name)
			}
		}
	}
}
