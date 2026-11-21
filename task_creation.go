package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type TaskWizard struct {
	TelegramID           int64     `bson:"telegram_id"`
	CreatorRole          string    `bson:"creator_role"`
	Step                 string    `bson:"step"`
	TaskType             string    `bson:"task_type,omitempty"`
	Title                string    `bson:"title,omitempty"`
	Description          string    `bson:"description,omitempty"`
	TargetURL            string    `bson:"target_url,omitempty"`
	RewardUSDT           string    `bson:"reward_usdt,omitempty"`
	Slots                int       `bson:"slots,omitempty"`
	VerificationProvider string    `bson:"verification_provider,omitempty"`
	ChannelVerified      bool      `bson:"channel_verified,omitempty"`
	CreatedAt            time.Time `bson:"created_at"`
	UpdatedAt            time.Time `bson:"updated_at"`
}

type TaskWizardStore struct{ collection *mongo.Collection }

func NewTaskWizardStore(db *Database) *TaskWizardStore {
	return &TaskWizardStore{collection: db.DB.Collection("task_wizards")}
}
func (s *TaskWizardStore) Start(ctx context.Context, userID int64, role string) error {
	now := time.Now().UTC()
	_, err := s.collection.UpdateOne(ctx, bson.M{"telegram_id": userID}, bson.M{
		"$set":         bson.M{"creator_role": role, "step": "type", "updated_at": now},
		"$setOnInsert": bson.M{"created_at": now},
		"$unset":       bson.M{"task_type": "", "title": "", "description": "", "target_url": "", "reward_usdt": "", "slots": "", "verification_provider": "", "channel_verified": ""},
	}, options.Update().SetUpsert(true))
	return err
}
func (s *TaskWizardStore) Get(ctx context.Context, userID int64) (*TaskWizard, error) {
	var w TaskWizard
	err := s.collection.FindOne(ctx, bson.M{"telegram_id": userID}).Decode(&w)
	if err != nil {
		return nil, err
	}
	return &w, nil
}
func (s *TaskWizardStore) Save(ctx context.Context, w *TaskWizard) error {
	w.UpdatedAt = time.Now().UTC()
	_, err := s.collection.ReplaceOne(ctx, bson.M{"telegram_id": w.TelegramID}, w, options.Replace().SetUpsert(true))
	return err
}
func (s *TaskWizardStore) Delete(ctx context.Context, userID int64) error {
	_, err := s.collection.DeleteOne(ctx, bson.M{"telegram_id": userID})
	return err
}

func (a *App) isVerifiedSeller(ctx context.Context, userID int64) bool {
	if a.IsAdmin(userID) {
		return false
	}
	u, err := a.Users.GetByTelegramID(ctx, userID)
	if err != nil || u.Status == "banned" || u.Status == "suspended" || u.Role != "seller" {
		return false
	}
	paid, err := a.Payments.HasPaidSellerRegistration(ctx, userID)
	return err == nil && paid
}

func (a *App) startTaskWizard(ctx context.Context, chatID, userID int64) {
	user, err := a.Users.GetByTelegramID(ctx, userID)
	if err != nil {
		a.sendPlain(ctx, chatID, "Account could not be verified. Please try again.")
		return
	}
	role := "seller"
	if a.IsAdmin(userID) {
		role = "admin"
	} else {
		paid, e := a.Payments.HasPaidSellerRegistration(ctx, userID)
		if e != nil || !paid || user.Role != "seller" {
			a.sendWithKeyboard(ctx, chatID, "Seller registration is required before creating tasks. Use /seller first.", UserKeyboard())
			return
		}
		balance, e := a.Wallet.Balance(ctx, userID)
		if e != nil {
			a.sendPlain(ctx, chatID, "Seller Wallet is temporarily unavailable.")
			return
		}
		if balance <= 0 {
			a.sendPlainWithInline(ctx, chatID, "💳 Seller Wallet is empty. Add Telegram Stars before creating a task.", walletTopupKeyboard())
			return
		}
	}
	if err := a.Wizards.Start(ctx, userID, role); err != nil {
		a.sendPlain(ctx, chatID, "Could not start Task Creation. Please try again.")
		return
	}
	a.sendPlainWithInline(ctx, chatID, "➕ CREATE TASK\n\nFirst, choose the task category:", taskTypeKeyboard())
}

