package owneremail

import (
	"context"
	"errors"
	"testing"
)

type fakeSubjects struct {
	subject string
	err     error
}

func (f fakeSubjects) SubjectForTenant(context.Context, string) (string, error) {
	return f.subject, f.err
}

type fakeEmails struct {
	email string
	err   error
}

func (f fakeEmails) EmailForSubject(context.Context, string) (string, error) {
	return f.email, f.err
}

func TestResolver_Email_HappyPath(t *testing.T) {
	t.Parallel()
	r := NewResolver(fakeSubjects{subject: "user_abc"}, fakeEmails{email: "owner@example.com"})
	got, err := r.Email(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("Email: %v", err)
	}
	if got != "owner@example.com" {
		t.Fatalf("got %q", got)
	}
}

func TestResolver_Email_NoSubjectMapping(t *testing.T) {
	t.Parallel()
	r := NewResolver(fakeSubjects{err: errors.New("not mapped")}, fakeEmails{email: "x@example.com"})
	_, err := r.Email(context.Background(), "tenant-1")
	if !errors.Is(err, ErrNoEmail) {
		t.Fatalf("want ErrNoEmail, got %v", err)
	}
}

func TestResolver_Email_ClerkLookupFails(t *testing.T) {
	t.Parallel()
	r := NewResolver(fakeSubjects{subject: "user_abc"}, fakeEmails{err: errors.New("clerk down")})
	_, err := r.Email(context.Background(), "tenant-1")
	if !errors.Is(err, ErrNoEmail) {
		t.Fatalf("want ErrNoEmail, got %v", err)
	}
}

func TestResolver_Email_EmptyEmail(t *testing.T) {
	t.Parallel()
	r := NewResolver(fakeSubjects{subject: "user_abc"}, fakeEmails{email: "  "})
	_, err := r.Email(context.Background(), "tenant-1")
	if !errors.Is(err, ErrNoEmail) {
		t.Fatalf("want ErrNoEmail on empty email, got %v", err)
	}
}
