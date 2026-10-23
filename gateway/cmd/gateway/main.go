// Command gateway runs kiff-gateway: the MCP endpoint agents
// connect to at mcp.kiff.dev. Every tool call is decided by apps/api with
// the agent's own key, then forwarded, held for the owner, or refused.
//
//	gateway [-addr=:8282]        serve /mcp (agents) and /v1/connections (owners)
//	gateway new-key -id=<id>     print a new credential key entry, id:base64
//	gateway rotate-credentials   re-encrypt every stored credential with the
//	                             current (first) key
//
// Environment:
//
//	KIFF_DATABASE_URL                Postgres for the gateway's own tables (required)
//	KIFF_GATEWAY_CREDENTIAL_KEYS     credential keys, "id:base64,…", current first
//	                                 (required; only the gateway receives it)
//	KIFF_API_URL                     apps/api base URL (default http://127.0.0.1:8080)
//	KIFF_APP_BASE_URL                dashboard origin for Needs you links
//	KIFF_GATEWAY_ALLOW_PRIVATE_TOOLS true lets tools use http and private addresses
//	                                 (local development only)
//	KIFF_GATEWAY_CHAT_CLIENTS        clients a held call may be offered to in the chat,
//	                                 "name>=version,…" (default claude-code>=2.1.287;
//	                                 "none" offers it to none). How the owner is told,
//	                                 never who may answer.
//	KIFF_LOG_FORMAT                  json (default) or text
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/gateway/internal/connection"
	"github.com/kiff/cloud/gateway/internal/dbrole"
	"github.com/kiff/cloud/gateway/internal/egress"
	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
	"github.com/kiff/cloud/gateway/internal/mcp"
	"github.com/kiff/cloud/gateway/internal/requestlog"
	"github.com/kiff/cloud/gateway/internal/secret"
)

const version = "0.1.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "gateway:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	var handler slog.Handler = slog.NewJSONHandler(os.Stderr, nil)
	if os.Getenv("KIFF_LOG_FORMAT") == "text" {
		handler = slog.NewTextHandler(os.Stderr, nil)
	}
	logger := slog.New(requestlog.Handler(handler))

	if len(args) > 0 && args[0] == "db-role" {
		// Run with the database's administrative login, before the gateway
		// starts with its own. The password comes in through the
		// environment so it never appears in argv.
		admin := os.Getenv("KIFF_ADMIN_DATABASE_URL")
		password := os.Getenv("KIFF_GATEWAY_DB_PASSWORD")
		if admin == "" || password == "" {
			return errors.New("db-role needs KIFF_ADMIN_DATABASE_URL and KIFF_GATEWAY_DB_PASSWORD")
		}
		ctx := context.Background()
		pool, err := pgxpool.New(ctx, admin)
		if err != nil {
			return err
		}
		defer pool.Close()
		if err := dbrole.Setup(ctx, pool, dbrole.Role, password); err != nil {
			return err
		}
		fmt.Println("gateway database role ready:", dbrole.Role)
		return nil
	}

	if len(args) > 0 && args[0] == "new-key" {
		fs := flag.NewFlagSet("new-key", flag.ContinueOnError)
		id := fs.String("id", "", "key id, e.g. v2")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		k, err := secret.NewKey(*id)
		if err != nil {
			return err
		}
		fmt.Println(k)
		return nil
	}
	keys, err := secret.ParseKeyring(os.Getenv("KIFF_GATEWAY_CREDENTIAL_KEYS"))
	if err != nil {
		return fmt.Errorf("KIFF_GATEWAY_CREDENTIAL_KEYS: %w", err)
	}
	dbURL := os.Getenv("KIFF_DATABASE_URL")
	if dbURL == "" {
		return errors.New("KIFF_DATABASE_URL is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := invocation.ApplySchema(ctx, pool); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	catalog := gateway.PostgresCatalog{Pool: pool}
	conns := &connection.Store{Pool: pool, Keys: keys}

	if len(args) > 0 && args[0] == "rotate-credentials" {
		n, err := conns.Reseal(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("re-encrypted %d credential(s) with key %s\n", n, keys.Current())
		return nil
	}

	fs := flag.NewFlagSet("gateway", flag.ContinueOnError)
	addr := fs.String("addr", ":8282", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	apiURL := os.Getenv("KIFF_API_URL")
	if apiURL == "" {
		apiURL = "http://127.0.0.1:8080"
	}
	policy := egress.Policy{AllowPrivate: os.Getenv("KIFF_GATEWAY_ALLOW_PRIVATE_TOOLS") == "true"}
	if policy.AllowPrivate {
		logger.Warn("gateway: KIFF_GATEWAY_ALLOW_PRIVATE_TOOLS is set; tools may use http and private addresses")
	}
	remote := &mcp.Client{HTTP: policy.Client(30 * time.Second), Version: version}
	api := kiffapi.New(apiURL, nil)
	g := &gateway.Gateway{
		API:            api,
		Catalog:        catalog,
		Store:          invocation.NewStore(pool),
		Forward:        remote,
		Credentials:    conns,
		AppURL:         os.Getenv("KIFF_APP_BASE_URL"),
		ForwardTimeout: 30 * time.Second,
		Logger:         logger,
		Clients:        gateway.PostgresClients{Pool: pool},
		ChatClients:    gateway.ParseChatClients(envOr("KIFF_GATEWAY_CHAT_CLIENTS", gateway.DefaultChatClients)),
		// OAuth discovery; "off" disables it.
		Resource: envOr("KIFF_GATEWAY_RESOURCE", "https://mcp.kiff.dev/mcp"),
		Issuer:   envOr("KIFF_OAUTH_ISSUER", "https://api.kiff.dev"),
	}
	if g.Resource == "off" {
		g.Resource = ""
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", g.Server(version))
	g.RegisterReconciliation(mux)
	if g.Resource != "" {
		mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", g.ResourceMetadata)
		mux.HandleFunc("GET /.well-known/oauth-protected-resource", g.ResourceMetadata)
		// Connect links: one URL per agent link.
		mux.HandleFunc("GET /.well-known/oauth-protected-resource/a/{id}/mcp", g.LinkResourceMetadata)
		mux.HandleFunc("/a/{id}/mcp", func(w http.ResponseWriter, r *http.Request) {
			id := r.PathValue("id")
			if !gateway.ValidLinkID(id) {
				http.NotFound(w, r)
				return
			}
			g.LinkServer(version, id).ServeHTTP(w, r)
		})
	}
	(&connection.Handler{Store: conns, API: api, Remote: remote, Policy: policy, Logger: logger}).Register(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })

	srv := &http.Server{Addr: *addr, Handler: requestlog.Wrap(logger, mux), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			if n, err := g.SweepStale(ctx); err != nil {
				logger.Error("gateway: stale sweep failed", slog.String("err", err.Error()))
			} else if n > 0 {
				logger.Warn("gateway: resolved calls abandoned by a stopped process", slog.Int64("count", n))
			}
			if _, err := g.SweepResults(ctx); err != nil {
				logger.Error("gateway: result sweep failed", slog.String("err", err.Error()))
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	logger.Info("gateway listening", slog.String("addr", *addr), slog.String("api", apiURL))
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
