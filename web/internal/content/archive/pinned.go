package archive

// pinned is the SHA-256 of each revision's manifest.json. The manifest
// lists the SHA-256 of every captured file, so this one value fixes the
// whole revision. cmd/archive-docs prints the line to add here when it
// captures a revision. Never change an existing line: a revision the DPA
// or the Terms may point at must not change.
var pinned = map[string]string{
	// Captured from ece3830db42d (main).
	"2026-10-09": "df6e53105a5916788552bec262047baae397734646e62f7aee0894f99845bc06",
	// Captured from a0b7a19700ba (main), the v1 launch revision.
	"2026-10-10": "a6472dab5c364e093fc9d56b8b0ea6d46e93cebb0c78aaafc134e94898e332bd",
}
