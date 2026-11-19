package pages

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/kiff/cloud/web/internal/gatewayclient"
)

func toolHref(name string) string { return "/tools/" + url.PathEscape(name) }

// custodyLine says who holds the tool's credential.
func custodyLine(t gatewayclient.Connection) string {
	switch t.Mode {
	case "verify":
		return "Nobody at KIFF. The tool checks KIFF's execution permit on each call."
	case "relay":
		return "Your relay, from your vault. KIFF holds only the relay's transport credential."
	}
	if t.HasCredential {
		return "KIFF, stored encrypted."
	}
	return "None stored."
}

func toolsFoundLine(n int) string {
	if n == 1 {
		return "1 tool found"
	}
	return strconv.Itoa(n) + " tools found"
}

// toolName is the gateway's rule for the name agents see.
var toolName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// defaultToolName offers the server's tool name when the gateway would
// accept it, and leaves the field for the owner otherwise.
func defaultToolName(tool string) string {
	if toolName.MatchString(tool) {
		return tool
	}
	return ""
}

// chosenOr keeps what the owner typed for the tool they chose, when a
// save was refused; other tools get their default.
func chosenOr(v ToolsNewView, tool, typed, def string) string {
	if v.Chosen == tool && typed != "" {
		return typed
	}
	return def
}

// ToolCardForm is what the owner chose for a Card on a connected tool.
type ToolCardForm struct {
	Agent                       string
	PerCall, Total, Calls       string
	AmountMeasure, CountMeasure string
	Window, OnExceed, Hold      string
	// NextSteps are the answers besides approve and reject the owner can
	// give to a held call, as typed: "return first, proof first".
	NextSteps string
}

// ToolCardView backs /tools/{name}/card.
type ToolCardView struct {
	Email, Subject, DisplayName string
	Tool                        gatewayclient.Connection
	// Agents are the account's agents, offered for the agent field.
	Agents        []string
	Form          ToolCardForm
	Error         string
	LoadError     string
	NotConfigured bool
	// Step opens a Card being changed on one step.
	Step string
	// Single opens that one step on its own, to change it and save.
	Single bool
	// Editing is the Card being changed, when there is one.
	Editing string
}

var windowLabels = map[string]string{"calendar_day": "per day", "rolling_24h": "per 24 hours", "rolling_1h": "per hour", "rolling_7d": "per 7 days", "lifetime": "in total"}

// ToolCardTerms says what a Card on t allows, in the tool's own units:
// "up to 500 amount_eur per call, 2000 amount_eur and 20 calls per day".
func ToolCardTerms(t gatewayclient.Connection, f ToolCardForm) string {
	var parts []string
	unit := toolAmountUnit(t)
	if t.AmountArg != "" && f.PerCall != "" {
		parts = append(parts, "up to "+f.PerCall+" "+unit+" per call")
	}
	var totals []string
	if t.AmountArg != "" && f.Total != "" {
		totals = append(totals, f.Total+" "+unit)
	}
	if f.Calls != "" {
		totals = append(totals, f.Calls+" calls")
	}
	if len(totals) > 0 {
		parts = append(parts, strings.Join(totals, " and ")+" "+windowLabels[f.Window])
	}
	if f.Total != "" && f.AmountMeasure != "" {
		parts = append(parts, "amount measures "+f.AmountMeasure)
	}
	if f.Calls != "" && f.CountMeasure != "" {
		parts = append(parts, "count measures "+f.CountMeasure)
	}
	return strings.Join(parts, ", ")
}

// toolAmountUnit is what a tool's amount is called beside a number: its
// unit when the owner said ("EUR"), else the argument's own name.
func toolAmountUnit(t gatewayclient.Connection) string {
	if u := amountUnitLabel(t.AmountUnit, t.AmountScale); u != "" {
		return u
	}
	return t.AmountArg
}
