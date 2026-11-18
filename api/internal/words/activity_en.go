package words

// English Activity lines: word for word as KIFF wrote them before they
// were translated.
func init() {
	why := func(w string) string {
		if w == "" {
			return ""
		}
		return ": " + w
	}
	en.Activity = Activity{
		ByYou: "by you", BySomeone: "by a person on the account",
		ByRole:      map[string]string{"admin": "by an admin", "editor": "by an editor", "approver": "by an approver", "owner": "by an owner"},
		ByOtherRole: func(role string) string { return "by an " + role },

		WaitingForYou:  "Waiting for you",
		NotSent:        "Not sent",
		NotSentBecause: func(reason string) string { return "Not sent · " + reason },
		ApprovedOnceBy: func(by string) string { return "Approved once " + by },
		ExtraRoomBy:    func(by string) string { return "Extra room " + by },
		RefusedBy:      func(by string) string { return "Refused " + by },

		AllowedByCard:    "Allowed by its Card",
		ApprovedBy:       func(by string) string { return "Approved " + by },
		ApprovedThenDone: func(by string) string { return "Approved " + by + ", then done" },
		DoneWithinCard:   "Done, within its Card",
		ConnectionRefused: func(w string) string {
			return "Not sent: the connection refused it before sending" + why(w) + ". It still counts toward today's limits until KIFF can confirm nothing was done."
		},
		AppRefusedCounts: func(w string) string { return "The app refused it, and it still counts toward today's limits" + why(w) },
		AppRefusedMoney:  "The app refused it. It counts against today's total until KIFF can confirm no money moved.",
		AppRefusedOther:  "The app refused it. It counts against today's total until KIFF can confirm nothing was done.",
		NotSentToTool: func(allowed, w string) string {
			return allowed + ", but it couldn't be sent: nothing reached the tool" + why(w)
		},
		Sending: func(allowed string) string { return allowed + ", and sent: waiting for the tool's answer" },
		AnswerLost: func(allowed string) string {
			return allowed + ", and sent, but the tool's answer was lost: check the tool's own records"
		},
		FoundAtVendor: func(allowed string) string {
			return allowed + ", and sent. KIFF couldn't tell how it ended, so it looked in the tool's own records and found it there: it went through"
		},
		NotRecorded: func(allowed string) string {
			return allowed + ", but whether the tool did it wasn't recorded: check the tool's own records"
		},
		ConnectionWhy: map[string]string{
			"halt_unknown":        "it couldn't check whether the agent was halted",
			"halted":              "the agent or the account is halted",
			"halted_before":       "the call was approved before the latest halt",
			"not_granted":         "the connection doesn't include this action",
			"permit_invalid":      "KIFF's permit didn't match the call",
			"permit_expired":      "KIFF's permit expired before the call left",
			"bad_arguments":       "the call's arguments couldn't be read",
			"record_unavailable":  "it couldn't keep a record of the call",
			"profile_not_applied": "KIFF hasn't applied the approved settings yet",
			"definition_invalid":  "the action names a field the app doesn't take",
			"definition_unread":   "it couldn't check the action against the app",
			"operation_changed":   "the app changed this action since it was approved; the connection sends nothing until it's connected again",
			"signed_out":          "the app ended the sign-in; the connection sends nothing until you sign in again",
		},
	}
}
