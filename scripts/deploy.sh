#!/usr/bin/env bash
#
# scripts/deploy.sh — deploy api + web + gateway to the kiff-cloud
# AWS box.
#
# Architecture:
#
#   laptop                     S3 staging                EC2 box
#   ──────────                 ──────────                ───────────────
#   go build (linux/arm64)  →  kiff-deploy-staging-...  →  /opt/kiff/bin
#   sha256sum captured                                     /etc/kiff/*.env
#                                                          systemctl restart
#                                                          curl /healthz
#
# Transport is S3 + SSM Run Command — no SSH, no inbound port.
# Secrets resolve server-side from Secrets Manager so values
# never enter shell history, argv, or CloudTrail beyond the ARN.
#
# Pre-conditions:
#   - AWS CLI authed for the kiff-cloud account.
#   - go 1.24+, jq, sha256sum, awk on the laptop.
#   - The box's resources exist (instance, RDS, log groups).
#   - Drop a .env.deploy alongside this script with at minimum:
#       KIFF_AUTH_PROVIDER=clerk         # required; the binaries refuse stub
#       KIFF_DEMO_TENANT_ID=<ulid>       # required for clerk mode
#       KIFF_STUB_SUBJECTS=dev-user      # optional; comma-separated
#     .env.deploy is gitignored. Do not put secrets in it; the
#     Clerk and DB and web-demo-key secrets live in Secrets
# Manager and are resolved on the box.
#
#       KIFF_CLERK_FRONTEND_API=clerk.kiff.dev   # required for clerk mode
#       KIFF_CLERK_PUBLISHABLE_KEY=pk_live_...  # public (it ships in every page); a repository variable, not a secret
#                                                 # required for clerk mode
#
# Flags:
#   --skip-build               skip the local cross-compile step
#   --skip-smoke               skip the post-restart healthz check
#   --git-sha=<sha>            redeploy a previously-staged build
#   --dry-run                  print the SSM command body without sending
#
# Exit codes:
#   0 success
#   1 usage error
#   2 prerequisite missing (aws, go, jq, sha256sum)
#   3 build failed
#   4 upload failed
#   5 remote install/restart failed
#   6 smoke failed (binaries not healthy after restart)
#   7 ssm command timed out
#   8 a public /healthz answered other than 200 during the deploy
set -euo pipefail

# ─── constants pinned to the box's captured state ──────────────
AWS_REGION="${AWS_REGION:-eu-west-1}"
INSTANCE_ID="${INSTANCE_ID:-}"
STAGING_BUCKET="${STAGING_BUCKET:-}"
# The gateway's tool-credential keys ("id:base64,…", current
# first). Only kiff-gateway's env file receives them. Its Secrets Manager
# name comes from the environment, like the ARNs below.
GATEWAY_KEYS_SECRET="${GATEWAY_KEYS_SECRET:-}"
# The execution-permit signing key ("kid:base64url(seed)") and any
# extra published keys (the next key, or a retired one). Optional: a missing
# signing key leaves permits off. Only kiff-api's env file receives them.
# Their names come from the environment; "-" reads neither.
PERMIT_KEY_SECRET="${PERMIT_KEY_SECRET:-}"
PERMIT_PUBLISHED_SECRET="${PERMIT_PUBLISHED_SECRET:-}"
# The secret the connection service signs the refund events it hands KIFF
# with, by its full ARN. "-" reads nothing and
# leaves the gateway's receiver (/v1/planque/refund-events) off: the box's
# role is granted secrets by exact ARN, and an ungranted read stops the
# deploy. Set it in .env.deploy once the secret exists and the role may
# read it. Only
# kiff-gateway's env file receives it.
PLANQUE_EVENT_SECRET_ARN="${PLANQUE_EVENT_SECRET_ARN:-}"
# The integration proofs' two secrets, by full ARN, as PLANQUE_EVENT_SECRET_ARN above: planque's
# read token(s) for GET /v1/ops/proofs/state (KIFF_PROOF_STATE_TOKENS) and the GitHub token KIFF
# uses to confirm a runner commit is on kiff/agents main (KIFF_PROOF_GITHUB_TOKEN). Empty state
# tokens: no planque read. No GitHub token is needed: kiff/agents is public, so KIFF asks GitHub's
# compare API without one (a confirmed commit is kept for good); the token only raises the rate
# limit. KIFF_PROOF_RUNNER_VERSIONS, when set, replaces that check with its list: a runner commit
# it doesn't list is never counted, so leave it "-" unless GitHub can't be reached.
# Set the state tokens' ARN in .env.deploy ("-" for none) once the secret exists and the box's
# role may read it; check-deploy-secrets.sh then checks the grant before every deploy.
PROOF_STATE_TOKENS_ARN="${PROOF_STATE_TOKENS_ARN:-}"
PROOF_GITHUB_TOKEN_ARN="${PROOF_GITHUB_TOKEN_ARN:-}"
DB_SECRET_ARN="${DB_SECRET_ARN:-}"
# KIFF's one secret: a JSON of named values (clerk_secret_key, session_secret, anchor_private_key,
# web_demo_key, resend_api_key, stripe_webhook_secret, stripe_secret_key, clerk_webhook_secret,
# gateway_keys, permit_key, permit_published, planque_event_secret, proof_state_tokens,
# proof_github_token, google_play_service_account), by its full ARN. Set: every value is read from it,
# once, and a required name it lacks stops the deploy; an optional name it lacks is off. Empty: each
# value is read from its own secret below, as before. The database's own (DB_SECRET_ARN, RDS-managed
# and rotated) is never in it.
KIFF_SECRET_ARN="${KIFF_SECRET_ARN:-}"
CLERK_SECRET_ARN="${CLERK_SECRET_ARN:-}"
# Clerk's publishable key is public (it ships in every page), so it comes from the repository
# variable KIFF_CLERK_PUBLISHABLE_KEY (deploy.yml, or .env.deploy), not from Secrets Manager.
KIFF_CLERK_PUBLISHABLE_KEY="${KIFF_CLERK_PUBLISHABLE_KEY:-}"
SESSION_SECRET_ARN="${SESSION_SECRET_ARN:-}"
ANCHOR_WALLET_ARN="${ANCHOR_WALLET_ARN:-}"
WEB_DEMO_KEY_ARN="${WEB_DEMO_KEY_ARN:-}"
# Resend API key for customer email notifications.
RESEND_SECRET_ARN="${RESEND_SECRET_ARN:-}"
# Stripe webhook signing secret (whsec_…) for the billing
# sync. Verifies the Stripe-Signature on the
# unauthenticated /webhooks/stripe endpoint; empty leaves the
# webhook route unmounted.
STRIPE_WEBHOOK_SECRET_ARN="${STRIPE_WEBHOOK_SECRET_ARN:-}"
# Stripe restricted key (rk_live_…) the API uses to mint Customer
# Portal sessions on /v1/me/billing-portal. Runtime credential
# (used on every "Manage billing" click), not one-time — rotate via
# Secrets Manager, do not revoke. Empty leaves the portal route
# unmounted (the past_due banner still works off webhook state).
STRIPE_SECRET_KEY_ARN="${STRIPE_SECRET_KEY_ARN:-}"
# Clerk's webhook signing secret (whsec_…) for /webhooks/clerk, the
# new-device email. On since
# 10 Nov 2026; kiff-ec2-app-role reads this exact ARN. To turn it off,
# set it to "-" in .env.deploy.
CLERK_WEBHOOK_SECRET_ARN="${CLERK_WEBHOOK_SECRET_ARN:-}"
# Business intake link for the pricing page's CTA: a public URL, so a repository variable
# (KIFF_PRICING_BUSINESS_INTAKE_URL), not a secret.
KIFF_PRICING_BUSINESS_INTAKE_URL="${KIFF_PRICING_BUSINESS_INTAKE_URL:-}"
DB_HOST="${DB_HOST:-}"
# The contact address Let's Encrypt has for the Caddyfile's certificates.
KIFF_ACME_EMAIL="${KIFF_ACME_EMAIL:-}"
DB_NAME="${DB_NAME:-kiff}"
SSM_TIMEOUT_SEC="${SSM_TIMEOUT_SEC:-300}"

# ─── arg parsing ───────────────────────────────────────────────
skip_build=0
skip_smoke=0
dry_run=0
forced_sha=""

for arg in "$@"; do
	case "$arg" in
		--skip-build) skip_build=1 ;;
		--skip-smoke) skip_smoke=1 ;;
		--dry-run)    dry_run=1 ;;
		--git-sha=*)  forced_sha="${arg#*=}"; skip_build=1 ;;
		-h|--help)
			sed -n '2,/^set/p' "$0" | sed 's/^# \?//' | head -n 50
			exit 0
			;;
		*) echo "unknown flag: $arg" >&2; exit 1 ;;
	esac
done

# ─── helpers ────────────────────────────────────────────────────
log()  { printf '[deploy] %s\n' "$*" >&2; }
fail() { printf '[deploy] error: %s\n' "$1" >&2; exit "${2:-1}"; }

