package pages

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
)

func renderReceipt(t *testing.T, v ReceiptView) string {
	t.Helper()
	var b strings.Builder
	if err := receiptBody(v).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(b.String(), "&#39;", "'")
}

func settledRejection() *apiclient.Receipt {
	at := time.Date(2026, 10, 13, 1, 56, 29, 0, time.UTC)
	answered := at.Add(25 * time.Minute)
	return &apiclient.Receipt{SchemaVersion: "v0.1", TraceID: "settled-tc-74e3", ProposalID: "tc-74e3", TenantID: "01m4ep", EntityID: "tc-74e3",
		ActionName: "shop_refund_order", Outcome: "rejected", IssuedAt: answered,
		CausalChain: []apiclient.ChainStep{
			{Sequence: 1, Kind: "proposal_decision", ActorID: "luna-desk", OccurredAt: at, Message: "held for its owner"},
			{Sequence: 2, Kind: "proposal_decision", ActorID: "luna-desk", OccurredAt: at, Message: "asked again: held for its owner"},
			{Sequence: 3, Kind: "hold_settled", ActorID: "ai@kiff.dev", OccurredAt: answered, Message: "rejected, next step: return_first",
				Data: map[string]any{"outcome": "rejected", "answer": "reject", "answered_by": "ai@kiff.dev", "answered_by_id": "user_3KQe",
					"role": "admin", "separation_of_duties": false, "next_step": "return_first", "note": "Ask for photos."}},
		},
		Attestation: &apiclient.Attestation{Algorithm: "secp256k1", Signature: "0x" + strings.Repeat("ab", 32), WalletAddress: "0x9f00000000000000000000000000000000003a21",
			AnchorTxHash: "0x7c" + strings.Repeat("0", 60) + "e104", AnchoredAt: answered.Add(24 * time.Hour), Network: "base-sepolia"}}
}

// A receipt tells the call in words, then the proof and how to check
// it; the old sections and crypto wording are gone.
func TestReceiptPage(t *testing.T) {
	r := settledRejection()
	page := renderReceipt(t, ReceiptView{R: r, Related: "decision-tc-74e3", Signed: "/dashboard/receipts/settled-tc-74e3/signed"})
	for _, want := range []string{
		"<code>shop_refund_order</code>", // no "on tc-74e3"
		"luna-desk asked to run shop_refund_order. Its Card held it for you; ai@kiff.dev rejected it and told the agent: Return first. Nothing ran.",
		"luna-desk asked: held for its owner", "Asked again: held for its owner",
		"Rejected: Return first", "ai@kiff.dev, an admin · separation of duties off · note: “Ask for photos.”", "Nothing ran",
		"Signed and anchored", `href="/dashboard/receipts/settled-tc-74e3/signed"`, "See the signed receipt",
		"How to check it", "Technical details", "Base Sepolia",
		"The receipt at the hold", `href="/dashboard/receipts/decision-tc-74e3"`,
		"<dt>Agent</dt>", "<dt>Call</dt>", "<dt>Account</dt>",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("receipt lacks %q", want)
		}
	}
	for _, never := range []string{"on tc-74e3", "Merkle", "on-chain", "Attested Action Receipt", "Compliance", "Insurer", "Risk score",
		"domain behind this decision", "production state", "the call ran", "proposal decision recorded"} {
		if strings.Contains(page, never) {
			t.Errorf("receipt still says %q", never)
		}
	}
}

