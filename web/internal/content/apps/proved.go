package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Which apps work comes from planque's reviewed proofs once KIFF_APPS_PROVED
// is on: GET {planque}/v1/proved, the same list tafs cloud's gallery reads,
// so the site and the gallery can't disagree. A proof is one data change in
// planque, never an edit here.
//
// apps.json still holds every app's id, name and order, and what's coming or
// not yet. With the proofs on:
//   - an app planque proved works, connected the way it was proved (how
//     "key" is ConnectsKey, "oauth" is a sign-in);
//   - an app apps.json lists as working with no proof is coming, never
//     working on this list's word alone;
//   - an app planque proved that apps.json doesn't list isn't added: it needs
//     its name and place here first (Unlisted says which).
//
// Until a read has worked, and whenever one fails or answers in another
// shape, the last good proofs stand, or with none, apps.json as embedded.
// With the switch off nothing is read, and the list is exactly apps.json.

// ProvedPath is planque's read of the reviewed proofs.
const ProvedPath = "/v1/proved"

// Proof is one app's entry in planque's proofs: the day it's been listed as
// working since, how it was run (key or oauth), and where that's recorded.
type Proof struct {
	At   string `json:"at"`
	How  string `json:"how"`
	Link string `json:"link,omitempty"`
}

// The effective list and its generation: the docs render the apps table once
// per generation (docs.go), so a new list reaches them without a restart.
var (
	current    atomic.Pointer[[]App]
	generation atomic.Uint64
	unlisted   atomic.Pointer[[]string]
)

// Generation changes each time the effective list does.
func Generation() uint64 { return generation.Load() }

// Unlisted is the apps planque proved that apps.json doesn't name, from the
// last proofs applied: they need an entry here to be shown.
func Unlisted() []string {
	if u := unlisted.Load(); u != nil {
		return append([]string(nil), (*u)...)
	}
	return nil
}

func effective() []App { return *current.Load() }

var (
	foldName  = regexp.MustCompile(`[^a-z0-9]`)
	provedKey = regexp.MustCompile(`^[a-z0-9]{1,64}$`)
)

// fold is an app's name as planque keys its proofs: lower case letters and
// digits only (google-calendar and Google Calendar are googlecalendar).
func fold(s string) string { return foldName.ReplaceAllString(strings.ToLower(s), "") }

// decodeProofs reads planque's answer, {"proved": {app: {at, how, link}}}.
// Any entry it can't vouch for refuses the whole answer, so a half-read list
// never reaches the site.
func decodeProofs(raw []byte) (map[string]Proof, error) {
	var f struct {
		Proved map[string]Proof `json:"proved"`
	}
	if err := json.Unmarshal(raw, &f); err != nil || f.Proved == nil {
		return nil, fmt.Errorf("proofs: not {\"proved\": {…}}")
	}
	// An answer that proves nothing is a broken or cut-off one, never "no app
	// connects": it changes nothing, as tafs cloud's gallery keeps its list.
	if len(f.Proved) == 0 {
		return nil, fmt.Errorf("proofs: an empty map proves nothing")
	}
	for name, p := range f.Proved {
		if !provedKey.MatchString(name) {
			return nil, fmt.Errorf("proofs: %q isn't a folded app name", name)
		}
		if p.How != "key" && p.How != "oauth" {
			return nil, fmt.Errorf("proofs: %s's how %q is neither key nor oauth", name, p.How)
		}
		if _, err := time.Parse(time.DateOnly, p.At); err != nil {
			return nil, fmt.Errorf("proofs: %s's at %q isn't a day", name, p.At)
		}
		if p.Link != "" {
			if u, err := url.Parse(p.Link); err != nil || u.Scheme != "https" || u.Host == "" {
				return nil, fmt.Errorf("proofs: %s's link isn't https", name)
			}
		}
	}
	return f.Proved, nil
}

// withProofs is apps.json under these proofs, in apps.json's order, and the
// proved apps it doesn't name.
func withProofs(base []App, proofs map[string]Proof) ([]App, []string) {
	out := make([]App, 0, len(base))
	used := map[string]bool{}
	for _, a := range base {
		p, ok := proofs[fold(a.ID)]
		key := fold(a.ID)
		if !ok {
			p, ok = proofs[fold(a.Name)]
			key = fold(a.Name)
		}
		switch {
		case ok:
			used[key] = true
			a.Status, a.Connects = StatusWorks, ConnectsKey
			if p.How == "oauth" {
				a.Connects = ConnectsSignIn
			}
		case a.Status == StatusWorks:
			// Working on apps.json's word alone: coming, until planque proves it.
			a.Status = StatusComing
		}
		out = append(out, a)
	}
	var rest []string
	for name := range proofs {
		if !used[name] {
			rest = append(rest, name)
		}
	}
	return out, rest
}

// apply makes these proofs the effective list, if they change it.
func apply(proofs map[string]Proof) {
	next, rest := withProofs(list, proofs)
	unlisted.Store(&rest)
	if equal(effective(), next) {
		return
	}
	current.Store(&next)
	generation.Add(1)
}

func equal(a, b []App) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ProvedSource reads planque's proofs and keeps the list current.
type ProvedSource struct {
	// Base is planque's root, such as https://planque.kiff.dev.
	Base string
	// Client reads it; nil is a client with a 5-second timeout.
	Client *http.Client
	// Every is how often the proofs are read again; 0 is 10 minutes.
	Every time.Duration
	// Logf reports a read that failed and the proved apps not listed; nil is silent.
	Logf func(format string, args ...any)

	once sync.Once
}

// Read reads the proofs once and applies them. On any failure the list stays
// as it was, and the error says why.
func (s *ProvedSource) Read(ctx context.Context) error {
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.Base, "/")+ProvedPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("proofs: planque answered %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	proofs, err := decodeProofs(raw)
	if err != nil {
		return err
	}
	apply(proofs)
	if rest := Unlisted(); len(rest) > 0 && s.Logf != nil {
		s.Logf("apps: planque proved %v, which apps.json doesn't list", rest)
	}
	return nil
}

// Start reads the proofs now, then every Every until ctx ends. It returns
// after the first read, whatever it answered: a planque that's down leaves the
// embedded list, and the next read tries again.
func (s *ProvedSource) Start(ctx context.Context) {
	s.once.Do(func() {
		every := s.Every
		if every <= 0 {
			every = 10 * time.Minute
		}
		s.readLogged(ctx)
		go func() {
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					s.readLogged(ctx)
				}
			}
		}()
	})
}

func (s *ProvedSource) readLogged(ctx context.Context) {
	if err := s.Read(ctx); err != nil && s.Logf != nil {
		s.Logf("apps: planque's proofs not read, the list stays as it was: %v", err)
	}
}
