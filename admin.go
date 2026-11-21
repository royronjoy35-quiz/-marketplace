package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// IsAdmin is based only on the server-side ADMIN_IDS allowlist.
func (a *App) IsAdmin(telegramID int64) bool {
	return a.Config.AdminIDs[telegramID]
}

func (a *App) requireAdmin(chatID, userID int64) bool {
	if a.IsAdmin(userID) {
		return true
	}
	a.sendPlain(context.Background(), chatID, "Admin access required.")
	return false
}

func (a *App) sendAdminDashboard(ctx context.Context, chatID, userID int64) {
	if !a.requireAdmin(chatID, userID) {
		return
	}

	users := a.DB.DB.Collection("users")
	tasks := a.DB.DB.Collection("tasks")
	payments := a.DB.DB.Collection("seller_payments")
	withdrawals := a.DB.DB.Collection("withdrawals")

	count := func(collection interface {
		CountDocuments(context.Context, interface{}, ...*options.CountOptions) (int64, error)
	}, filter interface{}) int64 {
		n, err := collection.CountDocuments(ctx, filter)
		if err != nil {
			return -1
		}
		return n
	}
	format := func(n int64) string {
		if n < 0 {
			return "Unavailable"
		}
		return fmt.Sprintf("%d", n)
	}

	message := fmt.Sprintf(
		"🛡 ADMIN DASHBOARD\n\n👥 Total users: %s\n🏪 Sellers: %s\n🧰 Workers: %s\n\n📚 Active tasks: %s\n🕒 Pending tasks: %s\n\n⭐ Paid Seller registrations: %s\n🏦 Pending withdrawals: %s\n\nUpdated: %s UTC\n\nQUICK ACTIONS\n/ban USER_ID · /unban USER_ID\n/suspend USER_ID · /restore USER_ID\n/approve_task TASK_ID · /reject_task TASK_ID\n/pause_task TASK_ID · /resume_task TASK_ID · /delete_task TASK_ID",
		format(count(users, bson.M{})),
		format(count(users, bson.M{"role": "seller"})),
		format(count(users, bson.M{"role": "worker"})),
		format(count(tasks, bson.M{"status": TaskStatusActive})),
		format(count(tasks, bson.M{"status": TaskStatusPendingReview})),
		format(count(payments, bson.M{"status": sellerPaymentPaid, "currency": "XTR"})),
		format(count(withdrawals, bson.M{"status": bson.M{"$in": []string{"pending", "pending_review"}}})),
		time.Now().UTC().Format("02 Jan 2006 15:04"),
	)

	a.sendWithKeyboard(ctx, chatID, message, AdminKeyboard())
}

func (a *App) sendAdminTaskList(ctx context.Context, chatID, userID int64) {
	if !a.requireAdmin(chatID, userID) {
		return
	}

	tasks, err := a.Marketplace.ListTasks(ctx, "", 0, 20)
	if err != nil {
		a.sendPlain(ctx, chatID, "Could not load Marketplace tasks.")
		return
	}
	message := "📚 ALL MARKETPLACE TASKS\n\n" + FormatTaskList(tasks, "No tasks have been created yet.") + "\n\nTask actions:\n/approve_task TASK_ID\n/reject_task TASK_ID\n/pause_task TASK_ID\n/resume_task TASK_ID\n/delete_task TASK_ID"
	a.sendWithKeyboard(ctx, chatID, message, AdminKeyboard())
}

func (a *App) sendAdminUserList(ctx context.Context, chatID, userID int64, role string) {
	if !a.requireAdmin(chatID, userID) {
		return
	}
	if role != "user" && role != "seller" && role != "worker" {
		a.sendPlain(ctx, chatID, "Invalid account category.")
		return
	}

	filter := bson.M{"role": role}
	cursor, err := a.DB.DB.Collection("users").Find(
		ctx,
		filter,
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(20),
	)
	if err != nil {
		a.sendPlain(ctx, chatID, "Could not load account list.")
		return
	}
	defer cursor.Close(ctx)

	var users []User
	if err := cursor.All(ctx, &users); err != nil {
		a.sendPlain(ctx, chatID, "Could not read account list.")
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "👥 %s ACCOUNTS (LATEST 20)\n\n", strings.ToUpper(role))
	if len(users) == 0 {
		b.WriteString("No accounts found.")
	} else {
		for i, user := range users {
			username := ""
			if user.Username != "" {
				username = " (@" + user.Username + ")"
			}
			fmt.Fprintf(
				&b,
				"%d. %s%s\nTelegram ID: %d\nStatus: %s\nJoined: %s\n\n",
				i+1,
				safeName(user.FirstName),
				username,
				user.TelegramID,
				user.Status,
				user.CreatedAt.UTC().Format("02 Jan 2006"),
			)
		}
	}
	b.WriteString("\nAccount actions (Telegram ID):\n/ban USER_ID\n/unban USER_ID\n/suspend USER_ID\n/restore USER_ID")
	a.sendWithKeyboard(ctx, chatID, b.String(), AdminKeyboard())
}

