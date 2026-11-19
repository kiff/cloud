package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

type activityResp struct {
	Calls  []proposals.CallRow  `json:"calls"`
	Counts proposals.CallCounts `json:"counts"`
	Shown  int                  `json:"shown"`
	Agents []string             `json:"agents"`
}

func activityRig(t *testing.T, id auth.Identity, outcomes *fakeOutcomes) (http.Handler, *proposals.MemoryReader) {
	t.Helper()
	reader := proposals.NewMemoryReader()
	opts := []handlers.ControlsOption{handlers.WithControlsDecisionsReader(reader)}
	if outcomes != nil {
		opts = append(opts, handlers.WithControlsToolOutcomes(outcomes))
	}
	h := handlers.NewControlsHandler(domain.NewMemoryControlStore(), nil, opts...)
	inner := http.NewServeMux()
	h.Register(inner)
	return withFakeIdentity(inner, id), reader
}

func getActivity(t *testing.T, h http.Handler, query string) (int, activityResp) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/activity"+query, nil))
	var out activityResp
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, out
}

// Activity lists calls across every domain, one row a call: a
// call held for its owner and then allowed is one row, its latest
// decision, under Held; the filters count every call and page.
func TestActivity_OneRowPerCallAcrossDomains(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	outcomes := &fakeOutcomes{m: map[string]tooloutcome.Outcome{
		"p-held": {Status: tooloutcome.Done, At: at.Add(5 * time.Minute)},
		"p-ok":   {Status: tooloutcome.Done, At: at.Add(time.Minute)},
	}}
	h, reader := activityRig(t, auth.Identity{Tenant: "t1", Subject: "owner", Method: auth.MethodSession}, outcomes)
	add := func(pid, actor, action, outcome, control string, ago time.Duration) {
		reader.Append("t1", proposals.DecisionRow{ProposalID: pid, ActorID: actor, ActionName: action, Outcome: outcome, ControlID: control, CreatedAt: at.Add(-ago)})
	}
	add("p-held", "luna-desk", "shop_refund_order", "owner_required", "ctrl-gateway", 3*time.Hour)
	add("p-held", "luna-desk", "shop_refund_order", "allowed", "ctrl-gateway", time.Hour)
	add("p-ok", "luna-desk", "shop_get_order", "allowed", "ctrl-gateway", 2*time.Hour)
	add("p-no", "onboard-agent", "refund_order", "blocked", "ctrl-refund", 30*time.Minute)
	reader.Append("t2", proposals.DecisionRow{ProposalID: "other", ActorID: "x", Outcome: "allowed", CreatedAt: at})

	code, all := getActivity(t, h, "")
	if code != 200 || len(all.Calls) != 3 || all.Shown != 3 {
		t.Fatalf("all: %d %+v", code, all)
	}
	if all.Calls[0].ProposalID != "p-no" || all.Calls[1].ProposalID != "p-held" || all.Calls[2].ProposalID != "p-ok" {
		t.Errorf("not newest first, one row a call: %+v", all.Calls)
	}
	held := all.Calls[1]
	if !held.Held || held.Decisions != 2 || held.Outcome != "allowed" || held.ToolOutcome == nil {
		t.Errorf("held then allowed: %+v", held)
	}
	if all.Calls[0].ToolOutcome != nil {
		t.Error("a refused call has a tool outcome")
	}
	if all.Counts != (proposals.CallCounts{All: 3, Allowed: 1, Refused: 1, Held: 1}) {
		t.Errorf("counts = %+v", all.Counts)
	}
	for show, want := range map[string]string{"held": "p-held", "allowed": "p-ok", "refused": "p-no"} {
		if _, got := getActivity(t, h, "?show="+show); len(got.Calls) != 1 || got.Calls[0].ProposalID != want || got.Shown != 1 || got.Counts.All != 3 {
			t.Errorf("show=%s: %+v", show, got)
		}
	}
	// The agents are everyone with a call, whatever the filter.
	if _, got := getActivity(t, h, "?actor=luna-desk"); len(got.Calls) != 2 || got.Counts.All != 2 || strings.Join(got.Agents, ",") != "luna-desk,onboard-agent" {
		t.Errorf("actor: %+v", got)
	}
	if _, got := getActivity(t, h, "?limit=1&offset=1"); len(got.Calls) != 1 || got.Calls[0].ProposalID != "p-held" || got.Shown != 3 {
		t.Errorf("page 2 of 1: %+v", got)
	}
	if _, got := getActivity(t, h, "?proposal=p-held"); len(got.Calls) != 1 || got.Calls[0].ProposalID != "p-held" || !got.Calls[0].Held {
		t.Errorf("one call: %+v", got)
	}
	if code, _ := getActivity(t, h, "?show=bogus"); code != http.StatusBadRequest {
		t.Errorf("bad show = %d", code)
	}
}

