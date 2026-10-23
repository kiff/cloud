package mandate

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

type RoomDuration string

const (
	RoomThisWindow RoomDuration = "this_window"
	RoomUntilDate  RoomDuration = "until_date"
	RoomPermanent  RoomDuration = "permanent"
)

type RoomGrant struct {
	ID             string
	Amount         int64
	Duration       RoomDuration
	At             time.Time
	ExpiresAt      time.Time
	RequestedUntil time.Time
	ConfirmedAt    time.Time
	By             string
	Measure        Measure
	Window         Window
	Location       string
}

type RoomRequest struct {
	ID, Quantity string
	Window       Window
	Measure      Measure
	Amount       int64
	Duration     RoomDuration
	Until        time.Time
	WindowEnd    time.Time
	ExpectedBase *int64
}

type RoomStore interface {
	GrantRoom(context.Context, string, string, RoomRequest, string, time.Time) (Mandate, error)
}

type RoomChangeStore interface {
	ChangeCardRoom(context.Context, string, string, string, time.Time, RoomDuration, time.Time) (Exception, error)
}

// Changing a cumulative ceiling does not also relax a per-action threshold.
// That threshold is a separate review after the room revision. For a purely
// per-action hold, the existing deliberate permanent threshold edit remains.
// A resource the hold is on is added to this Card's scope either way:
// it is what the owner asked to change, and room alone would leave
// the call outside the Card.
func roomForHold(m Mandate, e Exception, u Usage, by string, now time.Time, duration RoomDuration, until time.Time) (Mandate, bool, error) {
	if !cumulativeBreaches(e.Breaches) {
		return ChangeFor(m, e.Request, u, now)
	}
	m, changed := withHeldResource(m, e.Request)
	for i, a := range m.Aggregates {
		since, err := a.Window.Start(now, m.Loc())
		if err != nil {
			return m, false, err
		}
		used, err := limitUsed(u, m, a, now, since)
		if err != nil {
			return m, false, err
		}
		draw := DrawFor(a.Quantity, e.Request.Parameters)
		if withinCeiling(used, draw, a.Ceiling(m, now)) {
			continue
		}
		if used < 0 || draw < 0 || draw > math.MaxInt64-used {
			return m, false, ErrUsageUnavailable
		}
		delta := used + draw - a.Ceiling(m, now)
		if delta <= 0 {
			continue
		}
		m, err = RoomFor(m, RoomRequest{ID: fmt.Sprintf("%s-room-%d", e.ID, i), Quantity: a.Quantity.Key(), Measure: a.EffectiveMeasure(), Window: a.Window, Amount: delta, Duration: duration, Until: until}, by, now)
		if err != nil {
			return m, false, err
		}
		changed = true
	}
	return m, changed, nil
}

func (a Aggregate) ActiveRoom(m Mandate, now time.Time) int64 {
	var total int64
	for _, r := range a.ExtraRoom {
		if r.At.After(now) || !now.Before(r.ExpiresAt) || r.Measure != a.EffectiveMeasure() || r.Window != a.Window || r.Location != m.Loc().String() {
			continue
		}
		if r.Amount < 0 || r.Amount > math.MaxInt64-total {
			return 0
		}
		total += r.Amount
	}
	if total > math.MaxInt64-a.Limit {
		return 0
	}
	return total
}

func (a Aggregate) Ceiling(m Mandate, now time.Time) int64 { return a.Limit + a.ActiveRoom(m, now) }

