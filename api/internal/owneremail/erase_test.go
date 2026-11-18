package owneremail

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/clerk/clerk-sdk-go/v2"
)

func TestClerkIdentityEraser(t *testing.T) {
	var asked []string
	answer := map[string]error{
		"user_gone": &clerk.APIErrorResponse{HTTPStatusCode: http.StatusNotFound},
		"user_down": &clerk.APIErrorResponse{HTTPStatusCode: http.StatusBadGateway},
	}
	e := &ClerkIdentityEraser{del: func(_ context.Context, id string) (*clerk.DeletedResource, error) {
		asked = append(asked, id)
		if err := answer[id]; err != nil {
			return nil, err
		}
		return &clerk.DeletedResource{ID: id, Deleted: true}, nil
	}}
	ctx := context.Background()
	if err := e.EraseIdentity(ctx, "user_ana"); err != nil {
		t.Fatalf("a Clerk user is deleted: %v", err)
	}
	if err := e.EraseIdentity(ctx, "user_gone"); err != nil {
		t.Fatalf("a user Clerk no longer has is already erased: %v", err)
	}
	if err := e.EraseIdentity(ctx, "user_down"); err == nil {
		t.Fatal("Clerk failing must stop the erasure, so it's tried again")
	}
	if err := e.EraseIdentity(ctx, "dev:ana"); err != nil {
		t.Fatalf("a subject that isn't a Clerk user has nothing to delete: %v", err)
	}
	want := []string{"user_ana", "user_gone", "user_down"}
	if len(asked) != len(want) {
		t.Fatalf("asked Clerk for %v, want %v", asked, want)
	}
	for i := range want {
		if asked[i] != want[i] {
			t.Fatalf("asked Clerk for %v, want %v", asked, want)
		}
	}
	var apiErr *clerk.APIErrorResponse
	if err := e.EraseIdentity(ctx, "user_down"); !errors.As(err, &apiErr) {
		t.Fatalf("the Clerk error is kept: %v", err)
	}
}
