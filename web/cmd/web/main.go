// Package main runs the kiff-cloud web binary.
//
// The binary serves both the public marketing site at kiff.dev and
// the authenticated dashboard at app.kiff.dev. It is a single Go
// service.
//
// Env-var config (read at startup):
//
//	KIFF_API_BASE_URL    apps/api root for cross-binary calls
//	KIFF_GATEWAY_BASE_URL  KIFF MCP gateway root for /tools; empty
//	                     leaves tools unmanageable from the dashboard
//	KIFF_DEMO_API_KEY    legacy operator API key for the public
//	                     receipt-render path; optional once the
//	                     per-session dashboard is the primary surface
//	KIFF_SESSION_SECRET  HMAC secret signing dashboard cookies.
//	                     Required when the dashboard auth path is
//	                     active (KIFF_AUTH_PROVIDER set). Must be
//	                     at least 32 bytes.
//	KIFF_AUTH_PROVIDER   "stub" (local dev) or "clerk" (production,
//	                     wires up via the apps/api Clerk verifier).
//	                     Empty disables the /sign-in + /dashboard
//	                     routes entirely.
//	KIFF_STUB_SUBJECTS   optional comma-separated allow-list for
//	                     the stub provider; empty allows any.
//	KIFF_COOKIE_SECURE   "true" to flag the session cookie Secure.
//	                     Default true; set "false" only for local
//	                     plain-HTTP dev.
//	KIFF_COOKIE_DOMAIN   Domain attribute for the session cookie.
//	                     Set to ".kiff.dev" in production so one
//	                     session spans the apex (kiff.dev) and the
//	                     dashboard host (app.kiff.dev), which both
//	                     proxy to this binary. Empty (default) =
//	                     host-only cookie, correct for local dev.
package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/assets"
	"github.com/kiff/cloud/web/internal/auth"
	"github.com/kiff/cloud/web/internal/content/apps"
	"github.com/kiff/cloud/web/internal/content/desktop"
	"github.com/kiff/cloud/web/internal/gatewayclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"

	clerksdk "github.com/clerk/clerk-sdk-go/v2"
)