func RoomFor(m Mandate, req RoomRequest, by string, now time.Time) (Mandate, error) {
	if !m.Active(now) {
		return Mandate{}, ErrMoveInactive
	}
	if req.ID == "" || req.Amount <= 0 || by == "" {
		return Mandate{}, fmt.Errorf("%w: room needs an id, positive amount and confirming identity", ErrInvalidMandate)
	}
	if req.Duration == "" {
		req.Duration = RoomThisWindow
	}
	if req.Duration != RoomThisWindow && req.Duration != RoomUntilDate && req.Duration != RoomPermanent {
		return Mandate{}, fmt.Errorf("%w: invalid room duration", ErrInvalidMandate)
	}
	if (!req.WindowEnd.IsZero() && req.Duration != RoomThisWindow) || (req.Duration != RoomUntilDate && !req.Until.IsZero()) {
		return Mandate{}, fmt.Errorf("%w: an end date is only valid with until_date", ErrInvalidMandate)
	}
	m.Aggregates = append([]Aggregate(nil), m.Aggregates...)
	found := -1
	for i, a := range m.Aggregates {
		if a.Quantity.Key() == req.Quantity && a.Window == req.Window && a.EffectiveMeasure() == req.Measure {
			if found != -1 {
				return Mandate{}, fmt.Errorf("%w: ambiguous cumulative limit", ErrInvalidMandate)
			}
			found = i
		}
	}
	if found < 0 {
		return Mandate{}, fmt.Errorf("%w: cumulative limit not found", ErrInvalidMandate)
	}
	a := m.Aggregates[found]
	if req.ExpectedBase != nil && *req.ExpectedBase != a.Limit {
		return Mandate{}, fmt.Errorf("%w: the base ceiling changed; review the revision again", ErrInvalidMandate)
	}
	for _, r := range a.ExtraRoom {
		if r.ID != req.ID {
			continue
		}
		if r.Amount != req.Amount || r.Duration != req.Duration || r.By != by || (req.Duration == RoomUntilDate && !r.RequestedUntil.Equal(req.Until.UTC())) {
			return Mandate{}, fmt.Errorf("%w: room id already used for different terms", ErrInvalidMandate)
		}
		return m, nil // never renew a grant on a transport retry
	}
	if req.Amount > math.MaxInt64-a.Ceiling(m, now) {
		return Mandate{}, fmt.Errorf("%w: cumulative ceiling overflow", ErrInvalidMandate)
	}
	if req.Duration == RoomPermanent {
		// Retain confirmation evidence even though this changes the base.
		a.Limit += req.Amount
		a.ExtraRoom = append(append([]RoomGrant(nil), a.ExtraRoom...), RoomGrant{ID: req.ID, Amount: req.Amount, Duration: req.Duration, By: by, At: now.UTC(), Measure: a.EffectiveMeasure(), Window: a.Window, Location: m.Loc().String()})
	} else {
		end := req.Until.UTC()
		grantAt := now.UTC()
		if req.Duration == RoomThisWindow {
			end = chargedLimit(a, m, now).WindowEnd
			if !m.ValidUntil.IsZero() && end.After(m.ValidUntil) {
				end = m.ValidUntil.UTC()
			}
			if !req.WindowEnd.IsZero() {
				if req.WindowEnd.After(end) || !req.WindowEnd.After(now) || (a.Window == WindowCalendarDay && !req.WindowEnd.Equal(end)) {
					return Mandate{}, fmt.Errorf("%w: the confirmed window ended or changed; review extra room again", ErrInvalidMandate)
				}
				end = req.WindowEnd.UTC()
				if a.Window == WindowRollingHour {
					grantAt = end.Add(-time.Hour)
				}
				if a.Window == WindowRollingDay {
					grantAt = end.Add(-24 * time.Hour)
				}
			}
		}
		if !m.ValidUntil.IsZero() && end.After(m.ValidUntil) {
			end = m.ValidUntil.UTC()
		}
		if end.IsZero() || !end.After(now) {
			return Mandate{}, fmt.Errorf("%w: extra room must end after confirmation", ErrInvalidMandate)
		}
		a.ExtraRoom = append(append([]RoomGrant(nil), a.ExtraRoom...), RoomGrant{ID: req.ID, Amount: req.Amount, Duration: req.Duration, By: by, At: grantAt, ConfirmedAt: now.UTC(), ExpiresAt: end, RequestedUntil: req.Until.UTC(), Measure: a.EffectiveMeasure(), Window: a.Window, Location: m.Loc().String()})
	}
	m.Aggregates[found] = a
	return m, nil
}

func (s *MemoryStore) GrantRoom(_ context.Context, tenant, id string, req RoomRequest, by string, now time.Time) (Mandate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failRecord != nil {
		return Mandate{}, s.failRecord
	}
	m, ok := s.mandates[key(tenant, id)]
	if !ok {
		return Mandate{}, ErrNotFound
	}
	seen, err := roomHistory(s.revisions[key(tenant, id)], req, by)
	if err != nil {
		return Mandate{}, err
	}
	if seen {
		return m, nil
	}
	changed, err := RoomFor(m, req, by, now)
	if err != nil {
		return Mandate{}, err
	}
	// A duplicate confirmation returns its existing revision, not a new one.
	if roomRecorded(m, req.ID) {
		return m, nil
	}
	s.issueLocked(changed, by)
	return changed, nil
}

