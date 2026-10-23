package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/gateway/internal/kiffapi"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

// CardTool is the gateway's own tool: the agent reads its Card, with what
// each limit has used and has left. It is answered by the gateway,
// never forwarded, recorded or decided, so it draws nothing.
const CardTool = "kiff_card"

// ReservedToolName reports whether a connected tool may not use name
// because the gateway lists a tool of its own under it.
func ReservedToolName(name string) bool {
	return strings.EqualFold(name, CardTool) || strings.EqualFold(name, PendingTool)
}

const cardToolDescription = "Shows the KIFF Card this agent acts under: which tools it covers, the most one call may carry, " +
	"and how much of each total is used and left right now. Reading it changes nothing. " +
	"The numbers are as of this read; every call is still checked when it is made."

func cardToolListing() mcp.Tool {
	title := "Show my KIFF Card"
	return mcp.Tool{Name: CardTool, Title: title, Description: cardToolDescription,
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Annotations: &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true}}
}

// cardResult answers kiff_card.
func (a *agent) cardResult(ctx context.Context) (json.RawMessage, error) {
	card, err := a.g.API.AgentCard(ctx, a.key, a.trial)
	if err != nil {
		return mcp.TextResult("KIFF could not read your Card just now. Calls are still checked when they are made; try again shortly.", true), nil
	}
	tools, err := a.g.Catalog.Tools(ctx, a.me.TenantID)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	if len(card.Cards) == 0 {
		b.WriteString("You act as agent " + card.AgentID + ". No Card of yours applies here, so calls to connected tools are refused until an admin issues one.")
	} else {
		b.WriteString("You act as agent " + card.AgentID + ".")
		for _, t := range tools {
			if ReservedToolName(t.Name) {
				continue
			}
			b.WriteString("\n- " + t.Name + ": " + cardLine(card, t, true))
		}
	}
	b.WriteString("\nThese numbers are as of now; every call is still checked when it is made.")
	res := mcp.TextResult(b.String(), false)
	// The Card itself, for programs.
	var m map[string]any
	if json.Unmarshal(res, &m) == nil {
		m["structuredContent"] = card
		res, _ = json.Marshal(m)
	}
	return res, nil
}

// withCardLine adds what the agent's Card allows to a tool's description:
// its limits, never what is used or left. The tool list is the start of an
// agent's prompt, cached between its turns and runs; a number that moved
// with every call made each run read the whole prompt again (kiff/tafs#138,
// item 58). kiff_card answers what is left, as of the read.
func withCardLine(desc string, card kiffapi.AgentCard, t Tool) string {
	line := "Your KIFF Card: " + cardLine(card, t, false)
	if desc == "" {
		return line
	}
	return desc + "\n\n" + line
}