func (a *App) sendAdminPayments(ctx context.Context, chatID, userID int64) {
	if !a.requireAdmin(chatID, userID) {
		return
	}

	collection := a.DB.DB.Collection("seller_payments")
	total, err := collection.CountDocuments(ctx, bson.M{})
	if err != nil {
		a.sendPlain(ctx, chatID, "Could not load payment records.")
		return
	}
	paid, _ := collection.CountDocuments(ctx, bson.M{"status": sellerPaymentPaid})
	pending, _ := collection.CountDocuments(ctx, bson.M{"status": sellerPaymentPending})

	cursor, err := collection.Find(
		ctx,
		bson.M{},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(10),
	)
	if err != nil {
		a.sendPlain(ctx, chatID, "Could not load recent payments.")
		return
	}
	defer cursor.Close(ctx)

	var records []SellerPayment
	if err := cursor.All(ctx, &records); err != nil {
		a.sendPlain(ctx, chatID, "Could not read payment records.")
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "💳 SELLER PAYMENT HISTORY\n\nTotal records: %d\nPaid: %d\nPending: %d\n\nLATEST 10\n", total, paid, pending)
	for _, record := range records {
		fmt.Fprintf(&b, "• User: %d | %d XTR | %s | %s\n", record.TelegramID, record.Amount, record.Status, record.CreatedAt.UTC().Format("02 Jan 15:04"))
	}
	if len(records) == 0 {
		b.WriteString("No payment records yet.")
	}
	a.sendWithKeyboard(ctx, chatID, b.String(), AdminKeyboard())
}

func (a *App) sendAdminWithdrawals(ctx context.Context, chatID, userID int64) {
	if !a.requireAdmin(chatID, userID) {
		return
	}

	collection := a.DB.DB.Collection("withdrawals")
	total, err := collection.CountDocuments(ctx, bson.M{})
	if err != nil {
		a.sendPlain(ctx, chatID, "Could not load withdrawal records.")
		return
	}
	pending, _ := collection.CountDocuments(ctx, bson.M{
		"status": bson.M{"$in": []string{"pending", "pending_review"}},
	})

	cursor, err := collection.Find(
		ctx,
		bson.M{},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(10),
	)
	if err != nil {
		a.sendPlain(ctx, chatID, "Could not load recent withdrawals.")
		return
	}
	defer cursor.Close(ctx)

	var records []Withdrawal
	if err := cursor.All(ctx, &records); err != nil {
		a.sendPlain(ctx, chatID, "Could not read withdrawal records.")
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "🏦 WITHDRAWAL MANAGEMENT\n\nTotal requests: %d\nPending review: %d\n\nLATEST 10\n", total, pending)
	if len(records) == 0 {
		b.WriteString("No withdrawal requests yet.\nThe BEP-20 hybrid withdrawal module is not active yet.")
	} else {
		for _, record := range records {
			fmt.Fprintf(&b, "• User: %d | %s USDT | %s | %s\n", record.UserID, record.AmountUSDT, record.Network, record.Status)
		}
	}
	b.WriteString("\nManual review commands:\n/approve_withdraw WITHDRAWAL_ID\n/reject_withdraw WITHDRAWAL_ID\n/mark_withdraw_paid WITHDRAWAL_ID TX_HASH\n\nMarking paid records an Admin-supplied transaction hash; it does not broadcast or verify a blockchain transfer.")
	a.sendWithKeyboard(ctx, chatID, b.String(), AdminKeyboard())
}

func (a *App) sendAdminHistory(ctx context.Context, chatID, userID int64) {
	if !a.requireAdmin(chatID, userID) {
		return
	}

	payments, err := a.DB.DB.Collection("seller_payments").CountDocuments(
		ctx,
		bson.M{"status": sellerPaymentPaid},
	)
	if err != nil {
		a.sendPlain(ctx, chatID, "Could not load transaction history.")
		return
	}
	withdrawals, err := a.DB.DB.Collection("withdrawals").CountDocuments(ctx, bson.M{})
	if err != nil {
		withdrawals = 0
	}

	message := fmt.Sprintf(
		"🧾 TRANSACTION HISTORY\n\nVerified Telegram Stars payments: %d\nWithdrawal records: %d\n\nTask and USDT earnings ledger will be connected in the financial module.",
		payments,
		withdrawals,
	)
	a.sendWithKeyboard(ctx, chatID, message, AdminKeyboard())
}

