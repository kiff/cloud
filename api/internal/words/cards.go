package words

// Cards are the words of a Card's terms as the owner reads them before
// confirming it ("Refund a payment: up to €40 each", "€300 in total a
// day"), and of the amounts, held reasons and pending use around them.
// internal/cardwords composes them; English is word for word what KIFF
// wrote before they were translated.
type Cards struct {
	// How an amount is written (Money); a bare number says it has no
	// currency with NoCurrency after it.
	Money      MoneyStyle
	NoCurrency string

	// A Card's lines. UpToEach is a grant's cap for one call; a Card that
	// asks past its limits adds MoreAsksFirst to a grant's caps and
	// ThenAsks to a total; EachCallAsks is a count of 0 on such a Card.
	UpToEach      func(amount string) string
	MoreAsksFirst string
	ThenAsks      string
	EachCallAsks  string
	// CallCount is a number of calls ("1 call", "20 calls"); a total ends
	// with its window's words, by window: an amount's (WindowAmount, "in
	// total a day") or a count's (WindowCount, "a day"). ForEach ends a
	// per-item total ("for each invoice").
	CallCount    func(n int64) string
	WindowAmount map[string]string
	WindowCount  map[string]string
	ForEach      func(item string) string

	// A held call's reason: which call it would be (Ordinal, "21st"),
	// against a count ("20 calls a day: this would be the 21st"), or an
	// amount over a limit for one call or a total; and when KIFF has
	// neither, why it's outside the Card.
	Ordinal         func(n int64) string
	WouldBe         func(limit, ordinal string) string
	WouldBeFor      func(limit, ordinal, value string) string
	OverCalls       func(limit int64, ordinal string) string
	OverForOneCall  func(amount, limit string) string
	OverTheTotal    func(amount, limit string) string
	OutsideCard     string
	OverActionLimit string
	OverCardTotal   string
	NotCardResource string

	// A limit's use still pending from calls the tool refused: a count's,
	// an amount's without a unit, and an amount's.
	PendingCalls  func(calls int) string
	PendingNoUnit func(calls int) string
	PendingAmount func(amount string, calls int) string

	// A draft's title, from the name it was submitted with ("Refunds'
	// Card"); TafCard when it has none.
	CardOf  func(label string) string
	TafCard string
}

// MoneyStyle is how a language writes an amount: the thousands separator,
// the currency after the amount ("40 €") or before it ("€40"), and what
// goes between them ("" for "€40", a space for "€ 40" or "40 €"). The
// decimal separator is Words.DecimalComma's.
type MoneyStyle struct {
	Thousands     string
	CurrencyAfter bool
	CurrencyGap   string
}
