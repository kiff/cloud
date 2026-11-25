// Package main runs the kiff-cloud API binary.
//
// The API is the multi-tenant runtime wrapper for github.com/kiff/kiff.
// Per the auth-boundary design it sits behind an authenticating middleware that reads
// either a Clerk session JWT or a kiff_live_<tenant>_<random32> API
// key on every request and produces (Identity, actor.Actor, tenant id)
// for downstream handlers.
//
// Run modes (selected by KIFF_AUTH_PROVIDER):
//
//	stub   — default, in-memory MapResolver seeded at startup. No
//	         Clerk credentials required. Suitable for tests and dev.
//	clerk  — verifies Clerk session JWTs. Requires CLERK_SECRET_KEY
//	         and KIFF_DEMO_TENANT_ID until the real subject→tenant
//	         mapping table lands. Fail-fast on missing secrets.
//
// Storage:
//
//	KIFF_DATABASE_URL set       — Postgres-backed stores; schemas
//	                              applied at startup (idempotent).
//	KIFF_DATABASE_URL unset     — In-memory stores. Useful for
//	                              local smoke tests; data is lost
//	                              when the process exits.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	clerksdk "github.com/clerk/clerk-sdk-go/v2"
	clerksession "github.com/clerk/clerk-sdk-go/v2/session"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiff/kiff/pkg/kiff/actor"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/agentdrafts"
	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/anchor"
	"github.com/kiff/cloud/api/internal/approvals"
	"github.com/kiff/cloud/api/internal/appsignin"
	"github.com/kiff/cloud/api/internal/appstore"
	"github.com/kiff/cloud/api/internal/attention"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/callabout"
	"github.com/kiff/cloud/api/internal/cardwarnings"
	"github.com/kiff/cloud/api/internal/codesignin"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/guard"
	"github.com/kiff/cloud/api/internal/halts"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/handoff"
	"github.com/kiff/cloud/api/internal/idempotency"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/newdevice"
	"github.com/kiff/cloud/api/internal/notify"
	"github.com/kiff/cloud/api/internal/oauth"
	"github.com/kiff/cloud/api/internal/operatoralerts"
	"github.com/kiff/cloud/api/internal/operatoraudit"
	"github.com/kiff/cloud/api/internal/owneremail"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/payreports"
	"github.com/kiff/cloud/api/internal/permit"
	"github.com/kiff/cloud/api/internal/personlang"
	"github.com/kiff/cloud/api/internal/playstore"
	"github.com/kiff/cloud/api/internal/posture"
	"github.com/kiff/cloud/api/internal/proofs"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/region"
	"github.com/kiff/cloud/api/internal/requestlog"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/settle"
	"github.com/kiff/cloud/api/internal/stream"
	"github.com/kiff/cloud/api/internal/support"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/tooloutcome"
	"github.com/kiff/cloud/api/internal/usage"
)

// Env-var names the binary reads. Centralized so the help banner
// and the .env.example stay in sync.
const (
	envAddr = "KIFF_API_ADDR"
	// envPlanqueURLs lists the planques that send relay calls on KIFF's
	// permits, comma separated, such as https://planque.kiff.dev: each
	// confirms a halt. Empty: none to confirm with.
	envPlanqueURLs = "KIFF_PLANQUE_URLS"
	envDatabaseURL = "KIFF_DATABASE_URL"
	envAuthProv    = "KIFF_AUTH_PROVIDER"
	envClerkSecret = "CLERK_SECRET_KEY"
	envDemoTenant  = "KIFF_DEMO_TENANT_ID"
	envStubSubs    = "KIFF_STUB_SUBJECTS"
	envLogFormat   = "KIFF_LOG_FORMAT"
	// envAllowPrivateWebhooks is the dev escape hatch for the alert
	// webhook SSRF guard. When true, alert targets may point
	// at private/loopback addresses (local + integration testing).
	// Unset/false in production so the guard blocks them at
	// registration and at delivery.
	envAllowPrivateWebhooks = "KIFF_ALLOW_PRIVATE_WEBHOOKS"
	// envPhoneAlertURLs is the comma-separated list of app backends a
	// phone's session may register as its account's alert target,
	// such as tafs cloud's. Empty: a phone registers none.
	envPhoneAlertURLs = "KIFF_PHONE_ALERT_URLS"
	// envAppBaseURL is the dashboard origin (e.g. https://app.kiff.dev)
	// used to absolutize the approval review link in outbound alert
	// payloads. Empty leaves the review URL relative.
	envAppBaseURL = "KIFF_APP_BASE_URL"

	// the gateway-OAuth design: the authorization server for the gateway. Enabled when
	// there is a database and KIFF_APP_BASE_URL (the consent page's
	// origin) is set. Defaults are production's.
	envOAuthIssuer = "KIFF_OAUTH_ISSUER" // default https://api.kiff.dev
	// envRegionTable is the IP-to-country table (api/internal/region) the
	// session-starting routes check the caller's country against. Production
	// requires it (deploy.sh fetches it monthly); unset elsewhere, nothing is
	// checked.
	envRegionTable = "KIFF_REGION_TABLE"
	// envAppSignIn turns on signing in inside the tafs apps: "on" for everyone,
	// "test" for the tenants in envAppSignInTenants only; anything else is off.
	envAppSignIn = "KIFF_APP_SIGNIN"
	// envAppSignInTenants is the comma-separated tenants "test" lets sign in
	// (a production secret: it names accounts). Empty refuses everyone.
	envAppSignInTenants = "KIFF_APP_SIGNIN_TENANTS"

	// envSignInCodeKey is the key of signing in with a code sent by email
	// (codesignin): 32 random bytes, hex or base64, used for nothing else.
	// Unset, codes are off: no one can turn them on.
	envSignInCodeKey = "KIFF_SIGNIN_CODE_KEY"
	// envAppTermsVersion is the tafs terms in force; a terms change sets it.
	envAppTermsVersion = "KIFF_APP_TERMS_VERSION"
	// envAndroidApps names the Android apps allowed to make and use kiff.dev's
	// passkeys, "<package>=<signing certificate SHA-256 as AA:BB:…>", comma
	// separated: each one's android:apk-key-hash origin is accepted beside
	// https://kiff.dev (the desktop client contract, §2.6). The web service
	// serves the same apps in /.well-known/assetlinks.json.
	envAndroidApps        = "KIFF_ANDROID_APPS"
	envOAuthResource      = "KIFF_OAUTH_RESOURCE"       // default https://mcp.kiff.dev/mcp
	envOAuthRedirectHosts = "KIFF_OAUTH_REDIRECT_HOSTS" // comma-separated https hosts
	envOAuthMetadataHosts = "KIFF_OAUTH_METADATA_HOSTS" // comma-separated hosts whose client metadata documents are accepted
	envOAuthSharedNets    = "KIFF_OAUTH_SHARED_NETS"    // comma-separated CIDRs given a larger registration budget

	// envStripeWebhookSecret is the Stripe webhook signing secret
	// (whsec_…) used to verify the Stripe-Signature on the
	// unauthenticated /webhooks/stripe endpoint. Empty leaves
	// the webhook route unmounted.
	envStripeWebhookSecret = "KIFF_STRIPE_WEBHOOK_SECRET"

	// envClerkWebhookSecret is the sign-in provider's webhook signing
	// secret (whsec_…), verified on the unauthenticated /webhooks/clerk
	// endpoint: its session.created events drive the "new device signed
	// in" email (newdevice). Empty leaves the route unmounted and sends
	// no such email.
	envClerkWebhookSecret = "KIFF_CLERK_WEBHOOK_SECRET"

	// envStripeSecretKey is the Stripe secret key (sk_live_…/sk_test_…)
	// used to mint Customer Portal sessions on the authenticated
	// /v1/me/billing-portal endpoint. Empty leaves that route
	// unmounted. Lives api-side only — the web tier never holds it.
	envStripeSecretKey = "KIFF_STRIPE_SECRET_KEY"

	// envAppleBundleID is the tafs app's bundle id: Apple's signed
	// payloads for any other app are refused. Default bot.tafs.app.
	envAppleBundleID = "KIFF_APPLE_BUNDLE_ID"
	// envAppleEnvironments is the comma-separated App Store environments
	// whose payloads are accepted. Default "Production,Sandbox": App
	// Review buys in the sandbox against the production server.
	envAppleEnvironments = "KIFF_APPLE_ENVIRONMENTS"
	// envAppleSandboxTenants is the comma-separated accounts, besides those
	// on the internal plan, whose Sandbox purchases count: App Review's.
	// Every other account's test purchases are refused.
	envAppleSandboxTenants = "KIFF_APPLE_SANDBOX_TENANTS"

	// Google Play Billing for the tafs Android app. Set up when the
	// package name and the service account are both set; otherwise its
	// routes answer that it isn't, and usage lists no Play products.
	//
	// envGooglePackageName is the app's package name on Google Play.
	envGooglePackageName = "KIFF_GOOGLE_PACKAGE_NAME"
	// envGoogleServiceAccount is the JSON key (raw, or base64 of it) of the
	// service account KIFF reads purchases and acknowledges Solo with,
	// invited in the Play Console. A secret: the deploy reads it from Secrets
	// Manager, by the name in GOOGLE_PLAY_SERVICE_ACCOUNT_SECRET.
	envGoogleServiceAccount = "KIFF_GOOGLE_SERVICE_ACCOUNT"
	// envGooglePushAudience and envGooglePushServiceAccount are the Pub/Sub
	// push subscription's OIDC audience and the service account it signs
	// its tokens as: a push carrying any other token is refused. Both are
	// needed for /webhooks/google.
	envGooglePushAudience       = "KIFF_GOOGLE_PUSH_AUDIENCE"
	envGooglePushServiceAccount = "KIFF_GOOGLE_PUSH_SERVICE_ACCOUNT"
	// envGoogleTestTenants is the comma-separated accounts, besides those
	// on the internal plan, whose licence testers' purchases count: the
	// Play review account's. Every other account's test purchases are
	// refused.
	envGoogleTestTenants = "KIFF_GOOGLE_TEST_TENANTS"

	// envReceiptMode selects the receipt pipeline operating mode.
	// Three values:
	//
	//   disabled — the pipeline is not constructed. The cloud
	//              executes actions and serves the runtime surface
	//              normally; receipts are not built or persisted.
	//              Backward-compatible with deploys that pre-date
	//              the receipt pipeline.
	//
	//   local    — receipts are built and signed in-process via
	//              LocalAttester. The on-chain anchor fields stay
	//              empty. No RPC dependency. Default when a
	//              receipt store is wired and the operator has
	//              not explicitly set the mode.
	//
	//   anchor   — local-mode behaviour plus on-chain anchoring
	//              against the configured KiffAnchor contract.
	//              Requires KIFF_ANCHOR_{PRIVATE_KEY,RPC_URL,CONTRACT}
	//              to be set; the binary fails-fast at startup
	//              when they are missing.
	envReceiptMode = "KIFF_RECEIPT_MODE"

	// envAnchorPrivateKey is the hex-encoded secp256k1 private key
	// the cloud's master service wallet uses for receipt
	// attestation. Required when envReceiptMode=anchor.
	envAnchorPrivateKey = "KIFF_ANCHOR_PRIVATE_KEY"
	envAnchorRPCURL     = "KIFF_ANCHOR_RPC_URL"
	envAnchorContract   = "KIFF_ANCHOR_CONTRACT"

	// Mainnet anchoring. When all three are set, a second
	// attester is built and paid (mainnet-entitled) tenants route to
	// it; free tenants stay on the Sepolia path above. The private
	// key is optional and falls back to KIFF_ANCHOR_PRIVATE_KEY when
	// unset (the anchoring design amendment recommends a separate real-value
	// mainnet wallet — set this to use one). Until these are set, a
	// mainnet-entitled tenant anchors on Sepolia (honest pending),
	// and the routing logs the gap.
	envAnchorRPCURLMainnet     = "KIFF_ANCHOR_RPC_URL_MAINNET"
	envAnchorContractMainnet   = "KIFF_ANCHOR_CONTRACT_MAINNET"
	envAnchorPrivateKeyMainnet = "KIFF_ANCHOR_PRIVATE_KEY_MAINNET"

	// envAnchorInterval is the cadence between anchor sweeps, parsed
	// as a Go duration ("30s", "5m"). Defaults to 30s; set to "0"
	// to disable the loop entirely (immediate inline reconcile
	// still runs).
	envAnchorInterval = "KIFF_ANCHOR_INTERVAL"

	// Anchor RPC hardening. The submit pacer + transient-retry
	// envelope applied to every on-chain attester. All optional; the
	// receipt package defaults (750ms spacing, 5 retries, 1s→30s
	// backoff) apply when unset, which is the right shape for a
	// single anchor loop against a public Base RPC endpoint.
	//
	//   - envAnchorSubmitInterval: minimum spacing between on-chain
	//     submits, a Go duration. "0" disables pacing.
	//   - envAnchorMaxRetries: how many times a transient submit
	//     failure (429, "underpriced", transport blip) is retried
	//     with exponential backoff before the receipt falls back to
	//     the next sweep.
	envAnchorSubmitInterval = "KIFF_ANCHOR_SUBMIT_INTERVAL"
	envAnchorMaxRetries     = "KIFF_ANCHOR_MAX_RETRIES"

	// envOpsSubjects is the comma-separated allow-list of Clerk
	// subjects permitted to reach the operator console's
	// cross-tenant /v1/ops/* endpoints. Empty disables the
	// surface entirely (NewOpsHandler returns nil). This is the v1
	// gate; the hardening path replaces it with a Clerk staff role or
	// a cloud_meta.operators table behind the same predicate.
	envOpsSubjects = "KIFF_OPS_SUBJECTS"

	// The integration proofs (proofs). The runner's ingest, POST
	// /v1/ops/proofs, takes a GitHub Actions OIDC token only, for this
	// audience, repository (name, id and owner id), branch ref and
	// workflow_ref; any one missing leaves the ingest unmounted. KIFF_PROOF_TEST_TENANTS
	// (comma-separated) are the only tenants a run may name, the same ids
	// as planque's PLANQUE_MANAGED_TEST_TENANTS. KIFF_PROOF_GITHUB_TOKEN
	// (optional) raises the rate limit of the check that a run's runner
	// commit is on kiff/agents main; KIFF_PROOF_RUNNER_VERSIONS (optional,
	// comma-separated shas) confirms commits without GitHub instead.
	// KIFF_PROOF_RELEASE_ANYWAY=on lets an operator release a red app's
	// writes, each time with the owner's yes recorded; unset, no release.
	// KIFF_PROOF_STATE_TOKENS (comma-separated) are planque's service
	// tokens for GET /v1/ops/proofs/state; unset leaves it unmounted.
	// KIFF_PROOF_TEST_KEYS (comma-separated key fingerprints) are the proof
	// runner's test phone keys: their hold answers count only on the test
	// tenants, and they never make an override.
	envProofOIDCAudience   = "KIFF_PROOF_OIDC_AUDIENCE"
	envProofOIDCRepository = "KIFF_PROOF_OIDC_REPOSITORY"
	envProofOIDCRef        = "KIFF_PROOF_OIDC_REF"
	envProofOIDCWorkflow   = "KIFF_PROOF_OIDC_WORKFLOW"
	// GitHub's numeric ids of the repository and its owner, pinned with
	// the name: required with the four above.
	envProofOIDCRepositoryID = "KIFF_PROOF_OIDC_REPOSITORY_ID"
	envProofOIDCOwnerID      = "KIFF_PROOF_OIDC_OWNER_ID"
	envProofTestTenants      = "KIFF_PROOF_TEST_TENANTS"
	envProofGitHubToken      = "KIFF_PROOF_GITHUB_TOKEN"
	envProofRunnerVersions   = "KIFF_PROOF_RUNNER_VERSIONS"
	envProofReleaseAnyway    = "KIFF_PROOF_RELEASE_ANYWAY"
	envProofStateTokens      = "KIFF_PROOF_STATE_TOKENS"
	envProofTestKeys         = "KIFF_PROOF_TEST_KEYS"

	// the execution-permits design execution permits. The signing key is "kid:base64url(seed)";
	// unset leaves permits off, and a decide request asking for one is
	// refused. Published keys ("kid:base64url(pub),…") are the next key
	// before it signs and a retired one until its permits have expired.
	envPermitSigningKey    = "KIFF_PERMIT_SIGNING_KEY"
	envPermitPublishedKeys = "KIFF_PERMIT_PUBLISHED_KEYS"
	envPermitIssuer        = "KIFF_PERMIT_ISSUER"   // default https://api.kiff.dev
	envPermitLifetime      = "KIFF_PERMIT_LIFETIME" // default 60s, at most 300s

	// Test accounts kept small: every hour, what
	// each listed account finished with more than envPruneAfter ago is
	// deleted, and the account goes on (tenant.Prune). Comma-separated
	// tenant ids; empty prunes nothing. Never a customer's account.
	envPruneTenants = "KIFF_PRUNE_TENANTS"
	envPruneAfter   = "KIFF_PRUNE_AFTER" // default 48h, at least 24h

	// Customer email notifications via Resend. When
	// envResendAPIKey is set, suspend/unsuspend send a customer email;
	// unset → no-op notifier (control actions run identically). From /
	// reply-to are configurable; defaults are KIFF <hello@kiff.dev>, reply-to support@kiff.dev,
	// on the Resend-verified kiff.dev domain.
	envResendAPIKey  = "RESEND_API_KEY"
	envNotifyFrom    = "KIFF_NOTIFY_FROM"
	envNotifyReplyTo = "KIFF_NOTIFY_REPLY_TO"
)

// receiptMode names the receipt pipeline modes selected by
// envReceiptMode.
type receiptMode string

const (
	receiptModeDisabled receiptMode = "disabled"
	receiptModeLocal    receiptMode = "local"
	receiptModeAnchor   receiptMode = "anchor"
)

