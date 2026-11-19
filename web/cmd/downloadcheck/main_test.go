package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/content/desktop"
)

// A site whose page has no rows and no description, against no releases:
// the rows are missing, so it's a mismatch.
func TestTheExitStatusSaysWhatHappened(t *testing.T) {
	t.Cleanup(desktop.Reset)
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html></html>")) }))
	defer empty.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusBadGateway) }))
	defer down.Close()
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	t.Run("a mismatch is 1, and the report is written", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "report.txt")
		var out bytes.Buffer
		if code := run([]string{"-site", empty.URL, "-langs", "en", "-report", file}, env(nil), &out); code != 1 {
			t.Fatalf("exit %d, want 1\n%s", code, out.String())
		}
		raw, err := os.ReadFile(file)
		if err != nil || string(raw) != out.String() || !strings.Contains(out.String(), "doesn't match") {
			t.Fatalf("report %q (%v), printed %q", raw, err, out.String())
		}
	})
	t.Run("releases that can't be read are 2", func(t *testing.T) {
		var out bytes.Buffer
		code := run([]string{"-site", empty.URL, "-langs", "en"}, env(map[string]string{"KIFF_DESKTOP_RELEASES": "on", "KIFF_GITHUB_API": down.URL}), &out)
		if code != 2 || !strings.Contains(out.String(), "couldn't be made") {
			t.Fatalf("exit %d, want 2\n%s", code, out.String())
		}
	})
	t.Run("a bad flag is 2", func(t *testing.T) {
		var out bytes.Buffer
		if code := run([]string{"-nope"}, env(nil), &out); code != 2 {
			t.Fatalf("exit %d, want 2", code)
		}
	})
}