# Resolve repo root regardless of where deploy.sh is invoked from.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

# ─── prerequisite checks ────────────────────────────────────────
for cmd in aws go jq awk; do
	command -v "$cmd" >/dev/null 2>&1 || fail "missing prereq: $cmd" 2
done

# sha256: prefer GNU `sha256sum` (Linux), fall back to `shasum -a 256` (macOS).
if command -v sha256sum >/dev/null 2>&1; then
	sha256_cmd() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
	sha256_cmd() { shasum -a 256 "$1" | awk '{print $1}'; }
else
	fail "missing prereq: sha256sum or shasum" 2
fi

if ! aws sts get-caller-identity >/dev/null 2>&1; then
	fail "AWS CLI is not authenticated; run 'aws configure' or export creds" 2
fi

# Source .env.deploy. Do not echo its contents. In a git worktree the file
# usually lives only in the main checkout, so fall back to it: a deploy
# that silently ran without it once shipped production on stub auth
# (2026-10-03).
env_file="$REPO_ROOT/.env.deploy"
if [[ ! -f "$env_file" ]]; then
	common_dir="$(git -C "$REPO_ROOT" rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
	main_root=""
	[[ -n "$common_dir" ]] && main_root="$(cd "$common_dir/.." 2>/dev/null && pwd || true)"
	if [[ -n "$main_root" && -f "$main_root/.env.deploy" ]]; then
		env_file="$main_root/.env.deploy"
	fi
fi
if [[ -f "$env_file" ]]; then
	log "env: $env_file"
	# shellcheck disable=SC1090
	set -a; . "$env_file"; set +a
else
	log "env: no .env.deploy found"
fi

# Auth provider: required, never defaulted, and only clerk. This script
# deploys production, where the binaries run with KIFF_ENV=production and
# refuse stub auth at startup; failing here keeps the old binaries running
# instead of restarting into a crash loop.
if [[ -z "${KIFF_AUTH_PROVIDER:-}" ]]; then
	fail "KIFF_AUTH_PROVIDER is not set (no .env.deploy at $REPO_ROOT or the main checkout?). Set it to clerk." 2
fi
if [[ "$KIFF_AUTH_PROVIDER" != "clerk" ]]; then
	fail "KIFF_AUTH_PROVIDER=$KIFF_AUTH_PROVIDER: production runs only with clerk; stub lets anyone sign in as anyone." 2
fi
KIFF_DEMO_TENANT_ID="${KIFF_DEMO_TENANT_ID:-}"
KIFF_STUB_SUBJECTS="${KIFF_STUB_SUBJECTS:-}"
KIFF_LOG_FORMAT="${KIFF_LOG_FORMAT:-json}"
KIFF_CLERK_FRONTEND_API="${KIFF_CLERK_FRONTEND_API:-}"
# Clerk's sign-in can't load without its publishable key: fail before any restart.
if [[ -z "$KIFF_CLERK_PUBLISHABLE_KEY" ]]; then
	fail "KIFF_CLERK_PUBLISHABLE_KEY is not set (the repository variable, or .env.deploy). It is public: pk_live_..." 2
fi
# Session cookie domain. Defaults to ".kiff.dev" so a session
# minted on the marketing apex (kiff.dev) is also sent to the
# dashboard host (app.kiff.dev) — both proxy to the same kiff-web
# binary. Without this the cookie is host-only and sign-in loops
# between the two hosts through Clerk. Override per-environment in
# .env.deploy if the public host ever changes.
KIFF_COOKIE_DOMAIN="${KIFF_COOKIE_DOMAIN:-.kiff.dev}"

# Canonical host for the authenticated surface (sign-in, sign-out,
# dashboard, ops). The web binary answers for both kiff.dev and
# app.kiff.dev; the Clerk handshake derives its redirect_url from the
# request Host, so a sign-in started on the apex stays on the apex,
# where clerk.browser.js cannot hydrate a session and the user loops
# between the Account Portal and /sign-in/callback forever. Setting
# this redirects any gated request on the apex to the app host before
# Clerk is involved, so the handshake always runs where it works.
# Empty disables canonicalization (correct for local single-host dev).
KIFF_APP_HOST="${KIFF_APP_HOST:-app.kiff.dev}"

# Receipt anchoring defaults. KIFF_RECEIPT_MODE=anchor flips on
# the on-chain anchoring loop in api; the wallet private
# key is resolved from Secrets Manager server-side. The contract
# address is the deployed KiffAnchor on Base Sepolia
# (the api's contract/deployments.json). The interval is the
# cadence between anchor sweeps; "0" disables the loop.
KIFF_RECEIPT_MODE="${KIFF_RECEIPT_MODE:-anchor}"
KIFF_ANCHOR_RPC_URL="${KIFF_ANCHOR_RPC_URL:-https://sepolia.base.org}"
KIFF_ANCHOR_CONTRACT="${KIFF_ANCHOR_CONTRACT:-0x2F5468eE0De25c6a86D5F1445F37E6a79039DEa4}"
KIFF_ANCHOR_INTERVAL="${KIFF_ANCHOR_INTERVAL:-30s}"

# Operator console allow-list. Comma-separated Clerk
# subjects permitted to reach the internal /ops control plane. Set on
# BOTH binaries: web mounts /ops behind RequireOperator and
# api re-checks the same list on /v1/ops/*. "-" leaves the
# surface entirely unmounted (404). Set in .env.deploy / env.
#
# This MUST be the Clerk subject (the `sub` claim / Clerk user id,
# e.g. user_…), NOT the kiff tenant slug. The slug is a
# non-reversible hash of the subject and will never match the gate.
KIFF_OPS_SUBJECTS="${KIFF_OPS_SUBJECTS:-}"

# The planques that send relay calls on KIFF's permits; each confirms the
# owner's halt. Comma-separated. Override via
# .env.deploy / env.
KIFF_PLANQUE_URLS="${KIFF_PLANQUE_URLS-https://planque.kiff.dev}"
# The apps allowed to make and use kiff.dev's passkeys (the desktop client
# contract, §2.6). Apple's: "<Team ID>.<bundle ID>", comma separated, served in
# /.well-known/apple-app-site-association. Android's: "<package>=<signing
# certificate SHA-256 as AA:BB:…>", served in /.well-known/assetlinks.json and
# accepted by the API as an android:apk-key-hash origin. Public identifiers,
# not secrets. Empty: the files are 404 and only https://kiff.dev is accepted.
# The tafs app for iPhone, iPad and its Mac Catalyst build (the owner's Team ID, public in every signed build).
KIFF_APPLE_APP_IDS="${KIFF_APPLE_APP_IDS:-R54YWFRK4B.bot.tafs.app}"
# The tafs app for Android: its applicationId (not the Kotlin namespace bot.tafs.app) and Play App Signing's certificate
# (not the upload key), as Play re-signs the installed app.
KIFF_ANDROID_APPS="${KIFF_ANDROID_APPS:-dev.kiff.tafs=65:A3:0B:E8:02:2F:E5:94:36:F8:05:79:04:30:2D:3E:B8:BC:5F:4F:FC:6E:19:1E:BA:69:BD:9C:B7:C3:10:76}"
# kiff.dev's apps list from planque's reviewed proofs (GET /v1/proved), not the list built in: off
# until tafs's gallery reads the same (TAFS_PROVED_FROM_CATALOG). Kept on every deploy, so a switch
# turned on stays on: the repository variable KIFF_APPS_PROVED=on (deploy.yml), or .env.deploy for
# a deploy from a laptop, turns it on. Empty or anything else is off.
KIFF_APPS_PROVED="${KIFF_APPS_PROVED:-}"
# kiff.dev's download page from each computer's published GitHub release (kiff/tafs-macos, -windows,
# -linux): off until KIFF_DESKTOP_RELEASES=on (repository variable, deploy.yml). Then a computer is
# available when its release is published with its file and GitHub's digest, coming soon otherwise, and
# not available when KIFF_DESKTOP_NOT_AVAILABLE names it ("windows,linux"). Empty: off, none held back.
KIFF_DESKTOP_RELEASES="${KIFF_DESKTOP_RELEASES:-}"
KIFF_DESKTOP_NOT_AVAILABLE="${KIFF_DESKTOP_NOT_AVAILABLE:-}"
# The integration proofs' runner: the OIDC claims KIFF accepts
# from it, the managed test tenants, the test phone's keys, a runner-version allow-list and the
# release-anyway switch. None is a secret; all ten are environment variables of `production`
# (deploy.yml). Empty: the proof ingest isn't mounted, no key is restricted, release-anyway is off.
KIFF_PROOF_OIDC_AUDIENCE="${KIFF_PROOF_OIDC_AUDIENCE:-}"
KIFF_PROOF_OIDC_REPOSITORY="${KIFF_PROOF_OIDC_REPOSITORY:-}"
KIFF_PROOF_OIDC_REPOSITORY_ID="${KIFF_PROOF_OIDC_REPOSITORY_ID:-}"
KIFF_PROOF_OIDC_OWNER_ID="${KIFF_PROOF_OIDC_OWNER_ID:-}"
KIFF_PROOF_OIDC_REF="${KIFF_PROOF_OIDC_REF:-}"
KIFF_PROOF_OIDC_WORKFLOW="${KIFF_PROOF_OIDC_WORKFLOW:-}"
KIFF_PROOF_TEST_TENANTS="${KIFF_PROOF_TEST_TENANTS:-}"
KIFF_PROOF_TEST_KEYS="${KIFF_PROOF_TEST_KEYS:-}"
KIFF_PROOF_RUNNER_VERSIONS="${KIFF_PROOF_RUNNER_VERSIONS:-}"
KIFF_PROOF_RELEASE_ANYWAY="${KIFF_PROOF_RELEASE_ANYWAY:-}"
# In the production environment all ten are always set, "-" meaning none (deploy.yml checks it);
# "-" is written as empty, so the API sees no value.
for v in KIFF_PROOF_OIDC_AUDIENCE KIFF_PROOF_OIDC_REPOSITORY KIFF_PROOF_OIDC_REPOSITORY_ID \
	KIFF_PROOF_OIDC_OWNER_ID KIFF_PROOF_OIDC_REF KIFF_PROOF_OIDC_WORKFLOW \
	KIFF_PROOF_TEST_TENANTS KIFF_PROOF_TEST_KEYS KIFF_PROOF_RELEASE_ANYWAY KIFF_PROOF_RUNNER_VERSIONS; do
	if [ "${!v}" = "-" ]; then printf -v "$v" '%s' ""; fi
