package videos

import (
	"net/url"
	"testing"
)

func TestLookupEveryGalleryClip(t *testing.T) {
	for _, clip := range append(append(Demos(), ProductTours()...), Capsules()...) {
		t.Run(clip.Slug, func(t *testing.T) {
			got, ok := Lookup(clip.Slug)
			if !ok || got != clip {
				t.Errorf("Lookup(%q) = %+v, %v; want %+v, true", clip.Slug, got, ok, clip)
			}
		})
	}
	for _, slug := range []string{"", "missing", "../mcp-from-zero-en", "https://example.com/video"} {
		if _, ok := Lookup(slug); ok {
			t.Errorf("Lookup(%q) must reject unknown slugs", slug)
		}
	}
}

func TestClipShareURLs(t *testing.T) {
	clips := append(append(Demos(), ProductTours()...), Capsules()...)
	clips = append(clips, Clip{Slug: "encoding", Title: "€50 + €80 & approval — Français?"})
	for _, clip := range clips {
		t.Run(clip.Slug, func(t *testing.T) {
			wantPath := "/proof/videos/" + clip.Slug
			wantURL := "https://kiff.dev" + wantPath
			if clip.PagePath() != wantPath || clip.PageURL() != wantURL {
				t.Errorf("watch path/URL = %q, %q; want %q, %q", clip.PagePath(), clip.PageURL(), wantPath, wantURL)
			}
			wa, err := url.Parse(clip.WhatsAppURL())
			if err != nil {
				t.Fatal(err)
			}
			if wa.Scheme != "https" || wa.Host != "wa.me" || wa.Query().Get("text") != clip.Title+"\n"+wantURL {
				t.Errorf("WhatsApp share failed to preserve title and contextual URL: %s", wa)
			}
			li, err := url.Parse(clip.LinkedInURL())
			if err != nil {
				t.Fatal(err)
			}
			if li.Scheme != "https" || li.Host != "www.linkedin.com" || li.Path != "/sharing/share-offsite/" || li.Query().Get("url") != wantURL {
				t.Errorf("LinkedIn share failed to preserve contextual URL: %s", li)
			}
		})
	}
}

func TestShortFilmAssets(t *testing.T) {
	for _, clip := range Capsules() {
		if clip.DurationSeconds != 58 || clip.Language != "en" {
			t.Errorf("short film metadata: %+v", clip)
		}
		if clip.ShareImage() != clip.asset("cover.jpg") {
			t.Errorf("short film must use the landscape social cover: %s", clip.ShareImage())
		}
		file := "/demo-1080p.mp4"
		if clip.Portrait {
			file = "/demo-1920p.mp4"
		}
		if clip.ShortDownload() != clip.asset(file[1:]) {
			t.Errorf("native download does not match format: %s", clip.ShortDownload())
		}
		for _, uri := range []string{clip.SrcPortrait(), clip.Src720(), clip.ShortDownload(), clip.Captions(), clip.Subtitles()} {
			u, err := url.Parse(uri)
			if err != nil || u.Query().Get("v") != "captions-20261007" {
				t.Errorf("corrected media must bypass the previous cache: %s", uri)
			}
		}
	}
}
