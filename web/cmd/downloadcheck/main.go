// Command downloadcheck checks the live /tafs/download against the computers'
// published releases (package downloadcheck), and prints what differs.
//
// It reads the deploy's own settings from the environment, as the site does:
// KIFF_DESKTOP_RELEASES ("on" reads the releases), KIFF_DESKTOP_NOT_AVAILABLE,
// KIFF_WINDOWS_SIGNED (off, Windows is coming soon whatever its release) and
// KIFF_GITHUB_API. GITHUB_TOKEN, when set, is sent to GitHub for its
// higher rate limit only; the repositories are public.
//
// Exit status: 0 when the page matches (a release published in the last 15
// minutes may still be pending), 1 on a mismatch, 2 when the check couldn't be
// made because a release couldn't be read. With -report, the report is also
// written to that file, for the workflow's comment.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/downloadcheck"
	"github.com/kiff/cloud/web/internal/pages"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout))
}

func run(args []string, getenv func(string) string, out io.Writer) int {
	fs := flag.NewFlagSet("downloadcheck", flag.ContinueOnError)
	site := fs.String("site", "https://kiff.dev", "the site's root")
	langs := fs.String("langs", "en,fr", "the page's languages to check, comma-separated")
	report := fs.String("report", "", "also write the report to this file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var ls []string
	for _, l := range strings.Split(*langs, ",") {
		if l = strings.TrimSpace(l); l != "" {
			ls = append(ls, l)
		}
	}
	// As the site reads it: off, the page offers no Windows installer.
	pages.WindowsSigned = strings.TrimSpace(getenv("KIFF_WINDOWS_SIGNED")) == "on"
	cfg := downloadcheck.Config{
		Site:         *site,
		Langs:        ls,
		Releases:     strings.TrimSpace(getenv("KIFF_DESKTOP_RELEASES")) == "on",
		NotAvailable: getenv("KIFF_DESKTOP_NOT_AVAILABLE"),
		GitHubAPI:    strings.TrimSpace(getenv("KIFF_GITHUB_API")),
		Token:        strings.TrimSpace(getenv("GITHUB_TOKEN")),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rep, err := downloadcheck.Run(ctx, cfg)
	text := rep.Text()
	code := 0
	switch {
	case err != nil:
		text = "The check couldn't be made: " + err.Error() + "\n"
		code = 2
	case rep.Mismatch():
		code = 1
	}
	fmt.Fprint(out, text)
	if *report != "" {
		if werr := os.WriteFile(*report, []byte(text), 0o644); werr != nil {
			fmt.Fprintln(os.Stderr, "report:", werr)
		}
	}
	return code
}
