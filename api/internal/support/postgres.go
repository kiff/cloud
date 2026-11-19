package support

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/support_tickets.sql
var supportTicketsSQL string

// SupportTicketsSQL returns the DDL that creates the
// cloud_meta.support_tickets + support_ticket_messages tables.
// Idempotent. Call once at startup after tenant.ApplyCloudMeta.
func SupportTicketsSQL() string { return supportTicketsSQL }

// ApplySchema runs the support DDL against pool. Idempotent.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "support/support_tickets", supportTicketsSQL); err != nil {
		return fmt.Errorf("apply support_tickets schema: %w", err)
	}
	return nil
}

// PostgresStore persists tickets + messages in cloud_meta.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore wraps the cloud pool. The pool must have
// ApplyCloudMeta + ApplySchema applied.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

const sqlCreateTicket = `
INSERT INTO cloud_meta.support_tickets
    (id, tenant_id, author_subject, author_email, title, body, category, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
`

func (p *PostgresStore) CreateTicket(ctx context.Context, t Ticket) error {
	_, err := p.pool.Exec(ctx, sqlCreateTicket,
		t.ID, t.TenantID, t.AuthorSubject, t.AuthorEmail, t.Title, t.Body, t.Category, string(t.Status), t.CreatedAt)
	if err != nil {
		return fmt.Errorf("create support ticket: %w", err)
	}
	return nil
}

const ticketCols = `id, tenant_id, author_subject, author_email, title, body, category, status, created_at, updated_at`

func scanTicket(row pgx.Row) (Ticket, error) {
	var t Ticket
	var status string
	if err := row.Scan(&t.ID, &t.TenantID, &t.AuthorSubject, &t.AuthorEmail,
		&t.Title, &t.Body, &t.Category, &status, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return Ticket{}, err
	}
	t.Status = Status(status)
	return t, nil
}

func (p *PostgresStore) queryTickets(ctx context.Context, sql string, args ...any) ([]Ticket, error) {
	rows, err := p.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query support tickets: %w", err)
	}
	defer rows.Close()
	out := []Ticket{}
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, fmt.Errorf("scan support ticket: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (p *PostgresStore) ListForTenant(ctx context.Context, tenantID string) ([]Ticket, error) {
	return p.queryTickets(ctx,
		`SELECT `+ticketCols+` FROM cloud_meta.support_tickets WHERE tenant_id = $1 ORDER BY updated_at DESC`,
		tenantID)
}

func (p *PostgresStore) GetForTenant(ctx context.Context, tenantID, id string) (Ticket, error) {
	t, err := scanTicket(p.pool.QueryRow(ctx,
		`SELECT `+ticketCols+` FROM cloud_meta.support_tickets WHERE tenant_id = $1 AND id = $2`,
		tenantID, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Ticket{}, ErrNotFound
		}
		return Ticket{}, fmt.Errorf("get support ticket: %w", err)
	}
	return t, nil
}

func (p *PostgresStore) ListAll(ctx context.Context, status Status) ([]Ticket, error) {
	if status == "" {
		return p.queryTickets(ctx,
			`SELECT `+ticketCols+` FROM cloud_meta.support_tickets ORDER BY updated_at DESC`)
	}
	return p.queryTickets(ctx,
		`SELECT `+ticketCols+` FROM cloud_meta.support_tickets WHERE status = $1 ORDER BY updated_at DESC`,
		string(status))
}

func (p *PostgresStore) GetByID(ctx context.Context, id string) (Ticket, error) {
	t, err := scanTicket(p.pool.QueryRow(ctx,
		`SELECT `+ticketCols+` FROM cloud_meta.support_tickets WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Ticket{}, ErrNotFound
		}
		return Ticket{}, fmt.Errorf("get support ticket by id: %w", err)
	}
	return t, nil
}

func (p *PostgresStore) SetStatus(ctx context.Context, id string, status Status) error {
	tag, err := p.pool.Exec(ctx,
		`UPDATE cloud_meta.support_tickets SET status = $2, updated_at = NOW() WHERE id = $1`,
		id, string(status))
	if err != nil {
		return fmt.Errorf("set support ticket status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *PostgresStore) AddMessage(ctx context.Context, m Message) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Bump the ticket first: a zero row count means the ticket does
	// not exist, so we return ErrNotFound instead of letting the
	// message INSERT trip the foreign key.
	tag, err := tx.Exec(ctx,
		`UPDATE cloud_meta.support_tickets SET updated_at = $2 WHERE id = $1`, m.TicketID, m.CreatedAt)
	if err != nil {
		return fmt.Errorf("bump ticket updated_at: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO cloud_meta.support_ticket_messages (id, ticket_id, author_kind, author_subject, body, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		m.ID, m.TicketID, m.AuthorKind, m.AuthorSubject, m.Body, m.CreatedAt); err != nil {
		return fmt.Errorf("add support message: %w", err)
	}
	return tx.Commit(ctx)
}

func (p *PostgresStore) ListMessages(ctx context.Context, ticketID string) ([]Message, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id, ticket_id, author_kind, author_subject, body, created_at
		 FROM cloud_meta.support_ticket_messages WHERE ticket_id = $1 ORDER BY created_at ASC`,
		ticketID)
	if err != nil {
		return nil, fmt.Errorf("list support messages: %w", err)
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.TicketID, &m.AuthorKind, &m.AuthorSubject, &m.Body, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan support message: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