func taskTypeKeyboard() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "🌐 Website", CallbackData: "tw:type:website"}, {Text: "📢 Telegram Channel", CallbackData: "tw:type:channel"}},
		{{Text: "👥 Telegram Group", CallbackData: "tw:type:group"}, {Text: "📱 Social Media", CallbackData: "tw:type:social"}},
		{{Text: "🧩 Other", CallbackData: "tw:type:other"}, {Text: "❌ Cancel", CallbackData: "tw:cancel"}},
	}}
}
func providerKeyboard() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "🔐 Trusted API Receipt", CallbackData: "tw:provider:signed_receipt"}},
		{{Text: "🧑‍⚖️ Manual Review", CallbackData: "tw:provider:manual"}},
		{{Text: "❌ Cancel", CallbackData: "tw:cancel"}},
	}}
}
func channelDoneKeyboard() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{{Text: "✅ Done — Check Admin Status", CallbackData: "tw:channel:done"}}, {{Text: "❌ Cancel", CallbackData: "tw:cancel"}}}}
}
func taskPublishKeyboard() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{{Text: "🚀 Confirm & Publish", CallbackData: "tw:publish"}}, {{Text: "❌ Cancel", CallbackData: "tw:cancel"}}}}
}
func rewardKeyboard() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "$0.045", CallbackData: "tw:reward:0.045"}, {Text: "$0.05", CallbackData: "tw:reward:0.05"}, {Text: "$0.10", CallbackData: "tw:reward:0.10"}},
		{{Text: "$0.25", CallbackData: "tw:reward:0.25"}, {Text: "$0.50", CallbackData: "tw:reward:0.50"}, {Text: "Custom", CallbackData: "tw:reward:custom"}},
	}}
}
func slotsKeyboard() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "10 Workers", CallbackData: "tw:slots:10"}, {Text: "25 Workers", CallbackData: "tw:slots:25"}},
		{{Text: "50 Workers", CallbackData: "tw:slots:50"}, {Text: "100 Workers", CallbackData: "tw:slots:100"}},
		{{Text: "Custom", CallbackData: "tw:slots:custom"}},
	}}
}

func walletTopupKeyboard() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "⭐ 100 Stars — $1.20", CallbackData: "wallet:topup:100"}, {Text: "⭐ 250 Stars — $3.00", CallbackData: "wallet:topup:250"}},
		{{Text: "⭐ 500 Stars — $6.00", CallbackData: "wallet:topup:500"}, {Text: "⭐ 1,000 Stars — $12.00", CallbackData: "wallet:topup:1000"}},
		{{Text: "⭐ 2,000 Stars — $24.00", CallbackData: "wallet:topup:2000"}},
	}}
}

func (a *App) sendPlainWithInline(ctx context.Context, chatID int64, text string, kb *InlineKeyboardMarkup) {
	if err := a.Bot.SendMessageWithInlineKeyboard(ctx, chatID, text, kb); err != nil {
		a.sendPlain(ctx, chatID, text+"\n\nPlease try again.")
	}
}

