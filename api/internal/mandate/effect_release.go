package mandate

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// EffectRelease is append-only compensation. The original draw, claim and
// receipt remain unchanged; this record cannot make an operation send again.
type EffectRelease struct {
	TenantID, MandateID, ProposalID, QuantityKey string
	Amount                                       int64
	At                                           time.Time
	Source, EvidenceID, Reason                   string
	ChargedLimits                                []ChargedLimit
}

// NoEffectEvidence is supplied only after reading a trusted executor's durable
// terminal outcome, never from a requesting agent's explanation or error flag.
type NoEffectEvidence struct {
	TenantID, ProposalID, Subject, Action string
	Source, EvidenceID, Reason            string
	At                                    time.Time
}

func (e NoEffectEvidence) validate() error {
	if e.TenantID == "" || e.ProposalID == "" || e.Subject == "" || e.Action == "" ||
		e.Source == "" || e.EvidenceID == "" || e.Reason == "" || e.At.IsZero() {
		return errors.New("mandate: incomplete terminal no-effect evidence")
	}
	return nil
}

type EffectReleaseStore interface {
	ReleaseEffects(context.Context, NoEffectEvidence) ([]EffectRelease, error)
	EffectReleases(context.Context, string, string) ([]EffectRelease, error)
}

type LimitBalance struct{ Gross, Released, Net int64 }

type limitUsageReader interface {
	Used(context.Context, string, string, string, time.Time, string) (int64, error)
}

type limitBalanceReader interface {
	LimitBalance(context.Context, string, Mandate, Aggregate, time.Time, string) (LimitBalance, error)
}

// ReadLimitBalance preserves compatibility with conservative stores which have
// no release ledger: they never invent a release or silently grant more room.
func ReadLimitBalance(ctx context.Context, s limitUsageReader, tenant string, m Mandate, agg Aggregate, now time.Time, exclude string) (LimitBalance, error) {
	if r, ok := s.(limitBalanceReader); ok {
		return r.LimitBalance(ctx, tenant, m, agg, now, exclude)
	}
	since, err := agg.Window.Start(now, m.Loc())
	if err != nil {
		return LimitBalance{}, err
	}
	gross, err := s.Used(ctx, tenant, m.ID, agg.Quantity.Key(), since, exclude)
	return LimitBalance{Gross: gross, Net: gross}, err
}

type limitUsage struct {
	Usage
	read func(Mandate, Aggregate, time.Time) (int64, error)
}

func usageFor(ctx context.Context, s limitUsageReader, tenant, exclude string) Usage {
	return limitUsage{Usage: UsageFunc(func(id string, q Quantity, since time.Time) (int64, error) {
		return s.Used(ctx, tenant, id, q.Key(), since, exclude)
	}), read: func(m Mandate, a Aggregate, now time.Time) (int64, error) {
		b, err := ReadLimitBalance(ctx, s, tenant, m, a, now, exclude)
		return b.Net, err
	}}
}

func limitUsed(u Usage, m Mandate, a Aggregate, now, timeStart time.Time) (int64, error) {
	if r, ok := u.(limitUsage); ok {
		return r.read(m, a, now)
	}
	return u.Used(m.ID, a.Quantity, timeStart)
}

func eligibleRelease(d Draw, e NoEffectEvidence) (EffectRelease, bool, error) {
	if d.AccountingVersion < CumulativeAccountingVersion {
		return EffectRelease{}, false, nil
	}
	if d.Subject != e.Subject || !strings.EqualFold(d.Action, e.Action) {
		return EffectRelease{}, false, errors.New("mandate: outcome does not match the authorized action")
	}
	if e.At.Before(d.DrawnAt) {
		return EffectRelease{}, false, errors.New("mandate: outcome predates authorization")
	}
	terms := []ChargedLimit{}
	for _, t := range d.ChargedLimits {
		if t.Measure == MeasureEffects {
			terms = append(terms, t)
		}
	}
	if len(terms) == 0 {
		return EffectRelease{}, false, nil
	}
	return EffectRelease{TenantID: d.TenantID, MandateID: d.MandateID, ProposalID: d.ProposalID, QuantityKey: d.QuantityKey,
		Amount: d.Amount, At: e.At, Source: e.Source, EvidenceID: e.EvidenceID, Reason: e.Reason, ChargedLimits: terms}, true, nil
}

// A release follows its original limit and authorization time. In particular,
// an expired calendar/rolling charge cannot create credit in a later window,
// or in a differently configured measure or timebase.
func releaseCounts(r EffectRelease, d Draw, m Mandate, a Aggregate, now, since time.Time) bool {
	if r.Amount != d.Amount || d.DrawnAt.Before(since) || r.At.After(now) {
		return false
	}
	for _, t := range r.ChargedLimits {
		if t.Measure != a.EffectiveMeasure() || t.Window != a.Window || t.Location != m.Loc().String() || !now.Before(t.WindowEnd) {
			continue
		}
		if a.Window == WindowCalendarDay && !t.WindowStart.Equal(since) {
			continue
		}
		return true
	}
	return false
}

func addBalance(b *LimitBalance, d Draw, r *EffectRelease, m Mandate, a Aggregate, now, since time.Time) error {
	if d.Amount < 0 || d.Amount > math.MaxInt64-b.Gross {
		return fmt.Errorf("%w: invalid or overflowing ledger", ErrUsageUnavailable)
	}
	b.Gross += d.Amount
	if r != nil && releaseCounts(*r, d, m, a, now, since) {
		b.Released += d.Amount
	}
	b.Net = b.Gross - b.Released
	return nil
}