// An answered call keeps its place by when it was made (made_at), not its
// latest decision, and a call that wasn't sent says why in the reader's
// language (kiff/tafs#138, the owner's iPad, 21 Oct): the Record read only
// "Not sent", and an approved card jumped below the taf's message.
func TestActivity_RowsSayWhenAndWhy(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 21, 17, 11, 0, 0, time.UTC)
	h, reader := activityRig(t, auth.Identity{Tenant: "t1", Subject: "owner", Method: auth.MethodSession}, nil)
	reader.Append("t1", proposals.DecisionRow{ProposalID: "p-held", ActorID: "a", Outcome: "owner_required", CreatedAt: at})
	reader.Append("t1", proposals.DecisionRow{ProposalID: "p-held", ActorID: "a", Outcome: "blocked", Reasons: []string{"mandate_owner_rejected"}, CreatedAt: at.Add(time.Minute)})
	reader.Append("t1", proposals.DecisionRow{ProposalID: "p-unit", ActorID: "a", Outcome: "blocked", Reasons: []string{"amount_unit_unverified"}, CreatedAt: at.Add(2 * time.Minute)})
	reader.Append("t1", proposals.DecisionRow{ProposalID: "p-odd", ActorID: "a", Outcome: "blocked", Reasons: []string{"something_else"}, CreatedAt: at.Add(3 * time.Minute)})
	reader.Append("t1", proposals.DecisionRow{ProposalID: "p-ok", ActorID: "a", Outcome: "allowed", Reasons: []string{"mandate_owner_rejected"}, CreatedAt: at.Add(4 * time.Minute)})
	read := func(lang string) map[string]proposals.CallRow {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/me/activity", nil)
		req.Header.Set("Accept-Language", lang)
		h.ServeHTTP(rec, req)
		var out activityResp
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		m := map[string]proposals.CallRow{}
		for _, c := range out.Calls {
			m[c.ProposalID] = c
		}
		return m
	}
	en := read("en-GB")
	if c := en["p-held"]; !c.MadeAt.Equal(at) || !c.CreatedAt.Equal(at.Add(time.Minute)) || c.Reason != "You refused it." {
		t.Errorf("answered: %+v", c)
	}
	if en["p-unit"].Reason != "KIFF couldn't check its currency." || en["p-odd"].Reason != "" || en["p-ok"].Reason != "" {
		t.Errorf("reasons: %q %q %q", en["p-unit"].Reason, en["p-odd"].Reason, en["p-ok"].Reason)
	}
	es := read("es-ES,es;q=0.9,en;q=0.8")
	if es["p-held"].Reason != "La rechazaste." {
		t.Errorf("es: %q", es["p-held"].Reason)
	}
	// Its line too: what came of it, in the reader's language.
	if es["p-held"].Detail != "No enviado · La rechazaste." || en["p-held"].Detail != "Not sent · You refused it." {
		t.Errorf("detail: es %q, en %q", es["p-held"].Detail, en["p-held"].Detail)
	}
	if de := read("de"); de["p-odd"].Detail != "Nicht gesendet" {
		t.Errorf("de detail: %q", de["p-odd"].Detail)
	}
}

// The whole account's record is a person's to read, not a key's.
func TestActivity_RefusesKeys(t *testing.T) {
	t.Parallel()
	for name, id := range map[string]auth.Identity{
		"tenant key": {Tenant: "t1", Subject: "k", APIKey: true, Method: auth.MethodAPIKey},
		"domain key": {Tenant: "t1", Subject: "k", DomainID: "dom-1"},
	} {
		h, _ := activityRig(t, id, nil)
		if code, _ := getActivity(t, h, ""); code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, code)
		}
	}
	h, _ := activityRig(t, auth.Identity{Tenant: "t1", Subject: "owner"}, nil)
	if code, got := getActivity(t, h, ""); code != 200 || got.Calls == nil {
		t.Errorf("empty account: %d %+v", code, got)
	}
}

