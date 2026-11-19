package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kiff/cloud/api/internal/attention"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/words"
)

// feedAttentionStore lists a fixed feed.
type feedAttentionStore struct {
	fakeAttentionStore
	feed attention.Feed
}

func (f feedAttentionStore) List(context.Context, string, string, int, int, bool) (attention.Feed, error) {
	items := append([]attention.Item(nil), f.feed.Items...)
	return attention.Feed{Items: items}, nil
}

// cardWarnings reads each Card's warnings; a Card in fail can't be read.
type cardWarnings struct {
	byCard map[string][]mandate.CardWarning
	fail   string
	reads  *int
}

func (c cardWarnings) CardWarnings(_ context.Context, _, card string) ([]mandate.CardWarning, error) {
	*c.reads++
	if card == c.fail {
		return nil, errors.New("down")
	}
	return c.byCard[card], nil
}

// A Card warning in Home reads in the person's language, composed from the
// warning; English keeps what was stored. A warning it can't find, or a
// Card whose warnings can't be read, keeps the stored line, and each Card
// is read once.
func TestHomeCardWarningInThePersonsLanguage(t *testing.T) {
	spent := mandate.CardWarning{ID: "w1", Kind: "spent", Used: 30, Ceiling: 30, Window: mandate.WindowRollingHour}
	stored := words.WarningReason(spent, words.EN)
	feed := attention.Feed{Items: []attention.Item{
		{ID: "card-warning:w1", Kind: "card_warning", SourceID: "m_1", Reason: stored},
		{ID: "card-warning:w9", Kind: "card_warning", SourceID: "m_1", Reason: "an old one"},
		{ID: "card-warning:w2", Kind: "card_warning", SourceID: "m_down", Reason: "unreadable"},
		{ID: "approval:a1", Kind: "approval", Reason: "an approval's reason"},
	}}
	reads := 0
	mux := http.NewServeMux()
	handlers.NewAttentionHandler(feedAttentionStore{feed: feed}).
		WithAttentionWarnings(cardWarnings{byCard: map[string][]mandate.CardWarning{"m_1": {spent}}, fail: "m_down", reads: &reads}).
		Register(mux)
	list := func(lang string) []attention.Item {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v1/me/attention", nil)
		req.Header.Set("Accept-Language", lang)
		req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{Subject: "bob", Tenant: "t1", Method: auth.MethodSession}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out attention.Feed
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		return out.Items
	}
	got := list("de")
	if got[0].Reason != words.WarningReason(spent, words.DE) || got[0].Reason == stored {
		t.Errorf("German warning: %q", got[0].Reason)
	}
	if got[1].Reason != "an old one" || got[2].Reason != "unreadable" || got[3].Reason != "an approval's reason" {
		t.Errorf("kept lines: %+v", got[1:])
	}
	if reads != 2 {
		t.Errorf("Cards read %d times, want once each (2)", reads)
	}
	reads = 0
	if got := list("en"); got[0].Reason != stored || reads != 0 {
		t.Errorf("English: %q, %d reads", got[0].Reason, reads)
	}
}