func (a *App) sendAdminSettings(ctx context.Context, chatID, userID int64) {
	if !a.requireAdmin(chatID, userID) {
		return
	}

	threshold := "Not configured"
	var setting struct {
		Value string `bson:"value"`
	}
	if err := a.DB.DB.Collection("marketplace_settings").FindOne(ctx, bson.M{"_id": "auto_withdraw_threshold"}).Decode(&setting); err == nil && setting.Value != "" {
		threshold = setting.Value + " USDT"
	}
	message := fmt.Sprintf(
		"⚙️ MARKETPLACE SETTINGS\n\nSeller registration: %d Telegram Star(s), one-time\nWithdrawal network: BEP-20\nWithdrawal mode: Hybrid\nBitget wallet funding: Friday\nAutomatic withdrawal threshold: %s\n\nSet threshold: /set_auto_withdraw_threshold AMOUNT\nExample: /set_auto_withdraw_threshold 5\n\nNote: This value is stored for configuration. Automatic blockchain payouts are not enabled by this setting alone.",
		SellerRegistrationStars, threshold,
	)
	a.sendWithKeyboard(ctx, chatID, message, AdminKeyboard())
}

// handleWithdrawalCommand performs database state transitions only. It does not
// send USDT; an Admin must verify the real BEP-20 transfer before marking paid.
func (a *App) handleWithdrawalCommand(ctx context.Context, msg *TelegramMessage, fields []string, action string) {
	if !a.requireAdmin(msg.Chat.ID, msg.From.ID) {
		return
	}
	if action == "paid" {
		if len(fields) != 3 || strings.TrimSpace(fields[2]) == "" {
			a.sendPlain(ctx, msg.Chat.ID, "Usage: /mark_withdraw_paid WITHDRAWAL_ID TX_HASH")
			return
		}
		result, err := a.DB.DB.Collection("withdrawals").UpdateOne(ctx, bson.M{"_id": fields[1], "status": "approved"}, bson.M{"$set": bson.M{"status": "paid", "tx_hash": strings.TrimSpace(fields[2]), "paid_at": time.Now().UTC(), "paid_by": msg.From.ID}})
		if err != nil || result.MatchedCount == 0 {
			a.sendPlain(ctx, msg.Chat.ID, "Withdrawal not found or not approved. Verify the transfer and Withdrawal ID first.")
			return
		}
		a.sendWithKeyboard(ctx, msg.Chat.ID, "Withdrawal marked paid with the supplied transaction hash. The bot did not broadcast or verify the blockchain transaction.", AdminKeyboard())
		return
	}
	if len(fields) != 2 {
		a.sendPlain(ctx, msg.Chat.ID, "Usage: /approve_withdraw WITHDRAWAL_ID or /reject_withdraw WITHDRAWAL_ID")
		return
	}
	fromStatuses := []string{"pending", "pending_review"}
	result, err := a.DB.DB.Collection("withdrawals").UpdateOne(ctx, bson.M{"_id": fields[1], "status": bson.M{"$in": fromStatuses}}, bson.M{"$set": bson.M{"status": action, "reviewed_at": time.Now().UTC(), "reviewed_by": msg.From.ID}})
	if err != nil || result.MatchedCount == 0 {
		a.sendPlain(ctx, msg.Chat.ID, "Withdrawal not found or it has already been reviewed.")
		return
	}
	a.sendWithKeyboard(ctx, msg.Chat.ID, "Withdrawal status updated: "+action+". Approval does not send funds; complete the transfer manually before marking paid.", AdminKeyboard())
}

func (a *App) handleThresholdCommand(ctx context.Context, msg *TelegramMessage, fields []string) {
	if !a.requireAdmin(msg.Chat.ID, msg.From.ID) {
		return
	}
	if len(fields) != 2 || !ValidateUSDTAmount(fields[1]) {
		a.sendPlain(ctx, msg.Chat.ID, "Usage: /set_auto_withdraw_threshold AMOUNT (positive USDT amount)")
		return
	}
	_, err := a.DB.DB.Collection("marketplace_settings").UpdateOne(ctx, bson.M{"_id": "auto_withdraw_threshold"}, bson.M{"$set": bson.M{"value": fields[1], "updated_at": time.Now().UTC(), "updated_by": msg.From.ID}}, options.Update().SetUpsert(true))
	if err != nil {
		a.sendPlain(ctx, msg.Chat.ID, "Could not save the threshold.")
		return
	}
	a.sendWithKeyboard(ctx, msg.Chat.ID, "Automatic withdrawal threshold saved as "+fields[1]+" USDT. This is a stored setting; automatic payouts still require the wallet module.", AdminKeyboard())
}