// A read-only account key reads the account's activity; the same role on
// an agent's key, or a full account key, is still refused here.
func TestActivity_ReadOnlyAccountKeyReads(t *testing.T) {
	t.Parallel()
	readOnly := auth.Identity{Tenant: "t1", Subject: "owner", APIKey: true, Method: auth.MethodAPIKey, Roles: []string{auth.RoleAccountReader}}
	h, _ := activityRig(t, readOnly, nil)
	if code, got := getActivity(t, h, ""); code != http.StatusOK || got.Calls == nil {
		t.Errorf("read-only account key: %d %+v", code, got)
	}
	for name, id := range map[string]auth.Identity{
		"viewer-role agent key": {Tenant: "t1", Subject: "k", APIKey: true, Method: auth.MethodAPIKey, Roles: []string{auth.RoleAccountReader}, AgentID: "agent-a"},
		"admin account key":     {Tenant: "t1", Subject: "k", APIKey: true, Method: auth.MethodAPIKey, Roles: []string{"admin"}},
	} {
		h, _ := activityRig(t, id, nil)
		if code, _ := getActivity(t, h, ""); code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, code)
		}
	}
}

// a review: the counts cover every call, so every call is
// reachable: a filtered history past 10,000 pages on across it.
func TestActivity_PagesPastTenThousand(t *testing.T) {
	t.Parallel()
	h, reader := activityRig(t, auth.Identity{Tenant: "t1", Subject: "owner"}, nil)
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	const allowed = 10100
	for i := 0; i < allowed; i++ {
		reader.Append("t1", proposals.DecisionRow{ProposalID: fmt.Sprintf("p-%05d", i), ActorID: "a", Outcome: "allowed", CreatedAt: at.Add(-time.Duration(i) * time.Second)})
	}
	reader.Append("t1", proposals.DecisionRow{ProposalID: "refused", ActorID: "a", Outcome: "blocked", CreatedAt: at})
	seen := map[string]bool{}
	for _, offset := range []int{10000, 10050} {
		_, page := getActivity(t, h, fmt.Sprintf("?show=allowed&limit=50&offset=%d", offset))
		if len(page.Calls) != 50 || page.Shown != allowed {
			t.Fatalf("offset %d: %d calls, shown %d", offset, len(page.Calls), page.Shown)
		}
		if want := fmt.Sprintf("p-%05d", offset); page.Calls[0].ProposalID != want {
			t.Errorf("offset %d starts at %s, want %s", offset, page.Calls[0].ProposalID, want)
		}
		for _, c := range page.Calls {
			if seen[c.ProposalID] {
				t.Fatalf("offset %d repeats %s", offset, c.ProposalID)
			}
			seen[c.ProposalID] = true
		}
	}
	if _, past := getActivity(t, h, "?show=allowed&limit=50&offset=10100"); len(past.Calls) != 0 {
		t.Errorf("past the end: %d calls", len(past.Calls))
	}
}

// titlesFake is the gateway's tool titles, by tool name.
type titlesFake map[string]string

func (f titlesFake) ToolTitles(context.Context, string) (map[string]string, error) { return f, nil }

// Each activity row says its tool's title, as Cards and held calls do
// (kiff/tafs#102); a row without one shows its action.
func TestActivity_RowsSayTheToolsTitle(t *testing.T) {
	t.Parallel()
	reader := proposals.NewMemoryReader()
	h := handlers.NewControlsHandler(domain.NewMemoryControlStore(), nil,
		handlers.WithControlsDecisionsReader(reader), handlers.WithControlsToolTitles(titlesFake{"create_refund_k3f9": "Refund a payment"}))
	inner := http.NewServeMux()
	h.Register(inner)
	srv := withFakeIdentity(inner, auth.Identity{Tenant: "t1", Subject: "owner", Method: auth.MethodSession})
	at := time.Date(2026, 10, 19, 19, 0, 0, 0, time.UTC)
	reader.Append("t1", proposals.DecisionRow{ProposalID: "p1", ActionName: "create_refund_k3f9", Outcome: "allowed", CreatedAt: at})
	reader.Append("t1", proposals.DecisionRow{ProposalID: "p2", ActionName: "refund_order", Outcome: "allowed", CreatedAt: at.Add(-time.Minute)})
	_, got := getActivity(t, srv, "")
	if len(got.Calls) != 2 || got.Calls[0].Title != "Refund a payment" || got.Calls[1].Title != "" {
		t.Fatalf("titles: %+v", got.Calls)
	}
}
