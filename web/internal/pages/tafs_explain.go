package pages

// tafsExplain is what screen 7's four stickers open: a small explainer under
// each one. Every line is true today for every route it names; the fingerprint
// line is per step only.
type tafsExplain struct {
	// "Open code": one line, then the four public repos, each a node with its
	// own line. The arcs between them carry no words.
	Open  string
	Nodes [4]tafsNode
	// "Proof of everything": what a receipt holds, and the receipt crop's alt
	// text.
	Proof, ProofAlt string
	// "You stay in control": the alert, the Card and the held call, each with
	// its line and its crop's alt text.
	Control, ControlAlt [3]string
	// "Privacy first": the heading, then one step per route; Partner names the
	// link to /subprocessors from the managed route.
	PrivacyHead string
	Privacy     [3]string
	Partner     string
	// The name of the buttons that choose a step.
	Step string
}

// tafsNode is one public repo in "Open code".
type tafsNode struct{ Name, Line string }

// tafsRepos are the four public repos, in the order of tafsExplain.Nodes.
var tafsRepos = [4]string{"tafs", "agents", "cloud", "planque"}

var tafsExplainEN = tafsExplain{
	Open: "The code is open. Read it on GitHub.",
	Nodes: [4]tafsNode{
		{"tafs", "The apps, and the tafs that do your routine work."},
		{"agents", "The runtime every taf runs on."},
		{"cloud", "KIFF: your Cards, its decisions, and signed receipts."},
		{"planque", "The keys you give it, sealed. Every call checked."},
	},
	Proof:    "What was done, who approved it, and KIFF's signature on it.",
	ProofAlt: "A receipt in the tafs app: a €3,000 refund, approved once by you, with KIFF's receipt, signed and checked.",
	Control: [3]string{
		"Needs you: what waits for your answer, and until when.",
		"The Card: what a taf may do, what waits for you, and what it never does.",
		"Each step that needs you asks for your fingerprint.",
	},
	ControlAlt: [3]string{
		"An alert in the tafs app: a refund of €100 is over the €50 limit for one call, with Answer.",
		"A taf's Card in the tafs app: refunds up to €50 each, more asks you first, at most €5,000 a day; anything outside it is refused.",
		"A held call in the tafs app: a €100 refund waits, nothing is sent until you approve, with Approve once with your fingerprint.",
	},
	PrivacyHead: "The AI never sees the passwords or keys your connected apps use.",
	Privacy: [3]string{
		"Direct connections: planque keeps the key or sign-in sealed and opens it only for that connection's calls. The taf and the AI never see it.",
		"Managed connections: our sign-in partner holds the sign-in, never planque or a taf.",
		"Your own Chrome: a taf never reads password, card or code fields. At a sign-in page it stops and asks you to sign in in your own tab.",
	},
	Partner: "Who our sign-in partner is",
	Step:    "Step",
}
