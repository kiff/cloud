package pages

import (
	"fmt"
	"strings"

	studio "github.com/kiff/kiff-studio"
)

// Build handoff constants. Kept here so the one-instruction prompt, the
// manual fallback steps, and any copy stay in lockstep — change the CLI
// version or skill URL in one place.
const (
	studioSkillURL   = "https://kiff.dev/skills/kiff-domains.md"
	studioCLIInstall = "go install github.com/kiff/kiff/cmd/kiff@v0.7.0"
	studioActivateAt = "https://app.kiff.dev/dashboard/build"
)

// studioAssistantPrompt builds the single instruction a user hands to
// their AI coding agent (Claude Code, Cursor, Codex, …). It packages
// whatever the user picked — the derived domain/entity/action and the
// observed parameters — into one self-contained task so the browser
// steps collapse to: pick an action → copy this → paste into your agent.
// The prompt is grounded in the draft; it invents no policy, and it
// tells the agent to ask the human for the business decisions KIFF
// cannot infer (thresholds, who approves).
//
// It mirrors the homepage promise ("give one instruction to your AI
// coding agent") so the journey from kiff.dev into the product reads as
// one continuous motion.
func studioAssistantPrompt(v StudioView) string {
	bp := v.Draft.Blueprint
	domain := firstNonBlank(bp.Domain, "your-domain")
	entity := firstNonBlank(bp.Entity, "your entity")

	var b strings.Builder
	if v.EditDomain != "" {
		fmt.Fprintf(&b, "Evolve my KIFF domain %q in this codebase.\n\n", v.EditDomain)
	} else {
		fmt.Fprintf(&b, "Set up KIFF governance in this codebase for the %q domain (entity: %s).\n\n", domain, entity)
	}

	b.WriteString("Do the whole thing as one task:\n")
	fmt.Fprintf(&b, "1. Install the kiff-domains skill if you don't already have it, then follow it — it's the authority on modelling a KIFF domain. Skill: %s\n", studioSkillURL)
	fmt.Fprintf(&b, "2. Install the CLI: %s\n", studioCLIInstall)
	b.WriteString("3. Scaffold (or locate) the domain package in this repo, then complete kiff.yaml and the executor bodies:\n")

	if len(bp.Actions) == 0 {
		b.WriteString("   - model the entity, its states, the lifecycle transitions, and the actions to govern.\n")
	} else {
		b.WriteString("   - govern these observed actions:\n")
		for _, a := range bp.Actions {
			b.WriteString(studioPromptActionLine(a))
		}
		b.WriteString("   - model the states and transitions, set the allowed states per action, and mark which actions need approval.\n")
	}
	b.WriteString("4. Ask me only for the business decisions you can't infer from the code — thresholds, risk bands, and who approves. Don't invent them.\n")
	b.WriteString("5. Confirm it's sound: kiff verify\n")
	fmt.Fprintf(&b, "6. When it passes, print the finished kiff.yaml so I can paste it at %s to validate and activate in observe.\n\n", studioActivateAt)

	b.WriteString("Keep the codebase the source of truth — KIFF governs the contract, it doesn't replace your code.")
	return b.String()
}

// studioPromptActionLine renders one action as a grounded bullet in the
// prompt: the action name plus the observed required parameters and the
// risk starting-guess when present. Nothing is fabricated.
func studioPromptActionLine(a studio.Action) string {
	line := "     · " + a.Name
	if len(a.RequiredParameters) > 0 {
		line += " (params: " + strings.Join(a.RequiredParameters, ", ") + ")"
	}
	if a.Risk != "" {
		line += " — risk guess: " + a.Risk
	}
	return line + "\n"
}

// firstNonBlank returns the first non-empty, trimmed string.
func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
