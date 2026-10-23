package mandate

import (
	"fmt"
	"time"
)

// CumulativeAccountingVersion identifies authorizations that charge approved
// per-action exceptions to every applicable cumulative budget. Zero is legacy
// accounting and is deliberately never inferred from today's Card terms.
const CumulativeAccountingVersion = 1

type Measure string

const (
	MeasureAttempts Measure = "attempts"
	MeasureEffects  Measure = "effects"
)

func (a Aggregate) EffectiveMeasure() Measure {
	if a.Measure != "" {
		return a.Measure
	}
	if a.Quantity.Kind == KindCount {
		return MeasureAttempts
	}
	return MeasureEffects
}

func (a Aggregate) validateMeasure() error {
	switch a.EffectiveMeasure() {
	case MeasureAttempts, MeasureEffects:
		return nil
	default:
		return fmt.Errorf("%w: measure must be attempts or effects", ErrInvalidMandate)
	}
}

// resolvedMeasures snapshots defaults on new management revisions. Existing
// stored revisions stay untouched and retain an unspecified legacy measure.
func resolvedMeasures(m Mandate) Mandate {
	m.Aggregates = append([]Aggregate(nil), m.Aggregates...)
	for i := range m.Aggregates {
		m.Aggregates[i].Measure = m.Aggregates[i].EffectiveMeasure()
	}
	return m
}

// withinCeiling avoids overflowing used+draw. Unknown or negative usage is
// never interpreted as room. The same predicate governs evaluation and review.
func withinCeiling(used, draw, ceiling int64) bool {
	return used >= 0 && draw >= 0 && ceiling >= 0 && used <= ceiling && draw <= ceiling-used
}

// reviewedThresholds reports whether the approval covered every currently
// exceeded per-action threshold. Naming a Card alone is insufficient: another
// quantity on the same Card is a new question for its owner.
func reviewedThresholds(current, reviewed []Breach) bool {
	for _, b := range current {
		if b.Outcome != OutcomePerActionExceeded && b.Outcome != OutcomeResourceOutside {
			continue // room is checked separately; approval never waives it
		}
		found := false
		for _, old := range reviewed {
			if old.Outcome == OutcomePerActionExceeded && b.Outcome == OutcomePerActionExceeded && old.MandateID == b.MandateID &&
				old.Quantity.Key() == b.Quantity.Key() && old.Draw == b.Draw && b.Limit >= old.Limit {
				found = true
				break
			}
			// The owner saw this exact resource outside this Card's scope.
			if old.Outcome == OutcomeResourceOutside && b.Outcome == OutcomeResourceOutside && old.MandateID == b.MandateID &&
				old.Resource == b.Resource && old.Value == b.Value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func cumulativeBreaches(bs []Breach) bool {
	for _, b := range bs {
		if b.Outcome == OutcomeLimitReached {
			return true
		}
	}
	return false
}

// CanApproveOnce is also checked by the store, so an older client cannot
// approve past a cumulative ceiling. Redemption still rechecks current usage.
func (e Exception) CanApproveOnce() bool {
	if len(e.Breaches) == 0 {
		return false
	}
	for _, b := range e.Breaches {
		if b.Outcome != OutcomePerActionExceeded && b.Outcome != OutcomeResourceOutside {
			return false
		}
	}
	return true
}

// ownerBreach reports whether an outcome is one a Card's owner can be
// asked about: the call is outside the Card, not malformed.
func ownerBreach(o Outcome) bool {
	return o == OutcomeLimitReached || o == OutcomePerActionExceeded || o == OutcomeResourceOutside
}

type LegacyApproval struct {
	By     string
	At     *time.Time
	Reason string
}

// ApprovalRecord preserves superseded answers in the existing history.
// An ExternalAnswer with decision "defer" is evidence, never consent.
// The currently effective approval stays in ResolvedBy/At.
type ApprovalRecord struct {
	ExternalAnswer    *ExternalAnswer `json:"external_answer,omitempty"`
	By                string
	At                *time.Time
	Reason            string
	AccountingVersion int
	Breaches          []Breach
}

func preserveApproval(e *Exception) {
	if e.ExternalAnswer != nil && e.ExternalAnswer.Decision == "defer" {
		a := e.ExternalAnswer
		e.PriorApprovals = append(append([]ApprovalRecord(nil), e.PriorApprovals...), ApprovalRecord{ExternalAnswer: a,
			By: a.Principal, At: &a.At, Reason: a.Reason,
			AccountingVersion: e.AccountingVersion, Breaches: append([]Breach(nil), e.Breaches...),
		})
		e.ExternalAnswer = nil
		return
	}
	if e.Status != ExceptionApproved || e.ResolvedAt == nil {
		return
	}
	e.PriorApprovals = append(append([]ApprovalRecord(nil), e.PriorApprovals...), ApprovalRecord{ExternalAnswer: e.ExternalAnswer,
		By: e.ResolvedBy, At: e.ResolvedAt, Reason: e.Reason,
		AccountingVersion: e.AccountingVersion, Breaches: append([]Breach(nil), e.Breaches...),
	})
	e.ExternalAnswer = nil
}

// requireFreshConsent preserves the original legacy answer before superseding
// it. No consumed approval, draw, balance or receipt is rewritten.
func requireFreshConsent(e *Exception) {
	if e.Status != ExceptionApproved || e.AccountingVersion != 0 {
		return
	}
	e.LegacyApproval = &LegacyApproval{By: e.ResolvedBy, At: e.ResolvedAt,
		Reason: "new cumulative accounting requires fresh owner review"}
	e.Status, e.ResolvedBy, e.ResolvedAt = ExceptionHeld, "", nil
	e.AccountingVersion = CumulativeAccountingVersion
	e.Reason = e.LegacyApproval.Reason
}

func approvedNoRoom(e Exception, bs []Breach) Decision {
	d := ownerRequired(e)
	d.Outcome, d.Breaches = OutcomeApprovedNoRoom, bs
	d.Reason = "approved, but the Card has no room left; change the Card or reject"
	return d
}

func chargedLimit(agg Aggregate, m Mandate, now time.Time) ChargedLimit {
	start, _ := agg.Window.Start(now, m.Loc()) // evaluation validated the window
	t := ChargedLimit{Measure: agg.EffectiveMeasure(), Window: agg.Window,
		WindowStart: start, Location: m.Loc().String()}
	switch agg.Window {
	case WindowCalendarDay:
		// Next local midnight, not 24 hours: DST days can be 23 or 25 hours.
		t.WindowEnd = start.In(m.Loc()).AddDate(0, 0, 1).UTC()
	case WindowRollingHour:
		t.WindowEnd = now.Add(time.Hour).UTC()
	case WindowRollingDay:
		t.WindowEnd = now.Add(24 * time.Hour).UTC()
	case WindowLifetime:
		t.WindowEnd = lifetimeEnd
	}
	return t
}
