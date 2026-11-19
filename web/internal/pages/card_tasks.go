package pages

import (
	"net/url"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// CardTask is a job a customer recognises, mapped onto the starter
// domain and action that carry it. Issuing a card for a task never asks
// the customer for the contract underneath: the domain comes from the
// cloud's template of the same name, created on first use.
type CardTask struct {
	Slug     string // form value and card-name suffix
	Label    string
	Blurb    string
	Template string // cloud starter template, also the domain's name
	Action   string
	// Money tasks are capped in euros per day (a sum of amount_cents);
	// the rest are capped by how many times per day.
	Money    bool
	CountOf  string // what a count limit counts, e.g. "plan changes"
	Examples string // placeholder for the limit input

	// What an integration sends for this job, and what a one-off test
	// request uses: the entity the action is about, the event that puts
	// a new one into a state the action is allowed in, and the smallest
	// parameters that exercise the card.
	EntityType string
	EntityHint string // how the prompt describes the entity id, e.g. "the order id"
	SetupEvent string
	TestParams map[string]any
	TestDraw   string // what one test request draws, in words
	Role       string // a role the template grants this action
}

// CardTasks are the tasks the simple issue form offers, in the order it
// shows them.
var CardTasks = []CardTask{
	{
		Slug: "refunds", Label: "Refund orders",
		Blurb:    "Refund paid orders, up to an amount per day.",
		Template: "card-refund", Action: "REFUND_ORDER", Money: true, Examples: "1,000",
		EntityType: "Order", EntityHint: "the order id", SetupEvent: "ORDER_PAID",
		TestParams: map[string]any{"amount_cents": 100}, TestDraw: "€1", Role: "support",
	},
	{
		Slug: "store-credit", Label: "Issue store credit",
		Blurb:    "Offer store credit to keep an eligible customer, up to an amount per day.",
		Template: "retention-credit", Action: "ISSUE_STORE_CREDIT", Money: true, Examples: "500",
		EntityType: "RetentionCase", EntityHint: "the retention case id", SetupEvent: "CASE_MARKED_ELIGIBLE",
		TestParams: map[string]any{"amount_cents": 100}, TestDraw: "€1", Role: "support",
	},
	{
		Slug: "plan-changes", Label: "Change subscription plans",
		Blurb:    "Move an active subscription to another plan, up to a number per day.",
		Template: "subscription-plan-change", Action: "CHANGE_PLAN", CountOf: "plan changes", Examples: "40",
		EntityType: "Subscription", EntityHint: "the subscription id", SetupEvent: "SUBSCRIPTION_ACTIVATED",
		TestParams: map[string]any{"target_plan": "kiff-test-plan"}, TestDraw: "one plan change", Role: "support",
	},
}

// CardTaskBySlug returns the task with that slug.
func CardTaskBySlug(slug string) (CardTask, bool) {
	for _, t := range CardTasks {
		if t.Slug == slug {
			return t, true
		}
	}
	return CardTask{}, false
}

// CardTaskFor finds the task a card was issued for: a card on a task's
// domain that grants the task's action. Cards made any other way (the
// full form, the API) have no task, and the page offers only what does
// not depend on one.
func CardTaskFor(m apiclient.Mandate) (CardTask, bool) {
	for _, t := range CardTasks {
		if m.Domain != t.Template {
			continue
		}
		for _, g := range m.Grants {
			if g.Action == t.Action {
				return t, true
			}
		}
	}
	return CardTask{}, false
}

// Quantity is the card aggregate this task's limit is expressed in.
func (t CardTask) Quantity() string {
	if t.Money {
		return "sum(amount_cents)"
	}
	return "count"
}

// CardQuickForm is what the simple form submitted, carried back on a
// refusal so nothing has to be retyped.
type CardQuickForm struct {
	Task    string
	Subject string
	Limit   string
}

// CardQuickView renders the task-first issue form.
type CardQuickView struct {
	Email       string
	Subject     string
	DisplayName string
	Flash       *DashboardFlash
	Me          *apiclient.Me
	Form        CardQuickForm
	Rejected    string
}

// AdvancedHref opens the full form, keeping the agent if one was named.
func (v CardQuickView) AdvancedHref() string {
	if v.Form.Subject == "" {
		return "/cards/new?advanced=1"
	}
	return "/cards/new?advanced=1&subject=" + url.QueryEscape(v.Form.Subject)
}

// TaskChecked reports whether a task is the selected one; the first
// task is selected when nothing was submitted yet.
func (v CardQuickView) TaskChecked(slug string) bool {
	if v.Form.Task == "" {
		return slug == CardTasks[0].Slug
	}
	return v.Form.Task == slug
}

// CardStartHref opens the quick issue form with a job, an agent and a
// daily limit already filled in, so an example on the site or in the
// /delegate walkthrough becomes a real Card in one step. Signed-out
// visitors go through sign-in and arrive with the same form.
func CardStartHref(task, subject, limit string) string {
	q := url.Values{}
	if task != "" {
		q.Set("task", task)
	}
	if subject != "" {
		q.Set("subject", subject)
	}
	if limit != "" {
		q.Set("limit", limit)
	}
	if len(q) == 0 {
		return "/cards/new"
	}
	return "/cards/new?" + q.Encode()
}

// PrefillLimit keeps a prefilled limit only if it is a whole number of
// at most nine digits, as the form would accept; anything else leaves
// the field empty rather than putting arbitrary text in it.
func PrefillLimit(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 9 {
		return ""
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}