// A decision receipt says what was decided and never that the call ran;
// unsigned and signed-only receipts say how far their proof goes.
func TestReceiptPageOutcomesAndProof(t *testing.T) {
	at := time.Date(2026, 10, 13, 2, 48, 0, 0, time.UTC)
	for outcome, want := range map[string]string{
		"allowed":           "It fit its Card, so KIFF allowed it.",
		"blocked":           "KIFF refused it, so nothing was authorized.",
		"owner_required":    "Its Card held it for its owner.",
		"approval_required": "KIFF held it for an approver.",
		"approved_no_room":  "its Card had no room for it yet",
	} {
		r := &apiclient.Receipt{TraceID: "decision-p", ProposalID: "p", EntityID: "order-1043", ActionName: "refund_order", Outcome: outcome, IssuedAt: at,
			CausalChain: []apiclient.ChainStep{{Sequence: 1, Kind: "proposal_decision", ActorID: "onboard-agent", OccurredAt: at, Message: "proposal decision recorded"}}}
		page := renderReceipt(t, ReceiptView{R: r})
		if !strings.Contains(page, want) || !strings.Contains(page, "refund_order on order-1043") || strings.Contains(page, "call ran") ||
			!strings.Contains(page, "onboard-agent asked: "+receiptOutcomeLabel(outcome)) {
			t.Errorf("%s: lead wrong or implies the call ran", outcome)
		}
		if !strings.Contains(page, "Not signed yet") || strings.Contains(page, "See the signed receipt") {
			t.Errorf("%s: an unsigned receipt offers the signed JSON or claims a signature", outcome)
		}
	}
	// Issued since receipts say who refused a call, so an app's own refusal is quoted.
	signed := &apiclient.Receipt{TraceID: "decision-p", ActionName: "refund_order", Outcome: "allowed", IssuedAt: refusedByRecordedSince.Add(time.Hour),
		CausalChain: []apiclient.ChainStep{{Sequence: 1, Kind: "proposal_decision", ActorID: "a", OccurredAt: at, Message: "allowed"},
			{Sequence: 2, Kind: "tool_outcome", OccurredAt: at, Data: map[string]any{"status": "refused_by_tool", "message": "only 0.00 is left"}}},
		Attestation: &apiclient.Attestation{WalletAddress: "0xabc", Signature: "0xdef"}}
	page := renderReceipt(t, ReceiptView{R: signed, Signed: "/s"})
	for _, want := range []string{"Signed", "It is not in a public record yet.", "Refused by the tool", "The tool said: “only 0.00 is left”", "See the signed receipt"} {
		if !strings.Contains(page, want) {
			t.Errorf("signed receipt lacks %q", want)
		}
	}
	if strings.Contains(page, "Signed and anchored") || strings.Contains(page, "Public record") {
		t.Error("a receipt not in a public record claims one")
	}
}

// a review: a refusal then an allowed retry, with no tool report, was
// authorized; the timeline never ends "Nothing ran".
func TestReceiptRefusedThenAllowed(t *testing.T) {
	at := time.Date(2026, 10, 13, 2, 0, 0, 0, time.UTC)
	r := &apiclient.Receipt{TraceID: "decision-p", ProposalID: "p", ActionName: "refund_order", Outcome: "allowed", IssuedAt: at,
		CausalChain: []apiclient.ChainStep{
			{Sequence: 1, Kind: "proposal_decision", ActorID: "a", OccurredAt: at, Message: "refused"},
			{Sequence: 2, Kind: "proposal_decision", ActorID: "a", OccurredAt: at.Add(time.Minute), Message: "allowed"},
		}}
	page := renderReceipt(t, ReceiptView{R: r})
	if strings.Contains(page, "Nothing ran") || strings.Contains(page, "never authorized") {
		t.Error("an allowed retry ends with nothing ran")
	}
	// A refusal never followed by an authorization still says so.
	r.CausalChain = r.CausalChain[:1]
	r.Outcome = "blocked"
	if page := renderReceipt(t, ReceiptView{R: r}); !strings.Contains(page, "Nothing ran") {
		t.Error("a plain refusal lost its ending")
	}
}

