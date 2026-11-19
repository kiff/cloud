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

// Mandate is a bounded grant of authority to one or more agents, its
// holders (the mandates design and its amendment, "a Card can be
// shared").
type Mandate struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	// Subjects is every holder, Subject first; empty on an API that
	// predates shared Cards, meaning Subject alone.
	Subjects    []string           `json:"subjects,omitempty"`
	Domain      string             `json:"domain"`
	Environment string             `json:"environment"`
	Grants      []MandateGrant     `json:"grants"`
	Aggregates  []MandateAggregate `json:"aggregates"`
	Location    string             `json:"location,omitempty"`
	Active      bool               `json:"active"`
	ValidFrom   *time.Time         `json:"valid_from,omitempty"`
	ValidUntil  *time.Time         `json:"valid_until,omitempty"`
	RevokedAt   *time.Time         `json:"revoked_at,omitempty"`
	ArchivedAt  *time.Time         `json:"archived_at,omitempty"`
	// OnExceed is what happens to work outside the Card: "owner" (it
	// comes home to the owner) or "refuse".
	OnExceed string `json:"on_exceed,omitempty"`
	// HoldExpirySeconds is how long work held for the owner waits for an
	// answer.
	HoldExpirySeconds int64 `json:"hold_expiry_seconds,omitempty"`
}

// ComesHome reports whether work outside this Card is held for its
// owner rather than refused.
func (m Mandate) ComesHome() bool { return m.OnExceed == "owner" }

type MandateGrant struct {
	Action       string   `json:"action"`
	EntityStates []string `json:"entity_states"`
	// PerActionMax caps a single action under this grant ("up to €500
	// each"), whatever the Card's totals have left.
	PerActionMax []MandatePerAction `json:"per_action_max,omitempty"`
	// Resources limits the grant to named resources: refund_order only
	// for order_id ord_123. Carried on every reissue, so an edit that
	// does not show it cannot drop it.
	Resources *MandateResources `json:"resources,omitempty"`
	// NextSteps are the answers besides approve and reject the owner may
	// give when a call under this grant is held. Carried on every reissue
	// like Resources: an edit that does not show them keeps them.
	NextSteps []string `json:"next_steps,omitempty"`
}

// MandateResources is a grant's resource scope.
type MandateResources struct {
	Parameter string   `json:"parameter"`
	Values    []string `json:"values"`
}

// MandatePerAction is the most one action may carry of a summed
// parameter, e.g. {Quantity: "sum(amount_cents)", Max: 50000}.
type MandatePerAction struct {
	Quantity string `json:"quantity"`
	Max      int64  `json:"max"`
}

type MandateAggregate struct {
	Measure    string      `json:"measure,omitempty"`
	RoomGrants []RoomGrant `json:"room_grants,omitempty"`
	Quantity   string      `json:"quantity"`
	Limit      int64       `json:"limit"`
	Window     string      `json:"window"`
	// Per names the argument a per-item total counts each value of apart.
	Per string `json:"per,omitempty"`
}

// MandateLimit is one cap and what has been drawn against it in the
// current window.
//
// Status is "ok" or "unavailable". Unavailable means the usage could
// not be read — deliberately not reported as zero, because a zero reads
// as an unused limit, which is the most dangerous thing this surface
// could imply.
type MandateLimit struct {
	Forecast        *CardForecast `json:"forecast"`
	Unit            string        `json:"unit"`
	Scale           int           `json:"scale"`
	Measure         string        `json:"measure"`
	BaseLimit       int64         `json:"base_limit"`
	ExtraRoom       int64         `json:"extra_room"`
	RoomWindowEnd   time.Time     `json:"room_window_end"`
	RoomGrants      []RoomGrant   `json:"room_grants"`
	GrossAuthorized int64         `json:"gross_authorized"`
	Released        int64         `json:"released"`
	NetCounted      int64         `json:"net_counted"`
	Quantity        string        `json:"quantity"`
	Limit           int64         `json:"limit"`
	Window          string        `json:"window"`
	Used            int64         `json:"used"`
	Remaining       int64         `json:"remaining"`
	WindowStarted   *time.Time    `json:"window_started,omitempty"`
	Status          string        `json:"status"`
	// Per is, with status "per_item", the argument the total counts each
	// value of apart: it has one balance per item, so no used or left.
	Per string `json:"per,omitempty"`
}

