package purchase

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// issue_card is allowed only with the exact arguments of the current,
// executing case, within its time: another case, digest, state or end, or a
// case not executing, is refused.
func TestACardIsAllowedOnlyForTheExecutingCasesExactArguments(t *testing.T) {
	c, digest := contractCase(t)
	now := time.Date(2026, 11, 28, 9, 15, 11, 0, time.UTC)
	r := Row{Case: c, Digest: digest, State: StateExecuting, LeaseHolder: "exe_1", ValidUntil: now.Add(15 * time.Minute)}
	args, err := CardArgs(r, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := AllowsCard(r, args, now); err != nil {
		t.Fatal(err)
	}
	var a IssueCardArgs
	_ = json.Unmarshal(args, &a)
	if a.Digest != digest || a.State != StateApproved || !a.ValidUntil.Equal(r.ValidUntil) {
		t.Fatalf("args %+v", a)
	}
	edit := func(f func(*IssueCardArgs, *Case)) json.RawMessage {
		var x IssueCardArgs
		_ = json.Unmarshal(args, &x)
		var cc Case
		_ = json.Unmarshal(x.Case, &cc)
		f(&x, &cc)
		x.Case, _ = json.Marshal(cc)
		b, _ := json.Marshal(x)
		return b
	}
	for name, bad := range map[string]json.RawMessage{
		"a higher cap":   edit(func(_ *IssueCardArgs, cc *Case) { cc.Grant.Cap++ }),
		"another case":   edit(func(_ *IssueCardArgs, cc *Case) { cc.CaseID = "pc_other" }),
		"held":           edit(func(x *IssueCardArgs, _ *Case) { x.State = StateHeld }),
		"a later end":    edit(func(x *IssueCardArgs, _ *Case) { x.ValidUntil = x.ValidUntil.Add(time.Hour) }),
		"another digest": edit(func(x *IssueCardArgs, _ *Case) { x.Digest = "00" }),
		"not json":       json.RawMessage(`[]`),
	} {
		if err := AllowsCard(r, bad, now); !errors.Is(err, ErrNotIssuable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	approved := r
	approved.State = StateApproved
	if err := AllowsCard(approved, args, now); !errors.Is(err, ErrNotIssuable) {
		t.Errorf("approved, no lease: %v", err)
	}
	if err := AllowsCard(r, args, r.ValidUntil); !errors.Is(err, ErrNotIssuable) {
		t.Errorf("at its end: %v", err)
	}
}