func main() {
	if err := run(); err != nil {
		// Slog isn't configured yet at this layer; print the
		// fatal cause to stderr and exit non-zero so process
		// supervisors notice.
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", envOr(envAddr, ":8080"), "HTTP listen address")
	flag.Parse()

	logger := newLogger(os.Getenv(envLogFormat))

	if err := checkProductionPosture(os.Getenv); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, closePool, err := openPool(ctx, os.Getenv(envDatabaseURL), logger)
	if err != nil {
		return err
	}
	defer closePool()

	// Dedicated pool for the proposal idempotency advisory lock.
	// It MUST be separate from the pool the limiter
	// and stores draw from: the lock is held across the
	// dedup+limit+evaluate critical section, and the limiter needs
	// its own connection during that window. Sharing one pool
	// deadlocks under a same-key retry storm (waiters park
	// connections blocked on pg_advisory_lock while the leader
	// can't get a connection for the limiter). These connections
	// only run advisory lock/unlock, so the pool is small.
	lockPool, closeLockPool, err := openLockPool(ctx, os.Getenv(envDatabaseURL))
	if err != nil {
		return err
	}
	defer closeLockPool()

	apiKeyStore, walletNonceStore, walletStore, err := buildStores(ctx, pool)
	if err != nil {
		return err
	}

	// Developer-session store (the CLI-auth design, Decision 3). Postgres-backed
	// in production so sessions survive restarts and are per-device
	// revocable; in-memory when there is no pool (local/dev/tests). The
	// schema is applied below alongside the other cloud_meta tables.
	devSessionStore := buildDeveloperSessionStore(pool)
	// The devices people approve with, beside their sessions.
	deviceStore := buildDeviceStore(pool)
	// Device sign-ins waiting for approval outlive a deploy (in Postgres).
	var waitingSignIns auth.WaitingSignIns
	if pool != nil {
		waitingSignIns = auth.NewPostgresWaitingSignIns(pool)
	}
	signedRequests := buildSignedRequests(pool)
	// A passkey computer's own credential (Windows Hello), registered by its
	// pairing and removed with the device.
	passkeys := buildPasskeyStore(pool)
	// The origins a passkey's registration or assertion may come from: kiff.dev's,
	// and each Android app's. A value that doesn't parse stops the start.
	androidApps, err := auth.ParseAndroidApps(os.Getenv(envAndroidApps))
	if err != nil {
		return err
	}
	passkeyRP := auth.PasskeyRelyingParty(androidApps)
	// The approval windows (kiff/tafs#494): one fingerprint, then taps.
	windows := buildWindowStore(pool)
	approvalChecks := buildApprovalCheckStore(pool)

	receiptStore := buildReceiptStore(pool)

	subjectEnsurer, err := buildSubjectEnsurer(ctx, pool, logger)
	if err != nil {
		return err
	}

	// Tenant memberships. Postgres-backed in production, where
	// startup backfills an admin membership for every existing
	// subject→tenant row; in-memory otherwise, where each tenant's first
	// person becomes its founding admin.
	memberStore, err := buildMembershipStore(ctx, pool, logger)
	if err != nil {
		return err
	}

	resolver, fixtureSubjects, err := buildResolver(os.Getenv(envAuthProv), os.Getenv(envDemoTenant), os.Getenv(envStubSubs), subjectEnsurer)
	if err != nil {
		return err
	}

	provider, providerName, err := buildProvider(os.Getenv(envAuthProv), os.Getenv(envClerkSecret), resolver)
	if err != nil {
		return err
	}

	tenantRuntime, tenantStore, domainSource, executorRegistry, closeRuntime, err := buildTenantRuntime(ctx, pool, os.Getenv(envDatabaseURL), logger)
	if err != nil {
		return err
	}
	defer closeRuntime()

	// Studio draft store (the Studio MVP design MVP-2).
	// Postgres-backed when a pool is available; in-memory
	// otherwise (matches the rest of the binary's no-Postgres
	// smoke posture).
	var draftSource domain.DraftSource
	if pool != nil {
		if err := domain.ApplyTenantDomainDraftsSchema(ctx, pool); err != nil {
			return fmt.Errorf("apply tenant_domain_drafts: %w", err)
		}
		draftSource = domain.NewPostgresDraftSource(pool)
	} else {
		draftSource = domain.NewMemoryDraftSource()
	}

	// Control records store. Created on every domain activation;
	// powers GET /v1/me/controls and the controls dashboard page.
	var controlStore domain.ControlStore
	if pool != nil {
		if err := domain.ApplyTenantControlsSchema(ctx, pool); err != nil {
			return fmt.Errorf("apply tenant_controls: %w", err)
		}
		controlStore = domain.NewPostgresControlStore(pool)
	} else {
		controlStore = domain.NewMemoryControlStore()
	}
	logger.Info("control store wired", slog.Bool("postgres", pool != nil))

	// Support tickets store. Postgres-backed when a pool is
	// available; in-memory otherwise. Powers /v1/me/support/* (and the
	// operator triage surface in a follow-up).
	var supportStore support.Store
	if pool != nil {
		if err := support.ApplySchema(ctx, pool); err != nil {
			return fmt.Errorf("apply support_tickets: %w", err)
		}
		supportStore = support.NewPostgresStore(pool)
	} else {
		supportStore = support.NewMemoryStore()
	}
	logger.Info("support store wired", slog.Bool("postgres", pool != nil))

	// Mandate store. Bounds what a named subject may do
	// cumulatively; without a pool the in-memory store keeps local dev
	// and tests working, and a deployment that issues no mandates
	// behaves exactly as it did before.
	// Agent drafts: a new agent and its Cards, confirmed by an
	// owner, whose key the runtime claims once.
	var agentDrafts agentdrafts.Store = agentdrafts.NewMemoryStore()
	if pool != nil {
		if err := agentdrafts.ApplySchema(ctx, pool); err != nil {
			return fmt.Errorf("apply agent_drafts: %w", err)
		}
		agentDrafts = agentdrafts.NewPostgresStore(pool)
	}
	// The owner's halts.
	var haltStore halts.Store = halts.NewMemoryStore()
	if pool != nil {
		if err := halts.ApplySchema(ctx, pool); err != nil {
			return fmt.Errorf("apply halts: %w", err)
		}
		haltStore = halts.NewPostgresStore(pool)
	}
	// The payments their owners reported, which halt their tafs.
	var payReports payreports.Store = payreports.NewMemoryStore()
	if pool != nil {
		if err := payreports.ApplySchema(ctx, pool); err != nil {
			return fmt.Errorf("apply payment reports: %w", err)
		}
		payReports = payreports.NewPostgresStore(pool)
	}
	var mandateStore mandate.Store
	if pool != nil {
		if err := mandate.ApplySchema(ctx, pool); err != nil {
			return fmt.Errorf("apply mandates: %w", err)
		}
		mandateStore = mandate.NewPostgresStore(pool)
		// Built in the background, without blocking writes or the start.
		go func() {
			start := time.Now()
			logger.Info("warning index: building")
			waiting := func() { logger.Info("warning index: another instance is building it; checking again in a minute") }
			if err := mandate.BuildWarningIndexesWhenFree(ctx, pool, time.Minute, waiting); err != nil {
				logger.Error("warning index: build failed", slog.String("err", err.Error()))
				return
			}
			logger.Info("warning index: ready", slog.Duration("took", time.Since(start)))
		}()
	} else {
		mandateStore = mandate.NewMemoryStore()
	}
	logger.Info("mandate store wired", slog.Bool("postgres", pool != nil))
	// What each call is about, in the taf's words, beside its decision.
	var callAbout callabout.Store = callabout.NewMemory()
	if pool != nil {
		if err := callabout.ApplySchema(ctx, pool); err != nil {
			return fmt.Errorf("apply call_about: %w", err)
		}
		callAbout = callabout.NewPostgres(pool)
	}
	// "Your step": steps a taf hands to its owner to finish.
	var handoffs handoff.Store = handoff.NewMemory()
	if pool != nil {
		if err := handoff.ApplySchema(ctx, pool); err != nil {
			return fmt.Errorf("apply handoffs: %w", err)
		}
		handoffs = handoff.NewPostgres(pool)
	}
	// A draft whose confirmation or discard was interrupted can't be
	// claimed; the sweep revokes its Cards and settles it.
	go handlers.SweepAgentDrafts(ctx, agentDrafts, mandateStore, time.Minute, logger)

	permitTargets, permitIssuer, err := buildPermits(ctx, pool, logger)
	if err != nil {
		return err
	}
	if err := startPrune(ctx, pool, logger); err != nil {
		return err
	}
	startSweepExpired(ctx, pool, logger)

	// Agent records. Same pattern: Postgres when there is a
	// pool, in memory otherwise.
	var agentStore agents.Store
	if pool != nil {
		if err := agents.ApplySchema(ctx, pool); err != nil {
			return fmt.Errorf("apply agents: %w", err)
		}
		agentStore = agents.NewPostgresStore(pool)
	} else {
		agentStore = agents.NewMemoryStore()
	}

	// Groups, and who may use each agent. After the memberships
	// and agents schemas: the first apply backfills an "Everyone" group
	// from them.
	var accessStore access.Store
	if pool != nil {
		if err := access.ApplySchema(ctx, pool); err != nil {
			return fmt.Errorf("apply access: %w", err)
		}
		// Once: grants left on archived agents go, each in the trail. The count
		// is logged, no ids; a failure leaves them, as they give no one use.
		if n, err := access.ClearArchivedGrants(ctx, pool); err != nil {
			logger.Error("access: clearing archived agents' grants failed", slog.String("err", err.Error()))
		} else if n > 0 {
			logger.Info("access: cleared grants left on archived agents", slog.Int("removed", n))
		}
		accessStore = access.NewPostgresStore(pool)
	} else {
		accessStore = access.NewMemoryStore()
	}

	// The two meters: each gateway call, once it has
	// ended, classified into cloud_meta.billable_calls. Postgres only: the
	// gateway's record is there.
	var meterStore *meter.Postgres
	if pool != nil {
		if err := meter.ApplySchema(ctx, pool); err != nil {
			return fmt.Errorf("apply meter: %w", err)
		}
		meterStore = meter.NewPostgres(pool)
		go meter.Run(ctx, meterStore, meterSyncEvery, logger)
	}

	// Task packs: the one-time top-ups of tafs tasks, their ledger and the
	// members' requests for them. The pack table is config (KIFF_TASK_PACKS,
	// else the one built in); a broken table stops the API.
	packCatalog, err := packs.LoadCatalog()
	if err != nil {
		return err
	}
	var (
		packLedger   packs.Ledger
		packRequests packs.Requests
		// creditLedger is the packs of credits; runSettler takes Solo's
		// runs from its balance (Postgres only: runs are kept there).
		creditLedger packs.CreditLedger
		runSettler   billing.RunSettler
	)
	if pool != nil {
		if err := packs.ApplySchema(ctx, pool); err != nil {
			return err
		}
		pg := packs.NewPostgres(pool)
		packLedger, packRequests, creditLedger, runSettler = pg, pg, pg, pg
	} else {
		mem := packs.NewMemory()
		packLedger, packRequests, creditLedger = mem, mem, mem
	}
	// The tafs app's Apple purchases: each account's appAccountToken.
	var appleAccounts appstore.Accounts = appstore.NewMemoryAccounts()
	if pool != nil {
		if err := appstore.ApplySchema(ctx, pool); err != nil {
			return err
		}
		appleAccounts = appstore.NewPostgresAccounts(pool)
	}
	// The Android app's Google Play purchases, by purchase token.
	var googlePurchases playstore.Purchases = playstore.NewMemoryPurchases()
	if pool != nil {
		if err := playstore.ApplySchema(ctx, pool); err != nil {
			return err
		}
		googlePurchases = playstore.NewPostgresPurchases(pool)
	}

	// Durable fleet work is available only with Postgres. Capture is installed
	// before serving traffic; new tenant provisioning installs the same extension.
	var attentionStore *attention.Store
	if pool != nil {
		attentionStore = attention.NewStore(tenant.NewSchemaPool(pool))
		ids, err := (tenantActiveLister{tenantStore}).ListActiveIDs(ctx)
		if err != nil {
			return fmt.Errorf("list attention tenants: %w", err)
		}
		for _, id := range ids {
			if err := attentionStore.Migrate(ctx, id); err != nil {
				return fmt.Errorf("migrate attention %s: %w", id, err)
			}
		}
		workerCtx, cancelWorkers := context.WithCancel(ctx)
		workersDone := make(chan struct{})
		go func() {
			defer close(workersDone)
			attention.Run(workerCtx, attentionStore, (tenantActiveLister{tenantStore}).ListActiveIDs, 4, time.Second, logger)
		}()
		defer func() { cancelWorkers(); <-workersDone }()
	}

	// Outbound alert targets store. Postgres-backed when a
	// pool is available; in-memory otherwise. Powers the
	// /v1/me/alert-targets surface and the decide-path dispatcher.
	var alertStore alert.Store
	var alertDeliveries alert.DeliveryStore
	var alertRetries alert.RetryStore
	if pool != nil {
		if err := alert.ApplySchema(ctx, pool); err != nil {
			return fmt.Errorf("apply alert_targets: %w", err)
		}
		if err := alert.ApplyDeliveriesSchema(ctx, pool); err != nil {
			return fmt.Errorf("apply alert_deliveries: %w", err)
		}
		pgAlerts := alert.NewPostgresStore(pool)
		// Targets an earlier app registered take what phones take now: a
		// member's request for more tasks, and its answer; a
		// connection a vendor signed out.
		if n, err := pgAlerts.AddPhoneOutcomes(ctx, parseOperatorSubjects(os.Getenv(envPhoneAlertURLs)),
			alert.PhoneOutcomesAdded); err != nil {
			logger.Warn("alert targets: adding the phone outcomes failed", slog.String("err", err.Error()))
		} else if n > 0 {
			logger.Info("alert targets: phone outcomes added", slog.Int64("targets", n))
		}
		alertStore = pgAlerts
		alertDeliveries = alert.NewPostgresDeliveryStore(pool)
		alertRetries = alert.NewPostgresRetryStore(pool)
	} else {
		alertStore = alert.NewMemoryStore()
		alertDeliveries = alert.NewMemoryDeliveryStore()
		alertRetries = alert.NewMemoryRetryStore(alertStore)
	}
	// Dispatcher fans out consequential decisions to a tenant's
	// webhooks, best-effort and off the request path. The base URL
	// absolutizes the approval review link in the payload. The HTTP
	// sink dials through an SSRF guard; allowPrivate is the
	// env-gated dev escape hatch for local webhooks. The delivery
	// store persists each attempt (delivered/failed/blocked) against
	// the triggering decision.
	allowPrivateWebhooks := os.Getenv(envAllowPrivateWebhooks) == "true"
	alertDispatcher := alert.NewDispatcher(alertStore, alert.NewHTTPSink(allowPrivateWebhooks),
		alert.WithLogger(logger),
		alert.WithBaseURL(os.Getenv(envAppBaseURL)),
		alert.WithDeliveryStore(alertDeliveries),
		alert.WithRetries(alertRetries),
	)
	// Failed deliveries are sent again on a backoff for about a day; a
	// target that keeps failing is switched off.
	retrier := &alert.Retrier{Targets: alertStore, Retries: alertRetries, Deliveries: alertDeliveries,
		Sink: alert.NewHTTPSink(allowPrivateWebhooks), Logger: logger}
	retryCtx, stopRetries := context.WithCancel(ctx)
	retriesDone := make(chan struct{})
	go func() { defer close(retriesDone); retrier.Run(retryCtx) }()
	// Held calls nobody answered are recorded as they expire, and their
	// alert targets told.
	if expirer, ok := mandateStore.(mandate.HoldExpirer); ok {
		go handlers.SweepExpiredHolds(ctx, expirer, alertDispatcher, time.Minute, logger)
	}
	// Hand-offs nobody answered close as no_answer, their runtime is told,
	// and every hand-off gets its receipts.
	handoffSweeper := &handoff.Sweeper{Store: handoffs, Receipts: receiptStore, Alerts: alertDispatcher, Logger: logger}
	go handoffSweeper.Run(ctx, time.Minute)
	defer func() { stopRetries(); <-retriesDone }()
	logger.Info("alert dispatcher wired",
		slog.Bool("postgres", pool != nil),
		slog.Bool("allow_private_webhooks", allowPrivateWebhooks),
	)

	// Billing webhook sync. The Stripe webhook maps verified
	// subscription/checkout events to tenant plan state. Postgres-
	// backed when a pool is wired (idempotency ledger + customer
	// correlation survive restarts), in-memory otherwise. The route
	// is mounted only when a signing secret is configured.
	var billingStore billing.Store
	var billingLedger billing.UsageLedger
	if pool != nil {
		if err := billing.ApplySchema(ctx, pool); err != nil {
			return fmt.Errorf("apply billing schema: %w", err)
		}
		pgBilling := billing.NewPostgresStore(pool)
		billingStore = pgBilling
		billingLedger = pgBilling // *PostgresStore also implements UsageLedger
	} else {
		billingStore = billing.NewMemoryStore()
		billingLedger = billing.NewMemoryUsageLedger()
	}
	stripeWebhookSecret := os.Getenv(envStripeWebhookSecret)
	// The v1 offer's Stripe: checkout, the plan from each
	// subscription as Stripe has it now, overage and Team's seats. Nil
	// without the secret key.
	stripeClient := billing.NewStripe(os.Getenv(envStripeSecretKey))
	var billingProcessor *billing.Processor
	var appleHandler *handlers.AppleHandler
	var googleBilling *playstore.Billing
	var googlePush *playstore.PushVerifier
	if tenantStore != nil {
		billingProcessor = billing.NewProcessor(billingStore, tenantStore, logger).WithPacks(packLedger, packCatalog).
			WithRequestAlerts(packRequests, alertDispatcher)
		if stripeClient != nil {
			billingProcessor.WithSubscriptions(stripeClient)
		}
		// Apple's signed payloads, verified against the pinned Apple root;
		// notifications are idempotent in the same ledger as Stripe's events.
		bundleID := strings.TrimSpace(os.Getenv(envAppleBundleID))
		if bundleID == "" {
			bundleID = "bot.tafs.app"
		}
		environments := strings.Split(os.Getenv(envAppleEnvironments), ",")
		if strings.TrimSpace(os.Getenv(envAppleEnvironments)) == "" {
			environments = []string{"Production", "Sandbox"}
		}
		verifier, err := appstore.NewVerifier(bundleID, environments)
		if err != nil {
			return err
		}
		appleHandler = handlers.NewAppleHandler(verifier,
			appstore.NewProcessor(tenantStore, appleAccounts, packLedger, packCatalog, billingStore, logger).
				WithCredits(creditLedger).
				WithRequestAlerts(packRequests, alertDispatcher).
				WithSandboxAccounts(strings.Split(os.Getenv(envAppleSandboxTenants), ",")...), logger)

		// Google Play: each purchase token read back from the Play API with
		// KIFF's service account, applied with the same rules as Apple's.
		googleBilling, googlePush, err = buildGooglePlay(tenantStore, appleAccounts, googlePurchases, packLedger, creditLedger, packCatalog,
			billingStore, packRequests, alertDispatcher, logger)
		if err != nil {
			return err
		}
	}
	logger.Info("billing webhook wired",
		slog.Bool("postgres", pool != nil),
		slog.Bool("secret_configured", stripeWebhookSecret != ""),
	)

	// Billing Customer Portal. Mints Stripe-hosted portal
	// sessions (card update / invoices / cancel) on the authenticated
	// /v1/me/billing-portal endpoint. Needs the Stripe secret key,
	// which stays api-side; nil when unset leaves the route unmounted.
	stripePortalClient := billing.NewPortalClient(os.Getenv(envStripeSecretKey))
	logger.Info("billing portal wired",
		slog.Bool("secret_key_configured", stripePortalClient != nil),
	)

	// Overage: each counted action past what the period
	// includes is one meter event on its overage price, which needs Stripe.
	// Each run that used credits is settled: on Solo, from the included
	// credits, then the packs, which doesn't. Team's seats follow its admins
	// and editors, through Stripe.
	// The governed-operation reporter is retired with the unit it
	// billed.
	if meterStore != nil && tenantStore != nil {
		var actions, credits billing.OverageSender
		if stripeClient != nil {
			actions = billing.NewMeterEventClient(os.Getenv(envStripeSecretKey), billing.MeterEventAction)
			// Company's credits past the included ones, on the credit
			// overage price.
			credits = billing.NewMeterEventClient(os.Getenv(envStripeSecretKey), billing.MeterEventCredit)
		}
		reporter := billing.NewOverageReporter(meterStore, tenantStore, actions, logger).WithRuns(meterStore, runSettler).WithCredits(credits)
		if alertDispatcher != nil {
			reporter.WithWarnings(billing.NewCreditWarnings(tenantStore, meterStore, creditLedger, alertDispatcher))
		}
		go reporter.Run(ctx, overageEvery)
		if stripeClient != nil && memberStore != nil {
			go billing.NewSeatsReconciler(tenantStore, memberStore, stripeClient, logger).Run(ctx, seatsEvery)
		}
	}
	logger.Info("billing overage and seats wired", slog.Bool("enabled", stripeClient != nil && meterStore != nil))

	// Usage aggregator. Postgres-backed in production; in-memory
	// fallback when no pool is wired so the routing tree stays
	// usable in tests / dev. The aggregator is started here and
	// stopped at shutdown so cloud counters (proposal outcomes,
	// framework runtime counters) flush periodically into
	// cloud_meta.tenant_usage. Same pool is shared.
	usageStore, closeUsage, err := buildUsageStore(ctx, pool, logger)
	if err != nil {
		return err
	}
	defer closeUsage()
	usageAgg := usage.NewAggregator(usageStore, usage.Options{Interval: 30 * time.Second})
	usageAgg.Start(ctx)
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := usageAgg.Stop(shutdown); err != nil {
			logger.WarnContext(shutdown, "usage aggregator stop", slog.String("err", err.Error()))
		}
	}()
	logger.Info("usage aggregator started", slog.Bool("postgres", pool != nil))

	// Plan-limit enforcement. Reads the per-tenant plan
	// from tenant.Metadata via the tenant store and the
	// current-period proposal count from the usage store. The
	// aggregator is used as the flusher so the limiter sees
	// every increment up to the request instant — without that,
	// a 30-second flush window would let the same tenant exceed
	// the limit by tens of requests.
	planResolver := tenant.NewPlanResolver(tenantStore)
	limiter, err := usage.NewProposalLimiter(usageStore, planResolver,
		usage.WithLimiterFlusher(usageAgg),
	)
	if err != nil {
		return fmt.Errorf("plan limiter: %w", err)
	}
	logger.Info("proposal limiter wired (free_dev = 1000/mo, builder = 50000, agency = 250000)")

	guardStore, err := buildGuardStore(ctx, pool, logger)
	if err != nil {
		return err
	}

	// In stub mode, the resolver pins every fixture subject to the
	// demo tenant id. Seed a corresponding row in the tenant store
	// so /v1/me (which looks the tenant up) returns 200 instead of
	// 401 with "tenant not found". Postgres-backed deploys skip
	// this; the lazy resolver auto-provisions tenants per subject.
	if pool == nil && len(fixtureSubjects) > 0 {
		seedID := strings.ToLower(strings.TrimSpace(os.Getenv(envDemoTenant)))
		// The tenant store rejects non-ULID ids. If the operator
		// supplied a non-ULID demo tenant id (or the default
		// "demo-tenant" was inferred), mint a fresh ULID and
		// re-stamp every fixture subject onto it so the resolver
		// stays in sync.
		if seedID == "" || !isULID(seedID) {
			seedID = tenant.NewID()
			if mr, ok := resolver.(*auth.MapResolver); ok {
				for _, sub := range fixtureSubjects {
					existing, _, _ := mr.Resolve(ctx, auth.Identity{Subject: sub, Method: auth.MethodSession})
					mr.Set(sub, existing, seedID)
				}
			}
		}
		if _, err := tenantStore.Get(ctx, seedID); err != nil {
			if err := tenantStore.Create(ctx, tenant.Tenant{
				ID:          seedID,
				Slug:        "demo",
				DisplayName: "Demo tenant (stub mode)",
				Status:      tenant.StatusActive,
			}); err != nil {
				logger.Warn("seed demo tenant: failed; /v1/me will return 401",
					slog.String("err", err.Error()))
			} else {
				logger.Info("seeded demo tenant for stub mode",
					slog.String("tenant_id", seedID))
			}
		}
	}

	// Decision-reconcile watermark: Postgres-backed when a pool is
	// available; nil otherwise (in-memory deploys keep the full-scan path).
	var decisionCursor receipt.DecisionCursor
	if pool != nil && receiptStore != nil {
		if err := receipt.ApplyDecisionCursorSchema(ctx, pool); err != nil {
			return fmt.Errorf("apply decision cursor schema: %w", err)
		}
		decisionCursor = receipt.NewPostgresDecisionCursor(pool)
	}

	// Settled receipts: how each held call ended, from its Card
	// exception, who answered and what the tool reported.
	var settledTools tooloutcome.Reader
	if pool != nil {
		settledTools = tooloutcome.NewPostgres(pool)
	}
	var settlements receipt.PipelineOption
	if mandateStore != nil {
		src := settle.Source{Exceptions: mandateStore, Tools: settledTools}
		if memberStore != nil {
			src.Answers = memberStore
		}
		settlements = receipt.WithSettlements(src)
	}
	// The answerer by email on settled receipts, as Needs you names them.
	var answererEmails receipt.PipelineOption
	if pool != nil {
		answererEmails = receipt.WithAnswererEmails(owneremail.NewClerkEmailLookup())
	}
	pipeline, attesterMode, closePipeline, err := buildReceiptPipeline(ctx, receiptStore, tenantStore, decisionCursor, logger, settlements, answererEmails)
	if err != nil {
		return err
	}
	defer closePipeline()

	var postExecuteHook handlers.PostExecuteHook
	var blockedExecuteHook handlers.BlockedExecuteHook
	if pipeline != nil {
		postExecuteHook = func(hookCtx context.Context, tenantID string, rt *kiffruntime.Runtime) {
			built, rerr := pipeline.ReconcileTenant(hookCtx, tenantID, rt)
			if rerr != nil {
				logger.WarnContext(hookCtx, "receipt: post-execute reconcile failed",
					slog.String("tenant", tenantID),
					slog.String("err", rerr.Error()),
				)
				return
			}
			if built > 0 {
				logger.DebugContext(hookCtx, "receipt: post-execute reconcile",
					slog.String("tenant", tenantID),
					slog.Int("built", built),
				)
			}
		}
		blockedExecuteHook = func(hookCtx context.Context, tenantID string, rt *kiffruntime.Runtime) {
			built, rerr := pipeline.ReconcileBlockedStateNotAllowed(hookCtx, tenantID, rt)
			if rerr != nil {
				logger.WarnContext(hookCtx, "receipt: blocked-execute reconcile failed",
					slog.String("tenant", tenantID),
					slog.String("err", rerr.Error()),
				)
				return
			}
			if built > 0 {
				logger.DebugContext(hookCtx, "receipt: blocked-execute reconcile",
					slog.String("tenant", tenantID),
					slog.Int("built", built),
				)
			}
		}
	}

	// Anchor sweep loop. Disabled when the pipeline is unavailable
	// (no receipt store) or when the operator sets the interval to
	// zero. Postgres-backed deployments wire the loop against the
	// tenant store; in-memory deployments skip it (the only audit
	// available is the integration test's, which doesn't outlive
	// the test).
	// Ended sign-in connections are deleted 30 days after they end, with
	// their tokens and codes. The tables exist whenever there is a
	// pool (openPool applies the OAuth schema).
	if pool != nil {
		go oauth.RunRetention(ctx, oauth.NewStore(pool), logger, 6*time.Hour, nil)
	}
	if pipeline != nil && pool != nil {
		interval := parseAnchorInterval(os.Getenv(envAnchorInterval), 30*time.Second)
		if interval > 0 {
			lister := tenantActiveLister{store: tenant.NewPostgresStore(pool)}
			go pipeline.RunAnchorLoop(ctx, lister, tenantRuntime, interval)
			logger.Info("receipt anchor loop started",
				slog.String("attester", attesterMode),
				slog.Duration("interval", interval),
			)
		} else {
			logger.Info("receipt anchor loop disabled by KIFF_ANCHOR_INTERVAL=0")
		}
	}

	// GET /v1/proposals reader. Reads kiff_t_<id>.kiff_audit
	// through tenant.SchemaPool (search_path-pinned per checkout)
	// and enriches each row with the matching cloud_meta.receipts
	// row when one exists. Wired only when both a Postgres pool
	// and a receipt store are available — the in-memory smoke
	// path leaves it nil and the handler returns 503 on the route,
	// which is correct for a config that has no per-tenant pool to
	// read from.
	var decisionsReader proposals.Reader
	if pool != nil && receiptStore != nil {
		decisionsReader = proposals.NewPostgresReader(tenant.NewSchemaPool(pool), receiptStore)
	}

	// Posture-change history reader. Reads posture_changed
	// records from kiff_t_<id>.kiff_audit through the same
	// search_path-pinned SchemaPool. Wired when a Postgres pool is
	// available; nil leaves the control header's posture block omitted
	// ("not yet recorded"), preserving the contract.
	var approvalReader handlers.TenantApprovalLister
	var approvalDomains handlers.ApprovalDomainResolver
	var postureReader posture.Reader
	if pool != nil {
		postureReader = posture.NewPostgresReader(tenant.NewSchemaPool(pool))
		tenantApprovals := approvals.NewTenantReader(tenant.NewSchemaPool(pool))
		approvalReader = tenantApprovals
		approvalDomains = tenantApprovals
	}

	// Operator audit store. Append-only record of mutating
	// operator actions; required for the /v1/ops suspend/unsuspend +
	// audit-log routes. Postgres-only — the in-memory smoke path
	// leaves it nil, which leaves the mutation routes unmounted.
	var operatorAudit operatoraudit.Store
	if pool != nil {
		if err := operatoraudit.ApplySchema(ctx, pool); err != nil {
			return fmt.Errorf("apply operator_audit schema: %w", err)
		}
		operatorAudit = operatoraudit.NewPostgresStore(pool)
		logger.Info("operator audit store: Postgres-backed (cloud_meta.operator_audit)")
	}

	// Operator alert queue. Postgres-only; nil
	// leaves the /v1/ops/alerts routes unmounted.
	var operatorAlerts operatoralerts.Store
	if pool != nil {
		if err := operatoralerts.ApplySchema(ctx, pool); err != nil {
			return fmt.Errorf("apply operator_alerts schema: %w", err)
		}
		operatorAlerts = operatoralerts.NewPostgresStore(pool)
		logger.Info("operator alert queue: Postgres-backed (cloud_meta.operator_alerts)")
	}

	// The integration proofs' runs and overrides (proofs): Postgres when
	// there's a pool, else in memory.
	var proofStore proofs.Store = proofs.NewMemoryStore()
	if pool != nil {
		if err := proofs.ApplySchema(ctx, pool); err != nil {
			return fmt.Errorf("apply proofs schema: %w", err)
		}
		proofStore = proofs.NewPostgresStore(pool)
	}
	// One reader of every app's and entry's state, cached a minute; past
	// 24 h unreadable it says unknown and logs, at most hourly.
	proofReader := proofs.NewReader(proofStore, func(lastGood time.Time) {
		logger.Error("proofs: states unreadable for over 24 h; every app and entry reads unknown",
			slog.Time("last_good", lastGood))
	})
	proofTestKeys, badKeys := proofs.ParseTestKeys(splitList(os.Getenv(envProofTestKeys)))
	if len(badKeys) > 0 {
		logger.Warn("proofs: unreadable entries in "+envProofTestKeys+" ignored", slog.Int("count", len(badKeys)))
	}
	// The test tenants must be KIFF's own accounts (plan internal) holding
	// only proof test keys: any other id keeps the ingest unmounted and the
	// test keys refused there.
	proofTenants := vetProofTestTenants(ctx, proofTenantStores{tenants: tenantStore, devices: deviceStore, passkeys: passkeys, sessions: devSessionStore},
		proofTestKeys, splitList(os.Getenv(envProofTestTenants)), logger)
	// A test tenant holds only test keys: no device or passkey that isn't one
	// joins it, by any path (409 test_tenant_key), so it never stops being a
	// test account. Every handler takes the stores from here on.
	testTenantGuard := proofTestTenantGuard(tenantStore, proofTestKeys, splitList(os.Getenv(envProofTestTenants)))
	deviceStore = auth.GuardDevices(deviceStore, testTenantGuard)
	passkeys = auth.GuardPasskeys(passkeys, testTenantGuard)
	devSessionStore = auth.GuardSessions(devSessionStore, testTenantGuard)

	// Customer email notifications. The owner-email resolver
	// maps tenant → owner subject (Postgres) → email (Clerk, never
	// stored). The notifier sends via Resend when RESEND_API_KEY is
	// set; otherwise notifications stay off and suspend/unsuspend run
	// identically without email.
	var ownerResolver *owneremail.Resolver
	if pool != nil {
		ownerResolver = owneremail.NewResolver(
			tenant.NewPostgresSubjectStore(pool),
			owneremail.NewClerkEmailLookup(),
		).WithMembers(memberStore)
	} else {
		// No Postgres → no tenant→subject mapping → no owner-email
		// resolution, so customer notifications stay off regardless of
		// RESEND_API_KEY. Log it so the startup picture is complete.
		logger.Info("customer notifications: disabled (in-memory mode, no owner-email resolver)")
	}
	var customerNotifier notify.Notifier
	if key := strings.TrimSpace(os.Getenv(envResendAPIKey)); key != "" {
		from := strings.TrimSpace(os.Getenv(envNotifyFrom))
		if from == "" {
			from = "KIFF <hello@kiff.dev>"
		}
		replyTo := strings.TrimSpace(os.Getenv(envNotifyReplyTo))
		if replyTo == "" {
			replyTo = "support@kiff.dev"
		}
		rn, err := notify.NewResendNotifier(notify.ResendConfig{APIKey: key, From: from, ReplyTo: replyTo})
		if err != nil {
			return fmt.Errorf("resend notifier: %w", err)
		}
		customerNotifier = rn
		logger.Info("customer notifications: Resend enabled",
			slog.String("from", from), slog.String("reply_to", replyTo))
	} else {
		logger.Info("customer notifications: disabled (RESEND_API_KEY unset)")
	}

	// The "new device signed in" email (newdevice): the sign-in provider's
	// signed session.created events, a hash of each person's devices, and
	// the email a moment later. Needs the database, Resend and the secret.
	var newDevices *newdevice.Service
	if secret := strings.TrimSpace(os.Getenv(envClerkWebhookSecret)); secret != "" {
		mailer, isMailer := customerNotifier.(newdevice.Mailer)
		switch {
		case pool == nil || !isMailer || customerNotifier == nil:
			logger.Info("new-device email: disabled (needs Postgres and Resend)")
		default:
			if err := newdevice.ApplySchema(ctx, pool); err != nil {
				return fmt.Errorf("apply new_device schema: %w", err)
			}
			site := strings.TrimRight(os.Getenv(envAppBaseURL), "/")
			if site == "" {
				site = "https://kiff.dev"
			}
			clerkDevices := newdevice.NewClerkDescriber()
			newDevices = &newdevice.Service{Store: newdevice.NewPostgres(pool), Mailer: mailer, Emails: owneremail.NewClerkEmailLookup(),
				Describer: clerkDevices, Accounts: clerkDevices, Subjects: tenant.NewPostgresSubjectStore(pool), Tenants: tenantStore, Secret: secret,
				SiteURL: site, SessionsURL: newDeviceSessionsURL, Logger: logger}
			go newDevices.Run(ctx, 30*time.Second)
			logger.Info("new-device email: enabled", slog.String("route", "/webhooks/clerk"))
		}
	}

	// Signing in with a code sent by email (codesignin): needs its key, the
	// database, Resend and the sign-in provider, else it's off and no one
	// can turn it on.
	stepUp := buildStepUp(providerName)
	var codeSignIn *codesignin.Service
	codeKey, err := codesignin.ParseKey(os.Getenv(envSignInCodeKey))
	if err != nil {
		return err
	}
	mailer, isMailer := customerNotifier.(codesignin.Mailer)
	switch {
	case codeKey == nil:
		logger.Info("code sign-in: off (KIFF_SIGNIN_CODE_KEY unset)")
	case pool == nil || !isMailer || customerNotifier == nil || providerName != auth.ProviderClerk:
		logger.Info("code sign-in: off (needs Postgres, Resend and the sign-in provider)")
	default:
		codeSignIn, err = codesignin.New(codeKey, codesignin.NewPostgres(pool), codesignin.NewClerkPeople(), mailer, logger)
		if err != nil {
			return err
		}
		// A session made by a code sign-in is never a recent sign-in.
		stepUp.CodeSignedIn = codeSignIn.SignedInWithCode
		logger.Info("code sign-in: on")
	}

	if warningStore, ok := mandateStore.(*mandate.PostgresStore); ok && attentionStore != nil {
		worker := &cardwarnings.Worker{Store: warningStore, Attention: attentionStore,
			Recipients: ownerResolver, Targets: alertStore, Sink: alert.NewHTTPSink(allowPrivateWebhooks),
			BaseURL: os.Getenv(envAppBaseURL), Logger: logger}
		if mailer, ok := customerNotifier.(cardwarnings.Mailer); ok {
			worker.Mailer = mailer
		}
		if tenantStore != nil {
			worker.Tenants = tenantStore // each email in its recipient's language
		}
		warningCtx, stopWarnings := context.WithCancel(ctx)
		warningsDone := make(chan struct{})
		go func() { defer close(warningsDone); worker.Run(warningCtx) }()
		defer func() { stopWarnings(); <-warningsDone }()
	}

	// Refunds governed by the gateway's thune module: a refund the processor
	// says failed gives its Card room back, and owners are told about it and
	// about a charge also refunded outside KIFF, read from the gateway's own
	// rows (tooloutcome.RefundWatch). A refund the owner said didn't happen
	// that the processor then lists takes its room again, and is told.
	if releaser, ok := mandateStore.(tooloutcome.EffectReleaser); ok && pool != nil {
		if err := tooloutcome.ApplyRefundSchema(ctx, pool); err != nil {
			return fmt.Errorf("apply refund notices schema: %w", err)
		}
		refundWatch := &tooloutcome.RefundWatch{Outcomes: tooloutcome.NewPostgres(pool), Releases: releaser,
			Alerts: alert.Refunds{Notifier: alertDispatcher}, Logger: logger}
		// A refund the owner said didn't happen, then found at the
		// processor, takes its room on the Card again.
		if r, ok := mandateStore.(mandate.EffectReinstateStore); ok {
			refundWatch.Reinstates = r
		}
		go refundWatch.Run(ctx, time.Minute)
	}

	// The v1 offer sells tasks and actions, with the
	// owner's tasks ceiling; the allowance alerts on governed operations
	// are retired with the allowance they measured.

	// Live-event broker: pushes decision events to dashboard SSE
	// subscribers. In-process (single-binary deploy); see internal/stream.
	broker := stream.NewBroker()

	handler := newRouter(routerDeps{
		provider:             provider,
		apiKeyStore:          apiKeyStore,
		agentDrafts:          agentDrafts,
		halts:                haltStore,
		payReports:           payReports,
		planques:             planquesFor(splitCSV(os.Getenv(envPlanqueURLs)), permitIssuer),
		devSessionStore:      devSessionStore,
		deviceStore:          deviceStore,
		waitingSignIns:       waitingSignIns,
		signedRequests:       signedRequests,
		passkeys:             passkeys,
		passkeyRP:            passkeyRP,
		windows:              windows,
		approvalChecks:       approvalChecks,
		walletNonceStore:     walletNonceStore,
		walletStore:          walletStore,
		receiptStore:         receiptStore,
		tenantStore:          tenantStore,
		resolver:             resolver,
		tenantRuntime:        tenantRuntime,
		domainSource:         domainSource,
		draftSource:          draftSource,
		controlStore:         controlStore,
		supportStore:         supportStore,
		alertStore:           alertStore,
		attentionStore:       attentionStore,
		alertDeliveries:      alertDeliveries,
		alertNotifier:        alertDispatcher,
		allowPrivateWebhooks: allowPrivateWebhooks,
		phoneAlertURLs:       parseOperatorSubjects(os.Getenv(envPhoneAlertURLs)),
		billingProcessor:     billingProcessor,
		billingLedger:        billingLedger,
		stripeWebhookSecret:  stripeWebhookSecret,
		newDevices:           newDevices,
		stripePortalClient:   stripePortalClient,
		stripe:               stripeClient,
		appBaseURL:           os.Getenv(envAppBaseURL),
		pool:                 pool,
		lockPool:             lockPool,
		executorRegistry:     executorRegistry,
		runtimeResetter:      tenantRuntime,
		postExecuteHook:      postExecuteHook,
		blockedHook:          blockedExecuteHook,
		usageStore:           usageStore,
		usageAgg:             usageAgg,
		meter:                meterStore,
		packLedger:           packLedger,
		creditLedger:         creditLedger,
		packRequests:         packRequests,
		packCatalog:          packCatalog,
		appleAccounts:        appleAccounts,
		appleHandler:         appleHandler,
		googleHandler:        handlers.NewGoogleHandler(googleBilling, googlePush, logger),
		googleBilling:        googleBilling != nil,
		limiter:              limiter,
		decisionsReader:      decisionsReader,
		postureReader:        postureReader,
		approvalReader:       approvalReader,
		approvalDomains:      approvalDomains,
		guardStore:           guardStore,
		mandateStore:         mandateStore,
		callAbout:            callAbout,
		handoffs:             handoffs,
		handoffSweeper:       handoffSweeper,
		permitIssuer:         permitIssuer,
		permitTargets:        permitTargets,
		agentStore:           agentStore,
		accessStore:          accessStore,
		operatorSubjects:     parseOperatorSubjects(os.Getenv(envOpsSubjects)),
		operatorAudit:        operatorAudit,
		operatorAlerts:       operatorAlerts,
		proofStore:           proofStore,
		proofReader:          proofReader,
		proofTestKeys:        proofTestKeys,
		proofTestTenants:     proofTenants.own,
		proofTenantsRefused:  len(proofTenants.refused) > 0,
		ownerEmail:           ownerResolver,
		customerNotifier:     customerNotifier,
		memberStore:          memberStore,
		stepUp:               stepUp,
		codeSignIn:           codeSignIn,
		broker:               broker,
		logger:               logger,
	})
	proofs.LogBoot(logger, proofBootConfig(proofStore, proofTestKeys, proofTenants))

	regionGate, err := buildRegionGate(os.Getenv(envRegionTable), logger)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr: *addr,
		// One line per request: its route, status and duration, never an id
		// (requestlog).
		Handler:           requestlog.Wrap(regionGate.Wrap(handler), logger),
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Info("api listening",
		slog.String("addr", *addr),
		slog.String("auth_provider", string(providerName)),
		slog.Bool("postgres", pool != nil),
		slog.Int("stub_subjects", len(fixtureSubjects)),
	)

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", slog.String("err", err.Error()))
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// ─── Wiring helpers ───────────────────────────────────────────────

// routerDeps bundles the wiring inputs newRouter needs. Keeping
// this as a struct lets the integration test in main_test.go build
// a router from fakes without dragging in the full run() signature.
type routerDeps struct {
	attentionStore *attention.Store
	provider       auth.Provider
	apiKeyStore    auth.APIKeyStore
	agentDrafts    agentdrafts.Store
	// halts are the owner's halts, and planques the planques that confirm
	// them.
	halts    halts.Store
	planques halts.Confirmer
	// payReports are the payments reported as unexpected; each halts its taf.
	payReports      payreports.Store
	devSessionStore auth.DeveloperSessionStore
	// deviceStore keeps the devices people approve with. Nil: a
	// phone's key stays on its session alone, as before.
	deviceStore auth.DeviceStore
	// waitingSignIns keeps device sign-ins waiting for approval across a
	// restart; nil keeps them in memory only.
	waitingSignIns auth.WaitingSignIns
	// signedRequests records a phone's signed setup calls so none is made
	// twice, across restarts and processes.
	signedRequests auth.SignedRequests
	// passkeys keeps the passkeys computers approve with (Windows Hello).
	passkeys auth.PasskeyStore
	// passkeyRP is who passkeys are for and the origins they may come from.
	passkeyRP auth.RelyingParty
	// windows keeps the approval windows (kiff/tafs#494).
	windows              auth.WindowStore
	approvalChecks       auth.ApprovalCheckStore
	walletNonceStore     auth.WalletNonceStore
	walletStore          auth.WalletStore
	receiptStore         receipt.Store
	tenantStore          tenant.Store
	resolver             auth.ActorResolver
	tenantRuntime        handlers.TenantRuntimeAccessor
	domainSource         domain.Source
	draftSource          domain.DraftSource
	controlStore         domain.ControlStore
	supportStore         support.Store
	alertStore           alert.Store
	alertDeliveries      alert.DeliveryStore
	alertNotifier        alert.Notifier
	allowPrivateWebhooks bool
	phoneAlertURLs       []string
	billingProcessor     *billing.Processor
	billingLedger        billing.UsageLedger
	stripeWebhookSecret  string
	// newDevices is the new-device email's webhook and sign-in marker; nil
	// when it's off.
	newDevices         *newdevice.Service
	stripePortalClient *billing.PortalClient
	stripe             *billing.Stripe
	// Task packs, the members' requests for them, the pack table, and the
	// tafs app's Apple purchases.
	packLedger    packs.Ledger
	creditLedger  packs.CreditLedger
	packRequests  packs.Requests
	packCatalog   packs.Catalog
	appleAccounts appstore.Accounts
	appleHandler  *handlers.AppleHandler
	// googleHandler serves the Android app's Google Play purchases; with
	// googleBilling false, it answers that they aren't set up.
	googleHandler *handlers.GoogleHandler
	googleBilling bool
	appBaseURL    string
	pool          *pgxpool.Pool
	// gatewayOwners overrides the gateway-table read of who connected each
	// tool; tests set it.
	gatewayOwners    handlers.GatewayToolOwners
	lockPool         *pgxpool.Pool
	executorRegistry *executors.Registry
	runtimeResetter  handlers.RuntimeResetter
	postExecuteHook  handlers.PostExecuteHook
	blockedHook      handlers.BlockedExecuteHook
	usageStore       usage.Store
	usageAgg         *usage.Aggregator
	meter            *meter.Postgres
	limiter          handlers.Limiter
	decisionsReader  proposals.Reader
	postureReader    posture.Reader
	// approvalReader serves the tenant-wide approval queue for
	// management identities. Nil in memory mode, where the handler
	// reports 503 for that path rather than silently narrowing the
	// queue to one domain.
	approvalReader   handlers.TenantApprovalLister
	approvalDomains  handlers.ApprovalDomainResolver
	guardStore       guard.Store
	mandateStore     mandate.Store
	callAbout        callabout.Store
	handoffs         handoff.Store
	handoffSweeper   *handoff.Sweeper
	permitIssuer     *permit.Issuer
	permitTargets    permit.Targets
	agentStore       agents.Store
	accessStore      access.Store
	operatorSubjects []string
	operatorAudit    operatoraudit.Store
	operatorAlerts   operatoralerts.Store
	proofStore       proofs.Store
	proofReader      *proofs.Reader
	proofTestKeys    proofs.TestKeys

	// proofTestTenants are KIFF_PROOF_TEST_TENANTS vetted at boot: KIFF's
	// own only. proofTenantsRefused is set when any listed id wasn't, and
	// keeps the ingest unmounted.
	proofTestTenants    []string
	proofTenantsRefused bool

	ownerEmail  *owneremail.Resolver
	memberStore membership.Store
	stepUp      *auth.StepUp
	// codeSignIn is signing in with a code sent by email; nil when off.
	codeSignIn       *codesignin.Service
	customerNotifier notify.Notifier
	broker           *stream.Broker
	logger           *slog.Logger
}

// newRouter assembles the http.Handler tree: /healthz stays
// public, /v1/* sits behind the auth middleware. Pure function so
// the test can drive it with stub stores.
func newRouter(deps routerDeps) http.Handler {
	if deps.passkeyRP.ID == "" {
		deps.passkeyRP = auth.KiffRelyingParty
	}
	mwOpts := []auth.MiddlewareOption{
		auth.WithLogger(deps.logger),
		auth.WithWallets(deps.walletStore),
		auth.WithDeveloperSessions(deps.devSessionStore),
		auth.WithDevices(deps.deviceStore),
	}
	var memberSvc *membership.Service
	var members *handlers.MembersHandler
	// holdAlert raises an operator alert when who answered a hold could
	// not be recorded: the answer stands, but its statement line would
	// lack the role and policy.
	holdAlert := func(ctx context.Context, tenantID, holdID string, err error) {
		deps.logger.ErrorContext(ctx, "hold answer attribution not recorded",
			slog.String("tenant", tenantID), slog.String("hold", holdID), slog.String("err", err.Error()))
		if deps.operatorAlerts != nil {
			_, _ = deps.operatorAlerts.Upsert(ctx, operatoralerts.Upsert{TenantID: tenantID,
				Kind: "hold_attribution_failed", Severity: operatoralerts.SeverityWarn,
				Detail: "who answered hold " + holdID + " could not be recorded: " + err.Error()}, time.Now().UTC())
		}
	}
	if deps.memberStore != nil {
		memberSvc = membership.NewService(deps.memberStore)
		mwOpts = append(mwOpts, auth.WithMemberships(memberSvc))
	}
	appSignIn := buildAppSignIn(deps)
	oauthServer := buildOAuthServer(deps, appSignIn)
	if oauthServer != nil {
		mwOpts = append(mwOpts, auth.WithOAuthTokens(oauthServer))
	}
	// The proof runner's test phone keys act only on the test tenants: a
	// session holding one elsewhere reaches no route, and one is never
	// signed in, made an approval key or paired elsewhere.
	keyRestriction := proofKeyRestriction(deps)
	mwOpts = append(mwOpts, auth.WithKeyRestriction(keyRestriction))
	mw := auth.NewMiddleware(deps.provider, deps.apiKeyStore, deps.resolver, mwOpts...)
	mux := http.NewServeMux()
	if oauthServer != nil {
		oauthServer.RegisterPublic(mux)
	}

	// Health and robots.txt stay unauthenticated.
	mux.Handle("GET /healthz", handlers.Health())
	mux.Handle("GET /robots.txt", handlers.Robots())

	// the execution-permits design: the permit verification keys, public by design.
	if deps.permitIssuer != nil {
		mux.Handle("GET "+permit.JWKSPath, deps.permitIssuer.JWKSHandler())
	}

	// Device-authorization surface (the CLI-auth design, `kiff auth login`). The
	// start/token poll routes are unauthenticated (the CLI has no
	// credential yet) and mount on the public mux; the approve route is
	// registered on the authed sub-mux below. Only wired when a
	// developer-session store is present.
	var deviceAuth *handlers.DeviceAuthHandler
	if deps.devSessionStore != nil {
		// Approving a device needs a recent sign-in.
		opts := []handlers.DeviceAuthOption{handlers.WithDeviceLogger(deps.logger), handlers.WithDeviceStepUp(deps.stepUp)}
		if appSignIn != nil {
			// The tafs apps' token: the same session and device as this flow.
			var consents appsignin.ConsentStore = &appsignin.MemoryConsents{}
			if deps.pool != nil {
				consents = appsignin.NewPostgresConsents(deps.pool)
			}
			opts = append(opts, handlers.WithDeviceAppSignIn(appSignIn, consents))
		}
		if deps.deviceStore != nil {
			// A phone pairs a computer (the desktop-client design, §5).
			opts = append(opts, handlers.WithDeviceStore(deps.deviceStore), handlers.WithDevicePairings(buildPairingStore(deps.pool)),
				handlers.WithDevicePasskeys(deps.passkeys), handlers.WithDevicePasskeyRP(deps.passkeyRP))
			if deps.memberStore != nil {
				opts = append(opts, handlers.WithDevicePairingMembers(deps.memberStore))
			}
		}
		if deps.waitingSignIns != nil {
			opts = append(opts, handlers.WithDeviceWaitingSignIns(deps.waitingSignIns))
		}
		if deps.windows != nil {
			opts = append(opts, handlers.WithDeviceWindows(deps.windows))
		}
		if deps.appBaseURL != "" {
			opts = append(opts, handlers.WithDeviceVerificationURI(strings.TrimRight(deps.appBaseURL, "/")+"/auth/device"))
		}
		// A re-link tells the person by email, as other changes to who can
		// approve on their account do.
		if mn, ok := deps.customerNotifier.(notify.MembershipNotifier); ok && deps.customerNotifier != nil && deps.ownerEmail != nil {
			opts = append(opts, handlers.WithDeviceNotices(owneremail.NewClerkEmailLookup(), mn))
			if deps.tenantStore != nil {
				opts = append(opts, handlers.WithDeviceTenants(deps.tenantStore))
			}
		}
		// A device approved in the tafs app marks the browser sign-in that
		// approved it, so its new-device email says "tafs by KIFF".
		if deps.newDevices != nil {
			opts = append(opts, handlers.WithDeviceSignIns(deps.newDevices))
		}
		opts = append(opts, handlers.WithDeviceKeyRestriction(keyRestriction))
		deviceAuth = handlers.NewDeviceAuthHandler(deps.devSessionStore, opts...)
		deviceAuth.RegisterPublic(mux)
	}

	// Stripe billing webhook is unauthenticated by necessity —
	// Stripe cannot present a tenant identity — and secured instead
	// by Stripe-Signature verification inside the handler. Mounted on
	// the public mux (outside the /v1 auth middleware) only when a
	// signing secret is configured; nil handler Register is a no-op.
	handlers.NewStripeWebhookHandler(deps.billingProcessor, deps.stripeWebhookSecret, deps.logger).Register(mux)
	// The integration proofs' ingest (proofs) is unauthenticated by the
	// session middleware for the same reason: the proof runner is a GitHub
	// Actions job, trusted only by its OIDC token, checked in the handler.
	buildProofIngest(deps).Register(mux)
	handlers.NewProofStateHandler(deps.proofReader, splitList(os.Getenv(envProofStateTokens))).Register(mux)
	// The sign-in provider's webhook, the same way: its Svix signature is
	// the only trust (newdevice). Mounted only when its secret is set.
	if deps.newDevices != nil {
		mux.Handle("POST /webhooks/clerk", deps.newDevices)
	}
	// App Store Server Notifications, the same way: Apple's signature,
	// verified against the pinned Apple root, is the only trust.
	deps.appleHandler.RegisterPublic(mux)
	// Play's Real-time Developer Notifications, a Pub/Sub push: Google's
	// OIDC token, for the configured audience and service account, is the
	// only trust, and every purchase is read back from Play.
	if deps.googleHandler != nil {
		deps.googleHandler.RegisterPublic(mux)
	}

	// Authenticated routes register against a sub-mux so the auth
	// middleware does not wrap /healthz.
	authed := http.NewServeMux()
	// Who may use each agent, and a person's run grants, which decide
	// checks on each call of their run.
	accessHandler := handlers.NewAccessHandler(deps.accessStore, deps.memberStore, deps.agentStore, deps.logger).WithKeys(deps.apiKeyStore)
	if deps.ownerEmail != nil {
		accessHandler.WithEmails(owneremail.NewClerkEmailLookup())
	}
	// What of the account's record each person reads:
	// an admin all of it, anyone else their own use.
	var ownUse *handlers.OwnUse
	if deps.meter != nil {
		ownUse = handlers.NewOwnUse(accessHandler, deps.meter)
	}
	if deps.handoffs != nil {
		handlers.NewHandoffsHandler(deps.handoffs, ownUse, deps.handoffSweeper, deps.alertNotifier, deps.logger).Register(authed)
	}
	runGrants := handlers.NewRunGrants(deps.permitIssuer, accessHandler, deps.logger)
	// What the person talking to Coach may do: the permit KIFF issued for
	// Coach's tool call is the credential, so the route takes no bearer.
	if runGrants != nil && deps.permitTargets != nil && deps.signedRequests != nil {
		(&handlers.CoachAct{Grants: runGrants, Targets: deps.permitTargets, Used: deps.signedRequests}).Register(mux)
	}
	// One agent's recent runs in credits, for Coach's estimate of what a taf
	// will likely use: the same permit is the credential.
	if runGrants != nil && deps.permitTargets != nil && deps.meter != nil {
		(&handlers.CoachCredits{Grants: runGrants, Targets: deps.permitTargets, Runs: deps.meter}).Register(mux)
	}
	if deps.attentionStore != nil {
		ah := handlers.NewAttentionHandler(deps.attentionStore)
		// A Card warning in Home in the reader's language.
		if ws, ok := deps.mandateStore.(handlers.AttentionWarnings); ok {
			ah.WithAttentionWarnings(ws)
		}
		ah.Register(authed)
	}

	// Cloud-managed routes (key CRUD, wallet linking) live under
	// /v1/keys and /v1/me/* — both rooted on /v1 but distinct from
	// the framework's protocol surface.
	keysOpts := []auth.KeysHandlerOption{auth.WithKeysStepUp(deps.stepUp)}
	if deps.memberStore != nil {
		store := deps.memberStore
		keysOpts = append(keysOpts, auth.WithKeysMinterRemoved(func(ctx context.Context, tenantID, subject string) bool {
			m, err := store.Latest(ctx, tenantID, subject)
			return err == nil && m.Status == membership.StatusRemoved
		}))
	}
	auth.NewKeysHandler(deps.apiKeyStore, keysOpts...).Register(authed)
	if oauthServer != nil {
		oauthServer.Register(authed)
	}
	if appSignIn != nil {
		appSignIn.RegisterAuthenticated(authed)
	}
	// Signing in with a code sent by email: the web's two routes (from the
	// box only, before anyone is signed in) and the person's setting.
	codeH := codesignin.NewHandler(deps.codeSignIn, deps.stepUp, deps.logger)
	codeH.RegisterWeb(mux)
	codeH.RegisterAuthenticated(authed)

	// The people on an account: /v1/tenant/* behind the auth
	// middleware and the route policy, and the routes a person uses
	// before or across accounts behind WrapPerson (no tenant resolved).
	if memberSvc != nil {
		memberOpts := []handlers.MembersOption{
			handlers.WithMembersStepUp(deps.stepUp),
			handlers.WithMembersCredentials(deps.apiKeyStore, deps.devSessionStore),
			handlers.WithMembersLogger(deps.logger),
			handlers.WithMembersAppBaseURL(deps.appBaseURL),
			handlers.WithMembersAccess(deps.accessStore),
		}
		if deps.ownerEmail != nil {
			memberOpts = append(memberOpts, handlers.WithMembersEmails(owneremail.NewClerkEmailLookup()))
		}
		if mn, ok := deps.customerNotifier.(notify.MembershipNotifier); ok && deps.customerNotifier != nil {
			memberOpts = append(memberOpts, handlers.WithMembersNotifier(mn))
		}
		members = handlers.NewMembersHandler(memberSvc, deps.tenantStore, memberOpts...)
		members.Register(authed)
		personal := http.NewServeMux()
		members.RegisterPersonal(personal)
		// The person's own routes sit behind a middleware that copies the
		// request: Mark carries the route they match to the request log.
		mux.Handle("GET /v1/me/memberships", mw.WrapPerson(requestlog.Mark(personal)))
		mux.Handle("POST /v1/invites/preview", mw.WrapPerson(requestlog.Mark(personal)))
		mux.Handle("POST /v1/invites/accept", mw.WrapPerson(requestlog.Mark(personal)))
	}
	if deviceAuth != nil {
		deviceAuth.RegisterAuthenticated(authed)
	}
	// planque checks a person's device signature with KIFF.
	if deps.deviceStore != nil {
		var members auth.Memberships
		if memberSvc != nil {
			members = memberSvc
		}
		handlers.NewDeviceVerifyHandler(deps.deviceStore, members).WithSessions(deps.devSessionStore).WithPasskeys(deps.passkeys).WithWindows(deps.windows).WithApprovalChecks(deps.approvalChecks).Register(authed)
		handlers.NewDeviceSignatureHandler(deps.signedRequests).WithWindows(deps.windows).WithPasskeys(deps.passkeys, deps.passkeyRP).WithLogger(deps.logger).Register(authed)
		// The approval window (kiff/tafs#494): opened by the approval key,
		// read and closed by the phone.
		if deps.windows != nil {
			handlers.NewApprovalWindowHandler(deps.windows, deps.logger).WithPasskeys(deps.passkeys).Register(authed)
		}
		// Connection profiles: KIFF's words, and the owner's approval of
		// a profile, checked for the gateway (the connection-profiles design Appendix A).
		handlers.NewProfilesHandler(deps.deviceStore, members).WithWindows(deps.devSessionStore, deps.windows).WithPasskeys(deps.passkeys).Register(authed)
	}
	auth.NewWalletHandler(auth.NewWalletService(deps.walletNonceStore, deps.walletStore)).Register(authed)
	// Live event stream (SSE): GET /v1/me/stream. Pushes decision/
	// approval events to the dashboard so it updates without a reload.
	// nil broker → NewStreamHandler returns nil → Register is a no-op.
	handlers.NewStreamHandler(deps.broker, deps.logger).Register(authed)
	if deps.receiptStore != nil {
		handlers.NewReceiptsHandler(deps.receiptStore).Register(authed)
	}
	if deps.tenantStore != nil {
		var meOpts []handlers.MeOption
		if deps.ownerEmail != nil {
			// The person's email at /v1/me, read from Clerk as the members
			// handler reads it.
			meOpts = append(meOpts, handlers.WithMeEmails(owneremail.NewClerkEmailLookup(), deps.logger))
		}
		if deps.memberStore != nil {
			meOpts = append(meOpts, handlers.WithMeGovernance(deps.memberStore))
		}
		if deps.deviceStore != nil {
			meOpts = append(meOpts, handlers.WithMeDevices(deps.deviceStore))
		}
		handlers.NewMeHandler(deps.tenantStore, meOpts...).Register(authed)
		handlers.NewBillingPortalHandler(deps.tenantStore, deps.stripePortalClient, deps.appBaseURL, deps.logger).Register(authed)
		if deps.stripe != nil {
			var members handlers.MembersLister
			if deps.memberStore != nil {
				members = deps.memberStore
			}
			handlers.NewBillingCheckoutHandler(deps.tenantStore, deps.stripe, members, deps.appBaseURL, deps.logger).
				WithPacks(deps.packCatalog, deps.packRequests).Register(authed)
		}
		deps.appleHandler.Register(authed)
		if deps.googleHandler != nil {
			deps.googleHandler.Register(authed)
		}
		// planque says when a vendor signed a connection out; the account's
		// alert targets that list connection_signed_out hear of it.
		handlers.NewConnectionsHandler(deps.alertNotifier).Register(authed)
		if deps.packRequests != nil {
			requests := handlers.NewPackRequestsHandler(deps.tenantStore, deps.packRequests, deps.packCatalog, deps.logger)
			if deps.alertNotifier != nil {
				requests.WithAlerts(deps.alertNotifier)
			}
			if mn, ok := deps.customerNotifier.(notify.MembershipNotifier); ok && deps.customerNotifier != nil && deps.ownerEmail != nil && deps.memberStore != nil {
				requests.WithEmail(deps.memberStore, owneremail.NewClerkEmailLookup(), mn, deps.appBaseURL)
			}
			requests.Register(authed)
		}
		handlers.NewBillingBudgetHandler(deps.tenantStore, deps.logger).Register(authed)
		handlers.NewNotificationPrefsHandler(deps.tenantStore, deps.logger).Register(authed)
		// Self-serve data-subject-rights surface:
		// GET /v1/me/export. apiKeyStore / receiptStore may be nil
		// in a stripped smoke run; the bundle omits those sections.
		accountHandler := handlers.NewAccountHandler(deps.tenantStore, deps.apiKeyStore, deps.receiptStore).WithStepUp(deps.stepUp).WithSessions(deps.devSessionStore)
		// The Data Act's export: the account's Cards with their versions, and
		// every decision in its Record.
		if deps.mandateStore != nil {
			accountHandler.WithExportCards(deps.mandateStore)
		}
		if calls, ok := deps.decisionsReader.(proposals.CallsReader); ok {
			accountHandler.WithExportDecisions(calls)
		}
		accountHandler.Register(authed)
		accountHandler.RegisterPublic(mux)
		// Private support tickets: file/list/view/reply on a
		// tenant's own tickets. Tenant-scoped; operator triage is a
		// separate /v1/ops surface.
		if deps.supportStore != nil {
			handlers.NewSupportHandler(deps.supportStore, handlers.WithSupportLogger(deps.logger)).Register(authed)
		}
		// Outbound alert targets: register/list/delete the
		// webhooks fired on consequential decisions. Tenant-scoped.
		if deps.alertStore != nil {
			handlers.NewAlertTargetsHandler(deps.alertStore, deps.allowPrivateWebhooks).WithPhoneURLs(deps.phoneAlertURLs).Register(authed)
		}
		// Per-control proof-pack export: GET
		// /v1/me/proof-pack/{action}. Assembles contract + state +
		// policy + decisions + signed receipts for one control over
		// a window, from the read stores already wired. Optional
		// stores degrade gracefully (sections omitted, not errors).
		handlers.NewProofPackHandler(
			deps.tenantStore, deps.controlStore, deps.domainSource,
			deps.executorRegistry, deps.decisionsReader, deps.receiptStore,
		).Register(authed)
	}
	if deps.domainSource != nil && deps.executorRegistry != nil {
		handlers.NewTemplatesHandler(deps.domainSource, deps.executorRegistry, deps.runtimeResetter,
			handlers.WithTemplatesLogger(deps.logger),
			handlers.WithTemplatesControlStore(deps.controlStore),
		).Register(authed)
		handlers.NewDomainHandler(deps.domainSource, deps.executorRegistry, deps.runtimeResetter,
			handlers.WithDomainLogger(deps.logger),
			handlers.WithDomainControlStore(deps.controlStore),
		).Register(authed)
		// Domain-apex read projection: GET
		// /v1/me/domains and /v1/me/domains/{domain}. Read-only; the
		// control store supplies the evidence entry point + activation,
		// the decisions reader powers the observed-agents section.
		handlers.NewDomainsHandler(deps.domainSource, deps.executorRegistry,
			handlers.WithDomainsLogger(deps.logger),
			handlers.WithDomainsControlStore(deps.controlStore),
			handlers.WithDomainsDecisionsReader(deps.decisionsReader),
			// POST /v1/me/domains creates an additional domain; reset
			// the tenant's runtime cache so the resolver sees it.
			handlers.WithDomainsRuntimeResetter(deps.runtimeResetter),
			// A connector key removes only the tools it connected.
			handlers.WithDomainsGatewayOwners(gatewayOwners(deps)),
			// Enforce the per-plan domain cap at create.
			handlers.WithDomainsMaxResolver(func(ctx context.Context, tenantID string) int {
				if deps.tenantStore == nil {
					return 0
				}
				t, err := deps.tenantStore.Get(ctx, tenantID)
				if err != nil {
					return 0 // allow-when-uncertain; the limiter is a guardrail, not a gate
				}
				return tenant.PlanFor(t).MaxDomains
			}),
		).Register(authed)
		if deps.draftSource != nil {
			handlers.NewDraftHandler(deps.domainSource, deps.draftSource, deps.executorRegistry, deps.runtimeResetter, deps.pool,
				handlers.WithDraftLogger(deps.logger),
				handlers.WithDraftControlStore(deps.controlStore),
			).Register(authed)
		}
		if deps.controlStore != nil {
			handlers.NewControlsHandler(deps.controlStore, deps.logger,
				// Control-scoped decision timeline. Reader powers
				// the windowed query/counts; tenant store resolves the
				// governance mode for the control header.
				handlers.WithControlsDecisionsReader(deps.decisionsReader),
				handlers.WithControlsTenantStore(deps.tenantStore),
				handlers.WithControlsPostureReader(deps.postureReader),
				handlers.WithControlsReceipts(deps.receiptStore),
				handlers.WithControlsAlertDeliveries(deps.alertDeliveries),
				handlers.WithControlsToolOutcomes(toolOutcomes(deps)),
				handlers.WithControlsReleases(releasedProposals(deps)),
				handlers.WithControlsCallAbout(deps.callAbout),
				handlers.WithControlsToolTitles(gatewayTitles(deps)),
				handlers.WithControlsHoldAnswers(holdExceptions(deps), holdAnswers(deps)),
				// The owner's halts and resumes on the Record.
				handlers.WithControlsHalts(deps.halts),
				// Which payments were reported, on their receipts.
				handlers.WithControlsPaymentReports(deps.payReports),
				handlers.WithControlsOwnUse(ownUse),
			).Register(authed)
		}
	}

	// Per-tenant runtime — the framework's KIFF protocol surface
	// (events / entities / approvals). Mounted only when a runtime
	// resolver was wired (production has one; the integration
	// test in main_test.go can pass nil to skip).
	// The proposals handler, so the mandates handler can later say a held call in words.
	var proposalsHandler *handlers.ProposalsHandler
	if deps.tenantRuntime != nil {
		runtimeHandler := handlers.NewTenantRuntimeHandler(deps.tenantRuntime, "/v1", deps.logger)
		// So a management review from the tenant-wide queue reaches the
		// runtime that owns the approval, not the tenant default.
		if deps.approvalDomains != nil {
			runtimeHandler = runtimeHandler.WithApprovalDomains(deps.approvalDomains)
		}
		if deps.memberStore != nil {
			runtimeHandler = runtimeHandler.WithHoldGate(deps.memberStore).WithHoldGateAlert(holdAlert)
		}
		if deps.postExecuteHook != nil {
			runtimeHandler.WithPostExecuteHook(deps.postExecuteHook)
		}
		if deps.blockedHook != nil {
			runtimeHandler.WithBlockedExecuteHook(deps.blockedHook)
		}
		authed.Handle("POST /v1/events/raw", runtimeHandler)
		authed.Handle("/v1/entities/", runtimeHandler)
		authed.Handle("/v1/approvals/", runtimeHandler)

		// Pre-execution proposal gate. Lives on /v1/proposals
		// so it sits beside but distinct from the framework's
		// protocol surface — KIFF is the decider, the customer
		// remains the executor.
		propOpts := []handlers.ProposalsOption{
			handlers.WithProposalsLogger(deps.logger),
			handlers.WithProposalsCallAbout(deps.callAbout),
		}
		if deps.callAbout != nil {
			var holds mandate.TafWordsEraser
			if e, ok := deps.mandateStore.(mandate.TafWordsEraser); ok {
				holds = e
			}
			handlers.NewCallWordsHandler(deps.callAbout, holds, deps.logger).Register(authed)
		}
		if deps.usageAgg != nil {
			// Cloud-side proposal counters land in
			// cloud_meta.tenant_usage via the same aggregator the
			// framework runtime counters use.
			propOpts = append(propOpts, handlers.WithProposalsMetrics(deps.usageAgg.RecorderForDomain))
		}
		if deps.limiter != nil {
			propOpts = append(propOpts, handlers.WithProposalsLimiter(deps.limiter))
		}
		// Billable governed-operation ledger: one idempotent
		// row per governed operation, the source the Stripe
		// meter-events reporter will drain.
		if deps.billingLedger != nil {
			propOpts = append(propOpts, handlers.WithProposalsUsageLedger(deps.billingLedger))
		}
		if deps.decisionsReader != nil {
			propOpts = append(propOpts, handlers.WithProposalsReader(deps.decisionsReader))
		}
		if deps.alertNotifier != nil {
			propOpts = append(propOpts, handlers.WithProposalsAlerter(deps.alertNotifier))
			// A held call's alert says whether it moves money, from its
			// tool's owner-approved profile (alert.Hold.MoneyOut).
			if deps.pool != nil {
				propOpts = append(propOpts, handlers.WithProposalsToolProfiles(tooloutcome.NewPostgres(deps.pool)))
			}
		}
		// Live decision events → dashboard SSE (best-effort; nil-safe).
		if deps.broker != nil {
			propOpts = append(propOpts, handlers.WithProposalsEventPublisher(deps.broker))
		}
		// Erasing-tenant guard: reject decide calls for
		// tenants in deleted_pending or deleted state.
		if deps.tenantStore != nil {
			propOpts = append(propOpts, handlers.WithProposalsTenantStore(deps.tenantStore))
		}
		// Control identity: stamp the governing
		// control's id + environment on each decision so the
		// window-native query and the evidence object can
		// scope to one control.
		if deps.controlStore != nil {
			propOpts = append(propOpts, handlers.WithProposalsControlStore(deps.controlStore))
		}
		// Atomic idempotency: a per-(tenant, key) advisory
		// lock serializes concurrent retries of one key so they
		// collapse to one decision. The lock pool is dedicated
		// (openLockPool) — never the stores'/limiter's pool — so
		// the lock-held critical section can't starve the
		// limiter's own connection and deadlock.
		if deps.mandateStore != nil {
			var locker mandate.Locker
			if deps.lockPool != nil {
				locker = idempotency.NewAdvisoryLocker(deps.lockPool)
			}
			propOpts = append(propOpts, handlers.WithProposalsMandates(
				mandate.NewAuthorizer(deps.mandateStore, locker, nil)))
			// The one ask per agent, action and unit for calls whose Card
			// has no limit in their unit (kiff/tafs#464 item 11).
			if asks, ok := deps.mandateStore.(handlers.CardUnitAsks); ok {
				propOpts = append(propOpts, handlers.WithProposalsCardUnitAsks(asks))
			}
			// A sealed fill's release, kept from the owner's allow, goes out
			// with the decision that sends the call.
			if releases, ok := deps.mandateStore.(mandate.SealedReleases); ok {
				propOpts = append(propOpts, handlers.WithProposalsSealedReleases(releases))
			}
			if blocks, ok := deps.mandateStore.(mandate.BrowserBlocks); ok {
				propOpts = append(propOpts, handlers.WithProposalsBrowserBlocks(blocks))
			}
			// the cards-for-any-agent design: the owner is emailed about each newly held
			// action, when Resend and owner-email resolution are both
			// configured (as for the ops notifications).
			if deps.ownerEmail != nil && deps.customerNotifier != nil {
				propOpts = append(propOpts, handlers.WithProposalsHoldEmail(deps.ownerEmail, deps.customerNotifier, deps.appBaseURL))
			}
		}
		if deps.lockPool != nil {
			propOpts = append(propOpts, handlers.WithProposalsLocker(idempotency.NewAdvisoryLocker(deps.lockPool)))
		}
		if deps.permitIssuer != nil {
			propOpts = append(propOpts, handlers.WithProposalsPermits(deps.permitIssuer))
		}
		if runGrants != nil {
			propOpts = append(propOpts, handlers.WithProposalsRunGrants(runGrants))
		}
		if deps.meter != nil {
			propOpts = append(propOpts, handlers.WithProposalsCallRequesters(deps.meter))
		}
		if deps.permitTargets != nil {
			propOpts = append(propOpts, handlers.WithProposalsPermitTargets(deps.permitTargets))
		}
		// Mounted whether or not permits are on, so connecting a sealed
		// tool never depends on it; with permits off nothing reads it.
		if deps.permitTargets != nil {
			handlers.NewPermitTargetsHandler(deps.permitTargets, deps.logger).WithGatewayOwners(gatewayOwners(deps)).Register(authed)
		}
		// A gateway trial against a revision draft's Cards reads the draft
		// (kiff/tafs#107); without it every such trial is refused.
		if deps.agentDrafts != nil {
			propOpts = append(propOpts, handlers.WithProposalsAgentDrafts(deps.agentDrafts))
		}
		// A halted agent's calls are refused, and after a resume the ones
		// asked before it.
		if deps.halts != nil {
			propOpts = append(propOpts, handlers.WithProposalsHalts(deps.halts))
			haltsHandler := handlers.NewHaltsHandler(deps.halts, deps.planques, deps.receiptStore, deps.logger).WithWindows(deps.windows)
			haltsHandler.Register(authed)
			// "Report this payment": halts the payment's taf, through the same halt.
			if calls := callsReader(deps.decisionsReader); calls != nil && deps.payReports != nil && deps.receiptStore != nil && deps.pool != nil {
				handlers.NewPaymentReportsHandler(deps.receiptStore, calls, tooloutcome.NewPostgres(deps.pool), toolOutcomes(deps),
					deps.payReports, haltsHandler, deps.logger).Register(authed)
			}
		}
		if deps.meter != nil && deps.tenantStore != nil {
			propOpts = append(propOpts, handlers.WithProposalsMeterLimits(meter.NewLimits(deps.tenantStore, deps.meter)))
		}
		proposalsHandler = handlers.NewProposalsHandler(deps.tenantRuntime, propOpts...)
		proposalsHandler.Register(authed)

		// Tenant-wide list-pending-approvals.
		// Sits beside the framework's approval-review surface (the
		// /v1/approvals/{id}/grant + /deny routes routed via
		// runtimeHandler above). Literal-path pattern matches first
		// in Go 1.22+ so /v1/approvals (no trailing slash) lands
		// here, /v1/approvals/{id}/grant continues to flow through
		// the framework handler.
		handlers.NewApprovalsListHandler(deps.tenantRuntime, deps.approvalReader, deps.logger).Register(authed)

		// Read-only governed lifecycle view for a single entity
		// (Slice 4). Assembles the entity's audit + decision +
		// approval history via the framework's lifecycle projection.
		// Its specific pattern (GET /v1/entities/{id}/lifecycle) takes
		// Go 1.22+ precedence over the framework's /v1/entities/
		// subtree, so execute/approvals/timeline keep flowing through
		// the framework handler.
		handlers.NewEntityLifecycleHandler(deps.tenantRuntime, deps.logger).Register(authed)
	}

	// Tenant-facing usage summary. Always mounted when a
	// usage store is wired: tenants need to see their own counters
	// even before any proposal arrives so the activation path is
	// observable. The flusher drains the in-memory aggregator
	// before each snapshot read so counters from the same request
	// window are visible (the alternative was a 30s lag, which
	// breaks the activation read-after-write expectation and
	// would also break plan-limit enforcement).
	if deps.usageStore != nil {
		usageOpts := []handlers.UsageOption{handlers.WithUsageLogger(deps.logger)}
		if deps.tenantStore != nil {
			usageOpts = append(usageOpts, handlers.WithUsageTenantStore(deps.tenantStore))
		}
		if deps.usageAgg != nil {
			usageOpts = append(usageOpts, handlers.WithUsageFlusher(deps.usageAgg))
		}
		if uh := handlers.NewUsageHandler(deps.usageStore, usageOpts...); uh != nil {
			uh.Register(authed)
		}
	}
	if deps.meter != nil && deps.tenantStore != nil {
		meterUsage := handlers.NewMeterUsageHandler(deps.tenantStore, deps.meter, deps.logger).WithToolTitles(gatewayTitles(deps)).WithOwnUse(ownUse)
		if deps.agentStore != nil {
			meterUsage.WithAgentNames(deps.agentStore)
		}
		if deps.packLedger != nil {
			meterUsage.WithPacks(deps.packLedger, deps.packCatalog).WithRequests(deps.packRequests)
		}
		if deps.appleAccounts != nil {
			meterUsage.WithApple(deps.appleAccounts)
		}
		if deps.googleBilling {
			meterUsage.WithGoogle()
		}
		if deps.accessStore != nil {
			meterUsage.WithGroups(deps.accessStore)
		}
		if deps.memberStore != nil {
			meterUsage.WithPeople(deps.memberStore)
		}
		meterUsage.Register(authed)
		// The credits each run of a taf or of Coach used: kept from its
		// runtime's report, read as the tasks are.
		credits := handlers.NewCreditsHandler(deps.tenantStore, deps.meter, deps.logger).WithOwnUse(ownUse).
			WithCatalog(deps.packCatalog).WithRequests(deps.packRequests, deps.alertNotifier)
		if deps.creditLedger != nil {
			credits.WithCreditPacks(deps.creditLedger)
			// Both ledgers say what an account bought: a pack sold once
			// per account is offered until then.
			if b, ok := deps.creditLedger.(packs.BoughtRead); ok {
				credits.WithBought(b)
				meterUsage.WithBought(b)
			}
		}
		if deps.agentStore != nil {
			credits.WithAgentNames(deps.agentStore)
		}
		credits.Register(authed)
	}

	if deps.mandateStore != nil {
		mandatesHandler := handlers.NewMandatesHandler(deps.mandateStore, deps.domainSource,
			handlers.WithMandatesTenants(deps.tenantStore),
			handlers.WithMandatesLogger(deps.logger),
			handlers.WithMandatesAgents(deps.agentStore),
			handlers.WithMandatesAccess(deps.accessStore),
			handlers.WithMandatesMemberships(deps.memberStore),
			handlers.WithMandatesHoldAlert(holdAlert),
			handlers.WithMandatesCardChangeNotice(members.NoticeCardChange),
			handlers.WithMandatesToolOutcomes(toolOutcomes(deps)),
			handlers.WithMandatesToolTitles(gatewayTitles(deps)),
			handlers.WithMandatesProofTestKeys(deps.proofTestKeys, deps.proofTestTenants),
			handlers.WithMandatesPermitTargets(deps.permitTargets),
			handlers.WithMandatesAlerts(deps.alertNotifier),
			handlers.WithMandatesAgentDrafts(deps.agentDrafts, deps.apiKeyStore),
			handlers.WithMandatesCalls(callsReader(deps.decisionsReader)),
			handlers.WithMandatesSetupReads(setupReadDeps(deps, memberSvc)),
			handlers.WithMandatesApprovalWindows(deps.devSessionStore, deps.windows),
			handlers.WithMandatesApprovalPasskeys(deps.passkeys),
			handlers.WithMandatesApprovalChecks(deps.approvalChecks),
			handlers.WithMandatesPasskeys(deps.passkeys, deps.passkeyRP),
			handlers.WithMandatesOwnUse(ownUse),
			handlers.WithMandatesAppBaseURL(deps.appBaseURL),
		)
		mandatesHandler.Register(authed)
		mandatesHandler.RegisterExternalApprover(mux)
		mandatesHandler.RegisterAgentDraftClaim(mux)
		// The held-action email says the call as the apps do.
		if proposalsHandler != nil {
			proposalsHandler.SetHeldWords(mandatesHandler)
		}
	}

	handlers.NewAgentsHandler(deps.agentStore,
		handlers.WithAgentsSources(deps.mandateStore, deps.apiKeyStore, deps.guardStore),
		handlers.WithAgentsAccess(deps.accessStore),
		handlers.WithAgentsUnshare(accessHandler.RemoveAgentGrants),
		handlers.WithAgentsLogger(deps.logger),
	).Register(authed)
	accessHandler.Register(authed)
	if runGrants != nil {
		runGrants.Register(authed)
		// Reading another person's work goes through KIFF, recorded first.
		(&handlers.Oversight{Grants: runGrants}).Register(authed)
	}

	if deps.guardStore != nil {
		handlers.NewGuardHandler(deps.guardStore,
			handlers.WithGuardLogger(deps.logger),
		).Register(authed)
	}

	// Governed-surface coverage (GET /v1/guard/coverage). Joins the
	// tool catalog connected runtimes push against the tenant's
	// activated controls, so "what can our agents do that nothing
	// governs?" is answerable before any control is authored. The
	// decisions reader is optional and adds the drift join; without it
	// the report is coverage-only and says so. NewCoverageHandler
	// returns nil unless both required stores are wired, so the route
	// is absent rather than half-informed.
	if deps.guardStore != nil && deps.controlStore != nil {
		covOpts := []handlers.CoverageOption{handlers.WithCoverageLogger(deps.logger)}
		if deps.decisionsReader != nil {
			covOpts = append(covOpts, handlers.WithCoverageDecisions(deps.decisionsReader))
		}
		handlers.NewCoverageHandler(deps.guardStore, deps.controlStore, covOpts...).Register(authed)
	}

	// What share of an agent's proposed actions needed nobody (the
	// review ratio). Read-only, and it runs no query the system did
	// not already run — the same window-complete per-outcome count the
	// Control Room header reads. NewReviewRatioHandler returns nil
	// without a decisions reader, so the route is absent rather than
	// answering from nothing.
	if deps.decisionsReader != nil {
		handlers.NewReviewRatioHandler(deps.decisionsReader,
			handlers.WithReviewRatioLogger(deps.logger),
			handlers.WithReviewRatioTenants(deps.tenantStore),
		).Register(authed)
	}

	// Operator console cross-tenant read surface +
	// control-plane mutations. Mounted only when the
	// operator allow-list is non-empty and the required stores are
	// wired; NewOpsHandler returns nil otherwise and Register is a
	// no-op. The handler re-checks the allow-list per request (the API
	// gate is the real boundary) and returns 404 to non-operators.
	// WithOpsControlPlane adds suspend/unsuspend + the audit log when
	// the operator-audit store is available.
	opsOpts := []handlers.OpsOption{}
	if deps.operatorAudit != nil {
		opsOpts = append(opsOpts, handlers.WithOpsControlPlane(deps.operatorAudit, deps.runtimeResetter))
		if deps.creditLedger != nil {
			// Bonus credits: audited like the other mutations.
			opsOpts = append(opsOpts, handlers.WithOpsBonusCredits(deps.creditLedger))
		}
	}
	if deps.operatorAlerts != nil {
		opsOpts = append(opsOpts, handlers.WithOpsAlertQueue(deps.operatorAlerts))
	}
	// Atomic state-change+audit mutator. Postgres-backed when
	// a pool is available; otherwise the handler defaults to its
	// in-memory mutator (same no-mutation-without-audit guarantee).
	if deps.pool != nil {
		opsOpts = append(opsOpts, handlers.WithOpsMutator(handlers.NewPostgresOpsMutator(deps.pool)))
	}
	// Customer notifications wired only when both the resolver and a
	// real notifier exist (Resend configured) — avoids pointless Clerk
	// lookups when email is disabled.
	if deps.ownerEmail != nil && deps.customerNotifier != nil {
		opsOpts = append(opsOpts, handlers.WithOpsNotifications(deps.ownerEmail, deps.customerNotifier, deps.logger))
	}
	// Support-ticket triage: operators read/answer/close tickets
	// from the console when a support store is wired.
	if deps.supportStore != nil {
		opsOpts = append(opsOpts, handlers.WithOpsSupport(deps.supportStore, deps.appBaseURL))
	}
	// Each account's API keys by prefix, and its runs and credits from the
	// run ledger, with the consumption table (ops round, batch 1).
	var opsRuns handlers.OpsRunDays
	if deps.meter != nil {
		opsRuns = deps.meter
	}
	opsOpts = append(opsOpts, handlers.WithOpsAccounts(deps.apiKeyStore, opsRuns))
	// The integration proofs' monitoring page and replay. The events'
	// reader (the private reports bucket) isn't wired yet, so the replay
	// shows each run's summary and checks.
	opsOpts = append(opsOpts, handlers.WithOpsProofs(deps.proofStore, nil, proofReleaseAnyway()),
		handlers.WithOpsProofTestKeys(deps.proofTestKeys))
	handlers.NewOpsHandler(
		deps.operatorSubjects,
		deps.tenantStore, deps.usageStore, deps.receiptStore,
		deps.decisionsReader, deps.postureReader, deps.alertDeliveries,
		opsOpts...,
	).Register(authed)

	// the governance-roles design's capability table sits between authentication and every
	// handler: a person's role is checked for each route before the
	// handler runs. Keys pass through to their handlers' own checks.
	policy := auth.DefaultRoutePolicy()
	if deps.windows != nil {
		policy.WithWindows(deps.windows)
	}
	if deps.signedRequests != nil {
		policy.WithSignedRequests(deps.signedRequests)
	}
	if deps.passkeys != nil {
		policy.WithPasskeys(deps.passkeys, deps.passkeyRP)
	}
	// Mark: the route authed matches reaches the request log, through the
	// middlewares that copy the request.
	policed := policy.Wrap(requestlog.Mark(authed))
	// Each signed-in person's app language, kept for the emails KIFF
	// writes them later (personlang; kiff/tafs/docs/languages.md).
	if deps.tenantStore != nil {
		policed = (&personlang.Recorder{Tenants: deps.tenantStore, Logger: deps.logger}).Wrap(policed)
	}
	mux.Handle("/v1/", mw.Wrap(policed))
	mux.Handle("/v1", mw.Wrap(policed))

	return mux
}

func buildGuardStore(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) (guard.Store, error) {
	if pool == nil {
		logger.Info("guard connection store: in-memory mode (no Postgres); connections reset on restart")
		return guard.NewMemoryStore(), nil
	}
	if err := guard.ApplySchema(ctx, pool); err != nil {
		return nil, fmt.Errorf("apply guard connections schema: %w", err)
	}
	logger.Info("guard connection store: Postgres-backed (cloud_meta.guard_connections)")
	return guard.NewPostgresStore(pool), nil
}

// usageAllowanceReader adapts the usage Store to
// billing.AllowanceUsageReader: it sums the tenant's
// kiff.cloud.proposals.allowed counter for the current UTC month —
// the same measure the proposal limiter compares the plan allowance
// against (the consumption model amendment) — so the alert reflects
// exactly the allowance the tenant is metered on. Only `allowed`
// (productive usage) counts; blocked / approval_required / invalid
// do not consume the allowance, matching the billed unit.
type usageAllowanceReader struct {
	store usage.Store
	now   func() time.Time
}

func (r usageAllowanceReader) MonthlyOperations(ctx context.Context, tenantID string) (int, error) {
	now := r.now().UTC()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	rows, err := r.store.Snapshot(ctx, usage.Filter{
		TenantID:    tenantID,
		CounterName: usage.CloudCounterProposalsAllowed,
		HourFrom:    from,
	})
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, row := range rows {
		total += row.Count
	}
	return int(total), nil
}

// buildUsageStore returns the cloud's tenant_usage Store: Postgres
// when pool is set (production), in-memory otherwise (tests / dev).
// The schema is applied here so missing migrations don't surface
// as a query failure on the first request.
func buildUsageStore(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) (usage.Store, func(), error) {
	if pool == nil {
		logger.Info("usage store: in-memory mode (no Postgres); counters reset on restart")
		return usage.NewMemoryStore(), func() {}, nil
	}
	if err := usage.ApplySchema(ctx, pool); err != nil {
		return nil, func() {}, fmt.Errorf("apply tenant_usage schema: %w", err)
	}
	logger.Info("usage store: Postgres-backed (cloud_meta.tenant_usage)")
	return usage.NewPostgresStore(pool), func() {}, nil
}

// buildTenantRuntime returns a tenant-runtime accessor and a close
// function. When pool is non-nil, the resolver is the production
// CloudResolver (Postgres-backed, per-tenant search_path pinning).
// When pool is nil, the resolver is the in-memory MemoryResolver
// — the binary still serves the runtime surface, but tenants must
// buildTenantRuntime returns the tenant-runtime accessor, the
// tenant store, the domain source, the executor registry, and a
// close function. The caller wires the runtime accessor into the
// runtime handler, the tenant store into the /v1/me handler, and
// the source + registry into the templates handler. When pool is
// non-nil, the resolver is the production CloudResolver
// (Postgres-backed). When pool is nil, the resolver is the
// in-memory MemoryResolver.
func buildTenantRuntime(ctx context.Context, pool *pgxpool.Pool, databaseURL string, logger *slog.Logger) (*cloudruntime.TenantRuntime, tenant.Store, domain.Source, *executors.Registry, func(), error) {
	registry := executors.DefaultRegistry()

	if pool == nil {
		// In-memory mode. Tenants and domains live in-process; the
		// binary is useful for the integration test and the
		// "no-Postgres smoke" path. No data is seeded here — that
		// is the caller's job (or the test's).
		store := tenant.NewMemoryStore()
		source := domain.NewMemorySource()
		resolver := cloudruntime.NewMemoryResolver(store, source, registry)
		logger.Info("tenant runtime: in-memory mode; seed via tenant.MemoryStore + domain.MemorySource")
		return cloudruntime.New(resolver), store, source, registry, func() {}, nil
	}

	// Postgres-backed mode. The same pool is used for cloud_meta
	// reads; per-tenant pools (search_path = kiff_t_<id>) are
	// opened lazily by CloudResolver.
	if err := domain.ApplyTenantDomainsSchema(ctx, pool); err != nil {
		return nil, nil, nil, nil, func() {}, fmt.Errorf("apply tenant_domains: %w", err)
	}
	store := tenant.NewPostgresStore(pool)
	source := domain.NewPostgresSource(pool)
	resolver := cloudruntime.NewCloudResolver(databaseURL, store, source, registry, nil)
	closer := func() { resolver.Close() }
	logger.Info("tenant runtime: Postgres-backed; CloudResolver active")
	return cloudruntime.New(resolver), store, source, registry, closer, nil
}

// openPool dials Postgres if a URL is provided, applies the cloud's
// schemas, and returns the pool plus a close function. When no URL
// is set, returns (nil, no-op closer, nil) — callers fall back to
// in-memory stores.
func openPool(ctx context.Context, url string, logger *slog.Logger) (*pgxpool.Pool, func(), error) {
	if url == "" {
		logger.Warn("running with in-memory stores; data will not survive restart",
			slog.String("hint", "set "+envDatabaseURL+" to enable Postgres-backed persistence"),
		)
		return nil, func() {}, nil
	}
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(dialCtx, url)
	if err != nil {
		return nil, nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(dialCtx); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("ping postgres: %w", err)
	}
	if err := tenant.ApplyCloudMeta(dialCtx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("apply cloud_meta: %w", err)
	}
	// Backfill redaction_salt on any pre-existing tenants.
	// Idempotent — rerunning on a fully-backfilled table is a
	// no-op. The legacy receipts those tenants anchored before the
	// salt landed remain verifiable against their unsalted leaves;
	// any *new* anchor under the same tenant uses the salted form.
	//
	// Precondition: the redaction_salt column must exist on
	// cloud_meta.tenants. ApplyCloudMeta above guarantees that
	// (the ALTER TABLE ... ADD COLUMN IF NOT EXISTS clause is
	// part of the embedded SQL). Any future schema work that
	// ALTERs cloud_meta.tenants must run before this call, not
	// between this call and ApplyCloudMeta.
	if updated, err := tenant.BackfillRedactionSalts(dialCtx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("backfill redaction salts: %w", err)
	} else if updated > 0 {
		logger.Info("redaction salt backfill",
			slog.Int("tenants_updated", updated),
		)
	}
	if err := auth.ApplyAPIKeysSchema(dialCtx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("apply api_keys: %w", err)
	}
	if n, err := auth.BackfillSetupReader(dialCtx, pool); err != nil {
		pool.Close()
		return nil, nil, err
	} else if n > 0 {
		logger.Info("setup_reader backfill", slog.Int64("keys", n))
	}
	if err := auth.ApplyDeveloperSessionsSchema(dialCtx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("apply developer_sessions: %w", err)
	}
	if err := auth.ApplyDevicesSchema(dialCtx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("apply approval_devices: %w", err)
	}
	if err := appsignin.ApplyConsentsSchema(dialCtx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("apply app_terms_consents: %w", err)
	}
	if err := codesignin.ApplySchema(dialCtx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("apply code sign-in: %w", err)
	}
	if err := auth.ApplyPairingsSchema(dialCtx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("apply pairings: %w", err)
	}
	if err := auth.ApplyPasskeysSchema(dialCtx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("apply passkeys: %w", err)
	}
	if err := auth.ApplyDeviceSignInsSchema(dialCtx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("apply device_sign_ins: %w", err)
	}
	// Phones signed in before get their device record (idempotent).
	if n, err := auth.LinkLegacyDevices(dialCtx, pool, time.Now()); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("link phones to their devices: %w", err)
	} else if n > 0 {
		slog.Default().Info("devices: linked phones signed in before device names", slog.Int("count", n))
	}
	if err := auth.ApplySignedRequestsSchema(dialCtx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("apply signed_requests: %w", err)
	}
	if err := auth.ApplyWindowsSchema(dialCtx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("apply approval_windows: %w", err)
	}
	if err := auth.ApplyApprovalChecksSchema(dialCtx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("apply approval_checks: %w", err)
	}
	if err := oauth.ApplySchema(dialCtx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("apply oauth: %w", err)
	}
	if err := auth.ApplyWalletsSchema(dialCtx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("apply wallets: %w", err)
	}
	if err := receipt.ApplySchema(dialCtx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("apply receipts: %w", err)
	}
	return pool, func() { pool.Close() }, nil
}

