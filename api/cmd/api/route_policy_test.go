package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
)

// Every write route the API registers is named in the governance-roles design's route
// policy, so who may call it was decided rather than defaulted. (An
// unnamed write is admin-only, which is safe but usually not what the
// route's author meant.)
func TestEveryWriteRouteHasARule(t *testing.T) {
	found := 0
	pattern := regexp.MustCompile(`Handle(?:Func)?\("((?:POST|PUT|PATCH|DELETE) /v1/[^"]*)"`)
	err := filepath.WalkDir("../../internal", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range pattern.FindAllStringSubmatch(string(src), -1) {
			route := m[1]
			found++
			if strings.HasPrefix(route, "POST /v1/auth/device/start") || strings.HasPrefix(route, "POST /v1/auth/device/token") || route == "POST /v1/auth/app/token" || route == "POST /v1/pair/claim" ||
				strings.HasPrefix(route, "POST /v1/invites/") || route == "POST /v1/coach/act" || route == "POST /v1/coach/credits" ||
				route == "POST /v1/auth/code/send" || route == "POST /v1/auth/code/verify" {
				continue // public (app/token's credential is its code and verifier; pair/claim's credential is its proof, coach/act's and coach/credits' credential is KIFF's own permit; auth/code's caller is the web, from the box, before anyone is signed in), or behind WrapPerson: never reach the policy
			}
			if strings.HasPrefix(route, "POST /v1/ops/") {
				continue // covered by the "POST /v1/ops/" rule; the console checks its allow-list
			}
			if _, ok := auth.DefaultRules[route]; !ok {
				t.Errorf("%s registers %q, which the governance-roles route policy (auth.DefaultRules) does not name", path, route)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found < 40 {
		t.Fatalf("found only %d write routes; the scan is not seeing the handlers", found)
	}
}
