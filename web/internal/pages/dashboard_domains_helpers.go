package pages

import (
	"fmt"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// joinOrNone renders a string slice as a comma list, or "none" when empty.
func joinOrNone(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}

// domainStatusLabel humanizes a domain/control status for display.
func domainStatusLabel(status string) string {
	switch status {
	case "", "active":
		return "active"
	default:
		return status
	}
}

// domainContinuitySuffix renders the "continuous since" half of the
// retention story when the domain reports an activation time. This is
// the seed of the compounding-evidence narrative: the longer a domain
// has been live, the more its verifiable record is worth. Empty when
// no activation time is known.
func domainContinuitySuffix(d *apiclient.DomainDetail) string {
	if d == nil || d.ActivatedAt == nil {
		return ""
	}
	return " · governing since " + d.ActivatedAt.Format("Jan 2, 2006")
}

// domainAgentKindLabel labels the identity dimension an observed agent
// was matched on, so the surface doesn't guess.
func domainAgentKindLabel(kind string) string {
	switch kind {
	case "agent":
		return "agent"
	case "api_key":
		return "API key"
	case "actor":
		// A name the caller claimed without a key bound to it: shown,
		// but not as the agent.
		return "claimed name"
	case "subject":
		return "subject"
	default:
		return kind
	}
}

// domainAgentsBasis states, honestly, what the observed-agents list is:
// recently observed proposing identities over the window, not an
// authoritative roster. The api currently samples the most recent
// decisions, so this copy deliberately avoids implying the list is
// complete — completeness lands with the store-side aggregation.
func domainAgentsBasis(s apiclient.DomainAgentsSection) string {
	if s.Window == nil {
		return "Agents seen in recent decisions."
	}
	return fmt.Sprintf(
		"Agents seen in decisions between %s and %s. Some may not show.",
		s.Window.From.Format("Jan 2"), s.Window.To.Format("Jan 2"),
	)
}

// --- per-domain governance posture ---

// postureStateLabel is the pill copy for the domain's current posture.
// Empty posture means the control predates per-domain posture and still
// follows the workspace default — stated honestly rather than implying
// a mode was chosen.
func postureStateLabel(posture string) string {
	switch posture {
	case "enforce":
		return "Enforcing"
	case "observe":
		return "Observe"
	default:
		return "Workspace default"
	}
}

// postureStatePillClass maps the posture to its pill styling hook.
func postureStatePillClass(posture string) string {
	switch posture {
	case "enforce":
		return "dash-posture-enforce"
	case "observe":
		return "dash-posture-observe"
	default:
		return "dash-posture-unset"
	}
}

// posturedButtonLabel is the label for one posture option. When it is
// the active mode the label reads as a state ("Observe · current");
// otherwise it reads as the action ("Switch to enforce").
func posturedButtonLabel(mode string, current bool) string {
	if current {
		switch mode {
		case "enforce":
			return "Enforce · current"
		default:
			return "Observe · current"
		}
	}
	switch mode {
	case "enforce":
		return "Switch to enforce"
	default:
		return "Switch to observe"
	}
}
