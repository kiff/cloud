package packs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// A request's status. An open request past its end reads as expired.
const (
	RequestOpen      = "open"
	RequestApproved  = "approved"
	RequestDeclined  = "declined"
	RequestWithdrawn = "withdrawn"
	RequestExpired   = "expired"
)

// MaxReason bounds a request's reason and an answer's note, in runes.
const MaxReason = 500

// What a request asks for.
const (
	// AskPack: a pack, on Solo. Approved by buying it for the request.
	AskPack = "pack"
	// AskRaise: a raise of the spend limit, on Company. Approved by a raise,
	// which answers every open request for one.
	AskRaise = "raise"
)

// Request is a member asking the account's admins to add a pack.
type Request struct {
	ID       string `json:"id"`
	TenantID string `json:"-"`
	AskedBy  string `json:"asked_by"`
	// Kind is what it asks for: AskPack or AskRaise. A raise names no pack.
	Kind       string     `json:"kind"`
	Pack       string     `json:"pack,omitempty"`
	Reason     string     `json:"reason"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AnsweredBy string     `json:"answered_by,omitempty"`
	AnsweredAt *time.Time `json:"answered_at,omitempty"`
	Note       string     `json:"note,omitempty"`
	PackID     string     `json:"pack_id,omitempty"`
}

var (
	// ErrAlreadyAsked: the member has an open request already.
	ErrAlreadyAsked = errors.New("packs: an open request already")
	// ErrRequestNotFound: no such request on the account.
	ErrRequestNotFound = errors.New("packs: no such request")
	// ErrAnswered: the request was answered, withdrawn or expired.
	ErrAnswered = errors.New("packs: the request isn't open")
)

// Requests keeps the members' requests.
type Requests interface {
	// Ask records r, open until r.ExpiresAt.
	Ask(ctx context.Context, r Request) (Request, error)
	Get(ctx context.Context, tenantID, id string, now time.Time) (Request, error)
	// List lists the account's requests made since since, newest first;
	// only askedBy's when it isn't "", only open ones when openOnly.
	List(ctx context.Context, tenantID, askedBy string, openOnly bool, since, now time.Time) ([]Request, error)
	// Withdraw withdraws an open request; only the member who asked.
	Withdraw(ctx context.Context, tenantID, id, by string, now time.Time) (Request, error)
	// Decline declines an open request, with a note.
	Decline(ctx context.Context, tenantID, id, by, note string, now time.Time) (Request, error)
	// ApproveRaises approves every open request for a raise: by raised the
	// spend limit. It returns those it approved.
	ApproveRaises(ctx context.Context, tenantID, by string, now time.Time) ([]Request, error)
}

// read gives an open request past its end its status.
func (r Request) read(now time.Time) Request {
	if r.Status == RequestOpen && !now.Before(r.ExpiresAt) {
		r.Status = RequestExpired
	}
	return r
}

// kind is what r asks for: a pack unless it's a raise.
func (r Request) kind() string {
	if r.Kind == AskRaise {
		return AskRaise
	}
	return AskPack
}

const requestCols = `id, tenant_id, asked_by, kind, pack, reason, status, created_at, expires_at, COALESCE(answered_by, ''), answered_at, note, COALESCE(pack_id, '')`

func scanRequest(row pgx.Row) (Request, error) {
	var r Request
	err := row.Scan(&r.ID, &r.TenantID, &r.AskedBy, &r.Kind, &r.Pack, &r.Reason, &r.Status, &r.CreatedAt, &r.ExpiresAt, &r.AnsweredBy, &r.AnsweredAt, &r.Note, &r.PackID)
	return r, err
}

// Ask implements Requests.
func (p *Postgres) Ask(ctx context.Context, r Request) (Request, error) {
	r.ID = newID("pr_")
	r.Kind = r.kind()
	if r.Kind == AskRaise {
		r.Pack = ""
	}
	r.Status = RequestOpen
	r.CreatedAt = r.CreatedAt.UTC()
	r.Reason = strings.TrimSpace(r.Reason)
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Request{}, fmt.Errorf("packs: ask: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A request past its end no longer holds the member's one open slot.
	if _, err := tx.Exec(ctx, `UPDATE cloud_meta.task_pack_requests SET status = 'expired'
		WHERE tenant_id = $1 AND asked_by = $2 AND status = 'open' AND expires_at <= $3`, r.TenantID, r.AskedBy, r.CreatedAt); err != nil {
		return Request{}, fmt.Errorf("packs: ask: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO cloud_meta.task_pack_requests (id, tenant_id, asked_by, kind, pack, reason, status, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'open', $7, $8)`, r.ID, r.TenantID, r.AskedBy, r.Kind, r.Pack, r.Reason, r.CreatedAt, r.ExpiresAt.UTC())
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Request{}, ErrAlreadyAsked
	}
	if err != nil {
		return Request{}, fmt.Errorf("packs: ask: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Request{}, fmt.Errorf("packs: ask: %w", err)
	}
	return r, nil
}

