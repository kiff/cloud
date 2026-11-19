package apps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// restore puts the embedded list back after a test that applied proofs.
func restore(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		embedded := append([]App(nil), list...)
		current.Store(&embedded)
		unlisted.Store(nil)
		generation.Add(1)
	})
}

// planque answers its proofs with body, counting the reads.
func planque(t *testing.T, status int, body *atomic.Value) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ProvedPath || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		reads.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	t.Cleanup(srv.Close)
	return srv, &reads
}

func body(s string) *atomic.Value {
	var v atomic.Value
	v.Store(s)
	return &v
}

// planque's proofs today: the seven by key, Gmail, Typeform, and Google's
// Calendar and Drive by sign-in.
const today = `{"proved": {
  "stripe": {"at": "2026-11-13", "how": "key", "link": "https://github.com/kiff/tafs/commit/3084c2a2"},
  "qonto": {"at": "2026-11-13", "how": "key", "link": "https://github.com/kiff/tafs/commit/3084c2a2"},
  "shippo": {"at": "2026-11-13", "how": "key"},
  "intercom": {"at": "2026-11-13", "how": "key"},
  "zendesk": {"at": "2026-11-13", "how": "key"},
  "pipedrive": {"at": "2026-11-13", "how": "key"},
  "github": {"at": "2026-11-13", "how": "key"},
  "gmail": {"at": "2026-11-13", "how": "oauth"},
  "typeform": {"at": "2026-11-16", "how": "oauth"},
  "googlecalendar": {"at": "2026-11-16", "how": "oauth"},
  "googledrive": {"at": "2026-11-16", "how": "oauth"}
}}`

// With planque's proofs the list is the same as apps.json today: the proofs
// and the hand-kept statuses agree, so switching it on changes no page.
func TestTodaysProofsGiveTodaysList(t *testing.T) {
	restore(t)
	srv, _ := planque(t, 200, body(today))
	before := All()
	if err := (&ProvedSource{Base: srv.URL}).Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !equal(before, All()) {
		t.Errorf("today's proofs changed the list:\nbefore %v\nafter  %v", before, All())
	}
	if len(Unlisted()) != 0 {
		t.Errorf("unlisted %v, want none", Unlisted())
	}
}

// how "key" connects with its key, "oauth" with a sign-in: planque's word,
// whatever apps.json said, and a coming app planque proved works.
func TestAProofDecidesWorksAndHowItConnects(t *testing.T) {
	restore(t)
	srv, _ := planque(t, 200, body(`{"proved": {
	  "stripe": {"at": "2026-11-13", "how": "oauth"},
	  "googlesheets": {"at": "2026-11-17", "how": "oauth"},
	  "outlook": {"at": "2026-11-17", "how": "key"}
	}}`))
	if err := (&ProvedSource{Base: srv.URL}).Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"stripe": ConnectsSignIn, "google-sheets": ConnectsSignIn, "outlook": ConnectsKey} {
		a, _ := ByID(id)
		if !a.Works() || a.Connects != want {
			t.Errorf("%s: %+v, want works by %s", id, a, want)
		}
	}
	if a, _ := ByID("google-sheets"); a.Label() != "Connects with a sign-in" {
		t.Errorf("google-sheets reads %q", a.Label())
	}
	if a, _ := ByID("outlook"); a.Label() != "Connects with its API key" {
		t.Errorf("outlook reads %q", a.Label())
	}
}

// An app apps.json says works, with no proof, is coming: the list never says
// an app works on its own word once planque's proofs are on. Coming and not
// yet stay as they are, and an app only planque names isn't added.
func TestAnUnprovedWorkingAppIsComing(t *testing.T) {
	restore(t)
	srv, _ := planque(t, 200, body(`{"proved": {"stripe": {"at": "2026-11-13", "how": "key"}, "notion": {"at": "2026-11-17", "how": "oauth"}}}`))
	if err := (&ProvedSource{Base: srv.URL}).Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a, _ := ByID("gmail"); a.Status != StatusComing || a.Connects != ConnectsSignIn || a.Label() != "Coming" {
		t.Errorf("gmail: %+v, want coming by sign-in", a)
	}
	if a, _ := ByID("github"); a.Status != StatusComing || a.Connects != ConnectsKey {
		t.Errorf("github: %+v, want coming by key", a)
	}
	if a, _ := ByID("harvest"); a.Status != StatusNotYet || a.Connects != "" {
		t.Errorf("harvest: %+v, want not yet", a)
	}
	if n := len(Working()); n != 1 {
		t.Errorf("%d work, want only stripe", n)
	}
	if n := len(All()); n != 21 {
		t.Errorf("%d apps, want apps.json's 21: none added", n)
	}
	if u := Unlisted(); len(u) != 1 || u[0] != "notion" {
		t.Errorf("unlisted %v, want [notion]", u)
	}
	if err := validate(All()); err != nil {
		t.Errorf("the list under proofs doesn't validate: %v", err)
	}
}

