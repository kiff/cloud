package domain

import (
	"errors"
	"testing"
)

func TestResolveRequestDomain(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		keyBound string
		req      string
		want     string
		wantErr  error
	}{
		{"tenant-wide, no selector -> default", "", "", "", nil},
		{"tenant-wide, explicit -> passthrough", "", "payouts", "payouts", nil},
		{"scoped, no selector -> bound", "refunds", "", "refunds", nil},
		{"scoped, matching selector -> bound", "refunds", "refunds", "refunds", nil},
		{"scoped, conflicting selector -> 403", "refunds", "payouts", "", ErrDomainSelectorMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveRequestDomain(tc.keyBound, tc.req)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// stated explicitly: a domain-scoped key can never be steered onto
// another domain by a crafted request selector.
