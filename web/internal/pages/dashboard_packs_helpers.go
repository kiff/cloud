package pages

import (
	"fmt"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// Task packs on Settings: who bills the plan, the packs the workspace has,
// the packs on sale and the members' requests for them. Prices are
// Stripe's, shown at its checkout; never here.

// billedByApple reports that Apple bills the workspace's plan: the plan is
// managed with Apple and never sold here.
func billedByApple(v DashboardView) bool {
	return v.Meters != nil && v.Meters.Billing.Source == "apple"
}

// appleManageURL is Apple's subscriptions page, from the API.
func appleManageURL(v DashboardView) string {
	if v.Meters != nil && v.Meters.Billing.ManageURL != nil {
		return *v.Meters.Billing.ManageURL
	}
	return "https://apps.apple.com/account/subscriptions"
}

// appleSoloMovesToTeam reports an account Apple bills for Solo, which may
// move to Team here: its paid Solo days become Team days.
func appleSoloMovesToTeam(v DashboardView) bool {
	return billedByApple(v) && canonicalPlan(v.Me) == "solo"
}

// appleSoloStillRenews is the line for an account that moved to Team while
// its App Store Solo still renews with Apple; "" when it doesn't.
func appleSoloStillRenews(v DashboardView) string {
	if v.Meters == nil || v.Meters.Billing.AppleSoloUntil == nil {
		return ""
	}
	return "Company is billed here now. The Solo bought in the App Store renews on " +
		v.Meters.Billing.AppleSoloUntil.Format("2 January") + " unless it's cancelled with Apple, from the Apple account that bought it."
}

// billingSourceLine is the one line saying who bills the plan.
func billingSourceLine(v DashboardView) string {
	if v.Meters == nil {
		return ""
	}
	switch v.Meters.Billing.Source {
	case "apple":
		return "Billed by Apple."
	case "google":
		return "Billed by Google Play."
	case "stripe":
		return "Billed by KIFF."
	case "contract":
		return "Managed by your company's agreement."
	}
	return ""
}

// tasksLeftLine is "65 tasks left", or "" where there's no limit.
func tasksLeftLine(v DashboardView) string {
	if v.Meters == nil || v.Meters.Tasks.Left == nil {
		return ""
	}
	n := *v.Meters.Tasks.Left
	return fmt.Sprintf("%s %s left this period", commaInt(n), pluralize(n, "task", "tasks"))
}

// tasksBreakdown is "20 included · 75 from packs".
func tasksBreakdown(v DashboardView) string {
	if v.Meters == nil || v.Meters.Tasks.Left == nil {
		return ""
	}
	return fmt.Sprintf("%s included · %s from packs", commaInt(v.Meters.Tasks.Included), commaInt(v.Meters.Tasks.Packs))
}

// packHeld is one of the workspace's packs: "Rayo · 60 of 70 tasks left,
// until Oct 22, 2027".
func packHeld(p apiclient.UsagePack) string {
	return fmt.Sprintf("%s · %s of %s tasks left, until %s", p.Name, commaInt(p.TasksLeft), commaInt(p.Tasks), p.ExpiresAt.Format("Jan 2, 2006"))
}

// packOnSale is a pack on sale: its line and how long its tasks last.
func packOnSale(p apiclient.StorePack) string {
	return fmt.Sprintf("%s. They last %d months.", p.Line, p.Months)
}

// storePacks are the packs on sale here, for someone who may add tasks to
// an account whose plan runs: on a trial not started or ended, packs wait
// for a plan. A pack with no Stripe price is sold in the app only.
func storePacks(v DashboardView) []apiclient.StorePack {
	if v.Meters == nil || !v.Meters.CanAddPacks || v.Meters.Store == nil {
		return nil
	}
	var out []apiclient.StorePack
	for _, p := range v.Meters.Store.Packs {
		if p.StripePrice != "" {
			out = append(out, p)
		}
	}
	return out
}

// openRequests are the members' requests waiting for an answer, for
// someone who may add tasks.
func openRequests(v DashboardView) []apiclient.PackRequest {
	if v.Meters == nil || !v.Meters.CanAddTasks {
		return nil
	}
	var out []apiclient.PackRequest
	for _, r := range v.Meters.Requests {
		if r.Status == "open" {
			out = append(out, r)
		}
	}
	return out
}

// requestPackLine is the requested pack's line, from the packs on sale.
func requestPackLine(v DashboardView, r apiclient.PackRequest) string {
	if v.Meters != nil && v.Meters.Store != nil {
		for _, p := range v.Meters.Store.Packs {
			if p.ID == r.Pack {
				return p.Line
			}
		}
	}
	return r.Pack
}

func packCheckoutURL(packID, requestID string) string {
	u := "/dashboard/checkout?pack=" + packID
	if requestID != "" {
		u += "&request=" + requestID
	}
	return u
}
