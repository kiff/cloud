package handlers_test

import (
	"context"
	"errors"
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

type fakeReleased struct {
	m   map[string]bool
	err error
}

func (f fakeReleased) ReleasedProposals(_ context.Context, _ string, ids []string) (map[string]bool, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]bool{}
	for _, id := range ids {
		if f.m[id] {
			out[id] = true
		}
	}
	return out, nil
}

func countedRig(t *testing.T, released handlers.ControlsOption) http.Handler {
	t.Helper()
	at := time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC)
	outcomes := &fakeOutcomes{m: map[string]tooloutcome.Outcome{
		"p-released":    {Status: tooloutcome.RefusedByTool, Message: "no mailbox", At: at},
		"p-counted":     {Status: tooloutcome.RefusedByTool, Message: "no mailbox", At: at},
		"p-notsent":     {Status: tooloutcome.NotSent, At: at},
		"p-done":        {Status: tooloutcome.Done, At: at},
		"p-done-oddity": {Status: tooloutcome.Done, At: at},
	}}
	reader := proposals.NewMemoryReader()
	for _, id := range []string{"p-released", "p-counted", "p-notsent", "p-done", "p-done-oddity"} {
		reader.Append("t1", proposals.DecisionRow{ProposalID: id, ActorID: "gmail-sender", ActionName: "send", Outcome: "allowed", ControlID: "ctrl-gateway", CreatedAt: at})
	}
	opts := []handlers.ControlsOption{handlers.WithControlsDecisionsReader(reader), handlers.WithControlsToolOutcomes(outcomes)}
	if released != nil {
		opts = append(opts, released)
	}
	h := handlers.NewControlsHandler(domain.NewMemoryControlStore(), nil, opts...)
	inner := http.NewServeMux()
	h.Register(inner)
	return withFakeIdentity(inner, auth.Identity{Tenant: "t1", Subject: "owner", Method: auth.MethodSession})
}

func countedOf(rows []proposals.CallRow) map[string]*bool {
	out := map[string]*bool{}
	for _, r := range rows {
		if r.ToolOutcome != nil {
			out[r.ProposalID] = r.ToolOutcome.Counted
		}
	}
	return out
}

// tool_outcome.counted is false only for a call the tool refused, or that was
// never sent, when a Card gave its room back (a release exists): the apps then
// say it doesn't count toward the day's limit. Absent for every other call,
// including a finished call that has a release on record, and when releases
// can't be read: the apps keep today's line.
//
// Category: honesty
// Attack: a refused call shown as "doesn't count" with no release, and a finished call marked uncounted
// Refused: counted:false needs a release and a refused or never-sent call; the tool's own words never set it
func TestActivity_CountedIsFalseOnlyForAReleasedRefusedCall(t *testing.T) {
	t.Parallel()
	released := fakeReleased{m: map[string]bool{"p-released": true, "p-notsent": true, "p-done": true}}
	h := countedRig(t, handlers.WithControlsReleases(released))
	code, got := getActivity(t, h, "")
	if code != 200 || len(got.Calls) != 5 {
		t.Fatalf("activity: %d, %d calls", code, len(got.Calls))
	}
	c := countedOf(got.Calls)
	for _, id := range []string{"p-released", "p-notsent"} {
		if c[id] == nil || *c[id] {
			t.Errorf("%s: counted = %v, want false", id, c[id])
		}
	}
	for _, id := range []string{"p-counted", "p-done", "p-done-oddity"} {
		if c[id] != nil {
			t.Errorf("%s: counted = %v, want absent", id, *c[id])
		}
	}
	// The wire says nothing for a call that counts.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/activity?proposal=p-counted", nil))
	if body := rec.Body.String(); strings.Contains(body, `"counted"`) {
		t.Errorf("a counted call's row names counted: %s", body)
	}
}

// Without the read, or when it fails, the rows are as before and the page
// still answers.
func TestActivity_CountedIsAbsentWithoutReleases(t *testing.T) {
	t.Parallel()
	for name, opt := range map[string]handlers.ControlsOption{
		"no reader":     nil,
		"reader failed": handlers.WithControlsReleases(fakeReleased{err: errors.New("down")}),
	} {
		h := countedRig(t, opt)
		code, got := getActivity(t, h, "")
		if code != 200 || len(got.Calls) != 5 {
			t.Fatalf("%s: %d, %d calls", name, code, len(got.Calls))
		}
		for id, c := range countedOf(got.Calls) {
			if c != nil {
				t.Errorf("%s: %s has counted %v", name, id, *c)
			}
		}
	}
}
