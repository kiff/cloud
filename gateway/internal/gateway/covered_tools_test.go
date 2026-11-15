package gateway_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
)

// otherTafsRead is another taf's tool on the same account, and ownUncovered a
// tool of this taf's own connection that no Card covers.
var (
	otherTafsRead = gateway.Tool{Name: "get_charge", Description: "Read a charge", ReadOnly: true,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`), UpstreamURL: "http://tool.test/mcp", UpstreamTool: "get_charge"}
	ownUncovered = gateway.Tool{Name: "list_orders", Description: "List orders", ReadOnly: true,
		InputSchema: json.RawMessage(`{"type":"object"}`), UpstreamURL: "http://tool.test/mcp", UpstreamTool: "list_orders"}
)

// listed is tools/list's tool names for key, in a trial of trial when set.
func (r *rig) listed(t *testing.T, key, trial string) []string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+key)
	if trial != "" {
		req.Header.Set(gateway.TrialHeader, trial)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil {
		t.Fatalf("tools/list: %d", resp.StatusCode)
	}
	var names []string
	for _, tool := range out.Result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func cardFor(actions ...string) kiffapi.AgentCard {
	c := kiffapi.AgentCard{AgentID: "coding-agent", Cards: []kiffapi.Card{{}}}
	for _, a := range actions {
		c.Cards[0].Grants = append(c.Cards[0].Grants, kiffapi.CardGrant{Action: a})
	}
	return c
}

// Invariant: a taf is listed only the account's tools one of its own Cards
// covers. Another taf's tool on the same account, and a tool of its own
// connection no Card covers, aren't offered: a call to one is refused, and a
// refused read stops the taf's writes for its session (a version's trial read
// through another taf's get_charge, was refused, and refunded nothing).
// Category: authority
// Attack: a taf on an account where another taf holds a Stripe read lists its tools
// Refused: the other taf's read and the taf's own uncovered tool aren't listed; its Card's tool and KIFF's own are
func TestATafIsListedOnlyItsCardsTools(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{refundTool, otherTafsRead, ownUncovered}
	got := r.listed(t, "agent-key", "")
	if !slices.Contains(got, "issue_refund") || !slices.Contains(got, "kiff_card") || !slices.Contains(got, "kiff_pending") {
		t.Fatalf("listed %v", got)
	}
	for _, name := range []string{"get_charge", "list_orders"} {
		if slices.Contains(got, name) {
			t.Errorf("%s is listed: %v", name, got)
		}
	}
}

// A Card withdrawn mid-session takes its tools off the next list, and a Card
// that adds one puts it on: the list is read from the Cards each time.
func TestACardsToolsComeAndGoWithIt(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{refundTool, otherTafsRead}
	r.api.mu.Lock()
	r.api.card = cardFor("get_charge")
	r.api.mu.Unlock()
	if got := r.listed(t, "agent-key", ""); slices.Contains(got, "issue_refund") || !slices.Contains(got, "get_charge") {
		t.Fatalf("with get_charge's Card: %v", got)
	}
	r.api.mu.Lock()
	r.api.card = cardFor()
	r.api.mu.Unlock()
	if got := r.listed(t, "agent-key", ""); slices.Contains(got, "get_charge") || slices.Contains(got, "issue_refund") {
		t.Fatalf("with its Card withdrawn: %v", got)
	}
}

// In a trial, the list follows the draft's Cards (a tool the version adds is
// offered), and after it the Cards in force again.
func TestATrialListsItsDraftsTools(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{refundTool, ownUncovered}
	r.api.mu.Lock()
	r.api.trialCards = map[string]kiffapi.AgentCard{"drf_v2": cardFor("issue_refund", "list_orders")}
	r.api.mu.Unlock()
	if got := r.listed(t, "agent-key", "drf_v2"); !slices.Contains(got, "list_orders") || !slices.Contains(got, "issue_refund") {
		t.Fatalf("in the trial: %v", got)
	}
	if got := r.listed(t, "agent-key", ""); slices.Contains(got, "list_orders") || !slices.Contains(got, "issue_refund") {
		t.Fatalf("after the trial: %v", got)
	}
}

// Setup's and a tool-less agent's keys are listed as before: a Card's tools
// only, and KIFF's own without one.
func TestKeysWithoutTheRoleAreListedAsBefore(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{refundTool, otherTafsRead}
	for _, key := range []string{"setup-key", "no-tools-key"} {
		if got := r.listed(t, key, ""); slices.Contains(got, "get_charge") || !slices.Contains(got, "issue_refund") {
			t.Fatalf("%s: %v", key, got)
		}
	}
	r.api.mu.Lock()
	r.api.cardFails = true
	r.api.mu.Unlock()
	if got := r.listed(t, "setup-key", ""); slices.Contains(got, "issue_refund") || slices.Contains(got, "get_charge") {
		t.Fatalf("setup without its Cards: %v", got)
	}
}

// A taf whose Card can't be read is listed the account's tools as before,
// rather than none: each call is still decided when it is made.
func TestATafWhoseCardCantBeReadIsListedAsBefore(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{refundTool, otherTafsRead}
	var logged bytes.Buffer
	r.gw.Logger = slog.New(slog.NewTextHandler(&logged, nil))
	r.api.mu.Lock()
	r.api.cardFails = true
	r.api.mu.Unlock()
	if got := r.listed(t, "agent-key", ""); !slices.Contains(got, "issue_refund") || !slices.Contains(got, "get_charge") {
		t.Fatalf("without the Card: %v", got)
	}
	// It's logged, by agent and reason.
	if out := logged.String(); !strings.Contains(out, "listed without its Card") || !strings.Contains(out, "agent=coding-agent") {
		t.Fatalf("logged %q", out)
	}
}

// A taf whose Card KIFF refused to show (a 4xx: a revoked or unknown key) is
// listed KIFF's own tools only: KIFF answered, so nothing falls back.
func TestATafWhoseCardKIFFRefusedIsListedOnlyKIFFsTools(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{refundTool, otherTafsRead}
	r.api.mu.Lock()
	r.api.cardRefused = true
	r.api.mu.Unlock()
	got := r.listed(t, "agent-key", "")
	slices.Sort(got)
	if want := []string{"kiff_card", "kiff_pending"}; !slices.Equal(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
}

// A call to a tool the taf isn't listed is still decided by KIFF, and refused:
// not being listed never means it goes anywhere.
func TestACallToAnUnlistedToolIsStillRefused(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{refundTool, otherTafsRead}
	r.api.mu.Lock()
	r.api.refuse[0] = kiffapi.Decision{Outcome: "blocked", Reasons: []string{"no_mandate_issued"}}
	r.api.mu.Unlock()
	if got := r.listed(t, "agent-key", ""); slices.Contains(got, "get_charge") {
		t.Fatalf("get_charge is listed: %v", got)
	}
	status, out := r.rpc(t, "agent-key", "tools/call", map[string]any{"name": "get_charge",
		"arguments": map[string]any{"id": "ch_1", gateway.OperationArg: "op-unlisted"}})
	res, _ := out["result"].(map[string]any)
	if status != http.StatusOK || res == nil || res["isError"] != true {
		t.Fatalf("tools/call get_charge: %d %v", status, out)
	}
	if r.tool.count() != 0 {
		t.Fatalf("tool calls = %d", r.tool.count())
	}
	r.api.mu.Lock()
	decided := len(r.api.params)
	r.api.mu.Unlock()
	if decided != 1 {
		t.Fatalf("KIFF decided %d calls, want 1", decided)
	}
}

// Invariant: a held call's result opens by saying it isn't done, so a taf
// never reports one as done (it said "the evidence is submitted but waits for
// your approval" of a call KIFF held).
// Category: integrity
// Attack: a taf reads a held call's result and reports the call as made
// Refused: the result begins "HELD: not done, nothing sent." and tells it never to say done, sent or submitted
func TestAHeldResultOpensWithHeld(t *testing.T) {
	r := newRig(t)
	got := r.call(t, "agent-key", amount(5000, "op-held"))
	if !got.isError || !strings.HasPrefix(got.text, "HELD: not done, nothing sent.") ||
		!strings.Contains(got.text, "never say it was done, sent or submitted") ||
		!strings.Contains(got.text, "Waiting for the owner's approval") {
		t.Fatalf("held: %q", got.text)
	}
	if r.tool.count() != 0 {
		t.Fatalf("tool calls = %d", r.tool.count())
	}
	// A hold that names the limit it went over opens the same way.
	why := "40.00 EUR would go over the 250.00 EUR limit: 220.00 EUR is used"
	r.api.mu.Lock()
	r.api.why[4000] = []string{why}
	r.api.mu.Unlock()
	if got := r.call(t, "agent-key", amount(4000, "op-held-why")); !strings.HasPrefix(got.text, "HELD: not done, nothing sent.") || !strings.Contains(got.text, why) {
		t.Fatalf("held over a named limit: %q", got.text)
	}
	// So does a call approved with no room left on the Card.
	r.api.mu.Lock()
	r.api.refuse[3000] = kiffapi.Decision{Outcome: "approved_no_room", Reasons: []string{"mandate_approved_no_room"}}
	r.api.mu.Unlock()
	if got := r.call(t, "agent-key", amount(3000, "op-no-room")); !strings.HasPrefix(got.text, "HELD: not done, nothing sent.") || !strings.Contains(got.text, "no room left") {
		t.Fatalf("approved with no room: %q", got.text)
	}
	if r.tool.count() != 0 {
		t.Fatalf("tool calls = %d", r.tool.count())
	}
}
