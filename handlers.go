
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const maxTelegramUpdateBytes = 1 << 20

const (
	menuMarketplace = "🛍 Marketplace"
	menuProfile     = "👤 My Profile"
	menuOrders      = "📦 My Orders"
	menuEarnings    = "💰 My Earnings"
	menuWithdraw    = "🏦 Withdraw"
	menuSupport     = "🎧 Support"
)

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
	return &App{
		Config: cfg,
		DB:     db,
		Bot:    bot,
		Users:  NewUserStore(db),
	}
}

// Main User Menu

func MainMenuKeyboard() *ReplyKeyboardMarkup {
	return &ReplyKeyboardMarkup{
		Keyboard: [][]ReplyKeyboardButton{
			{
				{Text: menuMarketplace},
				{Text: menuProfile},
			},
			{
				{Text: menuOrders},
				{Text: menuEarnings},
			},
			{
				{Text: menuWithdraw},
				{Text: menuSupport},
			},
		},
		ResizeKeyboard:        true,
		IsPersistent:          true,
		InputFieldPlaceholder: "Choose an option...",
	}
}

// Telegram Webhook

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

		r.Body = http.MaxBytesReader(
			w,
			r.Body,
			maxTelegramUpdateBytes,
		)
		defer r.Body.Close()

		var update TelegramUpdate

		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			http.Error(w, "invalid Telegram update", http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusOK)

		go a.ProcessUpdate(update)
	})
}

// Process Telegram Updates

func (a *App) ProcessUpdate(update TelegramUpdate) {

	if update.Message == nil || update.Message.From == nil {
		return
	}

	msg := update.Message

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	// Automatic User Registration

	if err := a.Users.UpsertTelegramUser(
		ctx,
		msg.From.ID,
		msg.From.Username,
		msg.From.FirstName,
	); err != nil {

		slog.Error(
			"user registration failed",
			"error", err,
			"telegram_id", msg.From.ID,
		)
		return
	}

	text := strings.TrimSpace(msg.Text)

	if text == "" {
		return
	}

	command := strings.ToLower(strings.Fields(text)[0])

	// Commands

	switch {
	case command == "/start" || command == "/menu":

		name := strings.TrimSpace(msg.From.FirstName)

		if name == "" {
			name = "there"
		}

		a.sendMenu(
			ctx,
			msg.Chat.ID,
			fmt.Sprintf(
				"Welcome, %s! 🚀\n\nWelcome to ⒹⓇ Marketplace.\nYour account is registered.\n\nChoose an option from the menu below.",
				name,
			),
		)
		return

	case command == "/help":

		a.sendMenu(
			ctx,
			msg.Chat.ID,
			"ⒹⓇ Marketplace Help\n\nUse the menu buttons to navigate the marketplace.\n\nSend /start to reopen the main menu.",
		)
		return

	case command == "/status":

		a.sendMenu(
			ctx,
			msg.Chat.ID,
			"ⒹⓇ Marketplace is online. Your account is connected.",
		)
		return
	}

	// Main Menu Navigation

	switch text {

	case menuProfile, "My Profile":

		a.sendProfile(
			ctx,
			msg.Chat.ID,
			msg.From.ID,
		)

	case menuMarketplace, "Marketplace":

		a.sendMenu(
			ctx,
			msg.Chat.ID,
			"🛍 Marketplace\n\nThe marketplace catalogue is being prepared. Available listings will appear here soon.",
		)

	case menuOrders, "My Orders":

		a.sendMenu(
			ctx,
			msg.Chat.ID,
			"📦 My Orders\n\nYour order history will appear here after the order system is activated.",
		)

	case menuEarnings, "My Earnings":

		a.sendMenu(
			ctx,
			msg.Chat.ID,
			"💰 My Earnings\n\nYour earnings and transaction history will appear here after the ledger module is activated.\n\nNo withdrawable balance is calculated yet.",
		)

	case menuWithdraw, "Withdraw":

		a.sendMenu(
			ctx,
			msg.Chat.ID,
			"🏦 Withdraw\n\nUSDT withdrawals are not active yet.\n\nBEP-20 address validation and the hybrid approval system will be added in the wallet phase.",
		)

	case menuSupport, "Support":

		a.sendMenu(
			ctx,
			msg.Chat.ID,
			"🎧 Support\n\nThe support ticket system is being prepared.",
		)

	default:

		a.sendMenu(
			ctx,
			msg.Chat.ID,
			"I couldn't recognise that option. Please choose a button from the menu.",
		)
	}
}

// Send Main Menu

func (a *App) sendMenu(
	ctx context.Context,
	chatID int64,
	text string,
) {

	if err := a.Bot.SendMessageWithKeyboard(
		ctx,
		chatID,
		text,
		MainMenuKeyboard(),
	); err != nil {

		slog.Error(
			"Telegram menu reply failed",
			"error", err,
			"chat_id", chatID,
		)
	}
}

// My Profile

func (a *App) sendProfile(
	ctx context.Context,
	chatID int64,
	telegramID int64,
) {

	user, err := a.Users.GetByTelegramID(
		ctx,
		telegramID,
	)

	if err != nil {

		slog.Error(
			"profile lookup failed",
			"error", err,
			"telegram_id", telegramID,
		)

		a.sendMenu(
			ctx,
			chatID,
			"Unable to load your profile right now. Please try again.",
		)
		return
	}

	username := "Not set"

	if user.Username != "" {
		username = "@" + user.Username
	}

	name := user.FirstName

	if name == "" {
		name = "Not set"
	}

	joined := user.CreatedAt.UTC().Format("02 Jan 2006")

	role := user.Role
	if role == "" {
		role = "user"
	}

	status := user.Status
	if status == "" {
		status = "active"
	}

	profile := fmt.Sprintf(
		"👤 MY PROFILE\n\nName: %s\nUsername: %s\nTelegram ID: %d\nAccount Type: %s\nStatus: %s\nJoined: %s",
		name,
		username,
		user.TelegramID,
		role,
		status,
		joined,
	)

	a.sendMenu(
		ctx,
		chatID,
		profile,
	)
}

// Unused reader protection helper is handled by MaxBytesReader above.
var _ = io.EOF
