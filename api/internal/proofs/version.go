package proofs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// VersionChecker says whether a runner commit is on kiff/agents main. A run
// whose runner_version isn't confirmed is kept but never counted, so a run
// from an unreviewed runner can't clear a hold or turn on a badge.
type VersionChecker interface {
	OnMain(ctx context.Context, sha string) (bool, error)
}

// AllowList confirms only the commits it lists (KIFF_PROOF_RUNNER_VERSIONS),
// for a deployment that can't reach GitHub's API. Empty confirms nothing.
type AllowList map[string]bool

// OnMain implements VersionChecker.
func (a AllowList) OnMain(_ context.Context, sha string) (bool, error) { return a[sha], nil }

// GitHubMain confirms a commit is on a repository's branch by GitHub's
// compare API: main...sha is "identical" or "behind" when sha is reachable
// from main. A confirmed commit is cached for good; an unconfirmed one for
// ten minutes; an error isn't cached, and leaves the run uncounted.
type GitHubMain struct {
	Repo   string // owner/name, e.g. kiff/agents
	Branch string // e.g. main
	Token  string // optional; raises GitHub's rate limit
	API    string // https://api.github.com unless set (tests)
	HTTP   *http.Client
	now    func() time.Time

	mu   sync.Mutex
	seen map[string]versionSeen
}

type versionSeen struct {
	on bool
	at time.Time
}

// OnMain implements VersionChecker.
func (g *GitHubMain) OnMain(ctx context.Context, sha string) (bool, error) {
	if !reVersion.MatchString(sha) {
		return false, nil
	}
	now := time.Now
	if g.now != nil {
		now = g.now
	}
	g.mu.Lock()
	if s, ok := g.seen[sha]; ok && (s.on || now().Sub(s.at) < 10*time.Minute) {
		g.mu.Unlock()
		return s.on, nil
	}
	g.mu.Unlock()

	api := g.API
	if api == "" {
		api = "https://api.github.com"
	}
	client := g.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	url := fmt.Sprintf("%s/repos/%s/compare/%s...%s", strings.TrimRight(api, "/"), g.Repo, g.Branch, sha)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var on bool
	switch resp.StatusCode {
	case http.StatusOK:
		var body struct {
			Status string `json:"status"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
			return false, err
		}
		on = body.Status == "identical" || body.Status == "behind"
	case http.StatusNotFound:
		on = false // no such commit in the repository
	default:
		return false, fmt.Errorf("proofs: github compare: status %d", resp.StatusCode)
	}
	g.mu.Lock()
	if g.seen == nil {
		g.seen = map[string]versionSeen{}
	}
	g.seen[sha] = versionSeen{on: on, at: now()}
	g.mu.Unlock()
	return on, nil
}
