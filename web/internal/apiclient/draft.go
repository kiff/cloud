package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// draft.go — apiclient methods for the cloud-side draft store
// (MVP-2). Mirrors the four routes shipped by api an earlier
// change:
//
//   GET    /v1/me/domain/draft           → load
//   PUT    /v1/me/domain/draft           → upsert (always saves)
//   DELETE /v1/me/domain/draft           → discard
//   POST   /v1/me/domain/draft/activate  → promote to active in
//                                          one transaction
//
// The PUT path is the auto-save spine — every block edit on the
// Studio page lands here. The activate path is the explicit
// "Save and activate" click that promotes the draft to the
// active domain for the runtime.

// Draft is the apps/web view of /v1/me/domain/draft's response.
// YAML is the raw bytes stored on the api side; Parsed carries
// the framework's parsed summary when the YAML happens to be
// valid; Issues carries the validation messages otherwise.
//
// Drafts may be invalid mid-edit (the auto-save promise is
// "always accept the user's work; report what's wrong"), so
// either Parsed or Issues populates per response — never both
// in v1.
type Draft struct {
	YAML      string                  `json:"yaml"`
	UpdatedAt int64                   `json:"updated_at"`
	Parsed    *Domain                 `json:"parsed,omitempty"`
	Issues    []DomainValidationIssue `json:"issues,omitempty"`
}

// ErrDraftNotFound is returned when the tenant has no draft.
// The Studio handler treats it as "render the placeholder
// stage 1 default" rather than as an error.
var ErrDraftNotFound = errors.New("apiclient: tenant has no draft")

// GetDraftBearer fetches the tenant's current draft. Returns
// ErrDraftNotFound when no draft exists.
func (c *Client) GetDraftBearer(ctx context.Context, bearer string) (*Draft, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/me/domain/draft", nil)
	if err != nil {
		// The shared do() helper collapses any 404 to
		// ErrReceiptNotFound; the draft handler returns 404 for
		// missing drafts, so map that to the draft-specific
		// sentinel here.
		if errors.Is(err, ErrReceiptNotFound) {
			return nil, ErrDraftNotFound
		}
		return nil, err
	}
	var d Draft
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("apiclient: decode draft: %w", err)
	}
	return &d, nil
}

// PutDraftBearer upserts the tenant's draft from the supplied
// YAML bytes. Always saves on the api side; the response
// carries either Parsed (valid) or Issues (invalid) so the
// editor can render inline errors without losing the user's
// work.
func (c *Client) PutDraftBearer(ctx context.Context, bearer string, yaml []byte) (*Draft, error) {
	if c == nil {
		return nil, errors.New("apiclient: nil client")
	}
	if strings.TrimSpace(bearer) == "" {
		return nil, errors.New("apiclient: empty bearer")
	}
	if len(yaml) == 0 {
		return nil, errors.New("apiclient: empty yaml")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/v1/me/domain/draft", bytes.NewReader(yaml))
	if err != nil {
		return nil, fmt.Errorf("apiclient: build draft put: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/x-yaml")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: put draft: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		var d Draft
		if err := json.Unmarshal(respBody, &d); err != nil {
			return nil, fmt.Errorf("apiclient: decode draft put: %w", err)
		}
		return &d, nil
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case http.StatusRequestEntityTooLarge:
		return nil, errors.New("apiclient: draft yaml exceeds the cloud's per-request limit")
	default:
		return nil, fmt.Errorf("apiclient: put draft unexpected status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
}

// DeleteDraftBearer discards the tenant's draft. Idempotent at
// the api side — calling DELETE on a non-existent draft still
// returns 204 (the api's MemoryDraftSource and PostgresDraftSource
// both no-op on missing keys). Returns nil on success.
//
// Retained client surface: after the block editor was retired there is
// no web caller, but the DELETE /v1/me/domain/draft server endpoint is
// still live, so the client keeps a complete surface for it (exercised
// by draft_test.go).
func (c *Client) DeleteDraftBearer(ctx context.Context, bearer string) error {
	_, err := c.do(ctx, bearer, http.MethodDelete, "/v1/me/domain/draft", nil)
	if err != nil {
		// The shared do() helper treats 204 as 2xx; only
		// transport / 4xx surfaces here.
		if errors.Is(err, ErrReceiptNotFound) {
			// Nothing to delete; treat as success.
			return nil
		}
		return err
	}
	return nil
}

// ActivateDraftBearer promotes the tenant's draft to active.
// Validates one last time on the api side (reusing the
// executor registry); on success, returns the active domain
// summary with the new version. On validation failure, returns
// a *DomainValidationError carrying the issues.
//
// Retained client surface: the draft-activate web path was retired
// with the block editor (the authenticated-surface design; the
// block-free path now activates a finished kiff.yaml via
// PutDomainBearer). There is no web caller, but the POST
// /v1/me/domain/draft/activate server endpoint is still live, so the
// client keeps a complete surface for it (exercised by
// draft_test.go).
//
// The activate path is transactional on the api side: the
// active row upserts and the draft row deletes inside one
// BEGIN/COMMIT (Postgres mode). The runtime resolver, which
// only reads the active table, never sees a missing-domain
// window.
func (c *Client) ActivateDraftBearer(ctx context.Context, bearer string) (*Domain, error) {
	if c == nil {
		return nil, errors.New("apiclient: nil client")
	}
	if strings.TrimSpace(bearer) == "" {
		return nil, errors.New("apiclient: empty bearer")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/me/domain/draft/activate", nil)
	if err != nil {
		return nil, fmt.Errorf("apiclient: build draft activate: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: activate draft: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		var d Domain
		if err := json.Unmarshal(respBody, &d); err != nil {
			return nil, fmt.Errorf("apiclient: decode activate response: %w", err)
		}
		return &d, nil
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case http.StatusNotFound:
		// No draft to activate.
		return nil, ErrDraftNotFound
	case http.StatusUnprocessableEntity:
		var verr rawValidateErrorBody
		_ = json.Unmarshal(respBody, &verr)
		return nil, &DomainValidationError{
			StatusCode: resp.StatusCode,
			Message:    firstNonEmptyString(verr.Error, "draft validation failed"),
			Issues:     verr.Issues,
		}
	default:
		return nil, fmt.Errorf("apiclient: activate draft unexpected status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
}
