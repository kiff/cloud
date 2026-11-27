package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CardException is an action held for a Card's owner because it falls
// outside the Card (the mandates design amendment, "the work comes
// home").
type CardException struct {
	ExternalAnswer *ExternalAnswer `json:"external_answer,omitempty"`
	// NextSteps are, while held, the answers besides approve and reject
	// that the Card offers, e.g. return_first. Refusal is, once
	// rejected, the step and note the agent was given.
	NextSteps         []string             `json:"next_steps,omitempty"`
	Refusal           *Refusal             `json:"refusal,omitempty"`
	ID                string               `json:"id"`
	ProposalID        string               `json:"proposal_id"`
	Subject           string               `json:"subject"`
	Action            string               `json:"action"`
	EntityID          string               `json:"entity_id"`
	CanApproveOnce    *bool                `json:"can_approve_once"`
	OwnerAnswer       string               `json:"owner_answer"`
	RoomUnavailable   bool                 `json:"room_unavailable"`
	AccountingVersion int                  `json:"accounting_version"`
	Status            string               `json:"status"` // held, approved, rejected, changed, consumed, invalidated, expired
	Breaches          []CardBreach         `json:"breaches"`
	ReviewedBreaches  []CardBreach         `json:"reviewed_breaches,omitempty"`
	PriorApprovals    []CardApprovalRecord `json:"prior_approvals,omitempty"`
	Parameters        map[string]int64     `json:"parameters"`
	Reason            string               `json:"reason,omitempty"`
	CreatedAt         time.Time            `json:"created_at"`
	ResolvedAt        *time.Time           `json:"resolved_at,omitempty"`
	ResolvedBy        string               `json:"resolved_by,omitempty"`
	// ResolvedRole is the answerer's role on the account and
	// ResolvedUnderSoD whether separation of duties was on; absent
	// for answers given before roles existed.
	ResolvedRole     string `json:"resolved_role,omitempty"`
	ResolvedUnderSoD *bool  `json:"resolved_under_separation_of_duties,omitempty"`
	// Resolution is how it was answered: approve-once, reject, or
	// change-card (a Card edit that let it through, not an approval).
	Resolution string     `json:"resolution,omitempty"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`
	// ToolOutcome is how the call ended at the tool once the approval was
	// used; nil before, or without a gateway record.
	ToolOutcome *ToolOutcome `json:"tool_outcome,omitempty"`
	// Summary, AmountText and ReasonText are KIFF's own lines for the call
	// (what it is, its amount, why it's held), in the language asked for
	// (WithLanguage): a screen the apps open shows them as KIFF wrote them.
	Summary    string `json:"summary,omitempty"`
	AmountText string `json:"amount_text,omitempty"`
	ReasonText string `json:"reason_text,omitempty"`
	// Details are KIFF's own line for each argument ("charge: ch_…"), but
	// the amount and KIFF's operation id.
	Details []string `json:"details,omitempty"`
	// Arguments are what a held gateway call will be sent with, as the
	// agent sent them, each value as raw JSON so a number keeps every
	// digit. Only while held. An empty, non-nil map is a call with no
	// arguments. ArgumentsUnavailable: the call's arguments could not
	// be read, which is not the same as none.
	Arguments            map[string]json.RawMessage `json:"arguments,omitempty"`
	ArgumentsUnavailable bool                       `json:"arguments_unavailable,omitempty"`
	// ParameterUnits are the units the held action declares for its own
	// parameters, so its amount reads right even when only a count held
	// it. UnitsUnavailable: its domain could not be read.
	ParameterUnits   map[string]AmountUnit `json:"parameter_units,omitempty"`
	UnitsUnavailable bool                  `json:"units_unavailable,omitempty"`
	// ExpiresAt is when a held action stops waiting for an answer and is
	// refused.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Stored prior approvals remain separate from today's collectability.
type CardApprovalRecord struct {
	ExternalAnswer    *ExternalAnswer `json:"external_answer,omitempty"`
	By                string
	At                *time.Time
	Reason            string
	AccountingVersion int
}

// CardBreach is one Card the action is outside of, and how.
type CardBreach struct {
	MandateID string `json:"mandate_id"`
	Outcome   string `json:"outcome"` // per_action_exceeded | limit_reached | resource_not_on_card
	Quantity  string `json:"quantity"`
	Limit     int64  `json:"limit"`
	Used      int64  `json:"used"`
	Draw      int64  `json:"draw"`
	Reason    string `json:"reason"`
	// Resource and Value are, for resource_not_on_card, the parameter the
	// Card's scope names and the value the call sent ("" for none).
	Resource string `json:"resource,omitempty"`
	Value    string `json:"value,omitempty"`
	// Unit and Scale say what a summed amount means (EUR, 2 decimal
	// places), as the Card declares it; empty when it declares none, and
	// the numbers are then shown as before. UnitUnavailable: the Card or
	// its domain could not be read, so what the amount means is not
	// known and it must not be guessed.
	Unit            string `json:"unit,omitempty"`
	Scale           int    `json:"scale,omitempty"`
	UnitUnavailable bool   `json:"unit_unavailable,omitempty"`
	// UnitAmbiguous: the Card was read, but its actions do not agree on
	// what this amount means (USD on one, EUR on another), so its total
	// is in no one unit.
	UnitAmbiguous bool `json:"unit_ambiguous,omitempty"`
	// Window is, for limit_reached, the window of the total it is past
	// (calendar_day, rolling_24h, rolling_1h, rolling_7d, lifetime). The dashboard
	// reads it from the Card when the API does not send it; empty when the Card
	// could not say which.
	Window string `json:"window,omitempty"`
}

// AmountUnit is what an integer amount means: 2550 at scale 2 in EUR is
// 25.50 EUR.
type AmountUnit struct {
	Unit  string `json:"unit"`
	Scale int    `json:"scale"`
}

// Over is how far outside the Card the action is on this breach.
func (b CardBreach) Over() int64 {
	if b.Outcome == "resource_not_on_card" {
		return 0
	}
	if b.Outcome == "per_action_exceeded" {
		return b.Draw - b.Limit
	}
	return b.Used + b.Draw - b.Limit
}

// ApprovedOutside is a statement's record of authority the owner used
// outside the Card.
type ApprovedOutside struct {
	Entries []ApprovedOutsideEntry `json:"entries"`
	Totals  map[string]int64       `json:"totals"`
}

type ApprovedOutsideEntry struct {
	ExceptionID string    `json:"exception_id"`
	ProposalID  string    `json:"proposal_id"`
	Quantity    string    `json:"quantity"`
	Amount      int64     `json:"amount"`
	RecordedAt  time.Time `json:"recorded_at"`
	Action      string    `json:"action"`
	EntityID    string    `json:"entity_id"`
}

// ErrExceptionConflict is a 409: the exception was already answered, or
// its Card changed since the page was loaded.
var ErrExceptionConflict = errors.New("apiclient: the exception was already answered or its card changed")

// ListCardExceptionsBearer lists the tenant's exceptions, newest first;
// status "" lists all of them.
func (c *Client) ListCardExceptionsBearer(ctx context.Context, bearer, status string) ([]CardException, error) {
	path := "/v1/me/card-exceptions"
	if status != "" {
		path += "?status=" + url.QueryEscape(status)
	}
	body, err := c.do(ctx, bearer, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Exceptions []CardException `json:"exceptions"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out.Exceptions, nil
}

// CardExceptionBearer returns one exception.
func (c *Client) CardExceptionBearer(ctx context.Context, bearer, id string) (*CardException, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/me/card-exceptions/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	var e CardException
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// Refusal is what a rejection told the agent to do instead.
type Refusal struct {
	NextStep string `json:"next_step,omitempty"`
	Note     string `json:"note,omitempty"`
}

// AnswerCardExceptionBearer gives the owner's answer: "approve-once",
// "reject" or "change-card".
func (c *Client) AnswerCardExceptionBearer(ctx context.Context, bearer, id, answer string) (*CardException, error) {
	return c.answerCardException(ctx, bearer, id, answer, nil)
}

// RejectCardExceptionBearer refuses a held action and tells the agent what
// to do instead: one of the exception's NextSteps, and an optional note.
func (c *Client) RejectCardExceptionBearer(ctx context.Context, bearer, id string, r Refusal) (*CardException, error) {
	if r == (Refusal{}) {
		return c.answerCardException(ctx, bearer, id, "reject", nil)
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	e, err := c.answerCardException(ctx, bearer, id, "reject", b)
	if err != nil && strings.Contains(err.Error(), "unexpected status 400") {
		return nil, fmt.Errorf("%w: %v", ErrRefusalNotAccepted, err)
	}
	return e, err
}

// ErrRefusalNotAccepted is a rejection the API refused as given: a next
// step the Card does not offer, or a note that is too long. The action
// stays held.
var ErrRefusalNotAccepted = errors.New("apiclient: the next step or note was not accepted")

func (c *Client) answerCardException(ctx context.Context, bearer, id, answer string, payload []byte) (*CardException, error) {
	switch answer {
	case "approve-once", "reject", "change-card":
	default:
		return nil, errors.New("apiclient: unknown answer " + answer)
	}
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	body, err := c.do(ctx, bearer, http.MethodPost, "/v1/me/card-exceptions/"+url.PathEscape(id)+"/"+answer, reader)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			return nil, ErrExceptionConflict
		}
		return nil, err
	}
	var e CardException
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// ToolOutcome is how a call KIFF authorized ended at the tool, as the
// gateway recorded it. It sits next to the decision: authorized is not
// done. Status: done, refused_by_tool, unknown, not_sent or sending.
type ToolOutcome struct {
	Status  string    `json:"status"`
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at"`
	// RefusedBy is "connection" for a call its connection refused before
	// sending it, and Code is that refusal's code. The API leaves Message
	// empty then; a page never quotes a connection's own text.
	RefusedBy string `json:"refused_by,omitempty"`
	Code      string `json:"code,omitempty"`
}

// ByConnection says the connection refused the call before sending it, so
// nothing reached the tool.
func (o *ToolOutcome) ByConnection() bool {
	return o != nil && o.Status == "refused_by_tool" && o.RefusedBy == "connection"
}

func (e CardException) MayApproveOnce() bool {
	if e.Status != "held" || e.RoomUnavailable {
		return false
	}
	if e.CanApproveOnce != nil {
		return *e.CanApproveOnce
	}
	for _, b := range e.Breaches {
		if b.Outcome != "per_action_exceeded" && b.Outcome != "resource_not_on_card" {
			return false
		}
	}
	return true
}
func (e CardException) HasCumulativeBreach() bool {
	for _, b := range e.Breaches {
		if b.Outcome == "limit_reached" {
			return true
		}
	}
	return false
}
func (e CardException) NeedsOwner() bool {
	return e.Status == "held" || e.Status == "approved_no_room" || e.Status == "unavailable"
}
