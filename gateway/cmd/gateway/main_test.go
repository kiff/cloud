package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRobotsDisallowsAll(t *testing.T) {
	rr := httptest.NewRecorder()
	robots(rr, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "User-agent: *\nDisallow: /\n" {
		t.Fatalf("GET /robots.txt: got %d %q", rr.Code, rr.Body.String())
	}
}
