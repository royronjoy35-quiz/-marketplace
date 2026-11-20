
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

type telegramAPIResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
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

	apiURL := fmt.Sprintf(
		"https://api.telegram.org/bot%s/%s",
		b.token,
		method,
	)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		apiURL,
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram HTTP error: %s", resp.Status)
	}

	var result telegramAPIResponse

	if err := json.NewDecoder(
		io.LimitReader(resp.Body, 1<<20),
	).Decode(&result); err != nil {
		return fmt.Errorf("invalid Telegram response: %w", err)
	}

	if !result.OK {
		return fmt.Errorf("telegram API error: %s", result.Description)
	}

	return nil
}

func (b *telegramBot) send(
	ctx context.Context,
	chatID int64,
	message string,
) error {
	payload := map[string]any{
		"chat_id": chatID,
		"text":    message,
	}

	return b.apiRequest(ctx, "sendMessage", payload)
}

func (b *telegramBot) setWebhook(
	ctx context.Context,
	webhookURL string,
	secret string,
) error {
	payload := map[string]any{
		"url":            webhookURL,
		"allowed_updates": []string{"message"},
	}

	if secret != "" {
		payload["secret_token"] = secret
	}

	return b.apiRequest(ctx, "setWebhook", payload)
}

var linkPattern = regexp.MustCompile(
	`(?i)(https?://|www\.|t\.me/|telegram\.me/|tg://|mailto:|[a-z0-9][a-z0-9.-]*\.(com|net|org|io|app|dev|info|me|co|xyz|bd|uk|ru|site|online)(/|[?:# ]|$))`,
)

func containsLink(text string, entities ...telegramEntity) bool {
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
