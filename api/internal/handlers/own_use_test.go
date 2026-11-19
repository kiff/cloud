package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/proposals"
)

// askedBy is the meter's record of whose runs made which calls.
type askedBy map[string]map[string]bool

func (a askedBy) KeysAskedBy(_ context.Context, _, subject string) (map[string]bool, error) {
	if a[subject] == nil {
		return map[string]bool{}, nil
	}
	return a[subject], nil
}

// ownUseWorld is one account: Ada (admin), Ed (editor, who made Refunds),
// Paul (viewer) and Maria (approver), Refunds shared with Paul and Maria,
// and Bills, which Ada made. Paul's run, Maria's run and a schedule each
// made a Refunds call that was held; Bills made one too.
type ownUseWorld struct {
	activity *http.ServeMux
	holds    *http.ServeMux
	own      *OwnUse
}

func newOwnUseWorld(t *testing.T) *ownUseWorld {
	t.Helper()
	ctx := context.Background()
	members := membership.NewMemoryStore()
	if err := members.Mutate(ctx, "acme", func(tx membership.Tx) error {
		for subject, role := range map[string]membership.Role{"ada": membership.RoleAdmin, "ed": membership.RoleEditor,
			"paul": membership.RoleViewer, "maria": membership.RoleApprover} {
			if err := tx.InsertMember(membership.Member{TenantID: "acme", Subject: subject, Role: role, Status: membership.StatusActive}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	agentStore := agents.NewMemoryStore()
	for id, by := range map[string]string{"refunds": "ed", "bills": "ada"} {
		if err := agentStore.Create(ctx, agents.Agent{TenantID: "acme", ID: id, Kind: agents.KindTaf, CreatedAt: time.Now(), CreatedBy: by}); err != nil {
			t.Fatal(err)
		}
	}
	grants := access.NewMemoryStore()
	for _, s := range []string{"paul", "maria"} {
		if err := grants.AddGrant(ctx, access.Grant{TenantID: "acme", Agent: "refunds", Subject: s, CreatedAt: time.Now(), CreatedBy: "ed"}); err != nil {
			t.Fatal(err)
		}
	}
	own := NewOwnUse(NewAccessHandler(grants, members, agentStore, nil),
		askedBy{"paul": {"p-paul": true}, "maria": {"p-maria": true}})

	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	reader := proposals.NewMemoryReader()
	for i, c := range [][2]string{{"p-paul", "refunds"}, {"p-maria", "refunds"}, {"p-sched", "refunds"}, {"p-bills", "bills"}} {
		reader.Append("acme", proposals.DecisionRow{ProposalID: c[0], ActorID: c[1], Outcome: "allowed", CreatedAt: at.Add(time.Duration(i) * time.Minute)})
	}
	activity := http.NewServeMux()
	NewControlsHandler(domain.NewMemoryControlStore(), nil, WithControlsDecisionsReader(reader), WithControlsOwnUse(own)).Register(activity)

	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return at })
	for _, m := range []mandate.Mandate{
		{ID: "card-r", Subject: "refunds", Domain: "gateway", Grants: []mandate.Grant{{Action: "refund"}}},
		{ID: "card-b", Subject: "bills", Domain: "gateway", Grants: []mandate.Grant{{Action: "refund"}}},
	} {
		m.TenantID = "acme"
		if err := store.Issue(ctx, m, "ada"); err != nil {
			t.Fatal(err)
		}
	}
	expires := at.Add(24 * time.Hour)
	for _, e := range []mandate.Exception{
		{ID: "exc-paul", ProposalID: "p-paul", Subject: "refunds", Request: mandate.Request{AskedBy: "paul"}},
		{ID: "exc-maria", ProposalID: "p-maria", Subject: "refunds", Request: mandate.Request{AskedBy: "maria"}},
		{ID: "exc-sched", ProposalID: "p-sched", Subject: "refunds"},
		{ID: "exc-bills", ProposalID: "p-bills", Subject: "bills"},
	} {
		card := "card-r"
		if e.Subject == "bills" {
			card = "card-b"
		}
		e.TenantID, e.Action, e.Status, e.CreatedAt, e.ExpiresAt = "acme", "refund", mandate.ExceptionHeld, at, &expires
		e.Breaches = []mandate.Breach{{MandateID: card}}
		if err := store.SaveException(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	holds := http.NewServeMux()
	NewMandatesHandler(store, nil, WithMandatesClock(func() time.Time { return at.Add(time.Hour) }), WithMandatesOwnUse(own)).Register(holds)
	return &ownUseWorld{activity: activity, holds: holds, own: own}
}

func ownUsePerson(subject string, role membership.Role) auth.Identity {
	return auth.Identity{Subject: subject, Tenant: "acme", Method: auth.MethodSession, MemberRole: role, Roles: membership.IdentityRoles(role)}
}

func (w *ownUseWorld) calls(t *testing.T, id auth.Identity) (ids, agentList []string, all int) {
	t.Helper()
	rr := do(t, w.activity, id, http.MethodGet, "/v1/me/activity", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("%s activity: %d %s", id.Subject, rr.Code, rr.Body.String())
	}
	var out struct {
		Calls  []proposals.CallRow  `json:"calls"`
		Counts proposals.CallCounts `json:"counts"`
		Agents []string             `json:"agents"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, c := range out.Calls {
		ids = append(ids, c.ProposalID)
	}
	sort.Strings(ids)
	return ids, out.Agents, out.Counts.All
}

func (w *ownUseWorld) holdIDs(t *testing.T, id auth.Identity) []string {
	t.Helper()
	rr := do(t, w.holds, id, http.MethodGet, "/v1/me/card-exceptions", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("%s holds: %d %s", id.Subject, rr.Code, rr.Body.String())
	}
	ids := exceptionIDs(t, rr.Body.Bytes())
	sort.Strings(ids)
	return ids
}

// Invariant: sharing a taf lets someone use it, not read the account (RFC
// 050 decision 12). A person reads every call and hold of the tafs they
// manage, their own runs' calls, and, when they may answer holds, the holds
// of the tafs they use; an admin, or a person on an account from before
// roles, reads all of it.
// Category: privacy
// Attack: a viewer a taf is shared with reads the account's Record, or another person's held call by id
// Refused: KIFF scopes the Record and held calls to the reader; another person's hold reads as missing
func TestEachPersonReadsTheirOwnUse(t *testing.T) {
	w := newOwnUseWorld(t)
	ada, ed := ownUsePerson("ada", membership.RoleAdmin), ownUsePerson("ed", membership.RoleEditor)
	paul, maria := ownUsePerson("paul", membership.RoleViewer), ownUsePerson("maria", membership.RoleApprover)
	legacy := auth.Identity{Subject: "owner", Tenant: "acme", Method: auth.MethodSession, Roles: []string{"admin"}}

	for _, c := range []struct {
		who          auth.Identity
		calls, holds string
		agents       string
	}{
		{ada, "p-bills,p-maria,p-paul,p-sched", "exc-bills,exc-maria,exc-paul,exc-sched", "bills,refunds"},
		{legacy, "p-bills,p-maria,p-paul,p-sched", "exc-bills,exc-maria,exc-paul,exc-sched", "bills,refunds"},
		// Ed manages Refunds, which he made, and not Bills.
		{ed, "p-maria,p-paul,p-sched", "exc-maria,exc-paul,exc-sched", "refunds"},
		// Paul, a viewer, reads his own run's call and hold only.
		{paul, "p-paul", "exc-paul", "refunds"},
		// Maria, an approver, reads her own run's calls, and every hold of
		// Refunds, which she uses and may answer: answering is her job.
		{maria, "p-maria", "exc-maria,exc-paul,exc-sched", "refunds"},
	} {
		ids, agentList, all := w.calls(t, c.who)
		sort.Strings(agentList)
		if fmt.Sprint(ids) != fmt.Sprint(splitIDs(c.calls)) || all != len(ids) || fmt.Sprint(agentList) != fmt.Sprint(splitIDs(c.agents)) {
			t.Errorf("%s reads calls %v (counted %d), agents %v; want %s, agents %s", c.who.Subject, ids, all, agentList, c.calls, c.agents)
		}
		if got := w.holdIDs(t, c.who); fmt.Sprint(got) != fmt.Sprint(splitIDs(c.holds)) {
			t.Errorf("%s reads holds %v, want %s", c.who.Subject, got, c.holds)
		}
	}

	// Counted tasks (Usage): Paul's own; Ed's and every task of Refunds; Ada's all.
	for _, c := range []struct {
		who  auth.Identity
		want string
	}{{paul, "&{paul []}"}, {ed, "&{ed [refunds]}"}, {ada, "<nil>"}} {
		s, err := w.own.Scope(context.Background(), c.who, "acme")
		if err != nil || fmt.Sprint(s.Tasks()) != c.want {
			t.Errorf("%s's tasks scope %v (%v), want %s", c.who.Subject, s.Tasks(), err, c.want)
		}
	}

	// Another person's held call, by its id, reads as missing; one's own reads.
	if rr := do(t, w.holds, paul, http.MethodGet, "/v1/me/card-exceptions/exc-maria", nil); rr.Code != http.StatusNotFound {
		t.Errorf("paul read maria's hold: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, w.holds, paul, http.MethodGet, "/v1/me/card-exceptions/exc-paul", nil); rr.Code != http.StatusOK {
		t.Errorf("paul's own hold: %d %s", rr.Code, rr.Body.String())
	}
}

func splitIDs(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}
