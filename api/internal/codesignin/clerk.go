package codesignin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/signintoken"
	"github.com/clerk/clerk-sdk-go/v2/user"
)

// ClerkPeople reads people from the sign-in provider (Clerk) and asks it for
// one-time sign-in tokens, with the key set at startup (clerk.SetKey from
// CLERK_SECRET_KEY).
type ClerkPeople struct {
	// list, get and token are Clerk's; tests set them.
	list  func(ctx context.Context, p *user.ListParams) (*clerk.UserList, error)
	get   func(ctx context.Context, id string) (*clerk.User, error)
	token func(ctx context.Context, p *signintoken.CreateParams) (*clerk.SignInToken, error)
}

// NewClerkPeople reads through Clerk's API.
func NewClerkPeople() *ClerkPeople {
	return &ClerkPeople{list: user.List, get: user.Get, token: signintoken.Create}
}

// verified are u's verified addresses, lower-cased.
func verified(u *clerk.User) []string {
	if u == nil {
		return nil
	}
	var out []string
	for _, e := range u.EmailAddresses {
		if e == nil || e.Verification == nil || e.Verification.Status != "verified" {
			continue
		}
		if a := strings.ToLower(strings.TrimSpace(e.EmailAddress)); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// ByVerifiedEmail is the one person with address verified. An address on
// no one, only unverified, or (which the provider shouldn't allow) verified
// on two people, is no one.
func (c *ClerkPeople) ByVerifiedEmail(ctx context.Context, address string) (string, bool, error) {
	list, err := c.list(ctx, &user.ListParams{EmailAddresses: []string{address}})
	if err != nil {
		return "", false, fmt.Errorf("codesignin: clerk list users: %w", err)
	}
	if list == nil {
		return "", false, nil
	}
	var found []string
	for _, u := range list.Users {
		for _, a := range verified(u) {
			if a == address {
				found = append(found, u.ID)
				break
			}
		}
	}
	if len(found) != 1 || found[0] == "" {
		return "", false, nil
	}
	return found[0], true, nil
}

// VerifiedEmails are the person's verified addresses.
func (c *ClerkPeople) VerifiedEmails(ctx context.Context, subject string) ([]string, error) {
	u, err := c.get(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("codesignin: clerk get user: %w", err)
	}
	return verified(u), nil
}

// Ticket is a one-time sign-in token for the person.
func (c *ClerkPeople) Ticket(ctx context.Context, subject string, ttl time.Duration) (string, error) {
	secs := int64(ttl.Seconds())
	t, err := c.token(ctx, &signintoken.CreateParams{UserID: clerk.String(subject), ExpiresInSeconds: clerk.Int64(secs)})
	if err != nil {
		return "", fmt.Errorf("codesignin: clerk sign-in token: %w", err)
	}
	if t == nil || t.UserID != subject || t.Token == "" {
		return "", fmt.Errorf("codesignin: clerk gave a token for someone else, or none")
	}
	return t.Token, nil
}