done

# Test accounts kept small (tenant.Prune): every hour the API deletes what
# each finished with more than KIFF_PRUNE_AFTER ago, and the account goes
# on: the nightly Eval's (ai@kiff.dev) and the assistant's test account.
# Never a customer's account. Set in .env.deploy / env ("-" for none).
KIFF_PRUNE_TENANTS="${KIFF_PRUNE_TENANTS:-}"
KIFF_PRUNE_AFTER="${KIFF_PRUNE_AFTER:-48h}"

# Customer email notifications. The RESEND_API_KEY itself is
# resolved server-side from Secrets Manager (RESEND_SECRET_ARN), never
# rendered into the SSM payload. From/Reply-To are non-secret config.
KIFF_NOTIFY_FROM="${KIFF_NOTIFY_FROM:-KIFF Operations <ops@kiff.dev>}"
KIFF_NOTIFY_REPLY_TO="${KIFF_NOTIFY_REPLY_TO:-support@kiff.dev}"

# App backends a phone may register as its account's held-call alert
# target, comma-separated: today tafs cloud, at cloud.tafs.kiff.dev
# (eu-west-1). Empty means a phone registers none.
# Override via .env.deploy / env.
KIFF_PHONE_ALERT_URLS="${KIFF_PHONE_ALERT_URLS:-https://cloud.tafs.kiff.dev/v1/kiff/alerts}"
KIFF_APPLE_SANDBOX_TENANTS="${KIFF_APPLE_SANDBOX_TENANTS:-}"
# Google Play Billing for the Android app.
# The service account's JSON key lives in Secrets Manager; a missing secret
# leaves Google Play billing off, and so does "-" for its name, which comes
# from the environment. The rest is non-secret config.
GOOGLE_PLAY_SERVICE_ACCOUNT_SECRET="${GOOGLE_PLAY_SERVICE_ACCOUNT_SECRET:-}"
KIFF_GOOGLE_PACKAGE_NAME="${KIFF_GOOGLE_PACKAGE_NAME:-}"
KIFF_GOOGLE_PUSH_AUDIENCE="${KIFF_GOOGLE_PUSH_AUDIENCE:-}"
KIFF_GOOGLE_PUSH_SERVICE_ACCOUNT="${KIFF_GOOGLE_PUSH_SERVICE_ACCOUNT:-}"
KIFF_GOOGLE_TEST_TENANTS="${KIFF_GOOGLE_TEST_TENANTS:-}"
# Signing in inside the tafs apps (API and web): "on", or "test" for KIFF_APP_SIGNIN_TENANTS only
# (comma-separated tenants; empty refuses everyone). Empty or anything else is off.
KIFF_APP_SIGNIN="${KIFF_APP_SIGNIN:-}"
KIFF_APP_SIGNIN_TENANTS="${KIFF_APP_SIGNIN_TENANTS:-}"

# ─── the box, its database and the secrets it reads ─────────────
# Nothing that names KIFF's account, box or users has a default here: each
# comes from .env.deploy or the environment, and a missing one stops the
# deploy before anything is built. "-" turns an optional one off: the
# receiver of planque's refund events, the proofs' state read, Clerk's
# webhook, the operator console, the pruning of test accounts, the permit
# keys and Google Play's service account.
for v in INSTANCE_ID STAGING_BUCKET DB_HOST KIFF_ACME_EMAIL DB_SECRET_ARN CLERK_SECRET_ARN \
	SESSION_SECRET_ARN ANCHOR_WALLET_ARN WEB_DEMO_KEY_ARN RESEND_SECRET_ARN \
	STRIPE_WEBHOOK_SECRET_ARN STRIPE_SECRET_KEY_ARN GATEWAY_KEYS_SECRET; do
	[[ -n "${!v}" && "${!v}" != "-" ]] || fail "$v is not set (no .env.deploy at $REPO_ROOT or the main checkout?)" 2
done
for v in PLANQUE_EVENT_SECRET_ARN PROOF_STATE_TOKENS_ARN CLERK_WEBHOOK_SECRET_ARN \
	KIFF_OPS_SUBJECTS KIFF_PRUNE_TENANTS PERMIT_KEY_SECRET PERMIT_PUBLISHED_SECRET \
	GOOGLE_PLAY_SERVICE_ACCOUNT_SECRET; do
	[[ -n "${!v}" ]] || fail "$v is not set; \"-\" turns it off (no .env.deploy at $REPO_ROOT or the main checkout?)" 2
	if [ "${!v}" = "-" ]; then printf -v "$v" '%s' ""; fi
done
# check-deploy-secrets.sh reads the secrets' names from this file and their
# values from its environment.
while IFS= read -r v; do export "$v"; done < <(sed -nE 's/^([A-Z_]+(_SECRET|_ARN))="\$\{[A-Z_]+:?-.*/\1/p' "$SCRIPT_DIR/deploy.sh" | sort -u)

# ─── git sha used for both build label and S3 prefix ───────────
git_sha="${forced_sha:-$(git rev-parse --short=12 HEAD)}"
# The box's role must read every secret this script reads. A secret
# with no grant stops here, before anything is uploaded; IAM that these
# credentials can't read only warns.
secrets_ok=0
"$(dirname "$0")/check-deploy-secrets.sh" >&2 || secrets_ok=$?
case "$secrets_ok" in
	0) ;;
	2) log "warning: couldn't check the role's secret grants with these credentials" ;;
	*) log "a secret this deploy reads isn't granted to the box's role; grant it first"; exit 1 ;;
esac

log "deploying git_sha=${git_sha} to instance=${INSTANCE_ID}"
log "auth_provider=${KIFF_AUTH_PROVIDER}"

# ─── build ──────────────────────────────────────────────────────
build_dir="$REPO_ROOT/.deploy-staging/$git_sha"
mkdir -p "$build_dir"

if [[ $skip_build -eq 0 ]]; then
	log "building api → ${build_dir}/kiff-api"
	(cd "$REPO_ROOT/api" && \
		GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
		go build -trimpath -ldflags='-s -w' -o "$build_dir/kiff-api" ./cmd/api) \
		|| fail "go build api failed" 3

	log "building web → ${build_dir}/kiff-web"
	(cd "$REPO_ROOT/web" && \
		GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
		go build -trimpath -ldflags='-s -w' -o "$build_dir/kiff-web" ./cmd/web) \
		|| fail "go build web failed" 3

	log "building gateway → ${build_dir}/kiff-gateway"
	(cd "$REPO_ROOT/gateway" && \
		GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
		go build -trimpath -ldflags='-s -w' -o "$build_dir/kiff-gateway" ./cmd/gateway) \
		|| fail "go build gateway failed" 3
fi

[[ -f "$build_dir/kiff-api" ]] || fail "kiff-api binary not found at $build_dir/kiff-api (use --skip-build only when the binaries already exist)" 3
[[ -f "$build_dir/kiff-web" ]] || fail "kiff-web binary not found at $build_dir/kiff-web" 3
[[ -f "$build_dir/kiff-gateway" ]] || fail "kiff-gateway binary not found at $build_dir/kiff-gateway" 3

