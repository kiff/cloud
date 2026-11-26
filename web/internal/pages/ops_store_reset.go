package pages

// orNone is s, or "none" when it's empty, for the store reset's read-back.
func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// heldOrNot says whether a store subscription still bills the account.
func heldOrNot(held bool) string {
	if held {
		return "still held"
	}
	return "cleared"
}

// setAside says the old subscription was set aside, so the store's later
// word on it is ignored.
func setAside(reset bool) string {
	if reset {
		return "; the old one is set aside, and the store's later notifications about it are ignored"
	}
	return ""
}

// trialWords is the read-back's trial: as a new sign-up's, running or ended.
func trialWords(trial string) string {
	switch trial {
	case "not_started":
		return "not started, as for a new sign-up"
	case "":
		return "unknown"
	}
	return trial
}

// allowanceFrom is when Coach's no-plan allowance counts from.
func allowanceFrom(at string) string {
	if at == "" {
		return "every run since the account began"
	}
	return "runs since " + at
}
