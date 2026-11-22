package words

// The code sign-in emails in en.
func init() {
	en.CodeSignInEmail = CodeSignInEmail{
		CodeSubject:          "Your KIFF sign-in code",
		CodeIntro:            "Someone asked to sign in to KIFF with a code sent to this address. Your code:",
		CodeValid:            "It works once, for 10 minutes, in the browser that asked for it.",
		CodeNeverShare:       "Never give this code to anyone. KIFF will never ask you for it.",
		CodeNotYou:           "If it wasn't you, ignore this email. No one can sign in without the code.",
		CodeAskedWhen:        "When",
		CodeAskedBrowser:     "Browser",
		CodeAskedPlace:       "Approximate location",
		CodeOffSubject:       "Signing in with a code isn't on for your KIFF account",
		CodeOffIntro:         "Someone asked to sign in to KIFF with a code sent to this address. Signing in with a code isn't on for your account, so no code was sent.",
		CodeOffHow:           "To use it, sign in another way, then turn on \"Sign in with a code sent by email\" in your account's Sign-in settings.",
		CodeOffNotYou:        "If it wasn't you, ignore this email. Nothing has changed.",
		CodeTurnedOnSubject:  "Signing in with a code is now on for your KIFF account",
		CodeTurnedOnIntro:    "Signing in with a code sent by email was turned on for your KIFF account.",
		CodeTurnedOffSubject: "Signing in with a code is now off for your KIFF account",
		CodeTurnedOffIntro:   "Signing in with a code sent by email was turned off for your KIFF account.",
		CodeChangedNotYou:    "If it wasn't you, sign in another way, check your account's Sign-in settings and sign out the devices you don't know.",
		CodeWhy:              "KIFF sends this when someone asks to sign in with a code sent to this address.",
	}
}
