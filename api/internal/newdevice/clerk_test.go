package newdevice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/clerk/clerk-sdk-go/v2"
)

func ptr(s string) *string { return &s }

func fakeClerk(s *clerk.Session, u *clerk.User, userErr error) *ClerkDescriber {
	return &ClerkDescriber{
		getSession: func(context.Context, string) (*clerk.Session, error) { return s, nil },
		getUser:    func(context.Context, string) (*clerk.User, error) { return u, userErr },
	}
}

func google() *clerk.ExternalAccount { return &clerk.ExternalAccount{Provider: "oauth_google"} }

// The session's device, browser and place, never its IP; the method only
// when the person can only have signed in one way.
func TestClerkDescribesTheSignIn(t *testing.T) {
	s := &clerk.Session{UserID: "user_ana", LatestActivity: &clerk.SessionActivity{DeviceType: ptr("Android"), BrowserName: ptr("Chrome"),
		BrowserVersion: ptr("131.0"), IPAddress: ptr("203.0.113.7"), City: ptr("Paris"), Country: ptr("FR")}}
	for name, c := range map[string]struct {
		u        *clerk.User
		method   string
		password bool
	}{
		"password only":            {&clerk.User{PasswordEnabled: true}, "password", true},
		"Google only":              {&clerk.User{ExternalAccounts: []*clerk.ExternalAccount{google()}}, "Google", false},
		"password and Google":      {&clerk.User{PasswordEnabled: true, ExternalAccounts: []*clerk.ExternalAccount{google()}}, "", true},
		"two providers":            {&clerk.User{ExternalAccounts: []*clerk.ExternalAccount{google(), {Provider: "oauth_apple"}}}, "", false},
		"a provider we can't name": {&clerk.User{ExternalAccounts: []*clerk.ExternalAccount{{Provider: "oauth_custom_x"}}}, "", false},
		"a code by email":          {&clerk.User{}, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			d, err := fakeClerk(s, c.u, nil).Describe(context.Background(), "sess_1", "user_ana")
			if err != nil || d.Device != "Android" || d.Browser != "Chrome" || d.City != "Paris" || d.Country != "FR" ||
				d.Method != c.method || d.Password != c.password {
				t.Fatalf("%+v %v", d, err)
			}
			if strings.Contains(d.Device+d.Browser+d.City+d.Country+d.Method, "203.0.113.7") {
				t.Fatal("the IP address was read")
			}
		})
	}
	// The person can't be read: the device and place still come.
	if d, err := fakeClerk(s, nil, errors.New("down")).Describe(context.Background(), "sess_1", "user_ana"); err != nil || d.Device != "Android" || d.Method != "" {
		t.Fatalf("no person: %+v %v", d, err)
	}
	// Another person's session describes nothing.
	if _, err := fakeClerk(s, &clerk.User{}, nil).Describe(context.Background(), "sess_1", "user_bob"); err == nil {
		t.Fatal("another person's session was described")
	}
	// No activity: nothing but the method.
	if d, err := fakeClerk(&clerk.Session{UserID: "user_ana"}, &clerk.User{PasswordEnabled: true}, nil).Describe(context.Background(), "sess_1", "user_ana"); err != nil ||
		d.Device != "" || d.City != "" || d.Method != "password" {
		t.Fatalf("no activity: %+v %v", d, err)
	}
}

// When Clerk made the account, for an event that doesn't say.
func TestClerkSaysWhenTheAccountWasMade(t *testing.T) {
	c := fakeClerk(nil, &clerk.User{CreatedAt: 1793955543000}, nil)
	if made, err := c.AccountMade(context.Background(), "user_ana"); err != nil || made.UnixMilli() != 1793955543000 {
		t.Fatalf("%v %v", made, err)
	}
	if _, err := fakeClerk(nil, &clerk.User{}, nil).AccountMade(context.Background(), "user_ana"); err == nil {
		t.Fatal("no time was taken as one")
	}
	if _, err := fakeClerk(nil, nil, errors.New("down")).AccountMade(context.Background(), "user_ana"); err == nil {
		t.Fatal("a failed read was taken as a time")
	}
}
