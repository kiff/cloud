// Package skills embeds the public, agent-installable KIFF skills served
// under /skills/. A skill is a portable SKILL.md playbook a coding agent
// (Cursor, Kiro, Codex, Claude Code, …) can install so it authors KIFF
// artifacts against the real grammar — the same distribution shape as
// other agent-skill catalogs, served from kiff.dev so it can be fetched
// with a single curl.
//
// These files are the public surface: no private repo paths, no
// authenticated routes, property-led voice per the site's honesty rules.
package skills

import _ "embed"

//go:embed kiff-domains.md
var domains string

//go:embed kiff.md
var kiff string

// Kiff returns the kiff SKILL.md served at GET /skills/kiff.md: the
// playbook a coding agent or a running agent installs to connect tools
// through the MCP gateway, draft a Card for the owner, and handle a held
// call without routing around it.
func Kiff() string { return kiff }

// Domains returns the kiff-domains SKILL.md served at
// GET /skills/kiff-domains.md — the domain-authoring playbook a coding
// agent installs to build and extend kiff.yaml domains.
func Domains() string { return domains }
