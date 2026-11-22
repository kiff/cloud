// Package personlang keeps each person's last language, so what KIFF
// writes to them without a request (the held-call and Card-warning
// emails) comes in the language their app last showed them
// (kiff/tafs/docs/languages.md, "With no request").
//
// It lives in the account's metadata, under MetadataKey: the person's
// subject to their language, and the account's last one, for an address
// that is no one person's. No new store of personal data, and account
// erasure takes it with the account. English until one is seen.
package personlang

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/words"
)

// MetadataKey is the account metadata key: {"people": {subject: lang},
// "last": lang}.
const MetadataKey = "person_langs"

// MaxPeople bounds the people kept per account; past it, a new person's
// language isn't kept (they read English, or the account's last).
const MaxPeople = 500

// maxSeen bounds what a recorder remembers having written; past it, it
// forgets them all and writes again only on a change it can't rule out.
const maxSeen = 10000

// saveTries is how many times a write is read back and tried again when
// another write to the account's metadata replaced it meanwhile.
const saveTries = 3

// Tenants is what the recorder reads and writes.
type Tenants interface {
	Get(ctx context.Context, id string) (tenant.Tenant, error)
	SetMetadata(ctx context.Context, id, key string, value any) error
}

// Of is a person's last language on the account; the account's last one
// when theirs isn't known, else English.
func Of(t tenant.Tenant, subject string) words.Lang {
	people, last := read(t)
	if l, ok := people[subject]; ok {
		return words.EmailLang(l)
	}
	return words.EmailLang(last)
}

// Account is the account's last language, for an address that is no one
// person's (a warning email to a team inbox); English until one is seen.
func Account(t tenant.Tenant) words.Lang {
	_, last := read(t)
	return words.EmailLang(last)
}

func read(t tenant.Tenant) (map[string]string, string) {
	people := map[string]string{}
	m, _ := t.Metadata[MetadataKey].(map[string]any)
	if m == nil {
		return people, ""
	}
	if p, ok := m["people"].(map[string]any); ok {
		for k, v := range p {
			if s, ok := v.(string); ok {
				people[k] = s
			}
		}
	}
	last, _ := m["last"].(string)
	return people, last
}

// Recorder notes the language of each signed-in person's requests: a
// person's session (never an agent's key) whose request says its app's
// languages. It writes only when the language changed, after the request,
// so no request waits on it, and a failed write is only logged.
type Recorder struct {
	Tenants Tenants
	Logger  *slog.Logger
	// Go runs a write; nil runs it in its own goroutine.
	Go func(func())

	mu   sync.Mutex
	seen map[string]words.Lang
}

// Wrap notes the language of next's signed-in requests.
func (r *Recorder) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		next.ServeHTTP(w, req)
		header := req.Header.Get("Accept-Language")
		if r == nil || r.Tenants == nil || header == "" {
			return
		}
		id, err := auth.IdentityFrom(req.Context())
		if err != nil || id.APIKey || id.Method == auth.MethodAPIKey || id.Subject == "" || id.Tenant == "" {
			return
		}
		r.Note(id.Tenant, id.Subject, words.EmailLangFromHeader(header))
	})
}

// Note keeps l as subject's language on tenantID's account, unless it's
// what was kept last.
func (r *Recorder) Note(tenantID, subject string, l words.Lang) {
	key := tenantID + "|" + subject
	r.mu.Lock()
	if r.seen == nil || len(r.seen) >= maxSeen {
		r.seen = map[string]words.Lang{}
	}
	if r.seen[key] == l {
		r.mu.Unlock()
		return
	}
	r.seen[key] = l
	r.mu.Unlock()
	run := r.Go
	if run == nil {
		run = func(f func()) { go f() }
	}
	run(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.save(ctx, tenantID, subject, l); err != nil {
			r.mu.Lock()
			delete(r.seen, key) // tried again on the next request
			r.mu.Unlock()
			if r.Logger != nil {
				r.Logger.WarnContext(ctx, "personlang: keeping a person's language failed", slog.String("tenant", tenantID), slog.String("err", err.Error()))
			}
		}
	})
}

// save writes subject's language into the account's metadata, then reads
// it back: two people's writes at once can each replace the other's whole
// key, so a write that didn't stick is merged again and retried.
func (r *Recorder) save(ctx context.Context, tenantID, subject string, l words.Lang) error {
	for try := 0; try < saveTries; try++ {
		t, err := r.Tenants.Get(ctx, tenantID)
		if err != nil {
			return err
		}
		people, last := read(t)
		_, known := people[subject]
		if !known && len(people) >= MaxPeople {
			// Full: the account's last language still moves, the person isn't kept.
			if last == string(l) {
				return nil
			}
		} else if people[subject] == string(l) && last == string(l) {
			return nil
		} else {
			people[subject] = string(l)
		}
		out := map[string]any{"last": string(l)}
		p := map[string]any{}
		for k, v := range people {
			p[k] = v
		}
		out["people"] = p
		if err := r.Tenants.SetMetadata(ctx, tenantID, MetadataKey, out); err != nil {
			return err
		}
		back, err := r.Tenants.Get(ctx, tenantID)
		if err != nil {
			return err
		}
		if got, _ := read(back); got[subject] == string(l) || (!known && len(people) >= MaxPeople) {
			return nil
		}
	}
	return errLost
}

var errLost = errors.New("personlang: another write replaced the language each time")
