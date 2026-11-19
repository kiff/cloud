package handlers_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/kiff/cloud/api/internal/handlers"
)

// A read cut short because the app went away is not KIFF failing; a
// timeout, or a cancellation the request didn't have, still is.
func TestAClientThatLeftIsNoFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	gone := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	cancel()
	here := httptest.NewRequest("GET", "/", nil)
	canceled := fmt.Errorf("mandate: current usage unavailable; refuse: %w", context.Canceled)
	if !handlers.ClientLeftForTest(gone, canceled) {
		t.Fatal("a read the app walked away from read as a failure")
	}
	for _, c := range []struct {
		name string
		r    bool
		err  error
	}{
		{"the request is still open", false, canceled},
		{"a deadline", true, fmt.Errorf("acquire connection: %w", context.DeadlineExceeded)},
		{"another error", true, errors.New("relation does not exist")},
	} {
		r := here
		if c.r {
			r = gone
		}
		if handlers.ClientLeftForTest(r, c.err) {
			t.Fatalf("%s: read as the client leaving", c.name)
		}
	}
}
