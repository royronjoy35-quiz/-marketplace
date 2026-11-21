package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const maxTelegramUpdateBytes = 1 << 20

type TelegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *TelegramMessage `json:"message,omitempty"`
}

type TelegramMessage struct {
	Text string        `json:"text,omitempty"`
	Chat TelegramChat  `json:"chat"`
	From *TelegramUser `json:"from,omitempty"`
}

type TelegramChat struct {
	ID int64 `json:"id"`
}
type TelegramUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
}

type App struct {
	Config Config
	DB     *Database
	Bot    *TelegramClient
	Users  *UserStore
}

func NewApp(cfg Config, db *Database, bot *TelegramClient) *App {
	return &App{Config: cfg, DB: db, Bot: bot, Users: NewUserStore(db)}
}

func (a *App) TelegramWebhookHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != a.Config.WebhookSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxTelegramUpdateBytes)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		var update TelegramUpdate
		if err := json.Unmarshal(raw, &update); err != nil {
			http.Error(w, "invalid Telegram update", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)

		// Phase 1 handles the basic message path. Durable queue/retry and update
		// deduplication will be added before production payment workflows.
		go a.ProcessUpdate(update)
	})
}

func (a *App) ProcessUpdate(update TelegramUpdate) {
	if update.Message == nil || update.Message.From == nil {
		return
	}
	msg := update.Message
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	if err := a.Users.UpsertTelegramUser(ctx, msg.From.ID, msg.From.Username, msg.From.FirstName); err != nil {
		slog.Error("user upsert failed", "error", err, "telegram_id", msg.From.ID)
		return
	}

	command := strings.TrimSpace(msg.Text)
	var reply string
	switch {
	case command == "/start" || strings.HasPrefix(command, "/start "):
		reply = "Welcome to DR Marketplace! 🚀\n\nYour account has been registered.\nMarketplace features are coming soon."
	case command == "/help":
		reply = "DR Marketplace Help\n\nUse /start to open the bot. Contact support if you need assistance."
	case command == "/status":
		reply = "DR Marketplace bot is online."
	default:
		return
	}
	if err := a.Bot.SendMessage(ctx, msg.Chat.ID, reply); err != nil {
		slog.Error("Telegram reply failed", "error", err, "chat_id", msg.Chat.ID)
	}
}
