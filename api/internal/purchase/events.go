package purchase

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// The issuer's money event kinds KIFF keeps.
const (
	EventCapture = "capture"
	EventRefund  = "refund"
)

// ErrCardNoCase: an issuer event for a card no case issued. It's kept, to
// be found and its card cancelled, and said at once.
var ErrCardNoCase = errors.New("purchase: an issuer event for a card in no case")

// ErrOtherAccount: an issuer event from another connected account than the
// case's business: not counted.
var ErrOtherAccount = errors.New("purchase: an issuer event from another account")

// Paused says why the Card's buying is paused, or "" when it isn't.
type Paused struct {
	Reason string
	CaseID string
}

// MoneyEvent is one of the issuer's money events on a card: a capture or a
// refund, its authorization ("" for a capture none covers), its amount in
// minor units.
type MoneyEvent struct {
	EventID       string
	Account       string
	Card          string
	Kind          string
	Authorization string
	Amount        int64
}

// RecordMoney keeps an issuer money event once, by its event id, for the case
// whose card it is, and settles the case's money again: a force capture or
// an over-capture pauses the Card's buying in the same transaction, so
// within limits buys nothing more until the owner resumes it. A repeat of an
// event changes nothing. It answers the settlement, ErrCardNoCase for a card
// no case issued (kept with an empty case), or ErrOtherAccount.
func (s *Store) RecordMoney(ctx context.Context, e MoneyEvent) (Settlement, error) {
	if e.Kind != EventCapture && e.Kind != EventRefund {
		return Settlement{}, errors.New("purchase: a money event is a capture or a refund")
	}
	if e.Amount < 0 {
		e.Amount = -e.Amount
	}
	now := s.now()
	var out Settlement
	var noCase bool
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		r, err := scanRow(tx.QueryRow(ctx, `SELECT `+rowCols+` FROM purchase.cases WHERE issued_card = $1 AND issued_card <> '' FOR UPDATE`, e.Card))
		if errors.Is(err, ErrNoCase) {
			noCase = true
			_, err := tx.Exec(ctx, `INSERT INTO purchase.card_events (event_id, tenant_id, case_id, version, card, kind, authorization_id, amount, at)
				VALUES ($1, '', '', 0, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`, e.EventID, e.Card, e.Kind, e.Authorization, e.Amount, now)
			return err
		}
		if err != nil {
			return err
		}
		c := r.Case
		var account string
		if err := tx.QueryRow(ctx, `SELECT account FROM purchase.accounts WHERE tenant_id = $1`, c.Tenant).Scan(&account); err != nil || account == "" || account != e.Account {
			return ErrOtherAccount
		}
		if _, err := tx.Exec(ctx, `INSERT INTO purchase.card_events (event_id, tenant_id, case_id, version, card, kind, authorization_id, amount, at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT DO NOTHING`,
			e.EventID, c.Tenant, c.CaseID, c.Version, e.Card, e.Kind, e.Authorization, e.Amount, now); err != nil {
			return err
		}
		if out, err = settleIn(ctx, tx, c); err != nil {
			return err
		}
		if out.Exception {
			_, err = tx.Exec(ctx, `INSERT INTO purchase.paused (tenant_id, card_id, reason, case_id, at) VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (tenant_id, card_id) DO NOTHING`, c.Tenant, c.Card.ID, out.Money, c.CaseID, now)
		}
		return err
	})
	if err != nil {
		return Settlement{}, err
	}
	if noCase {
		return Settlement{}, ErrCardNoCase
	}
	return out, nil
}

// Settlement reads a case version's money status from what KIFF kept: the
// authorizations it approved and the issuer's money events on its card.
func (s *Store) Settlement(ctx context.Context, tenant, caseID string, version int) (Settlement, error) {
	r, err := s.Get(ctx, tenant, caseID, version)
	if err != nil {
		return Settlement{}, err
	}
	var out Settlement
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		out, err = settleIn(ctx, tx, r.Case)
		return err
	})
	return out, err
}

func settleIn(ctx context.Context, tx pgx.Tx, c Case) (Settlement, error) {
	var events []CardEvent
	approved := map[string]bool{}
	rows, err := tx.Query(ctx, `SELECT authorization_id, amount FROM purchase.authorizations WHERE tenant_id = $1 AND case_id = $2 AND version = $3 AND approved ORDER BY at`,
		c.Tenant, c.CaseID, c.Version)
	if err != nil {
		return Settlement{}, err
	}
	for rows.Next() {
		var id string
		var amount int64
		if err := rows.Scan(&id, &amount); err != nil {
			rows.Close()
			return Settlement{}, err
		}
		// One authorization's requests share its id: iauth_x#0, iauth_x#1.
		base, _, _ := strings.Cut(id, "#")
		approved[base] = true
		events = append(events, CardEvent{Kind: "authorization", Authorization: base, Amount: amount})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Settlement{}, err
	}
	rows, err = tx.Query(ctx, `SELECT kind, authorization_id, amount FROM purchase.card_events WHERE tenant_id = $1 AND case_id = $2 AND version = $3 ORDER BY at, event_id`,
		c.Tenant, c.CaseID, c.Version)
	if err != nil {
		return Settlement{}, err
	}
	for rows.Next() {
		var e CardEvent
		if err := rows.Scan(&e.Kind, &e.Authorization, &e.Amount); err != nil {
			rows.Close()
			return Settlement{}, err
		}
		events = append(events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Settlement{}, err
	}
	return Settle(events, approved, c.Price.Max), nil
}

// PausedCard says whether a Card's buying is paused, and why.
func (s *Store) PausedCard(ctx context.Context, tenant, cardID string) (Paused, error) {
	var p Paused
	err := s.Pool.QueryRow(ctx, `SELECT reason, case_id FROM purchase.paused WHERE tenant_id = $1 AND card_id = $2`, tenant, cardID).Scan(&p.Reason, &p.CaseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Paused{}, nil
	}
	return p, err
}

// Resume lifts a Card's pause: the owner's to do, after reading why.
func (s *Store) Resume(ctx context.Context, tenant, cardID string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM purchase.paused WHERE tenant_id = $1 AND card_id = $2`, tenant, cardID)
	return err
}

// oneLine is an issuer's word as KIFF keeps it: one line, at most max runes.
func oneLine(s string, max int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}
