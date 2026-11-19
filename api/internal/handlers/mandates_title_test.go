package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// titleServer is the Cards API with a domain to check issued Cards against.
func titleServer(t *testing.T) (*http.ServeMux, *mandate.MemoryStore) {
	t.Helper()
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	h := NewMandatesHandler(store, stubDomainSource{yaml: mandateDomainYAML}, WithMandatesClock(func() time.Time {
		return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	}))
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, store
}

// titleCard is a Card body to issue, with the given title ("" for none).
func titleCard(id, title string) map[string]any {
	b := map[string]any{
		"id": id, "subject": "agent-a", "domain": "refund-control",
		"grants":     []map[string]any{{"action": "AUTO_REFUND"}},
		"aggregates": []map[string]any{{"quantity": "sum(amount)", "limit": 500000, "window": "calendar_day"}},
	}
	if title != "" {
		b["title"] = title
	}
	return b
}

// listedTitles reads GET /v1/me/mandates: each Card's title, and whether
// the view carries one at all.
func listedTitles(t *testing.T, mux *http.ServeMux) map[string]*string {
	t.Helper()
	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Mandates []struct {
			ID    string  `json:"id"`
			Title *string `json:"title"`
		} `json:"mandates"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode list: %v body=%s", err, rr.Body.String())
	}
	out := map[string]*string{}
	for _, m := range body.Mandates {
		out[m.ID] = m.Title
	}
	return out
}

// A Card can carry a title for people: set when it's issued, read
// back with the Card, and absent when none was given.
func TestCardTitleIsIssuedAndListed(t *testing.T) {
	mux, _ := titleServer(t)
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", titleCard("named", "  Refunds on Stripe ")); rr.Code != http.StatusCreated {
		t.Fatalf("issue status = %d body=%s", rr.Code, rr.Body.String())
	} else if !strings.Contains(rr.Body.String(), `"title":"Refunds on Stripe"`) {
		t.Errorf("issue answer lacks the trimmed title: %s", rr.Body.String())
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", titleCard("plain", "")); rr.Code != http.StatusCreated {
		t.Fatalf("issue status = %d body=%s", rr.Code, rr.Body.String())
	}
	got := listedTitles(t, mux)
	if got["named"] == nil || *got["named"] != "Refunds on Stripe" {
		t.Errorf("named title = %v", got["named"])
	}
	if got["plain"] != nil {
		t.Errorf("a Card without a title lists one: %q", *got["plain"])
	}
}

// A title is plain text of at most 60 characters; anything else is refused
// with a message that says why, and nothing is issued.
func TestCardTitleIsRefusedWhenTooLongOrNotOneLine(t *testing.T) {
	mux, store := titleServer(t)
	long := strings.Repeat("é", mandate.MaxTitle+1)
	rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", titleCard("long", long))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "at most 60") {
		t.Fatalf("a 61-character title: status = %d body=%s", rr.Code, rr.Body.String())
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", titleCard("lines", "Refunds\non Stripe")); rr.Code != http.StatusBadRequest {
		t.Fatalf("a two-line title: status = %d body=%s", rr.Code, rr.Body.String())
	}
	exact := strings.Repeat("é", mandate.MaxTitle)
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", titleCard("exact", exact)); rr.Code != http.StatusCreated {
		t.Fatalf("a 60-character title: status = %d body=%s", rr.Code, rr.Body.String())
	}
	ms, _ := store.List(context.Background(), "t1")
	if len(ms) != 1 || ms[0].ID != "exact" {
		t.Errorf("issued = %v, want only the 60-character one", ms)
	}
}

// Changing a Card's title is not a change to the Card: no revision, and
// its limits stay. A reissue without a title keeps it; one with a title
// replaces it; "" clears it.
func TestChangingACardTitleKeepsItsTermsAndRevision(t *testing.T) {
	mux, store := titleServer(t)
	ctx := context.Background()
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", titleCard("mdt", "Refunds")); rr.Code != http.StatusCreated {
		t.Fatalf("issue status = %d body=%s", rr.Code, rr.Body.String())
	}
	rr := do(t, mux, owner(), http.MethodPut, "/v1/me/mandates/mdt/title", map[string]any{"title": "Refunds on Stripe"})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"title":"Refunds on Stripe"`) {
		t.Fatalf("set title: status = %d body=%s", rr.Code, rr.Body.String())
	}
	if rs, _ := store.Revisions(ctx, "t1", "mdt"); len(rs) != 1 {
		t.Errorf("renaming wrote a revision: %d revisions", len(rs))
	}
	ms, _ := store.List(ctx, "t1")
	if len(ms) != 1 || ms[0].Title != "Refunds on Stripe" || len(ms[0].Aggregates) != 1 || ms[0].Aggregates[0].Limit != 500000 {
		t.Fatalf("after renaming: %+v", ms)
	}

	// A reissue that names no title keeps the current one.
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", titleCard("mdt", "")); rr.Code != http.StatusCreated {
		t.Fatalf("reissue status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := listedTitles(t, mux)["mdt"]; got == nil || *got != "Refunds on Stripe" {
		t.Errorf("a reissue without a title lost it: %v", got)
	}
	// One that names a title replaces it.
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", titleCard("mdt", "Read Stripe")); rr.Code != http.StatusCreated {
		t.Fatalf("reissue status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := listedTitles(t, mux)["mdt"]; got == nil || *got != "Read Stripe" {
		t.Errorf("a reissue's title = %v, want Read Stripe", got)
	}
	// "" clears it.
	if rr := do(t, mux, owner(), http.MethodPut, "/v1/me/mandates/mdt/title", map[string]any{"title": ""}); rr.Code != http.StatusOK {
		t.Fatalf("clear title: status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := listedTitles(t, mux)["mdt"]; got != nil {
		t.Errorf("a cleared title still lists: %q", *got)
	}
}

