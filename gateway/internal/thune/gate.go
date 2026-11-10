package thune

import (
	"context"
	"errors"
	"time"
)

// Gate is what the gateway calls before it decides a refund and after it
// forwards one. It builds the case under the charge's lock, takes the
// claim, and records the call's two statuses.
type Gate struct {
	Store  *Store
	Reader Reader
	// Calls settles a lost call found by its marker in what Before reads;
	// nil leaves the gateway's record as it is.
	Calls Calls
	// Lease is how long the charge lock is held for one call; it must
	// exceed a decide plus a forward. Zero: 2 minutes.
	Lease time.Duration
}

func (g *Gate) lease() time.Duration {
	if g.Lease > 0 {
		return g.Lease
	}
	return 2 * time.Minute
}

// Before is the case a call is decided on.
type Before struct {
	Case Case
	// Claim is who holds the request; Claim.Won is false when another
	// call holds it, and the case is then refused with ReasonClaimed.
	Claim ClaimResult
	// Retry: the call was decided before (held); its case was read again.
	Retry bool
	// Void: the facts changed while the call was held; the approval no
	// longer applies, and the owner is asked again on a new call.
	Void bool
}

// ErrChargeBusy: another call is deciding or sending a refund on the same
// charge right now. Nothing was read or recorded; retry shortly.
var ErrChargeBusy = errors.New("thune: another refund on this charge is being decided")

// Before builds key's case for ask on id's charge. The charge's lock is
// taken first and held until After; on any error it is released here.
func (g *Gate) Before(ctx context.Context, id Identity, ask Ask, key string) (b Before, err error) {
	if err := id.Valid(); err != nil {
		return b, err
	}
	s := g.Store
	ok, err := s.LockCharge(ctx, id.TenantID, id.Account, id.Object, key, g.lease())
	if err != nil {
		return b, err
	}
	if !ok {
		return b, ErrChargeBusy
	}
	defer func() {
		if err != nil {
			_ = s.UnlockCharge(context.WithoutCancel(ctx), id.TenantID, id.Account, id.Object, key)
		}
	}()
	prev, perr := s.Get(ctx, key)
	if perr != nil && !errors.Is(perr, ErrNotFound) {
		return b, perr
	}
	facts, err := g.Reader.ReadCharge(ctx, id.TenantID, id.Account, id.Object)
	if err != nil {
		return b, err
	}
	// What was read is recorded first: a refund made outside KIFF, or a
	// lost call's refund found by its marker, is seen by every case.
	if err := (&Watcher{Store: s, Calls: g.Calls}).Settle(ctx, id.TenantID, facts); err != nil {
		return b, err
	}
	// KIFF's own refunds on the charge whose answers are still out (sending,
	// or lost) count as pending until the processor lists them: a second
	// request can't spend a remainder one of them may already have taken.
	inflight, err := s.InFlight(ctx, id.TenantID, id.Account, id.Object, key)
	if err != nil {
		return b, err
	}
	facts = facts.WithInFlight(inflight)
	if perr == nil {
		// A retry of a decided call: read again before sending.
		b.Retry = true
		switch prev.Execution {
		case ExecHeld, ExecAllowed:
		default:
			// Ended: its record answers.
			b.Case = caseOf(prev)
			b.Claim = ClaimResult{Won: true, Holder: key, HolderExecution: prev.Execution, HolderFinancial: prev.Financial}
			return b, nil
		}
		if Changed(prev.Facts, facts) {
			won, err := s.SetExecution(ctx, key, []string{ExecHeld, ExecAllowed}, ExecVoid)
			if err != nil {
				return b, err
			}
			if !won {
				// Another retry of this call moved it first (it is sending,
				// or sent): the claim is that call's, and its record answers.
				cur, err := s.Get(ctx, key)
				if err != nil {
					return b, err
				}
				b.Case = caseOf(cur)
				b.Claim = ClaimResult{Won: true, Holder: key, HolderExecution: cur.Execution, HolderFinancial: cur.Financial}
				return b, nil
			}
			if prev.Identity.Request != "" {
				if _, err := s.Release(ctx, prev.Identity, key); err != nil {
					return b, err
				}
			}
			b.Void = true
			b.Case = caseOf(prev)
			b.Case.Facts = facts
			b.Case.Verdict, b.Case.Reasons, b.Case.Authorized = VerdictRefuse, []string{ReasonFactsChanged}, 0
			return b, nil
		}
		c := Build(prev.Identity, Ask{Amount: prev.Requested, Remainder: prev.Remainder}, facts, s.now())
		// The refund already decided is not its own "second refund".
		if _, err := s.Refresh(ctx, key, c); err != nil {
			return b, err
		}
		b.Case = caseOf(prev)
		b.Case.Facts = facts
		b.Claim = ClaimResult{Won: true, Holder: key}
		return b, nil
	}
	c := Build(id, ask, facts, s.now())
	if c.Verdict != VerdictRefuse && id.Request != "" {
		cl, err := s.Claim(ctx, id, key)
		if err != nil {
			return b, err
		}
		b.Claim = cl
		if !cl.Won {
			c.Verdict, c.Reasons, c.Authorized = VerdictRefuse, []string{ReasonClaimed}, 0
		}
	}
	if _, err := s.Record(ctx, key, c); err != nil {
		return b, err
	}
	b.Case = c
	return b, nil
}

