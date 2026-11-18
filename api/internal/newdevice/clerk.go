package newdevice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/session"
	"github.com/clerk/clerk-sdk-go/v2/user"
)

// ClerkDescriber reads a sign-in's description from Clerk: the session's
// latest activity (device, browser, the network's guess of the place) and
// the person's sign-in methods. It uses the key set at startup
// (clerk.SetKey from CLERK_SECRET_KEY). The IP address Clerk returns is
// never read.
type ClerkDescriber struct {
	// getSession and getUser are Clerk's; tests set them.
	getSession func(ctx context.Context, id string) (*clerk.Session, error)
	getUser    func(ctx context.Context, id string) (*clerk.User, error)
}

// NewClerkDescriber reads through Clerk's API.
func NewClerkDescriber() *ClerkDescriber {
	return &ClerkDescriber{getSession: session.Get, getUser: user.Get}
}

// Describe reads the session and its person. A session that isn't the
// person's describes nothing.
func (c *ClerkDescriber) Describe(ctx context.Context, sessionID, subject string) (Description, error) {
	s, err := c.getSession(ctx, sessionID)
	if err != nil {
		return Description{}, fmt.Errorf("newdevice: clerk get session: %w", err)
	}
	if s == nil || s.UserID != subject {
		return Description{}, errors.New("newdevice: the session isn't the person's")
	}
	var d Description
	if a := s.LatestActivity; a != nil {
		d.Device, d.Browser, d.City, d.Country = str(a.DeviceType), str(a.BrowserName), str(a.City), str(a.Country)
	}
	u, err := c.getUser(ctx, subject)
	if err != nil {
		// The device and place are known; the method isn't.
		return d, nil
	}
	d.Method, d.Password = method(u)
	return d, nil
}

// AccountMade is when Clerk made the person's account.
func (c *ClerkDescriber) AccountMade(ctx context.Context, subject string) (time.Time, error) {
	u, err := c.getUser(ctx, subject)
	if err != nil {
		return time.Time{}, fmt.Errorf("newdevice: clerk get user: %w", err)
	}
	if u == nil || u.CreatedAt <= 0 {
		return time.Time{}, errors.New("newdevice: clerk gave no account time")
	}
	return time.UnixMilli(u.CreatedAt), nil
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// providers names Clerk's sign-in providers as their owners write them.
var providers = map[string]string{
	"oauth_google": "Google", "oauth_apple": "Apple", "oauth_microsoft": "Microsoft", "oauth_github": "GitHub",
	"oauth_gitlab": "GitLab", "oauth_linkedin_oidc": "LinkedIn", "oauth_linkedin": "LinkedIn", "oauth_facebook": "Facebook",
}

// method is how the person signed in when it can only have been one way,
// and whether they have a password. Clerk doesn't say which way a session
// signed in, so a person with a password and a provider, or two providers,
// has no method named.
func method(u *clerk.User) (string, bool) {
	if u == nil {
		return "", false
	}
	var named []string
	for _, a := range u.ExternalAccounts {
		if a == nil {
			continue
		}
		name, ok := providers[strings.TrimSpace(a.Provider)]
		if !ok {
			// One we can't name: nothing is certain.
			return "", u.PasswordEnabled
		}
		named = append(named, name)
	}
	switch {
	case u.PasswordEnabled && len(named) == 0:
		return "password", true
	case !u.PasswordEnabled && len(named) == 1:
		return named[0], false
	}
	return "", u.PasswordEnabled
}
