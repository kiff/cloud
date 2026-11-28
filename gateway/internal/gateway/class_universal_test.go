package gateway

import (
	"encoding/json"
	"os"
	"testing"
)

// The universal classes (the universal classes contract): on an account on
// KIFF_K4_UNIVERSAL, generic nouns raise, a read still needs two signals,
// and a tool no rule classes is irreversible, said so (ClassByUnknown).
func TestTheUniversalClasses(t *testing.T) {
	u := func(name string, readOnly bool) (Class, string) {
		return ClassifyBy(ClassedTool{Name: name, ReadOnly: readOnly, Universal: true})
	}
	for _, tc := range []struct {
		name     string
		readOnly bool
		want     Class
		by       string
	}{
		// Two signals make a read, whatever the name says; one never does.
		{"list_cards", true, ClassRead, ""},
		{"list_invoices", false, ClassWrite, ""},
		{"show_view", true, ClassRead, ""},
		// readOnlyHint never lowers alone, and never lowers money.
		{"create_payment", true, ClassMoney, ""},
		{"refresh_balance", true, ClassIrreversible, ClassByUnknown},
		// Money nouns.
		{"create_transfer_request", false, ClassMoney, ""},
		{"create_card", false, ClassMoney, ""},
		{"create_sepa_mandate", false, ClassMoney, ""},
		{"update_direct_debit", false, ClassMoney, ""},
		{"add_beneficiary", false, ClassMoney, ""},
		{"create_payment_link", false, ClassMoney, ""},
		// People and access nouns.
		{"create_team", false, ClassIrreversible, ""},
		{"create_membership", false, ClassIrreversible, ""},
		{"update_member_role", false, ClassIrreversible, ""},
		{"add_bank_account", false, ClassIrreversible, ""},
		{"create_api_key", false, ClassIrreversible, ""},
		{"create_webhook", false, ClassIrreversible, ""},
		// A known write verb and nothing else: a write.
		{"create_client", false, ClassWrite, ""},
		{"update_client", false, ClassWrite, ""},
		// Unknown is closed: no annotation, no verb or noun a rule knows.
		{"do_thing", false, ClassIrreversible, ClassByUnknown},
		{"NOTION_DO_THING", false, ClassIrreversible, ClassByUnknown},
		{"post_message", false, ClassIrreversible, ClassByUnknown},
		// Raised by another step: not "unknown", so never carried over.
		{"send_client_invoice", false, ClassIrreversible, ""},
		{"delete_client", false, ClassDelete, ""},
		{"GMAIL_SEND_EMAIL", false, ClassIrreversible, ""},
		{"GMAIL_LIST_THREADS", true, ClassRead, ""},
	} {
		if c, by := u(tc.name, tc.readOnly); c != tc.want || by != tc.by {
			t.Errorf("%s (read-only %v): %s %q, want %s %q", tc.name, tc.readOnly, c, by, tc.want, tc.by)
		}
	}
	// The money mark and the destructive hint are signals of their own.
	if c, by := ClassifyBy(ClassedTool{Name: "do_thing", Money: true, Universal: true}); c != ClassMoney || by != "" {
		t.Errorf("money mark: %s %q", c, by)
	}
	if c, by := ClassifyBy(ClassedTool{Name: "do_thing", Destructive: true, Universal: true}); c != ClassIrreversible || by != "" {
		t.Errorf("destructive hint: %s %q", c, by)
	}
	// A pinned operation that raises is a signal too.
	if c, by := ClassifyBy(ClassedTool{Name: "stripe_api_write", Operation: "PostInvoicesInvoiceVoid", Universal: true}); c != ClassIrreversible || by != "" {
		t.Errorf("pinned void: %s %q", c, by)
	}
	// Off the universal classes, nothing changes: today's rules.
	for name, want := range map[string]Class{"do_thing": ClassWrite, "create_team": ClassWrite, "add_beneficiary": ClassWrite, "show_view": ClassWrite} {
		if c, by := ClassifyBy(ClassedTool{Name: name, ReadOnly: name == "show_view"}); c != want || by != "" {
			t.Errorf("%s off the switch: %s %q, want %s", name, c, by, want)
		}
	}
}

// No tool classes lower under the universal rule than under today's
// rules or Qonto's reviewed table, with the table or without it.
func TestTheUniversalClassesNeverLowerQonto(t *testing.T) {
	raw, err := os.ReadFile("classes/qonto.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Tools []struct {
			Name  string `json:"name"`
			Class Class  `json:"class"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for _, tool := range f.Tools {
		ro := tool.Class == ClassRead
		today := Classify(ClassedTool{App: "qonto", Name: tool.Name, ReadOnly: ro})
		for _, app := range []string{"qonto", ""} {
			got := Classify(ClassedTool{App: app, Name: tool.Name, ReadOnly: ro, Universal: true})
			if classRank[got] < classRank[tool.Class] || classRank[got] < classRank[today] {
				t.Errorf("%s (app %q): universal %s, table %s, today %s", tool.Name, app, got, tool.Class, today)
			}
		}
	}
}

// A call carries ClassByUnknown only when it's irreversible by "unknown"
// alone; the parameters an agent sends never set it.
func TestACallSaysWhenItsClassIsUnknownAlone(t *testing.T) {
	unknown := Tool{Name: "do_thing_q1", UpstreamTool: "do_thing", Universal: true}
	p, err := unknown.params(map[string]any{ClassByParam: "nothing"})
	if err != nil {
		t.Fatal(err)
	}
	if p[ClassParam] != "irreversible" || p[ClassByParam] != ClassByUnknown {
		t.Errorf("unknown: %v", p)
	}
	for _, tool := range []Tool{
		{Name: "send_x", UpstreamTool: "send_client_invoice", Universal: true},
		{Name: "create_client_q1", UpstreamTool: "create_client", Universal: true},
		{Name: "do_thing_q1", UpstreamTool: "do_thing"},
	} {
		p, err := tool.params(map[string]any{ClassByParam: ClassByUnknown})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := p[ClassByParam]; ok {
			t.Errorf("%s: %v", tool.UpstreamTool, p)
		}
	}
	// A reviewed condition that lowers the call (a draft invoice) drops it.
	draft := Tool{Name: "create_client_invoice_q1", UpstreamTool: "create_client_invoice", App: "qonto", Universal: true}
	if c, _, by := draft.CallClassBy(map[string]any{"status": "draft"}); c != ClassWrite || by != "" {
		t.Errorf("a draft invoice: %s %q", c, by)
	}
}

func TestUniversalTenants(t *testing.T) {
	SetUniversalTenants(map[string]bool{"t_a": true, "t_b": false})
	defer SetUniversalTenants(nil)
	if !UniversalFor("t_a") || UniversalFor("t_b") || UniversalFor("t_c") {
		t.Error("the set")
	}
}
