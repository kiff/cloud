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

// Support ticket surface. The apps/web view of the tenant-facing
// support API (/v1/me/support/tickets). Shapes are declared here rather
// than imported from apps/api so the web binary stays a separate Go
// module; the fields track support.Ticket / support.Message.

// SupportTicket is one ticket's summary + opening post.
type SupportTicket struct {
	ID            string    `json:"id"`
	AuthorSubject string    `json:"author_subject"`
	AuthorEmail   string    `json:"author_email,omitempty"`
	Title         string    `json:"title"`
	Body          string    `json:"body"`
	Category      string    `json:"category"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// SupportMessage is one reply on a ticket's thread.
type SupportMessage struct {
	ID            string    `json:"id"`
	TicketID      string    `json:"ticket_id"`
	AuthorKind    string    `json:"author_kind"` // "user" | "operator"
	AuthorSubject string    `json:"author_subject,omitempty"`
	Body          string    `json:"body"`
	CreatedAt     time.Time `json:"created_at"`
}

// SupportTicketDetail is GET /v1/me/support/tickets/{id}: the ticket
// plus its full thread, oldest message first.
type SupportTicketDetail struct {
	Ticket   SupportTicket    `json:"ticket"`
	Messages []SupportMessage `json:"messages"`
}

// CreateSupportTicketRequest is the POST body for a new ticket.
type CreateSupportTicketRequest struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Category string `json:"category,omitempty"`
}

// ListSupportTicketsBearer fetches GET /v1/me/support/tickets for the
// caller's tenant, most-recently-updated first. Returns an empty slice
// (not an error) when the tenant has filed none.
func (c *Client) ListSupportTicketsBearer(ctx context.Context, bearer string) ([]SupportTicket, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/me/support/tickets", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tickets []SupportTicket `json:"tickets"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode support tickets: %w", err)
	}
	return out.Tickets, nil
}

// GetSupportTicketBearer fetches GET /v1/me/support/tickets/{id}.
// Returns ErrTicketNotFound when it is missing or belongs to another
// tenant (the api 404s both).
func (c *Client) GetSupportTicketBearer(ctx context.Context, bearer, id string) (*SupportTicketDetail, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("apiclient: empty ticket id")
	}
	resp, err := c.doRaw(ctx, bearer, http.MethodGet, "/v1/me/support/tickets/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		var d SupportTicketDetail
		if err := json.Unmarshal(body, &d); err != nil {
			return nil, fmt.Errorf("apiclient: decode support ticket: %w", err)
		}
		return &d, nil
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case http.StatusNotFound:
		return nil, ErrTicketNotFound
	default:
		return nil, fmt.Errorf("apiclient: get support ticket %q unexpected status %d: %s",
			id, resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// CreateSupportTicketBearer files a new ticket via POST
// /v1/me/support/tickets. Returns the created ticket on 201.
func (c *Client) CreateSupportTicketBearer(ctx context.Context, bearer string, req CreateSupportTicketRequest) (*SupportTicket, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: marshal create ticket: %w", err)
	}
	body, err := c.do(ctx, bearer, http.MethodPost, "/v1/me/support/tickets", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	var t SupportTicket
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, fmt.Errorf("apiclient: decode created ticket: %w", err)
	}
	return &t, nil
}

// ReplySupportTicketBearer appends a reply to a ticket via POST
// /v1/me/support/tickets/{id}/reply. A user reply reopens an
// answered/closed ticket server-side. Returns ErrTicketNotFound when
// the ticket is missing or another tenant's.
func (c *Client) ReplySupportTicketBearer(ctx context.Context, bearer, id, replyBody string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("apiclient: empty ticket id")
	}
	payload, err := json.Marshal(map[string]string{"body": replyBody})
	if err != nil {
		return fmt.Errorf("apiclient: marshal reply: %w", err)
	}
	resp, err := c.doRaw(ctx, bearer, http.MethodPost,
		"/v1/me/support/tickets/"+url.PathEscape(id)+"/reply", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return ErrTicketNotFound
	default:
		return fmt.Errorf("apiclient: reply to ticket %q unexpected status %d: %s",
			id, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
}
