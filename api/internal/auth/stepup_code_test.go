package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A session made after the person's latest code sign-in is never a recent
// sign-in for the changes that need one: an inbox alone never makes them. An
// older session still is, and the apps' sheet (WithoutCodeCheck) lets a code
// session through.
func TestStepUpRefusesASessionMadeByACodeSignIn(t *testing.T) {
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	codeAt := now.Add(-2 * time.Minute)
	fresh := time.Duration(0)
	made := map[string]time.Time{"sess_code": codeAt.Add(5 * time.Second), "sess_older": codeAt.Add(-time.Minute)}
	s := &StepUp{
		Now: func() time.Time { return now },
		SessionCreatedAt: func(_ context.Context, sid string) (time.Time, error) {
			if at, ok := made[sid]; ok {
				return at, nil
			}
			return time.Time{}, errors.New("unknown session")
		},
		CodeSignedIn: func(_ context.Context, subject string) (time.Time, bool, error) {
			return codeAt, subject == "user_1", nil
		},
	}
	id := func(subject, sid string) Identity {
		return Identity{Subject: subject, Method: MethodSession, FactorAge: &fresh, Metadata: map[string]string{"clerk_session_id": sid}}
	}
	ctx := context.Background()
	if _, err := s.Check(ctx, id("user_1", "sess_code")); !errors.Is(err, ErrReauthRequired) {
		t.Errorf("a session made by the code sign-in passed: %v", err)
	}
	if _, err := s.Check(ctx, id("user_1", "sess_unknown")); !errors.Is(err, ErrReauthRequired) {
		t.Errorf("a session KIFF can't date passed: %v", err)
	}
	if _, err := s.Check(ctx, id("user_1", "sess_older")); err != nil {
		t.Errorf("a session made before the code sign-in: %v", err)
	}
	if _, err := s.Check(ctx, id("user_2", "sess_code")); err != nil {
		t.Errorf("a person with no code sign-in: %v", err)
	}
	if _, err := s.WithoutCodeCheck().Check(ctx, id("user_1", "sess_code")); err != nil {
		t.Errorf("the sheet's check: %v", err)
	}
	s.CodeSignedIn = func(context.Context, string) (time.Time, bool, error) { return time.Time{}, false, errors.New("down") }
	if _, err := s.Check(ctx, id("user_2", "sess_older")); !errors.Is(err, ErrReauthRequired) {
		t.Errorf("code sign-ins unreadable: %v", err)
	}
}
