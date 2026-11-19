package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/personlang"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/words"
)

// The membership emails come in each recipient's language: the one their
// app last showed them, else the account's last one, else English. An
// invitation, to someone with no language yet, comes in the account's.
func TestMembershipEmailsAreInEachRecipientsLanguage(t *testing.T) {
	f := newMembersFixture(t)
	f.h.tenants = oneTenant{t: tenant.Tenant{ID: "t1", Slug: "acme", Metadata: map[string]any{tenant.MetadataKeyAccountName: "Acme",
		personlang.MetadataKey: map[string]any{"people": map[string]any{"carol": "de", "dave": "pl"}, "last": "fr"},
	}}}
	for _, p := range []struct{ subject, role string }{{"carol", "approver"}, {"dave", "approver"}, {"erin", "approver"}} {
		tok := f.invite(p.subject+"@x.io", p.role)
		if _, err := f.svc.AcceptInvite(context.Background(), tok, membership.Actor{Subject: p.subject}); err != nil {
			t.Fatal(err)
		}
	}

	// The invitation: the account's language (French), and the inviter as
	// its admin, in French.
	f.notifier.sent = nil
	f.invite("zoe@x.io", "viewer")
	if len(f.notifier.sent) != 1 || f.notifier.sent[0].Lang != words.FR || f.notifier.sent[0].InvitedBy != "Un administrateur" {
		t.Fatalf("invitation = %+v", f.notifier.sent)
	}

	// A new approver: the role in each reader's word (the glossary's).
	f.notifier.notices = nil
	f.invite("frank@x.io", "approver")
	roles := map[string]string{
		"carol@x.io": "Neu als Genehmiger in Acme",
		"dave@x.io":  "Nowa rola na koncie Acme: zatwierdzający",
		"erin@x.io":  "Nouveau rôle sur Acme : approbateur",
	}
	if len(f.notifier.notices) != len(roles) {
		t.Fatalf("authority notices = %v", f.notifier.notices)
	}
	for _, n := range f.notifier.notices {
		to, subject, _ := strings.Cut(n, "|")
		if roles[to] != subject {
			t.Errorf("%s got %q, want %q", to, subject, roles[to])
		}
	}

	// Separation of duties off: each approver in their own language, and
	// erin, who has none, in the account's.
	alice := session("alice", membership.RoleAdmin, true)
	if rr := do(t, f.mux, alice, http.MethodPut, "/v1/tenant/governance", map[string]any{"separation_of_duties": true}); rr.Code != http.StatusOK {
		t.Fatalf("on: %d", rr.Code)
	}
	f.notifier.notices, f.notifier.bodies = nil, nil
	if rr := do(t, f.mux, alice, http.MethodPut, "/v1/tenant/governance", map[string]any{"separation_of_duties": false}); rr.Code != http.StatusOK {
		t.Fatalf("off: %d", rr.Code)
	}
	want := map[string]string{
		"carol@x.io": "Die Funktionstrennung wurde ausgeschaltet",
		"dave@x.io":  "Rozdział obowiązków został wyłączony",
		"erin@x.io":  "La séparation des tâches a été désactivée",
	}
	if len(f.notifier.notices) != len(want) {
		t.Fatalf("notices = %v", f.notifier.notices)
	}
	for i, n := range f.notifier.notices {
		to, subject, _ := strings.Cut(n, "|")
		if want[to] != subject {
			t.Errorf("%s got %q, want %q", to, subject, want[to])
		}
		if !strings.Contains(f.notifier.bodies[i], "alice@x.io") || !strings.Contains(f.notifier.bodies[i], "Acme") {
			t.Errorf("%s body %q: want who and the account", to, f.notifier.bodies[i])
		}
	}
}

// oneTenant is a tenant store that reads one account, whatever the id.
type oneTenant struct {
	tenant.Store
	t tenant.Tenant
}

func (o oneTenant) Get(context.Context, string) (tenant.Tenant, error) { return o.t, nil }
