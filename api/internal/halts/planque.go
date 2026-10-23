package halts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/permit"
)

// Confirmer asks planque to confirm it sees a halt: planque reads the halt
// back from KIFF, with the account's connector key, the way its dispatch
// does before every send, and answers only once it sees it.
type Confirmer interface {
	Confirm(ctx context.Context, tenant, agent string, haltedAt time.Time) error
}

// Planques confirms with each planque KIFF sends relay calls to. With none,
// nothing dispatches outside KIFF's gateway, and a halt is confirmed at once.
// Each ask is signed with Key, KIFF's permit key, so a planque answers only
// KIFF; without a key it goes unsigned, and a planque refuses it.
type Planques struct {
	URLs   []string // such as https://planque.kiff.dev
	HTTP   *http.Client
	Key    *permit.Key
	Issuer string // the permits' iss, such as https://api.kiff.dev
}

// Confirm asks every planque; any that doesn't confirm is an error.
func (p *Planques) Confirm(ctx context.Context, tenant, agent string, haltedAt time.Time) error {
	hc := p.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	body, _ := json.Marshal(map[string]any{"agent": agent, "halted_at": haltedAt.UTC().Format(time.RFC3339Nano)})
	var signed string
	if p.Key != nil && len(p.URLs) > 0 {
		var err error
		if signed, err = p.Key.SignHaltConfirm(p.Issuer, tenant, agent, haltedAt, time.Now()); err != nil {
			return fmt.Errorf("halts: signing the ask: %w", err)
		}
	}
	for _, base := range p.URLs {
		u := strings.TrimRight(base, "/") + "/v1/accounts/" + url.PathEscape(tenant) + "/halts/confirm"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if signed != "" {
			req.Header.Set(permit.HaltConfirmHeader, signed)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return fmt.Errorf("halts: planque at %s: %w", base, err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		var out struct {
			Seen bool `json:"seen"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &out) != nil || !out.Seen {
			return fmt.Errorf("halts: planque at %s didn't confirm the halt (%d)", base, resp.StatusCode)
		}
	}
	return nil
}