// planque down, answering an error, or answering another shape: the list
// stays as it was, embedded or the last good proofs.
func TestAFailedReadKeepsTheList(t *testing.T) {
	restore(t)
	embedded := All()
	for name, c := range map[string]struct {
		status int
		body   string
	}{
		"500":          {500, `{}`},
		"not json":     {200, `<html>`},
		"no proved":    {200, `{"apps": []}`},
		"bad how":      {200, `{"proved": {"stripe": {"at": "2026-11-13", "how": "magic"}}}`},
		"bad day":      {200, `{"proved": {"stripe": {"at": "yesterday", "how": "key"}}}`},
		"unfolded":     {200, `{"proved": {"Google Calendar": {"at": "2026-11-13", "how": "oauth"}}}`},
		"http link":    {200, `{"proved": {"stripe": {"at": "2026-11-13", "how": "key", "link": "http://x.test"}}}`},
		"empty object": {200, `{"proved": null}`},
		"empty map":    {200, `{"proved": {}}`},
	} {
		srv, _ := planque(t, c.status, body(c.body))
		if err := (&ProvedSource{Base: srv.URL}).Read(context.Background()); err == nil {
			t.Errorf("%s: read took it", name)
		}
		if !equal(All(), embedded) {
			t.Errorf("%s: the list changed", name)
		}
	}
	// Down altogether.
	if err := (&ProvedSource{Base: "http://127.0.0.1:1", Client: &http.Client{Timeout: time.Second}}).Read(context.Background()); err == nil {
		t.Error("an unreachable planque was read")
	}
	if !equal(All(), embedded) {
		t.Error("an unreachable planque changed the list")
	}
	// The last good proofs stand when a later read fails.
	good, _ := planque(t, 200, body(`{"proved": {"stripe": {"at": "2026-11-13", "how": "key"}}}`))
	if err := (&ProvedSource{Base: good.URL}).Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := All()
	bad, _ := planque(t, 500, body(`{}`))
	_ = (&ProvedSource{Base: bad.URL}).Read(context.Background())
	if !equal(All(), after) || len(Working()) != 1 {
		t.Error("a failed read undid the last good proofs")
	}
}

// Start reads at once, then again on its schedule: a proof added in planque
// reaches the list without a restart, and the generation moves with it.
func TestStartRefreshes(t *testing.T) {
	restore(t)
	b := body(`{"proved": {"stripe": {"at": "2026-11-13", "how": "key"}}}`)
	srv, reads := planque(t, 200, b)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	(&ProvedSource{Base: srv.URL, Every: 20 * time.Millisecond}).Start(ctx)
	if reads.Load() < 1 || len(Working()) != 1 {
		t.Fatalf("Start didn't read first: %d reads, %d working", reads.Load(), len(Working()))
	}
	gen := Generation()
	b.Store(`{"proved": {"stripe": {"at": "2026-11-13", "how": "key"}, "gmail": {"at": "2026-11-13", "how": "oauth"}}}`)
	deadline := time.Now().Add(2 * time.Second)
	for len(Working()) != 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(Working()) != 2 {
		t.Fatalf("the refresh didn't take gmail: %d working after %d reads", len(Working()), reads.Load())
	}
	if Generation() == gen {
		t.Error("the list changed but its generation didn't")
	}
	// The same proofs again change nothing, generation included.
	gen = Generation()
	n := reads.Load()
	for reads.Load() < n+2 && time.Now().Before(deadline.Add(time.Second)) {
		time.Sleep(10 * time.Millisecond)
	}
	if Generation() != gen {
		t.Error("an unchanged read moved the generation")
	}
}

func TestFoldMatchesPlanquesKeys(t *testing.T) {
	for in, want := range map[string]string{"google-calendar": "googlecalendar", "Google Calendar": "googlecalendar", "zoho-books": "zohobooks", "GitHub": "github"} {
		if got := fold(in); got != want {
			t.Errorf("fold(%q) = %q, want %q", in, got, want)
		}
	}
	if strings.ContainsAny(fold("A_b.c d-e"), "_. -") {
		t.Error("fold keeps separators")
	}
}
