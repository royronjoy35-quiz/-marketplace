package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-telegram/bot"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ---- merged from main.go ----

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := SetupDatabase(ctx, cfg)
	if err != nil {
		log.Fatalf("database initialization failed: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := db.Client.Disconnect(shutdownCtx); err != nil {
			log.Printf("MongoDB disconnect: %v", err)
		}
	}()

	telegramBot, err := bot.New(cfg.BotToken)
	if err != nil {
		log.Fatalf("Telegram bot initialization failed: %v", err)
	}
	app := &BotApp{Config: cfg, DB: db}
	app.RegisterCoreUserHandlers(telegramBot)
	go app.AutoWalletTransferWorker(ctx, telegramBot)

	// Render Web Service health endpoint. Telegram updates are received by long polling;
	// this HTTP listener keeps the service bound to Render's assigned PORT.
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "10000"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("DR Sell Bot is running"))
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	server := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		log.Printf("HTTP health server listening on :%s", port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP server error: %v", err)
			stop()
		}
	}()

	go func() {
		log.Println("Telegram long polling started")
		telegramBot.Start(ctx)
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown: %v", err)
	}
	log.Println("DR Sell Bot stopped")
}

// ---- merged from config.go ----

type Config struct {
	BotToken, BotUsername, MongoURL, DBName, ChannelID                         string
	AdminID                                                                    int64
	MinAmount, MaxAmount                                                       float64
	ReceivingAddresses                                                         map[string]string
	ChannelInviteLink, FAQHowToSendLink, FAQHowBotWorksLink                    string
	FAQPaymentTimeLink, FAQHowToSellLink, FAQPaymentProofLink                  string
	PhotoSellAmount, PhotoOrderSubmitted, PhotoPaymentSent, PhotoOrderRejected string
	PhotoWithdrawSubmitted, PhotoWithdrawSent, PhotoProfile, PhotoRefer        string
	PhotoRates, PhotoWallet, PhotoSupport, PhotoMethods, PhotoNumberSubmitted  string
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func LoadConfig() (Config, error) {
	var c Config
	c.BotToken = strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	admin, err := strconv.ParseInt(env("ADMIN_ID", "0"), 10, 64)
	if err != nil || admin <= 0 {
		return c, fmt.Errorf("ADMIN_ID must be a positive integer")
	}
	c.AdminID = admin
	c.BotUsername = strings.TrimPrefix(strings.TrimSpace(os.Getenv("BOT_USERNAME")), "@")
	c.MongoURL = strings.TrimSpace(os.Getenv("MONGO_URL"))
	c.DBName = env("DB_NAME", "ⒹⓇsellbot")
	c.ChannelID = strings.TrimSpace(os.Getenv("CHANNEL_ID"))
	c.MinAmount, err = strconv.ParseFloat(env("MIN_AMOUNT", "0.2"), 64)
	if err != nil {
		return c, fmt.Errorf("MIN_AMOUNT must be numeric")
	}
	c.MaxAmount, err = strconv.ParseFloat(env("MAX_AMOUNT", "25.0"), 64)
	if err != nil {
		return c, fmt.Errorf("MAX_AMOUNT must be numeric")
	}
	if c.MinAmount <= 0 || c.MaxAmount < c.MinAmount {
		return c, fmt.Errorf("invalid minimum or maximum amount")
	}
	if c.BotToken == "" || c.MongoURL == "" || c.ChannelID == "" {
		return c, fmt.Errorf("BOT_TOKEN, MONGO_URL and CHANNEL_ID are required")
	}

	c.ChannelInviteLink = "https://t.me/+HjTrtYf2H7QwYjQ1"
	c.FAQHowToSendLink = "https://t.me/drsellchannel650533332666dE80130/"
	c.FAQHowBotWorksLink = "https://t.me/drsellchannel650533332666dE80130/106"
	c.FAQPaymentTimeLink = "https://t.me/drsellchannel650533332666dE80130/9"
	c.FAQHowToSellLink = "https://t.me/drsellchannel650533332666dE80130/113"
	c.FAQPaymentProofLink = "https://t.me/drsellchannel650533332666dE80130/112?single"

	c.ReceivingAddresses = map[string]string{
		"Binance Pay": strings.TrimSpace(os.Getenv("BINANCE_PAY")),
		"Bybit Pay":   strings.TrimSpace(os.Getenv("BYBIT_PAY")),
		"BEP-20":      strings.TrimSpace(os.Getenv("BEP20_ADDRESS")),
	}
	base := "https://t.me/ox76a659533302644dE801f65d51/"
	c.PhotoSellAmount = base + "20"
	c.PhotoOrderSubmitted = base + "17"
	c.PhotoPaymentSent = base + "19"
	c.PhotoOrderRejected = base + "16"
	c.PhotoWithdrawSubmitted = base + "14"
	c.PhotoWithdrawSent = base + "13"
	c.PhotoProfile = base + "21"
	c.PhotoRefer = base + "26"
	c.PhotoRates = base + "28"
	c.PhotoWallet = base + "22"
	c.PhotoSupport = base + "25"
	c.PhotoMethods = base + "27"
	c.PhotoNumberSubmitted = base + "29"
	return c, nil
}

// ---- merged from database.go ----

type Database struct {
	Client                                                                                                 *mongo.Client
	DB                                                                                                     *mongo.Database
	Orders, Users, Settings, Tickets, Admins, AuditLogs, Reviews, WithdrawRequests, Sessions, WalletLedger *mongo.Collection
}

func SetupDatabase(ctx context.Context, cfg Config) (*Database, error) {
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client, err := mongo.Connect(connectCtx, options.Client().ApplyURI(cfg.MongoURL).SetServerSelectionTimeout(10*time.Second))
	if err != nil {
		return nil, fmt.Errorf("MongoDB connect: %w", err)
	}
	if err = client.Ping(connectCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("MongoDB ping: %w", err)
	}

	// IMPORTANT: The original Python database.py hardcodes this database name.
	// Keep it identical so the Go bot reads the existing production collections.
	db := client.Database("ⒹⓇsellbot")
	d := &Database{Client: client, DB: db,
		Orders: db.Collection("sell_orders"), Users: db.Collection("sell_users"),
		Settings: db.Collection("sell_bot_settings"), Tickets: db.Collection("sell_tickets"),
		Admins: db.Collection("sell_admins"), AuditLogs: db.Collection("sell_audit_logs"),
		Reviews: db.Collection("sell_user_reviews"), WithdrawRequests: db.Collection("sell_withdraw_requests"), Sessions: db.Collection("sell_workflow_sessions"), WalletLedger: db.Collection("sell_wallet_ledger"),
	}
	if err := d.ensureIndexes(ctx); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	if err := d.ensureDefaults(ctx, cfg); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	return d, nil
}

func (d *Database) ensureIndexes(ctx context.Context) error {
	indexes := []struct {
		c    *mongo.Collection
		keys []mongo.IndexModel
	}{
		{d.Orders, []mongo.IndexModel{
			{Keys: bson.D{{Key: "id", Value: 1}}, Options: options.Index().SetUnique(true)},
			{Keys: bson.D{{Key: "txid", Value: 1}}, Options: options.Index().SetUnique(true).SetSparse(true)},
			{Keys: bson.D{{Key: "txid_normalized", Value: 1}}, Options: options.Index().SetUnique(true).SetSparse(true)},
			{Keys: bson.D{{Key: "proof_image_hash", Value: 1}}, Options: options.Index().SetUnique(true).SetSparse(true)},
			{Keys: bson.D{{Key: "proof_photo_file_id", Value: 1}}, Options: options.Index().SetUnique(true).SetSparse(true)},
		}},
		{d.Users, []mongo.IndexModel{{Keys: bson.D{{Key: "user_id", Value: 1}}, Options: options.Index().SetUnique(true)}}},
		{d.Settings, []mongo.IndexModel{{Keys: bson.D{{Key: "setting_key", Value: 1}}, Options: options.Index().SetUnique(true)}}},
		{d.Tickets, []mongo.IndexModel{{Keys: bson.D{{Key: "ticket_id", Value: 1}}, Options: options.Index().SetUnique(true)}}},
		{d.Admins, []mongo.IndexModel{{Keys: bson.D{{Key: "admin_id", Value: 1}}, Options: options.Index().SetUnique(true)}}},
		{d.WithdrawRequests, []mongo.IndexModel{{Keys: bson.D{{Key: "id", Value: 1}}, Options: options.Index().SetUnique(true)}}},
		{d.Sessions, []mongo.IndexModel{{Keys: bson.D{{Key: "session_key", Value: 1}}, Options: options.Index().SetUnique(true)}, {Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)}}},
		{d.WalletLedger, []mongo.IndexModel{{Keys: bson.D{{Key: "operation_id", Value: 1}}, Options: options.Index().SetUnique(true)}, {Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "created_at", Value: -1}}}}},
	}
	for _, item := range indexes {
		if _, err := item.c.Indexes().CreateMany(ctx, item.keys); err != nil {
			return fmt.Errorf("create indexes for %s: %w", item.c.Name(), err)
		}
	}
	return nil
}