// openLockPool builds the dedicated pool the proposal idempotency
// advisory lock runs on. It is separate from the main
// cloud_meta pool so a lock held across the dedup+limit+evaluate
// critical section can never starve the limiter/stores of a
// connection on the main pool — the deadlock the review
// caught. These connections only execute pg_advisory_lock /
// pg_advisory_unlock, so the pool is small; MaxConns bounds how
// many distinct keys can hold or block on a lock at once (excess
// callers queue in Go for a lock-pool connection rather than
// deadlocking). Returns (nil, no-op, nil) when no database URL is
// set so the binary's in-memory mode skips the lock entirely.
func openLockPool(ctx context.Context, url string) (*pgxpool.Pool, func(), error) {
	if url == "" {
		return nil, func() {}, nil
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, nil, fmt.Errorf("parse lock pool config: %w", err)
	}
	cfg.MaxConns = 16
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(dialCtx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("connect lock pool: %w", err)
	}
	if err := pool.Ping(dialCtx); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("ping lock pool: %w", err)
	}
	return pool, func() { pool.Close() }, nil
} // buildStores returns concrete API-key + wallet stores, either
// Postgres-backed (when pool != nil) or in-memory.
// buildDeveloperSessionStore returns the Postgres-backed developer-
// session store in production (durable + per-device revocable) or the
// in-memory store when there is no pool (local/dev/tests). The
// developer_sessions schema is applied by the pool opener.
// buildOAuthServer is the gateway-OAuth design's authorization server, or nil when there
// is no database or no consent page origin.
func buildOAuthServer(deps routerDeps, app *appsignin.Handler) *oauth.Server {
	if deps.pool == nil || deps.appBaseURL == "" {
		return nil
	}
	cfg := oauth.Config{
		Issuer:        envOr(envOAuthIssuer, "https://api.kiff.dev"),
		Resource:      envOr(envOAuthResource, "https://mcp.kiff.dev/mcp"),
		ConsentURL:    strings.TrimRight(deps.appBaseURL, "/") + "/oauth/consent",
		RedirectHosts: splitHosts(envOr(envOAuthRedirectHosts, "claude.ai,claude.com,chatgpt.com")),
		// Claude identifies itself with a document on claude.ai, so it
		// never registers (CIMD).
		MetadataHosts: splitHosts(envOr(envOAuthMetadataHosts, "claude.ai")),
		// Anthropic's outbound range, should Claude fall back to registering.
		SharedNets: splitNets(deps.logger, envOr(envOAuthSharedNets, "160.79.104.0/21")),
	}
	opts := []oauth.Option{oauth.WithLogger(deps.logger)}
	if app != nil {
		opts = append(opts, oauth.WithAppSignIn(appsignin.ClientID, app.Authorize))
	}
	// Consent binds the key only to an agent the account has:
	// the same agents GET /v1/me/agents shows.
	if known := handlers.NewAgentsHandler(deps.agentStore,
		handlers.WithAgentsSources(deps.mandateStore, deps.apiKeyStore, deps.guardStore),
		handlers.WithAgentsLogger(deps.logger)); known != nil {
		opts = append(opts, oauth.WithAgentCheck(known.Known))
		// "A new agent for this app" (Amendment 1, A5): refuse any name the
		// account already uses, recorded or implicit, then record it.
		if store := deps.agentStore; store != nil {
			opts = append(opts, oauth.WithAgentCreator(func(ctx context.Context, tenantID, agentID, createdBy string) error {
				if used, err := known.Known(ctx, tenantID, agentID); err != nil {
					return err
				} else if used {
					return oauth.ErrAgentExists
				}
				err := store.Create(ctx, agents.Agent{TenantID: tenantID, ID: agentID, CreatedAt: time.Now().UTC(), CreatedBy: createdBy})
				if errors.Is(err, agents.ErrExists) {
					return oauth.ErrAgentExists
				}
				return err
			}))
		}
	}
	if deps.memberStore != nil {
		opts = append(opts, oauth.WithGranterActive(oauth.MemberGranterActive(deps.memberStore.Latest)))
	}
	return oauth.New(oauth.NewStore(deps.pool), deps.apiKeyStore, cfg, opts...)
}

