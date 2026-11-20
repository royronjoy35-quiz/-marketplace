
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	AppName       string
	AppEnv        string
	Port          string
	BotToken      string
	WebhookURL    string
	WebhookSecret string

	MongoURI      string
	MongoDatabase string
	RedisURL      string

	AdminIDs []int64

	MinPayoutUSDT  string
	MaxPayoutUSDT  string
	DailyLimitUSDT string

	BSCChainID              int64
	PayoutsEnabled          bool
	MainnetPayoutsEnabled   bool
}

func loadConfig() (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		AppName:       env("APP_NAME", "TaskVerse"),
		AppEnv:        env("APP_ENV", "production"),
		Port:          env("PORT", "10000"),
		BotToken:      strings.TrimSpace(os.Getenv("BOT_TOKEN")),
		WebhookURL:    strings.TrimSpace(os.Getenv("WEBHOOK_URL")),
		WebhookSecret: strings.TrimSpace(os.Getenv("TELEGRAM_WEBHOOK_SECRET")),

		MongoURI:      strings.TrimSpace(os.Getenv("MONGODB_URI")),
		MongoDatabase: env("MONGODB_DATABASE", "taskverse"),
		RedisURL:      strings.TrimSpace(os.Getenv("REDIS_URL")),

		MinPayoutUSDT:  env("MIN_PAYOUT_USDT", "5"),
		MaxPayoutUSDT:  env("MAX_PAYOUT_USDT", "10"),
		DailyLimitUSDT: env("DAILY_PAYOUT_LIMIT_USDT", "25"),

		PayoutsEnabled:        envBool("PAYOUTS_ENABLED", false),
		MainnetPayoutsEnabled: envBool("MAINNET_PAYOUTS_ENABLED", false),
	}

	if cfg.BotToken == "" {
		return Config{}, fmt.Errorf("BOT_TOKEN is required")
	}

	if cfg.WebhookURL == "" {
		return Config{}, fmt.Errorf("WEBHOOK_URL is required")
	}

	if !strings.HasPrefix(cfg.WebhookURL, "https://") {
		return Config{}, fmt.Errorf("WEBHOOK_URL must use HTTPS")
	}

	if cfg.MongoURI == "" || cfg.RedisURL == "" {
		return Config{}, fmt.Errorf("MONGODB_URI and REDIS_URL are required")
	}

	port, err := strconv.Atoi(cfg.Port)
	if err != nil || port < 1 || port > 65535 {
		return Config{}, fmt.Errorf("PORT must be between 1 and 65535")
	}

	chain, err := strconv.ParseInt(env("BSC_CHAIN_ID", "97"), 10, 64)
	if err != nil {
		return Config{}, fmt.Errorf("invalid BSC_CHAIN_ID: %w", err)
	}
	cfg.BSCChainID = chain

	ids, err := parseAdminIDs(os.Getenv("ADMIN_IDS"))
	if err != nil {
		return Config{}, err
	}
	cfg.AdminIDs = ids

	return cfg, nil
}

func env(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}

	return parsed
}

func parseAdminIDs(raw string) ([]int64, error) {
	var ids []int64

	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid ADMIN_IDS value %q", part)
		}

		ids = append(ids, id)
	}

	return ids, nil
}
