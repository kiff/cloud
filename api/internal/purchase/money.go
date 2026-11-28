package purchase

// The money's status, kept apart from the case's and the order's.
const (
	MoneyNone         = "none"
	MoneyAuthorized   = "authorized"
	MoneyCaptured     = "captured"
	MoneyCapturedLess = "captured_less"
	MoneySplit        = "split"
	MoneyOverCapture  = "over_capture"
	MoneyForceCapture = "force_capture"
	MoneyReversed     = "reversed"
	MoneyRefunded     = "refunded"
	MoneyLapsed       = "lapsed"
	MoneyDisputed     = "disputed"
)

// CardEvent is one of the issuer's card events for a case's card.
type CardEvent struct {
	Kind string // authorization, capture, reversal, refund, lapse, dispute
	// Authorization is the authorization the event belongs to: "" for a
	// capture no authorization covers (a force capture).
	Authorization string
	Amount        int64
}

// Settlement is what reconciliation found: the money's status, the
// amounts, and whether the owner must be told at once (an over-capture or
// a force capture, which pause the Card's buying).
type Settlement struct {
	Money      string
	Authorized int64
	Captured   int64
	Refunded   int64
	Exception  bool
}

// Settle reconciles a case's card events against what was approved: the
// authorizations KIFF approved (approved, by id) and the case's maximum.
// A capture above what was authorized, or one no approved authorization
// covers, is an exception: reported, never blocked, since a capture can't
// be.
func Settle(events []CardEvent, approved map[string]bool, max int64) Settlement {
	var s Settlement
	captures, reversed, lapsed, disputed := 0, false, false, false
	for _, e := range events {
		switch e.Kind {
		case "authorization":
			if approved[e.Authorization] {
				s.Authorized += e.Amount
			}
		case "capture":
			if e.Authorization == "" || !approved[e.Authorization] {
				s.Money, s.Exception = MoneyForceCapture, true
			}
			s.Captured += e.Amount
			captures++
		case "reversal":
			s.Authorized -= e.Amount
			reversed = true
		case "refund":
			s.Refunded += e.Amount
		case "lapse":
			lapsed = true
		case "dispute":
			disputed = true
		}
	}
	if s.Money == MoneyForceCapture {
		return s
	}
	switch {
	case disputed:
		s.Money = MoneyDisputed
	case s.Captured > s.Authorized || s.Captured > max:
		s.Money, s.Exception = MoneyOverCapture, true
	case s.Refunded > 0:
		s.Money = MoneyRefunded
	case captures > 1:
		s.Money = MoneySplit
	case captures == 1 && s.Captured == s.Authorized:
		s.Money = MoneyCaptured
	case captures == 1:
		s.Money = MoneyCapturedLess
	case reversed && s.Authorized <= 0:
		s.Money = MoneyReversed
	case lapsed:
		s.Money = MoneyLapsed
	case s.Authorized > 0:
		s.Money = MoneyAuthorized
	default:
		s.Money = MoneyNone
	}
	return s
}