const (
	envAPIBaseURL    = "KIFF_API_BASE_URL"
	envPublicAPIURL  = "KIFF_PUBLIC_API_URL"
	envDemoAPIKey    = "KIFF_DEMO_API_KEY"
	envSessionSecret = "KIFF_SESSION_SECRET"
	envAuthProvider  = "KIFF_AUTH_PROVIDER"
	envStubSubjects  = "KIFF_STUB_SUBJECTS"
	envCookieSecure  = "KIFF_COOKIE_SECURE"

	// envAppSignIn turns on the tafs apps' sign-in page (/sign-in/app), with
	// the API's own switch of the same name.
	envAppSignIn = "KIFF_APP_SIGNIN"

	// envGatewayBaseURL is how the web reaches the KIFF MCP gateway for
	// /tools; empty leaves /tools saying it is not available.
	envGatewayBaseURL = "KIFF_GATEWAY_BASE_URL"
	// envPublicGatewayURL is the MCP endpoint shown to users when they
	// connect an agent; empty → https://mcp.kiff.dev/mcp.
	envPublicGatewayURL = "KIFF_PUBLIC_GATEWAY_URL"

	// envCookieDomain sets the session cookie's Domain attribute.
	// Empty (default) scopes the cookie to the exact host that
	// issued it. In production the marketing apex (kiff.dev) and
	// the dashboard host (app.kiff.dev) both proxy to this same
	// binary, so a host-only cookie minted on one host is not sent
	// to the other — the user signs in on kiff.dev, gets bounced
	// to /sign-in on app.kiff.dev, and loops through Clerk forever.
	// Set this to ".kiff.dev" in production so one session spans
	// the apex + all app subdomains. Leave empty for local dev
	// (localhost is single-host).
	envCookieDomain = "KIFF_COOKIE_DOMAIN"

	// envAppHost is the canonical host the authenticated surface
	// (sign-in, sign-out, dashboard, ops) must run on. The web
	// binary answers for both kiff.dev and app.kiff.dev; the Clerk
	// sign-in handshake derives its redirect_url from the request
	// Host, so a sign-in started on the apex keeps the whole
	// Account-Portal handshake on the apex, where clerk.browser.js
	// cannot hydrate a session and the user loops forever. Set this
	// to "app.kiff.dev" in production so any gated request on the
	// apex is redirected to the app host before Clerk is involved.
	// Empty (default) disables canonicalization — correct for local
	// single-host dev.
	envAppHost = "KIFF_APP_HOST"

	// envAppleAppIDs and envAndroidApps are the apps kiff.dev vouches for, so
	// they can make and use its passkeys: /.well-known/apple-app-site-association
	// and /.well-known/assetlinks.json (the desktop client contract, §2.6).
	// Unset, each file is 404. KIFF_ANDROID_APPS is the API's same value.
	envAppleAppIDs = "KIFF_APPLE_APP_IDS"
	envAndroidApps = "KIFF_ANDROID_APPS"

	// envClerkSecretKey is the Clerk Backend API secret used to
	// verify session JWTs server-side. Same value the apps/api
	// binary reads under CLERK_SECRET_KEY; the web side needs it
	// to verify the __session cookie that Clerk drops after a
	// successful sign-in.
	envClerkSecretKey = "CLERK_SECRET_KEY"

	// envClerkPublishableKey is the public pk_live_… / pk_test_…
	// string. Currently surfaced for symmetry; future flows that
	// embed Clerk's frontend SDK will need it.
	envClerkPublishableKey = "KIFF_CLERK_PUBLISHABLE_KEY"

	// envClerkFrontendAPI is the Clerk frontend API host (without
	// scheme). For dev: "<adjective>-<noun>-NN.clerk.accounts.dev".
	// For prod with the custom CNAME wired: "clerk.kiff.dev".
	envClerkFrontendAPI = "KIFF_CLERK_FRONTEND_API"

	// Paid-activation hosted-checkout URLs. Each is read at
	// startup and rendered as the href on the corresponding
	// pricing-page CTA. Empty values fall back to a prefilled
	// mailto:hello@kiff.dev so the page always has a working CTA.
	// The operator's paid-activation runbook has the
	// procedure that turns a payment back into a paid plan.
	envLaunchPilotCheckoutURL = "KIFF_PRICING_LAUNCH_PILOT_CHECKOUT_URL"
	envTeamCheckoutURL        = "KIFF_PRICING_TEAM_CHECKOUT_URL"
	envBusinessIntakeURL      = "KIFF_PRICING_BUSINESS_INTAKE_URL"
	// The v1 offer's app link: TestFlight, then the App Store. The
	// trial goes to /dashboard/checkout.
	envAppURL = "KIFF_OFFER_APP_URL"
	// The Android app's Google Play listing, public since 10 Nov 2026;
	// the env var overrides it.
	envPlayURL   = "KIFF_OFFER_PLAY_URL"
	playStoreURL = "https://play.google.com/store/apps/details?id=dev.kiff.tafs"
	// envAppStoreLive ("on") links /download's App Store badge to tafs's
	// listing. Off until Apple approves the app, so the page never links to
	// a listing that doesn't open: the iOS side says it's coming.
	envAppStoreLive = "KIFF_APP_STORE_LIVE"
	// envWatchLive ("on") makes /tafs/watch public, once the Wear OS watch is on Google Play. Off, the page answers in no
	// language and nothing links it (pages.WatchLive).
	envWatchLive = "KIFF_WATCH_LIVE"
	// envMacSecurityKey ("on") says, on the privacy pages and /support in
	// every language, that a Mac without a Secure Enclave can approve with a
	// security key. Off until the macOS app's macPasskeyApprovals is on.
	envMacSecurityKey = "KIFF_MAC_SECURITY_KEY"
	// envAppsProved ("on") takes which apps work from planque's reviewed
	// proofs (GET {planque}/v1/proved), the same list tafs' gallery reads,
	// read at start and every 10 minutes (apps.ProvedSource). Off, the list
	// is apps.json as embedded and nothing is read.
	envAppsProved = "KIFF_APPS_PROVED"
	// envPlanqueURL is planque's root for that read; unset, planque's public
	// host.
	envPlanqueURL = "KIFF_PLANQUE_URL"
	planquePublic = "https://planque.kiff.dev"
	// envDesktopReleases ("on") takes whether each computer's app can be
	// downloaded from its repository's published GitHub release
	// (desktop.Source), read at start and every 10 minutes. Off, nothing is
	// read and every computer is coming soon.
	envDesktopReleases = "KIFF_DESKTOP_RELEASES"
	// envDesktopNotAvailable names, comma-separated, the computers the page
	// says aren't available ("windows,linux"), with the releases read on.
	envDesktopNotAvailable = "KIFF_DESKTOP_NOT_AVAILABLE"
	// envGitHubAPI is GitHub's API root for that read; unset, api.github.com.
	envGitHubAPI = "KIFF_GITHUB_API"

	// envOpsSubjects is the comma-separated allow-list of Clerk
	// subjects permitted to reach the internal operator console at
	// /ops. Empty leaves the surface unmounted. The same var gates
	// the apps/api /v1/ops/* endpoints, so set it on both binaries
	// (the deploy renders one env file per binary).
	envOpsSubjects = "KIFF_OPS_SUBJECTS"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "web:", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", ":8081", "listen address")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := checkProductionPosture(os.Getenv); err != nil {
		return err
	}
	// Before any handler or route is built: what /tafs/watch's switch says decides its routes and links.
	pages.HoldWatch(strings.TrimSpace(os.Getenv(envWatchLive)) == "on")
	// Before the docs and llms-full.txt are rendered: the first read of
	// planque's proofs, if they're on, decides the apps table they start with.
	startAppsProved(context.Background(), os.Getenv, logger)
	startDesktopReleases(context.Background(), os.Getenv, logger)

	// Legacy operator-keyed receipt path.
	publicAPIClient, err := apiclient.New(apiclient.Config{
		BaseURL: os.Getenv(envAPIBaseURL),
		APIKey:  os.Getenv(envDemoAPIKey),
	})
	if err != nil && !errors.Is(err, apiclient.ErrNotConfigured) {
		return fmt.Errorf("apiclient (operator key): %w", err)
	}
	if publicAPIClient == nil {
		logger.Info("operator-key receipt path not configured",
			slog.String("hint", "set "+envAPIBaseURL+" and "+envDemoAPIKey+" to enable /dashboard/receipts/{trace_id}"))
	}

	// Per-session API client (no key embedded; bearer is forwarded
	// per request from the user's session). Wired only when the
	// auth provider is configured.
	sessionAPIClient, err := buildSessionAPIClient(os.Getenv(envAPIBaseURL), logger)
	if err != nil {
		return fmt.Errorf("apiclient (session): %w", err)
	}

	// Sign-in provider + session manager. Either both are wired
	// or both are nil; the dashboard routes only mount when both
	// are present.
	provider, sessions, err := buildAuthStack(logger)
	if err != nil {
		return err
	}

	// BearerSource refreshes the upstream bearer on every apps/api
	// call. Clerk JWTs expire in ~60 seconds, so without this the
	// dashboard would 401 after the first minute and bounce the
	// user through sign-in on every click.
	var bearers handlers.BearerSource = handlers.StaticBearerSource()
	if minter, ok := provider.(auth.BearerMinter); ok {
		bearers = auth.NewHostedBearerSource(minter)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", handlers.AppHome(strings.TrimSpace(os.Getenv(envAppHost)), sessions, handlers.Landing()))
	demoApp := handlers.DemoApp(logger)
	mux.Handle("GET /demo", demoApp)
	delegatePage := handlers.Delegate()
	mux.Handle("GET /delegate", delegatePage)
	mux.Handle("POST /delegate/step", delegatePage)
	mux.Handle("GET /demo/", demoApp)
	mux.Handle("POST /demo/", demoApp)
	// The standalone /how-it-works page was retired as redundant with
	// the homepage "how it works" section, which itself moved to
	// /integrate when the developer-journey content came off the
	// commercial page. Redirect the old URL there so links and search
	// results don't dead-end.
	mountRemovedPages(mux)
	// /what-is-it explained the framework-era origin story and was no
	// longer linked; the docs' What KIFF is page does the same job for
	// Cards. Redirect so old links and search results still land.
	mux.Handle("GET /what-is-it", handlers.Redirect("/docs/what-kiff-is"))
	mux.Handle("GET /boundary-test", http.RedirectHandler("/pricing", http.StatusMovedPermanently))
	mux.Handle("GET /invariants", handlers.Invariants())
	mux.Handle("GET /whitepaper", handlers.Whitepaper())
	// The video gallery merged into /proof (#videos).
	mux.Handle("GET /videos", http.RedirectHandler("/proof#videos", http.StatusMovedPermanently))
	// Crawlers ask for /favicon.ico whatever the page declares.
	mux.Handle("GET /favicon.ico", http.RedirectHandler("/static/img/favicon/favicon.ico", http.StatusMovedPermanently))
	mux.Handle("GET /blog", handlers.Blog(logger))
	mux.Handle("GET /blog/tags/{tag}", handlers.Blog(logger))
	mux.Handle("GET /blog/{slug}", handlers.Blog(logger))
	mux.Handle("GET /docs", handlers.Docs(logger))
	mux.Handle("GET /docs/{slug}", handlers.Docs(logger))
	// The limits reference became the card reference: same subject, and
	// the noun a ceiling is a property of. The old path is published in
	// llms.txt and linked from posts that are dated records, so it
	// resolves rather than 404s.
	mux.Handle("GET /docs/limits", http.RedirectHandler("/docs/kiff-cards", http.StatusMovedPermanently))
	// Dated, unchanging copies of /docs and /security that the DPA and
	// the Terms point at.
	docsArchive := handlers.DocsArchive(logger)
	mux.Handle("GET /docs/archive", docsArchive)
	mux.Handle("GET /docs/archive/{rev}", docsArchive)
	mux.Handle("GET /docs/archive/{rev}/{slug}", docsArchive)
	mux.Handle("GET /docs/archive/{rev}/assets/{name}", docsArchive)
	mux.Handle("GET /deck", handlers.Deck(logger))
	mux.Handle("GET /deck/{slug}", handlers.Deck(logger))
	mux.Handle("GET /proof", handlers.Proof(logger))
	mux.Handle("GET /proof/videos/{video}", handlers.ProofVideo(logger))
	mux.Handle("GET /proof/{slug}", handlers.Proof(logger))
	mux.Handle("GET /spot", handlers.Spot(logger))
	mux.Handle("GET /spot/{slug}", handlers.Spot(logger))
	// The pain-routed /solutions pages were folded into the homepage
	// "use cases" section. Redirect the retired URLs so old links and
	// search results land on the new section instead of the catch-all
	// homepage.
	mux.Handle("GET /solutions", handlers.Redirect("/#use-cases"))
	mux.Handle("GET /solutions/{slug}", handlers.Redirect("/#use-cases"))
	mux.Handle("GET /security", handlers.Security())
	pages.MacSecurityKeyApprovals = strings.TrimSpace(os.Getenv(envMacSecurityKey)) == "on"
	offer := handlers.OfferLinks{
		AppURL:  strings.TrimSpace(os.Getenv(envAppURL)),
		PlayURL: cmp.Or(strings.TrimSpace(os.Getenv(envPlayURL)), playStoreURL),
		// The translated /tafs/download links the App Store as the English one does.
		AppStoreLive: strings.TrimSpace(os.Getenv(envAppStoreLive)) == "on",
	}
	mux.Handle("GET /pricing", handlers.Pricing(handlers.PricingLinks{
		Offer:                  offer,
		LaunchPilotCheckoutURL: strings.TrimSpace(os.Getenv(envLaunchPilotCheckoutURL)),
		TeamCheckoutURL:        strings.TrimSpace(os.Getenv(envTeamCheckoutURL)),
		BusinessIntakeURL:      strings.TrimSpace(os.Getenv(envBusinessIntakeURL)),
	}))
	mux.Handle("GET /privacy", handlers.Privacy())
	mux.Handle("GET /account/deleted", handlers.AccountDeleted(sessions))
	mux.Handle("GET /terms", handlers.Terms())
	mux.Handle("GET /legal/dpa", handlers.DPA())
	mux.Handle("GET /legal/sla", handlers.SLA())
	mux.Handle("GET /acceptable-use", handlers.AcceptableUse())
	mux.Handle("GET /subprocessors", handlers.Subprocessors())
	// What a country's law adds to the two privacy policies, in English and in
	// the country's language (internal/content/country).
	mux.Handle("GET /privacy/countries", handlers.CountryIndex())
	mux.Handle("GET /privacy/countries/{country}", handlers.CountrySection())
	mux.Handle("GET /privacy/countries/{country}/{lang}", handlers.CountrySection())
	mux.Handle("GET /cookies", handlers.Cookies())
	// The DPA lives at /legal/dpa; /dpa is the short form.
	mux.Handle("GET /dpa", handlers.Redirect("/legal/dpa"))
	mux.Handle("GET /support", handlers.Support())
	mux.Handle("GET /tafs", handlers.Tafs(offer))
	// What you download is the tafs app, so the page lives under /tafs;
	// kiff.dev/download is its short link, for print (owner, 13 Nov 2026).
	mux.Handle("GET /tafs/download", handlers.Download(offer, strings.TrimSpace(os.Getenv(envAppStoreLive)) == "on"))
	mux.Handle("GET /download", handlers.Redirect("/tafs/download"))
	// tafs on a watch, in English, once it's live; its translations are routed below, or held back with it.
	if pages.WatchLive {
		mux.Handle("GET /tafs/watch", handlers.TafsWatch())
	}
	// tafs's own privacy policy and terms, in English (Google's review of
	// tafs's sign-in reads /tafs/privacy#google). Their translations are
	// routed with the other translated pages below.
	mux.Handle("GET /tafs/privacy", handlers.TafsPrivacy())
	mux.Handle("GET /tafs/terms", handlers.TafsTerms())
	// The pages a tafs user reaches, in the other seven languages tafs
	// speaks, each at its own path ("/de/privacy"). English stays above.
	for _, tr := range handlers.TranslatedRoutes() {
		mux.Handle("GET "+tr.Path, handlers.Translated(tr.Lang, tr.Page, offer))
	}
	// "/de" and "/de/" hold no page: each language's bare prefix sends a
	// reader to its /tafs. "/de/" is registered as "/de/{$}", the prefix
	// alone, so it never catches "/de/anything-else".
	for from, to := range handlers.LanguageRootTargets() {
		pattern := "GET " + from
		if strings.HasSuffix(from, "/") {
			pattern += "{$}"
		}
		mux.Handle(pattern, handlers.LanguageRoot(to))
	}
	mux.Handle("GET /llms.txt", handlers.LLMsText())
	// llms-full.txt carries the public pages' text, rendered once here by
	// the server's own handler (every public route is mounted above), as
	// an anonymous visitor on kiff.dev gets them.
	mux.Handle("GET /llms-full.txt", handlers.LLMsFullText(serverHandler(mux, strings.TrimSpace(os.Getenv(envAppHost))), logger))
	mux.Handle("GET /robots.txt", handlers.Robots())
	mux.Handle("GET /sitemap.xml", handlers.Sitemap())
	mux.Handle("GET /skills/kiff-domains.md", handlers.SkillDomains())
	mux.Handle("GET /skills/kiff.md", handlers.SkillKiff())
	mux.Handle("GET /.well-known/security.txt", handlers.SecurityTxt())
	appleApps, err := handlers.ParseAppleAppIDs(os.Getenv(envAppleAppIDs))
	if err != nil {
		return err
	}
	androidApps, err := handlers.ParseAndroidApps(os.Getenv(envAndroidApps))
	if err != nil {
		return err
	}
	mux.Handle("GET /.well-known/apple-app-site-association", handlers.AppleAppSiteAssociation(appleApps))
	mux.Handle("GET /.well-known/assetlinks.json", handlers.AssetLinks(androidApps))
	mux.Handle("GET /healthz", handlers.Health())
	mux.Handle("GET /static/", assets.Handler())

	if provider != nil && sessions != nil {
		authH := handlers.NewAuthHandler(provider, sessions, logger)
		authH.Register(mux)
		// /sign-in/app — the tafs apps' sign-in, in the app's sheet. Off
		// unless KIFF_APP_SIGNIN is "on", the API's own switch: off, it
		// says so and does nothing else.
		appSignIn := strings.TrimSpace(os.Getenv(envAppSignIn)) == "on"
		handlers.NewAppSignInHandler(appSignIn, sessionAPIClient, sessions, bearers, provider, logger).Register(mux)

		// /dashboard surface — both the index and the action
		// handlers (key mint, template apply, send event,
		// approvals) sit behind RequireSession. The dashboard
		// handler also serves the Settings tab and the 301
		// redirects from the retired /dashboard/start* IA.
		dashH := handlers.NewDashboardHandler(sessionAPIClient, sessions, bearers, logger)
		// The connect snippet shows the public API endpoint (not the
		// web's internal reach-the-API URL). Empty → the page defaults
		// to https://api.kiff.dev.
		dashH.PublicAPIURL = strings.TrimSpace(os.Getenv(envPublicAPIURL))
		if base := strings.TrimSpace(os.Getenv(envGatewayBaseURL)); base != "" {
			gw, err := gatewayclient.New(base)
			if err != nil {
				logger.Error("gateway client", slog.String("err", err.Error()))
				os.Exit(1)
			}
			dashH.SetGateway(gw)
		}
		dashH.PublicGatewayURL = strings.TrimSpace(os.Getenv(envPublicGatewayURL))
		dashboardMux := http.NewServeMux()
		dashH.Register(dashboardMux)

		// /dashboard/build — KIFF Studio authoring surface
		// (MVP-2 /, IA promoted to top-level tab). Mounted on
		// the same gated mux so it inherits RequireSession.
		studioH := handlers.NewStudioHandler(sessionAPIClient, sessions, bearers, logger)
		studioH.Register(dashboardMux)

		// /auth/device — the browser approval page for `kiff auth
		// login`. Mounted on the same gated mux so it inherits
		// RequireSession: an unauthenticated visitor is bounced through
		// sign-in (carrying the code in ?next) and returns here to
		// approve.
		handlers.NewDeviceApproveHandler(sessionAPIClient, sessions, bearers, logger).Register(dashboardMux)

		// /oauth/consent — an app asks to connect an agent to the KIFF
		// gateway. Gated like /auth/device.
		handlers.NewOAuthConsentHandler(sessionAPIClient, sessions, bearers, logger).Register(dashboardMux)
		// /oauth/device — the device flow's approval page: a CLI prints
		// a code; an admin or editor checks it here.
		handlers.NewOAuthDeviceHandler(sessionAPIClient, sessions, bearers, logger).Register(dashboardMux)

		// The people on an account and the account switcher.
		// SelectTenant keeps the session's account in line with the
		// person's memberships and sends it to the API on every call.
		accountsH := handlers.NewAccountsHandler(sessionAPIClient, sessions, bearers, logger)
		accountsH.Register(dashboardMux)
		handlers.NewPeopleHandler(sessionAPIClient, sessions, bearers, logger).Register(dashboardMux)
		// /invite/{token} sits behind RequireSession only: the shell and
		// SelectTenant would create an account for someone arriving to
		// join another one.
		inviteMux := http.NewServeMux()
		accountsH.RegisterInvite(inviteMux)
		inviteGated := handlers.RequireSession(sessions)(inviteMux)
		mux.Handle("GET /invite/", inviteGated)
		mux.Handle("POST /invite/", inviteGated)

		// WithChrome enriches GET renders with the shared shell's context
		// data (notification bell + domain selector), inside RequireSession
		// so it has the session, outside the mux so every dashboard surface
		// — including Studio and the device page — gets them.
		gated := handlers.RequireSession(sessions)(accountsH.SelectTenant(dashH.WithChrome(dashboardMux)))
		// The inner mux already disambiguates by method (GET vs
		// POST). Pass-through with a method-less mount on the
		// outer mux would conflict with "GET /" (the landing
		// page); scope explicitly to GET + POST so the outer
		// mux can route both.
		mountSignedInApp(mux, gated)

		// /ops — internal operator console. Its own gated mux
		// wrapped with RequireSession AND RequireOperator, so a
		// tenant session that is not on the KIFF_OPS_SUBJECTS
		// allow-list gets 404 (the surface stays invisible). Mounted
		// only when at least one operator subject is configured; the
		// data boundary lives in apps/api's /v1/ops/* endpoints,
		// which re-check the same allow-list.
		opsSubjects := handlers.ParseOperatorSubjects(os.Getenv(envOpsSubjects))
		if len(opsSubjects) > 0 {
			opsH := handlers.NewOpsHandler(sessionAPIClient, sessions, bearers, logger)
			opsMux := http.NewServeMux()
			opsH.Register(opsMux)
			// RequireOperator is the console's gate. apps/api's route policy
			// lets any person's POST /v1/ops/* through (CapPersonal) on
			// purpose and defers to the same allow-list there; tightening
			// one does not make the other redundant.
			gatedOps := handlers.RequireSession(sessions)(handlers.RequireOperator(opsSubjects)(accountsH.SelectTenant(opsMux)))
			// Mount both GET and POST under /ops/ (via the shared
			// builder) so the console's POST mutation forms reach the
			// gated handler instead of 405ing at the outer router.
			// The guard test drives the same MountOps, so the wiring
			// can't silently regress.
			handlers.MountOps(mux, gatedOps)
			logger.Info("operator console wired",
				slog.Int("operator_subjects", len(opsSubjects)),
			)
		}

		logger.Info("dashboard auth wired",
			slog.String("provider", provider.Name()),
			slog.Bool("session_api_configured", sessionAPIClient != nil),
		)
	} else {
		// No dashboard auth path mounted (public / unauthenticated
		// deploy). The session-gated receipt route above isn't
		// reachable, so fall back to the legacy operator-keyed
		// receipt page for public receipt links. When auth IS
		// wired, the gated DashboardHandler.receipt serves
		// /dashboard/receipts/{trace_id} through the user's own
		// session — no operator key required.
		mux.Handle("GET /dashboard/receipts/{trace_id}", handlers.Receipt(publicAPIClient, logger))
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           serverHandler(mux, strings.TrimSpace(os.Getenv(envAppHost))),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("web listening", slog.String("addr", *addr))
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

// buildSessionAPIClient returns a per-session apiclient.Client
// (no embedded API key) when KIFF_API_BASE_URL is set, or nil
// otherwise. The dashboard's /v1/me probe and future fetches go
// through this client.
func buildSessionAPIClient(baseURL string, logger *slog.Logger) (*apiclient.Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		logger.Info("session apiclient not configured (no KIFF_API_BASE_URL); /dashboard will render in API-less mode")
		return nil, nil
	}
	c, err := apiclient.NewSessionAware(baseURL, 0)
	if err != nil {
		return nil, err
	}
	logger.Info("session apiclient ready", slog.String("api_base_url", c.BaseURL()))
	return c, nil
}

// buildAuthStack returns the configured auth.Provider plus the
// session.Manager, or (nil, nil, nil) when the dashboard auth
// path is disabled (KIFF_AUTH_PROVIDER unset). When the provider
// is set, KIFF_SESSION_SECRET must also be set, or this function
// errors out fail-fast.
func buildAuthStack(logger *slog.Logger) (auth.Provider, *session.Manager, error) {
	provName := strings.TrimSpace(os.Getenv(envAuthProvider))
	if provName == "" {
		logger.Info("dashboard auth disabled (KIFF_AUTH_PROVIDER unset); /sign-in and /dashboard routes will not be mounted")
		return nil, nil, nil
	}

	secret, err := loadSessionSecret(logger)
	if err != nil {
		return nil, nil, err
	}

	cookieSecure := parseBoolEnv(os.Getenv(envCookieSecure), true)
	cookieDomain := strings.TrimSpace(os.Getenv(envCookieDomain))
	sessionOpts := []session.ManagerOption{session.WithSecure(cookieSecure)}
	if cookieDomain != "" {
		sessionOpts = append(sessionOpts, session.WithDomain(cookieDomain))
	}
	mgr, err := session.NewManager(secret, sessionOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("session manager: %w", err)
	}

	switch auth.ProviderName(provName) {
	case auth.ProviderStub:
		allowed := splitCSV(os.Getenv(envStubSubjects))
		logger.Info("auth provider: stub",
			slog.Bool("cookie_secure", cookieSecure),
			slog.String("cookie_domain", cookieDomain),
			slog.Int("allowed_subjects", len(allowed)),
		)
		return auth.NewStubProvider(nil, allowed...), mgr, nil
	case auth.ProviderClerk:
		secret := strings.TrimSpace(os.Getenv(envClerkSecretKey))
		if secret == "" {
			return nil, nil, fmt.Errorf("KIFF_AUTH_PROVIDER=clerk requires %s to be set", envClerkSecretKey)
		}
		clerksdk.SetKey(secret)
		frontend := strings.TrimSpace(os.Getenv(envClerkFrontendAPI))
		if frontend == "" {
			return nil, nil, fmt.Errorf("KIFF_AUTH_PROVIDER=clerk requires %s (e.g. clerk.kiff.dev for prod or <name>.clerk.accounts.dev for dev)", envClerkFrontendAPI)
		}
		publishable := strings.TrimSpace(os.Getenv(envClerkPublishableKey))
		clerkProvider, err := auth.NewClerkProvider(frontend, publishable)
		if err != nil {
			return nil, nil, fmt.Errorf("clerk provider: %w", err)
		}
		logger.Info("auth provider: clerk",
			slog.Bool("cookie_secure", cookieSecure),
			slog.String("cookie_domain", cookieDomain),
			slog.String("frontend_api", frontend),
		)
		return clerkProvider, mgr, nil
	default:
		return nil, nil, fmt.Errorf("KIFF_AUTH_PROVIDER=%q: unknown (expected stub or clerk)", provName)
	}
}

// loadSessionSecret reads KIFF_SESSION_SECRET, validates length,
// and returns the bytes. When unset and KIFF_AUTH_PROVIDER=stub,
// generates a random per-process secret with a warning — local
// dev shouldn't need to set the secret to use the stub. When
// unset in any other mode, returns an error.
func loadSessionSecret(logger *slog.Logger) ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(envSessionSecret))
	if raw != "" {
		if len(raw) < 32 {
			return nil, fmt.Errorf("%s must be at least 32 bytes (got %d)", envSessionSecret, len(raw))
		}
		return []byte(raw), nil
	}
	provider := strings.TrimSpace(os.Getenv(envAuthProvider))
	if provider == string(auth.ProviderStub) {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return nil, fmt.Errorf("generate dev session secret: %w", err)
		}
		logger.Warn("KIFF_SESSION_SECRET not set; generated an ephemeral one for stub mode (sessions invalidate on every restart)",
			slog.String("hint", "set KIFF_SESSION_SECRET to a 32+ byte string to keep sessions across restarts"),
		)
		return []byte(hex.EncodeToString(buf)), nil
	}
	return nil, fmt.Errorf("%s is required when %s=%s", envSessionSecret, envAuthProvider, provider)
}

