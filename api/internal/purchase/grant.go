package purchase

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"
)

// ToolIssueCard is the relay's call that makes a case's single-use card.
// KIFF allows it, and signs its permit, only for the current version of an
// approved case whose execution lease is held, within its time: the
// arguments are the case, its digest and that decision, so the permit's
// hash binds the card to exactly that case.
const ToolIssueCard = "issue_card"

// IssueCardArgs are issue_card's arguments as the relay reads them.
type IssueCardArgs struct {
	Case       json.RawMessage `json:"case"`
	Digest     string          `json:"digest"`
	State      string          `json:"state"`
	ValidUntil time.Time       `json:"valid_until"`
}

// ErrNotIssuable: issue_card's arguments aren't a case KIFF allows a card for.
var ErrNotIssuable = errors.New("purchase: no card for this case now")

// CardArgs are issue_card's arguments for an executing case.
func CardArgs(r Row, now time.Time) (json.RawMessage, error) {
	if r.State != StateExecuting || r.LeaseHolder == "" || !now.Before(r.ValidUntil) {
		return nil, ErrNotIssuable
	}
	body, err := json.Marshal(r.Case)
	if err != nil {
		return nil, err
	}
	return json.Marshal(IssueCardArgs{Case: body, Digest: r.Digest, State: StateApproved, ValidUntil: r.ValidUntil.UTC()})
}

// AllowsCard is KIFF's decision on an issue_card call: the arguments are
// exactly the ones CardArgs gives for the case's current, executing
// version, so a card is made only for that case, once its lease is held,
// and never after its time.
func AllowsCard(r Row, args json.RawMessage, now time.Time) error {
	want, err := CardArgs(r, now)
	if err != nil {
		return err
	}
	var got IssueCardArgs
	if err := json.Unmarshal(args, &got); err != nil {
		return ErrNotIssuable
	}
	var c Case
	if err := json.Unmarshal(got.Case, &c); err != nil {
		return ErrNotIssuable
	}
	d, err := c.Digest()
	if err != nil || d != r.Digest || got.Digest != r.Digest || got.State != StateApproved || !got.ValidUntil.Equal(r.ValidUntil) {
		return ErrNotIssuable
	}
	canonical, _ := json.Marshal(IssueCardArgs{Case: mustJSON(c), Digest: got.Digest, State: got.State, ValidUntil: got.ValidUntil.UTC()})
	if !bytes.Equal(canonical, want) {
		return ErrNotIssuable
	}
	return nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
