package personlang

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/words"
)

// tid is a valid account id (a ULID).
const tid = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func get(t *testing.T, s *tenant.MemoryStore) tenant.Tenant {
	t.Helper()
	got, err := s.Get(context.Background(), tid)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// A person's signed-in requests keep their app's language for the emails
// KIFF writes them later; an agent's key never does, and the account's
// last language serves an address that is no one person's.
func TestRecorderKeepsEachPersonsLastLanguage(t *testing.T) {
	store := tenant.NewMemoryStore()
	if err := store.Create(context.Background(), tenant.Tenant{ID: tid, Slug: "acme"}); err != nil {
		t.Fatal(err)
	}
	r := &Recorder{Tenants: store, Go: func(f func()) { f() }}
	h := r.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	send := func(id auth.Identity, lang string) {
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		req.Header.Set("Accept-Language", lang)
		h.ServeHTTP(httptest.NewRecorder(), req.WithContext(auth.WithIdentity(req.Context(), id)))
	}
	if Of(get(t, store), "ana") != words.EN || Account(get(t, store)) != words.EN {
		t.Fatal("English until a language is seen")
	}
	send(auth.Identity{Subject: "ana", Tenant: tid, Method: auth.MethodDeveloper}, "de-AT, en;q=0.5")
	send(auth.Identity{Subject: "bo", Tenant: tid, Method: auth.MethodSession}, "fr")
	send(auth.Identity{Subject: "agent-1", Tenant: tid, Method: auth.MethodAPIKey, APIKey: true}, "pl")
	got := get(t, store)
	if Of(got, "ana") != words.DE || Of(got, "bo") != words.FR || Account(got) != words.FR {
		t.Fatalf("ana %s, bo %s, account %s", Of(got, "ana"), Of(got, "bo"), Account(got))
	}
	if Of(got, "agent-1") != words.FR {
		t.Fatal("an agent key's language was kept")
	}
	// Someone not yet seen reads the account's last language.
	if Of(got, "cy") != words.FR {
		t.Fatal("unknown person")
	}
	// Ana's app now ships Polish: her language follows, the others stay.
	send(auth.Identity{Subject: "ana", Tenant: tid, Method: auth.MethodDeveloper}, "pl")
	got = get(t, store)
	if Of(got, "ana") != words.PL || Of(got, "bo") != words.FR {
		t.Fatalf("after a change: ana %s, bo %s", Of(got, "ana"), Of(got, "bo"))
	}
}

// The same language again writes nothing.
func TestRecorderWritesOnlyOnAChange(t *testing.T) {
	writes := 0
	store := &counting{MemoryStore: tenant.NewMemoryStore(), n: &writes}
	if err := store.Create(context.Background(), tenant.Tenant{ID: tid, Slug: "acme"}); err != nil {
		t.Fatal(err)
	}
	r := &Recorder{Tenants: store, Go: func(f func()) { f() }}
	for i := 0; i < 5; i++ {
		r.Note(tid, "ana", words.DE)
	}
	r.Note(tid, "ana", words.IT)
	if writes != 2 {
		t.Fatalf("%d writes, want 2", writes)
	}
}

type counting struct {
	*tenant.MemoryStore
	n *int
}

func (c *counting) SetMetadata(ctx context.Context, id, key string, value any) error {
	*c.n++
	return c.MemoryStore.SetMetadata(ctx, id, key, value)
}

// Past MaxPeople, a new person isn't kept (they read the account's last
// language), and the account's last language still follows.
func TestRecorderKeepsAtMostMaxPeople(t *testing.T) {
	store := tenant.NewMemoryStore()
	if err := store.Create(context.Background(), tenant.Tenant{ID: tid, Slug: "acme"}); err != nil {
		t.Fatal(err)
	}
	people := map[string]any{}
	for i := 0; i < MaxPeople; i++ {
		people[fmt.Sprintf("p%d", i)] = "de"
	}
	if err := store.SetMetadata(context.Background(), tid, MetadataKey, map[string]any{"people": people, "last": "de"}); err != nil {
		t.Fatal(err)
	}
	r := &Recorder{Tenants: store, Go: func(f func()) { f() }}
	r.Note(tid, "newcomer", words.IT)
	got := get(t, store)
	kept, _ := read(got)
	if _, ok := kept["newcomer"]; ok || len(kept) != MaxPeople {
		t.Fatalf("kept %d people, newcomer kept %v", len(kept), ok)
	}
	if Account(got) != words.IT || Of(got, "newcomer") != words.IT {
		t.Fatalf("the account's last language didn't follow: %s", Account(got))
	}
	r.Note(tid, "p1", words.FR) // someone already kept still updates
	if Of(get(t, store), "p1") != words.FR {
		t.Fatal("a kept person's change was dropped")
	}
}

// Another write that replaces the whole key right after ours (two people
// changing language at once) doesn't lose ours: it's read back and retried.
func TestRecorderRetriesAWriteAnotherReplaced(t *testing.T) {
	inner := tenant.NewMemoryStore()
	if err := inner.Create(context.Background(), tenant.Tenant{ID: tid, Slug: "acme"}); err != nil {
		t.Fatal(err)
	}
	clobber := &clobbering{MemoryStore: inner, times: 1}
	r := &Recorder{Tenants: clobber, Go: func(f func()) { f() }}
	r.Note(tid, "ana", words.DE)
	got := get(t, inner)
	if Of(got, "ana") != words.DE || Of(got, "bo") != words.FR {
		t.Fatalf("ana %s, bo %s: a write was lost", Of(got, "ana"), Of(got, "bo"))
	}
}

// clobbering replaces the key with someone else's write right after each
// of the first times writes, as a concurrent recorder on another instance would.
type clobbering struct {
	*tenant.MemoryStore
	times int
}

func (c *clobbering) SetMetadata(ctx context.Context, id, key string, value any) error {
	if err := c.MemoryStore.SetMetadata(ctx, id, key, value); err != nil {
		return err
	}
	if c.times > 0 {
		c.times--
		return c.MemoryStore.SetMetadata(ctx, id, key, map[string]any{"people": map[string]any{"bo": "fr"}, "last": "fr"})
	}
	return nil
}
