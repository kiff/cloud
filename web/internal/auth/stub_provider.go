package auth

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
)

// StubProvider is the local-dev sign-in path. It renders a
// one-field email form and treats whatever the user types as
// both the subject and the bearer. The apps/api side runs in
// KIFF_AUTH_PROVIDER=stub and accepts the same string as a
// subject lookup against MapResolver.
//
// allowedSubjects, if non-empty, restricts which subjects can
// sign in. Local dev typically leaves this empty and trusts the
// dev to type anything; tests use it to deny everything except
// a known fixture.
//
// StubProvider must never run in production. cmd/web fails-fast
// when the production-mode env vars are set and the provider is
// stub.
type StubProvider struct {
	allowedSubjects map[string]struct{}
	render          func(w http.ResponseWriter, r *http.Request, errMsg string)
}

// NewStubProvider constructs a stub provider. allowed is an
// optional allow-list; empty allows any subject.
func NewStubProvider(render func(w http.ResponseWriter, r *http.Request, errMsg string), allowed ...string) *StubProvider {
	set := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		if a = strings.TrimSpace(a); a != "" {
			set[a] = struct{}{}
		}
	}
	if render == nil {
		render = defaultStubRender
	}
	return &StubProvider{allowedSubjects: set, render: render}
}

// Name implements Provider.
func (p *StubProvider) Name() string { return string(ProviderStub) }

// SignInForm implements Provider. Renders the email form via the
// configured renderer.
func (p *StubProvider) SignInForm(w http.ResponseWriter, r *http.Request) {
	p.render(w, r, "")
}

// VerifySignIn implements Provider. Reads the form's "subject"
// field; in stub mode the same value is both subject and
// bearer. Email is "subject" verbatim if it parses as an email,
// otherwise empty.
func (p *StubProvider) VerifySignIn(_ context.Context, r *http.Request) (Identity, error) {
	if err := r.ParseForm(); err != nil {
		return Identity{}, fmt.Errorf("auth: parse form: %w", err)
	}
	raw := strings.TrimSpace(r.PostForm.Get("subject"))
	if raw == "" {
		return Identity{}, errors.New("auth: subject is required")
	}
	if len(p.allowedSubjects) > 0 {
		if _, ok := p.allowedSubjects[raw]; !ok {
			return Identity{}, ErrInvalidCredentials
		}
	}
	id := Identity{
		Subject: raw,
		Bearer:  raw,
	}
	if strings.Contains(raw, "@") {
		id.Email = raw
	}
	if err := id.Validate(); err != nil {
		return Identity{}, err
	}
	return id, nil
}

// defaultStubRender writes a minimal HTML form. The cmd/web
// handler can override this with a templ-rendered page for a
// nicer look; the default is intentionally unstyled so the
// package has no dependency on internal/pages.
func defaultStubRender(w http.ResponseWriter, r *http.Request, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	body := `<!doctype html><html><body>`
	body += `<h1>Sign in (stub)</h1>`
	if errMsg != "" {
		body += `<p style="color:red">` + errMsg + `</p>`
	}
	// Keep next= on the POST, as the Clerk path does, so a gated link
	// with a query returns to the same page after signing in.
	action := "/sign-in"
	if next := r.URL.Query().Get("next"); next != "" {
		action += "?next=" + url.QueryEscape(next)
	}
	body += `<form method="post" action="` + html.EscapeString(action) + `">` +
		`<label>Email or subject id<br>` +
		`<input name="subject" autofocus required></label>` +
		` <button type="submit">Sign in</button>` +
		`</form>` +
		`<p><small>Stub mode. Type any email; you'll get an auto-provisioned tenant.</small></p>` +
		`</body></html>`
	_, _ = w.Write([]byte(body))
}
