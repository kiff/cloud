package desktop

import (
	"encoding/json"
	"testing"
)

// linuxRelease is a releases/latest answer for tafs-linux with the AppImage and
// the extra assets named, each on its own repository's release.
func linuxRelease(extra ...string) []byte {
	assets := []map[string]any{{"name": "tafs-v1.1.0-x86_64.AppImage", "size": 43000000, "digest": "sha256:" + sha,
		"browser_download_url": "https://github.com/kiff/tafs-linux/releases/download/v1.1.0/tafs-v1.1.0-x86_64.AppImage"}}
	for _, name := range extra {
		assets = append(assets, map[string]any{"name": name, "size": 120,
			"browser_download_url": "https://github.com/kiff/tafs-linux/releases/download/v1.1.0/" + name})
	}
	b, _ := json.Marshal(map[string]any{"tag_name": "v1.1.0", "assets": assets})
	return b
}

// A release with SHA256SUMS and its signature links both; with only one, or
// with one elsewhere, it links neither, so the page never offers a sum it
// can't also offer the signature for.
func TestALinuxReleaseLinksItsSumsAndSignatureTogether(t *testing.T) {
	r, ok, err := decodeRelease("linux", linuxRelease("SHA256SUMS", "SHA256SUMS.asc"))
	if err != nil || !ok {
		t.Fatalf("got %v %v", ok, err)
	}
	if r.Sums != "https://github.com/kiff/tafs-linux/releases/download/v1.1.0/SHA256SUMS" ||
		r.SumsSignature != "https://github.com/kiff/tafs-linux/releases/download/v1.1.0/SHA256SUMS.asc" {
		t.Errorf("sums %q, signature %q", r.Sums, r.SumsSignature)
	}
	for _, only := range []string{"SHA256SUMS", "SHA256SUMS.asc"} {
		r, _, _ := decodeRelease("linux", linuxRelease(only))
		if r.Sums != "" || r.SumsSignature != "" {
			t.Errorf("with only %s: sums %q, signature %q", only, r.Sums, r.SumsSignature)
		}
	}
	// A signature on another repository's release counts for nothing.
	var w wireRelease
	_ = json.Unmarshal(linuxRelease("SHA256SUMS", "SHA256SUMS.asc"), &w)
	w.Assets[2].URL = "https://github.com/someone/else/releases/download/v1.1.0/SHA256SUMS.asc"
	if a, b := sums(Channels["linux"], w); a != "" || b != "" {
		t.Errorf("another repository's signature: %q %q", a, b)
	}
}
