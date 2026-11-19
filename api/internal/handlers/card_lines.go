package handlers

import (
	"encoding/json"
	"strings"

	"github.com/kiff/cloud/api/internal/cardwords"
	"github.com/kiff/cloud/api/internal/words"
)

// Card lines: the words the owner reads before
// confirming a draft, written here so every app shows KIFF's own and none
// builds them. Each is read from a Card as shown, after shapeCard has put
// its units and titles on it.

// shownCard is a shown Card's terms, for its lines.
func shownCard(shown map[string]any) cardwords.Card {
	c := cardwords.Card{OnExceed: jsonStr(shown["on_exceed"])}
	for _, g := range jsonObjects(shown["grants"]) {
		gr := cardwords.Grant{Action: jsonStr(g["action"]), Title: jsonStr(g["title"])}
		for _, p := range jsonObjects(g["per_action_max"]) {
			gr.PerAction = append(gr.PerAction, cardwords.PerAction{Quantity: jsonStr(p["quantity"]), Max: jsonInt(p["max"]), Unit: jsonStr(p["unit"]), Scale: int(jsonInt(p["scale"]))})
		}
		c.Grants = append(c.Grants, gr)
	}
	for _, a := range jsonObjects(shown["aggregates"]) {
		c.Aggregates = append(c.Aggregates, cardwords.Aggregate{Quantity: jsonStr(a["quantity"]), Limit: jsonInt(a["limit"]), Window: jsonStr(a["window"]), Unit: jsonStr(a["unit"]), Scale: int(jsonInt(a["scale"])), Per: jsonStr(a["per"])})
	}
	return c
}

// withLines puts "lines" on each shown Card, and on the terms it revises
// ("current"): two grants that read alike, on one Card or two of the cards
// the owner reads together, each add their action. A Card's terms now read
// alike among themselves only, as the owner reads them on their own. The
// lines are in w's language.
func withLines(cards []map[string]any, w *words.Words) {
	var all []cardwords.Grant
	for _, c := range cards {
		all = append(all, shownCard(c).Grants...)
	}
	alike := cardwords.Alike(all)
	for _, c := range cards {
		// A window without words was refused at submit (draftCards); a Card
		// that has one anyway shows no lines rather than a guessed one.
		if lines, err := cardwords.Lines(shownCard(c), alike, w); err == nil {
			c["lines"] = lines
		}
		if cur, ok := c["current"].(map[string]any); ok {
			withOwnLines(cur, w)
		}
	}
}

// withOwnLines puts "lines" on one Card's terms, read on their own.
func withOwnLines(shown map[string]any, w *words.Words) {
	c := shownCard(shown)
	if lines, err := cardwords.Lines(c, cardwords.Alike(c.Grants), w); err == nil {
		shown["lines"] = lines
	}
}

// cardTitle is "Refunds assistant's Card": the name a draft was submitted
// with, never its agent id, in w's language.
func cardTitle(label string, w *words.Words) string {
	if name := strings.TrimSpace(label); name != "" {
		return w.CardOf(name)
	}
	return w.TafCard
}

func jsonStr(v any) string {
	s, _ := v.(string)
	return s
}

// jsonInt is a JSON number as an int64, 0 when it isn't one.
func jsonInt(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	return 0
}
