// Package assets embeds static files served by the web binary at
// /static/*.
//
// Vendored vendor files (open-props, htmx) and the hand-written
// app.css land in static/. Per the web-stack design, no build step
// produces these — they are checked in.
package assets

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
)

//go:embed all:static
var static embed.FS

// FS returns the embedded static file system rooted at /static.
func FS() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err) // unreachable: directory exists at build time
	}
	return sub
}

// hashes maps each embedded file's path (relative to static/) to a
// short digest of its contents. embed.FS files carry no modification
// time, so without this http.FileServer sends no Last-Modified, no ETag
// and no Cache-Control, and the browser downloads every stylesheet,
// font and image again on every navigation.
var hashes = func() map[string]string {
	out := map[string]string{}
	root := FS()
	_ = fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(root, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[p] = hex.EncodeToString(sum[:6])
		return nil
	})
	return out
}()

// URL returns the /static URL for p (relative to static/, e.g.
// "css/cards.css") versioned by its content, so it can be cached for
// good: a changed file gets a new URL on the next deploy.
func URL(p string) string {
	if h, ok := hashes[p]; ok {
		return "/static/" + p + "?v=" + h
	}
	return "/static/" + p
}

// Handler serves the embedded files under /static/. A request carrying
// ?v= (from URL, or a hand-bumped version) is immutable for a year;
// anything else may be reused for five minutes and then revalidates
// against its content ETag.
func Handler() http.Handler {
	files := http.FileServer(http.FS(FS()))
	return http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := hashes[r.URL.Path]; ok {
			w.Header().Set("ETag", `"`+h+`"`)
			if r.URL.Query().Has("v") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "public, max-age=300")
			}
		}
		files.ServeHTTP(w, r)
	}))
}
