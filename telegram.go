package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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
	IsPersistent          bool                    `json:"is_persistent"`
	OneTimeKeyboard       bool                    `json:"one_time_keyboard"`
	InputFieldPlaceholder string                  `json:"input_field_placeholder,omitempty"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
}
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

func (c *TelegramClient) SendMessageWithInlineKeyboard(ctx context.Context, chatID int64, text string, keyboard *InlineKeyboardMarkup) error {
	payload := map[string]any{"chat_id": chatID, "text": text}
	if keyboard != nil {
		payload["reply_markup"] = keyboard
	}
	return c.call(ctx, "sendMessage", payload)
}

func (c *TelegramClient) SendPhoto(ctx context.Context, chatID int64, fileID, caption string) error {
	payload := map[string]any{"chat_id": chatID, "photo": fileID}
	if strings.TrimSpace(caption) != "" {
		payload["caption"] = caption
	}
	return c.call(ctx, "sendPhoto", payload)
}

func (c *TelegramClient) AnswerCallbackQuery(ctx context.Context, callbackID, text string) error {
	payload := map[string]any{"callback_query_id": callbackID}
	if strings.TrimSpace(text) != "" {
		payload["text"] = text
	}
	return c.call(ctx, "answerCallbackQuery", payload)
}

type LabeledPrice struct {
	Label  string `json:"label"`
	Amount int64  `json:"amount"`
}

type telegramAPIResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description,omitempty"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

func NewTelegramClient(token string) *TelegramClient {
	return &TelegramClient{
		token: strings.TrimSpace(token),
		http: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// Common Telegram API Request

func (c *TelegramClient) call(
	ctx context.Context,
	method string,
	payload any,
) error {

	if c.token == "" {
		return fmt.Errorf("Telegram Bot Token is empty")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal Telegram request: %w", err)
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
		return fmt.Errorf("create Telegram request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Telegram API request failed: %w", err)
	}
	defer resp.Body.Close()

	var result telegramAPIResponse

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode Telegram response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !result.OK {
		return fmt.Errorf(
			"Telegram API %s failed: HTTP %d, error %d: %s",
			method,
			resp.StatusCode,
			result.ErrorCode,
			result.Description,
		)
	}

	return nil
}

// Send Normal Message

func (c *TelegramClient) SendMessage(
	ctx context.Context,
	chatID int64,
	text string,
) error {

	return c.SendMessageWithKeyboard(
		ctx,
		chatID,
		text,
		nil,
	)
}

// Send Message With Reply Keyboard

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
		// Keep the reply keyboard available without forcing it to stay open.
		// When the user hides it, Telegram clients can show their native
		// keyboard-toggle icon in the message input bar.
		// The icon itself is rendered and positioned by the Telegram client.
		markup := *keyboard
		markup.ResizeKeyboard = true
		markup.IsPersistent = false
		markup.OneTimeKeyboard = false
		payload["reply_markup"] = &markup
	}

	return c.call(
		ctx,
		"sendMessage",
		payload,
	)
}

// Set Telegram Webhook

func (c *TelegramClient) SetWebhook(
	ctx context.Context,
	webhookURL string,
	secret string,
) error {

	u, err := url.Parse(webhookURL)

	if err != nil ||
		u.Scheme != "https" ||
		u.Host == "" {
		return fmt.Errorf("invalid HTTPS webhook URL")
	}

	if len(secret) < 32 {
		return fmt.Errorf("webhook secret must contain at least 32 characters")
	}

	return c.call(
		ctx,
		"setWebhook",
		map[string]any{
			"url":          webhookURL,
			"secret_token": secret,
			"allowed_updates": []string{
				"message",
				"callback_query",
				"pre_checkout_query",
			},
			"drop_pending_updates": false,
		},
	)
}

// Send Telegram Stars Invoice

func (c *TelegramClient) SendInvoice(
	ctx context.Context,
	chatID int64,
	title string,
	description string,
	payload string,
	stars int64,
) error {

	if stars <= 0 {
		return fmt.Errorf("invoice amount must be positive")
	}

	if stars != SellerRegistrationStars {
		return fmt.Errorf(
			"invalid Seller registration amount: expected %d Stars",
			SellerRegistrationStars,
		)
	}

	if strings.TrimSpace(payload) == "" {
		return fmt.Errorf("invoice payload cannot be empty")
	}

	return c.call(
		ctx,
		"sendInvoice",
		map[string]any{
			"chat_id": chatID,

			"title": title,

			"description": description,

			"payload": payload,

			// Telegram Stars do not require a payment provider token.
			"provider_token": "",

			// Telegram Stars currency.
			"currency": "XTR",

			// 100 Stars = 100 XTR units.
			"prices": []LabeledPrice{
				{
					Label:  title,
					Amount: stars,
				},
			},
		},
	)
}

// Answer Telegram Pre-Checkout Query

func (c *TelegramClient) AnswerPreCheckoutQuery(
	ctx context.Context,
	queryID string,
	ok bool,
	errorMessage string,
) error {

	if strings.TrimSpace(queryID) == "" {
		return fmt.Errorf("pre-checkout query ID is empty")
	}

	payload := map[string]any{
		"pre_checkout_query_id": queryID,
		"ok":                    ok,
	}

	if !ok {
		if strings.TrimSpace(errorMessage) == "" {
			errorMessage = "Payment could not be verified. Please try again."
		}

		payload["error_message"] = errorMessage
	}

	return c.call(
		ctx,
		"answerPreCheckoutQuery",
		payload,
	)
}

// Get Telegram Webhook Information

func (c *TelegramClient) GetWebhookInfo(
	ctx context.Context,
) (json.RawMessage, error) {

	body := map[string]any{}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf(
		"https://api.telegram.org/bot%s/getWebhookInfo",
		c.token,
	)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpoint,
		bytes.NewReader(data),
	)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result telegramAPIResponse

	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 ||
		resp.StatusCode >= 300 ||
		!result.OK {
		return nil, fmt.Errorf(
			"getWebhookInfo failed: %s",
			result.Description,
		)
	}

	return result.Result, nil
}