func (a *App) handleTaskWizardMessage(ctx context.Context, msg *TelegramMessage, w *TaskWizard) {
	input := strings.TrimSpace(msg.Text)
	if input == "" {
		a.sendPlain(ctx, msg.Chat.ID, "Please send a text value or use /cancel.")
		return
	}
	switch w.Step {
	case "title":
		if len([]rune(input)) < 4 || len([]rune(input)) > 80 {
			a.sendPlain(ctx, msg.Chat.ID, "Title must be 4–80 characters.")
			return
		}
		w.Title, w.Step = input, "description"
		_ = a.Wizards.Save(ctx, w)
		a.sendPlain(ctx, msg.Chat.ID, "Step 2/6 — Send a task description (20–1000 characters).")
	case "description":
		if len([]rune(input)) < 20 || len([]rune(input)) > 1000 {
			a.sendPlain(ctx, msg.Chat.ID, "Description must be 20–1000 characters.")
			return
		}
		w.Description, w.Step = input, "target"
		_ = a.Wizards.Save(ctx, w)
		if w.TaskType == "channel" || w.TaskType == "group" {
			a.sendPlain(ctx, msg.Chat.ID, "Step 3/6 — Send the public @channelusername or @groupusername. The bot must be an Administrator there.")
		} else {
			a.sendPlain(ctx, msg.Chat.ID, "Step 3/6 — Send the target Website / Social Media URL (https://...).")
		}
	case "target":
		if !validTaskTarget(input, w.TaskType) {
			a.sendPlain(ctx, msg.Chat.ID, "Invalid target. Send a public @username for Telegram or a valid https:// URL for Website/Social tasks.")
			return
		}
		w.TargetURL = input
		if w.TaskType == "channel" || w.TaskType == "group" {
			w.Step = "channel_verify"
			_ = a.Wizards.Save(ctx, w)
			a.sendPlainWithInline(ctx, msg.Chat.ID, "🔐 Channel/Group Admin Verification\n\nAdd @"+a.Config.BotUsername+" as an Administrator in "+input+". Then press Done. The bot will check its real admin status using Telegram. If it is not an admin, this step will remain locked.", channelDoneKeyboard())
		} else if w.TaskType == "website" || w.TaskType == "social" {
			w.Step = "provider"
			_ = a.Wizards.Save(ctx, w)
			a.sendPlainWithInline(ctx, msg.Chat.ID, "Choose how this task will be verified. Trusted API Receipt requires your external verification backend. Otherwise choose Manual Review.", providerKeyboard())
		} else {
			w.VerificationProvider, w.Step = "manual", "reward"
			_ = a.Wizards.Save(ctx, w)
			a.sendPlainWithInline(ctx, msg.Chat.ID, "Step 4/6 — Choose reward per completed task. Minimum: 0.045 USDT.", rewardKeyboard())
		}
	case "reward", "reward_custom":
		if !ValidateUSDTAmount(input) {
			a.sendPlain(ctx, msg.Chat.ID, "Enter a valid USDT reward (up to 6 decimals). Minimum is 0.045 USDT.")
			return
		}
		if micro, e := usdtToMicro(input); e != nil || micro < 45000 {
			a.sendPlain(ctx, msg.Chat.ID, "Minimum reward per completed task is 0.045 USDT.")
			return
		}
		w.RewardUSDT, w.Step = input, "slots"
		_ = a.Wizards.Save(ctx, w)
		a.sendPlainWithInline(ctx, msg.Chat.ID, "Step 5/6 — Choose the number of workers.", slotsKeyboard())
	case "slots", "slots_custom":
		slots, err := strconv.Atoi(input)
		if err != nil || slots < 1 || slots > 500 {
			a.sendPlain(ctx, msg.Chat.ID, "Enter a whole number from 1 to 500.")
			return
		}
		w.Slots, w.Step = slots, "confirm"
		_ = a.Wizards.Save(ctx, w)
		a.showTaskWizardPreview(ctx, msg.Chat.ID, w)
	default:
		a.sendPlain(ctx, msg.Chat.ID, "This Task Creation session is not waiting for text. Use the inline buttons or /cancel.")
	}
}

