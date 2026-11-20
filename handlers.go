
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

type app struct {
	cfg    Config
	stores *stores
	bot    *telegramBot
	slots  chan struct{}
}

func newApp(cfg Config, db *stores, bot *telegramBot) *app {
	return &app{
		cfg:    cfg,
		stores: db,
		bot:    bot,
		slots:  make(chan struct{}, 256),
	}
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"service": a.cfg.AppName,
			"status":  "running",
		})
	})

	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("POST /telegram/webhook", a.webhook)

	return securityHeaders(mux)
}

func (a *app) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	if err := a.stores.mongo.Ping(ctx, nil); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}

	if err := a.stores.redis.Ping(ctx).Err(); err != nil {
		http.Error(w, "cache unavailable", http.StatusServiceUnavailable)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"service": a.cfg.AppName,
	})
}

func (a *app) webhook(w http.ResponseWriter, r *http.Request) {
	if a.cfg.WebhookSecret != "" &&
		r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != a.cfg.WebhookSecret {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	select {
	case a.slots <- struct{}{}:
		defer func() {
			<-a.slots
		}()
	default:
		http.Error(w, "busy, retry later", http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()

	var update telegramUpdate

	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, "invalid update", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if err := a.handleUpdate(ctx, update); err != nil {
		log.Printf("Update %d processing error: %v", update.UpdateID, err)
		http.Error(w, "temporary processing error", http.StatusBadGateway)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (a *app) handleUpdate(
	ctx context.Context,
	update telegramUpdate,
) error {
	if update.Message == nil {
		return nil
	}

	msg := update.Message

	text := msg.Text

	if text == "" {
		text = msg.Caption
	}

	if text == "" {
		return nil
	}

	// Reject links and URLs.
	if containsLink(
		text,
		append(msg.Entities, msg.CaptionEntities...)...,
	) {
		return a.bot.send(
			ctx,
			msg.Chat.ID,
			"⚠️ দুঃখিত! এই Bot-এ কোনো Link বা URL গ্রহণ করা হয় না।\n\nঅনুগ্রহ করে সরাসরি Text Message পাঠান।",
		)
	}

	text = strings.TrimSpace(text)

	// Commands.
	command := strings.ToLower(strings.Fields(text)[0])

	switch {
	case command == "/start" || strings.HasPrefix(command, "/start@"):
		return a.bot.sendMainMenu(
			ctx,
			msg.Chat.ID,
			"স্বাগতম! 🎉\n\n"+
				"TaskVerse-এ আপনাকে স্বাগতম।\n"+
				"এখানে বিভিন্ন Task সম্পন্ন করে Reward অর্জন করতে পারবেন।\n\n"+
				"নিচের Menu থেকে আপনার প্রয়োজনীয় সেবা নির্বাচন করুন।",
		)

	case command == "/menu":
		return a.bot.sendMainMenu(
			ctx,
			msg.Chat.ID,
			"📱 TaskVerse Main Menu\n\nআপনার প্রয়োজনীয় বিভাগ নির্বাচন করুন।",
		)
	}

	// Main menu navigation.
	switch text {

	case "👤 My Account":
		return a.bot.send(
			ctx,
			msg.Chat.ID,
			"👤 My Account\n\n"+
				"আপনার Account Dashboard প্রস্তুত করা হচ্ছে।",
		)

	case "📋 Available Tasks":
		return a.bot.send(
			ctx,
			msg.Chat.ID,
			"📋 Available Tasks\n\n"+
				"Task Marketplace প্রস্তুত করা হচ্ছে।",
		)

	case "💰 My Wallet":
		return a.bot.send(
			ctx,
			msg.Chat.ID,
			"💰 My Wallet\n\n"+
				"আপনার Wallet Dashboard প্রস্তুত করা হচ্ছে।",
		)

	case "📜 Transaction History":
		return a.bot.send(
			ctx,
			msg.Chat.ID,
			"📜 Transaction History\n\n"+
				"আপনার Transaction History প্রস্তুত করা হচ্ছে।",
		)

	case "🎧 Support":
		return a.bot.send(
			ctx,
			msg.Chat.ID,
			"🎧 Support\n\n"+
				"Support System প্রস্তুত করা হচ্ছে।",
		)

	default:
		return a.bot.send(
			ctx,
			msg.Chat.ID,
			"আপনার Message বুঝতে পারিনি।\n\n"+
				"অনুগ্রহ করে নিচের Menu থেকে একটি Option নির্বাচন করুন।",
		)
	}
}

func (a *app) runMaintenance(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(
				ctx,
				3*time.Second,
			)

			_ = a.stores.redis.Ping(pingCtx).Err()
			cancel()
		}
	}
}

func writeJSON(
	w http.ResponseWriter,
	status int,
	value any,
) {
	w.Header().Set(
		"Content-Type",
		"application/json; charset=utf-8",
	)

	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")

		next.ServeHTTP(w, r)
	})
}

func (b *telegramBot) setWebhook(
	ctx context.Context,
	webhookURL string,
	secret string,
) error {
	payload := map[string]any{
		"url":             webhookURL,
		"allowed_updates": []string{"message"},
	}

	if secret != "" {
		payload["secret_token"] = secret
	}

	return b.apiRequest(ctx, "setWebhook", payload)
}
