package mandate

import (
	"context"
	"errors"
	"strings"
	"time"
)

// EffectSurcharge is append-only: what a vendor used beyond what its Card
// decided, as KIFF read it back after the write (connection profiles: the verify read).
// The Card then counts the larger figure. The original draw stays as it
// was; a vendor that used less changes nothing, and the Card keeps what it
// counted.
type EffectSurcharge struct {
	TenantID, MandateID, ProposalID, QuantityKey string
	Amount                                       int64
	At                                           time.Time
	Source, EvidenceID                           string
}

// VerifiedEvidence is the gateway's verify read of one forwarded call, read
// from what the gateway recorded, never from the agent's word.
type VerifiedEvidence struct {
	TenantID, ProposalID, Subject, Action string
	// AmountArg is the Card parameter the amount is decided under.
	AmountArg       string
	Decided, Vendor int64
	At              time.Time
	Source          string
}

func (e VerifiedEvidence) validate() error {
	if e.TenantID == "" || e.ProposalID == "" || e.Subject == "" || e.Action == "" || e.AmountArg == "" ||
		e.Source == "" || e.At.IsZero() || e.Decided < 0 || e.Vendor <= e.Decided {
		return errors.New("mandate: incomplete verify evidence, or a vendor amount that isn't larger")
	}
	return nil
}

// EffectSurchargeStore records surcharges.
type EffectSurchargeStore interface {
	SurchargeEffects(context.Context, VerifiedEvidence) ([]EffectSurcharge, error)
}

// eligibleSurcharge is the surcharge e puts on draw d: only the draw of
// the decided amount itself, sum(<amount argument>) of exactly what was
// decided, by the same agent and action, after it was drawn.
func eligibleSurcharge(d Draw, e VerifiedEvidence) (EffectSurcharge, bool, error) {
	if d.QuantityKey != "sum("+e.AmountArg+")" || d.Amount != e.Decided {
		return EffectSurcharge{}, false, nil
	}
	if d.Subject != e.Subject || !strings.EqualFold(d.Action, e.Action) {
		return EffectSurcharge{}, false, errors.New("mandate: verify evidence does not match the authorized action")
	}
	if e.At.Before(d.DrawnAt) {
		return EffectSurcharge{}, false, errors.New("mandate: verify evidence predates authorization")
	}
	return EffectSurcharge{TenantID: d.TenantID, MandateID: d.MandateID, ProposalID: d.ProposalID, QuantityKey: d.QuantityKey,
		Amount: e.Vendor - e.Decided, At: e.At, Source: e.Source, EvidenceID: e.ProposalID}, true, nil
}
