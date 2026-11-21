package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	BotToken      string
	BotUsername   string
	WebhookSecret string
	PublicBaseURL string
	WebhookPath   string
	MongoURI      string
	DatabaseName  string
	Port          string
	AdminIDs      map[int64]bool
}

func LoadConfig() (Config, error) {
	cfg := Config{
		BotToken:      strings.TrimSpace(os.Getenv("BOT_TOKEN")),
		BotUsername:   strings.TrimPrefix(strings.TrimSpace(os.Getenv("BOT_USERNAME")), "@"),
		WebhookSecret: strings.TrimSpace(os.Getenv("TELEGRAM_WEBHOOK_SECRET")),
		PublicBaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL")), "/"),
		WebhookPath:   envOr("WEBHOOK_PATH", "/webhook/telegram"),
		MongoURI:      strings.TrimSpace(os.Getenv("MONGODB_URI")),
		DatabaseName:  envOr("DATABASE_NAME", "dr_marketplace"),
		Port:          envOr("PORT", "8080"),
		AdminIDs:      make(map[int64]bool),
	}
	if cfg.BotToken == "" {
		return Config{}, fmt.Errorf("BOT_TOKEN is required")
	}
	if cfg.BotUsername == "" {
		return Config{}, fmt.Errorf("BOT_USERNAME is required")
	}
	if len(cfg.WebhookSecret) < 32 {
		return Config{}, fmt.Errorf("TELEGRAM_WEBHOOK_SECRET must be at least 32 characters")
	}
	if cfg.MongoURI == "" {
		return Config{}, fmt.Errorf("MONGODB_URI is required")
	}
	if !strings.HasPrefix(cfg.WebhookPath, "/") {
		return Config{}, fmt.Errorf("WEBHOOK_PATH must start with /")
	}
	if cfg.PublicBaseURL != "" && !strings.HasPrefix(cfg.PublicBaseURL, "https://") {
		return Config{}, fmt.Errorf("PUBLIC_BASE_URL must use HTTPS")
	}
	if _, err := strconv.Atoi(cfg.Port); err != nil {
		return Config{}, fmt.Errorf("PORT must be numeric")
	}

	for _, raw := range strings.Split(os.Getenv("ADMIN_IDS"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return Config{}, fmt.Errorf("invalid ADMIN_IDS value %q", raw)
		}
		cfg.AdminIDs[id] = true
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
