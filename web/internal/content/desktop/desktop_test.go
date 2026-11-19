package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const sha = "ff7c873e4e9945d024b769549bbd170727d9f36bb31c14c88ea83df5e74826f6"

// release is a releases/latest answer with one asset.
func release(tag string, draft, pre bool, name, digest string) string {
	b, _ := json.Marshal(map[string]any{
		"tag_name": tag, "draft": draft, "prerelease": pre,
		"assets": []map[string]any{{"name": name, "size": 15597295, "digest": digest,
			"browser_download_url": "https://github.com/kiff/tafs-macos/releases/download/" + tag + "/" + name}},
	})
	return string(b)
}

// github is a fake GitHub: each repository's answer, by repo, and a count of reads.
type github struct {
	mu      sync.Mutex
	answers map[string]struct {
		code int
		body string
	}
	reads atomic.Int32
	agent atomic.Value
}

func (g *github) set(repo string, code int, body string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.answers[repo] = struct {
		code int
		body string
	}{code, body}
}

func newGitHub(t *testing.T) (*github, *httptest.Server) {
	g := &github{answers: map[string]struct {
		code int
		body string
	}{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.reads.Add(1)
		g.agent.Store(r.Header.Get("User-Agent"))
		// A repository's latest release is answered under its name, its list of
		// releases under "<repo>/releases".
		repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/releases/latest")
		g.mu.Lock()
		a, ok := g.answers[repo]
		g.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(a.code)
		_, _ = w.Write([]byte(a.body))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(ResetForTest)
	return g, srv
}

func TestAPublishedReleaseWithItsDigestIsAvailable(t *testing.T) {
	g, srv := newGitHub(t)
	g.set("kiff/tafs-macos", 200, release("v0.1-2", false, false, "tafs-0.1-2.dmg", "sha256:"+sha))
	if err := (&Source{API: srv.URL}).Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, r := Of("macos")
	if state != StateAvailable || r.Version != "v0.1-2" || r.Size != 15597295 || r.SHA256 != sha ||
		r.URL != "https://github.com/kiff/tafs-macos/releases/download/v0.1-2/tafs-0.1-2.dmg" {
		t.Fatalf("%s %+v", state, r)
	}
	if s, _ := Of("windows"); s != StateComingSoon {
		t.Errorf("windows with no release: %s", s)
	}
	if a, _ := g.agent.Load().(string); a != "kiff.dev" {
		t.Errorf("User-Agent %q", a)
	}
}

func TestOnlyAReleaseTheSiteCanVouchForCounts(t *testing.T) {
	for name, body := range map[string]string{
		"a draft":                   release("v1", true, false, "tafs.dmg", "sha256:"+sha),
		"a prerelease":              release("v1", false, true, "tafs.dmg", "sha256:"+sha),
		"another kind's file":       release("v1", false, false, "tafs.exe", "sha256:"+sha),
		"no digest":                 release("v1", false, false, "tafs.dmg", ""),
		"a short digest":            release("v1", false, false, "tafs.dmg", "sha256:abc"),
		"another hash":              release("v1", false, false, "tafs.dmg", "md5:"+sha),
		"a link off github":         strings.Replace(release("v1", false, false, "tafs.dmg", "sha256:"+sha), "https://github.com", "https://example.com", 1),
		"an http link":              strings.Replace(release("v1", false, false, "tafs.dmg", "sha256:"+sha), "https://github.com", "http://github.com", 1),
		"another repository's file": strings.Replace(release("v1", false, false, "tafs.dmg", "sha256:"+sha), "/kiff/tafs-macos/", "/kiff/tafs-linux/", 1),
	} {
		t.Run(name, func(t *testing.T) {
			g, srv := newGitHub(t)
			g.set("kiff/tafs-macos", 200, body)
			if err := (&Source{API: srv.URL}).Read(context.Background()); err != nil {
				t.Fatal(err)
			}
			if s, r := Of("macos"); s != StateComingSoon {
				t.Fatalf("%s %+v", s, r)
			}
		})
	}
}

// A release with both Windows installers offers the x64 one, whichever comes
// first, and one with only ARM64 offers nothing. Linux takes only x86_64.
func TestTheRightFileForTheComputer(t *testing.T) {
	asset := func(repo, tag, name string) map[string]any {
		return map[string]any{"name": name, "size": 1000, "digest": "sha256:" + sha,
			"browser_download_url": "https://github.com/" + repo + "/releases/download/" + tag + "/" + name}
	}
	body := func(assets ...map[string]any) string {
		b, _ := json.Marshal(map[string]any{"tag_name": "v1", "assets": assets})
		return string(b)
	}
	const win, lin = "kiff/tafs-windows", "kiff/tafs-linux"
	for name, c := range map[string]struct {
		os, repo, body, want string
	}{
		"ARM64 listed first": {"windows", win, body(asset(win, "v1", "tafs-setup-1.0-arm64.exe"), asset(win, "v1", "tafs-setup-1.0-x64.exe")), "tafs-setup-1.0-x64.exe"},
		"only ARM64":         {"windows", win, body(asset(win, "v1", "tafs-setup-1.0-arm64.exe")), ""},
		"an unnamed .exe":    {"windows", win, body(asset(win, "v1", "tafs.exe")), ""},
		"Linux x86_64":       {"linux", lin, body(asset(lin, "v1", "tafs-1.0-aarch64.AppImage"), asset(lin, "v1", "tafs-1.0-x86_64.AppImage")), "tafs-1.0-x86_64.AppImage"},
		"Linux aarch64 only": {"linux", lin, body(asset(lin, "v1", "tafs-1.0-aarch64.AppImage")), ""},
	} {
		t.Run(name, func(t *testing.T) {
			g, srv := newGitHub(t)
			g.set(c.repo, 200, c.body)
			if err := (&Source{API: srv.URL}).Read(context.Background()); err != nil {
				t.Fatal(err)
			}
			s, r := Of(c.os)
			if c.want == "" {
				if s != StateComingSoon {
					t.Fatalf("%s %+v, want coming soon", s, r)
				}
				return
			}
			if s != StateAvailable || !strings.HasSuffix(r.URL, "/"+c.want) {
				t.Fatalf("%s %+v, want %s", s, r, c.want)
			}
		})
	}
}

// GitHub down, or an answer that isn't a release, keeps what was read last;
// with nothing read yet, coming soon. A repository with no release (404) is
// coming soon, so unpublishing a release takes it off.
func TestAFailedReadKeepsTheLastGoodState(t *testing.T) {
	g, srv := newGitHub(t)
	src := &Source{API: srv.URL}
	g.set("kiff/tafs-linux", 500, "")
	if err := src.Read(context.Background()); err == nil {
		t.Fatal("a 500 read as fine")
	}
	if s, _ := Of("linux"); s != StateComingSoon {
		t.Fatalf("nothing read yet: %s", s)
	}
	g.set("kiff/tafs-linux", 200, strings.ReplaceAll(release("v2", false, false, "tafs-2.0-x86_64.AppImage", "sha256:"+sha), "tafs-macos", "tafs-linux"))
	if err := src.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	gen := Generation()
	for _, bad := range []struct {
		code int
		body string
	}{{500, ""}, {200, "<html>"}, {200, `{"assets": []}`}, {403, `{"message": "rate limit"}`}} {
		g.set("kiff/tafs-linux", bad.code, bad.body)
		_ = src.Read(context.Background())
		if s, r := Of("linux"); s != StateAvailable || r.Version != "v2" {
			t.Fatalf("%d %q: %s %+v", bad.code, bad.body, s, r)
		}
	}
	if Generation() != gen {
		t.Error("the generation moved with nothing changed")
	}
	g.set("kiff/tafs-linux", 404, `{"message": "Not Found"}`)
	_ = src.Read(context.Background())
	if s, _ := Of("linux"); s != StateComingSoon {
		t.Fatalf("no release left: %s", s)
	}
	if Generation() == gen {
		t.Error("the generation didn't move")
	}
}

func TestAComputerHeldBackIsNotAvailable(t *testing.T) {
	g, srv := newGitHub(t)
	g.set("kiff/tafs-windows", 200, release("v1", false, false, "tafs-setup.exe", "sha256:"+sha))
	_ = (&Source{API: srv.URL}).Read(context.Background())
	HoldNotAvailable(" Windows , linux")
	if s, _ := Of("windows"); s != StateNotAvailable {
		t.Errorf("windows: %s", s)
	}
	if s, _ := Of("linux"); s != StateNotAvailable {
		t.Errorf("linux: %s", s)
	}
	if s, _ := Of("macos"); s != StateComingSoon {
		t.Errorf("macos: %s", s)
	}
}

func TestStartReadsNowAndAgain(t *testing.T) {
	g, srv := newGitHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &Source{API: srv.URL, Every: 20 * time.Millisecond}
	src.Start(ctx)
	if n := g.reads.Load(); n != int32(len(Channels)) {
		t.Fatalf("%d reads at start, want %d", n, len(Channels))
	}
	g.set("kiff/tafs-macos", 200, release("v3", false, false, "tafs.dmg", "sha256:"+sha))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s, _ := Of("macos"); s == StateAvailable {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the release published after start wasn't read again")
}

// tafsRelease is one release of kiff/tafs, the repository the Catalyst Mac app
// shares with the phone apps.
func tafsRelease(tag string, draft, pre bool, files ...string) map[string]any {
	var assets []map[string]any
	for _, f := range files {
		assets = append(assets, map[string]any{"name": f, "size": 41000000, "digest": "sha256:" + sha,
			"browser_download_url": "https://github.com/kiff/tafs/releases/download/" + tag + "/" + f})
	}
	return map[string]any{"tag_name": tag, "draft": draft, "prerelease": pre, "assets": assets,
		"published_at": "2026-11-20T10:00:00Z"}
}

func list(rs ...map[string]any) string {
	b, _ := json.Marshal(rs)
	return string(b)
}

func TestTheCatalystMacIsTheNewestMacReleaseOnKiffTafs(t *testing.T) {
	for name, tc := range map[string]struct {
		body    string
		state   string
		version string
	}{
		"no release at all": {list(), StateComingSoon, ""},
		"only the phones' releases": {list(
			tafsRelease("android-v1.37.0", false, false, "tafs-1.37.0.aab"),
			tafsRelease("ios-v2820", false, false, "tafs.ipa"),
		), StateComingSoon, ""},
		"a Mac release among the phones'": {list(
			tafsRelease("android-v1.37.0", false, false, "tafs-1.37.0.aab"),
			tafsRelease("mac-v1.0.0", false, false, "tafs-mac-1.0.0.dmg"),
			tafsRelease("ios-v2820", false, false, "tafs.ipa"),
		), StateAvailable, "mac-v1.0.0"},
		"the newest Mac release wins": {list(
			tafsRelease("mac-v1.1.0", false, false, "tafs-mac-1.1.0.dmg"),
			tafsRelease("mac-v1.0.0", false, false, "tafs-mac-1.0.0.dmg"),
		), StateAvailable, "mac-v1.1.0"},
		"a Mac prerelease is passed over for the release before it": {list(
			tafsRelease("mac-v1.1.0", false, true, "tafs-mac-1.1.0.dmg"),
			tafsRelease("mac-v1.0.0", false, false, "tafs-mac-1.0.0.dmg"),
		), StateAvailable, "mac-v1.0.0"},
		"a Mac draft is passed over": {list(
			tafsRelease("mac-v1.1.0", true, false, "tafs-mac-1.1.0.dmg"),
		), StateComingSoon, ""},
		"a Mac release without its file is coming soon": {list(
			tafsRelease("mac-v1.0.0", false, false, "notes.txt"),
		), StateComingSoon, ""},
		"another app's DMG under a Mac tag doesn't count": {list(
			tafsRelease("mac-v1.0.0", false, false, "tafs-0.1-2.dmg"),
		), StateComingSoon, ""},
		"a Mac-named file in a phone release doesn't count": {list(
			tafsRelease("ios-v2820", false, false, "tafs-mac-1.0.0.dmg"),
		), StateComingSoon, ""},
	} {
		t.Run(name, func(t *testing.T) {
			g, srv := newGitHub(t)
			g.set("kiff/tafs/releases", 200, tc.body)
			if err := (&Source{API: srv.URL}).Read(context.Background()); err != nil {
				t.Fatal(err)
			}
			state, r := Of("macos13")
			if state != tc.state || r.Version != tc.version {
				t.Fatalf("macos13: %s %q, want %s %q", state, r.Version, tc.state, tc.version)
			}
			if state == StateAvailable && (r.URL != "https://github.com/kiff/tafs/releases/download/"+tc.version+"/tafs-mac-"+strings.TrimPrefix(tc.version, "mac-v")+".dmg" || r.SHA256 != sha || r.Size != 41000000) {
				t.Fatalf("macos13 release: %+v", r)
			}
			// The two Macs never mix: the macOS 12 app still comes only from kiff/tafs-macos.
			if s, _ := Of("macos"); s != StateComingSoon {
				t.Fatalf("macos read kiff/tafs: %s", s)
			}
		})
	}
}

func TestEachMacHasItsOwnRelease(t *testing.T) {
	g, srv := newGitHub(t)
	g.set("kiff/tafs-macos", 200, release("v0.1-2", false, false, "tafs-0.1-2.dmg", "sha256:"+sha))
	g.set("kiff/tafs/releases", 200, list(tafsRelease("mac-v1.0.0", false, false, "tafs-mac-1.0.0.dmg")))
	if err := (&Source{API: srv.URL}).Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s, r := Of("macos"); s != StateAvailable || r.Version != "v0.1-2" {
		t.Errorf("macos: %s %+v", s, r)
	}
	if s, r := Of("macos13"); s != StateAvailable || r.Version != "mac-v1.0.0" {
		t.Errorf("macos13: %s %+v", s, r)
	}
	HoldNotAvailable("macos13")
	if s, _ := Of("macos13"); s != StateNotAvailable {
		t.Errorf("macos13 held back: %s", s)
	}
	if s, _ := Of("macos"); s != StateAvailable {
		t.Errorf("holding macos13 back moved macos: %s", s)
	}
}

func TestTheCatalystMacsReleaseFileMustBeOnKiffTafs(t *testing.T) {
	g, srv := newGitHub(t)
	r := tafsRelease("mac-v1.0.0", false, false, "tafs-mac-1.0.0.dmg")
	r["assets"].([]map[string]any)[0]["browser_download_url"] = "https://github.com/kiff/tafs-macos/releases/download/mac-v1.0.0/tafs-mac-1.0.0.dmg"
	g.set("kiff/tafs/releases", 200, list(r))
	_ = (&Source{API: srv.URL}).Read(context.Background())
	if s, _ := Of("macos13"); s != StateComingSoon {
		t.Fatalf("a file on another repository: %s", s)
	}
}
