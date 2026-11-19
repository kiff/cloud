package handlers

import (
	"context"
	"time"

	"github.com/kiff/cloud/api/internal/cardwords"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/tooloutcome"
	"github.com/kiff/cloud/api/internal/words"
)

// pendingDraws are a Card's draws in the current windows whose call the
// tool refused, and that no trusted no-effect evidence has released, by
// proposal and quantity. They still count: a tool's error text is never
// evidence that nothing moved (is KIFF's own check). A
// limit line names them as pending, so a reader sees why a total is used
// without a done call to show for it. Best effort: nil when the draws,
// releases or outcomes can't be read, and the line says nothing extra.
func (h *MandatesHandler) pendingDraws(ctx context.Context, tenantID string, m mandate.Mandate, since time.Time) []mandate.Draw {
	if h.outcomes == nil {
		return nil
	}
	draws, err := h.store.Draws(ctx, tenantID, m.ID, since)
	if err != nil || len(draws) == 0 {
		return nil
	}
	ids := make([]string, 0, len(draws))
	for _, d := range draws {
		ids = append(ids, d.ProposalID)
	}
	outcomes, err := h.outcomes.Outcomes(ctx, tenantID, ids)
	if err != nil {
		return nil
	}
	released := map[string]bool{}
	if s, ok := h.store.(mandate.EffectReleaseStore); ok {
		rs, err := s.EffectReleases(ctx, tenantID, m.ID)
		if err != nil {
			return nil
		}
		for _, r := range rs {
			released[r.ProposalID+"|"+r.QuantityKey] = true
		}
	}
	var out []mandate.Draw
	for _, d := range draws {
		if o, ok := outcomes[d.ProposalID]; ok && o.Status == tooloutcome.RefusedByTool && !released[d.ProposalID+"|"+d.QuantityKey] {
			out = append(out, d)
		}
	}
	return out
}

// withPending adds to a limit line what of its use is pending: the sum
// and count of refused calls in its window. Only a total that counts
// effects has any: a total of attempts counts a refused call for good.
func withPending(line map[string]any, pending []mandate.Draw, agg mandate.Aggregate, since, now time.Time, w *words.Words) {
	if agg.EffectiveMeasure() != mandate.MeasureEffects {
		return
	}
	var sum int64
	calls := 0
	for _, d := range pending {
		if d.QuantityKey != agg.Quantity.Key() || d.DrawnAt.Before(since) || d.DrawnAt.After(now) {
			continue
		}
		sum += d.Amount
		calls++
	}
	if calls == 0 {
		return
	}
	line["pending"] = sum
	line["pending_calls"] = calls
	line["pending_text"] = pendingText(sum, calls, agg.Quantity.Kind == mandate.KindCount, "", 0, w)
}

// pendingText is what a limit line's pending use means, in the owner's
// words: never that nothing moved, which KIFF can't know from the tool's
// error alone. A sum whose parameter declares no unit names no amount: its
// raw number is in minor units, and would read as 100 times too much.
func pendingText(sum int64, calls int, count bool, unit string, scale int, w *words.Words) string {
	switch {
	case count:
		return w.PendingCalls(calls)
	case unit == "":
		return w.PendingNoUnit(calls)
	}
	return w.PendingAmount(cardwords.Money(sum, unit, scale, w), calls)
}
