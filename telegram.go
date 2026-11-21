
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type TelegramClient struct {
	token string
	http  *http.Client
}

type ReplyKeyboardButton struct {
	Text string `json:"text"`
}

type ReplyKeyboardMarkup struct {
	Keyboard              [][]ReplyKeyboardButton `json:"keyboard"`
	ResizeKeyboard        bool                    `json:"resize_keyboard"`
	IsPersistent          bool                    `json:"is_persistent,omitempty"`
	InputFieldPlaceholder string                  `json:"input_field_placeholder,omitempty"`
}

type telegramAPIResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description,omitempty"`
}

func NewTelegramClient(token string) *TelegramClient {
	return &TelegramClient{
		token: token,
		http:  &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *TelegramClient) call(ctx context.Context, method string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf(
		"https://api.telegram.org/bot%s/%s",
		c.token,
		method,
	)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpoint,
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result telegramAPIResponse

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !result.OK {
		return fmt.Errorf(
			"Telegram API %s: %s",
			method,
			result.Description,
		)
	}

	return nil
}

func (c *TelegramClient) SendMessage(
	ctx context.Context,
	chatID int64,
	text string,
) error {
	return c.SendMessageWithKeyboard(ctx, chatID, text, nil)
}

func (c *TelegramClient) SendMessageWithKeyboard(
	ctx context.Context,
	chatID int64,
	text string,
	keyboard *ReplyKeyboardMarkup,
) error {

	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}

	if keyboard != nil {
		payload["reply_markup"] = keyboard
	}

	return c.call(ctx, "sendMessage", payload)
}

func (c *TelegramClient) SetWebhook(
	ctx context.Context,
	webhookURL string,
	secret string,
) error {

	u, err := url.Parse(webhookURL)

	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("invalid HTTPS webhook URL")
	}

	return c.call(ctx, "setWebhook", map[string]any{
		"url":                  webhookURL,
		"secret_token":         secret,
		"allowed_updates":      []string{"message", "callback_query", "pre_checkout_query"},
		"drop_pending_updates": false,
	})
}
