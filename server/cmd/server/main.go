package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jimeng-relay/server/internal/config"
	adminhandler "github.com/jimeng-relay/server/internal/handler/admin"
	"github.com/jimeng-relay/server/internal/handler/health"
	relayhandler "github.com/jimeng-relay/server/internal/handler/relay"
	"github.com/jimeng-relay/server/internal/logging"
	adminmiddleware "github.com/jimeng-relay/server/internal/middleware/admin"
	"github.com/jimeng-relay/server/internal/middleware/observability"
	"github.com/jimeng-relay/server/internal/middleware/sigv4"
	"github.com/jimeng-relay/server/internal/relay/upstream"
	"github.com/jimeng-relay/server/internal/repository"
	"github.com/jimeng-relay/server/internal/repository/postgres"
	"github.com/jimeng-relay/server/internal/repository/sqlite"
	"github.com/jimeng-relay/server/internal/secretcrypto"
	adminservice "github.com/jimeng-relay/server/internal/service/admin"
	apikeyservice "github.com/jimeng-relay/server/internal/service/apikey"
	auditservice "github.com/jimeng-relay/server/internal/service/audit"
	idempotencyservice "github.com/jimeng-relay/server/internal/service/idempotency"
	"github.com/jimeng-relay/server/internal/service/keymanager"
)

