package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type app struct {
	cfg      Config
	stores   *stores
	bot      *telegramBot
	slots    chan struct{}
}

func newApp(cfg Config, db *stores, bot *telegramBot) *app {
	return &app{cfg: cfg, stores: db, bot: bot, slots: make(chan struct{}, 256)}
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"service": a.cfg.AppName, "status": "running"})
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
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": a.cfg.AppName})
}

func (a *app) webhook(w http.ResponseWriter, r *http.Request) {
	if a.cfg.WebhookSecret != "" &&
		r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != a.cfg.WebhookSecret {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	default:
		http.Error(w, "busy, retry later", http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	var update telegramUpdate
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&update); err != nil {
		http.Error(w, "invalid update", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := a.handleUpdate(ctx, update); err != nil {
		http.Error(w, "temporary processing error", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *app) handleUpdate(ctx context.Context, update telegramUpdate) error {
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
	if containsLink(text, append(msg.Entities, msg.CaptionEntities...)...) {
		return a.bot.send(ctx, msg.Chat.ID, "দুঃখিত, এই বটে কোনো Link বা URL গ্রহণ করা হয় না।")
	}
	if strings.TrimSpace(text) == "/start" {
		return a.bot.send(ctx, msg.Chat.ID, "স্বাগতম! TaskVerse প্রস্তুত করা হচ্ছে।")
	}
	return a.bot.send(ctx, msg.Chat.ID, "আপনার মেসেজ পাওয়া গেছে।")
}

func (a *app) runMaintenance(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			if err := a.stores.redis.Ping(pingCtx).Err(); err != nil {
				// Health endpoint will also report dependency failures.
			}
			cancel()
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
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