// Unavailable reports whether this limit's usage could not be read. A
// per-item total isn't unreadable: it has no single balance to read.
func (l MandateLimit) Unavailable() bool { return l.Status != "ok" && !l.PerItem() }

// PerItem reports whether this is a total for each item apart ("1 a week
// for each invoice"): its limit is shown, never a used or left.
func (l MandateLimit) PerItem() bool { return l.Status == "per_item" }

// PercentUsed is the share of the limit consumed, clamped to 0..100 so
// an over-drawn limit renders as full rather than overflowing its bar.
func (l MandateLimit) PercentUsed() int {
	if l.Limit <= 0 || l.Unavailable() {
		return 0
	}
	p := int(l.Used * 100 / l.Limit)
	if p > 100 {
		return 100
	}
	if p < 0 {
		return 0
	}
	return p
}

// MandateDraw is one consumption of authority.
type MandateDraw struct {
	ProposalID string    `json:"proposal_id"`
	Quantity   string    `json:"quantity"`
	Amount     int64     `json:"amount"`
	DrawnAt    time.Time `json:"drawn_at"`

	// Action and EntityID are what the movement was for. Empty on
	// draws recorded before the ledger carried them, which renders as
	// an unnamed movement rather than an invented one.
	Action   string `json:"action,omitempty"`
	EntityID string `json:"entity_id,omitempty"`
	// DrawnTotals is, per limit, how much of that limit's window had
	// been drawn once this movement landed.
	//
	// One per limit, because a quantity can be capped over more than
	// one window at once — five an hour and twenty a day are both true
	// of the same movement. Consumed-by-then, never a remaining: a
	// remaining would need the ceiling in force at that instant, and a
	// card raised later would make an old row claim room that did not
	// exist. What is left *now* is on the limit lines, the only place
	// it is true. Empty when the card's terms at that instant are not
	// known, which renders as nothing rather than as zero.
	DrawnTotals []MandateDrawTotal `json:"drawn_totals,omitempty"`
	// Effect is always "authorized": KIFF decided, the caller's own
	// system ran the action afterwards and does not report back.
	Effect string `json:"effect,omitempty"`
}

// MandateDrawTotal is one limit's subtotal for a movement.
type MandateDrawTotal struct {
	Window     string `json:"window"`
	DrawnTotal int64  `json:"drawn_total"`
}

// What names the movement in a way a reader recognises: the action on
// the entity it touched. Falls back to the proposal id only when the
// draw predates the ledger carrying either.
func (d MandateDraw) What() string {
	switch {
	case d.Action != "" && d.EntityID != "":
		return d.Action + " on " + d.EntityID
	case d.Action != "":
		return d.Action
	default:
		return "unnamed movement"
	}
}

// MandateStatement is the corporate-card statement for a machine.
type MandateStatement struct {
	Warnings           []CardWarning    `json:"warnings"`
	WarningsError      string           `json:"warnings_error"`
	ApprovedExceptions *ApprovedOutside `json:"approved_exceptions"`
	EffectReleases     []EffectRelease  `json:"effect_releases"`
	ReleasesError      string           `json:"releases_error"`
	Mandate            Mandate          `json:"mandate"`
	Limits             []MandateLimit   `json:"limits"`
	Draws              []MandateDraw    `json:"draws"`
	// Note states that a draw is recorded at authorization rather than
	// at execution. It is served by the API rather than written in the
	// template so the caveat cannot drift away from the number.
	Note string `json:"note"`

	// Revisions is every issuance of this card, oldest first. Changing
	// a ceiling means re-issuing the id, which overwrites the row, so
	// this is the only record of what the authority used to be and who
	// changed it.
	Revisions []MandateRevision `json:"revisions"`
	// ApprovedOutside is what the owner approved once outside this Card:
	// it did not draw on the balance, and is totalled separately.
	ApprovedOutside      *ApprovedOutside `json:"approved_outside,omitempty"`
	ApprovedOutsideError string           `json:"approved_outside_error,omitempty"`
	// MovementsError is set when the movement ledger could not be
	// read. Distinct from an empty window: "nothing drew on this card"
	// is a claim about the agent, and an unreadable ledger is a fact
	// about us.
	MovementsError string `json:"movements_error,omitempty"`
	// RevisionsError is set when the history could not be read. The
	// history being unknown and the card never having changed are
	// different facts and must not render the same way.
	RevisionsError string `json:"revisions_error,omitempty"`
}