const (
	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 30 * time.Second
	defaultWriteTimeout      = 60 * time.Second
	defaultIdleTimeout       = 120 * time.Second
	defaultMaxHeaderBytes    = 1 << 20
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Fatalf("server command failed: %v", err)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return runServer()
	}

	switch args[0] {
	case "serve":
		if len(args) > 1 {
			return fmt.Errorf("unexpected arguments for serve: %s", strings.Join(args[1:], " "))
		}
		return runServer()
	case "key":
		return runKeyCommand(args[1:], out)
	case "help", "-h", "--help":
		return printUsage(out)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runServer() error {
	cfg, err := config.Load(config.Options{})
	if err != nil {
		return fmt.Errorf("missing required configuration: %w", err)
	}

	logger := logging.NewLogger(slog.LevelInfo)
	if strings.ToLower(os.Getenv("DEBUG")) == "true" {
		logger = logging.NewLogger(slog.LevelDebug)
		log.Printf("DEBUG mode enabled")
	}

	ctx := context.Background()
	repos, cleanup, err := openRepositories(ctx, cfg)
	if err != nil {
		return err
	}
	defer cleanup()
	secretCipher, err := newSecretCipher(cfg.APIKeyEncryptionKey)
	if err != nil {
		return err
	}

	auditSvc := auditservice.NewService(repos.DownstreamRequests, repos.UpstreamAttempts, repos.AuditEvents, auditservice.Config{})
	idempotencySvc := idempotencyservice.NewService(repos.IdempotencyRecords, idempotencyservice.Config{})
	adminSvc := adminservice.NewService(repos.AdminUsers, repos.AdminSessions, adminservice.Config{})
	emailCfg := config.LoadEmailConfigFromEnv()
	emailProvider := adminservice.NewEmailProvider(emailCfg, logger)
	keyManager := keymanager.NewService(logger)
	upstreamClient, err := upstream.NewClient(cfg, upstream.Options{KeyManager: keyManager})
	if err != nil {
		return fmt.Errorf("init upstream client: %w", err)
	}
	authn := sigv4.New(repos.APIKeys, sigv4.Config{SecretCipher: secretCipher, ExpectedRegion: cfg.Region, ExpectedService: "cv"})
	app := http.NewServeMux()
	submitRoutes := relayhandler.NewSubmitHandler(upstreamClient, auditSvc, nil, idempotencySvc, repos.IdempotencyRecords, logger).Routes()
	getResultRoutes := relayhandler.NewGetResultHandler(upstreamClient, auditSvc, nil, logger).Routes()
	adminBootstrapRoutes := adminhandler.NewBootstrapHandler(adminSvc, logger).Routes()
	adminResetRoutes := adminhandler.NewResetHandler(repos.AdminUsers, repos.PasswordResetTokens, emailProvider, logger, adminhandler.ResetHandlerConfig{ResetBaseURL: emailCfg.AdminResetBaseURL}).Routes()
	adminAuthHandler := adminhandler.NewAuthHandler(repos.AdminUsers, repos.AdminSessions, adminhandler.AuthConfig{})
	app.Handle("/v1/submit", submitRoutes)
	app.Handle("/v1/get-result", getResultRoutes)
	app.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("Action")
		switch action {
		case "CVSync2AsyncSubmitTask":
			submitRoutes.ServeHTTP(w, r)
		case "CVSync2AsyncGetResult":
			getResultRoutes.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})

	obs := observability.Middleware(logger)
	mux := http.NewServeMux()

	// Health endpoints (no auth required)
	healthHandler := health.NewHandler(nil)
	mux.HandleFunc("/health", healthHandler.Health)
	mux.HandleFunc("/ready", healthHandler.Ready)

	adminRoutes := http.NewServeMux()
	adminRoutes.HandleFunc("/admin/login", adminAuthHandler.Login)
	adminRoutes.HandleFunc("/admin/logout", adminAuthHandler.Logout)
	adminRoutes.Handle("/admin/bootstrap", adminBootstrapRoutes)
	adminRoutes.Handle("/admin/reset-request", adminResetRoutes)
	adminRoutes.Handle("/admin/reset", adminResetRoutes)
	adminRoutes.HandleFunc("/admin/keys", adminhandler.CreateKey)
	adminRoutes.HandleFunc("/admin/pricing", adminhandler.UpsertPricing)
	adminRoutes.HandleFunc("/admin/budget", adminhandler.SetBudget)
	adminRoutes.HandleFunc("/admin/multiplier", adminhandler.SetMultiplier)
	adminAuthn := adminmiddleware.New(repos.AdminSessions, repos.AdminUsers, adminmiddleware.Config{CookieName: "admin_session"})
	adminChain := observability.RecoverMiddleware(logger)(obs(adminAuthn(adminRoutes)))
	mux.Handle("/admin/", adminChain)
	mux.Handle("/admin", adminChain)

	mux.Handle("/", observability.RecoverMiddleware(logger)(obs(authn(app))))

	log.Printf("Starting jimeng-relay server on port %s...", cfg.ServerPort)
	log.Printf("Upstream concurrent limit: %d, queue size: %d", cfg.UpstreamMaxConcurrent, cfg.UpstreamMaxQueue)
	log.Printf("Upstream submit min interval: %s", cfg.UpstreamSubmitMinInterval)
	log.Printf("API key lifecycle is managed via CLI: ./jimeng-server key ...")
	log.Printf("Registered relay submit routes: POST /v1/submit, POST /?Action=CVSync2AsyncSubmitTask")
	log.Printf("Registered relay get-result routes: POST /v1/get-result, POST /?Action=CVSync2AsyncGetResult")
	log.Printf("Registered admin route: POST /admin/bootstrap")
	log.Printf("Registered admin routes: POST /admin/login, POST /admin/logout, POST /admin/reset-request, POST /admin/reset")
	srv := &http.Server{
		Addr:              ":" + cfg.ServerPort,
		Handler:           mux,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		ReadTimeout:       defaultReadTimeout,
		WriteTimeout:      defaultWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
		MaxHeaderBytes:    defaultMaxHeaderBytes,
	}

	if err := srv.ListenAndServe(); err != nil {
		return fmt.Errorf("listen on :%s: %w", cfg.ServerPort, err)
	}
	return nil
}

type repositories struct {
	APIKeys             repository.APIKeyRepository
	DownstreamRequests  repository.DownstreamRequestRepository
	UpstreamAttempts    repository.UpstreamAttemptRepository
	AuditEvents         repository.AuditEventRepository
	IdempotencyRecords  repository.IdempotencyRecordRepository
	AdminUsers          repository.AdminUserRepository
	AdminSessions       repository.AdminSessionRepository
	PasswordResetTokens repository.PasswordResetTokenRepository
}

