package handoff

import (
	"context"
	"log/slog"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/receipt"
)

// ReceiptSaver saves a receipt (receipt.Store's Save).
type ReceiptSaver interface {
	Save(ctx context.Context, r receipt.Receipt) error
}

// maxPerSweep bounds one sweep's expiries and receipts; the next tick takes
// the rest.
const maxPerSweep = 200

// Sweeper closes what waited past its expiry and saves the receipts each
// hand-off owes: the raised one, and the settled one once it closed.
type Sweeper struct {
	Store    Store
	Receipts ReceiptSaver
	Alerts   alert.Notifier
	Logger   *slog.Logger
}

// Run sweeps every tick until ctx ends.
func (s *Sweeper) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.Sweep(ctx, time.Now().UTC())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Sweep is one pass as of now.
func (s *Sweeper) Sweep(ctx context.Context, now time.Time) {
	expired, err := s.Store.Expire(ctx, now, maxPerSweep)
	if err != nil {
		s.Logger.Warn("handoffs: expiry sweep failed", slog.String("err", err.Error()))
	}
	for _, h := range expired {
		Closed(s.Alerts, h, now)
	}
	owed, err := s.Store.Unreceipted(ctx, now, maxPerSweep)
	if err != nil {
		s.Logger.Warn("handoffs: receipt sweep failed", slog.String("err", err.Error()))
		return
	}
	for _, h := range owed {
		if err := s.Receipt(ctx, h, now); err != nil {
			s.Logger.Warn("handoffs: receipt not saved", slog.String("tenant", h.TenantID), slog.String("handoff", h.ID), slog.String("err", err.Error()))
		}
	}
}

// Receipt saves what h owes as of now: its raised receipt if it has none,
// and its settled one once it closed.
func (s *Sweeper) Receipt(ctx context.Context, h Handoff, now time.Time) error {
	if s.Receipts == nil {
		return nil
	}
	in, err := s.receiptInput(ctx, h)
	if err != nil {
		return err
	}
	if !h.RaisedReceipt {
		r, err := receipt.BuildHandoffRaised(in)
		if err != nil {
			return err
		}
		if err := s.Receipts.Save(ctx, r); err != nil {
			return err
		}
		if err := s.Store.Receipted(ctx, h.TenantID, h.ID, nil); err != nil {
			return err
		}
	}
	if h.SettledAt != nil || !h.Closed(now) {
		return nil
	}
	r, err := receipt.BuildHandoffSettled(in)
	if err != nil {
		return err
	}
	if err := s.Receipts.Save(ctx, r); err != nil {
		return err
	}
	return s.Store.Receipted(ctx, h.TenantID, h.ID, &now)
}

// receiptInput is h as its receipts carry it, with the gateway's record of
// the call that read the vendor after "Done".
func (s *Sweeper) receiptInput(ctx context.Context, h Handoff) (receipt.Handoff, error) {
	in := receipt.Handoff{ID: h.ID, TenantID: h.TenantID, Agent: h.Agent, Status: string(h.Status), LinkHost: h.LinkHost,
		WordsHash: h.WordsHash(), PreparedBy: h.PreparedBy, Version: h.Version, CreatedAt: h.CreatedAt, ExpiresAt: h.ExpiresAt,
		AnsweredBy: h.AnsweredBy, AnsweredByID: h.AnsweredByID, AnsweredAt: h.AnsweredAt, CheckedBy: h.CheckedBy}
	if d := h.Device; d != nil {
		in.Device = &receipt.SettlementDevice{Name: d.Name, KeyID: d.KeyID, PublicKey: d.PublicKey, Message: d.Message, Signature: d.Signature}
	}
	if h.CheckedBy != "" {
		calls, err := s.Store.Calls(ctx, h.TenantID, []string{h.CheckedBy})
		if err != nil {
			return receipt.Handoff{}, err
		}
		if c, ok := calls[h.CheckedBy]; ok {
			status := c.ToolOutcome
			if status == "" {
				status = "not_recorded"
			}
			at := c.CreatedAt
			if h.CheckedAt != nil {
				at = *h.CheckedAt
			}
			in.Check = &receipt.SettlementTool{Status: status, At: at}
		}
	}
	return in, nil
}

// Closed tells the alert targets h closed, so the agent's runtime continues
// the run waiting on it.
func Closed(n alert.Notifier, h Handoff, now time.Time) {
	if n == nil {
		return
	}
	at := now
	if h.AnsweredAt != nil {
		at = *h.AnsweredAt
	}
	n.Notify(alert.Event{TenantID: h.TenantID, ProposalID: h.ID, Action: receipt.HandoffAction, EntityID: h.Agent, EntityType: "agent",
		Outcome: alert.HandoffAnswered, DecidedAt: at, Handoff: &alert.Handoff{ID: h.ID, Agent: h.Agent, Status: string(h.Status)}})
}