// buildAppSignIn is the tafs apps' sign-in (package appsignin), or nil with
// no web origin for its page. Off unless KIFF_APP_SIGNIN is "on", or "test"
// for the tenants in KIFF_APP_SIGNIN_TENANTS.
func buildAppSignIn(deps routerDeps) *appsignin.Handler {
	if deps.appBaseURL == "" {
		return nil
	}
	on, test := appsignin.Switch(os.Getenv(envAppSignIn))
	opts := []appsignin.Option{appsignin.WithTermsVersion(envOr(envAppTermsVersion, appsignin.DefaultTermsVersion))}
	if test {
		opts = append(opts, appsignin.WithOnlyTenants(splitList(os.Getenv(envAppSignInTenants))))
	}
	if deps.stepUp != nil {
		// The sheet's approve is the sign-in itself, so a code sign-in made
		// for this request passes it (codesignin README).
		opts = append(opts, appsignin.WithStepUp(deps.stepUp.WithoutCodeCheck()))
	}
	return appsignin.New(on, strings.TrimRight(deps.appBaseURL, "/")+"/sign-in/app", opts...)
}

func splitHosts(s string) []string {
	var out []string
	for _, h := range strings.Split(s, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// splitNets parses comma-separated CIDRs, skipping and logging bad ones.
func splitNets(logger *slog.Logger, s string) []netip.Prefix {
	var out []netip.Prefix
	for _, h := range splitHosts(s) {
		p, err := netip.ParsePrefix(h)
		if err != nil {
			logger.Warn("ignoring invalid network", "env", envOAuthSharedNets, "value", h)
			continue
		}
		out = append(out, p.Masked())
	}
	return out
}

func buildDeviceStore(pool *pgxpool.Pool) auth.DeviceStore {
	if pool == nil {
		return auth.NewMemoryDeviceStore()
	}
	return auth.NewPostgresDeviceStore(pool)
}

func buildPairingStore(pool *pgxpool.Pool) auth.PairingStore {
	if pool == nil {
		return auth.NewMemoryPairingStore()
	}
	return auth.NewPostgresPairingStore(pool)
}

func buildPasskeyStore(pool *pgxpool.Pool) auth.PasskeyStore {
	if pool == nil {
		return auth.NewMemoryPasskeyStore()
	}
	return auth.NewPostgresPasskeyStore(pool)
}

func buildDeveloperSessionStore(pool *pgxpool.Pool) auth.DeveloperSessionStore {
	if pool == nil {
		return auth.NewMemoryDeveloperSessionStore()
	}
	return auth.NewPostgresDeveloperSessionStore(pool)
}

// buildSignedRequests keeps the record of a phone's signed setup calls in
// Postgres when there is a pool, so a replay is refused after a restart and
// by every process; in memory otherwise.
func buildSignedRequests(pool *pgxpool.Pool) auth.SignedRequests {
	if pool == nil {
		return auth.NewMemorySignedRequests()
	}
	return auth.NewPostgresSignedRequests(pool)
}

func buildApprovalCheckStore(pool *pgxpool.Pool) auth.ApprovalCheckStore {
	if pool == nil {
		return auth.NewMemoryApprovalCheckStore()
	}
	return auth.NewPostgresApprovalCheckStore(pool)
}

func buildWindowStore(pool *pgxpool.Pool) auth.WindowStore {
	if pool == nil {
		return auth.NewMemoryWindowStore()
	}
	return auth.NewPostgresWindowStore(pool)
}

func buildStores(_ context.Context, pool *pgxpool.Pool) (auth.APIKeyStore, auth.WalletNonceStore, auth.WalletStore, error) {
	if pool == nil {
		return auth.NewMemoryAPIKeyStore(), auth.NewMemoryWalletNonceStore(), auth.NewMemoryWalletStore(), nil
	}
	return auth.NewPostgresAPIKeyStore(pool),
		auth.NewPostgresWalletNonceStore(pool),
		auth.NewPostgresWalletStore(pool), nil
}

// buildReceiptStore returns a receipt.Store backed by Postgres
// when pool is set, in-memory otherwise. The schema is applied in
// openPool alongside the other cloud_meta DDL so a fresh checkout
// starts up cleanly.
func buildReceiptStore(pool *pgxpool.Pool) receipt.Store {
	if pool == nil {
		return receipt.NewMemoryStore()
	}
	return receipt.NewPostgresStore(pool)
}

// buildResolver returns the ActorResolver wired for the configured
// auth mode.
//
//	stub mode: in-memory MapResolver seeded with KIFF_STUB_SUBJECTS
//	           under KIFF_DEMO_TENANT_ID (or "demo-tenant" by
//	           default). Suitable for tests and local development.
//
//	clerk mode + nil ensurer: in-memory MapResolver seeded with
//	           KIFF_STUB_SUBJECTS under the demo tenant. Used when
//	           Postgres isn't available (no real subject→tenant
//	           table to back the lazy path); fixture subjects keep
//	           local-clerk smoke runs working through the API-key
//	           path.
//
//	clerk mode + non-nil ensurer: LazyResolver auto-provisions a
//	           tenant on first sign-in via the supplied ensurer.
//	           The MapResolver is wired as a fallback so any
//	           KIFF_STUB_SUBJECTS still resolve through the
//	           fixture path (handy for ops/service identities
//	           that should not auto-provision a tenant).
//
// Returns the seeded subject list for logging.
func buildResolver(provider, demoTenant, stubSubjects string, ensurer auth.SubjectTenantEnsurer) (auth.ActorResolver, []string, error) {
	resolver := auth.NewMapResolver()
	subs := splitCSV(stubSubjects)

	switch strings.TrimSpace(provider) {
	case "", "stub":
		// Seed the configured stub subjects, plus a default
		// fixture so a fresh checkout can hit /v1/keys with
		// "Authorization: Bearer dev-user" out of the box.
		tenantID := demoTenant
		if tenantID == "" {
			tenantID = "demo-tenant"
		}
		if len(subs) == 0 {
			subs = []string{"dev-user"}
		}
		for _, sub := range subs {
			resolver.Set(sub, actor.Actor{
				ID:          "actor-" + sub,
				Type:        actor.TypeHuman,
				DisplayName: sub,
				Roles:       []string{"admin"},
			}, tenantID)
		}
		return resolver, subs, nil

	case "clerk":
		// Seed any fixture subjects regardless of the lazy path —
		// they let API-key smoke tests work in clerk mode without
		// a Clerk JWT.
		seedTenant := demoTenant
		if seedTenant == "" {
			seedTenant = "demo-tenant"
		}
		for _, sub := range subs {
			resolver.Set(sub, actor.Actor{
				ID:          "actor-" + sub,
				Type:        actor.TypeService,
				DisplayName: sub,
				Roles:       []string{"operator"},
			}, seedTenant)
		}

		if ensurer != nil {
			// Production path: subject→tenant resolution goes
			// through the cloud_meta.subject_tenants table; fixture
			// subjects fall through to the MapResolver.
			return auth.NewLazyResolver(ensurer, auth.WithFallback(resolver)), subs, nil
		}

		// Postgres-less clerk mode (uncommon — typically dev only).
		// The auth middleware will return 401 for any subject not
		// pre-seeded into stubSubjects.
		if demoTenant == "" {
			return nil, nil, fmt.Errorf("env var %s is required when %s=clerk and Postgres is not configured (the lazy resolver needs cloud_meta to auto-provision tenants; without it, a demo tenant is the only fallback)", envDemoTenant, envAuthProv)
		}
		return resolver, subs, nil

	default:
		return nil, nil, fmt.Errorf("%w: %q", auth.ErrProviderUnsupported, provider)
	}
}

// buildProvider picks the auth.Provider based on env. Fails-fast in
// Clerk mode when CLERK_SECRET_KEY is unset — the SDK will read the
// env var itself on first use, but we want a clean error at startup
// rather than a confusing 401 on the first request.
func buildProvider(provider, clerkSecret string, mapResolver auth.ActorResolver) (auth.Provider, auth.ProviderName, error) {
	switch strings.TrimSpace(provider) {
	case "", "stub":
		stubBacking, ok := mapResolver.(*auth.MapResolver)
		if !ok {
			return nil, "", fmt.Errorf("stub provider requires *auth.MapResolver, got %T", mapResolver)
		}
		return auth.NewStubProvider(stubBacking), auth.ProviderStub, nil
	case "clerk":
		if strings.TrimSpace(clerkSecret) == "" {
			return nil, "", fmt.Errorf("env var %s is required when %s=clerk", envClerkSecret, envAuthProv)
		}
		// The clerk-sdk-go/v2 library uses a package-level secret
		// configured via clerk.SetKey; it does not auto-read the
		// environment. We propagate the env value into the SDK's
		// global once at startup. Tests that exercise the SDK with
		// a different key (live integration test) call SetKey
		// themselves; the last write wins, which is fine for v0.1.
		clerksdk.SetKey(clerkSecret)
		return auth.NewClerkSessions(), auth.ProviderClerk, nil
	default:
		return nil, "", fmt.Errorf("%w: %q", auth.ErrProviderUnsupported, provider)
	}
}

// newLogger returns a JSON or text slog.Logger based on KIFF_LOG_FORMAT.
func newLogger(format string) *slog.Logger {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "text":
		return slog.New(slog.NewTextHandler(os.Stdout, nil))
	default:
		return slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ─── Receipt pipeline wiring ─────────────────────────────────────

// buildReceiptPipeline returns a configured *receipt.Pipeline plus a
// short label describing the attester mode (for log attribution),
// along with a close function. Returns (nil, "disabled", noop, nil)
// when the mode resolves to disabled (default when no receipt store
// is wired, or when the operator explicitly sets
// KIFF_RECEIPT_MODE=disabled).
//
// Mode selection rule:
//   - KIFF_RECEIPT_MODE explicitly set: honor it. anchor mode
//     fails-fast at startup when the three KIFF_ANCHOR_* env vars
//     are not all present.
//   - KIFF_RECEIPT_MODE unset: infer. anchor when all three
//     KIFF_ANCHOR_* env vars are set, local otherwise. disabled
//     only when no receipt store is wired (in-memory smoke runs).
//
// The disabled mode keeps the cloud's pre-receipt behaviour
// available for callers who don't want the pipeline yet — useful
// for the AWS deploy that needs an explicit env-file flip before
// receipts start being persisted.
func buildReceiptPipeline(ctx context.Context, store receipt.Store, tenants tenant.Store, cursor receipt.DecisionCursor, logger *slog.Logger, extra ...receipt.PipelineOption) (*receipt.Pipeline, string, func(), error) {
	mode, err := resolveReceiptMode(os.Getenv(envReceiptMode), store, os.Getenv(envAnchorPrivateKey), os.Getenv(envAnchorRPCURL), os.Getenv(envAnchorContract))
	if err != nil {
		return nil, "", func() {}, err
	}

	// Shared across modes: the decision-reconcile watermark, wired
	// when Postgres is available. nil leaves ReconcileDecisions on its
	// full-scan path (correct, just unbounded).
	// extra options (settled receipts) apply to every attester mode;
	// a nil one is skipped.
	var extraOpts []receipt.PipelineOption
	for _, o := range extra {
		if o != nil {
			extraOpts = append(extraOpts, o)
		}
	}
	cursorOpt := func() []receipt.PipelineOption {
		if cursor == nil {
			return nil
		}
		return []receipt.PipelineOption{receipt.WithDecisionCursor(cursor)}
	}

	switch mode {
	case receiptModeDisabled:
		logger.Info("receipt pipeline: disabled",
			slog.String("hint", "set KIFF_RECEIPT_MODE=local or KIFF_RECEIPT_MODE=anchor to enable"),
		)
		return nil, string(mode), func() {}, nil

	case receiptModeLocal:
		wallet, err := buildAnchorWallet(os.Getenv(envAnchorPrivateKey))
		if err != nil {
			return nil, "", func() {}, fmt.Errorf("anchor wallet: %w", err)
		}
		attester := receipt.NewLocalAttester(orRandomWallet(wallet, logger), nil)
		pipeline, err := receipt.NewPipeline(store, attester, append(append([]receipt.PipelineOption{receipt.WithLogger(logger)}, cursorOpt()...), extraOpts...)...)
		if err != nil {
			return nil, "", func() {}, fmt.Errorf("local pipeline: %w", err)
		}
		logger.Info("receipt pipeline: local attester (signs only, no on-chain anchoring)")
		return pipeline, string(mode), func() {}, nil

	case receiptModeAnchor:
		wallet, err := buildAnchorWallet(os.Getenv(envAnchorPrivateKey))
		if err != nil {
			return nil, "", func() {}, fmt.Errorf("anchor wallet: %w", err)
		}
		if wallet == nil {
			// Should not happen — resolveReceiptMode validated this.
			return nil, "", func() {}, fmt.Errorf("anchor mode requires %s", envAnchorPrivateKey)
		}
		rpcURL := strings.TrimSpace(os.Getenv(envAnchorRPCURL))
		contractHex := strings.TrimSpace(os.Getenv(envAnchorContract))
		sepoliaAttester, closeSepolia, err := buildAnchorAttester(ctx, wallet, rpcURL, contractHex, tenants, receipt.NetworkBaseSepolia)
		if err != nil {
			return nil, "", func() {}, fmt.Errorf("anchor attester: %w", err)
		}

		// Optional mainnet attester for paid (mainnet-entitled)
		// tenants. Built only when the mainnet env vars are
		// set; nil otherwise, in which case mainnet-entitled tenants
		// fall back to Sepolia (honest pending) via RoutingAttester.
		mainnetAttester, closeMainnet, err := buildOptionalMainnetAttester(ctx, wallet, tenants)
		if err != nil {
			closeSepolia()
			return nil, "", func() {}, fmt.Errorf("mainnet anchor attester: %w", err)
		}
		closeAll := func() { closeSepolia(); closeMainnet() }

		// Anchor RPC hardening: wrap every on-chain attester
		// in a ThrottledAttester so the sweep paces its submits and
		// retries transient RPC failures (429, "underpriced") instead
		// of dropping receipts to the next sweep. Defaults come from
		// the receipt package; env knobs override. Done here, at the
		// outermost layer, so both the routed and default attesters
		// the pipeline holds are throttled.
		throttleOpts := anchorThrottleOptions(logger)
		defaultAttester := receipt.NewThrottledAttester(sepoliaAttester, throttleOpts...)
		var routedMainnet receipt.Attester
		if mainnetAttester != nil {
			routedMainnet = receipt.NewThrottledAttester(mainnetAttester, throttleOpts...)
		}

		opts := []receipt.PipelineOption{receipt.WithLogger(logger)}
		opts = append(opts, cursorOpt()...)
		opts = append(opts, extraOpts...)
		// Per-tenant chain routing: resolve each tenant's rung from
		// its plan (tenant.PlanFor.AnchorChain) and anchor on the
		// matching chain. Needs the tenant store; without it the
		// pipeline uses the single Sepolia attester for everyone.
		if tenants != nil {
			routing := receipt.NewRoutingAttester(
				planChainTierResolver{store: tenants},
				defaultAttester, routedMainnet, logger,
			)
			opts = append(opts, receipt.WithAttesterResolver(routing))
		}
		pipeline, err := receipt.NewPipeline(store, defaultAttester, opts...)
		if err != nil {
			closeAll()
			return nil, "", func() {}, fmt.Errorf("anchor pipeline: %w", err)
		}
		logger.Info("receipt pipeline: anchor attester wired",
			slog.String("sepolia_rpc", rpcURL),
			slog.String("contract", contractHex),
			slog.String("wallet", strings.ToLower(wallet.Address().Hex())),
			slog.Bool("mainnet_enabled", mainnetAttester != nil),
		)
		return pipeline, string(mode), closeAll, nil

	default:
		return nil, "", func() {}, fmt.Errorf("unknown receipt mode %q", mode)
	}
}

// resolveReceiptMode applies the selection rule from the
// buildReceiptPipeline doc comment. Returns disabled when no
// receipt store is wired (the cloud's in-memory smoke path).
//
// Validates the cross-field requirement: anchor mode requires the
// three KIFF_ANCHOR_* env vars. A misconfigured anchor mode is
// fatal at startup rather than silently degrading to local —
// otherwise an operator sets anchor in production, the env vars
// don't load for some reason, and receipts ship signed but
// unanchored without anyone noticing.
func resolveReceiptMode(raw string, store receipt.Store, anchorKey, anchorRPC, anchorContract string) (receiptMode, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "disabled":
		return receiptModeDisabled, nil
	case "local":
		if store == nil {
			return "", fmt.Errorf("%s=local requires a receipt store (set %s for Postgres-backed persistence)", envReceiptMode, envDatabaseURL)
		}
		return receiptModeLocal, nil
	case "anchor":
		if store == nil {
			return "", fmt.Errorf("%s=anchor requires a receipt store (set %s for Postgres-backed persistence)", envReceiptMode, envDatabaseURL)
		}
		if strings.TrimSpace(anchorKey) == "" || strings.TrimSpace(anchorRPC) == "" || strings.TrimSpace(anchorContract) == "" {
			return "", fmt.Errorf("%s=anchor requires %s, %s, and %s to all be set", envReceiptMode, envAnchorPrivateKey, envAnchorRPCURL, envAnchorContract)
		}
		return receiptModeAnchor, nil
	case "":
		// Inferred. No receipt store → disabled. All three anchor
		// env vars set → anchor. Otherwise → local.
		if store == nil {
			return receiptModeDisabled, nil
		}
		if strings.TrimSpace(anchorKey) != "" && strings.TrimSpace(anchorRPC) != "" && strings.TrimSpace(anchorContract) != "" {
			return receiptModeAnchor, nil
		}
		return receiptModeLocal, nil
	default:
		return "", fmt.Errorf("unknown %s value %q (expected disabled, local, or anchor)", envReceiptMode, v)
	}
}

// buildAnchorWallet returns a Wallet from the env hex, or nil when
// the env var is empty. Returns an error only when the value is set
// but malformed — empty is treated as "use local mode."
func buildAnchorWallet(hexKey string) (*anchor.Wallet, error) {
	if strings.TrimSpace(hexKey) == "" {
		return nil, nil
	}
	return anchor.NewWalletFromHex(hexKey)
}

// orRandomWallet returns w if non-nil, otherwise a freshly-generated
// random wallet (logged so the operator knows local mode is using
// an ephemeral signer).
func orRandomWallet(w *anchor.Wallet, logger *slog.Logger) *anchor.Wallet {
	if w != nil {
		return w
	}
	rand, err := anchor.NewRandomWallet()
	if err != nil {
		// Generating an ECDSA key from crypto/rand failing is a
		// process-fatal condition; signal by returning a nil wallet
		// and letting NewLocalAttester panic on first sign. In
		// practice we never see this — included here only for
		// completeness.
		logger.Error("receipt pipeline: random wallet generation failed; attestations will fail",
			slog.String("err", err.Error()),
		)
		return nil
	}
	logger.Warn("receipt pipeline: using ephemeral random wallet (local mode)",
		slog.String("address", strings.ToLower(rand.Address().Hex())),
	)
	return rand
}

// buildAnchorAttester dials the RPC, attaches to the deployed
// KiffAnchor at contractHex, and constructs the AnchorAttester. The
// returned close function disconnects the RPC on shutdown.
//
// tenants is the per-tenant salt source. Pass nil to fall
// back to unsalted leaves; production passes a tenant.Store-backed
// resolver so every receipt anchored carries the issuing tenant's
// secret.
func buildAnchorAttester(ctx context.Context, wallet *anchor.Wallet, rpcURL, contractHex string, tenants tenant.Store, network string) (*receipt.AnchorAttester, func(), error) {
	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return nil, nil, fmt.Errorf("dial rpc: %w", err)
	}
	addr := common.HexToAddress(contractHex)
	if (addr == common.Address{}) {
		client.Close()
		return nil, nil, fmt.Errorf("zero contract address")
	}
	submitter, err := anchor.AttachAnchor(ctx, client, wallet, addr)
	if err != nil {
		client.Close()
		return nil, nil, fmt.Errorf("attach anchor: %w", err)
	}
	salts := tenantSaltResolver(tenants)
	att := receipt.NewAnchorAttester(submitter, wallet, salts, nil, nil).WithNetwork(network)
	return att, func() { client.Close() }, nil
}

// buildOptionalMainnetAttester builds the mainnet attester for the
// per-tenant routing, or returns a nil attester when the
// mainnet env vars are not set (the common case until the mainnet
// KiffAnchor is deployed). A nil return is a true nil interface so
// RoutingAttester's `mainnet != nil` check works and mainnet-entitled
// tenants fall back to Sepolia.
//
// The mainnet wallet is KIFF_ANCHOR_PRIVATE_KEY_MAINNET when set (the
// the anchoring design amendment recommends a separate real-value wallet),
// otherwise the shared anchor wallet passed in.
func buildOptionalMainnetAttester(ctx context.Context, fallbackWallet *anchor.Wallet, tenants tenant.Store) (receipt.Attester, func(), error) {
	rpcURL := strings.TrimSpace(os.Getenv(envAnchorRPCURLMainnet))
	contractHex := strings.TrimSpace(os.Getenv(envAnchorContractMainnet))
	if rpcURL == "" || contractHex == "" {
		return nil, func() {}, nil
	}
	wallet := fallbackWallet
	if mk := strings.TrimSpace(os.Getenv(envAnchorPrivateKeyMainnet)); mk != "" {
		w, err := anchor.NewWalletFromHex(mk)
		if err != nil {
			return nil, func() {}, fmt.Errorf("mainnet wallet: %w", err)
		}
		wallet = w
	}
	att, closeFn, err := buildAnchorAttester(ctx, wallet, rpcURL, contractHex, tenants, receipt.NetworkBaseMainnet)
	if err != nil {
		return nil, func() {}, err
	}
	return att, closeFn, nil
}

// planChainTierResolver adapts the tenant store + plan model into a
// receipt.ChainTierResolver. It returns the tenant's chain
// rung ("sepolia"/"mainnet") from tenant.PlanFor; the routing
// attester maps that to the right submitter. Server-controlled: the
// rung comes from the plan on the tenant row, never from the caller.
type planChainTierResolver struct {
	store tenant.Store
}

func (r planChainTierResolver) ChainTierFor(ctx context.Context, tenantID string) (string, error) {
	t, err := r.store.Get(ctx, tenantID)
	if err != nil {
		return "", err
	}
	return string(tenant.PlanFor(t).AnchorChain), nil
}

// tenantSaltResolver adapts a tenant.Store into a
// receipt.SaltResolver. Returns nil when tenants is nil so callers
// can stay on the unsalted path during local development.
//
// A tenant lookup that fails returns the error to the attester,
// which fails the attestation rather than silently falling back to
// unsalted (the failure case for "tenant not found" is rare enough —
// receipts are produced for active tenants — that we'd rather catch
// it loudly than silently corrupt the salt boundary).
//
// A tenant whose redaction_salt is still NULL after backfill is
// treated as "anchor unsalted" — the legacy compat path. The salt
// resolver returns nil bytes; AnchorAttester observes
// !HasRedactionSalt and constructs an unsalted batch.
func tenantSaltResolver(tenants tenant.Store) receipt.SaltResolver {
	if tenants == nil {
		return nil
	}
	return receipt.SaltResolverFunc(func(ctx context.Context, tenantID string) ([]byte, error) {
		t, err := tenants.Get(ctx, tenantID)
		if err != nil {
			return nil, fmt.Errorf("look up tenant %s: %w", tenantID, err)
		}
		if !t.HasRedactionSalt() {
			return nil, nil
		}
		return t.RedactionSalt, nil
	})
}

// parseAnchorInterval parses the env value as a Go duration. Empty
// or malformed → fallback. "0" or "0s" → returns 0 (loop disabled).
func parseAnchorInterval(raw string, fallback time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return d
}

// parseOperatorSubjects splits the comma-separated KIFF_OPS_SUBJECTS
// value into a trimmed, non-empty slice. Empty input yields nil (the
// operator surface stays unmounted).
func parseOperatorSubjects(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// anchorThrottleOptions builds the ThrottledAttester option set for
// the anchor attesters from the optional env knobs. With both
// unset it returns just the logger option, leaving the receipt
// package defaults (750ms spacing, 5 retries, 1s→30s backoff) in
// place. A blank/malformed value falls back to the default for that
// knob rather than failing startup — the hardening is best-effort
// tuning, not a correctness gate.
func anchorThrottleOptions(logger *slog.Logger) []receipt.ThrottleOption {
	opts := []receipt.ThrottleOption{receipt.WithThrottleLogger(logger)}

	if raw := strings.TrimSpace(os.Getenv(envAnchorSubmitInterval)); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			opts = append(opts, receipt.WithThrottleInterval(d))
		} else {
			logger.Warn("anchor: ignoring malformed submit interval; using default",
				slog.String("env", envAnchorSubmitInterval),
				slog.String("value", raw),
			)
		}
	}

	if raw := strings.TrimSpace(os.Getenv(envAnchorMaxRetries)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			// base/max 0 keep the package defaults; only the retry
			// count is overridden here.
			opts = append(opts, receipt.WithThrottleRetry(n, 0, 0))
		} else {
			logger.Warn("anchor: ignoring malformed max retries; using default",
				slog.String("env", envAnchorMaxRetries),
				slog.String("value", raw),
			)
		}
	}

	return opts
}

