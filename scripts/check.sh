#!/usr/bin/env bash
# Checks a tree as it's published (CI runs it on every pull request):
# gofmt, vet, build and tests in each module, a gitleaks scan of every file,
# and a grep for what must not be published (account and resource ids,
# internal hostnames, keys, email addresses).
#
#   scripts/check.sh <out-dir>
#
# Postgres-backed tests run when KIFF_TEST_POSTGRES_URL is set and skip
# otherwise. gitleaks is pinned and fetched by `go run`; it needs network
# access the first time.
set -uo pipefail

GITLEAKS="github.com/zricethezav/gitleaks/v8@v8.28.0"

[[ $# -eq 1 ]] || { echo "usage: $0 <out-dir>" >&2; exit 2; }
# An out-dir that isn't there (an export that failed) stops here: without it,
# the steps below would run in the current directory, the private repo.
[[ -d "$1" ]] || { echo "no such out-dir: $1" >&2; exit 2; }
out="$(cd "$1" && pwd)"
export TZ=UTC

failed=()
step() { # label, dir, command...
	local label="$1" dir="$2"
	shift 2
	printf '==> %s\n' "$label"
	if ! (cd "$dir" && "$@"); then
		failed+=("$label")
	fi
}
unformatted() { local f; f="$(gofmt -l .)"; [[ -z "$f" ]] || { echo "$f"; return 1; }; }

for mod in gateway api; do
	step "$mod: gofmt" "$out/$mod" unformatted
	step "$mod: go vet" "$out/$mod" go vet ./...
	step "$mod: go build" "$out/$mod" go build ./...
	step "$mod: go test" "$out/$mod" go test -race -count=1 -timeout 600s ./...
done

step "gitleaks" "$out" go run "$GITLEAKS" dir --no-banner --redact -v .

# What must never be in the public tree. Each line: a label and an
# extended regex. Any match fails the check.
deny=(
	"AWS account id|(^|[^0-9a-fA-F])[0-9]{12}([^0-9a-fA-F]|$)"
	"AWS ARN or host|arn:aws|amazonaws\.com"
	"internal hostname|[a-z0-9-]+\.(internal|local|lan|corp)([^a-z]|$)|ip-[0-9]+-[0-9]+-[0-9]+-[0-9]+"
	"private key block|BEGIN [A-Z ]*PRIVATE KEY"
	"live API key|(sk|pk|rk|whsec)_live_[A-Za-z0-9]+"
	"Stripe or Clerk object id|(price|prod|cus|sub|acct|org|user)_[A-Za-z0-9]{14,}"
	"kiff hostname beyond the public ones|[a-z0-9.-]+\.kiff\.dev"
	"email address|[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[a-z]{2,}"
	"price (KIFF publishes none)|\\\$[0-9][0-9,]*(\.[0-9]+)?([kKmM]\b|/mo|/yr|/month|/year)"
	# A euro price, either way round (€8.99/mo, 8,99 € a month). A bare
	# amount is a Card's limit in a test (€5), not a price.
	"price in euros|(€|EUR) ?[0-9][0-9.,]*( ?(/ ?(mo|yr|month|year|seat)|a (month|year|seat)|per (month|year|seat|person)))|[0-9][0-9.,]* ?(€|EUR)( ?(/ ?(mo|yr|month|year|seat)|a (month|year|seat)|per (month|year|seat|person)))"
	# kiff-cloud's issues and RFCs are private. IETF RFCs have four digits,
	# and references to public repos (kiff/tafs#N) name their repo.
	"private issue reference|kiff(/kiff)?-cloud ?#|(^|[^A-Za-z0-9/_&.-])#[0-9]{2,5}\b"
	"private RFC reference|\bRFC[ -]?[0-9]{1,3}\b"
	# A path into kiff-cloud that the export does not carry: design docs,
	# demos, deploy and smoke scripts (deploy.sh by name too), the API's
	# server and HTTP layer, AGENTS.md, the desktop doc by name. Exported paths read as they do in
	# kiff/cloud (api/..., internal/...), never apps/...
	"private path reference|(^|[^A-Za-z0-9_./-])(apps/(api|web|gateway)/|docs/|demos?/|spikes/|sdks/|infra/|skills/|scripts/(demo|stripe|smoke|deploy|caddy|systemd)|cmd/api\b|handlers/[a-z_]+\.go|AGENTS\.md|desktop-client\.md|HANDOFF|BRIEF-|RECORD\.md|deploy\.sh)"
)
# Public names and placeholder data that match the patterns above, as
# whole matches.
# planque.kiff.dev is named in a doc comment, status.kiff.dev in the README; ai@ and user@ are test actors;
# sk_live_secret and sk_live_abc are the redaction tests' fake credentials.
# accounts.kiff.dev is the sign-in provider's account page, ops@ the sender of
# KIFF's emails and support@ their reply-to, all three seen by every customer;
# #666 and #666666 are an email's grey text colour, not issues.
allow='(api|app|mcp|www|planque|status|accounts)\.kiff\.dev|(security|privacy|gabriel|ai|ops|support)@kiff\.dev|:#666(666)?|user@app\.kiff\.dev|[^@]+@(example\.(com|org|net|invalid)|x\.io|kiff\.test|acme\.test)|sk_live_(secret|abc)'

# Backups an in-place edit can leave (BSD sed -i -e, .bak, .orig).
printf '==> no editor backups\n'
backups="$(cd "$out" && find . -type f \( -name '*-e' -o -name '*.bak' -o -name '*.orig' -o -name '*~' \) )"
if [[ -n "$backups" ]]; then
	echo "$backups"
	failed+=("backups")
fi

printf '==> grep for what must not be published\n'
grep_failed=0
for rule in "${deny[@]}"; do
	label="${rule%%|*}"
	re="${rule#*|}"
	hits="$(cd "$out" && grep -rnoIE "$re" . | grep -vE "^[^:]+:[0-9]+:($allow)$" || true)"
	if [[ -n "$hits" ]]; then
		echo "-- $label"
		echo "$hits"
		grep_failed=1
	fi
done
[[ $grep_failed -eq 0 ]] || failed+=("grep")

if [[ ${#failed[@]} -gt 0 ]]; then
	printf '\nFAILED:\n'
	printf '  %s\n' "${failed[@]}"
	exit 1
fi
printf '\nall checks passed on %s\n' "$out"
