package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// HoldGate applies the governance-roles design's rules to answering a hold, on top of the
// rules each answer path already had (an agent's key never answers, a
// key never answers what it asked for):
//
//   - M10: no person answers a hold on an action they proposed.
//   - The person whose run asked for the call (its AskedBy) doesn't
//     approve it with separation of duties on. Off, as on an account of
//     one, they answer their own run's holds, as they did before run
//     grants. Refusing grants nothing, so they may always refuse.
//   - M11, with the tenant's separation of duties on: only approvers
//     answer; no API key answers, whatever its roles; and no one who
//     issued or changed a Card answers holds drawn against it, whatever
//     their role is now.
//
// Nil (or without a membership store) the gate allows everything,
// which keeps deploys without memberships as they were.
type HoldGate struct {
	Memberships membership.Store
	// Revisions reads a Card's issuance history (mandate.Store). Nil:
	// the changer rule cannot be checked, so with separation of duties
	// on, answers on Card holds are refused rather than guessed.
	Revisions func(ctx context.Context, tenantID, mandateID string) ([]mandate.Revision, error)
	// OnRecordError is told when an answer's attribution could not be
	// written (production raises an operator alert). The answer stands
	// either way; the statement would lack its role and policy.
	OnRecordError func(ctx context.Context, tenantID, holdID string, err error)
}

var (
	errHoldKeyUnderSoD = errors.New("hold: key under separation of duties")
	errHoldRole        = errors.New("hold: role cannot answer")
	errHoldSelf        = errors.New("hold: proposer cannot answer")
	errHoldCardChanger = errors.New("hold: card changer cannot answer")
	errHoldUnavailable = errors.New("hold: cannot be authorized right now")
)

// Hold is what the gate needs to know about the hold being answered.
type Hold struct {
	// Proposers are the principals the hold records as having asked
	// for the action (an actor id, a subject, a key id).
	Proposers []string
	// MandateIDs are the Cards the hold was drawn against; empty for a
	// runtime approval.
	MandateIDs []string
	// AskedBy is the person whose run asked for the call, by subject;
	// empty for a call no person's run made.
	AskedBy string
	// Refusal marks a refusal: it grants nothing, so AskedBy may give it.
	Refusal bool
}

// Check returns the tenant's policy and nil when id may answer, or one
// of the gate's errors. cardEdit marks change-card: a Card edit reached
// from a hold, which needs Card authority (the route policy) rather
// than hold authority, so only the proposer rule applies to it.
func (g *HoldGate) Check(ctx context.Context, id auth.Identity, tenantID string, h Hold, cardEdit bool) (membership.Governance, error) {
	if g == nil || g.Memberships == nil {
		return membership.Governance{}, nil
	}
	gov, err := g.Memberships.Governance(ctx, tenantID)
	if err != nil {
		return membership.Governance{}, errHoldUnavailable
	}
	if id.IsPerson() {
		self := func(p string) bool {
			return p != "" && (p == id.Subject || p == "actor-"+id.Subject || p == id.ActorID())
		}
		for _, p := range h.Proposers {
			if self(p) {
				return gov, errHoldSelf
			}
		}
		if gov.SeparationOfDuties && !h.Refusal && self(h.AskedBy) {
			return gov, errHoldSelf
		}
	}
	if cardEdit {
		return gov, nil
	}
	if id.APIKey || id.MemberRole == "" {
		if gov.SeparationOfDuties {
			return gov, errHoldKeyUnderSoD
		}
		return gov, nil
	}
	if !membership.AnswersHolds(id.MemberRole, gov.SeparationOfDuties) {
		return gov, errHoldRole
	}
	if gov.SeparationOfDuties && len(h.MandateIDs) > 0 {
		if g.Revisions == nil {
			return gov, errHoldUnavailable
		}
		for _, mid := range h.MandateIDs {
			revs, err := g.Revisions(ctx, tenantID, mid)
			if err != nil {
				return gov, errHoldUnavailable
			}
			for _, rev := range revs {
				if rev.By != "" && (rev.By == id.Subject || (id.Email != "" && rev.By == id.Email)) {
					return gov, errHoldCardChanger
				}
			}
		}
	}
	return gov, nil
}

// Record writes who answered, with their role and the policy at the
// time. Best effort: the answer stands whether or not the attribution
// row is written, and a failure is the caller's to log.
func (g *HoldGate) Record(ctx context.Context, id auth.Identity, tenantID, holdID, answer string, gov membership.Governance, answeredAt ...time.Time) error {
	var at time.Time
	if len(answeredAt) > 0 {
		at = answeredAt[0]
	}
	return g.RecordSigned(ctx, id, tenantID, holdID, answer, gov, at, nil)
}

// RecordSigned is Record with the phone's signed proof of the answer,
// which the settled receipt carries; nil without one.
func (g *HoldGate) RecordSigned(ctx context.Context, id auth.Identity, tenantID, holdID, answer string, gov membership.Governance, at time.Time, device *membership.DeviceProof) error {
	if g == nil || g.Memberships == nil {
		return nil
	}
	err := g.Memberships.RecordHoldAnswer(ctx, membership.HoldAnswer{
		TenantID: tenantID, HoldID: holdID, Answer: answer, Answerer: id.ActorID(),
		Role: id.MemberRole, SeparationOfDuties: gov.SeparationOfDuties, At: at, Device: device,
	})
	if err != nil && g.OnRecordError != nil {
		g.OnRecordError(ctx, tenantID, holdID, err)
	}
	return err
}

// writeHoldGateError renders the gate's refusals. It reports false for
// an error the gate did not produce.
func writeHoldGateError(w http.ResponseWriter, err error) bool {
	var status int
	var code, msg string
	switch {
	case errors.Is(err, errHoldKeyUnderSoD):
		status, code, msg = http.StatusForbidden, "separation_of_duties_key",
			"separation of duties is on, so held actions are answered by an approver from a session, never by an API key"
	case errors.Is(err, errHoldRole):
		status, code, msg = http.StatusForbidden, "separation_of_duties_role",
			"separation of duties is on, so only an approver answers held actions; admins and editors change Cards instead"
	case errors.Is(err, errHoldSelf):
		status, code, msg = http.StatusForbidden, "self_review",
			"you proposed this action, so someone else on the account answers it"
	case errors.Is(err, errHoldCardChanger):
		status, code, msg = http.StatusForbidden, "card_changer",
			"you issued or changed a Card this action was held against, so with separation of duties on another approver answers it"
	case errors.Is(err, errHoldUnavailable):
		status, code, msg = http.StatusServiceUnavailable, "hold_check_unavailable",
			"whether you may answer this could not be checked right now; try again"
	default:
		return false
	}
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
	return true
}