func parseBoolEnv(raw string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return fallback
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		return fallback
	}
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

// signedInAppMounts are the outer-mux patterns that reach the signed-in
// app. The inner dashboard mux disambiguates further; the outer mux has
// "GET /" (the landing page), so a signed-in route whose top-level path
// is not listed here is unreachable: the request falls through and
// serves the landing page. That happened to /agent-cards, and to
// /agents when the Agents view shipped. Adding a top-level signed-in
// path means adding it here; TestSignedInAppMounts pins them.
var signedInAppMounts = []string{
	"GET /dashboard", "GET /dashboard/", "POST /dashboard/",
	// The device approval page lives at /auth/device, not under
	// /dashboard.
	"GET /auth/device", "POST /auth/device",
	// The consent page for apps connecting an agent.
	"GET /oauth/consent", "POST /oauth/consent",
	// The device flow's approval page.
	"GET /oauth/device", "POST /oauth/device",
	// Every agent connection, whatever the client.
	"GET /connections", "POST /connections/",
	"GET /cards", "GET /cards/", "POST /cards", "POST /cards/",
	// Kept as a redirect so the old path resolves.
	"GET /agent-cards",
	"GET /agents", "GET /agents/", "POST /agents", "POST /agents/",
	// Actions held outside a Card (the mandates design
	// amendment).
	"GET /needs-you", "GET /needs-you/", "POST /needs-you/",
	// Every call, with its receipt.
	"GET /activity", "GET /activity/",
	// Tools connected to the KIFF gateway.
	"GET /tools", "GET /tools/", "POST /tools", "POST /tools/",
	// "For me" or "for a company", asked once.
	"GET /welcome", "POST /welcome",
}

