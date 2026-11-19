package handlers

import (
	"time"

	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// Risk scoring for the operator command center. Computed at
// read time from cross-tenant usage counters + plan limits — no new
// table, no background job. Simple thresholds, not ML; every signal
// is backed by a real counter.

// OpsRiskLevel ranks a tenant's attention need.
const (
	riskLevelWarn = "warn"
	riskLevelHigh = "high"
)

// OpsRiskSignal is one reason a tenant needs attention.
type OpsRiskSignal struct {
	Key    string `json:"key"`
	Detail string `json:"detail"`
	Level  string `json:"level"`
}

// OpsTenantRisk is a tenant's computed risk for the attention list.
type OpsTenantRisk struct {
	ID      string          `json:"id"`
	Slug    string          `json:"slug"`
	Status  string          `json:"status"`
	Plan    string          `json:"plan"`
	Score   int             `json:"score"`
	Level   string          `json:"level"`
	Signals []OpsRiskSignal `json:"signals"`
}

// OpsBacklog is the platform-wide anchor backlog health.
type OpsBacklog struct {
	Pending          int   `json:"pending"`
	OldestAgeSeconds int64 `json:"oldest_age_seconds"`
}

// counterTotals is per-tenant counter sums for a window.
type counterTotals map[string]uint64

// groupUsageByTenant sums counters per tenant from a cross-tenant
// Snapshot result.
func groupUsageByTenant(rows []usage.Counter) map[string]counterTotals {
	out := map[string]counterTotals{}
	for _, c := range rows {
		t := out[c.TenantID]
		if t == nil {
			t = counterTotals{}
			out[c.TenantID] = t
		}
		t[c.CounterName] += c.Count
	}
	return out
}

// scoreTenant computes the risk signals for one tenant from its
// month-to-date counters and plan limit. Returns nil when nothing is
// noteworthy (the tenant stays off the attention list).
func scoreTenant(t tenant.Tenant, totals counterTotals) *OpsTenantRisk {
	submitted := totals[usage.CloudCounterProposalsSubmitted]
	blocked := totals[usage.CloudCounterProposalsBlocked]
	approval := totals[usage.CloudCounterProposalsApprovalRequired]

	var signals []OpsRiskSignal
	add := func(key, detail, level string) {
		signals = append(signals, OpsRiskSignal{Key: key, Detail: detail, Level: level})
	}

	// Blocked spike: a high block ratio on real volume is the
	// strongest "something is wrong" signal (misconfig or attack).
	if submitted >= 20 {
		if rate := float64(blocked) / float64(submitted); rate >= 0.30 {
			add("blocked_spike", pct(blocked, submitted)+" of proposals blocked", riskLevelHigh)
		}
		if rate := float64(approval) / float64(submitted); rate >= 0.50 {
			add("approval_spike", pct(approval, submitted)+" awaiting approval", riskLevelWarn)
		}
	}

	// Quota pressure: approaching or over the monthly proposal cap.
	if limit := tenant.PlanFor(t).MonthlyProposalLimit; limit > 0 {
		ratio := float64(submitted) / float64(limit)
		switch {
		case ratio >= 1.0:
			add("quota_exceeded", "over monthly proposal limit", riskLevelHigh)
		case ratio >= 0.80:
			add("quota_pressure", pctRatio(ratio)+" of monthly limit used", riskLevelWarn)
		}
	}

	if len(signals) == 0 {
		return nil
	}
	score, level := 0, riskLevelWarn
	for _, s := range signals {
		if s.Level == riskLevelHigh {
			score += 2
			level = riskLevelHigh
		} else {
			score++
		}
	}
	return &OpsTenantRisk{
		ID: t.ID, Slug: t.Slug, Status: string(t.Status),
		Plan: string(tenant.PlanFor(t).Plan), Score: score, Level: level, Signals: signals,
	}
}

func pct(n, d uint64) string {
	if d == 0 {
		return "0%"
	}
	return pctRatio(float64(n) / float64(d))
}

func pctRatio(r float64) string {
	v := int(r*100 + 0.5)
	return itoa(v) + "%"
}

// itoa avoids importing strconv just for small ints in templ-free
// strings; ops_activity.go already imports strconv at the package
// level, but keeping this local keeps the risk file self-contained.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// backlogAgeSeconds converts an oldest-pending timestamp to an age in
// seconds at now, or 0 when there is no backlog.
func backlogAgeSeconds(oldest time.Time, now time.Time) int64 {
	if oldest.IsZero() {
		return 0
	}
	d := now.Sub(oldest)
	if d < 0 {
		return 0
	}
	return int64(d.Seconds())
}
