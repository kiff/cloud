package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// issueTaskCard issues a card from the task-first form: a job, an agent,
// and a daily limit. The job's domain is created from the cloud's
// starter template the first time it is needed, so a customer can give
// an agent a card without authoring a contract first.
func (h *DashboardHandler) issueTaskCard(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/cards/new", http.StatusSeeOther)
		return
	}
	view := pages.CardQuickView{
		Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName,
		Form: pages.CardQuickForm{
			Task:    strings.TrimSpace(r.PostFormValue("task")),
			Subject: strings.TrimSpace(r.PostFormValue("subject")),
			Limit:   strings.TrimSpace(r.PostFormValue("limit")),
		},
	}
	refuse := func(msg string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		view.Rejected = msg
		_ = pages.CardQuick(view).Render(r.Context(), w)
	}

	task, ok := pages.CardTaskBySlug(view.Form.Task)
	if !ok {
		refuse("Pick the job this card is for.")
		return
	}
	if view.Form.Subject == "" {
		refuse("Name the agent. Use the same name your agent sends when it asks KIFF to act.")
		return
	}
	limit, err := parseTaskLimit(view.Form.Limit, task.Money)
	if err != nil {
		refuse(err.Error())
		return
	}
	if h.api == nil {
		refuse("apps/api is not configured. Set KIFF_API_BASE_URL on the web binary to issue a card.")
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}

	if msg := h.ensureTaskDomain(r, bearer, task); msg != "" {
		refuse(msg)
		return
	}
	cards, err := h.api.ListMandatesBearer(r.Context(), bearer)
	if err != nil {
		h.logger.WarnContext(r.Context(), "task card: list cards failed", slog.String("err", err.Error()))
		refuse("Your existing cards could not be read, so a name for this one could not be chosen safely. Try again in a moment.")
		return
	}
	id, updating := taskCardID(view.Form.Subject, task, cards, time.Now())

	// Updating an existing Card changes its daily limit and nothing else.
	// Reissuing rewrites every term, so the grant is carried over from
	// the Card as it stands: its per-action limits and state narrowing
	// stay (a review: they were silently dropped).
	grants := []apiclient.MandateGrant{{Action: task.Action}}
	if updating {
		grants = existingGrants(cards, id, task.Action)
	}

	measure := ""
	if updating {
		for _, m := range cards {
			if m.ID == id {
				for _, a := range m.Aggregates {
					if a.Quantity == task.Quantity() && a.Window == "calendar_day" {
						measure = a.Measure
					}
				}
			}
		}
	}
	m, err := h.api.IssueMandateBearer(r.Context(), bearer, apiclient.IssueMandateRequest{
		ID:      id,
		Subject: view.Form.Subject,
		Domain:  task.Template,
		Grants:  grants,
		Aggregates: []apiclient.MandateAggregate{
			{Quantity: task.Quantity(), Limit: limit, Window: "calendar_day", Measure: measure},
		},
	})
	if err != nil {
		var rejected *apiclient.MandateRejectedError
		if errors.As(err, &rejected) {
			refuse(rejected.Message)
			return
		}
		h.logger.WarnContext(r.Context(), "task card: issue failed", slog.String("mandate", id), slog.String("err", err.Error()))
		refuse("The card could not be issued: " + err.Error())
		return
	}

	verb := "can now"
	if updating {
		verb = "now has a new limit:"
	}
	_ = h.sessions.IssueFlash(w, session.Flash{
		Kind:    session.FlashMandateIssued,
		Message: m.Subject + " " + verb + " " + strings.ToLower(task.Label) + " up to " + taskLimitLabel(limit, task) + " per day.",
	})
	http.Redirect(w, r, "/cards/"+url.PathEscape(m.ID), http.StatusSeeOther)
}

// ensureTaskDomain makes sure the task's domain exists, creating it from
// the cloud's template when it does not. It returns a sentence for the
// customer when that cannot be done, or "" when the domain is there.
func (h *DashboardHandler) ensureTaskDomain(r *http.Request, bearer string, task pages.CardTask) string {
	domains, err := h.api.ListDomainsBearer(r.Context(), bearer)
	if err != nil {
		h.logger.WarnContext(r.Context(), "task card: list domains failed", slog.String("err", err.Error()))
		return "Your setup could not be read, so the card was not issued. Try again in a moment."
	}
	for _, d := range domains {
		if d.Name == task.Template {
			return ""
		}
	}
	_, err = h.api.CreateDomainFromTemplateBearer(r.Context(), bearer, task.Template)
	switch {
	case err == nil, errors.Is(err, apiclient.ErrDomainExists):
		return ""
	case errors.Is(err, apiclient.ErrDomainLimitReached):
		return "This job needs its own set of rules, and your plan's limit on domains is reached. Remove a domain you no longer use, or upgrade."
	default:
		h.logger.WarnContext(r.Context(), "task card: create domain from template failed",
			slog.String("template", task.Template), slog.String("err", err.Error()))
		return "The rules for this job could not be set up, so the card was not issued. Try again in a moment."
	}
}