// serverHandler is everything the server answers, wrapped. Cross-origin
// state changes are refused first, on every route, so a new form is
// covered without doing anything. A page asked for with a trailing
// slash is sent to its path without it, on every route too.
func serverHandler(mux *http.ServeMux, appHost string) http.Handler {
	return handlers.SameOriginOnly(handlers.NormalizeTypography(handlers.CanonicalAppHost(appHost)(handlers.CanonicalPath(handlers.TrailingSlash(mux)))))
}

func mountSignedInApp(mux *http.ServeMux, gated http.Handler) {
	for _, pattern := range signedInAppMounts {
		mux.Handle(pattern, gated)
	}
}

// removedPages are public pages that became docs (owner, 13 Nov 2026: fewer
// pages). Each URL answers a permanent redirect to the page that carries it.
var removedPages = map[string]string{
	"/how-it-works": "/docs/connect-an-agent",
	"/integrate":    "/docs/connect-an-agent",
	"/builders":     "/docs/quickstart",
	"/agent":        "/docs/what-kiff-is",
	// The owner's page map (13 Nov 2026): /why into home, /on-rails into
	// /pricing's "Larger" row, /ai-act into the docs, in every language.
	"/why":       "/",
	"/on-rails":  "/pricing",
	"/ai-act":    "/docs/ai-act",
	"/de/ai-act": "/docs/ai-act", "/fr/ai-act": "/docs/ai-act", "/es/ai-act": "/docs/ai-act",
	"/it/ai-act": "/docs/ai-act", "/pt/ai-act": "/docs/ai-act", "/nl/ai-act": "/docs/ai-act",
	"/pl/ai-act": "/docs/ai-act",
}

