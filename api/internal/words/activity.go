package words

// Activity are an Activity call's lines: what came of a call, KIFF's
// decision and what the tool did kept apart, and who answered a held one.
// English is word for word what KIFF wrote before they were translated.
type Activity struct {
	// Who answered a held call, with the language's "by" and its
	// contractions ("pelo proprietário", "dal proprietario"): the reader,
	// someone KIFF can't name, or a role on the account (admin, editor,
	// approver, owner); ByOtherRole for one it has no word for.
	ByYou, BySomeone string
	ByRole           map[string]string
	ByOtherRole      func(role string) string

	// A held call before KIFF records what came of it.
	WaitingForYou  string
	NotSent        string
	NotSentBecause func(reason string) string
	ApprovedOnceBy func(by string) string
	ExtraRoomBy    func(by string) string
	RefusedBy      func(by string) string

	// An allowed call, and what the tool did with it. Allowed is what KIFF
	// decided ("Allowed by its Card", or ApprovedBy); the lines after it
	// take it as their start. why is the tool's own message, or "".
	AllowedByCard     string
	ApprovedBy        func(by string) string
	ApprovedThenDone  func(by string) string
	DoneWithinCard    string
	ConnectionRefused func(why string) string
	AppRefusedCounts  func(why string) string
	AppRefusedMoney   string
	AppRefusedOther   string
	NotSentToTool     func(allowed, why string) string
	Sending           func(allowed string) string
	AnswerLost        func(allowed string) string
	// FoundAtVendor: its outcome wasn't known, and the profile's status
	// read found it at the vendor by its operation id (kiff/tafs#498).
	FoundAtVendor func(allowed string) string
	NotRecorded   func(allowed string) string
	// ConnectionWhy is why a connection refused a call, by its code.
	ConnectionWhy map[string]string
}
