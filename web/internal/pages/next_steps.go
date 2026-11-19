package pages

import "strings"

// NextStepLabel is a Card's next step in words: return_first reads
// "Return first".
func NextStepLabel(step string) string {
	s := strings.TrimSpace(strings.ReplaceAll(step, "_", " "))
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