func mountRemovedPages(mux *http.ServeMux) {
	for from, to := range removedPages {
		mux.Handle("GET "+from, handlers.Redirect(to))
	}
}

// startAppsProved reads planque's proofs into the apps list when
// KIFF_APPS_PROVED is on, and keeps them current; off, it reads nothing.
func startAppsProved(ctx context.Context, getenv func(string) string, logger *slog.Logger) bool {
	if strings.TrimSpace(getenv(envAppsProved)) != "on" {
		return false
	}
	base := strings.TrimSpace(getenv(envPlanqueURL))
	if base == "" {
		base = planquePublic
	}
	src := &apps.ProvedSource{Base: base, Logf: func(format string, args ...any) {
		logger.Warn(fmt.Sprintf(format, args...))
	}}
	src.Start(ctx)
	return true
}

// startDesktopReleases reads each computer's published release into the
// download page when KIFF_DESKTOP_RELEASES is on, and keeps it current, with
// the computers KIFF_DESKTOP_NOT_AVAILABLE names held back; off, it reads
// nothing and holds nothing back.
func startDesktopReleases(ctx context.Context, getenv func(string) string, logger *slog.Logger) bool {
	if strings.TrimSpace(getenv(envDesktopReleases)) != "on" {
		return false
	}
	desktop.HoldNotAvailable(getenv(envDesktopNotAvailable))
	src := &desktop.Source{API: strings.TrimSpace(getenv(envGitHubAPI)), Logf: func(format string, args ...any) {
		logger.Warn(fmt.Sprintf(format, args...))
	}}
	src.Start(ctx)
	return true
}
