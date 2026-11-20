package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/region"
)

// A table that doesn't load, or is too small to be a whole file, stops the
// API; no path checks nothing.
func TestTheRegionTableIsWholeOrTheAPIDoesntStart(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	if _, err := buildRegionGate(filepath.Join(dir, "missing.csv.gz"), quiet); err == nil {
		t.Error("a missing table started")
	}
	small := filepath.Join(dir, "small.csv")
	if err := os.WriteFile(small, []byte("192.0.2.0,192.0.2.255,FR\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildRegionGate(small, quiet); err == nil || !strings.Contains(err.Error(), "not a whole file") {
		t.Errorf("a one-line table: %v", err)
	}
	g, err := buildRegionGate("", quiet)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/auth/device/start", nil)
	r.RemoteAddr = "198.51.100.1:5000"
	g.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
	if w.Code != 204 {
		t.Errorf("no table: %d, want nothing checked", w.Code)
	}

	// A whole (gzipped) table checks the session routes.
	whole := filepath.Join(dir, "whole.csv.gz")
	f, err := os.Create(whole)
	if err != nil {
		t.Fatal(err)
	}
	z := gzip.NewWriter(f)
	for i := 0; i < 100_000; i++ {
		fmt.Fprintf(z, "10.%d.%d.0,10.%d.%d.255,ZZ\n", i>>8&255, i&255, i>>8&255, i&255)
	}
	fmt.Fprintln(z, "192.0.2.0,192.0.2.255,FR")
	fmt.Fprintln(z, "198.51.100.0,198.51.100.255,CN")
	z.Close()
	f.Close()
	g, err = buildRegionGate(whole, quiet)
	if err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]int{"192.0.2.9": 204, "198.51.100.1": 403} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/auth/device/start", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		r.Header.Set("X-Forwarded-For", addr)
		g.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%s: %d, want %d", addr, w.Code, want)
		}
	}
}

// Every route the gate checks is one the API registers, so a renamed route
// can't quietly leave the check.
func TestTheRegionRoutesAreServed(t *testing.T) {
	served := map[string]bool{}
	pattern := regexp.MustCompile(`Handle(?:Func)?\("((?:GET|POST) /[^"]*)"`)
	err := filepath.WalkDir("../../internal", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range pattern.FindAllStringSubmatch(string(src), -1) {
			served[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range region.SessionRoutes {
		if !served[r] {
			t.Errorf("%s is checked by the region gate but no handler registers it", r)
		}
	}
}
