package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	UpdateID int64 `json:"update_id"`
	Message  *telegramMessage `json:"message"`
}

type telegramMessage struct {
	Chat struct { ID int64 `json:"id"` } `json:"chat"`
	Text string `json:"text"`
	Caption string `json:"caption"`
	Entities []telegramEntity `json:"entities"`
	CaptionEntities []telegramEntity `json:"caption_entities"`
}

type telegramEntity struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

func newTelegramBot(token string) *telegramBot {
	return &telegramBot{
		token: token,
		client: &http.Client{Timeout: 8 * time.Second},
	}
}

func (b *telegramBot) send(ctx context.Context, chatID int64, text string) error {
	payload, err := json.Marshal(map[string]any{"chat_id": chatID, "text": text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+b.token+"/sendMessage", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Telegram API returned %s", resp.Status)
	}
	return nil
}

var linkPattern = regexp.MustCompile(`(?i)(https?://|http://|www\.|t\.me/|telegram\.me/|tg://|mailto:|[a-z0-9][a-z0-9.-]*\.(com|net|org|io|app|dev|info|me|co|xyz|bd|uk|ru|site|online)(/|[?:# ]|$))`)

func containsLink(text string, entities ...telegramEntity) bool {
	if linkPattern.MatchString(strings.TrimSpace(text)) {
		return true
	}
	for _, entity := range entities {
		if entity.Type == "url" || entity.Type == "text_link" || entity.Type == "email" {
			return true
		}
		if entity.URL != "" {
			return true
		}
	}
	return false
}
