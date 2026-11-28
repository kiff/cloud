package purchase

// The case's states, as contracts/purchase.json lists them, and where each
// may go. The money's and the order's statuses are kept apart from these.
const (
	StateHeld       = "held"
	StateApproved   = "approved"
	StateRefused    = "refused"
	StateSuperseded = "superseded"
	StateExpired    = "expired"
	StateExecuting  = "executing"
	StateUnknown    = "unknown"
	StateDone       = "done"
	StateNotBought  = "not_bought"
)

var next = map[string][]string{
	StateHeld:       {StateApproved, StateRefused, StateSuperseded, StateExpired},
	StateApproved:   {StateExecuting, StateExpired, StateNotBought},
	StateExecuting:  {StateDone, StateUnknown, StateNotBought},
	StateUnknown:    {StateDone, StateApproved, StateNotBought},
	StateRefused:    nil,
	StateSuperseded: nil,
	StateExpired:    nil,
	StateDone:       nil,
	StateNotBought:  nil,
}

// CanMove says whether a case in from may become to.
func CanMove(from, to string) bool {
	for _, s := range next[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Why a case ended with nothing bought.
const (
	EndedNotUsedInTime    = "not_used_in_time"
	EndedIssuerCheck      = "issuer_check"
	EndedDeclinedAtAuth   = "declined_at_authorization"
	EndedAddressChanged   = "address_changed"
	EndedCardNumberAsked  = "card_number_asked"
	EndedRoomTaken        = "room_taken"
	EndedPriceOutsideRule = "price_outside_rule"
)

// An execution's outcome.
const (
	OutcomeNone       = "none"
	OutcomeOrdered    = "ordered"
	OutcomeNotOrdered = "not_ordered"
	OutcomeUnknown    = "unknown"
)
