package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := LoadConfig()
	if err != nil {
		logger.Error("configuration error", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	db, err := ConnectDatabase(ctx, cfg.MongoURI, cfg.DatabaseName)
	cancel()
	if err != nil {
		logger.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	indexCtx, indexCancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := EnsureIndexes(indexCtx, db); err != nil {
		indexCancel()
		logger.Error("database index setup failed", "error", err)
		os.Exit(1)
	}
	indexCancel()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := db.Client.Disconnect(ctx); err != nil {
			logger.Error("database disconnect failed", "error", err)
		}
	}()

	bot := NewTelegramClient(cfg.BotToken)
	app := NewApp(cfg, db, bot)
	treasuryCtx, treasuryCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := app.Wallet.InitializeTreasury(treasuryCtx, cfg.TreasuryInitialUSDT); err != nil {
		treasuryCancel()
		logger.Error("owner treasury initialization failed", "error", err)
		os.Exit(1)
	}
	treasuryCancel()

	if cfg.PublicBaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = bot.SetWebhook(ctx, cfg.PublicBaseURL+cfg.WebhookPath, cfg.WebhookSecret)
		cancel()
		if err != nil {
			logger.Error("Telegram webhook registration failed", "error", err)
			os.Exit(1)
		}
		logger.Info("Telegram webhook registered", "path", cfg.WebhookPath)
	} else {
		logger.Warn("PUBLIC_BASE_URL is empty; webhook registration skipped")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", LiveHandler)
	mux.HandleFunc("GET /health/ready", app.ReadyHandler)
	mux.Handle("POST "+cfg.WebhookPath, app.TelegramWebhookHandler())

	server := &http.Server{
		Addr:              "0.0.0.0:" + cfg.Port,
		Handler:           requestLogger(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      25 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("HTTP server listening", "address", server.Addr)
		serverErr <- server.ListenAndServe()
	}()

	stopCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			os.Exit(1)
		}
	case <-stopCtx.Done():
		logger.Info("shutdown signal received")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
			_ = server.Close()
		}
	}
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("http request", "method", r.Method, "path", r.URL.Path,
			"duration_ms", time.Since(start).Milliseconds())
	})
}
