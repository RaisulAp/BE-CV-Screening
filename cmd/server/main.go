package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	migrations "cvscreening/be/db"
	"cvscreening/be/internal/aiclient"
	"cvscreening/be/internal/analyses"
	"cvscreening/be/internal/auth"
	"cvscreening/be/internal/captcha"
	"cvscreening/be/internal/catalog"
	"cvscreening/be/internal/config"
	"cvscreening/be/internal/demo"
	"cvscreening/be/internal/email"
	"cvscreening/be/internal/server"
	"cvscreening/be/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Root context cancelled on SIGINT/SIGTERM → drives graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// --- Database ---
	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.Migrate(ctx, migrations.FS); err != nil {
		return err
	}
	log.Info("migrations applied")

	if n, err := st.RecoverInterrupted(ctx); err != nil {
		log.Warn("recover interrupted", "err", err)
	} else if n > 0 {
		log.Info("recovered interrupted analyses", "count", n)
	}

	// --- AI client (real or mock) ---
	var ai aiclient.Client
	if cfg.AIMock {
		ai = aiclient.NewMockClient()
		log.Warn("AI_MOCK enabled — using dummy AI responses")
	} else {
		ai = aiclient.NewHTTPClient(cfg.AIServiceURL)
	}

	// --- Background pipeline worker ---
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	pipe := analyses.NewPipeline(st, ai, log)
	pipe.Start(workerCtx)

	// --- HTTP wiring ---
	emailClient := email.NewClient(cfg.ResendAPIKey, cfg.ResendFromAddr)
	captchaVerifier := captcha.NewVerifier(cfg.TurnstileSecretKey)
	if !emailClient.Enabled() {
		log.Warn("RESEND_API_KEY not set — email verification disabled, new accounts auto-verified")
	}
	if !captchaVerifier.Enabled() {
		log.Warn("TURNSTILE_SECRET_KEY not set — captcha disabled on registration")
	}

	authSvc := auth.NewService(st, cfg.JWTSecret, cfg.JWTExpiresIn, emailClient, cfg.FrontendURL)
	analysisSvc := analyses.NewService(st, pipe, ai, emailClient.Enabled())

	router := server.NewRouter(server.Deps{
		Cfg:         cfg,
		Log:         log,
		AuthHandler: auth.NewHandler(authSvc, captchaVerifier, cfg.CookieMaxAge(), cfg.CookieSecure),
		AnalysisH:   analyses.NewHandler(analysisSvc, cfg.MaxCVSizeBytes()),
		CatalogH:    catalog.NewHandler(st),
		DemoH:       demo.NewHandler(ai, cfg.MaxCVSizeBytes()),
	})

	srv := server.New(":"+cfg.Port, router)

	// Serve until a signal arrives.
	serveErr := make(chan error, 1)
	go func() {
		log.Info("listening", "port", cfg.Port, "aiMock", cfg.AIMock)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		cancelWorker()
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	// 1. Stop accepting HTTP, drain in-flight requests.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx, srv); err != nil {
		log.Warn("http shutdown", "err", err)
	}

	// 2. Stop taking new jobs, wait for the current one to finish.
	cancelWorker()
	pipe.Stop()
	log.Info("shutdown complete")
	return nil
}
