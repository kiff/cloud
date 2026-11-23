package words

// AccountEmail is the lines of the three emails about an account that an
// operator acts on: it was suspended, it was reactivated, and a support
// ticket has an update. They say "account", never "workspace" or "KIFF
// Cloud": that is the word the product uses.
//
// English is the source. AccountEmails holds the other languages, keyed by the
// primary language tag, as the translations write them; a language with no entry
// reads English, whatever the person's language is. A test fails on an entry
// that has an empty line.
type AccountEmail struct {
	SuspendedSubject   string
	SuspendedBody      string
	UnsuspendedSubject string
	UnsuspendedBody    string
	SupportSubject     string
	SupportBody        string
	SupportView        string
}

// accountEmailEN is the source of the three emails' lines.
var accountEmailEN = AccountEmail{
	SuspendedSubject: "Your KIFF account has been temporarily suspended",
	SuspendedBody: "Your KIFF account has been temporarily suspended. " +
		"This can happen when KIFF detects activity that needs review. " +
		"If you believe this is unexpected, reply to this email or contact support@kiff.dev.",
	UnsuspendedSubject: "Your KIFF account has been reactivated",
	UnsuspendedBody:    "Your KIFF account has been reactivated. You can use KIFF again.",
	SupportSubject:     "There's an update on your KIFF support ticket",
	// Content-free on purpose: it says an update exists and points back to
	// the in-app thread. The reply itself never travels by email.
	SupportBody: "There's a new update on your KIFF support ticket.",
	SupportView: "View your ticket",
}

// AccountEmails are the other languages' lines, by primary language tag.
var AccountEmails = map[string]AccountEmail{}

// AccountEmailFor is the account emails' lines in lang, or English for a
// language that has none yet.
func AccountEmailFor(lang Lang) AccountEmail {
	if lang != EN {
		if e, ok := AccountEmails[string(lang)]; ok {
			return e
		}
	}
	return accountEmailEN
}