func roomRecorded(m Mandate, id string) bool {
	for _, a := range m.Aggregates {
		for _, r := range a.ExtraRoom {
			if r.ID == id {
				return true
			}
		}
	}
	return false
}

func (s *PostgresStore) GrantRoom(ctx context.Context, tenant, id string, req RoomRequest, by string, now time.Time) (Mandate, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Mandate{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockCards(ctx, tx, tenant, []string{id}); err != nil {
		return Mandate{}, err
	}
	rows, err := tx.Query(ctx, mandateColumns+` FROM cloud_meta.mandates WHERE tenant_id=$1 AND id=$2`, tenant, id)
	if err != nil {
		return Mandate{}, err
	}
	ms, err := scanMandates(rows)
	if err != nil {
		return Mandate{}, err
	}
	if len(ms) == 0 {
		return Mandate{}, ErrNotFound
	}
	m := ms[0]
	rows, err = tx.Query(ctx, `SELECT aggregates FROM cloud_meta.mandate_revisions WHERE tenant_id=$1 AND mandate_id=$2 ORDER BY revision`, tenant, id)
	if err != nil {
		return Mandate{}, err
	}
	history := []Revision{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return Mandate{}, err
		}
		var aggs []Aggregate
		if err := json.Unmarshal(raw, &aggs); err != nil {
			rows.Close()
			return Mandate{}, err
		}
		history = append(history, Revision{Aggregates: aggs})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Mandate{}, err
	}
	seen, err := roomHistory(history, req, by)
	if err != nil {
		return Mandate{}, err
	}
	if seen {
		return m, tx.Commit(ctx)
	}
	changed, err := RoomFor(m, req, by, now)
	if err != nil {
		return Mandate{}, err
	}
	if !roomRecorded(m, req.ID) {
		if err := updateTerms(ctx, tx, changed, by); err != nil {
			return Mandate{}, err
		}
	}
	return changed, tx.Commit(ctx)
}

// The revision archive retains grant ids even if a later edit removes or
// replaces that limit. Replaying a prior confirmation cannot mint new room.
func roomHistory(revs []Revision, req RoomRequest, by string) (bool, error) {
	duration := req.Duration
	if duration == "" {
		duration = RoomThisWindow
	}
	for _, rev := range revs {
		for _, a := range rev.Aggregates {
			for _, r := range a.ExtraRoom {
				if r.ID != req.ID {
					continue
				}
				if a.Quantity.Key() != req.Quantity || r.Window != req.Window || r.Measure != req.Measure || r.Amount != req.Amount || r.Duration != duration || r.By != by || (!req.WindowEnd.IsZero() && !r.ExpiresAt.Equal(req.WindowEnd)) || (duration == RoomUntilDate && !r.RequestedUntil.Equal(req.Until.UTC())) {
					return false, fmt.Errorf("%w: room id already used for different terms", ErrInvalidMandate)
				}
				return true, nil
			}
		}
	}
	return false, nil
}

func keepRoom(m Mandate, old []Aggregate) Mandate {
	m.Aggregates = append([]Aggregate(nil), m.Aggregates...)
	for i, a := range m.Aggregates {
		if len(a.ExtraRoom) > 0 {
			continue
		}
		for _, before := range old {
			if a.Quantity.Key() == before.Quantity.Key() && a.Window == before.Window && a.EffectiveMeasure() == before.EffectiveMeasure() {
				// Management bodies cannot create grants. The dedicated operation owns
				// fixed amount, identity and expiry; a base-limit edit preserves them.
				m.Aggregates[i].ExtraRoom = append([]RoomGrant(nil), before.ExtraRoom...)
				break
			}
		}
	}
	return m
}

func RoomWindowEnd(a Aggregate, m Mandate, now time.Time) time.Time {
	return chargedLimit(a, m, now).WindowEnd
}