// taskCardID names a task card after its agent and job, so one agent
// can hold a card per job. Issuing the same job to the same agent again
// changes that card while it is in force. A card of that name that was
// revoked, has expired or was archived cannot be revived by re-issuing
// it (revocation is permanent), so the next free name is used instead.
func taskCardID(subject string, task pages.CardTask, cards []apiclient.Mandate, now time.Time) (id string, updating bool) {
	base := strings.TrimPrefix(mandateIDFromSubject(subject), "limit-")
	if base == "limit" {
		base = "agent"
	}
	return ownCardID(base+"-"+task.Slug, cards, now, func(m apiclient.Mandate) bool {
		return isCardOf(m, subject, task.Template)
	})
}

// isCardOf reports a Card that issuing for subject on domain may change:
// subject is its first holder, so a reissue keeps a shared Card's
// holders, and it is on the same domain.
func isCardOf(m apiclient.Mandate, subject, domain string) bool {
	return m.Subject == subject && m.Domain == domain
}

// ownCardID is freeCardID over only the Cards mine accepts. Card names
// are slugs, so "refund-agent" and "refund_agent" share one, and
// reissuing another agent's Card under it would move that Card to this
// agent and cancel its held and approved actions. Any Card mine refuses
// is skipped like one no longer in force.
func ownCardID(base string, cards []apiclient.Mandate, now time.Time, mine func(apiclient.Mandate) bool) (id string, updating bool) {
	others := make([]apiclient.Mandate, 0, len(cards))
	for _, m := range cards {
		if !mine(m) {
			m.RevokedAt = &now
		}
		others = append(others, m)
	}
	return freeCardID(base, others, now)
}

// freeCardID is base, or base-2, base-3… when base names a card that is
// no longer in force (revocation is permanent). updating reports that
// base names a card in force, which issuing under it changes.
func freeCardID(base string, cards []apiclient.Mandate, now time.Time) (id string, updating bool) {
	byID := map[string]apiclient.Mandate{}
	for _, m := range cards {
		byID[m.ID] = m
	}
	for n := 1; ; n++ {
		candidate := base
		if n > 1 {
			candidate = base + "-" + strconv.Itoa(n)
		}
		existing, taken := byID[candidate]
		if !taken {
			return candidate, false
		}
		if cardInForce(existing, now) {
			return candidate, true
		}
	}
}

func cardInForce(m apiclient.Mandate, now time.Time) bool {
	return m.RevokedAt == nil && m.ArchivedAt == nil && (m.ValidUntil == nil || now.Before(*m.ValidUntil))
}

// parseTaskLimit reads the daily limit: euros (returned in cents) for a
// money task, a whole count otherwise. It accepts "1,000", "1 000",
// "€1000", "1000.50" and the European "1000,50".
func parseTaskLimit(raw string, money bool) (int64, error) {
	s := strings.NewReplacer("€", "", " ", "", " ", "", "_", "").Replace(strings.TrimSpace(raw))
	if s == "" {
		return 0, errors.New("set how much the agent may do per day.")
	}
	if !money {
		n, err := strconv.ParseInt(strings.ReplaceAll(s, ",", ""), 10, 64)
		if err != nil || n <= 0 {
			return 0, errors.New("the daily limit has to be a whole number above zero, for example 40.")
		}
		return n, nil
	}
	// A single comma followed by one or two digits is a decimal comma;
	// any other comma separates thousands.
	if i := strings.LastIndex(s, ","); i >= 0 && !strings.Contains(s, ".") && strings.Count(s, ",") == 1 && len(s)-i-1 <= 2 {
		s = s[:i] + "." + s[i+1:]
	}
	s = strings.ReplaceAll(s, ",", "")
	whole, frac, _ := strings.Cut(s, ".")
	bad := errors.New("the daily limit has to be an amount in euros above zero, for example 1,000 or 250.50.")
	if whole == "" || len(frac) > 2 || len(whole) > 12 {
		return 0, bad
	}
	for _, r := range whole + frac {
		if r < '0' || r > '9' {
			return 0, bad
		}
	}
	for len(frac) < 2 {
		frac += "0"
	}
	cents, _ := strconv.ParseInt(whole+frac, 10, 64)
	if cents <= 0 {
		return 0, bad
	}
	return cents, nil
}

// taskLimitLabel renders a daily limit the way the customer typed it.
func taskLimitLabel(limit int64, task pages.CardTask) string {
	if !task.Money {
		return strconv.FormatInt(limit, 10) + " " + task.CountOf
	}
	euros := groupThousands(limit / 100)
	if c := limit % 100; c != 0 {
		return "€" + euros + "." + leftPad2(c)
	}
	return "€" + euros
}

func groupThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func leftPad2(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

// existingGrants returns the Card's complete grant list as it stands, so
// a quick update reissues every grant (each state scope and cap)
// unchanged. A Card with no grants, or none found, gets a bare grant for
// the task's action. a review: keeping only the first grant for the
// action dropped a second, tighter one.
func existingGrants(cards []apiclient.Mandate, id, action string) []apiclient.MandateGrant {
	for _, c := range cards {
		if c.ID == id && len(c.Grants) > 0 {
			return append([]apiclient.MandateGrant(nil), c.Grants...)
		}
	}
	return []apiclient.MandateGrant{{Action: action}}
}
