// Package llmstxt embeds the /llms.txt file served at the site root.
//
// llms.txt (llmstxt.org) is a curated, token-efficient map of the
// site's most important content for LLMs and AI agents — the
// equivalent of robots.txt / sitemap.xml for language models. It is a
// single hand-maintained markdown file (no build step); keep it in
// sync with the public site and docs as they change.
//
// It is deliberately property-led and lists only PUBLIC surfaces —
// the framework, kiff-guard, Studio, Cloud, docs, and legal pages.
// It must never reference the operator/admin surface (/ops) or any
// authenticated dashboard route.
package llmstxt

import _ "embed"

//go:embed llms.txt
var content string

// Content returns the llms.txt body served at GET /llms.txt.
func Content() string { return content }