# Each binary is built with its module's pinned toolchain. GOTOOLCHAIN=local
# on an older Go would otherwise ship its unpatched standard library.
for app in api web gateway; do
	want=$(awk '$1 == "toolchain" { print $2 }' "$REPO_ROOT/$app/go.mod")
	got=$(go version "$build_dir/kiff-$app" | awk '{ print $2 }')
	[[ -z "$want" || "$got" == "$want" ]] \
		|| fail "kiff-$app was built with $got; $app/go.mod pins $want (unset GOTOOLCHAIN=local and rebuild)" 3
done

api_sha="$(sha256_cmd "$build_dir/kiff-api")"
web_sha="$(sha256_cmd "$build_dir/kiff-web")"
gateway_sha="$(sha256_cmd "$build_dir/kiff-gateway")"
log "kiff-api sha256=${api_sha}"
log "kiff-web sha256=${web_sha}"
log "kiff-gateway sha256=${gateway_sha}"

# ─── upload binaries + supporting files to S3 ──────────────────
s3_prefix="s3://${STAGING_BUCKET}/${git_sha}"
log "uploading to ${s3_prefix}/"

aws --region "$AWS_REGION" s3 cp "$build_dir/kiff-api" "$s3_prefix/kiff-api" --only-show-errors \
	|| fail "s3 cp kiff-api failed" 4
aws --region "$AWS_REGION" s3 cp "$build_dir/kiff-web" "$s3_prefix/kiff-web" --only-show-errors \
	|| fail "s3 cp kiff-web failed" 4
aws --region "$AWS_REGION" s3 cp "$build_dir/kiff-gateway" "$s3_prefix/kiff-gateway" --only-show-errors \
	|| fail "s3 cp kiff-gateway failed" 4
aws --region "$AWS_REGION" s3 cp "$REPO_ROOT/scripts/systemd/kiff-gateway@.service" "$s3_prefix/kiff-gateway@.service" --only-show-errors \
	|| fail "s3 cp kiff-gateway@.service failed" 4
# The Caddyfile with its contact address filled in: the box compares it with
# the one it runs and reloads Caddy only when they differ.
sed "s|__KIFF_ACME_EMAIL__|${KIFF_ACME_EMAIL}|" "$REPO_ROOT/scripts/caddy/Caddyfile" > "$build_dir/Caddyfile"
grep -q __KIFF_ACME_EMAIL__ "$build_dir/Caddyfile" && fail "the Caddyfile's contact address wasn't filled in" 4
aws --region "$AWS_REGION" s3 cp "$build_dir/Caddyfile" "$s3_prefix/Caddyfile" --only-show-errors \
	|| fail "s3 cp Caddyfile failed" 4
aws --region "$AWS_REGION" s3 cp "$REPO_ROOT/scripts/cloudwatch-agent.json" "$s3_prefix/cloudwatch-agent.json" --only-show-errors \
	|| fail "s3 cp cloudwatch-agent.json failed" 4
aws --region "$AWS_REGION" s3 cp "$REPO_ROOT/scripts/logrotate-kiff" "$s3_prefix/logrotate-kiff" --only-show-errors \
	|| fail "s3 cp logrotate-kiff failed" 4
aws --region "$AWS_REGION" s3 cp "$REPO_ROOT/scripts/systemd/kiff-api@.service" "$s3_prefix/kiff-api@.service" --only-show-errors \
	|| fail "s3 cp kiff-api@.service failed" 4
aws --region "$AWS_REGION" s3 cp "$REPO_ROOT/scripts/systemd/kiff-web@.service" "$s3_prefix/kiff-web@.service" --only-show-errors \
	|| fail "s3 cp kiff-web@.service failed" 4
aws --region "$AWS_REGION" s3 cp "$REPO_ROOT/scripts/healthz-probe.sh" "$s3_prefix/healthz-probe.sh" --only-show-errors \
	|| fail "s3 cp healthz-probe.sh failed" 4
aws --region "$AWS_REGION" s3 cp "$REPO_ROOT/scripts/swap-slot.sh" "$s3_prefix/swap-slot.sh" --only-show-errors \
	|| fail "s3 cp swap-slot.sh failed" 4
aws --region "$AWS_REGION" s3 cp "$REPO_ROOT/scripts/systemd/kiff-healthz.service" "$s3_prefix/kiff-healthz.service" --only-show-errors \
	|| fail "s3 cp kiff-healthz.service failed" 4
aws --region "$AWS_REGION" s3 cp "$REPO_ROOT/scripts/systemd/kiff-healthz.timer" "$s3_prefix/kiff-healthz.timer" --only-show-errors \
	|| fail "s3 cp kiff-healthz.timer failed" 4
aws --region "$AWS_REGION" s3 cp "$REPO_ROOT/scripts/refresh-db-credentials.sh" "$s3_prefix/refresh-db-credentials.sh" --only-show-errors \
	|| fail "s3 cp refresh-db-credentials.sh failed" 4
aws --region "$AWS_REGION" s3 cp "$REPO_ROOT/scripts/systemd/kiff-db-credential-refresh.service" "$s3_prefix/kiff-db-credential-refresh.service" --only-show-errors \
	|| fail "s3 cp kiff-db-credential-refresh.service failed" 4
aws --region "$AWS_REGION" s3 cp "$REPO_ROOT/scripts/systemd/kiff-db-credential-refresh.timer" "$s3_prefix/kiff-db-credential-refresh.timer" --only-show-errors \
	|| fail "s3 cp kiff-db-credential-refresh.timer failed" 4
aws --region "$AWS_REGION" s3 cp "$REPO_ROOT/scripts/rds/eu-west-1-bundle.pem" "$s3_prefix/rds-ca.pem" --only-show-errors \
	|| fail "s3 cp rds-ca.pem failed" 4