// Naming a Card is the owner's, like issuing one: a runtime key can't, an
// unknown Card is a 404, and a title that's too long changes nothing.
func TestSettingACardTitleIsRefusedWhereItShouldBe(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	if err := store.Issue(ctx, mandate.Mandate{ID: "mdt", TenantID: "t1", Subject: "agent://a", Title: "Refunds"}, "owner"); err != nil {
		t.Fatal(err)
	}
	if rr := do(t, mux, runtimeKey(), http.MethodPut, "/v1/me/mandates/mdt/title", map[string]any{"title": "Mine now"}); rr.Code != http.StatusForbidden {
		t.Errorf("runtime key: status = %d, want 403", rr.Code)
	}
	if rr := do(t, mux, owner(), http.MethodPut, "/v1/me/mandates/nope/title", map[string]any{"title": "x"}); rr.Code != http.StatusNotFound {
		t.Errorf("unknown Card: status = %d, want 404", rr.Code)
	}
	if rr := do(t, mux, owner(), http.MethodPut, "/v1/me/mandates/mdt/title", map[string]any{"title": strings.Repeat("x", 61)}); rr.Code != http.StatusBadRequest {
		t.Errorf("61 characters: status = %d, want 400", rr.Code)
	}
	if rr := do(t, mux, owner(), http.MethodPut, "/v1/me/mandates/mdt/title", map[string]any{}); rr.Code != http.StatusBadRequest {
		t.Errorf("no title field: status = %d, want 400", rr.Code)
	}
	if ms, _ := store.List(ctx, "t1"); len(ms) != 1 || ms[0].Title != "Refunds" {
		t.Errorf("a refused change changed the title: %+v", ms)
	}
}

// An agent reads its own Cards' titles, so Coach can name them as the
// owner does.
func TestAgentCardCarriesTheTitle(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	for _, m := range []mandate.Mandate{
		{ID: "mdt-named", TenantID: "t1", Subject: "claude", Domain: "gateway", Grants: []mandate.Grant{{Action: "refund"}}, Title: "Refunds on Stripe"},
		{ID: "mdt-plain", TenantID: "t1", Subject: "claude", Domain: "gateway", Grants: []mandate.Grant{{Action: "read"}}},
	} {
		if err := store.Issue(ctx, m, "owner"); err != nil {
			t.Fatal(err)
		}
	}
	rr := do(t, mux, agentKey("claude", ""), http.MethodGet, "/v1/me/card", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Cards []struct {
			ID    string  `json:"id"`
			Title *string `json:"title"`
		} `json:"cards"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	titles := map[string]*string{}
	for _, c := range got.Cards {
		titles[c.ID] = c.Title
	}
	if titles["mdt-named"] == nil || *titles["mdt-named"] != "Refunds on Stripe" {
		t.Errorf("named Card's title = %v", titles["mdt-named"])
	}
	if t2, ok := titles["mdt-plain"]; !ok || t2 != nil {
		t.Errorf("plain Card: present=%v title=%v", ok, t2)
	}
}