// tenantActiveLister adapts tenant.Store to receipt.TenantLister so
// the anchor sweep loop can walk active tenants.
type tenantActiveLister struct {
	store tenant.Store
}

// ListActiveIDs implements receipt.TenantLister.
func (l tenantActiveLister) ListActiveIDs(ctx context.Context) ([]string, error) {
	rows, err := l.store.List(ctx, tenant.ListFilter{Status: tenant.StatusActive})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, t := range rows {
		ids = append(ids, t.ID)
	}
	return ids, nil
}

// Compile-time interface satisfaction for the anchor sweep loop.
var _ receipt.TenantLister = tenantActiveLister{}
var _ receipt.RuntimeResolver = (*cloudruntime.TenantRuntime)(nil)

// ─── Subject → tenant lazy provisioning ──────────────

// buildSubjectEnsurer constructs an auth.SubjectTenantEnsurer
// against cloud_meta.subject_tenants when Postgres is available,
// or returns nil otherwise. The lazy resolver path activates only
// when the ensurer is non-nil; in-memory deploys keep the
// MapResolver behaviour they had before.
func buildSubjectEnsurer(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) (auth.SubjectTenantEnsurer, error) {
	if pool == nil {
		return nil, nil
	}
	if err := tenant.ApplySubjectTenantsSchema(ctx, pool); err != nil {
		return nil, fmt.Errorf("apply subject_tenants schema: %w", err)
	}
	store := tenant.NewPostgresStore(pool)
	subjects := tenant.NewPostgresSubjectStore(pool)
	prov := tenant.NewProvisioner(pool, store)
	logger.Info("subject→tenant lazy resolver: enabled (cloud_meta.subject_tenants)")
	return &subjectTenantEnsurer{
		store:    store,
		prov:     prov,
		subjects: subjects,
	}, nil
}