func openRepositories(ctx context.Context, cfg config.Config) (repositories, func(), error) {
	dbType := strings.ToLower(strings.TrimSpace(cfg.DatabaseType))
	switch dbType {
	case "", "sqlite":
		repos, err := sqlite.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			return repositories{}, nil, fmt.Errorf("open sqlite repository: %w", err)
		}
		cleanup := func() { _ = repos.Close() }
		return repositories{APIKeys: repos.APIKeys, DownstreamRequests: repos.DownstreamRequests, UpstreamAttempts: repos.UpstreamAttempts, AuditEvents: repos.AuditEvents, IdempotencyRecords: repos.IdempotencyRecords, AdminUsers: repos.AdminUsers, AdminSessions: repos.AdminSessions, PasswordResetTokens: repos.PasswordResetTokens}, cleanup, nil
	case "postgres", "postgresql":
		db, err := postgres.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			return repositories{}, nil, fmt.Errorf("open postgres repository: %w", err)
		}
		return repositories{APIKeys: db.APIKeys(), DownstreamRequests: db.DownstreamRequests(), UpstreamAttempts: db.UpstreamAttempts(), AuditEvents: db.AuditEvents(), IdempotencyRecords: db.IdempotencyRecords(), AdminUsers: db.AdminUsers(), AdminSessions: db.AdminSessions(), PasswordResetTokens: db.PasswordResetTokens()}, db.Close, nil
	default:
		return repositories{}, nil, fmt.Errorf("unsupported database_type: %s", cfg.DatabaseType)
	}
}

func newSecretCipher(encodedKey string) (secretcrypto.Cipher, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedKey))
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", config.EnvAPIKeyEncryptionKey, err)
	}
	c, err := secretcrypto.NewAESCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("init api key secret cipher: %w", err)
	}
	return c, nil
}

func runKeyCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return printKeyUsage(out)
	}

	switch args[0] {
	case "help", "-h", "--help":
		return printKeyUsage(out)
	case "create":
		ctx := context.Background()
		_, cleanup, svc, err := newCLIKeyService(ctx)
		if err != nil {
			return err
		}
		defer cleanup()
		return runKeyCreate(ctx, svc, args[1:], out)
	case "list":
		ctx := context.Background()
		_, cleanup, svc, err := newCLIKeyService(ctx)
		if err != nil {
			return err
		}
		defer cleanup()
		return runKeyList(ctx, svc, out)
	case "revoke":
		ctx := context.Background()
		_, cleanup, svc, err := newCLIKeyService(ctx)
		if err != nil {
			return err
		}
		defer cleanup()
		return runKeyRevoke(ctx, svc, args[1:], out)
	case "rotate":
		ctx := context.Background()
		_, cleanup, svc, err := newCLIKeyService(ctx)
		if err != nil {
			return err
		}
		defer cleanup()
		return runKeyRotate(ctx, svc, args[1:], out)
	default:
		return fmt.Errorf("unknown key subcommand %q", args[0])
	}
}

func runKeyCreate(ctx context.Context, svc *apikeyservice.Service, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("key create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	description := fs.String("description", "", "human-friendly description")
	expiresAt := fs.String("expires-at", "", "RFC3339 expiration timestamp")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse key create flags: %w", err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}

	var expiry *time.Time
	if strings.TrimSpace(*expiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*expiresAt))
		if err != nil {
			return fmt.Errorf("parse --expires-at: %w", err)
		}
		expiry = &parsed
	}

	created, err := svc.Create(ctx, apikeyservice.CreateRequest{Description: strings.TrimSpace(*description), ExpiresAt: expiry})
	if err != nil {
		return err
	}
	return writeJSON(out, created)
}

