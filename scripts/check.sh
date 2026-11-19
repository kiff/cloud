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

for mod in gateway api web; do
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
	"Secrets Manager name|(^|[^/A-Za-z0-9.])kiff/cloud/[a-z0-9]"
	"EC2 instance id|\bi-[0-9a-f]{8}([0-9a-f]{9})?\b"
	"internal hostname|[a-z0-9-]+\.(internal|local|lan|corp)([^a-z]|$)|ip-[0-9]+-[0-9]+-[0-9]+-[0-9]+"
	"private key block|BEGIN [A-Z ]*PRIVATE KEY"
	"live API key|(sk|pk|rk|whsec)_live_[A-Za-z0-9]+"
	"Stripe or Clerk object id|(price|prod|cus|sub|acct|org|user)_[A-Za-z0-9]{14,}"
	"kiff hostname beyond the public ones|[a-z0-9][a-z0-9.-]*\.kiff\.dev"
	"email address|[A-Za-z0-9][A-Za-z0-9._%+-]*@[A-Za-z0-9.-]+\.[a-z]{2,}"
	# Prices: KIFF publishes its own (PRICING.md, the pricing and terms
	# pages). A price anywhere else is caught.
	"price in dollars|\\\$[0-9][0-9,]*(\.[0-9]+)?([kKmM]\b|/mo|/yr|/month|/year)"
	# A euro price, either way round (€8.99/mo, 8,99 € a month). A bare
	# amount is a Card's limit in a test (€5), not a price.
	"price in euros|(€|EUR) ?[0-9][0-9.,]*( ?(/ ?(mo|yr|month|year|seat)|a (month|year|seat)|per (month|year|seat|person)))|[0-9][0-9.,]* ?(€|EUR)( ?(/ ?(mo|yr|month|year|seat)|a (month|year|seat)|per (month|year|seat|person)))"
	# kiff-cloud's issues and RFCs are private. IETF RFCs have four digits,
	# and references to public repos (kiff/tafs#N) name their repo.
	"private issue reference|kiff(/kiff)?-cloud ?#|(^|[^A-Za-z0-9/_&.])#[0-9]{2,5}\b"
	"private RFC reference|\bRFC[ -]?[0-9]{1,3}\b"
	# A tenant id shaped like a real one (made-up ids in tests read tnt_test,
	# tnt_other, tnt_example9).
	# A link to KIFF's private source repository: its issues, files and pages.
	"private repository link|github\.com/kiff/kiff-cloud\b"
	"real-looking tenant id|tnt_01[0-9a-z]{4,}"
	# Who decided something, never the requirement itself: "the PM", a
	# ruling's time stamp, and a reference cut short to "(review on)".
	"coordination note|\bthe PM\b|\(PM[ ,]|\bPM [0-9]{1,2}:[0-9]{2}|\b(review|probe|ask|point|negatives|read) on\)"
	# A path into kiff-cloud that the export does not carry: design docs,
	# demos, the smoke and Stripe scripts, AGENTS.md, the desktop doc by name.
	# The API's server, its HTTP layer and the deploy are public. Exported paths
	# read as they do here (api/..., internal/...), never apps/...
	"private path reference|(^|[^A-Za-z0-9_./-])(apps/(api|web|gateway)/|docs/|demos?/|spikes/|sdks/|infra/|skills/|scripts/(demo|stripe|smoke)|AGENTS\.md|desktop-client\.md|HANDOFF|BRIEF-|RECORD\.md)"
)
# Public names and placeholder data that match the patterns above, as
# whole matches.
# planque.kiff.dev is named in a doc comment, status.kiff.dev in the README; ai@ and user@ are test actors;
# sk_live_secret and sk_live_abc are the redaction tests' fake credentials.
# accounts.kiff.dev is the sign-in provider's account page, ops@ the sender of
# KIFF's emails and support@ their reply-to, all three seen by every customer;
# #666 and #666666 are an email's grey text colour, not issues.
# legal@ and hello@ are the addresses the site's legal and contact pages give;
# clerk.kiff.dev is the sign-in provider's own host, which every browser loads;
# any .example domain is reserved for examples; pk_live_xyz is the sign-in
# tests' made-up publishable key.
allow='(api|app|mcp|www|planque|status|accounts|clerk|cloud\.tafs)\.kiff\.dev|(security|privacy|gabriel|ai|ops|support|legal|hello)@kiff\.dev|:#666(666)?|user@app\.kiff\.dev|[^@]+@(example\.(com|org|net|invalid|test)|[a-z0-9.-]+\.example|x\.io|kiff\.test|acme\.test)|sk_live_(secret|abc)|pk_live_xyz'