// a review: a settled receipt's outcome is how the hold settled, not
// its last decision. Legacy decision steps (no per-step outcome) stay
// "decided", never the person's later answer.
func TestReceiptLegacySettledKeepsDecisionUnknown(t *testing.T) {
	at := time.Date(2026, 10, 13, 1, 56, 0, 0, time.UTC)
	for _, settledAs := range []string{"rejected", "approved_once", "card_changed"} {
		r := &apiclient.Receipt{TraceID: "settled-p", ProposalID: "p", ActionName: "shop_refund_order", Outcome: settledAs, IssuedAt: at.Add(time.Hour),
			CausalChain: []apiclient.ChainStep{
				{Sequence: 1, Kind: "proposal_decision", ActorID: "luna-desk", OccurredAt: at, Message: "proposal decision recorded"},
				{Sequence: 2, Kind: "hold_settled", ActorID: "ai@kiff.dev", OccurredAt: at.Add(time.Hour),
					Data: map[string]any{"outcome": settledAs, "answered_by": "ai@kiff.dev", "answer": "x"}},
			}}
		page := renderReceipt(t, ReceiptView{R: r})
		if !strings.Contains(page, "luna-desk asked: decided") {
			t.Errorf("%s: the legacy decision is not left as decided", settledAs)
		}
		for _, wrong := range []string{"asked: rejected", "asked: approved once", "asked: Card changed"} {
			if strings.Contains(page, wrong) {
				t.Errorf("%s: the settlement is pinned on the decision (%q)", settledAs, wrong)
			}
		}
	}
}

// The signed receipt is shown to read first; downloading it is a choice
// on that page, and the folds on the receipt read as rows to open.
func TestReceiptSignedView(t *testing.T) {
	page := renderReceipt(t, ReceiptView{R: settledRejection(), Signed: "/s"})
	if strings.Count(page, `class="ac-chev"`) != 2 || strings.Contains(page, "/download") {
		t.Error("the folds lack their chevrons, or the receipt still links the download directly")
	}
	var b strings.Builder
	v := SignedReceiptView{TraceID: "settled-tc-74e3", JSON: "{\n  \"trace_id\": \"settled-tc-74e3\"\n}", Back: "/dashboard/receipts/settled-tc-74e3", Download: "/dashboard/receipts/settled-tc-74e3/download"}
	if err := DashboardReceiptSigned(v, components.NewDashboardChrome("A", "a@kiff.test", "")).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`href="/dashboard/receipts/settled-tc-74e3"`, "← The receipt", "Download saves it exactly as signed",
		`<pre class="rc-json"><code>{
  &#34;trace_id&#34;: &#34;settled-tc-74e3&#34;
}</code></pre>`, `href="/dashboard/receipts/settled-tc-74e3/download" download>Download</a>`} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("signed view lacks %q", want)
		}
	}
}

// A settled answer signed on a phone names the phone and its key, and the
// proof says the answer can be checked without KIFF.
func TestReceiptNamesThePhoneThatSigned(t *testing.T) {
	r := settledRejection()
	der := []byte{0x30, 0x59, 0x30, 0x13}
	for i := range r.CausalChain {
		if r.CausalChain[i].Kind == "hold_settled" {
			r.CausalChain[i].Data["device"] = map[string]any{"name": "tafs on iPhone", "key_id": "sess-phone",
				"public_key": base64.StdEncoding.EncodeToString(der), "signed_message": "kiff.answer.v1", "signature": "MEUC"}
		}
	}
	sum := sha256.Sum256(der)
	fp := hex.EncodeToString(sum[:8])
	fp = fp[0:4] + " " + fp[4:8] + " " + fp[8:12] + " " + fp[12:16]
	page := renderReceipt(t, ReceiptView{R: r, Signed: "/s"})
	for _, want := range []string{"signed on tafs on iPhone with key " + fp, "signed on the approver's phone, with key " + fp} {
		if !strings.Contains(page, want) {
			t.Errorf("receipt lacks %q", want)
		}
	}
	if strings.Contains(renderReceipt(t, ReceiptView{R: settledRejection(), Signed: "/s"}), "approver's phone") {
		t.Error("an answer without a phone claims one")
	}
}