func validTaskTarget(value, taskType string) bool {
	value = strings.TrimSpace(value)
	if taskType == "channel" || taskType == "group" {
		return strings.HasPrefix(value, "@") && len(value) >= 6 && !strings.ContainsAny(value, " /")
	}
	u, err := url.ParseRequestURI(value)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

func (a *App) showTaskWizardPreview(ctx context.Context, chatID int64, w *TaskWizard) {
	investment, ownerShare, workerPool, err := calculateTaskInvestment(w.RewardUSDT, w.Slots)
	if err != nil {
		a.sendPlain(ctx, chatID, "Task budget calculation failed: "+err.Error())
		return
	}
	text := fmt.Sprintf("📋 TASK PREVIEW\n\nType: %s\nTitle: %s\nDescription: %s\nTarget: %s\nVerification: %s\nReward / Worker: %s USDT\nWorkers: %d\n\nTotal Investment: %s USDT\nOwner Share (30%%): %s USDT\nWorker Reward Pool (70%%): %s USDT\n\nMinimum reward rule: PASS", w.TaskType, w.Title, w.Description, w.TargetURL, w.VerificationProvider, w.RewardUSDT, w.Slots, microToUSDT(investment), microToUSDT(ownerShare), microToUSDT(workerPool))
	if w.CreatorRole == "admin" {
		balance, e := a.Wallet.TreasuryBalance(ctx)
		if e != nil {
			a.sendPlain(ctx, chatID, "Owner Treasury could not be checked.")
			return
		}
		text += fmt.Sprintf("\n\nOwner Treasury Available: %s USDT", microToUSDT(balance))
	} else {
		balance, e := a.Wallet.Balance(ctx, w.TelegramID)
		if e != nil {
			a.sendPlain(ctx, chatID, "Seller Wallet could not be checked.")
			return
		}
		text += fmt.Sprintf("\n\nYour Wallet Balance: %s USDT", microToUSDT(balance))
	}
	text += "\n\nConfirming will reserve the full investment. Seller tasks go to Admin moderation; Admin tasks publish immediately."
	a.sendPlainWithInline(ctx, chatID, text, taskPublishKeyboard())
}

func (a *App) handleTaskWizardCallback(ctx context.Context, q *TelegramCallbackQuery) {
	chatID := q.From.ID
	if q.Message != nil {
		chatID = q.Message.Chat.ID
	}
	if !a.IsAdmin(q.From.ID) {
		u, e := a.Users.GetByTelegramID(ctx, q.From.ID)
		paid, pe := a.Payments.HasPaidSellerRegistration(ctx, q.From.ID)
		if e != nil || pe != nil || u.Status == "banned" || u.Status == "suspended" || u.Role != "seller" || !paid {
			a.sendPlain(ctx, chatID, "Only an active, verified Seller or Admin can continue Task Creation.")
			return
		}
	}
	parts := strings.Split(q.Data, ":")
	if len(parts) < 2 {
		return
	}
	if parts[1] == "cancel" {
		_ = a.Wizards.Delete(ctx, q.From.ID)
		a.sendPlain(ctx, chatID, "Task Creation cancelled.")
		return
	}
	w, err := a.Wizards.Get(ctx, q.From.ID)
	if err == nil {
		if (a.IsAdmin(q.From.ID) && w.CreatorRole != "admin") || (!a.IsAdmin(q.From.ID) && w.CreatorRole != "seller") {
			a.sendPlain(ctx, chatID, "Your account role changed during this Task Creation session. Cancel and start again from your current panel.")
			return
		}
	}
	if err != nil {
		a.sendPlain(ctx, chatID, "Task Creation session expired. Press Create Task again.")
		return
	}
	if len(parts) == 3 && parts[1] == "type" {
		if w.Step != "type" {
			return
		}
		typeValue := parts[2]
		switch typeValue {
		case "website", "channel", "group", "social", "other":
		default:
			return
		}
		w.TaskType, w.Step = typeValue, "title"
		_ = a.Wizards.Save(ctx, w)
		a.sendPlain(ctx, chatID, "Step 1/6 — Send a task title (4–80 characters).")
		return
	}
	if len(parts) == 3 && parts[1] == "provider" {
		if w.Step != "provider" {
			return
		}
		if parts[2] != "signed_receipt" && parts[2] != "manual" {
			return
		}
		if parts[2] == "signed_receipt" && !a.TaskFlow.providers.Has(ProviderSignedReceipt) {
			a.sendPlain(ctx, chatID, "Trusted Receipt Provider is not configured. Set VERIFICATION_RECEIPT_SECRET in Render first, or choose Manual Review.")
			return
		}
		w.VerificationProvider, w.Step = parts[2], "reward"
		_ = a.Wizards.Save(ctx, w)
		a.sendPlainWithInline(ctx, chatID, "Step 4/6 — Choose reward per completed task. Minimum: 0.045 USDT.", rewardKeyboard())
		return
	}
	if len(parts) == 3 && parts[1] == "channel" && parts[2] == "done" {
		if w.Step != "channel_verify" {
			return
		}
		botID, e := a.Bot.GetMeID(ctx)
		if e != nil {
			a.sendPlainWithInline(ctx, chatID, "Telegram Bot API could not be reached. Please try Done again.", channelDoneKeyboard())
			return
		}
		status, _, e := a.Bot.GetChatMember(ctx, w.TargetURL, botID)
		if e != nil || (status != "administrator" && status != "creator") {
			a.sendPlainWithInline(ctx, chatID, "❌ Bot is not an Administrator of "+w.TargetURL+" yet. Keep the bot as an active Administrator, then press Done again. This step cannot be skipped.", channelDoneKeyboard())
			return
		}
		w.ChannelVerified, w.VerificationProvider, w.Step = true, ProviderTelegramMembership, "reward"
		_ = a.Wizards.Save(ctx, w)
		a.sendPlainWithInline(ctx, chatID, "✅ Channel Admin status verified successfully.\n\nStep 4/6 — Choose reward per completed task. Minimum: 0.045 USDT.", rewardKeyboard())
		return
	}
	if len(parts) == 3 && parts[1] == "reward" {
		if w.Step != "reward" {
			return
		}
		if parts[2] == "custom" {
			w.Step = "reward_custom"
			_ = a.Wizards.Save(ctx, w)
			a.sendPlain(ctx, chatID, "Enter a custom reward in USDT (minimum 0.045, up to 6 decimals).")
			return
		}
		if !ValidateUSDTAmount(parts[2]) {
			return
		}
		amount, e := usdtToMicro(parts[2])
		if e != nil || amount < 45000 {
			a.sendPlain(ctx, chatID, "Minimum reward is 0.045 USDT.")
			return
		}
		w.RewardUSDT, w.Step = parts[2], "slots"
		_ = a.Wizards.Save(ctx, w)
		a.sendPlainWithInline(ctx, chatID, "Step 5/6 — Choose the number of workers.", slotsKeyboard())
		return
	}
	if len(parts) == 3 && parts[1] == "slots" {
		if w.Step != "slots" {
			return
		}
		if parts[2] == "custom" {
			w.Step = "slots_custom"
			_ = a.Wizards.Save(ctx, w)
			a.sendPlain(ctx, chatID, "Enter a whole number of workers from 1 to 500.")
			return
		}
		slots, e := strconv.Atoi(parts[2])
		if e != nil || slots < 1 || slots > 500 {
			return
		}
		w.Slots, w.Step = slots, "confirm"
		_ = a.Wizards.Save(ctx, w)
		a.showTaskWizardPreview(ctx, chatID, w)
		return
	}
	if len(parts) == 2 && parts[1] == "publish" {
		if w.Step != "confirm" {
			return
		}
		investment, ownerShare, workerPool, e := calculateTaskInvestment(w.RewardUSDT, w.Slots)
		if e != nil {
			a.sendPlain(ctx, chatID, e.Error())
			return
		}
		status := TaskStatusPendingReview
		if w.CreatorRole == "admin" {
			status = TaskStatusActive
		}
		verificationMode := VerificationManual
		if w.VerificationProvider == ProviderTelegramMembership || w.VerificationProvider == ProviderSignedReceipt {
			verificationMode = VerificationTrustedAPI
		}
		task := &Task{CreatorID: w.TelegramID, CreatorRole: w.CreatorRole, Title: w.Title, Description: w.Description,
			RewardUSDT: w.RewardUSDT, BudgetUSDT: microToUSDT(workerPool), Slots: w.Slots, Status: status,
			VerificationMode: verificationMode, VerificationProvider: w.VerificationProvider,
			VerificationProviderRef: w.TargetURL, VerificationValue: w.TargetURL, TaskType: w.TaskType, TargetURL: w.TargetURL,
			InvestmentUSDT: microToUSDT(investment), OwnerShareUSDT: microToUSDT(ownerShare), WorkerPoolUSDT: microToUSDT(workerPool)}
		if e := a.Wallet.CreateFundedTask(ctx, task, investment, ownerShare, workerPool, w.CreatorRole == "admin"); e != nil {
			if strings.Contains(e.Error(), "insufficient Seller Wallet") {
				a.sendPlainWithInline(ctx, chatID, "❌ Insufficient Seller Wallet balance. Add Stars before publishing. Your draft is saved.", walletTopupKeyboard())
			} else if strings.Contains(e.Error(), "Owner Treasury") {
				a.sendPlain(ctx, chatID, "❌ Owner Treasury has insufficient available balance. Admin must fund the Treasury after actual owner funding. Use /treasury_credit AMOUNT NOTE after the funds are received.")
			} else {
				a.sendPlain(ctx, chatID, "Task funding/publishing failed: "+e.Error())
			}
			return
		}
		_ = a.Wizards.Delete(ctx, w.TelegramID)
		if w.CreatorRole == "admin" {
			a.sendWithKeyboard(ctx, chatID, fmt.Sprintf("✅ Admin Task published and funded from Owner Treasury.\nTask: %s\nInvestment: %s USDT\nOwner share: %s USDT\nWorker pool: %s USDT\nTask ID: %s", task.Title, task.InvestmentUSDT, task.OwnerShareUSDT, task.WorkerPoolUSDT, task.ID.Hex()), AdminKeyboard())
		} else {
			a.sendWithKeyboard(ctx, chatID, fmt.Sprintf("✅ Task funded and submitted for Admin moderation.\nTask: %s\nInvestment: %s USDT\nOwner share (30%%): %s USDT\nWorker pool (70%%): %s USDT\nTask ID: %s", task.Title, task.InvestmentUSDT, task.OwnerShareUSDT, task.WorkerPoolUSDT, task.ID.Hex()), SellerKeyboard(false))
		}
	}
}

func (a *App) showWalletBalance(ctx context.Context, chatID, userID int64) {
	if !a.isVerifiedSeller(ctx, userID) {
		a.sendPlain(ctx, chatID, "Seller Wallet is available only to verified Seller accounts.")
		return
	}
	balance, err := a.Wallet.Balance(ctx, userID)
	if err != nil {
		a.sendPlain(ctx, chatID, "Wallet is temporarily unavailable.")
		return
	}
	a.sendPlainWithInline(ctx, chatID, fmt.Sprintf("💳 SELLER WALLET\n\nAvailable balance: %s USDT\n\nRate: 1,000 Telegram Stars = 12 USDT", microToUSDT(balance)), walletTopupKeyboard())
}

func (a *App) handleWalletCallback(ctx context.Context, q *TelegramCallbackQuery) {
	chatID := q.From.ID
	if a.IsAdmin(q.From.ID) {
		a.sendPlain(ctx, chatID, "Admin tasks are funded from Owner Treasury. Seller Wallet top-ups are for verified Sellers only.")
		return
	}
	if !a.isVerifiedSeller(ctx, q.From.ID) {
		a.sendPlain(ctx, chatID, "A verified Seller account is required for Wallet top-up.")
		return
	}
	parts := strings.Split(q.Data, ":")
	if len(parts) != 3 || parts[1] != "topup" {
		return
	}
	stars, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return
	}
	payment, err := a.Wallet.CreateTopup(ctx, q.From.ID, stars)
	if err != nil {
		a.sendPlain(ctx, chatID, "Could not create Stars invoice: "+err.Error())
		return
	}
	if err := a.Bot.SendInvoice(ctx, chatID, fmt.Sprintf("DR Wallet %d Stars", stars),
		fmt.Sprintf("Top up your Seller Wallet. %d Stars = %s USDT. Wallet credit is added only after Telegram confirms payment.", stars, microToUSDT(payment.AmountMicroUSDT)), payment.Payload, stars); err != nil {
		a.sendPlain(ctx, chatID, "Telegram could not send the Stars invoice. Please try again later.")
	}
}