// MandateRevision is the card as it stood after one issuance.
type MandateRevision struct {
	Revision  int64     `json:"revision"`
	ChangedAt time.Time `json:"changed_at"`
	ChangedBy string    `json:"changed_by"`
	// Subject is who held the Card after this issuance; empty on
	// revisions recorded before holders were.
	Subject string `json:"subject,omitempty"`
	// Grants are the Card's grants after this issuance, with their
	// per-action limits, so a change to one shows between two rows.
	Grants     []MandateGrant     `json:"grants,omitempty"`
	Aggregates []MandateAggregate `json:"aggregates"`
	ValidUntil *time.Time         `json:"valid_until,omitempty"`
}

// Ceiling renders the revision's ceilings as one line, for a history
// row that has to be readable beside the one above it.
func (r MandateRevision) Ceiling() string {
	if len(r.Aggregates) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(r.Aggregates))
	for _, a := range r.Aggregates {
		parts = append(parts, fmt.Sprintf("%d %s", a.Limit, a.Quantity))
	}
	return strings.Join(parts, ", ")
}

// By names who made the change, or says plainly that it is not known.
// An unattributed change is not an anonymous one by choice.
func (r MandateRevision) By() string {
	if s := strings.TrimSpace(r.ChangedBy); s != "" {
		return s
	}
	return "not recorded"
}

// ListMandatesBearer returns the tenant's mandates, in force or not.
func (c *Client) ListMandatesBearer(ctx context.Context, bearer string) ([]Mandate, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/me/mandates", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Mandates []Mandate `json:"mandates"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apiclient: decode mandates: %w", err)
	}
	return resp.Mandates, nil
}

// MandateStatementBearer returns one mandate's statement.
func (c *Client) MandateStatementBearer(ctx context.Context, bearer, mandateID string) (*MandateStatement, error) {
	body, err := c.do(ctx, bearer, http.MethodGet,
		"/v1/me/mandates/"+url.PathEscape(mandateID)+"/statement", nil)
	if err != nil {
		return nil, err
	}
	var st MandateStatement
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("apiclient: decode statement: %w", err)
	}
	return &st, nil
}

// MandateRejectedError is a mandate the cloud refused to issue.
//
// The refusals are the interesting part of this endpoint — a mandate
// summing a parameter no action declares numeric, or one that widens
// what a contract allows — and each carries a sentence saying what is
// wrong. That sentence is worth more to the person at the form than any
// wording a template could invent, so it is carried out intact rather
// than flattened into "could not issue limit".
type MandateRejectedError struct {
	StatusCode int
	Message    string
}

func (e *MandateRejectedError) Error() string { return e.Message }

// IssueMandateRequest is the body of POST /v1/me/mandates.
//
// ID is chosen by the caller and re-issuing an existing one updates it
// in place, which is how a limit is raised. Draws already recorded
// against that id survive, so raising a limit grants the remaining
// difference rather than a fresh window.
type IssueMandateRequest struct {
	ID          string             `json:"id"`
	Subject     string             `json:"subject"`
	Domain      string             `json:"domain,omitempty"`
	Environment string             `json:"environment,omitempty"`
	Grants      []MandateGrant     `json:"grants"`
	Aggregates  []MandateAggregate `json:"aggregates"`
	// ValidFrom is when the card starts. Omitted means no start date, so
	// it is in force at once; a reissue that must not change when
	// authority begins sends the Card's own.
	ValidFrom *time.Time `json:"valid_from,omitempty"`
	// ValidUntil is when the card stops. Omitted means it holds until
	// someone revokes it, which the api treats as an open end.
	ValidUntil *time.Time `json:"valid_until,omitempty"`
	// OnExceed is "owner" or "refuse". Empty lets the API decide: a new
	// Card comes home to its owner, a reissue keeps the Card's policy.
	OnExceed string `json:"on_exceed,omitempty"`
	// HoldExpiry is how long work held for the owner waits ("10m" to
	// "168h"). Empty keeps the Card's current one, 10 minutes when new.
	HoldExpiry string `json:"hold_expiry,omitempty"`
}

