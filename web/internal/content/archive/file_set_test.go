package archive

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
)

func TestRevisionFileSet(t *testing.T) {
	const id = "2026-10-09"
	const dir = "revisions/" + id + "/"
	for _, tc := range []struct {
		name, extra string
		duplicate   bool
	}{
		{name: "unchanged"},
		{name: "extra page", extra: "pages/extra.html"},
		{name: "extra image", extra: "assets/extra.jpg"},
		{name: "duplicate declaration", duplicate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, image := []byte("<p>captured</p>"), []byte("image")
			m := Manifest{Revision: id, Pages: []Page{{Slug: "example", File: "pages/example.html", SHA256: Digest(page)}}, Assets: []Asset{{File: "assets/example.jpg", SHA256: Digest(image)}}}
			if tc.duplicate {
				m.Assets = append(m.Assets, m.Assets[0])
			}
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			fsys := fstest.MapFS{
				dir + "manifest.json":      &fstest.MapFile{Data: raw},
				dir + "pages/example.html": &fstest.MapFile{Data: page},
				dir + "assets/example.jpg": &fstest.MapFile{Data: image},
			}
			if tc.extra != "" {
				fsys[dir+tc.extra] = &fstest.MapFile{Data: []byte("extra")}
			}
			_, err = verify(fsys, map[string]string{id: Digest(raw)}, id)
			if tc.extra == "" && !tc.duplicate {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("changed file set accepted")
			} else if tc.extra != "" && !strings.Contains(err.Error(), "unlisted") {
				t.Fatal(err)
			} else if tc.duplicate && !strings.Contains(err.Error(), "twice") {
				t.Fatal(err)
			}
		})
	}
}
