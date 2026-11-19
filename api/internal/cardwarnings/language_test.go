package cardwarnings

import (
	"context"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/owneremail"
	"github.com/kiff/cloud/api/internal/personlang"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/words"
)

const langTenant = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

type langMailer struct {
	to, text []string
	langs    []words.Lang
}

func (m *langMailer) CardBudgetWarning(_ context.Context, to, _, text, _ string, lang words.Lang) error {
	m.to, m.text, m.langs = append(m.to, to), append(m.text, text), append(m.langs, lang)
	return nil
}

type langRecipients struct{ people []owneremail.Recipient }

func (r langRecipients) HoldRecipients(context.Context, string) ([]string, error) {
	out := []string{}
	for _, p := range r.people {
		out = append(out, p.Email)
	}
	return out, nil
}

func (r langRecipients) HoldRecipientsWithSubjects(context.Context, string) ([]owneremail.Recipient, error) {
	return r.people, nil
}

// Each warning email is in its recipient's language, its reason too; an
// address that is no member's reads the account's last language; without
// the account's store, English.
func TestAWarningEmailIsInItsRecipientsLanguage(t *testing.T) {
	store := tenant.NewMemoryStore()
	if err := store.Create(context.Background(), tenant.Tenant{ID: langTenant, Slug: "acme"}); err != nil {
		t.Fatal(err)
	}
	rec := &personlang.Recorder{Tenants: store, Go: func(f func()) { f() }}
	rec.Note(langTenant, "ana", words.DE)
	rec.Note(langTenant, "bo", words.FR)
	mail := &langMailer{}
	w := &Worker{Mailer: mail, Tenants: store,
		Recipients: langRecipients{people: []owneremail.Recipient{{Email: "ana@example.com", Subject: "ana"}, {Email: "bo@example.com", Subject: "bo"}}}}
	e := mandate.CardWarning{ID: "cw_1", TenantID: langTenant, CardID: "card-1", Kind: "spent", Used: 30, Ceiling: 30, Window: mandate.WindowCalendarDay}
	for _, to := range []string{"email:ana@example.com", "email:bo@example.com", "email:team@example.com"} {
		if err := w.send(context.Background(), e, to); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range []words.Lang{words.DE, words.FR, words.FR} {
		if mail.langs[i] != want || mail.text[i] != words.WarningReason(e, want) {
			t.Errorf("%s: %s %q, want %s", mail.to[i], mail.langs[i], mail.text[i], want)
		}
	}
	if !strings.HasPrefix(mail.text[0], "Diese Summe") {
		t.Errorf("German reason: %q", mail.text[0])
	}
	w.Tenants = nil
	if err := w.send(context.Background(), e, "email:ana@example.com"); err != nil {
		t.Fatal(err)
	}
	if mail.langs[3] != words.EN || mail.text[3] != e.Reason && mail.text[3] != words.WarningReason(e, words.EN) {
		t.Errorf("without the store: %s %q", mail.langs[3], mail.text[3])
	}
}