// IssueMandateBearer creates or updates a mandate.
//
// A 4xx comes back as *MandateRejectedError carrying the cloud's own
// explanation; anything else is the usual transport error.
func (c *Client) IssueMandateBearer(ctx context.Context, bearer string, req IssueMandateRequest) (*Mandate, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: encode mandate: %w", err)
	}
	resp, err := c.doRaw(ctx, bearer, http.MethodPost, "/v1/me/mandates", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		var e struct {
			Error string `json:"error"`
		}
		var msg string
		if err := json.Unmarshal(body, &e); err == nil {
			msg = strings.TrimSpace(e.Error)
		}
		if msg == "" {
			msg = fmt.Sprintf("the limit was refused (status %d)", resp.StatusCode)
		}
		return nil, &MandateRejectedError{StatusCode: resp.StatusCode, Message: msg}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("apiclient: POST /v1/me/mandates unexpected status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var m Mandate
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("apiclient: decode mandate: %w", err)
	}
	return &m, nil
}

// RevokeMandateBearer ends a mandate's authority now.
//
// Revocation is not reversible for that id: re-issuing the same id
// updates the grant but cannot clear the revocation, by design. More
// authority for the same subject means a new mandate.
func (c *Client) RevokeMandateBearer(ctx context.Context, bearer, mandateID string) error {
	_, err := c.do(ctx, bearer, http.MethodPost,
		"/v1/me/mandates/"+url.PathEscape(mandateID)+"/revoke", nil)
	return err
}

// MoveMandateBearer gives a Card to another holder, keeping its id,
// terms, usage and statement.
func (c *Client) MoveMandateBearer(ctx context.Context, bearer, mandateID, subject string) error {
	body, err := json.Marshal(map[string]string{"subject": subject})
	if err != nil {
		return err
	}
	_, err = c.do(ctx, bearer, http.MethodPost,
		"/v1/me/mandates/"+url.PathEscape(mandateID)+"/move", bytes.NewReader(body))
	return err
}

func (c *Client) ArchiveMandateBearer(ctx context.Context, bearer, mandateID string) error {
	_, err := c.do(ctx, bearer, http.MethodPost,
		"/v1/me/mandates/"+url.PathEscape(mandateID)+"/archive", nil)
	return err
}

// Holders is every agent holding the Card, the first one first.
func (m Mandate) Holders() []string {
	if len(m.Subjects) > 0 {
		return m.Subjects
	}
	if m.Subject == "" {
		return nil
	}
	return []string{m.Subject}
}

// HeldBy reports whether agent holds the Card.
func (m Mandate) HeldBy(agent string) bool {
	for _, h := range m.Holders() {
		if h == agent {
			return true
		}
	}
	return false
}

// Shared reports a Card held by more than one agent.
func (m Mandate) Shared() bool { return len(m.Holders()) > 1 }

// ErrHoldersChanged: the Card's holders changed since the list the change
// was built from was read (the API's 412).
var ErrHoldersChanged = errors.New("apiclient: the card's holders changed since they were read")

// SetHoldersBearer replaces a Card's holders (PUT
// /v1/me/mandates/{id}/holders). expected is the list the change was
// built from; the API compares it under the Card's lock and answers
// ErrHoldersChanged when another change landed first, so two owners
// cannot silently undo each other. The API refuses with its own sentence
// (*MandateRejectedError) a list it cannot take: unchanged, duplicate,
// over 20, or a Card no longer in force.
func (c *Client) SetHoldersBearer(ctx context.Context, bearer, id string, holders, expected []string) (*Mandate, error) {
	if expected == nil {
		expected = []string{}
	}
	payload, _ := json.Marshal(map[string][]string{"subjects": holders, "expected": expected})
	resp, err := c.doRaw(ctx, bearer, http.MethodPut, "/v1/me/mandates/"+url.PathEscape(id)+"/holders", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusPreconditionFailed {
		return nil, ErrHoldersChanged
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if strings.TrimSpace(e.Error) == "" {
			e.Error = fmt.Sprintf("the holders were not changed (status %d)", resp.StatusCode)
		}
		return nil, &MandateRejectedError{StatusCode: resp.StatusCode, Message: e.Error}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("apiclient: PUT holders unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var m Mandate
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("apiclient: decode holders response: %w", err)
	}
	return &m, nil
}