// buildMembershipStore applies the governance-roles design schema and backfills an admin
// membership for each existing subject→tenant row when Postgres is
// available; in-memory otherwise.
func buildMembershipStore(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) (membership.Store, error) {
	if pool == nil {
		logger.Info("tenant memberships: in-memory (each tenant's first person is its admin)")
		return membership.NewMemoryStore(), nil
	}
	if err := membership.ApplySchema(ctx, pool); err != nil {
		return nil, err
	}
	n, err := membership.Backfill(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("backfill memberships: %w", err)
	}
	logger.Info("tenant memberships: Postgres-backed (cloud_meta.tenant_memberships)", slog.Int("backfilled", n))
	return membership.NewPostgresStore(pool), nil
}

// buildStepUp is the recent-sign-in check for the governance-roles design's sensitive
// changes. With Clerk it reads the token's fva claim and falls back to
// the Clerk session's creation time; the stub provider has no sign-in
// to repeat, so local runs trust it.
func buildStepUp(provider auth.ProviderName) *auth.StepUp {
	if provider != auth.ProviderClerk {
		return &auth.StepUp{TrustAll: true}
	}
	return &auth.StepUp{
		Window: auth.StepUpWindow,
		SessionCreatedAt: func(ctx context.Context, sessionID string) (time.Time, error) {
			sess, err := clerksession.Get(ctx, sessionID)
			if err != nil {
				return time.Time{}, err
			}
			return time.UnixMilli(sess.CreatedAt).UTC(), nil
		},
	}
}

