package pages

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/kiff/cloud/web/internal/delegate"
)

// DelegateView is everything /delegate renders.
type DelegateView struct {
	Session delegate.Session
}

func dgRoleClass(r delegate.Role) string {
	if r == delegate.RoleText {
		return ""
	}
	return "dg-r-" + string(r)
}

// dgUseHref loads a gallery Card into the page.
func dgUseHref(id string) string {
	return "/delegate?card=" + url.QueryEscape(id) + "#write"
}

func dgMeterStyle(s delegate.Session) string {
	return "width:" + strconv.Itoa(delegate.Pct(s.Remaining(), s.Card.Ceiling)) + "%"
}

func dgHolderName(s delegate.Session) string {
	if a, ok := s.HolderAgent(); ok {
		return a.Name
	}
	return ""
}

func dgChangeKind(s delegate.Session) string {
	if s.Last == nil {
		return ""
	}
	return s.Last.Kind
}

// dgLineHeld marks the Card line an action fell outside of, and
// dgLineChanged the line the owner just changed, so the reader is pointed
// at it instead of scanning for the difference.
func dgLineHeld(s delegate.Session, r delegate.Role) bool {
	return s.Last != nil && s.Last.Kind == "held" && s.Last.Role == r
}

func dgLineChanged(s delegate.Session, r delegate.Role) bool {
	if s.Last == nil || s.Last.Kind != "changed" {
		return false
	}
	for _, c := range s.Last.Changed {
		if c == r {
			return true
		}
	}
	return false
}

func dgWaitingWhat(s delegate.Session) string {
	h, _ := s.Waiting()
	return h.Sample.What
}

func dgWaitingAmount(s delegate.Session) string {
	h, _ := s.Waiting()
	return s.Card.Format(h.Sample.Amount)
}

func dgWaitingAllows(s delegate.Session) string {
	h, _ := s.Waiting()
	return "Your Card allows " + s.Allows(h.Role) + "."
}

func dgWaitingOver(s delegate.Session) string {
	h, _ := s.Waiting()
	return s.Card.Format(h.Over) + " outside your delegation."
}

func dgWaitingSource(s delegate.Session) string {
	h, _ := s.Waiting()
	return s.Card.SourceOf(h.Sample)
}

// dgChangeLabel is the Change Card button: what the Card would become.
func dgChangeLabel(s delegate.Session) string {
	h, _ := s.Waiting()
	per, ceil, changed := s.ChangeTo(h)
	var parts []string
	for _, r := range changed {
		if r == delegate.RolePerAction {
			parts = append(parts, "up to "+s.Card.Format(per)+" per action")
		} else {
			parts = append(parts, s.Card.Format(ceil)+" "+s.Card.Window)
		}
	}
	return "Change Card: " + strings.Join(parts, " and ")
}

func dgWaitingCount(s delegate.Session) string {
	if n := len(s.Pending); n > 1 {
		return "Needs you · " + strconv.Itoa(n) + " waiting"
	}
	return "Needs you"
}

func dgSampleSource(s delegate.Session) string {
	if n, ok := s.NextSample(); ok {
		return s.Card.SourceOf(n)
	}
	return ""
}

func dgDeltaLabel(s delegate.Session) string {
	if s.Last == nil || s.Last.Kind != "drew" {
		return ""
	}
	return "−" + s.Card.Format(s.Last.Delta)
}

func dgEntryLeft(s delegate.Session, e delegate.Entry) string {
	return s.Card.Format(e.Left) + " left"
}

func dgEntryDelta(s delegate.Session, e delegate.Entry) string {
	switch e.Outcome {
	case delegate.Allowed:
		return "−" + s.Card.Format(e.Delta)
	case delegate.Held:
		return "needs you"
	case delegate.Once:
		return "approved once"
	case delegate.Rejected:
		return "rejected"
	case delegate.Resolved:
		return "done after change"
	}
	return ""
}

// dgIsNewest marks every row the last step added (an auto-run adds
// several).
func dgIsNewest(s delegate.Session, e delegate.Entry) bool {
	return s.Last != nil && e.Seq > len(s.Entries)-s.Last.Rows
}

func dgSampleAmount(s delegate.Session) string {
	if n, ok := s.NextSample(); ok {
		return s.Card.Format(n.Amount)
	}
	return ""
}

func dgSampleWhat(s delegate.Session) string {
	if n, ok := s.NextSample(); ok {
		return n.What
	}
	return ""
}

func dgRevision(s delegate.Session) string { return "rev " + strconv.Itoa(s.Revision) }

func dgAudienceLabel(a delegate.Audience) string {
	if a == delegate.Personal {
		return "Personal"
	}
	return "Business"
}

// dgTab is the phone tab to open after the last step: where the change
// is, so the reader sees it without looking for it.
func dgTab(s delegate.Session) string {
	if _, waiting := s.Waiting(); waiting {
		return "queue"
	}
	if s.Last == nil || s.Holder == "" {
		return "queue"
	}
	switch s.Last.Kind {
	case "drew", "auto", "changed", "moved", "revoked", "assigned":
		return "card"
	}
	return "log"
}

func dgSuggested(s delegate.Session) (delegate.Agent, bool) {
	for _, v := range delegate.Match(s.Card, s.Agents) {
		if v.Suggested {
			return v.Agent, true
		}
	}
	return delegate.Agent{}, false
}

func dgQueueHint(s delegate.Session) string {
	switch {
	case len(s.Pending) > 0:
		return "needs you"
	case s.Holder == "" && !s.Revoked:
		return "pick an agent"
	case s.CanRun():
		return "working"
	}
	return "clear"
}

func dgItoa(n int) string { return strconv.Itoa(n) }

func dgLines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return strconv.Itoa(n) + " lines"
}

// dgRealHref turns the walkthrough's Card into a real one: the issue
// form opens with the matching job, the sample agent's name and a
// daily limit filled in. Cards with no standard job open the form.
func dgRealHref(id string) string {
	switch id {
	case "eu-refunds":
		return CardStartHref("refunds", "support-agent", "5000")
	case "plan-changes":
		return CardStartHref("plan-changes", "billing-agent", "40")
	}
	return CardStartHref("", "", "")
}

func dgRealNote(id string) string {
	switch id {
	case "eu-refunds", "plan-changes":
		return "The form opens with this job and a daily limit filled in. You can change both before issuing it."
	}
	return "Pick the job and the limit for your own agent."
}
