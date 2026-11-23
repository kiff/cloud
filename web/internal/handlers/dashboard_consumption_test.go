package handlers_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

func meters(plan string, tasks, tasksIncluded, packs, actions, actionsIncluded int) *apiclient.MeterUsage {
	m := &apiclient.MeterUsage{Plan: plan}
	m.Period.Start = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	m.Period.End = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	m.Tasks.Counted, m.Tasks.Used, m.Tasks.Included, m.Tasks.Packs = tasks, tasks, tasksIncluded, packs
	left := max(0, tasksIncluded+packs-tasks)
	m.Tasks.Left = &left
	m.Tasks.Ceiling = tasksIncluded + packs
	m.Actions.Counted, m.Actions.Included = actions, actionsIncluded
	return m
}

// The Settings usage card shows the account's tafs tasks against what the
// plan includes and its pack tasks, and KIFF actions against what's
// included, and says what's free and what overage costs.
func TestSettingsUsage_TasksAndActions(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Me.Plan = "solo"
	v.Meters = meters("solo", 15, 20, 0, 1200, 500)

	body := renderSettings(t, v)
	for _, want := range []string{
		"Usage",
		"Oct 1 – Nov 1",
		"15 of 20 tasks",
		"5 tasks left this period",
		"1,200 of 500 actions",
		"75% of tasks",
		"tafs use pack tasks, and stop when none are left",
		"Refused, held then refused, failed, halted, reads, trials and retries are free",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("usage card missing %q", want)
		}
	}
	for _, retired := range []string{"governed operations", "Your budget", "open beta", "ceiling", "€0.39"} {
		if strings.Contains(body, retired) {
			t.Errorf("usage card still says %q", retired)
		}
	}
}

// On the trial: its days left, its caps, and the way to start it when it
// hasn't started.
func TestSettingsUsage_Trial(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	end := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	v.Meters = meters("trial", 3, 25, 0, 40, 500)
	v.Meters.Trial = &struct {
		EndsAt   *time.Time `json:"ends_at"`
		DaysLeft int        `json:"days_left"`
	}{EndsAt: &end, DaysLeft: 3}
	body := renderSettings(t, v)
	for _, want := range []string{"trial: 3 days left", "3 of 25 tasks", "The trial includes 25 tasks and 500 actions", "Choose Company"} {
		if !strings.Contains(body, want) {
			t.Errorf("trial card missing %q", want)
		}
	}

	v.Meters.Trial.EndsAt = nil
	if body := renderSettings(t, v); !strings.Contains(body, "trial not started") || !strings.Contains(body, "Choose Company") || strings.Contains(body, "7-day trial") {
		t.Errorf("an account with no plan should be offered Company, with no trial")
	}
}

// Without the meters, the card says so instead of showing zeros.
func TestSettingsUsage_Unavailable(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Meters = nil
	body := renderSettings(t, v)
	if !strings.Contains(body, "<strong>—</strong>") {
		t.Errorf("a missing meter should read as a dash")
	}
}

