package pages

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/gatewayclient"
)

// ToolDetailView is one connected tool on its own page: an Overview with
// the Cards on it, and Settings as rows that each open one sheet, like a
// Card's or an agent's.
type ToolDetailView struct {
	Email, Subject, DisplayName string
	Tool                        gatewayclient.Connection
	// Tab is "overview" or "settings".
	Tab        string
	Cards      []apiclient.Mandate
	CardsError string
	Flash      *DashboardFlash
}

// ToolSheetView is one change to a connected tool on its own full screen.
type ToolSheetView struct {
	Kind  string
	Tool  gatewayclient.Connection
	Flash *DashboardFlash
	// Scoped are the Cards limited to values of the tool's current
	// resource argument: changing it leaves them naming values no call
	// carries any more.
	Scoped []apiclient.Mandate
}

func (v ToolSheetView) back() string { return toolHref(v.Tool.Name) + "/settings" }

// ToolCards are the tool Cards in force for the named tool.
func ToolCards(cards []apiclient.Mandate, tool string, now time.Time) []apiclient.Mandate {
	var out []apiclient.Mandate
	for _, m := range cards {
		if IsToolCard(m) && toolCardTool(m) == tool && m.RevokedAt == nil && (m.ValidUntil == nil || m.ValidUntil.After(now)) {
			out = append(out, m)
		}
	}
	return out
}

// ScopedCards are the Cards whose scope names the tool's resource argument.
func ScopedCards(cards []apiclient.Mandate, t gatewayclient.Connection) []apiclient.Mandate {
	var out []apiclient.Mandate
	for _, m := range cards {
		if r := m.Grants[0].Resources; r != nil && t.ResourceArg != "" && r.Parameter == t.ResourceArg {
			out = append(out, m)
		}
	}
	return out
}

func toolSettingsGroups(t gatewayclient.Connection) []settingsGroup {
	base := toolHref(t.Name) + "/settings/"
	amount := "None"
	if t.AmountArg != "" {
		amount = t.AmountArg
		if t.AmountPath != "" {
			amount = t.AmountPath
		}
		if u := amountUnitLabel(t.AmountUnit, t.AmountScale); u != "" {
			amount += " · " + u
		}
	}
	resource := settingsRow{Label: "Each call is on", Value: "Nothing in particular", Href: base + "resource", Icon: iconFilter}
	if t.ResourceArg != "" {
		resource.Value, resource.Mono = t.ResourceArg, true
	}
	if t.ResourcePath != "" {
		// Set when connecting; changing it means connecting again.
		resource.Value, resource.Mono, resource.Href = t.ResourcePath, true, ""
	}
	release := settingsRow{Label: "If a call did nothing", Value: "Counted", Href: base + "release", Icon: iconPulse}
	if t.OutcomeContract != "" {
		release.Value = "Room back"
	}
	if t.IdempotencyArg == "" {
		release.Value, release.Href = "Needs a same-call key", ""
	}
	checks := settingsGroup{Title: "What KIFF checks", Rows: []settingsRow{
		{Label: "Amount", Value: amount, Icon: iconCoin, Mono: t.AmountArg != ""}, resource, release}}
	if fixed := fixedArgsText(t.FixedArgs); fixed != "" {
		checks.Rows = append(checks.Rows, settingsRow{Label: "Always sends", Value: fixed, Icon: iconTerminal, Mono: true})
	}
	conn := settingsGroup{Title: "Connection", Rows: []settingsRow{
		{Label: "Who holds the key", Value: custodyShort(t), Href: base + "key", Icon: iconKey},
	}}
	if t.Mode != "verify" {
		conn.Rows = append(conn.Rows, settingsRow{Label: "Credential", Value: credentialValue(t), Href: base + "credential", Icon: iconShield})
	}
	same := "None"
	if t.IdempotencyArg != "" {
		same = t.IdempotencyArg
	}
	readOnly := "No"
	if t.ReadOnly {
		readOnly = "Yes"
	}
	conn.Rows = append(conn.Rows,
		settingsRow{Label: "Address", Value: shortHost(t.Host()), Icon: iconLink, Mono: true},
		settingsRow{Label: "Same-call key", Value: same, Icon: iconList, Mono: t.IdempotencyArg != ""},
		settingsRow{Label: "Only reads", Value: readOnly, Icon: iconTool})
	return []settingsGroup{checks, conn}
}

func custodyShort(t gatewayclient.Connection) string {
	switch t.Mode {
	case "verify":
		return "Nobody at KIFF"
	case "relay":
		return "Your relay"
	}
	return "KIFF, encrypted"
}

func credentialValue(t gatewayclient.Connection) string {
	if t.HasCredential {
		return "Stored"
	}
	return "None"
}

// toolTabs are the tool page's tabs.
var toolTabs = []struct{ ID, Label, Suffix string }{{"overview", "Overview", ""}, {"settings", "Settings", "/settings"}}

func scopedCardNames(ms []apiclient.Mandate) string {
	names := make([]string, 0, len(ms))
	for _, m := range ms {
		names = append(names, cardTitle(m))
	}
	return strings.Join(names, ", ")
}

// shortHost keeps a long host to one line in a row: its start and its
// domain, such as "tfjuy66v…on.aws".
func shortHost(h string) string {
	if len(h) <= 28 {
		return h
	}
	parts := strings.Split(h, ".")
	tail := strings.Join(parts[max(0, len(parts)-2):], ".")
	if len(tail) > 18 {
		tail = tail[len(tail)-18:]
	}
	return h[:min(10, len(h))] + "…" + tail
}

// fixedArgsText shows a tool's fixed arguments on one line:
// stripe_api_operation_id = PostRefunds. Text values show without quotes.
func fixedArgsText(raw json.RawMessage) string {
	var m map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil || len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := string(m[k])
		var s string
		if json.Unmarshal(m[k], &s) == nil {
			v = s
		}
		parts = append(parts, k+" = "+v)
	}
	return strings.Join(parts, ", ")
}