// subjectTenantEnsurer adapts tenant.EnsureForSubject to the
// auth.SubjectTenantEnsurer interface. The auth package can't
// import the tenant package directly (it would create a cycle:
// tenant.Provisioner uses tenant.Store, tenant.Store uses
// auth.Identity through the resolver path); this adapter sits in
// main.go where both packages are already imported.
type subjectTenantEnsurer struct {
	store    tenant.Store
	prov     *tenant.Provisioner
	subjects tenant.SubjectStore
}

// EnsureForSubject implements auth.SubjectTenantEnsurer.
func (s *subjectTenantEnsurer) EnsureForSubject(ctx context.Context, subject string) (string, error) {
	t, err := tenant.EnsureForSubject(ctx, s.store, s.prov, s.subjects, subject)
	if err != nil {
		return "", err
	}
	return t.ID, nil
}

// Compile-time interface satisfaction.
var _ auth.SubjectTenantEnsurer = (*subjectTenantEnsurer)(nil)

// isULID returns true when s is a 26-character lowercase ULID. The
// tenant store enforces this; we mirror the check here to decide
// whether a user-supplied demo-tenant id is usable as a row id.
func isULID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'z':
		default:
			return false
		}
	}
	return true
}

// permitRetention is how long an operation's binding is kept. Gateway
// operation ids are never reused, so the binding only has to outlive any
// retry of the same call.
// meterSyncEvery is how often calls that ended are put on their meter.
const meterSyncEvery = 30 * time.Second

// overageEvery is how often overage goes to Stripe, and seatsEvery how
// often Team's seats are checked against its people.
const (
	overageEvery = time.Minute
	seatsEvery   = 5 * time.Minute
)

const permitRetention = 30 * 24 * time.Hour

