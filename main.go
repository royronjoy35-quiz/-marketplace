
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	rootCtx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	// Connect MongoDB and Redis.
	clients, err := connectStores(rootCtx, cfg)
	if err != nil {
		log.Fatalf("Database connection error: %v", err)
	}
	defer clients.close(context.Background())

	// Initialize Telegram Bot and application.
	bot := newTelegramBot(cfg.BotToken)
	app := newApp(cfg, clients, bot)

	// Configure HTTP server.
	server := &http.Server{
		Addr:    net.JoinHostPort("0.0.0.0", cfg.Port),
		Handler: app.routes(),

		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// Bind the port before registering the Telegram webhook.
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		log.Fatalf("HTTP listener error: %v", err)
	}

	serveErr := make(chan error, 1)

	go func() {
		log.Printf(
			"%s HTTP server listening on %s",
			cfg.AppName,
			server.Addr,
		)

		serveErr <- server.Serve(listener)
	}()

	// Register Telegram webhook after the HTTP listener is ready.
	webhookCtx, cancelWebhook := context.WithTimeout(
		rootCtx,
		20*time.Second,
	)

	err = bot.setWebhook(
		webhookCtx,
		cfg.WebhookURL,
		cfg.WebhookSecret,
	)

	cancelWebhook()

	if err != nil {
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		_ = server.Shutdown(shutdownCtx)
		cancel()

		log.Fatalf("Telegram webhook registration failed: %v", err)
	}

	log.Println("Telegram webhook registered successfully")
	log.Printf("Webhook URL: %s", cfg.WebhookURL)
	log.Println("TaskVerse is ready")

	// Start background maintenance.
	go app.runMaintenance(rootCtx)

	// Wait for shutdown signal or HTTP server failure.
	select {
	case <-rootCtx.Done():
		log.Println("Shutdown signal received")

	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP server stopped unexpectedly: %v", err)
		}
		stop()
	}

	// Graceful shutdown.
	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		12*time.Second,
	)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
		_ = server.Close()
	}

	log.Println("TaskVerse stopped cleanly")
}
