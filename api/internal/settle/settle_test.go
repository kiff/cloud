package settle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

type exceptions []mandate.Exception

func (e exceptions) Exceptions(context.Context, string, mandate.ExceptionStatus) ([]mandate.Exception, error) {
	return e, nil
}

type answers map[string]membership.HoldAnswer

func (a answers) HoldAnswers(context.Context, string, []string) (map[string]membership.HoldAnswer, error) {
	return a, nil
}

type tools struct {
	m   map[string]tooloutcome.Outcome
	err error
}

func (t tools) Outcomes(context.Context, string, []string) (map[string]tooloutcome.Outcome, error) {
	return t.m, t.err
}

// Which holds are settled, and how each reads.
func TestSettled(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { x := now.Add(-d); return &x }
	ex := exceptions{
		{ID: "e-rej", ProposalID: "p-rej", Status: mandate.ExceptionRejected, ResolvedBy: "you (ai@kiff.dev)", ResolvedAt: ago(time.Hour),
			Refusal: &mandate.Refusal{NextStep: "return_first", Note: "Photos too."}},
		{ID: "e-exp", ProposalID: "p-exp", Status: mandate.ExceptionHeld, ExpiresAt: ago(time.Minute)}, // expired by now
		{ID: "e-wd", ProposalID: "p-wd", Status: mandate.ExceptionInvalidated, Reason: "card_revoked", ResolvedAt: ago(time.Hour)},
		{ID: "e-done", ProposalID: "p-done", Status: mandate.ExceptionConsumed, ResolvedBy: "bob", ResolvedAt: ago(time.Hour), ConsumedAt: ago(time.Minute)},
		{ID: "e-sending", ProposalID: "p-sending", Status: mandate.ExceptionConsumed, ResolvedAt: ago(time.Hour), ConsumedAt: ago(time.Hour)},
		{ID: "e-fresh", ProposalID: "p-fresh", Status: mandate.ExceptionConsumed, ResolvedAt: ago(time.Hour), ConsumedAt: ago(time.Minute)},
		{ID: "e-stale", ProposalID: "p-stale", Status: mandate.ExceptionConsumed, ResolvedAt: ago(time.Hour), ConsumedAt: ago(time.Hour)},
		{ID: "e-refit", ProposalID: "p-refit", Status: mandate.ExceptionChanged, Reason: "fits the card as it now stands", ResolvedAt: ago(time.Hour)},
		{ID: "e-held", ProposalID: "p-held", Status: mandate.ExceptionHeld},
		{ID: "e-appr", ProposalID: "p-appr", Status: mandate.ExceptionApproved, ResolvedAt: ago(time.Hour)},
		{ID: "e-noprop", Status: mandate.ExceptionRejected, ResolvedAt: ago(time.Hour)},
	}
	src := Source{Exceptions: ex, Now: func() time.Time { return now },
		Answers: answers{"e-rej": {Answer: "reject", Role: "admin"}, "e-done": {Answer: "reject", Role: "approver"}},
		Tools: tools{m: map[string]tooloutcome.Outcome{
			"p-done":    {Status: tooloutcome.Done, At: now.Add(-30 * time.Second)},
			"p-sending": {Status: tooloutcome.Sending, At: now},
		}}}
	got, err := src.Settled(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]string{}
	for _, s := range got {
		by[s.ProposalID] = s.Outcome
	}
	want := map[string]string{"p-rej": "rejected", "p-exp": "expired", "p-wd": "withdrawn", "p-done": "approved_once", "p-stale": "approved_once", "p-refit": "fit_on_retry"}
	if len(by) != len(want) {
		t.Errorf("settled = %v, want %v", by, want)
	}
	for p, o := range want {
		if by[p] != o {
			t.Errorf("%s = %q, want %q", p, by[p], o)
		}
	}
	for _, s := range got {
		switch s.ProposalID {
		case "p-rej":
			if s.Role != "admin" || s.NextStep != "return_first" || s.Note != "Photos too." || s.Answer != "reject" || s.SeparationOfDuties == nil {
				t.Errorf("rejection = %+v", s)
			}
		case "p-done":
			// An answer recorded for another kind of answer is not used.
			if s.Role != "" || s.Tool == nil || s.Tool.Status != "done" {
				t.Errorf("used approval = %+v", s)
			}
		case "p-stale":
			if s.Tool != nil {
				t.Errorf("no report recorded, yet a tool step: %+v", s)
			}
		case "p-exp":
			if s.AnsweredBy != "" || !s.At.Equal(now.Add(-time.Minute)) {
				t.Errorf("expired = %+v", s)
			}
		case "p-refit":
			if s.Answer != "" || s.AnsweredBy != "" {
				t.Errorf("fit on retry names an answer: %+v", s)
			}
		}
	}

	// A failed read of tool reports settles nothing rather than settle
	// calls as having none.
	src.Tools = tools{err: errors.New("down")}
	if _, err := src.Settled(context.Background(), "t1"); err == nil {
		t.Error("a failed tool read settled calls")
	}
}
