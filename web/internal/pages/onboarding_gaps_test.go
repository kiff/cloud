package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
)

// Gaps found running the onboarding as a new account (2026-10-08).

// A held gateway call says what it will be sent with, not KIFF's id for
// the call: "get_order on tc-94dffd…" told the person nothing.
func TestHeldCallShowsItsArgumentsNotTheCallKey(t *testing.T) {
	render := func(e apiclient.CardException, detail bool) string {
		t.Helper()
		var b bytes.Buffer
		if err := needsYouItem(e, detail, nil).Render(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	// Decoded from the API's JSON, as the web client does: a large
	// integer keeps every digit end to end.
	var e apiclient.CardException
	if err := json.Unmarshal([]byte(`{"id":"exc-1","proposal_id":"tc-94dffd352e23d1071d366c0c3d4b1415589c4cc3",
		"entity_id":"tc-94dffd352e23d1071d366c0c3d4b1415589c4cc3","subject":"onboard-agent","action":"get_order","status":"held",
		"arguments":{"order_id":"70349","account":9007199254740993,"lines":["a","b"],"note":"<script>x</script>","spaced":"A  B"}}`), &e); err != nil {
		t.Fatal(err)
	}
	body := render(e, false)
	for _, want := range []string{"<dt>order_id</dt>", "<code>&#34;70349&#34;</code>", "<code>9007199254740993</code>",
		`<code>[&#34;a&#34;,&#34;b&#34;]</code>`, "<code>&#34;A  B&#34;</code>", "call tc-94dffd352e2…"} {
		if !strings.Contains(body, want) {
			t.Errorf("held call missing %q", want)
		}
	}
	if strings.Contains(body, "on <code>tc-") {
		t.Error("held call still names KIFF's call id as what it is on")
	}
	// The agent wrote the arguments: they are escaped data.
	if strings.Contains(body, "<script>x</script>") {
		t.Error("an argument was rendered as markup")
	}
	// "A  B" and "A B" are different strings to the tool, and look it.
	e.Arguments["spaced"] = json.RawMessage(`"A B"`)
	if strings.Contains(render(e, false), "&#34;A  B&#34;") {
		t.Error("whitespace collapsed")
	}

	// Two values that differ only after the preview: the list says it
	// shortened them and links to the detail, which shows both in full.
	long := strings.Repeat("x", 200)
	e.Arguments = map[string]json.RawMessage{"to": json.RawMessage(`"` + long + `-1"`)}
	other := e
	other.Arguments = map[string]json.RawMessage{"to": json.RawMessage(`"` + long + `-2"`)}
	if render(e, false) != render(other, false) {
		t.Fatal("previews of values that differ only after the cut were expected to match")
	}
	if !strings.Contains(render(e, false), "See every value in full") {
		t.Error("a shortened preview does not say so")
	}
	if !strings.Contains(render(e, true), long+"-1") || !strings.Contains(render(other, true), long+"-2") || strings.Contains(render(e, true), "See every value in full") {
		t.Error("the detail does not show every value in full")
	}

	// No arguments, and arguments that could not be read, say different
	// things; neither is shown as the other.
	e.Arguments = map[string]json.RawMessage{}
	if !strings.Contains(render(e, false), "with no arguments") {
		t.Error("a call with no arguments")
	}
	e.Arguments, e.ArgumentsUnavailable = nil, true
	if b := render(e, false); !strings.Contains(b, "could not be read") || strings.Contains(b, "with no arguments") {
		t.Error("unavailable arguments")
	} else if strings.Contains(b, "Check with the agent") {
		t.Error("unreadable arguments send the approver to the agent's own account")
	}

	// A call made from code (not through the gateway) keeps its entity.
	code := apiclient.CardException{ID: "exc-2", EntityID: "ord-42", Action: "REFUND_ORDER", Status: "held"}
	if b := render(code, false); !strings.Contains(b, "on <code>ord-42</code>") || strings.Contains(b, "ny-callid") || strings.Contains(b, "could not be read") {
		t.Errorf("entity: %s", b)
	}
}

// Who answered is a person, not a sign-in id ("user_3KQe…").
func TestNameAnswerer(t *testing.T) {
	members := []apiclient.Member{{Subject: "user_bob", Email: "bob@example.com"}}
	for _, c := range []struct{ by, want string }{
		{"user_me", "you (me@example.com)"},
		{"user_bob", "bob@example.com"},
		{"user_gone", "user_gone"},
		{"", ""},
	} {
		e := apiclient.CardException{ResolvedBy: c.by}
		NameAnswerer(&e, "user_me", "me@example.com", members)
		if e.ResolvedBy != c.want {
			t.Errorf("%q: got %q, want %q", c.by, e.ResolvedBy, c.want)
		}
	}
}

// An agent holding a tool Card, or any agent of a personal account, is
// connected through the gateway: its page leads with that, and keeps
// kiff-guard one click away. A company agent with only domain Cards
// still leads with kiff-guard.
func TestAgentOverviewLeadsWithTheGatewayForToolAgents(t *testing.T) {
	render := func(ctx context.Context, v AgentDetailView) string {
		t.Helper()
		var b bytes.Buffer
		if err := AgentDetail(v).Render(ctx, &b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	base := AgentDetailView{Tab: "overview", GatewayURL: "https://mcp.kiff.dev/mcp", Agent: AgentSummary{ID: "onboard-agent"}}

	tool := base
	tool.Agent.Cards = []apiclient.Mandate{toolCardForTest()}
	body := render(context.Background(), tool)
	gw := strings.Index(body, "Connect it to the KIFF gateway")
	guard := strings.Index(body, "Or connect it in code, with kiff-guard")
	if gw < 0 || guard < gw || !strings.Contains(body, `href="/agents/onboard-agent/tools#gateway"`) {
		t.Errorf("tool agent: gateway at %d, guard at %d", gw, guard)
	}

	personal := render(components.WithAccountKind(context.Background(), "personal"), base)
	if !strings.Contains(personal, "Connect it to the KIFF gateway") {
		t.Error("personal account agent does not lead with the gateway")
	}

	company := render(context.Background(), base)
	if strings.Contains(company, "Connect it to the KIFF gateway") || !strings.Contains(company, "Connect it with kiff-guard") {
		t.Error("company agent with no tool Card should lead with kiff-guard")
	}

	noGateway := base
	noGateway.GatewayURL = ""
	if strings.Contains(render(components.WithAccountKind(context.Background(), "personal"), noGateway), "Connect it to the KIFF gateway") {
		t.Error("a deployment without a gateway offers it")
	}
}