// cardLine says what the agent's Cards allow for one tool, in one
// sentence or a few. live adds what each total has left and its runway,
// as of now; without it the line changes only when a Card does.
func cardLine(card kiffapi.AgentCard, t Tool, live bool) string {
	var parts []string
	totals := false
	for _, c := range card.Cards {
		if !covers(c, t.Name) {
			continue
		}
		var limits []string
		for _, g := range c.Grants {
			if !strings.EqualFold(g.Action, t.Name) {
				continue
			}
			if r := g.Resources; r != nil {
				limits = append(limits, "only for "+r.Parameter+" "+resourceList(r.Values))
			}
			for _, p := range g.PerActionMax {
				// Matched as the decision matches it, ignoring case.
				if arg, ok := sumOf(p.Quantity); ok && strings.EqualFold(arg, t.AmountArg) {
					limits = append(limits, fmt.Sprintf("up to %s per call", amountText(p.Max, t)))
				}
			}
		}
		for _, l := range c.Limits {
			// A count is of calls to every tool the Card covers, not
			// this tool's own budget.
			num, unit, shared := func(n int64) string { return strconv.FormatInt(n, 10) }, "calls", ", shared by every tool this Card covers"
			if arg, ok := sumOf(l.Quantity); ok {
				if !strings.EqualFold(arg, t.AmountArg) {
					continue // a total this tool's calls do not add to
				}
				// A total is the Card's, shared by every tool adding to it,
				// so it reads in the unit apps/api resolved for the Card,
				// and bare when that is ambiguous (no l.Unit).
				num, unit, shared = func(n int64) string { return strconv.FormatInt(n, 10) }, t.AmountArg, ""
				if l.Unit != "" {
					scale := l.Scale
					num, unit = func(n int64) string { return formatScaled(n, scale) }, l.Unit
				}
			}
			if l.Measure != "" {
				shared += ", measures " + l.Measure
			}
			if l.ExtraRoom > 0 {
				shared += fmt.Sprintf(", base %s plus %s extra room; fixed expiry is listed on the Card", num(l.BaseLimit), num(l.ExtraRoom))
			}
			totals = true
			if !live {
				limits = append(limits, fmt.Sprintf("a total of %s %s %s%s", num(l.Limit), unit, windowText(l.Window), shared))
				continue
			}
			if l.Status != "ok" {
				limits = append(limits, fmt.Sprintf("a total of %s %s %s%s (what is used could not be read)", num(l.Limit), unit, windowText(l.Window), shared))
				continue
			}
			limits = append(limits, fmt.Sprintf("%s of %s %s left %s%s", num(l.Remaining), num(l.Limit), unit, windowText(l.Window), shared))
			if f := l.Forecast; f != nil {
				switch f.State {
				case "estimated":
					if f.RunsOutAt != nil {
						limits = append(limits, "estimated to run out at "+f.RunsOutAt.Format(time.RFC3339)+" at the recent usage rate")
					}
				case "insufficient_history":
					limits = append(limits, "too little history to estimate runway")
				case "unavailable":
					limits = append(limits, "runway could not be read")
				case "spent":
					limits = append(limits, "this total is spent")
				case "no_projected_exhaustion":
					limits = append(limits, "no exhaustion projected within this window at the recent usage rate")
				}
				if f.ResetsAt != nil {
					limits = append(limits, "resets at "+f.ResetsAt.Format(time.RFC3339))
				}
			}
		}
		s := "Card " + c.ID + " covers this tool"
		if len(limits) > 0 {
			s += ": " + strings.Join(limits, "; ")
		}
		if c.OnExceed == "owner" {
			s += ". A per-action breach can be held for review. Approval still counts fully; a cumulative ceiling needs an explicit Card revision."
		} else {
			s += ". A call over it is refused."
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return "no Card of yours covers this tool, so calls to it are refused."
	}
	if totals && !live {
		parts = append(parts, "What is used and left of each total: "+CardTool+".")
	}
	return strings.Join(parts, " ")
}

// amountText writes one call's amount of t's amount argument (a per-call
// maximum): in the unit its owner declared (8000 with EUR, scale 2:
// "80.00 EUR"), else as the whole number with the argument's name.
func amountText(n int64, t Tool) string {
	if t.AmountUnit == "" {
		return fmt.Sprintf("%d %s", n, t.AmountArg)
	}
	return formatScaled(n, t.AmountScale) + " " + t.AmountUnit
}

// formatScaled writes n with scale decimal places: 8000, 2 -> "80.00".
func formatScaled(n int64, scale int) string {
	if scale <= 0 {
		return strconv.FormatInt(n, 10)
	}
	sign := ""
	if n < 0 {
		sign, n = "-", -n
	}
	s := strconv.FormatInt(n, 10)
	for len(s) <= scale {
		s = "0" + s
	}
	return sign + s[:len(s)-scale] + "." + s[len(s)-scale:]
}

func covers(c kiffapi.Card, action string) bool {
	for _, g := range c.Grants {
		if strings.EqualFold(g.Action, action) {
			return true
		}
	}
	return false
}

// sumOf returns the argument of a "sum(<argument>)" quantity.
func sumOf(q string) (string, bool) {
	if strings.HasPrefix(q, "sum(") && strings.HasSuffix(q, ")") {
		return q[len("sum(") : len(q)-1], true
	}
	return "", false
}

func windowText(w string) string {
	switch w {
	case "calendar_day":
		return "today"
	case "rolling_1h":
		return "in the last hour"
	case "rolling_24h":
		return "in the last 24 hours"
	case "lifetime":
		return "for the Card's whole life (it never resets)"
	}
	return "in this window (" + w + ")"
}

// resourceList is a scope's values for the agent: the first few, and how
// many more, so a long scope does not swamp the tool's description.
func resourceList(vs []string) string {
	const shown = 10
	if len(vs) <= shown {
		return strings.Join(vs, ", ")
	}
	return strings.Join(vs[:shown], ", ") + fmt.Sprintf(" and %d more", len(vs)-shown)
}
