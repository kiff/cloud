package owneremail

import (
	"context"
	"fmt"

	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/user"
)

// clerkUserGetter is the slice of the Clerk SDK this package needs.
// The production value is clerkGet (package-level user.Get); tests
// inject a fake so they never touch Clerk.
type clerkUserGetter func(ctx context.Context, id string) (*clerk.User, error)

// ClerkEmailLookup resolves a subject's email via the Clerk Backend
// API. It relies on the global Clerk key set by the API binary at
// startup (clerk.SetKey from CLERK_SECRET_KEY) — the same key the
// session verifier already uses.
type ClerkEmailLookup struct {
	get clerkUserGetter
}

// NewClerkEmailLookup returns a lookup backed by the Clerk SDK.
func NewClerkEmailLookup() *ClerkEmailLookup {
	return &ClerkEmailLookup{get: user.Get}
}

// EmailForSubject implements EmailLookup. Returns the user's primary
// email (falling back to the first listed) for the Clerk user id.
func (c *ClerkEmailLookup) EmailForSubject(ctx context.Context, subject string) (string, error) {
	if subject == "" {
		return "", fmt.Errorf("owneremail: empty subject")
	}
	u, err := c.get(ctx, subject)
	if err != nil {
		return "", fmt.Errorf("owneremail: clerk get user: %w", err)
	}
	return primaryEmail(u), nil
}

// primaryEmail returns the user's primary email address, falling back
// to the first listed when no primary is marked. Empty string when the
// user has no email addresses.
func primaryEmail(u *clerk.User) string {
	if u == nil || len(u.EmailAddresses) == 0 {
		return ""
	}
	if u.PrimaryEmailAddressID != nil {
		for _, e := range u.EmailAddresses {
			if e != nil && e.ID == *u.PrimaryEmailAddressID {
				return e.EmailAddress
			}
		}
	}
	for _, e := range u.EmailAddresses {
		if e != nil && e.EmailAddress != "" {
			return e.EmailAddress
		}
	}
	return ""
}
