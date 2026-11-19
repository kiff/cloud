package handlers

import (
	"net/http"

	"github.com/kiff/cloud/web/internal/content/skills"
)

// SkillDomains serves GET /skills/kiff-domains.md — the public,
// agent-installable domain-authoring playbook. Served as markdown so a
// coding agent (Cursor, Kiro, Codex, Claude Code, …) can install it with
// a single curl into its rules directory and then author or extend
// a tenant's kiff.yaml domains against the real grammar. No auth, no JS.
func SkillDomains() http.Handler {
	return skillHandler(skills.Domains())
}

// SkillKiff serves GET /skills/kiff.md: connect an agent or tool through
// KIFF, propose a Card for the owner to issue, and handle a held call.
func SkillKiff() http.Handler {
	return skillHandler(skills.Kiff())
}

func skillHandler(src string) http.Handler {
	body := []byte(src)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(body)
	})
}