# ─── render the remote install script ──────────────────────────
remote_install=$(cat <<EOF
#!/usr/bin/env bash
set -euo pipefail

GIT_SHA="${git_sha}"
S3_PREFIX="${s3_prefix}"
EXPECTED_API_SHA="${api_sha}"
EXPECTED_WEB_SHA="${web_sha}"
EXPECTED_GATEWAY_SHA="${gateway_sha}"
GATEWAY_KEYS_SECRET="${GATEWAY_KEYS_SECRET}"
PERMIT_KEY_SECRET="${PERMIT_KEY_SECRET}"
PERMIT_PUBLISHED_SECRET="${PERMIT_PUBLISHED_SECRET}"
PLANQUE_EVENT_SECRET_ARN="${PLANQUE_EVENT_SECRET_ARN}"
PROOF_STATE_TOKENS_ARN="${PROOF_STATE_TOKENS_ARN}"
PROOF_GITHUB_TOKEN_ARN="${PROOF_GITHUB_TOKEN_ARN}"
DB_SECRET_ARN="${DB_SECRET_ARN}"
KIFF_SECRET_ARN="${KIFF_SECRET_ARN}"
CLERK_SECRET_ARN="${CLERK_SECRET_ARN}"
KIFF_CLERK_PUBLISHABLE_KEY="${KIFF_CLERK_PUBLISHABLE_KEY}"
SESSION_SECRET_ARN="${SESSION_SECRET_ARN}"
ANCHOR_WALLET_ARN="${ANCHOR_WALLET_ARN}"
WEB_DEMO_KEY_ARN="${WEB_DEMO_KEY_ARN}"
RESEND_SECRET_ARN="${RESEND_SECRET_ARN}"
STRIPE_WEBHOOK_SECRET_ARN="${STRIPE_WEBHOOK_SECRET_ARN}"
STRIPE_SECRET_KEY_ARN="${STRIPE_SECRET_KEY_ARN}"
CLERK_WEBHOOK_SECRET_ARN="${CLERK_WEBHOOK_SECRET_ARN}"
KIFF_PRICING_BUSINESS_INTAKE_URL="${KIFF_PRICING_BUSINESS_INTAKE_URL}"
DB_HOST="${DB_HOST}"
DB_NAME="${DB_NAME}"
KIFF_AUTH_PROVIDER="${KIFF_AUTH_PROVIDER}"
KIFF_DEMO_TENANT_ID="${KIFF_DEMO_TENANT_ID}"
KIFF_STUB_SUBJECTS="${KIFF_STUB_SUBJECTS}"
KIFF_LOG_FORMAT="${KIFF_LOG_FORMAT}"
KIFF_CLERK_FRONTEND_API="${KIFF_CLERK_FRONTEND_API}"
KIFF_COOKIE_DOMAIN="${KIFF_COOKIE_DOMAIN}"
KIFF_APP_HOST="${KIFF_APP_HOST}"
KIFF_RECEIPT_MODE="${KIFF_RECEIPT_MODE}"
KIFF_ANCHOR_RPC_URL="${KIFF_ANCHOR_RPC_URL}"
KIFF_ANCHOR_CONTRACT="${KIFF_ANCHOR_CONTRACT}"
KIFF_ANCHOR_INTERVAL="${KIFF_ANCHOR_INTERVAL}"
KIFF_OPS_SUBJECTS="${KIFF_OPS_SUBJECTS}"
KIFF_PLANQUE_URLS="${KIFF_PLANQUE_URLS}"
KIFF_APPLE_APP_IDS="${KIFF_APPLE_APP_IDS}"
KIFF_ANDROID_APPS="${KIFF_ANDROID_APPS}"
KIFF_APPS_PROVED="${KIFF_APPS_PROVED}"
KIFF_DESKTOP_RELEASES="${KIFF_DESKTOP_RELEASES}"
KIFF_DESKTOP_NOT_AVAILABLE="${KIFF_DESKTOP_NOT_AVAILABLE}"
KIFF_PROOF_OIDC_AUDIENCE="${KIFF_PROOF_OIDC_AUDIENCE}"
KIFF_PROOF_OIDC_REPOSITORY="${KIFF_PROOF_OIDC_REPOSITORY}"
KIFF_PROOF_OIDC_REPOSITORY_ID="${KIFF_PROOF_OIDC_REPOSITORY_ID}"
KIFF_PROOF_OIDC_OWNER_ID="${KIFF_PROOF_OIDC_OWNER_ID}"
KIFF_PROOF_OIDC_REF="${KIFF_PROOF_OIDC_REF}"
KIFF_PROOF_OIDC_WORKFLOW="${KIFF_PROOF_OIDC_WORKFLOW}"
KIFF_PROOF_TEST_TENANTS="${KIFF_PROOF_TEST_TENANTS}"
KIFF_PROOF_TEST_KEYS="${KIFF_PROOF_TEST_KEYS}"
KIFF_PROOF_RUNNER_VERSIONS="${KIFF_PROOF_RUNNER_VERSIONS}"
KIFF_PROOF_RELEASE_ANYWAY="${KIFF_PROOF_RELEASE_ANYWAY}"
KIFF_PRUNE_TENANTS="${KIFF_PRUNE_TENANTS}"
KIFF_PRUNE_AFTER="${KIFF_PRUNE_AFTER}"
KIFF_NOTIFY_FROM="${KIFF_NOTIFY_FROM}"
KIFF_NOTIFY_REPLY_TO="${KIFF_NOTIFY_REPLY_TO}"
KIFF_PHONE_ALERT_URLS="${KIFF_PHONE_ALERT_URLS}"
KIFF_APPLE_SANDBOX_TENANTS="${KIFF_APPLE_SANDBOX_TENANTS}"
GOOGLE_PLAY_SERVICE_ACCOUNT_SECRET="${GOOGLE_PLAY_SERVICE_ACCOUNT_SECRET}"
KIFF_GOOGLE_PACKAGE_NAME="${KIFF_GOOGLE_PACKAGE_NAME}"
KIFF_GOOGLE_PUSH_AUDIENCE="${KIFF_GOOGLE_PUSH_AUDIENCE}"
KIFF_GOOGLE_PUSH_SERVICE_ACCOUNT="${KIFF_GOOGLE_PUSH_SERVICE_ACCOUNT}"
KIFF_GOOGLE_TEST_TENANTS="${KIFF_GOOGLE_TEST_TENANTS}"
KIFF_APP_SIGNIN="${KIFF_APP_SIGNIN}"
KIFF_APP_SIGNIN_TENANTS="${KIFF_APP_SIGNIN_TENANTS}"
AWS_REGION="${AWS_REGION}"

log() { printf '[remote] %s\n' "\$*"; }

# 1. Download artifacts from S3.
install -d -m 0755 -o root -g root /opt/kiff/bin /etc/kiff
install -d -m 0755 -o caddy -g caddy /var/lib/caddy/logs
# Reset ownership on any pre-existing log files so caddy:caddy can
# reopen them (a prior failed run may have left them root-owned).
chown caddy:caddy /var/lib/caddy/logs/*.log 2>/dev/null || true
install -d -m 0755 -o root -g root /var/log/kiff
tmp="\$(mktemp -d)"
db_refresh_paused=0
cleanup() {
  deploy_status=\$?
  if [[ \$db_refresh_paused -eq 1 ]]; then
    if ! systemctl restart kiff-db-credential-refresh.timer; then
      log "failed to restore database credential-refresh timer"
      [[ \$deploy_status -eq 0 ]] && deploy_status=16
    fi
  fi
  rm -rf "\$tmp"
  exit "\$deploy_status"
}
trap cleanup EXIT
for f in kiff-api kiff-web kiff-gateway kiff-gateway@.service Caddyfile cloudwatch-agent.json logrotate-kiff kiff-api@.service kiff-web@.service healthz-probe.sh swap-slot.sh kiff-healthz.service kiff-healthz.timer refresh-db-credentials.sh kiff-db-credential-refresh.service kiff-db-credential-refresh.timer rds-ca.pem; do
  log "fetching \$f"
  aws --region "\$AWS_REGION" s3 cp "\$S3_PREFIX/\$f" "\$tmp/\$f" --only-show-errors
done

# 2. Verify checksums match the laptop's build.
api_actual=\$(sha256sum "\$tmp/kiff-api" | awk '{print \$1}')
web_actual=\$(sha256sum "\$tmp/kiff-web" | awk '{print \$1}')
[[ "\$api_actual" == "\$EXPECTED_API_SHA" ]] || { log "kiff-api sha mismatch (got \$api_actual want \$EXPECTED_API_SHA)"; exit 11; }
[[ "\$web_actual" == "\$EXPECTED_WEB_SHA" ]] || { log "kiff-web sha mismatch (got \$web_actual want \$EXPECTED_WEB_SHA)"; exit 12; }
gateway_actual=\$(sha256sum "\$tmp/kiff-gateway" | awk '{print \$1}')
[[ "\$gateway_actual" == "\$EXPECTED_GATEWAY_SHA" ]] || { log "kiff-gateway sha mismatch (got \$gateway_actual want \$EXPECTED_GATEWAY_SHA)"; exit 14; }

# 3. Install binaries + caddyfile + units.
# The API executable is installed only after the gateway readiness gate.
install -m 0755 -o root -g root "\$tmp/kiff-web" /opt/kiff/bin/kiff-web
install -m 0755 -o root -g root "\$tmp/kiff-gateway" /opt/kiff/bin/kiff-gateway
install -m 0644 -o root -g root "\$tmp/kiff-gateway@.service" /etc/systemd/system/kiff-gateway@.service
install -m 0755 -o root -g root "\$tmp/healthz-probe.sh" /opt/kiff/bin/healthz-probe.sh
install -m 0755 -o root -g root "\$tmp/refresh-db-credentials.sh" /opt/kiff/bin/refresh-db-credentials.sh
# Amazon's RDS CA bundle for the region: every database URL below
# uses verify-full, so the services check the database's certificate and
# host name against it. It's public, so 0644: the units' dynamic users
# read it.
install -m 0644 -o root -g root "\$tmp/rds-ca.pem" /etc/kiff/rds-ca.pem
install -m 0755 -o root -g root "\$tmp/swap-slot.sh" /opt/kiff/bin/swap-slot.sh
install -m 0644 -o root -g root "\$tmp/kiff-api@.service" /etc/systemd/system/kiff-api@.service
# The units log to /var/log/kiff, rotated here.
install -m 0644 -o root -g root "\$tmp/logrotate-kiff" /etc/logrotate.d/kiff
install -m 0644 -o root -g root "\$tmp/kiff-web@.service" /etc/systemd/system/kiff-web@.service
install -m 0644 -o root -g root "\$tmp/kiff-healthz.service" /etc/systemd/system/kiff-healthz.service
install -m 0644 -o root -g root "\$tmp/kiff-healthz.timer"   /etc/systemd/system/kiff-healthz.timer
install -m 0644 -o root -g root "\$tmp/kiff-db-credential-refresh.service" /etc/systemd/system/kiff-db-credential-refresh.service
install -m 0644 -o root -g root "\$tmp/kiff-db-credential-refresh.timer" /etc/systemd/system/kiff-db-credential-refresh.timer

# 3b. The IP-to-country table the region checks read (api's and web's internal/region):
# DB-IP's free country file (CC BY 4.0, attributed on the privacy page), one a
# month. A month already here isn't fetched again. A fetch that fails, a file
# that doesn't look whole, or one whose size moves more than a tenth from the
# last good one (the file has no checksum) keeps the last good one; with none
# at all the deploy stops, since neither the API nor the web starts without it.
install -d -m 0755 -o root -g root /opt/kiff/geo
geo_month=\$(date -u +%Y-%m)
geo_file=/opt/kiff/geo/country-\$geo_month.csv.gz
if [ ! -s "\$geo_file" ]; then
	geo_last=\$(ls -1 /opt/kiff/geo/country-*.csv.gz 2>/dev/null | sort | tail -1 || true)
	geo_lines=0
	if curl -fsSL --max-time 120 -o "\$tmp/country.csv.gz" "https://download.db-ip.com/free/dbip-country-lite-\$geo_month.csv.gz" \
		&& gzip -t "\$tmp/country.csv.gz"; then
		geo_lines=\$(gzip -dc "\$tmp/country.csv.gz" | wc -l | tr -d " ")
	fi
	geo_prev=0
	[ -z "\$geo_last" ] || geo_prev=\$(gzip -dc "\$geo_last" | wc -l | tr -d " ")
	if [ "\$geo_lines" -lt 100000 ]; then
		log "region table: \$geo_month couldn't be fetched whole; keeping the last one"
	elif [ "\$geo_prev" -gt 0 ] && [ \$(( (geo_lines > geo_prev ? geo_lines - geo_prev : geo_prev - geo_lines) * 10 )) -gt "\$geo_prev" ]; then
		log "region table: \$geo_month has \$geo_lines ranges, last good \$geo_prev; more than a tenth apart, keeping the last one"
	else
		install -m 0644 -o root -g root "\$tmp/country.csv.gz" "\$geo_file"
		log "region table: \$geo_month fetched (\$geo_lines ranges)"
	fi
fi
geo_current=\$(ls -1 /opt/kiff/geo/country-*.csv.gz 2>/dev/null | sort | tail -1 || true)
[ -n "\$geo_current" ] || { log "region table: none on the box; the API and the web need one"; exit 5; }
ln -sfn "\$geo_current" /opt/kiff/geo/country.csv.gz
# Keep the last three months.
ls -1 /opt/kiff/geo/country-*.csv.gz | sort | head -n -3 | xargs -r rm -f

# 4. Resolve secrets from Secrets Manager (server-side, never logged).
# A secret that may be unset (the permit keys, Resend, Stripe): only a secret
# that does not exist means "unset", and the box's log says so. Any other
# error (throttling, a missing IAM grant) stops the deploy, so a feature is
# never turned off by a read that failed.
optional_secret() {
	local out
	if out=\$(aws --region "\$AWS_REGION" secretsmanager get-secret-value --secret-id "\$1" --query SecretString --output text 2>&1); then
		printf '%s' "\$out"
	elif printf '%s' "\$out" | grep -q ResourceNotFoundException; then
		echo "secret \$1 does not exist; deploying with it unset" >&2
		printf ''
	else
		echo "secret \$1 could not be read; refusing to deploy with it unset" >&2
		return 1
	fi
}
# KIFF's one secret (KIFF_SECRET_ARN), read once. secret_value NAME ARN MODE gives NAME from it when
# it's set; else the secret at ARN, as before (MODE "required" reads it plainly, "optional" through
# optional_secret). From the one secret, a required NAME it lacks stops the deploy, and an optional
# one it lacks is off.
kiff_bundle=""
if [ -n "\$KIFF_SECRET_ARN" ]; then
	kiff_bundle=\$(aws --region "\$AWS_REGION" secretsmanager get-secret-value --secret-id "\$KIFF_SECRET_ARN" --query SecretString --output text) || {
		echo "KIFF's one secret could not be read; refusing to deploy" >&2; exit 6; }
	printf '%s' "\$kiff_bundle" | jq -e 'type == "object"' > /dev/null || {
		echo "KIFF's one secret isn't a JSON object; refusing to deploy" >&2; exit 6; }
fi
secret_value() {
	if [ -n "\$kiff_bundle" ]; then
		if printf '%s' "\$kiff_bundle" | jq -e --arg n "\$1" 'has(\$n)' > /dev/null; then
			printf '%s' "\$kiff_bundle" | jq -j --arg n "\$1" '.[\$n]'
		elif [ "\$3" = optional ]; then
			echo "KIFF's one secret has no \$1; deploying with it unset" >&2
		else
			echo "KIFF's one secret has no \$1; refusing to deploy" >&2
			return 1
		fi
	elif [ "\$3" = optional ]; then
		optional_secret "\$2"
	else
		aws --region "\$AWS_REGION" secretsmanager get-secret-value --secret-id "\$2" --query SecretString --output text
	fi
}
# RDS-generated passwords contain URL-unsafe characters. Percent-encode the
# password through jq stdin so it is safe in both systemd's EnvironmentFile
# and pgx, without exposing the credential in process arguments.
db_password=\$(aws --region "\$AWS_REGION" secretsmanager get-secret-value --secret-id "\$DB_SECRET_ARN" --query SecretString --output text | jq -r .password)
db_password_uri=\$(printf '%s' "\$db_password" | jq -Rr '@uri')
# TLS for every database URL: the certificate and host name are checked
# against Amazon's RDS CA bundle installed above.
db_tls="sslmode=verify-full&sslrootcert=/etc/kiff/rds-ca.pem"
clerk_secret=\$(secret_value clerk_secret_key "\$CLERK_SECRET_ARN" required)
clerk_publishable="\$KIFF_CLERK_PUBLISHABLE_KEY"
session_secret=\$(secret_value session_secret "\$SESSION_SECRET_ARN" required)
anchor_private_key=\$(secret_value anchor_private_key "\$ANCHOR_WALLET_ARN" required)
web_demo_key=\$(secret_value web_demo_key "\$WEB_DEMO_KEY_ARN" required)
resend_key=\$(secret_value resend_api_key "\$RESEND_SECRET_ARN" optional)
stripe_webhook_secret=\$(secret_value stripe_webhook_secret "\$STRIPE_WEBHOOK_SECRET_ARN" optional)
stripe_secret_key=\$(secret_value stripe_secret_key "\$STRIPE_SECRET_KEY_ARN" optional)
clerk_webhook_secret=""
if [ -n "\$CLERK_WEBHOOK_SECRET_ARN" ] || [ -n "\$kiff_bundle" ]; then clerk_webhook_secret=\$(secret_value clerk_webhook_secret "\$CLERK_WEBHOOK_SECRET_ARN" optional); fi
pricing_business_intake_url="\$KIFF_PRICING_BUSINESS_INTAKE_URL"
gateway_keys=\$(secret_value gateway_keys "\$GATEWAY_KEYS_SECRET" required)
permit_key=""
if [ -n "\$PERMIT_KEY_SECRET" ] || [ -n "\$kiff_bundle" ]; then permit_key=\$(secret_value permit_key "\$PERMIT_KEY_SECRET" optional); fi
permit_published=""
if [ -n "\$PERMIT_PUBLISHED_SECRET" ] || [ -n "\$kiff_bundle" ]; then permit_published=\$(secret_value permit_published "\$PERMIT_PUBLISHED_SECRET" optional); fi
planque_event_secret=""
if [ -n "\$PLANQUE_EVENT_SECRET_ARN" ] || [ -n "\$kiff_bundle" ]; then
	planque_event_secret=\$(secret_value planque_event_secret "\$PLANQUE_EVENT_SECRET_ARN" optional)
fi
proof_state_tokens=""
if [ -n "\$PROOF_STATE_TOKENS_ARN" ] || [ -n "\$kiff_bundle" ]; then
	proof_state_tokens=\$(secret_value proof_state_tokens "\$PROOF_STATE_TOKENS_ARN" optional)
fi
proof_github_token=""
if [ -n "\$PROOF_GITHUB_TOKEN_ARN" ] || [ -n "\$kiff_bundle" ]; then
	proof_github_token=\$(secret_value proof_github_token "\$PROOF_GITHUB_TOKEN_ARN" optional)
fi
# The JSON key is multi-line: one base64 line in the env file. Read only
# once Google Play is configured (its package name set): until then there is
# no secret, and the EC2 role may not even be allowed to read its name.
google_play_sa_b64=""
if [ -n "\$KIFF_GOOGLE_PACKAGE_NAME" ] && { [ -n "\$GOOGLE_PLAY_SERVICE_ACCOUNT_SECRET" ] || [ -n "\$kiff_bundle" ]; }; then
	google_play_sa=\$(secret_value google_play_service_account "\$GOOGLE_PLAY_SERVICE_ACCOUNT_SECRET" optional)
	google_play_sa_b64=\$(printf '%s' "\$google_play_sa" | base64 -w0)
fi

# 5. Write env files. Mode 0640 root-only.
umask 0137
cat > /etc/kiff/api.env <<API_ENV
KIFF_ENV=production
KIFF_DATABASE_URL=postgresql://kiff_admin:\${db_password_uri}@\${DB_HOST}:5432/\${DB_NAME}?\${db_tls}
KIFF_AUTH_PROVIDER=\${KIFF_AUTH_PROVIDER}
CLERK_SECRET_KEY=\${clerk_secret}
KIFF_DEMO_TENANT_ID=\${KIFF_DEMO_TENANT_ID}
KIFF_STUB_SUBJECTS=\${KIFF_STUB_SUBJECTS}
KIFF_LOG_FORMAT=\${KIFF_LOG_FORMAT}
KIFF_RECEIPT_MODE=\${KIFF_RECEIPT_MODE}
KIFF_ANCHOR_PRIVATE_KEY=\${anchor_private_key}
KIFF_ANCHOR_RPC_URL=\${KIFF_ANCHOR_RPC_URL}
KIFF_ANCHOR_CONTRACT=\${KIFF_ANCHOR_CONTRACT}
KIFF_ANCHOR_INTERVAL=\${KIFF_ANCHOR_INTERVAL}
KIFF_OPS_SUBJECTS=\${KIFF_OPS_SUBJECTS}
RESEND_API_KEY=\${resend_key}
KIFF_STRIPE_WEBHOOK_SECRET=\${stripe_webhook_secret}
KIFF_STRIPE_SECRET_KEY=\${stripe_secret_key}
KIFF_CLERK_WEBHOOK_SECRET=\${clerk_webhook_secret}
KIFF_NOTIFY_FROM=\${KIFF_NOTIFY_FROM}
KIFF_NOTIFY_REPLY_TO=\${KIFF_NOTIFY_REPLY_TO}
KIFF_PHONE_ALERT_URLS=\${KIFF_PHONE_ALERT_URLS}
KIFF_APPLE_SANDBOX_TENANTS=\${KIFF_APPLE_SANDBOX_TENANTS}
KIFF_GOOGLE_PACKAGE_NAME=\${KIFF_GOOGLE_PACKAGE_NAME}
KIFF_GOOGLE_SERVICE_ACCOUNT=\${google_play_sa_b64}
KIFF_GOOGLE_PUSH_AUDIENCE=\${KIFF_GOOGLE_PUSH_AUDIENCE}
KIFF_GOOGLE_PUSH_SERVICE_ACCOUNT=\${KIFF_GOOGLE_PUSH_SERVICE_ACCOUNT}
KIFF_GOOGLE_TEST_TENANTS=\${KIFF_GOOGLE_TEST_TENANTS}
KIFF_APP_SIGNIN=\${KIFF_APP_SIGNIN}
KIFF_APP_SIGNIN_TENANTS=\${KIFF_APP_SIGNIN_TENANTS}
KIFF_APP_BASE_URL=https://\${KIFF_APP_HOST}
KIFF_PERMIT_SIGNING_KEY=\${permit_key}
KIFF_PERMIT_PUBLISHED_KEYS=\${permit_published}
KIFF_PLANQUE_URLS=\${KIFF_PLANQUE_URLS}
KIFF_ANDROID_APPS=\${KIFF_ANDROID_APPS}
KIFF_PRUNE_TENANTS=\${KIFF_PRUNE_TENANTS}
KIFF_PRUNE_AFTER=\${KIFF_PRUNE_AFTER}
KIFF_PROOF_OIDC_AUDIENCE=\${KIFF_PROOF_OIDC_AUDIENCE}
KIFF_PROOF_OIDC_REPOSITORY=\${KIFF_PROOF_OIDC_REPOSITORY}
KIFF_PROOF_OIDC_REPOSITORY_ID=\${KIFF_PROOF_OIDC_REPOSITORY_ID}
KIFF_PROOF_OIDC_OWNER_ID=\${KIFF_PROOF_OIDC_OWNER_ID}
KIFF_PROOF_OIDC_REF=\${KIFF_PROOF_OIDC_REF}
KIFF_PROOF_OIDC_WORKFLOW=\${KIFF_PROOF_OIDC_WORKFLOW}
KIFF_PROOF_TEST_TENANTS=\${KIFF_PROOF_TEST_TENANTS}
KIFF_PROOF_TEST_KEYS=\${KIFF_PROOF_TEST_KEYS}
KIFF_PROOF_RUNNER_VERSIONS=\${KIFF_PROOF_RUNNER_VERSIONS}
KIFF_PROOF_RELEASE_ANYWAY=\${KIFF_PROOF_RELEASE_ANYWAY}
KIFF_PROOF_STATE_TOKENS=\${proof_state_tokens}
KIFF_PROOF_GITHUB_TOKEN=\${proof_github_token}
KIFF_REGION_TABLE=/opt/kiff/geo/country.csv.gz
API_ENV
chmod 0640 /etc/kiff/api.env
chown root:root /etc/kiff/api.env

cat > /etc/kiff/db-refresh.env <<DB_REFRESH_ENV
AWS_REGION=\${AWS_REGION}
DB_SECRET_ARN=\${DB_SECRET_ARN}
DB_HOST=\${DB_HOST}
DB_NAME=\${DB_NAME}
DB_REFRESH_ENV
chmod 0640 /etc/kiff/db-refresh.env
chown root:root /etc/kiff/db-refresh.env

cat > /etc/kiff/web.env <<WEB_ENV
KIFF_ENV=production
KIFF_API_BASE_URL=http://127.0.0.1:9080
KIFF_GATEWAY_BASE_URL=http://127.0.0.1:9282
KIFF_DEMO_API_KEY=\${web_demo_key}
KIFF_AUTH_PROVIDER=\${KIFF_AUTH_PROVIDER}
KIFF_SESSION_SECRET=\${session_secret}
CLERK_SECRET_KEY=\${clerk_secret}
KIFF_CLERK_PUBLISHABLE_KEY=\${clerk_publishable}
KIFF_CLERK_FRONTEND_API=\${KIFF_CLERK_FRONTEND_API}
KIFF_COOKIE_SECURE=true
KIFF_COOKIE_DOMAIN=\${KIFF_COOKIE_DOMAIN}
KIFF_APP_HOST=\${KIFF_APP_HOST}
KIFF_PRICING_BUSINESS_INTAKE_URL=\${pricing_business_intake_url}
KIFF_OPS_SUBJECTS=\${KIFF_OPS_SUBJECTS}
KIFF_APPLE_APP_IDS=\${KIFF_APPLE_APP_IDS}
KIFF_ANDROID_APPS=\${KIFF_ANDROID_APPS}
KIFF_APPS_PROVED=\${KIFF_APPS_PROVED}
KIFF_DESKTOP_RELEASES=\${KIFF_DESKTOP_RELEASES}
KIFF_DESKTOP_NOT_AVAILABLE=\${KIFF_DESKTOP_NOT_AVAILABLE}
KIFF_APP_SIGNIN=\${KIFF_APP_SIGNIN}
KIFF_REGION_TABLE=/opt/kiff/geo/country.csv.gz
WEB_ENV
chmod 0640 /etc/kiff/web.env
chown root:root /etc/kiff/web.env

# The gateway connects as its own role, which owns only the gateway
# schema and cannot read cloud_meta or tenant schemas. Each deploy sets a
# fresh password; it lives only in gateway.env, so there is no secret to
# store or rotate. The running gateway keeps its open connections until
# the restart below.
gateway_db_password=\$(od -An -tx1 -N32 /dev/urandom | tr -d " \\n")
KIFF_ADMIN_DATABASE_URL="postgresql://kiff_admin:\${db_password_uri}@\${DB_HOST}:5432/\${DB_NAME}?\${db_tls}" \\
	KIFF_GATEWAY_DB_PASSWORD="\$gateway_db_password" /opt/kiff/bin/kiff-gateway db-role

# The gateway's keys go only here: 0600, read by systemd before the
# unit drops to its dynamic user.
umask 0177
cat > /etc/kiff/gateway.env <<GATEWAY_ENV
KIFF_DATABASE_URL=postgresql://kiff_gateway:\${gateway_db_password}@\${DB_HOST}:5432/\${DB_NAME}?\${db_tls}
KIFF_GATEWAY_CREDENTIAL_KEYS=\${gateway_keys}
KIFF_API_URL=http://127.0.0.1:9080
KIFF_APP_BASE_URL=https://\${KIFF_APP_HOST}
KIFF_LOG_FORMAT=\${KIFF_LOG_FORMAT}
KIFF_PLANQUE_URLS=\${KIFF_PLANQUE_URLS}
KIFF_PLANQUE_EVENT_SECRET=\${planque_event_secret}
GATEWAY_ENV
chmod 0600 /etc/kiff/gateway.env
chown root:root /etc/kiff/gateway.env
umask 0022

# 6. Install + start CloudWatch agent with the canonical config.
install -d -m 0755 /opt/aws/amazon-cloudwatch-agent/etc
install -m 0644 -o root -g root "\$tmp/cloudwatch-agent.json" /opt/aws/amazon-cloudwatch-agent/etc/amazon-cloudwatch-agent.json
/opt/aws/amazon-cloudwatch-agent/bin/amazon-cloudwatch-agent-ctl \
  -a fetch-config \
  -m ec2 \
  -s \
  -c file:/opt/aws/amazon-cloudwatch-agent/etc/amazon-cloudwatch-agent.json

# Service logs are kept 30 days at most (privacy#retention, the DPA's Annex 4).
# KIFF's own services log to /var/log/kiff (3 days, /etc/logrotate.d/kiff)
# and CloudWatch (7 days); journald keeps the rest, with no limit of its own
# otherwise.
install -d -m 0755 /etc/systemd/journald.conf.d
cat > /etc/systemd/journald.conf.d/kiff-retention.conf <<'JOURNALD'
[Journal]
MaxRetentionSec=30day
JOURNALD
systemctl restart systemd-journald
journalctl --vacuum-time=30d >/dev/null 2>&1 || true

# 7. Swap each service to its new binary without dropping requests. The
# new process starts in the idle slot and must answer /healthz, then the
# old one stops and drains its in-flight requests (scripts/swap-slot.sh).
# Caddy lists both slots and sends each request to the first that's up,
# so a swap never reloads it.
systemctl daemon-reload
systemctl enable caddy kiff-healthz.timer kiff-db-credential-refresh.timer
# Caddy's config changes only when the Caddyfile does: then it's validated
# and loaded once, before any swap, since its loopback addresses (9080 for
# the API, 9282 for the gateway) must answer before a new process calls
# them. A reload resets connections in flight, so it's skipped otherwise.
rm -f /etc/caddy/Caddyfile.tmpl
if ! cmp -s "\$tmp/Caddyfile" /etc/caddy/Caddyfile; then
  /usr/local/bin/caddy validate --config "\$tmp/Caddyfile" --adapter caddyfile >/dev/null
  # Validating as root opens the access logs, creating any missing one as
  # root, which the caddy user then can't open on reload (a fresh box).
  chown caddy:caddy /var/lib/caddy/logs/*.log 2>/dev/null || true
  install -m 0644 -o caddy -g caddy "\$tmp/Caddyfile" /etc/caddy/Caddyfile
  if systemctl is-active --quiet caddy; then
    /usr/local/bin/caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
  fi
  log "Caddyfile changed: loaded"
fi
systemctl is-active --quiet caddy || systemctl start caddy

# The gateway goes first: the API's accounting cutover needs a
# gateway that understands recoverable approved_no_room.
/opt/kiff/bin/swap-slot.sh gateway 8282 8292 kiff-gateway.service
log "gateway swapped"

# Only now can the API change slot. The API loads the accounting writer
# and installs the cutover when its new process starts. The credential
# refresh is paused for the swap.
if systemctl cat kiff-db-credential-refresh.timer >/dev/null 2>&1; then
  db_refresh_paused=1
  systemctl stop kiff-db-credential-refresh.timer kiff-db-credential-refresh.service
fi
install -m 0755 -o root -g root "\$tmp/kiff-api" /opt/kiff/bin/kiff-api
/opt/kiff/bin/swap-slot.sh api 8080 8090 kiff-api.service
/opt/kiff/bin/swap-slot.sh web 8081 8091 kiff-web.service

# Every service now runs in a slot, so the legacy single-instance units go.
rm -f /etc/systemd/system/kiff-api.service /etc/systemd/system/kiff-web.service /etc/systemd/system/kiff-gateway.service
systemctl daemon-reload
systemctl restart kiff-healthz.timer
systemctl restart kiff-db-credential-refresh.timer
db_refresh_paused=0

# 8. Smoke (loopback) on the slots that now serve. Allow up to 30 s.
api_port=\$(cat /etc/kiff/slot-api)
web_port=\$(cat /etc/kiff/slot-web)
gateway_port=\$(cat /etc/kiff/slot-gateway)
for i in 1 2 3 4 5 6 7 8 9 10; do
  if curl -sf http://127.0.0.1:\$api_port/healthz >/dev/null && curl -sf http://127.0.0.1:\$web_port/healthz >/dev/null && curl -sf http://127.0.0.1:\$gateway_port/healthz >/dev/null; then
    log "loopback smoke ok"
    break
  fi
  sleep 3
  [[ \$i -eq 10 ]] && { log "loopback smoke failed after 30s"; systemctl status "kiff-api@\$api_port" --no-pager | tail -20; systemctl status "kiff-web@\$web_port" --no-pager | tail -20; systemctl status "kiff-gateway@\$gateway_port" --no-pager | tail -20; exit 13; }
done

# 9. Status summary.
log "deployed git_sha=\$GIT_SHA"
log "kiff-api: slot \$api_port, \$(systemctl is-active "kiff-api@\$api_port")"
log "kiff-web: slot \$web_port, \$(systemctl is-active "kiff-web@\$web_port")"
log "kiff-gateway: slot \$gateway_port, \$(systemctl is-active "kiff-gateway@\$gateway_port")"
log "caddy:    \$(systemctl is-active caddy)"
log "db refresh timer: \$(systemctl is-active kiff-db-credential-refresh.timer)"

# Persist the deployed sha for the runbook.
echo "\$GIT_SHA" > /etc/kiff/deployed-git-sha
EOF
)

# ─── send the SSM command ───────────────────────────────────────
if [[ $dry_run -eq 1 ]]; then
	log "dry-run: would send the following SSM AWS-RunShellScript:"
	printf '%s\n' "$remote_install"
	exit 0
fi

# ─── public watch ────────────────────────────────────────────────
# A deploy must not refuse a call. While the remote install runs, ask
# each vhost's public /healthz once a second and note every answer but
# 200. Caddy holds a request for up to 30s while its upstream restarts,
# so curl waits longer than that before it counts a request as failed.
watch_log="$build_dir/healthz-watch.log"
watch_hosts=(mcp.kiff.dev api.kiff.dev app.kiff.dev)
watch_pid=""
if [[ $skip_smoke -eq 0 ]]; then
	: > "$watch_log"
	(
		while :; do
			for h in "${watch_hosts[@]}"; do
				code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 40 "https://${h}/healthz" || true)
				[[ "$code" == "200" ]] || printf '%s %s HTTP %s\n' "$(date -u +%H:%M:%S)" "$h" "${code:-000}" >> "$watch_log"
			done
			sleep 1
		done
	) &
	watch_pid=$!
	trap '[[ -n "$watch_pid" ]] && kill "$watch_pid" 2>/dev/null' EXIT
fi

log "sending SSM RunCommand to ${INSTANCE_ID} (timeout ${SSM_TIMEOUT_SEC}s)"
ssm_params_file="$build_dir/ssm-params.json"
jq -n --arg script "$remote_install" '{commands: [$script]}' > "$ssm_params_file"

cmd_id=$(aws --region "$AWS_REGION" ssm send-command \
	--instance-ids "$INSTANCE_ID" \
	--document-name "AWS-RunShellScript" \
	--timeout-seconds "$SSM_TIMEOUT_SEC" \
	--parameters "file://$ssm_params_file" \
	--query 'Command.CommandId' --output text) \
	|| fail "ssm send-command failed" 4

log "ssm command id=${cmd_id}"

# Poll for completion.
deadline=$(( $(date +%s) + SSM_TIMEOUT_SEC ))
while true; do
	now=$(date +%s)
	[[ $now -gt $deadline ]] && fail "ssm command timed out" 7

	status=$(aws --region "$AWS_REGION" ssm get-command-invocation \
		--command-id "$cmd_id" --instance-id "$INSTANCE_ID" \
		--query 'Status' --output text 2>/dev/null || echo "Pending")

	case "$status" in
		Success)  break ;;
		Failed|Cancelled|TimedOut)
			log "ssm status=${status}"
			aws --region "$AWS_REGION" ssm get-command-invocation \
				--command-id "$cmd_id" --instance-id "$INSTANCE_ID" \
				--query '{stdout:StandardOutputContent,stderr:StandardErrorContent}' \
				--output json | head -200
			fail "remote install failed" 5
			;;
		*) sleep 5 ;;
	esac
done

# ─── stdout summary ─────────────────────────────────────────────
out=$(aws --region "$AWS_REGION" ssm get-command-invocation \
	--command-id "$cmd_id" --instance-id "$INSTANCE_ID" \
	--query 'StandardOutputContent' --output text)
echo "$out" | grep -E '^\[remote\]' || true

if [[ -n "$watch_pid" ]]; then
	kill "$watch_pid" 2>/dev/null || true
	wait "$watch_pid" 2>/dev/null || true
	watch_pid=""
	if [[ -s "$watch_log" ]]; then
		log "public /healthz answered other than 200 during the deploy:"
		sed 's/^/[deploy]   /' "$watch_log" >&2
		fail "the deploy refused calls" 8
	fi
	log "public /healthz: 200 on ${watch_hosts[*]} throughout the deploy"
fi

# ─── public smoke (optional) ────────────────────────────────────
if [[ $skip_smoke -eq 0 ]]; then
	# Probe the box's public IP on :80 to confirm the Caddy that
	# just came up answers on the port we expose. The vhost-aware
	# Caddyfile won't match a bare-IP request, so any status is
	# fine; exit 0 is the goal. The IP is read from the instance,
	# so a moved or re-addressed box never sends this probe to an
	# address KIFF no longer holds.
	eip=$(aws --region "$AWS_REGION" ec2 describe-instances --instance-ids "$INSTANCE_ID" \
		--query 'Reservations[0].Instances[0].PublicIpAddress' --output text 2>/dev/null || echo "")
	if [[ -n "$eip" && "$eip" != "None" ]]; then
		http_status=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "http://${eip}/" || echo "000")
		# The address itself stays out of the log: kiff/cloud's deploy logs are public.
		log "public probe of the box's own address → HTTP ${http_status} (informational; vhost-aware Caddyfile may not match bare-IP)"
	fi
fi

log "deploy complete: git_sha=${git_sha}"
log "next: 'aws ssm start-session --target ${INSTANCE_ID}' to inspect"