func caseOf(a Adjustment) Case {
	return Case{Identity: a.Identity, Requested: a.Requested, Remainder: a.Remainder, Facts: a.Facts,
		Authorized: a.Authorized, Verdict: a.Verdict, Reasons: a.Reasons}
}

// Held records that the Card or the case held key for the owner, and
// frees the charge: an approval can take hours, and the facts are read
// again before sending.
func (g *Gate) Held(ctx context.Context, id Identity, key string) error {
	if _, err := g.Store.SetExecution(ctx, key, []string{ExecAllowed}, ExecHeld); err != nil {
		return err
	}
	return g.Store.UnlockCharge(ctx, id.TenantID, id.Account, id.Object, key)
}

// Sending records that key is about to leave. Only an allowed or held
// call moves; exactly one caller wins.
func (g *Gate) Sending(ctx context.Context, key string) (bool, error) {
	return g.Store.SetExecution(ctx, key, []string{ExecAllowed, ExecHeld}, ExecSending)
}

// After records how key's call ended and frees the charge. A call that
// was never sent releases its claim, so another taf can take the request;
// a sent or unknown one keeps it, and is watched.
func (g *Gate) After(ctx context.Context, id Identity, key, execution string) error {
	s := g.Store
	from := []string{ExecSending}
	switch execution {
	case ExecRefused, ExecExpired, ExecVoid, ExecNotSent:
		from = []string{ExecAllowed, ExecHeld, ExecSending}
	}
	if _, err := s.SetExecution(ctx, key, from, execution); err != nil {
		return err
	}
	switch execution {
	case ExecNotSent, ExecRefused, ExecExpired, ExecVoid:
		if id.Request != "" {
			if _, err := s.Release(ctx, id, key); err != nil {
				return err
			}
		}
	}
	return s.UnlockCharge(ctx, id.TenantID, id.Account, id.Object, key)
}

// Recover settles the adjustments of calls the gateway ended without
// telling thune: a gateway that died mid-call, whose call the sweep or a
// stale retry ended. It reads the gateway's own record of each call
// (gateway.invocations) and records in thune what After would have:
//   - failed (died while deciding): not sent; the claim is released.
//   - unknown (died while forwarding): unknown, and watched; never resent.
//   - refused: refused; the claim is released.
//   - forwarded: sent; not sent when the connection refused it before
//     sending anything; unknown when the tool answered with an error (the
//     no-effect statuses are the profile's, which only the live call
//     reads; unknown is the safe end, and the watch finds the truth).
//
// Only adjustments untouched for at least idle are taken, so a call still
// running inline settles itself. key limits it to one call; "" takes all.
// It returns how many it settled.
func (g *Gate) Recover(ctx context.Context, key string, idle time.Duration) (int, error) {
	s := g.Store
	rows, err := s.pool.Query(ctx, `
		SELECT a.key, i.state, COALESCE(i.tool_error, '') <> '', COALESCE(i.connection_refusal, '') <> ''
		  FROM gateway.thune_adjustments a JOIN gateway.invocations i ON i.key = a.key AND i.tenant_id = a.tenant_id
		 WHERE a.execution IN ('allowed','held','sending')
		   AND i.state IN ('failed','unknown','refused','forwarded')
		   AND a.updated_at <= $1 AND ($2 = '' OR a.key = $2)
		 ORDER BY a.key LIMIT 500`, s.now().Add(-idle), key)
	if err != nil {
		return 0, err
	}
	type ended struct {
		key, state       string
		toolErr, refusal bool
	}
	var todo []ended
	for rows.Next() {
		var e ended
		if err := rows.Scan(&e.key, &e.state, &e.toolErr, &e.refusal); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, e := range todo {
		a, err := s.Get(ctx, e.key)
		if err != nil {
			return n, err
		}
		var end string
		switch e.state {
		case "failed":
			end = ExecNotSent
		case "refused":
			end = ExecRefused
		case "unknown":
			end = ExecUnknown
		case "forwarded":
			end = ExecSent
			switch {
			case e.refusal:
				// The connection refused it before sending anything.
				end = ExecNotSent
			case e.toolErr:
				end = ExecUnknown
			}
		}
		if (end == ExecUnknown || end == ExecSent) && a.Execution != ExecSending {
			// The gateway's record says it left, and thune never saw it
			// sending: it may have; unknown, watched, never resent.
			end = ExecUnknown
		}
		from := []string{ExecAllowed, ExecHeld, ExecSending}
		if end == ExecSent {
			from = []string{ExecSending}
		}
		won, err := s.SetExecution(ctx, e.key, from, end)
		if err != nil {
			return n, err
		}
		if !won {
			continue
		}
		if (end == ExecNotSent || end == ExecRefused) && a.Identity.Request != "" {
			if _, err := s.Release(ctx, a.Identity, e.key); err != nil {
				return n, err
			}
		}
		if err := s.UnlockCharge(ctx, a.Identity.TenantID, a.Identity.Account, a.Identity.Object, e.key); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