// buildPermits wires the execution-permits design execution permits. The targets store is
// always built; the issuer is nil when no signing key is configured. A
// malformed key stops the start: a deployment that means to issue permits
// must not silently issue none.
func buildPermits(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) (permit.Targets, *permit.Issuer, error) {
	var store permit.Store = permit.NewMemoryStore()
	var targets permit.Targets = permit.NewMemoryTargets()
	if pool != nil {
		if err := permit.ApplySchema(ctx, pool); err != nil {
			return nil, nil, fmt.Errorf("apply permits: %w", err)
		}
		store, targets = permit.PostgresStore{Pool: pool}, permit.PostgresTargets{Pool: pool}
	}
	raw := strings.TrimSpace(os.Getenv(envPermitSigningKey))
	if raw == "" {
		logger.Info("execution permits off", slog.String("reason", envPermitSigningKey+" unset"))
		return targets, nil, nil
	}
	key, err := permit.ParseKey(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", envPermitSigningKey, err)
	}
	others, err := permit.ParsePublicKeys(os.Getenv(envPermitPublishedKeys))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", envPermitPublishedKeys, err)
	}
	life := permit.DefaultLifetime
	if v := strings.TrimSpace(os.Getenv(envPermitLifetime)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second || d > permit.MaxLifetime {
			return nil, nil, fmt.Errorf("%s: a duration from 1s to %s", envPermitLifetime, permit.MaxLifetime)
		}
		life = d
	}
	go func() {
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for {
			if n, err := store.Purge(ctx, time.Now().Add(-permitRetention)); err != nil {
				logger.Warn("permits: purge failed", slog.String("err", err.Error()))
			} else if n > 0 {
				logger.Info("permits: purged old bindings", slog.Int64("rows", n))
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	logger.Info("execution permits on", slog.String("kid", key.ID), slog.Duration("lifetime", life),
		slog.Int("published_keys", 1+len(others)), slog.Bool("postgres", pool != nil))
	return targets, &permit.Issuer{Key: key, Others: others, Store: store, Targets: targets,
		Iss: envOr(envPermitIssuer, "https://api.kiff.dev"), Lifetime: life}, nil
}

// planquesFor confirms halts with the planques at urls, asking each with
// the permit key, so a planque answers only KIFF.
func planquesFor(urls []string, issuer *permit.Issuer) *halts.Planques {
	p := &halts.Planques{URLs: urls}
	if issuer != nil {
		p.Key, p.Issuer = &issuer.Key, issuer.Iss
	}
	return p
}

// sweepEvery is how often the API scrubs the accounts whose grace window
// after deletion has ended: daily is enough for a 30-day window.
const sweepEvery = 24 * time.Hour

// startSweepExpired erases, once a day, every deleted account whose 30-day
// grace window has ended (tenant.SweepExpired): what the privacy policy
// promises after an account is deleted, with no one running a command. The
// first sweep is ten minutes after start. Each scrubbed or failed account is
// logged; a failed one is still deleted_pending and is tried the next day.
// The same daily run deletes every account's unclaimed pairings a day past
// their exp (auth.PairingKeptAfterExpiry), and logs the devices dry run
// (auth.DeviceCleanupDryRun): counts of unused and likely superseded devices,
// ending none.
func startSweepExpired(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	if pool == nil {
		return
	}
	store := tenant.NewPostgresStore(pool)
	prov := tenant.NewProvisioner(pool, store)
	devices := auth.NewPostgresDeviceStore(pool)
	pairings := auth.NewPostgresPairingStore(pool)
	if os.Getenv(envClerkSecret) != "" {
		// Erasure also deletes the sign-in of each person it leaves with no account.
		prov.WithIdentityEraser(codesignin.Eraser{Store: codesignin.NewPostgres(pool), Next: owneremail.NewClerkIdentityEraser()})
	}
	go func() {
		wait := 10 * time.Minute
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = sweepEvery
			onceAcross(ctx, pool, "kiff.sweep-expired", logger, func() {
				sweepExpiredOnce(ctx, store, prov, time.Now().UTC(), logger)
			})
			// Unclaimed pairings, and pairing refusals, a day past their exp, on every account: nothing reads
			// them then. A claimed one stays (auth.PairingKeptAfterExpiry).
			onceAcross(ctx, pool, "kiff.pairings-expired", logger, func() {
				n, err := pairings.DeleteExpired(ctx, time.Now().UTC().Add(-auth.PairingKeptAfterExpiry))
				if err != nil {
					logger.Error("pairings: deleting expired unclaimed failed", slog.String("err", err.Error()))
					return
				}
				logger.Info("pairings: deleted expired unclaimed", slog.Int64("count", n))
			})
			// Integration proof runs and overrides past their 60 days.
			onceAcross(ctx, pool, "kiff.proofs-expired", logger, func() {
				n, err := proofs.NewPostgresStore(pool).Sweep(ctx, time.Now().UTC().Add(-proofs.Retention))
				if err != nil {
					logger.Error("proofs: sweeping expired runs failed", slog.String("err", err.Error()))
					return
				}
				logger.Info("proofs: swept expired runs and overrides", slog.Int("count", n))
			})
			// Counts only: which devices a cleanup would end. Ending is off.
			onceAcross(ctx, pool, "kiff.devices-dry-run", logger, func() {
				if _, err := auth.DeviceCleanupDryRun(ctx, devices, time.Now().UTC(), auth.DeviceUnusedAfter, logger); err != nil {
					logger.Error("devices dry run: failed", slog.String("err", err.Error()))
				}
			})
		}
	}()
}

// proofOIDCConfig is the proof runner's OIDC settings from the environment.
func proofOIDCConfig() proofs.OIDCConfig {
	return proofs.OIDCConfig{
		Audience:   strings.TrimSpace(os.Getenv(envProofOIDCAudience)),
		Repository: strings.TrimSpace(os.Getenv(envProofOIDCRepository)),
		Ref:        strings.TrimSpace(os.Getenv(envProofOIDCRef)),
		Workflow:   strings.TrimSpace(os.Getenv(envProofOIDCWorkflow)),

		RepositoryID: strings.TrimSpace(os.Getenv(envProofOIDCRepositoryID)),
		OwnerID:      strings.TrimSpace(os.Getenv(envProofOIDCOwnerID)),
	}
}

// proofReleaseAnyway reports whether KIFF_PROOF_RELEASE_ANYWAY is on.
func proofReleaseAnyway() bool {
	return strings.TrimSpace(os.Getenv(envProofReleaseAnyway)) == "on"
}

// proofIngestReady reports whether the ingest mounts: the OIDC settings
// complete, a store, at least one vetted test tenant, and none refused.
func proofIngestReady(store proofs.Store, testTenants []string, refused bool) bool {
	return proofOIDCConfig().Complete() && store != nil && len(testTenants) > 0 && !refused
}

// buildProofIngest returns the integration proofs' ingest, or nil (nothing
// mounted) unless proofIngestReady.
func buildProofIngest(deps routerDeps) *handlers.ProofIngestHandler {
	if !proofIngestReady(deps.proofStore, deps.proofTestTenants, deps.proofTenantsRefused) {
		return nil
	}
	cfg := proofOIDCConfig()
	var versions proofs.VersionChecker
	if list := splitList(os.Getenv(envProofRunnerVersions)); len(list) > 0 {
		allow := proofs.AllowList{}
		for _, v := range list {
			allow[v] = true
		}
		versions = allow
	} else {
		versions = &proofs.GitHubMain{Repo: cfg.Repository, Branch: strings.TrimPrefix(cfg.Ref, "refs/heads/"),
			Token: strings.TrimSpace(os.Getenv(envProofGitHubToken))}
	}
	return handlers.NewProofIngestHandler(proofs.NewOIDCVerifier(cfg), deps.proofStore,
		deps.proofTestTenants, versions, deps.operatorAlerts, deps.logger)
}

// proofBootConfig is what the boot line reports: counts, ids and
// fingerprints, never a token's value.
func proofBootConfig(store proofs.Store, keys proofs.TestKeys, vet proofTenantVet) proofs.BootConfig {
	return proofs.BootConfig{
		TestTenants: vet.own, Refused: vet.refused, Vetted: vet.vetted,
		TestKeys:       keys,
		ReleaseAnyway:  proofReleaseAnyway(),
		StateTokens:    len(splitList(os.Getenv(envProofStateTokens))),
		RunnerVersions: len(splitList(os.Getenv(envProofRunnerVersions))),
		IngestMounted:  proofIngestReady(store, vet.own, len(vet.refused) > 0),
	}
}

// proofTenantVet is KIFF_PROOF_TEST_TENANTS checked at boot.
type proofTenantVet struct {
	own     []string
	refused []proofs.RefusedTenant
	vetted  bool // false when there's no tenant store to ask
}

// proofTenantStores are what the boot check reads: each test tenant's plan,
// and every approval device and passkey registered on it.
type proofTenantStores struct {
	tenants  tenant.Store
	devices  auth.DeviceStore
	passkeys auth.PasskeyStore
	// sessions, when set, says its sessions' approval keys, which count as
	// the devices' and passkeys' do (sessionApprovalKeys).
	sessions auth.DeveloperSessionStore
}

// sessionApprovalKeys is what the proofs' check reads of the sessions: their
// live approval keys on an account.
type sessionApprovalKeys interface {
	ApprovalKeysInTenant(ctx context.Context, tenant string, now time.Time) ([][]byte, error)
}

// vetProofTestTenants keeps the test tenants that are KIFF's own accounts
// (plan internal) and hold only proof test keys: every approval device and
// passkey on the tenant is one of KIFF_PROOF_TEST_KEYS. So a real person's
// tenant, with their phone or computer on it, is never accepted as a test
// tenant, even on plan internal; proofUseCheck asks again at each use of a
// test key, for a device added after boot. An unknown id is refused. An unreadable store vets
// nothing: every id is refused, so the ingest stays unmounted. With no
// tenant store (a dev API without a database) it can't tell, and keeps the
// list as given; LogBoot warns.
func vetProofTestTenants(ctx context.Context, stores proofTenantStores, keys proofs.TestKeys, ids []string, logger *slog.Logger) proofTenantVet {
	if stores.tenants == nil {
		return proofTenantVet{own: ids}
	}
	own, refused, err := proofs.VetTestTenants(ctx, ids, proofTestTenantCheck(stores, keys))
	if err != nil {
		logger.Error("proofs: a store couldn't be read to check "+envProofTestTenants+"; none is used",
			slog.String("err", err.Error()))
	}
	return proofTenantVet{own: own, refused: refused, vetted: true}
}

// proofTestTenantCheck says why a tenant can't be a proof test tenant: it
// doesn't exist, its plan isn't internal (which only an operator sets), or
// a device or passkey on it isn't a proof test key.
func proofTestTenantCheck(stores proofTenantStores, keys proofs.TestKeys) proofs.TestTenantCheck {
	return func(ctx context.Context, id string) (string, error) {
		t, err := stores.tenants.Get(ctx, id)
		if errors.Is(err, tenant.ErrNotFound) {
			return proofs.RefusedMissing, nil
		}
		if err != nil {
			return "", err
		}
		if tenant.PlanFor(t).Canonical != tenant.PlanInternal {
			return proofs.RefusedNotOwn, nil
		}
		if stores.devices == nil || stores.passkeys == nil {
			return "", errors.New("no device or passkey store to read")
		}
		devices, err := stores.devices.ListActiveInTenant(ctx, id)
		if err != nil {
			return "", err
		}
		passkeys, err := stores.passkeys.ListActiveInTenant(ctx, id)
		if err != nil {
			return "", err
		}
		var registered [][]byte
		for _, d := range devices {
			registered = append(registered, d.PublicKey)
		}
		for _, p := range passkeys {
			registered = append(registered, p.PublicKey)
		}
		// A session's approval key signs approvals as a device's key does, so it
		// counts too: a test tenant holds only test keys, whichever key signs.
		if stores.sessions != nil {
			lister, ok := stores.sessions.(sessionApprovalKeys)
			if !ok {
				return "", errors.New("the session store can't list approval keys")
			}
			approval, err := lister.ApprovalKeysInTenant(ctx, id, time.Now())
			if err != nil {
				return "", err
			}
			registered = append(registered, approval...)
		}
		if !keys.OnlyTestKeys(registered) {
			return proofs.RefusedDevices, nil
		}
		return "", nil
	}
}

// proofTestTenantGuard holds the listed test tenants that are KIFF's own
// accounts (plan internal) to KIFF_PROOF_TEST_KEYS: the boot check's rule,
// kept as keys are added rather than found broken at use. Nil (nothing held)
// when no test key is listed, as proofKeyRestriction.
func proofTestTenantGuard(tenants tenant.Store, keys proofs.TestKeys, ids []string) *auth.TestTenantGuard {
	if keys.Empty() {
		return nil
	}
	g := auth.NewTestTenantGuard(ids, keys.Has)
	if tenants == nil {
		return g
	}
	return g.WithOwn(func(ctx context.Context, id string) (bool, error) {
		t, err := tenants.Get(ctx, id)
		if errors.Is(err, tenant.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return tenant.PlanFor(t).Canonical == tenant.PlanInternal, nil
	})
}

// proofKeyRestriction keeps KIFF_PROOF_TEST_KEYS to the vetted test
// tenants, or is nil (nothing restricted) when no test key is listed. Each
// use of a test key checks its tenant again, as at boot: a real person's
// device or passkey added there after boot refuses the test keys at once.
func proofKeyRestriction(deps routerDeps) *auth.KeyRestriction {
	if deps.proofTestKeys.Empty() {
		return nil
	}
	return auth.NewKeyRestriction(deps.proofTestKeys.Has, deps.proofTestTenants).
		WithUseCheck(proofUseCheck(proofTenantStores{tenants: deps.tenantStore, devices: deps.deviceStore, passkeys: deps.passkeys, sessions: deps.devSessionStore},
			deps.proofTestKeys, deps.logger))
}

// proofUseCheck is the boot check run again when a test key is used: the
// tenant is still plan internal and every device and passkey on it is still
// a proof test key. A store that can't be read refuses. With no tenant
// store (a dev API without a database) there's nothing to ask, as at boot,
// and it's nil.
func proofUseCheck(stores proofTenantStores, keys proofs.TestKeys, logger *slog.Logger) func(context.Context, string) bool {
	if stores.tenants == nil {
		return nil
	}
	check := proofTestTenantCheck(stores, keys)
	return func(ctx context.Context, tenantID string) bool {
		reason, err := check(ctx, tenantID)
		switch {
		case err != nil:
			logger.ErrorContext(ctx, "proofs: a test tenant couldn't be checked at use; its test keys are refused",
				slog.String("tenant", tenantID), slog.String("err", err.Error()))
			return false
		case reason != "":
			logger.WarnContext(ctx, "proofs: a test tenant failed its check at use; its test keys are refused",
				slog.String("tenant", tenantID), slog.String("reason", reason))
			return false
		}
		return true
	}
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// onceAcross runs fn only on the API host that takes the Postgres advisory
// lock named name, held for the run: with more than one host, a job that
// scrubs or prunes runs once, not once per host. Another host holding it, or
// the lock unreadable, skips this run; the job's next run tries again.
func onceAcross(ctx context.Context, pool *pgxpool.Pool, name string, logger *slog.Logger, fn func()) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		logger.Warn("background job: no database connection", slog.String("job", name), slog.String("err", err.Error()))
		return
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, name).Scan(&got); err != nil {
		logger.Warn("background job: lock unreadable", slog.String("job", name), slog.String("err", err.Error()))
		return
	}
	if !got {
		return // another host runs it
	}
	defer func() {
		// A connection that couldn't unlock is closed, not pooled: its session
		// ends, and Postgres drops the lock with it. Release then discards it.
		if _, err := conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, name); err != nil {
			logger.Warn("background job: unlock failed; closing its connection", slog.String("job", name), slog.String("err", err.Error()))
			_ = conn.Conn().Close(context.Background())
		}
	}()
	fn()
}

// sweepExpiredOnce runs one sweep and logs what it did.
func sweepExpiredOnce(ctx context.Context, store tenant.Store, prov tenant.Deprovisioner, now time.Time, logger *slog.Logger) tenant.SweepSummary {
	got, err := tenant.SweepExpired(ctx, store, prov, now, false, func(e tenant.SweepEntry) {
		attrs := []any{slog.String("tenant", e.TenantID), slog.String("outcome", e.Outcome), slog.String("grace_window_ends", e.GraceWindowEnds)}
		if e.Outcome == "scrubbed" {
			if e.Error != "" {
				logger.Warn("sweep: erased a deleted account", append(attrs, slog.String("err", e.Error))...)
				return
			}
			logger.Info("sweep: erased a deleted account", attrs...)
			return
		}
		logger.Error("sweep: a deleted account wasn't erased", append(attrs, slog.String("err", e.Error))...)
	})
	if err != nil {
		logger.Error("sweep: failed", slog.String("err", err.Error()))
	}
	return got
}

// startPrune prunes the accounts in envPruneTenants every hour, the first
// time a minute after start. A malformed age stops the start; an account
// that can't be pruned is logged and tried again next hour.
func startPrune(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	ids := splitCSV(os.Getenv(envPruneTenants))
	if len(ids) == 0 {
		return nil
	}
	if pool == nil {
		logger.Warn("prune: off without a database", slog.Int("tenants", len(ids)))
		return nil
	}
	age := 48 * time.Hour
	if v := strings.TrimSpace(os.Getenv(envPruneAfter)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < tenant.MinPruneAge {
			return fmt.Errorf("%s: a duration of at least %s", envPruneAfter, tenant.MinPruneAge)
		}
		age = d
	}
	prov := tenant.NewProvisioner(pool, tenant.NewPostgresStore(pool))
	logger.Info("prune: on", slog.Any("tenants", ids), slog.Duration("after", age))
	go func() {
		wait := time.Minute
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = time.Hour
			onceAcross(ctx, pool, "kiff.prune", logger, func() {
				for _, id := range ids {
					got, err := prov.Prune(ctx, id, age, time.Now(), false)
					if err != nil {
						logger.Error("prune: failed", slog.String("tenant", id), slog.String("err", err.Error()))
						continue
					}
					if got.Total() > 0 {
						logger.Info("prune: done", slog.String("tenant", id), slog.Int64("rows", got.Total()), slog.Any("tables", got))
					}
				}
			})
		}
	}()
	return nil
}

// gatewayOwners reads who connected each gateway tool from the gateway's
// own table in the shared database. Nil without a database, so a
// connector key then changes none of the gateway's existing tools.
func gatewayOwners(deps routerDeps) handlers.GatewayToolOwners {
	if deps.gatewayOwners != nil {
		return deps.gatewayOwners
	}
	if deps.pool == nil {
		return nil
	}
	return tooloutcome.NewPostgres(deps.pool)
}

// gatewayTitles reads each gateway tool's title for people from the
// gateway's own table in the shared database. Nil without a database: Cards
// and drafts then show their actions without titles.
func gatewayTitles(deps routerDeps) handlers.GatewayToolTitles {
	if deps.pool == nil {
		return nil
	}
	return tooloutcome.NewPostgres(deps.pool)
}

// setupReadDeps are what issuing Setup's read Card checks (kiff/tafs#138):
// the account's devices and members, for the owner's approval, the
// gateway's tools, and the account's keys. Any missing leaves the route off.
func setupReadDeps(deps routerDeps, memberSvc *membership.Service) (auth.DeviceStore, auth.Memberships, handlers.SetupReadTools, auth.APIKeyStore) {
	if deps.deviceStore == nil || memberSvc == nil || deps.pool == nil || deps.apiKeyStore == nil {
		return nil, nil, nil, nil
	}
	return deps.deviceStore, memberSvc, tooloutcome.NewPostgres(deps.pool), deps.apiKeyStore
}

// holdExceptions reads held calls' Card exceptions, for how each was
// answered (kiff/tafs#167). Nil without a Card store.
func holdExceptions(deps routerDeps) handlers.HeldCallExceptions {
	if deps.mandateStore == nil {
		return nil
	}
	return deps.mandateStore
}

// holdAnswers reads who answered each hold, and from which phone. Nil
// without a membership store: the answers then name no one.
func holdAnswers(deps routerDeps) handlers.HoldAnswers {
	if deps.memberStore == nil {
		return nil
	}
	return deps.memberStore
}

// toolOutcomes reads how authorized gateway calls ended at the tool from
// the gateway's own records in the shared database. Nil without
// a database: the in-memory API has no gateway.
func toolOutcomes(deps routerDeps) tooloutcome.Reader {
	if deps.pool == nil {
		return nil
	}
	return tooloutcome.NewPostgres(deps.pool)
}

// releasedProposals reads which calls a Card gave its room back for, from the
// account's Card store when it keeps releases. Nil otherwise: no call is then
// marked as not counted.
func releasedProposals(deps routerDeps) mandate.ReleasedProposalsReader {
	r, _ := deps.mandateStore.(mandate.ReleasedProposalsReader)
	return r
}

// callsReader is the account's calls, which a trial's counts are read from
// (kiff/tafs#173), when the decisions reader lists them. Nil otherwise.
func callsReader(r proposals.Reader) proposals.CallsReader {
	c, _ := r.(proposals.CallsReader)
	return c
}

// buildGooglePlay sets up Google Play Billing from the environment: nil,
// nil when the package name or the service account is unset. The push
// verifier is nil without its audience and service account, leaving the
// webhook answering that it isn't set up.
func buildGooglePlay(tenants tenant.Store, accounts appstore.Accounts, purchases playstore.Purchases, ledger packs.Ledger,
	credits packs.CreditLedger, catalog packs.Catalog, seen billing.Store, requests packs.Requests, alerts alert.Notifier, logger *slog.Logger) (*playstore.Billing, *playstore.PushVerifier, error) {
	pkg := strings.TrimSpace(os.Getenv(envGooglePackageName))
	rawSA := strings.TrimSpace(os.Getenv(envGoogleServiceAccount))
	if pkg == "" || rawSA == "" {
		logger.Info("google play billing not set up", slog.Bool("package_name", pkg != ""), slog.Bool("service_account", rawSA != ""))
		return nil, nil, nil
	}
	if catalog.Solo.GoogleProduct == "" {
		return nil, nil, fmt.Errorf("%s is set but the pack table has no solo.google_product", envGooglePackageName)
	}
	sa, err := playstore.ParseServiceAccount(rawSA)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", envGoogleServiceAccount, err)
	}
	processor := appstore.NewProcessor(tenants, accounts, ledger, catalog, seen, logger).
		WithCredits(credits).
		WithRequestAlerts(requests, alerts).
		WithSandboxAccounts(strings.Split(os.Getenv(envGoogleTestTenants), ",")...)
	b := playstore.NewBilling(playstore.NewClient(sa, pkg), processor, accounts, tenants, purchases, catalog, logger)
	var push *playstore.PushVerifier
	aud, email := strings.TrimSpace(os.Getenv(envGooglePushAudience)), strings.TrimSpace(os.Getenv(envGooglePushServiceAccount))
	if aud != "" && email != "" {
		push = playstore.NewPushVerifier(aud, email)
	}
	logger.Info("google play billing wired", slog.String("package_name", pkg), slog.String("service_account", sa.ClientEmail),
		slog.Bool("notifications", push != nil))
	return b, push, nil
}

// newDeviceSessionsURL is where a person sees the devices signed in to
// their account and signs one out: the sign-in provider's account page.
const newDeviceSessionsURL = "https://accounts.kiff.dev/user"

// buildRegionGate loads the IP-to-country table at path and checks the
// session-starting routes against it. With no path (development, tests) it
// checks nothing; a path that doesn't load, or a table too small to be whole,
// stops the API, so a broken download never lets every country in.
func buildRegionGate(path string, logger *slog.Logger) (*region.Gate, error) {
	if path == "" {
		logger.Info("region: no table; session routes aren't checked by country")
		return region.NewGate(nil, nil, logger), nil
	}
	table, err := region.Open(path)
	if err != nil {
		return nil, fmt.Errorf("region table: %w", err)
	}
	if table.Len() < region.MinRanges {
		return nil, fmt.Errorf("region table: %d ranges, fewer than %d; not a whole file", table.Len(), region.MinRanges)
	}
	logger.Info("region: table loaded", slog.Int("ranges", table.Len()), slog.String("list_effective", region.ListEffective))
	return region.NewGate(table, region.SessionRoutes, logger), nil
}