// Get implements Requests.
func (p *Postgres) Get(ctx context.Context, tenantID, id string, now time.Time) (Request, error) {
	r, err := scanRequest(p.pool.QueryRow(ctx, `SELECT `+requestCols+` FROM cloud_meta.task_pack_requests WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ErrRequestNotFound
	}
	if err != nil {
		return Request{}, fmt.Errorf("packs: request: %w", err)
	}
	return r.read(now), nil
}

// List implements Requests.
func (p *Postgres) List(ctx context.Context, tenantID, askedBy string, openOnly bool, since, now time.Time) ([]Request, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+requestCols+` FROM cloud_meta.task_pack_requests
		WHERE tenant_id = $1 AND ($2 = '' OR asked_by = $2) AND created_at >= $3
		  AND (NOT $4 OR (status = 'open' AND expires_at > $5))
		ORDER BY created_at DESC, id LIMIT 100`, tenantID, askedBy, since.UTC(), openOnly, now.UTC())
	if err != nil {
		return nil, fmt.Errorf("packs: requests: %w", err)
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("packs: requests: %w", err)
		}
		out = append(out, r.read(now))
	}
	return out, rows.Err()
}

// answer moves an open request on; with onlyAsker, only for the member who asked.
func (p *Postgres) answer(ctx context.Context, tenantID, id, status, by, note string, onlyAsker bool, now time.Time) (Request, error) {
	r, err := scanRequest(p.pool.QueryRow(ctx, `
		UPDATE cloud_meta.task_pack_requests SET status = $3, answered_by = $4, answered_at = $5, note = $6
		WHERE tenant_id = $1 AND id = $2 AND status = 'open' AND expires_at > $5 AND (NOT $7 OR asked_by = $4)
		RETURNING `+requestCols, tenantID, id, status, by, now.UTC(), note, onlyAsker))
	if errors.Is(err, pgx.ErrNoRows) {
		cur, gerr := p.Get(ctx, tenantID, id, now)
		switch {
		case gerr != nil:
			return Request{}, gerr
		case onlyAsker && cur.AskedBy != by:
			return Request{}, ErrRequestNotFound
		default:
			return cur, ErrAnswered
		}
	}
	if err != nil {
		return Request{}, fmt.Errorf("packs: answer request: %w", err)
	}
	return r, nil
}

// Withdraw implements Requests.
func (p *Postgres) Withdraw(ctx context.Context, tenantID, id, by string, now time.Time) (Request, error) {
	return p.answer(ctx, tenantID, id, RequestWithdrawn, by, "", true, now)
}

// Decline implements Requests.
func (p *Postgres) Decline(ctx context.Context, tenantID, id, by, note string, now time.Time) (Request, error) {
	return p.answer(ctx, tenantID, id, RequestDeclined, by, strings.TrimSpace(note), false, now)
}

// ApproveRaises implements Requests.
func (p *Postgres) ApproveRaises(ctx context.Context, tenantID, by string, now time.Time) ([]Request, error) {
	rows, err := p.pool.Query(ctx, `
		UPDATE cloud_meta.task_pack_requests SET status = 'approved', answered_by = NULLIF($2, ''), answered_at = $3
		WHERE tenant_id = $1 AND kind = 'raise' AND status = 'open' AND expires_at > $3
		RETURNING `+requestCols, tenantID, by, now.UTC())
	if err != nil {
		return nil, fmt.Errorf("packs: approve raises: %w", err)
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("packs: approve raises: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
