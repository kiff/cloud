package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ErrConflict is a 409 from the API: the resource changed or the action
// lost a race (for example, another reviewer claimed the item first).
var ErrConflict = errors.New("apiclient: conflict")

type AttentionItem struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	SourceID  string    `json:"source_id"`
	DomainID  string    `json:"domain_id"`
	ActorID   string    `json:"actor_id"`
	Action    string    `json:"action"`
	EntityID  string    `json:"entity_id"`
	Reason    string    `json:"reason"`
	Status    string    `json:"status"`
	Assignee  string    `json:"assignee"`
	CreatedAt time.Time `json:"created_at"`
	Revision  int64     `json:"revision"`
	Unread    bool      `json:"unread"`
}
type AttentionFeed struct {
	Failed    int             `json:"failed"`
	Pending   int             `json:"pending"`
	Items     []AttentionItem `json:"items"`
	Open      int             `json:"open"`
	Unread    int             `json:"unread"`
	Truncated bool            `json:"truncated"`
}

func (c *Client) AttentionBearer(ctx context.Context, bearer string) (AttentionFeed, error) {
	return c.AttentionPageBearer(ctx, bearer, 0, true)
}
func (c *Client) AttentionPageBearer(ctx context.Context, bearer string, page int, unread bool) (AttentionFeed, error) {
	var feed AttentionFeed
	path := "/v1/me/attention?page=" + strconv.Itoa(page)
	if unread {
		path += "&unread=1"
	}
	body, err := c.do(ctx, bearer, http.MethodGet, path, nil)
	if err != nil {
		return feed, err
	}
	err = json.Unmarshal(body, &feed)
	return feed, err
}
func (c *Client) SeeAttentionBearer(ctx context.Context, bearer string, items map[string]int64) error {
	body, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		return err
	}
	_, err = c.do(ctx, bearer, http.MethodPost, "/v1/me/attention/seen", bytes.NewReader(body))
	return err
}
func (c *Client) ActAttentionBearer(ctx context.Context, bearer, id, action string, revision int64) error {
	body, err := json.Marshal(map[string]int64{"revision": revision})
	if err != nil {
		return err
	}
	_, err = c.do(ctx, bearer, http.MethodPost, "/v1/me/attention/"+url.PathEscape(id)+"/"+url.PathEscape(action), bytes.NewReader(body))
	return err
}
