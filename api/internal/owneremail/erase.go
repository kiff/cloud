package owneremail

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/user"
)

// clerkUserDeleter is the slice of the Clerk SDK erasure needs. The
// production value is user.Delete; tests inject a fake.
type clerkUserDeleter func(ctx context.Context, id string) (*clerk.DeletedResource, error)

// ClerkIdentityEraser deletes a person's Clerk user when their last
// account is erased (tenant.IdentityEraser), so they can't sign in
// again and Clerk keeps no record of them. It uses the global Clerk key
// the API binary sets at startup, like ClerkEmailLookup.
type ClerkIdentityEraser struct {
	del clerkUserDeleter
}

// NewClerkIdentityEraser returns an eraser backed by the Clerk SDK.
func NewClerkIdentityEraser() *ClerkIdentityEraser {
	return &ClerkIdentityEraser{del: user.Delete}
}

// EraseIdentity deletes the Clerk user named by subject. A subject that
// isn't a Clerk user id (a developer or test sign-in) has nothing at
// Clerk, and a user Clerk no longer has is already erased: both are done.
func (c *ClerkIdentityEraser) EraseIdentity(ctx context.Context, subject string) error {
	if !strings.HasPrefix(subject, "user_") {
		return nil
	}
	if _, err := c.del(ctx, subject); err != nil {
		var apiErr *clerk.APIErrorResponse
		if errors.As(err, &apiErr) && apiErr.HTTPStatusCode == http.StatusNotFound {
			return nil
		}
		return fmt.Errorf("owneremail: clerk delete user: %w", err)
	}
	return nil
}