# Files whose hits under one rule are public already, as they are: text the
# site serves today (its pages, posts, decks, docs and generated lists), a
# vendored library, or a guard test's own list of banned words. Each line: the
# rule's label, "|", and an extended regex for the path as grep prints it
# (./web/...).
allow_in=(
	# Served text: the blog, the archive of earlier pages, the decks, the docs
	# pages, the whitepaper, llms.txt, the proofs and spots pages, and the
	# generated lists the site shows (apps, invariants), with their sample
	# order numbers, design names, source paths and contact addresses.
	"private issue reference|\./web/internal/content/(blog|archive|apps)/"
	"private RFC reference|\./web/internal/content/(blog|archive|whitepaper|invariants)/"
	"private path reference|\./web/internal/content/(archive|blog|decks|docs|invariants|proofs|quickstart|spots|videos|whitepaper)/"
	"email address|\./web/internal/content/(blog|docs|llmstxt)/"
	"kiff hostname beyond the public ones|\./web/internal/content/(archive|blog|decks|docs)/"
	# A vendored library (open-props), minified: its colours read like numbers.
	"private issue reference|\./web/internal/assets/static/open-props\.min\.css"
	# The demo's sample orders ("Refund order #8812"), shown on the site.
	"private issue reference|\./web/internal/delegate/catalog\.go"
	# The archive of earlier pages, as kiff.dev serves it: links a visitor
	# can't open, kept as served.
	"private repository link|\./web/internal/content/archive/"
	# Page-voice guards: the words a served page must never contain.
	"private repository link|\./web/internal/handlers/(landing|pricing)_test\.go"
	"private path reference|\./web/internal/handlers/(dpa|legal_pages|privacy|sla|support|terms)_test\.go"
	# The quickstart's commands append to the user's own AGENTS.md.
	"private path reference|\./web/internal/content/quickstart/quickstart\.go"
	# The media bucket's URL, as kiff.dev serves it in a post and the videos
	# list: an account id, not a secret.
	"AWS account id|\./web/internal/content/(blog/posts/from-a-refund-tool-to-a-governed-mcp-agent\.md|videos/videos\.go)"
	"AWS ARN or host|\./web/internal/content/(blog/posts/from-a-refund-tool-to-a-governed-mcp-agent\.md|videos/videos\.go)"
	# Published prices: the pricing and terms pages, the words that quote them
	# and their tests, llms.txt, and the figures in the decks and proofs.
	"price in euros|\./web/internal/(content/llmstxt/|pages/(pricing|terms)(\.templ|_templ\.go)|pages/tafs_words_[a-z]{2}\.go|pages/tafs_test\.go|handlers/(tafs|terms)_test\.go)"
	"price in dollars|\./web/internal/content/(decks|proofs)/"
	# The deploy's check that the box's role may read each secret matches
	# secrets by the shape of their ARN (arn:aws:secretsmanager:*), not one ARN:
	# a real ARN carries the account id, which its own rule still catches.
	"AWS ARN or host|\./scripts/check-deploy-secrets\.sh"
)

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
	for one in "${allow_in[@]}"; do
		[[ "${one%%|*}" == "$label" ]] || continue
		hits="$(grep -vE "^(${one#*|})[^:]*:[0-9]+:" <<<"$hits" || true)"
	done
	if [[ -n "$hits" ]]; then
		echo "-- $label"
		echo "$hits"
		grep_failed=1
	fi
done
[[ $grep_failed -eq 0 ]] || failed+=("grep")

# A workflow job's env is printed in clear at the top of each step's log, and this repository's logs are
# public. So a value that names an account, a person, a key or a repository by id is read as `secrets.*`
# (masked), never `vars.*`. Checked in the repository's own workflows, which the published tree doesn't
# carry. The rule tries itself first on a line it must catch and one it must pass, so it never passes by
# matching nothing.
id_vars() { # files...: the `vars.NAME` reads whose NAME carries ids
	grep -noE 'vars\.[A-Z0-9_]+' "$@" 2>/dev/null |
		grep -E 'vars\.[A-Z0-9_]*(TENANT|SUBJECT|SERVICE_ACCOUNT|_EMAIL$|_IDS?$|_KEYS?$)' |
		# Clerk's publishable key is public by design: every page that signs in serves it.
		grep -vE 'vars\.KIFF_CLERK_PUBLISHABLE_KEY$' || true
}
printf '==> no id read from a workflow variable\n'
repo="$(cd "$(dirname "$0")/.." && pwd)"
probe="$(mktemp)"
printf '%s\n' '      KIFF_GOOGLE_TEST_TENANTS: ${{ vars.KIFF_GOOGLE_TEST_TENANTS }}' > "$probe"
caught="$(id_vars "$probe")"
printf '%s\n' '      KIFF_GOOGLE_TEST_TENANTS: ${{ secrets.KIFF_GOOGLE_TEST_TENANTS }}' '      KIFF_CLERK_PUBLISHABLE_KEY: ${{ vars.KIFF_CLERK_PUBLISHABLE_KEY }}' > "$probe"
passed="$(id_vars "$probe")"
rm -f "$probe"
if [[ -z "$caught" || -n "$passed" ]]; then
	echo "the rule itself is broken: it must catch a vars.*TENANTS read and pass the secrets.* form"
	failed+=("id vars")
elif [[ ! -d "$repo/.github/workflows" ]]; then
	echo "no workflows at $repo/.github/workflows: run scripts/check.sh from this repository's checkout"
	failed+=("id vars")
else
	hits="$(id_vars "$repo"/.github/workflows/*.yml)"
	if [[ -n "$hits" ]]; then
		echo "$hits"
		echo "read these from secrets.*: a job's env shows vars.* in clear in the public log"
		failed+=("id vars")
	fi
fi

if [[ ${#failed[@]} -gt 0 ]]; then
	printf '\nFAILED:\n'
	printf '  %s\n' "${failed[@]}"
	exit 1
fi
printf '\nall checks passed on %s\n' "$out"
