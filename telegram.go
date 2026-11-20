
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type telegramBot struct {
	token  string
	client *http.Client
}

type telegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message"`
}

type telegramMessage struct {
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`

	Text            string           `json:"text"`
	Caption         string           `json:"caption"`
	Entities        []telegramEntity `json:"entities"`
	CaptionEntities []telegramEntity `json:"caption_entities"`
}

type telegramEntity struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type telegramAPIResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

type replyKeyboard struct {
	Keyboard        [][]keyboardButton `json:"keyboard"`
	ResizeKeyboard  bool               `json:"resize_keyboard"`
	IsPersistent     bool               `json:"is_persistent"`
}

type keyboardButton struct {
	Text string `json:"text"`
}

func newTelegramBot(token string) *telegramBot {
	return &telegramBot{
		token: token,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (b *telegramBot) apiRequest(
	ctx context.Context,
	method string,
	payload any,
) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf(
		"https://api.telegram.org/bot%s/%s",
		b.token,
		method,
	)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		url,
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("Telegram request failed: %w", err)
	}
	defer resp.Body.Close()

	var result telegramAPIResponse

	if err := json.NewDecoder(
		io.LimitReader(resp.Body, 1<<20),
	).Decode(&result); err != nil {
		return fmt.Errorf("Telegram response error: %w", err)
	}

	if !result.OK {
		return fmt.Errorf(
			"Telegram API error: %s",
			result.Description,
		)
	}

	return nil
}

func (b *telegramBot) send(
	ctx context.Context,
	chatID int64,
	message string,
) error {
	return b.apiRequest(ctx, "sendMessage", map[string]any{
		"chat_id": chatID,
		"text":    message,
	})
}

func (b *telegramBot) sendMainMenu(
	ctx context.Context,
	chatID int64,
	message string,
) error {
	keyboard := replyKeyboard{
		Keyboard: [][]keyboardButton{
			{
				{Text: "👤 My Account"},
				{Text: "📋 Available Tasks"},
			},
			{
				{Text: "💰 My Wallet"},
				{Text: "📜 Transaction History"},
			},
			{
				{Text: "🎧 Support"},
			},
		},
		ResizeKeyboard: true,
		IsPersistent:  true,
	}

	return b.apiRequest(ctx, "sendMessage", map[string]any{
		"chat_id":      chatID,
		"text":         message,
		"reply_markup": keyboard,
	})
}

var linkPattern = regexp.MustCompile(
	`(?i)(https?://|www\.|t\.me/|telegram\.me/|tg://|mailto:|[a-z0-9][a-z0-9.-]*\.(com|net|org|io|app|dev|info|me|co|xyz|bd|uk|ru|site|online)(/|[?:# ]|$))`,
)

func containsLink(
	text string,
	entities ...telegramEntity,
) bool {
	if linkPattern.MatchString(strings.TrimSpace(text)) {
		return true
	}

	for _, entity := range entities {
		if entity.Type == "url" ||
			entity.Type == "text_link" ||
			entity.Type == "email" {
			return true
		}

		if entity.URL != "" {
			return true
		}
	}

	return false
}
