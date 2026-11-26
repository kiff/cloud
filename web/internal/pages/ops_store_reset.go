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
