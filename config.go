package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	AppName, AppEnv, Port, BotToken, WebhookSecret string
	MongoURI, MongoDatabase, RedisURL              string
	AdminIDs                                       []int64
	MinPayoutUSDT, MaxPayoutUSDT, DailyLimitUSDT   string
	BSCChainID                                     int64
	PayoutsEnabled, MainnetPayoutsEnabled         bool
}

func loadConfig() (Config, error) {
	_ = godotenv.Load()
	cfg := Config{
		AppName:              env("APP_NAME", "TaskVerse"),
		AppEnv:               env("APP_ENV", "production"),
		// Render injects PORT automatically. 10000 is only a local/fallback value,
		// not a claim that Render assigns every service this fixed port.
		Port:                 env("PORT", "10000"),
		BotToken:             strings.TrimSpace(os.Getenv("BOT_TOKEN")),
		WebhookSecret:        strings.TrimSpace(os.Getenv("TELEGRAM_WEBHOOK_SECRET")),
		MongoURI:             strings.TrimSpace(os.Getenv("MONGODB_URI")),
		MongoDatabase:        env("MONGODB_DATABASE", "taskverse"),
		RedisURL:             strings.TrimSpace(os.Getenv("REDIS_URL")),
		MinPayoutUSDT:        env("MIN_PAYOUT_USDT", "5"),
		MaxPayoutUSDT:        env("MAX_PAYOUT_USDT", "10"),
		DailyLimitUSDT:       env("DAILY_PAYOUT_LIMIT_USDT", "25"),
		PayoutsEnabled:       envBool("PAYOUTS_ENABLED", false),
		MainnetPayoutsEnabled: envBool("MAINNET_PAYOUTS_ENABLED", false),
	}
	if cfg.BotToken == "" || cfg.MongoURI == "" || cfg.RedisURL == "" {
		return Config{}, fmt.Errorf("BOT_TOKEN, MONGODB_URI and REDIS_URL are required")
	}
	if _, err := strconv.Atoi(cfg.Port); err != nil {
		return Config{}, fmt.Errorf("PORT must be a numeric port")
	}
	chain, err := strconv.ParseInt(env("BSC_CHAIN_ID", "97"), 10, 64)
	if err != nil {
		return Config{}, fmt.Errorf("BSC_CHAIN_ID must be an integer: %w", err)
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
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
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
