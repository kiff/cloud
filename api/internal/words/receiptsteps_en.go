package words

// A receipt's steps in en.
func init() {
	en.ReceiptSteps = ReceiptSteps{
		ReceiptStepLines: map[string]string{
			"decision.allowed":           "Allowed",
			"decision.refused":           "Refused",
			"decision.held_for_owner":    "Held for its owner",
			"decision.held_for_approval": "Held for approval",
			"decision.approved_no_room":  "Approved, waiting for room",
			"decision.invalid":           "Invalid",
			"settled.approved_once":      "Approved once",
			"settled.rejected":           "Rejected",
			"settled.rejected_next_step": "Rejected. Next step: %s",
			"settled.card_changed":       "The Card was changed instead",
			"settled.fit_on_retry":       "Nobody answered; on a retry it fit the Card as it then stood",
			"settled.expired":            "Nobody answered before the hold expired",
			"settled.withdrawn":          "Withdrawn",
			"settled.withdrawn_reason":   "Withdrawn: %s",
			"tool.reported":              "The tool reported: %s",
			"handoff.handed":             "Handed to its owner; KIFF sends nothing for this step",
			"handoff.done":               "The owner says they did it",
			"handoff.skipped":            "The owner chose not to",
			"handoff.no_answer":          "Nobody answered before it expired; never read as done",
			"handoff.checked":            "Checked with the vendor after the owner's answer: %s",
			"owner.halted":               "Halted by the owner: KIFF decides none of its calls until it's resumed",
			"owner.resumed":              "Resumed by the owner: new calls are decided again, nothing asked before the halt",
			"state.not_allowed":          "Not allowed in its current state: %s",
		},
		ReceiptAskedAgain: func(line string) string { return "Asked again: " + line },
	}
}