func (d *Database) ensureDefaults(ctx context.Context, cfg Config) error {
	defaults := []struct {
		key   string
		value any
	}{
		{"rate_tiers", []map[string]string{
			{"min": "0.50", "max": "0.99", "rate": "115"}, {"min": "1.00", "max": "2.49", "rate": "120"},
			{"min": "2.50", "max": "4.99", "rate": "124"}, {"min": "5.00", "max": "9.99", "rate": "126"},
			{"min": "10.0", "max": "50.00", "rate": "127"},
		}},
		{"active_payout_methods", []string{"bKash", "Nagad", "Rocket", "Upay", "Recharge", "Bangla QR"}},
		{"min_max_limits", map[string]string{"min": fmt.Sprintf("%g", cfg.MinAmount), "max": fmt.Sprintf("%g", cfg.MaxAmount)}},
		{"daily_amount_limit", "10000"},
		{"ref_percentages", map[string]string{"first_sale": "0.05", "lifetime": "0.005"}},
		{"exchange_rate_bdt_usdt", "120"},
	}
	for _, item := range defaults {
		var found bson.M
		err := d.Settings.FindOne(ctx, bson.M{"setting_key": item.key}).Decode(&found)
		if err == mongo.ErrNoDocuments {
			var value string
			if s, ok := item.value.(string); ok {
				value = s
			} else {
				b, e := json.Marshal(item.value)
				if e != nil {
					return e
				}
				value = string(b)
			}
			if _, e := d.Settings.InsertOne(ctx, bson.M{"setting_key": item.key, "setting_value": value}); e != nil {
				return fmt.Errorf("insert default %s: %w", item.key, e)
			}
		} else if err != nil {
			return err
		}
	}
	// Match the Python migration: upgrade only the exact untouched legacy list.
	var row struct {
		SettingValue string `bson:"setting_value"`
	}
	if err := d.Settings.FindOne(ctx, bson.M{"setting_key": "active_payout_methods"}).Decode(&row); err == nil {
		var methods []string
		if json.Unmarshal([]byte(row.SettingValue), &methods) == nil && equalStrings(methods, []string{"bKash", "Nagad", "Recharge", "Bangla QR"}) {
			b, _ := json.Marshal([]string{"bKash", "Nagad", "Rocket", "Upay", "Recharge", "Bangla QR"})
			_, err = d.Settings.UpdateOne(ctx, bson.M{"setting_key": "active_payout_methods"}, bson.M{"$set": bson.M{"setting_value": string(b)}})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
