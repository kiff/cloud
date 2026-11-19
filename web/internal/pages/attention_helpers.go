package pages

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// attentionReasons are the codes on a blocked decision. The feed carries
// them as the decision's JSON array (`["mandate_limit_reached"]`); a
// reason that is not an array is kept as one code.
func attentionReasons(reason string) []string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil
	}
	var codes []string
	if json.Unmarshal([]byte(reason), &codes) == nil {
		return codes
	}
	return []string{reason}
}

// reasonSentence says why an action was refused, for someone who did not
// write the code. An unknown code is shown as words rather than hidden.
func reasonSentence(code string) string {
	switch code {
	case "mandate_limit_reached":
		return "The Card's limit for this window is used up."
	case "mandate_per_action_exceeded":
		return "The action is larger than the Card allows for one action."
	case "mandate_resource_not_on_card":
		return "The action is on something the Card does not name."
	case "mandate_not_covered", "no_mandate_issued":
		return "No Card covers this action for this agent."
	case "mandate_expired":
		return "The Card that covered this action has expired."
	case "mandate_proposal_reused":
		return "The agent reused a request id for a different action."
	case "mandate_owner_rejected":
		return "You rejected it on Needs you."
	case "mandate_owner_expired":
		return "Nobody answered before the hold expired."
	case "mandate_unavailable":
		return "The Card check couldn't be answered, so KIFF refused it."
	case "invalid_parameter":
		return "A parameter the Card counts was not a whole number."
	case "key_not_bound_to_agent", "actor_not_key_holder":
		return "The agent used a key that is not issued to it."
	}
	return sentenceCase(strings.ReplaceAll(code, "_", " ")) + "."
}

// attentionWhy is every reason as one sentence each, or a fallback when
// the decision carried none.
func attentionWhy(item apiclient.AttentionItem) []string {
	if item.Kind == "card_warning" {
		return []string{item.Reason}
	}
	if item.Kind == "approval" {
		if r := strings.TrimSpace(item.Reason); r != "" && !strings.HasPrefix(r, "[") {
			return []string{r}
		}
		return []string{"A rule requires a person to approve it."}
	}
	var out []string
	for _, c := range attentionReasons(item.Reason) {
		out = append(out, reasonSentence(c))
	}
	if len(out) == 0 {
		return []string{"The decision recorded no reason."}
	}
	return out
}

// attentionCardsHref leads a card refusal to the agent's cards, where the
// limit can be read or raised; "" for refusals a card did not make.
func attentionCardsHref(item apiclient.AttentionItem) string {
	if item.Kind == "card_warning" {
		return "/cards/" + url.PathEscape(item.SourceID) + "/statement"
	}
	if item.ActorID == "" {
		return ""
	}
	for _, c := range attentionReasons(item.Reason) {
		if strings.HasPrefix(c, "mandate_") || c == "no_mandate_issued" {
			return "/cards?holder=" + url.QueryEscape(item.ActorID)
		}
	}
	return ""
}

// attentionKindLabel is the row's pill.
func attentionKindLabel(item apiclient.AttentionItem) string {
	if item.Kind == "card_warning" {
		return "Budget warning"
	}
	if item.Kind == "approval" {
		return "Waiting for approval"
	}
	return "Blocked"
}

// attentionWhen is when the work was opened: "5 Oct 18:05", with the year
// when it is not this one.
func attentionWhen(t, now time.Time) string {
	t = t.UTC()
	if t.Year() != now.UTC().Year() {
		return t.Format("2 Jan 2006 15:04")
	}
	return t.Format("2 Jan 15:04")
}
