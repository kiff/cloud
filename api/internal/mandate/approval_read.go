package mandate

import (
	"context"
	"fmt"
	"time"
)

const (
	ExceptionApprovedNoRoom ExceptionStatus = "approved_no_room"
	ExceptionUnavailable    ExceptionStatus = "unavailable"
)

// ExceptionProjection keeps the answer and its metadata from the same
// snapshot. Status describes collectability; OwnerAnswer is stored consent.
// Reads never claim an operation, draw budget or take authorization locks.
type ExceptionProjection struct {
	Exception        Exception
	Status           ExceptionStatus
	OwnerAnswer      ExceptionStatus
	ReviewedBreaches []Breach
}

type exceptionProjector interface {
	projectException(context.Context, Exception, time.Time) (ExceptionProjection, error)
}

func ReadException(ctx context.Context, s Store, e Exception, now time.Time) (ExceptionProjection, error) {
	var p ExceptionProjection
	var err error
	if reader, ok := s.(exceptionProjector); ok {
		p, err = reader.projectException(ctx, e, now)
	} else {
		if es, ok := s.(ExceptionStore); ok {
			fresh, readErr := es.Exception(ctx, e.TenantID, e.ID)
			err = readErr
			if err == nil {
				e = fresh
			}
		}
		if err == nil {
			var ms []Mandate
			// Terminal answers need no usage or Card query.
			if e.Status == ExceptionApproved || e.Status == ExceptionHeld {
				ms, err = s.ForSubject(ctx, e.TenantID, e.Subject)
			}
			if err == nil {
				p, err = projectApproval(ctx, s, ms, e, now)
			}
		}
	}
	if err != nil {
		return ExceptionProjection{Exception: e, Status: ExceptionUnavailable, OwnerAnswer: e.Status}, fmt.Errorf("%w: %v", ErrUsageUnavailable, err)
	}
	return p, nil
}

type approvalUsageReader interface {
	Used(context.Context, string, string, string, time.Time, string) (int64, error)
}

func projectApproval(ctx context.Context, s approvalUsageReader, ms []Mandate, e Exception, now time.Time) (ExceptionProjection, error) {
	p := ExceptionProjection{Exception: e, OwnerAnswer: e.Status}
	if e.Status == ExceptionApproved && e.AccountingVersion == 0 {
		requireFreshConsent(&e)
	}
	if e.Expired(now) {
		expire(&e)
	}
	// Project withdrawal without writes or authorization locks. Effectful
	// answer/collection paths persist this same transition under their locks.
	if _, err := refreshExternalApproval(ctx, s, &e, now); err != nil {
		return p, err
	}
	held := map[string]Mandate{}
	for _, m := range ms {
		held[m.ID] = m
	}
	if e.Status == ExceptionApproved || e.Status == ExceptionHeld {
		if reason := grantStillStands(e, held, now); reason != "" {
			resolve(&e, ExceptionInvalidated, "", now)
			e.Reason = reason
		}
	}
	p.Exception, p.Status = e, e.Status
	if e.Status != ExceptionApproved {
		return p, nil
	}
	usage := usageFor(ctx, s, e.TenantID, e.ProposalID)
	d, err := Evaluate(ms, e.Request, usage, now)
	if err != nil {
		return p, err
	}
	if d.Allowed() && d.Outcome != OutcomeNotCovered {
		return p, nil
	}
	if !ownerBreach(d.Outcome) {
		p.Status = ExceptionInvalidated
		return p, nil
	}
	bs, countable, err := Breaches(ms, e.Request, usage, now)
	if err != nil {
		return p, err
	}
	if !countable {
		p.Status = ExceptionInvalidated
		return p, nil
	}
	for _, b := range bs {
		if !held[b.MandateID].ComesHome() {
			p.Status = ExceptionInvalidated
			return p, nil
		}
	}
	if !reviewedThresholds(bs, e.Breaches) {
		// A new threshold is a fresh question even when room is also short.
		preserveApproval(&e)
		e.Status, e.ResolvedBy, e.ResolvedAt, e.Breaches = ExceptionHeld, "", nil, bs
		e.Reason = "a newly exceeded per-action threshold or resource requires fresh owner review"
		if e.Expired(now) {
			expire(&e)
		}
		p.Exception, p.Status = e, e.Status
		return p, nil
	}
	if cumulativeBreaches(bs) {
		p.Status = ExceptionApprovedNoRoom
		p.ReviewedBreaches = append([]Breach(nil), e.Breaches...)
		p.Exception.Breaches = bs
	}
	return p, nil
}