func runKeyList(ctx context.Context, svc *apikeyservice.Service, out io.Writer) error {
	items, err := svc.List(ctx)
	if err != nil {
		return err
	}
	return writeJSON(out, map[string]any{"items": items})
}

func runKeyRevoke(ctx context.Context, svc *apikeyservice.Service, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("key revoke", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	id := fs.String("id", "", "key id")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse key revoke flags: %w", err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}
	idv := strings.TrimSpace(*id)
	if idv == "" {
		return errors.New("--id is required")
	}
	if err := svc.Revoke(ctx, idv); err != nil {
		return err
	}
	return writeJSON(out, map[string]string{"id": idv, "status": "revoked"})
}

func runKeyRotate(ctx context.Context, svc *apikeyservice.Service, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("key rotate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	id := fs.String("id", "", "key id")
	description := fs.String("description", "", "new description")
	expiresAt := fs.String("expires-at", "", "RFC3339 expiration timestamp")
	grace := fs.Duration("grace-period", 5*time.Minute, "grace period before old key revocation")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse key rotate flags: %w", err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}
	idv := strings.TrimSpace(*id)
	if idv == "" {
		return errors.New("--id is required")
	}

	var expiry *time.Time
	if strings.TrimSpace(*expiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*expiresAt))
		if err != nil {
			return fmt.Errorf("parse --expires-at: %w", err)
		}
		expiry = &parsed
	}

	desc := strings.TrimSpace(*description)
	req := apikeyservice.RotateRequest{ID: idv, ExpiresAt: expiry, GracePeriod: *grace}
	if desc != "" {
		req.Description = &desc
	}

	rotated, err := svc.Rotate(ctx, req)
	if err != nil {
		return err
	}
	return writeJSON(out, rotated)
}

func newCLIKeyService(ctx context.Context) (repositories, func(), *apikeyservice.Service, error) {
	// Load .env file if it exists (same as server mode)
	if err := config.LoadEnvFile(".env"); err != nil {
		return repositories{}, nil, nil, fmt.Errorf("load env file: %w", err)
	}

	cfg := config.Config{
		DatabaseType: strings.TrimSpace(os.Getenv(config.EnvDatabaseType)),
		DatabaseURL:  strings.TrimSpace(os.Getenv(config.EnvDatabaseURL)),
	}
	if cfg.DatabaseType == "" {
		cfg.DatabaseType = "sqlite"
	}
	if cfg.DatabaseURL == "" {
		cfg.DatabaseURL = "./jimeng-relay.db"
	}
	cfg.APIKeyEncryptionKey = strings.TrimSpace(os.Getenv(config.EnvAPIKeyEncryptionKey))
	if cfg.APIKeyEncryptionKey == "" {
		return repositories{}, nil, nil, fmt.Errorf("%s is required", config.EnvAPIKeyEncryptionKey)
	}

	repos, cleanup, err := openRepositories(ctx, cfg)
	if err != nil {
		return repositories{}, nil, nil, err
	}
	secretCipher, err := newSecretCipher(cfg.APIKeyEncryptionKey)
	if err != nil {
		cleanup()
		return repositories{}, nil, nil, err
	}
	svc := apikeyservice.NewService(repos.APIKeys, apikeyservice.Config{SecretCipher: secretCipher})
	return repos, cleanup, svc, nil
}

func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printUsage(out io.Writer) error {
	if _, err := fmt.Fprintln(out, "Usage:"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "  jimeng-server serve"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "  jimeng-server key <create|list|revoke|rotate> [flags]"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "  jimeng-server help"); err != nil {
		return err
	}
	return nil
}

func printKeyUsage(out io.Writer) error {
	if _, err := fmt.Fprintln(out, "Usage:"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "  jimeng-server key create --description <text> [--expires-at RFC3339]"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "  jimeng-server key list"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "  jimeng-server key revoke --id <key-id>"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "  jimeng-server key rotate --id <key-id> [--description <text>] [--expires-at RFC3339] [--grace-period 5m]"); err != nil {
		return err
	}
	return nil
}