// A receipt keeps the connection's and the gateway's own text as the record,
// but the page never quotes it: a call its connection refused, one never
// sent and one whose answer was lost each read in KIFF's words, by status.
// An app's own refusal is still quoted, as the app's.
func TestReceiptNeverQuotesTheConnectionsText(t *testing.T) {
	at := time.Date(2026, 11, 18, 22, 0, 0, 0, time.UTC)
	const said = "This app isn't offered through tafs any more, so this connection is closed. Nothing was sent."
	for name, c := range map[string]struct {
		data map[string]any
		want string
	}{
		"refused by its connection": {map[string]any{"status": "refused_by_tool", "refused_by": "connection", "code": "app_closed", "message": said}, "Refused by the connection before sending"},
		"never sent":                {map[string]any{"status": "not_sent", "message": said}, "Not sent to the tool"},
		"its answer lost":           {map[string]any{"status": "unknown", "message": said}, "Outcome unknown"},
	} {
		r := &apiclient.Receipt{TraceID: "settled-p", ActionName: "append_row", Outcome: "allowed", IssuedAt: at,
			CausalChain: []apiclient.ChainStep{{Sequence: 1, Kind: "proposal_decision", ActorID: "a", OccurredAt: at, Message: "allowed"},
				{Sequence: 2, Kind: "tool_outcome", OccurredAt: at, Data: c.data}}}
		page := renderReceipt(t, ReceiptView{R: r})
		if !strings.Contains(page, c.want) {
			t.Errorf("%s: no %q", name, c.want)
		}
		for _, word := range []string{"offered through tafs", "connection is closed", "The tool said"} {
			if strings.Contains(page, word) {
				t.Errorf("%s: the page quotes %q", name, word)
			}
		}
	}
}

// A receipt issued before receipts said who refused a call quotes no
// refusal's message: it can't tell a connection's refusal from an app's own,
// so none of a connection's text is read as written. One issued since quotes
// an app's own refusal as before.
func TestAnOldReceiptQuotesNoRefusal(t *testing.T) {
	const said = "This app isn't offered through tafs any more, so this connection is closed. Nothing was sent."
	page := func(issued time.Time) string {
		r := &apiclient.Receipt{TraceID: "settled-p", ActionName: "append_row", Outcome: "allowed", IssuedAt: issued,
			CausalChain: []apiclient.ChainStep{{Sequence: 1, Kind: "proposal_decision", ActorID: "a", OccurredAt: issued, Message: "allowed"},
				{Sequence: 2, Kind: "tool_outcome", OccurredAt: issued, Data: map[string]any{"status": "refused_by_tool", "message": said}}}}
		return renderReceipt(t, ReceiptView{R: r})
	}
	old := page(refusedByRecordedSince.Add(-time.Hour))
	if strings.Contains(old, "offered through tafs") || strings.Contains(old, "The tool said") || strings.Contains(old, "Refused by the tool") ||
		!strings.Contains(old, "Refused before it was done") || !strings.Contains(old, "record whether the tool or its connection refused it") {
		t.Errorf("an old receipt quotes its refusal or names who refused it: %s", old)
	}
	if !strings.Contains(old, "Then refused before it was done.") {
		t.Errorf("the old receipt's lead names who refused it: %s", old)
	}
	if since := page(refusedByRecordedSince.Add(time.Hour)); !strings.Contains(since, "The tool said: “"+said+"”") {
		t.Error("a receipt since the cutoff lost an app's own refusal")
	}
}

// A receipt issued since says who refused a call: a connection's refusal
// reads as the connection's in the lead and the step, never "by the tool".
func TestAReceiptsLeadNamesTheConnection(t *testing.T) {
	at := refusedByRecordedSince.Add(time.Hour)
	r := &apiclient.Receipt{TraceID: "settled-p", ActionName: "append_row", Outcome: "allowed", IssuedAt: at,
		CausalChain: []apiclient.ChainStep{{Sequence: 1, Kind: "proposal_decision", ActorID: "a", OccurredAt: at, Message: "allowed"},
			{Sequence: 2, Kind: "tool_outcome", OccurredAt: at, Data: map[string]any{"status": "refused_by_tool", "refused_by": "connection", "code": "app_closed"}}}}
	page := renderReceipt(t, ReceiptView{R: r})
	if !strings.Contains(page, "Then refused by the connection before sending.") || strings.Contains(page, "refused by the tool") {
		t.Errorf("the lead: %s", page)
	}
}
