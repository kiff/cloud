package handlers

import (
	"context"
	"sort"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/proposals"
)

// CallsAskedBy lists the calls one person's runs made, by proposal id; the
// meter, which records each call's verified requester, is one.
type CallsAskedBy interface {
	KeysAskedBy(ctx context.Context, tenantID, subject string) (map[string]bool, error)
}

// OwnUse decides what of the account's record a person reads (the shared-tafs design
// decision 12): sharing a taf lets someone use it, not see the account. An
// admin, or a person on an account from before roles, reads all of it.
// Anyone else reads every call and hold of the agents they manage (KIFF's
// permission answer), the calls their own runs made, and the holds they
// may answer. KIFF decides it here; the clients filter nothing.
type OwnUse struct {
	access *AccessHandler
	asked  CallsAskedBy
}

// NewOwnUse wires it. With either piece missing, every person reads the
// whole record, as before decision 12.
func NewOwnUse(a *AccessHandler, asked CallsAskedBy) *OwnUse {
	if a == nil || asked == nil {
		return nil
	}
	return &OwnUse{access: a, asked: asked}
}

// ReadScope is one person's part of the record. A nil *ReadScope is the
// whole account.
type ReadScope struct {
	subject string
	answers map[string]access.Answer
	mine    map[string]bool
	// answersHolds: their role may answer held calls (holds.answer), so they
	// see the holds of the agents they use, which is the approver's job.
	answersHolds bool
}

// Scope is id's part of tenantID's record; nil when they read all of it.
// An error reads nothing: the caller fails closed.
func (o *OwnUse) Scope(ctx context.Context, id auth.Identity, tenantID string) (*ReadScope, error) {
	if o == nil || id.MemberRole == "" || id.MemberRole == membership.RoleAdmin {
		return nil, nil
	}
	answers, err := o.access.answersFor(ctx, tenantID, id.Subject)
	if err != nil {
		return nil, err
	}
	mine, err := o.asked.KeysAskedBy(ctx, tenantID, id.Subject)
	if err != nil {
		return nil, err
	}
	return &ReadScope{
		subject:      id.Subject,
		answers:      answers,
		mine:         mine,
		answersHolds: membership.Can(id.MemberRole, membership.CapHoldsAnswer),
	}, nil
}

// managed are the agents whose whole record the person reads.
func (s *ReadScope) managed() []string {
	var out []string
	for agent, a := range s.answers {
		if a.Manage {
			out = append(out, agent)
		}
	}
	sort.Strings(out)
	return out
}

// Calls is the calls the person reads: nil for all.
func (s *ReadScope) Calls() *proposals.CallScope {
	if s == nil {
		return nil
	}
	agents := map[string]bool{}
	for _, a := range s.managed() {
		agents[a] = true
	}
	return &proposals.CallScope{Agents: agents, Mine: s.mine}
}

// Tasks is the counted tasks the person reads: nil for all.
func (s *ReadScope) Tasks() *meter.TaskScope {
	if s == nil {
		return nil
	}
	return &meter.TaskScope{Subject: s.subject, Agents: append([]string{}, s.managed()...)}
}

// Hold reports whether the person reads a hold of agent that askedBy's run
// made ("" when no person's run made it): the agent is theirs to manage,
// the run was theirs, or they may answer holds on an agent they use.
func (s *ReadScope) Hold(agent, askedBy string) bool {
	if s == nil {
		return true
	}
	a := s.answers[agent]
	return a.Manage || (askedBy != "" && askedBy == s.subject) || (s.answersHolds && a.Use)
}

// Manages: they may change agent (its Cards, versions, sharing), so a
// question about its call is theirs to take on.
func (s *ReadScope) Manages(agent string) bool {
	return s == nil || s.answers[agent].Manage
}

// Whole reports whether the person reads the whole account's record.
func (s *ReadScope) Whole() bool { return s == nil }

// WithMandatesOwnUse keeps each person's held calls to their part of the
// account. Nil: everyone reads all of them.
func WithMandatesOwnUse(o *OwnUse) MandatesOption {
	return func(h *MandatesHandler) { h.ownUse = o }
}

// WithOwnUse keeps each person's counted tasks to their part of the
// account. Nil: everyone reads all of them.
func (h *MeterUsageHandler) WithOwnUse(o *OwnUse) *MeterUsageHandler {
	h.ownUse = o
	return h
}