func (a *App) handleSuccessfulWalletPayment(ctx context.Context, chatID, userID int64, payment *SuccessfulPayment) {
	if payment == nil || payment.Currency != "XTR" || payment.TelegramPaymentChargeID == "" {
		a.sendPlain(ctx, chatID, "Wallet payment could not be verified. Contact support; do not pay again.")
		return
	}
	stored, err := a.Wallet.GetTopup(ctx, payment.InvoicePayload, userID)
	if err != nil || stored.Stars != payment.TotalAmount {
		a.sendPlain(ctx, chatID, "Payment does not match a valid Wallet invoice. Contact support.")
		return
	}
	if err := a.Wallet.ConfirmTopup(ctx, payment.InvoicePayload, userID, payment.TotalAmount, payment.TelegramPaymentChargeID); err != nil {
		a.sendPlain(ctx, chatID, "Wallet credit is pending verification. Please contact support and do not pay again.")
		return
	}
	balance, _ := a.Wallet.Balance(ctx, userID)
	a.sendWithKeyboard(ctx, chatID, fmt.Sprintf("✅ Stars payment confirmed.\nAdded: %s USDT\nNew Wallet Balance: %s USDT", microToUSDT(stored.AmountMicroUSDT), microToUSDT(balance)), SellerKeyboard(false))
}
