package mandate

import (
	"slices"
	"sort"
	"time"
)

// The replay (kiff/tafs/docs/allow-from-now-on.md): what a draft's Cards
// would have done with the calls held over the last days. Only held calls are
// decided again: a held call keeps its whole request, and KIFF keeps no other
// call's parameters. Nothing is sent, held or recorded.

// ReplayTotal is what the calls a replay lets go carry, in one unit.
type ReplayTotal struct {
	Quantity string `json:"quantity"` // "amount"
	Unit     string `json:"unit"`
	Total    int64  `json:"total"`
}

// ReplayResult is how many held calls a draft's Cards would have let go on
// their own, and what they carry.
type ReplayResult struct {
	Calls      int           `json:"calls"`
	ByQuantity []ReplayTotal `json:"by_quantity"`
}

// Replay decides each of held again against cards, in the order the calls
// were made, as a fresh call would have been decided then: on the draws
// recorded before it, plus the draws of the calls the replay has let go
// before it. A held call's own real draw (once approved) is never counted:
// the replay's own stands for it. A unit hold, or one whose request can't be
// decided, isn't counted.
func Replay(cards []Mandate, held []Exception, draws []Draw) (ReplayResult, error) {
	calls := slices.Clone(held)
	sort.SliceStable(calls, func(i, j int) bool { return calls[i].CreatedAt.Before(calls[j].CreatedAt) })
	skip := make(map[string]bool, len(calls))
	for _, e := range calls {
		skip[e.ProposalID] = true
	}
	var ledger []Draw
	for _, d := range draws {
		if !skip[d.ProposalID] {
			ledger = append(ledger, d)
		}
	}
	var out ReplayResult
	totals := map[string]int64{}
	for _, e := range calls {
		if e.KindOf() == KindCardUnit {
			continue
		}
		at := e.CreatedAt
		used := UsageFunc(func(id string, q Quantity, since time.Time) (int64, error) {
			var n int64
			for _, d := range ledger {
				if d.MandateID == id && d.QuantityKey == q.Key() && !d.DrawnAt.Before(since) && d.DrawnAt.Before(at) {
					n += d.Amount
				}
			}
			return n, nil
		})
		d, err := Trial(cards, e.Request, used, at)
		if err != nil {
			return ReplayResult{}, err
		}
		if d.Outcome != OutcomeWithinLimit {
			continue
		}
		out.Calls++
		ledger = append(ledger, drawsFor(cards, nil, e.TenantID, e.ProposalID, e.Request, at)...)
		// What it carries, once per unit: the amounts its own breaches named.
		byUnit := map[string]int64{}
		for _, b := range e.Breaches {
			if b.Unit == "" || b.Quantity.Kind == KindCount {
				continue
			}
			if a := DrawFor(b.Quantity, e.Request.Parameters); a > byUnit[b.Unit] {
				byUnit[b.Unit] = a
			}
		}
		for u, a := range byUnit {
			totals[u] += a
		}
	}
	units := make([]string, 0, len(totals))
	for u := range totals {
		units = append(units, u)
	}
	sort.Strings(units)
	out.ByQuantity = []ReplayTotal{}
	for _, u := range units {
		out.ByQuantity = append(out.ByQuantity, ReplayTotal{Quantity: "amount", Unit: u, Total: totals[u]})
	}
	return out, nil
}

// ReplaySince is how far back a replay over calls made from from must read
// draws: the start of the widest window of cards' totals at from.
func ReplaySince(cards []Mandate, from time.Time) time.Time {
	since := from
	for _, m := range cards {
		for _, a := range m.Aggregates {
			if s, err := a.Window.Start(from, m.Loc()); err == nil && s.Before(since) {
				since = s
			}
		}
	}
	return since
}