// Packs: the tasks a workspace has from its packs, the packs on sale to
// someone who may add tasks, and the members' requests to approve or
// decline. Never a price: Stripe's checkout shows it.
func TestSettingsPacks(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Me.Plan = "team"
	v.Meters = meters("team", 50, 40, 70, 10, 1000)
	v.Meters.Billing.Source = "stripe"
	v.Meters.PackList = []apiclient.UsagePack{{Name: "Rayo", Tasks: 70, TasksLeft: 60, ExpiresAt: time.Date(2027, 10, 1, 0, 0, 0, 0, time.UTC)}}
	v.Meters.CanAddTasks, v.Meters.CanAddPacks = true, true
	v.Meters.Store = &struct {
		Packs []apiclient.StorePack `json:"packs"`
	}{Packs: []apiclient.StorePack{{ID: "pico", Name: "Pico", Line: "Pico · 12 tasks", Tasks: 12, Months: 12, StripePrice: "kiff_pack_pico"},
		{ID: "rayo", Name: "Rayo", Line: "Rayo · 70 tasks", Tasks: 70, Months: 12, StripePrice: "kiff_pack_rayo"},
		// A pack of credits is sold in the app only: no Stripe price.
		{ID: "c-test", Name: "Test", Line: "Test · 500 credits"}}}
	v.Meters.Requests = []apiclient.PackRequest{{ID: "pr_1", Pack: "rayo", Reason: "Refunds ran out", Status: "open"},
		{ID: "pr_2", Pack: "pico", Status: "declined"}}
	body := renderSettings(t, v)
	for _, want := range []string{
		"Billed by KIFF.",
		"50 of 110 tasks",
		"60 tasks left this period",
		"40 included · 70 from packs",
		"Rayo · 60 of 70 tasks left, until Oct 1, 2027",
		"Add tasks",
		"Pico · 12 tasks. They last 12 months.",
		`href="/dashboard/checkout?pack=pico"`,
		"Refunds ran out",
		`href="/dashboard/checkout?pack=rayo&amp;request=pr_1"`,
		`action="/dashboard/pack-requests/pr_1/decline"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, "pr_2") {
		t.Error("an answered request is offered again")
	}
	if strings.Contains(body, "Test · 500 credits") || strings.Contains(body, "pack=c-test") {
		t.Error("a pack sold in the app only is offered here")
	}
	for _, price := range []string{"$", "€4", "4.99"} {
		if strings.Contains(body, "Pico · 12 tasks"+price) {
			t.Errorf("a pack shows a price %q", price)
		}
	}

	// A trial not started or ended: packs wait for a plan, so none are on
	// sale, and the packs held are still listed.
	v.Meters.CanAddPacks = false
	if body := renderSettings(t, v); strings.Contains(body, "Pico · 12 tasks. They last") || !strings.Contains(body, "Rayo · 60 of 70 tasks left") {
		t.Error("packs are on sale to an account whose plan doesn't run, or the held pack is gone")
	}

	// A member can't add tasks: no packs on sale, no requests to answer.
	v.Meters.CanAddTasks = false
	if body := renderSettings(t, v); strings.Contains(body, "Add tasks") || strings.Contains(body, "pr_1") {
		t.Error("a member is offered packs or requests")
	}
}

// A plan Apple bills is managed with Apple: Settings links there and sells
// no plan. Packs are still sold.
func TestSettingsBilledByApple(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Me.Plan = "solo"
	v.Me.HasBilling = true
	v.Meters = meters("solo", 5, 20, 0, 0, 500)
	apple := "https://apps.apple.com/account/subscriptions"
	v.Meters.Billing.Source, v.Meters.Billing.ManageURL = "apple", &apple
	v.Meters.CanAddTasks, v.Meters.CanAddPacks = true, true
	v.Meters.Store = &struct {
		Packs []apiclient.StorePack `json:"packs"`
	}{Packs: []apiclient.StorePack{{ID: "pico", Name: "Pico", Line: "Pico · 12 tasks", Tasks: 12, Months: 12, StripePrice: "kiff_pack_pico"}}}
	body := renderSettings(t, v)
	for _, want := range []string{"Billed by Apple.", `href="https://apps.apple.com/account/subscriptions"`, "Manage with Apple", "Add Pico",
		// Solo may move to Company here, its paid days credited.
		`href="/dashboard/checkout?plan=team"`, "Move to Company", "become Company days"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, sold := range []string{"Manage billing", "Choose Company", `href="/pricing"`} {
		if strings.Contains(body, sold) {
			t.Errorf("an Apple-billed workspace is offered %q", sold)
		}
	}
	// Apple's free week isn't Solo: nothing is credited, and no Team is offered here.
	v.Me.Plan = "trial"
	if body := renderSettings(t, v); strings.Contains(body, "Move to Company") {
		t.Error("an Apple trial is offered Team on credited days")
	}

	// Moved to Team: Stripe bills it, and Solo still renews with Apple until it's cancelled there.
	v.Me.Plan = "team"
	until := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	v.Meters.Billing.Source, v.Meters.Billing.ManageURL, v.Meters.Billing.AppleSoloUntil = "stripe", nil, &until
	body = renderSettings(t, v)
	for _, want := range []string{"Billed by KIFF.", "renews on 3 November unless it&#39;s cancelled with Apple, from the Apple account that bought it", "Cancel with Apple", `href="https://apps.apple.com/account/subscriptions"`} {
		if !strings.Contains(body, want) {
			t.Errorf("moved to Team: missing %q", want)
		}
	}
	v.Meters.Billing.AppleSoloUntil = nil

	v.Meters.Billing.Source, v.Meters.Billing.ManageURL = "contract", nil
	v.Me.Plan = "company"
	if body := renderSettings(t, v); !strings.Contains(body, "Managed by your company&#39;s agreement.") && !strings.Contains(body, "Managed by your company's agreement.") {
		t.Error("a contract isn't said")
	}
}
