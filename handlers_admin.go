package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/shopspring/decimal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- merged from admin_order_handlers.go ----

var adminRejectSessions sync.Map // map[admin user ID]order ID

func orderInt64(row bson.M, key string) int64 {
	v, _ := strconv.ParseInt(fmt.Sprint(row[key]), 10, 64)
	return v
}

func orderString(row bson.M, key, fallback string) string {
	v := strings.TrimSpace(fmt.Sprint(row[key]))
	if v == "" || v == "<nil>" {
		return fallback
	}
	return v
}

func (a *BotApp) AdminMessageHandler(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return false
	}
	uid := update.Message.From.ID
	isAdmin, err := IsAdmin(ctx, a.DB, a.Config, uid)
	if err != nil || !isAdmin {
		return false
	}
	if a.AdminWithdrawMessageHandler(ctx, b, update) {
		return true
	}
	if a.AdvancedAdminMessage(ctx, b, update) {
		return true
	}
	adminText := strings.TrimSpace(update.Message.Text)
	if a.adminToolsMessage(ctx, b, update.Message.From, update.Message.Chat.ID, adminText) {
		return true
	}
	target, rejectOK := adminRejectSessions.Load(uid)
	if !rejectOK {
		var restored int64
		if found, loadErr := loadWorkflowSession(ctx, a.DB, "admin_reject", uid, &restored); loadErr == nil && found {
			target, rejectOK = restored, true
			adminRejectSessions.Store(uid, restored)
		}
	}
	if rejectOK {
		reason := strings.TrimSpace(update.Message.Text)
		if reason == "" {
			a.sendText(ctx, b, update.Message.Chat.ID, "❌ অনুগ্রহ করে Reject Reason লিখে পাঠান।", nil)
			return true
		}
		orderID := target.(int64)
		res, e := a.DB.Orders.UpdateOne(ctx, bson.M{"id": orderID, "status": bson.M{"$in": []string{"pending_verification", "approved_pending_payout", "approved_pending_payment"}}}, bson.M{"$set": bson.M{"status": "rejected", "reject_reason": reason, "rejected_at": BangladeshTime()}})
		adminRejectSessions.Delete(uid)
		_ = deleteWorkflowSession(ctx, a.DB, "admin_reject", uid)
		if e != nil || res.ModifiedCount != 1 {
			a.sendText(ctx, b, update.Message.Chat.ID, "❌ অর্ডারটি Reject করা যায়নি; এটি হয়তো ইতোমধ্যে পরিবর্তিত হয়েছে।", nil)
			return true
		}
		var row bson.M
		_ = a.DB.Orders.FindOne(ctx, bson.M{"id": orderID}).Decode(&row)
		if row != nil {
			userID := orderInt64(row, "user_id")
			a.sendPhotoOrText(ctx, b, userID, a.Config.PhotoOrderRejected, fmt.Sprintf("❌ <b>Sell Order Rejected</b>\n\nOrder: <code>#%d</code>\nকারণ: %s\n\nপ্রয়োজনে Support-এ যোগাযোগ করুন।", orderID, htmlEscape(reason)), MainMenu())
			_ = LogAdminAction(ctx, a.DB, uid, update.Message.From.Username, strconv.FormatInt(orderID, 10), orderString(row, "txid", "N/A"), "Rejected", orderString(row, "amount", "0"), orderString(row, "total_bdt", "0"))
		}
		a.sendText(ctx, b, update.Message.Chat.ID, fmt.Sprintf("✅ Order #%d Reject করা হয়েছে।", orderID), nil)
		return true
	}

	if a.adminParityMessage(ctx, b, update) {
		return true
	}

	text := strings.TrimSpace(update.Message.Text)
	switch {
	case text == "⏳ Pending Orders" || strings.HasPrefix(text, "⏳ Pending Orders ("):
		a.sendAdminOrderList(ctx, b, update.Message.Chat.ID, "pending")
		return true
	case text == "💸 Pending Payments" || strings.HasPrefix(text, "💸 Pending Payments ("):
		a.sendAdminOrderList(ctx, b, update.Message.Chat.ID, "payments")
		return true
	case strings.HasPrefix(text, "🛠️ Support Tickets"):
		a.ShowAdminSupportTickets(ctx, b, update.Message.Chat.ID)
		return true
	case text == "📦 Sell Order History":
		a.sendAdminOrderList(ctx, b, update.Message.Chat.ID, "history")
		return true
	case text == "🏧 Withdrawal Requests" || strings.HasPrefix(text, "🏧 Withdrawal Requests ("):
		a.SendPendingWithdrawals(ctx, b, update.Message.Chat.ID)
		return true
	}
	return false
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return r.Replace(s)
}

func (a *BotApp) sendAdminOrderList(ctx context.Context, b *bot.Bot, chatID int64, kind string) {
	statuses := []string{"paid", "rejected", "permanently_rejected", "approved_pending_payment", "approved_pending_payout", "pending_verification"}
	if kind == "pending" {
		statuses = []string{"pending_verification"}
	}
	if kind == "payments" {
		statuses = []string{"approved_pending_payment"}
	}
	cursor, err := a.DB.Orders.Find(ctx, bson.M{"status": bson.M{"$in": statuses}, "type": bson.M{"$ne": "admin_add"}}, optionsFindNewest25())
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Order list লোড করা যায়নি।", nil)
		return
	}
	defer cursor.Close(ctx)
	count := 0
	for cursor.Next(ctx) {
		var row bson.M
		if cursor.Decode(&row) != nil {
			continue
		}
		count++
		id := orderInt64(row, "id")
		userID := orderInt64(row, "user_id")
		payoutNumber := orderString(row, "payout_number", "N/A")
		if strings.HasPrefix(payoutNumber, "Photo [File ID:") {
			payoutNumber = "🖼️ [QR Code Image Attached]"
		}
		body := fmt.Sprintf("📦 <b>Sell Order <code>#%d</code></b>\n👤 User ID: <code>%d</code>\n💰 Amount: <code>%s USDT</code>\n💵 Total: ৳<code>%s</code>\n🌐 Method: <b>%s</b>\n📌 Status: <code>%s</code>\n💳 Payout: <b>%s</b>\n📱 Number: <code>%s</code>\n⏱️ Time: <i>%s</i>", id, userID, orderString(row, "amount", "0"), orderString(row, "total_bdt", "0"), orderString(row, "network", "N/A"), orderString(row, "status", "unknown"), orderString(row, "payout", "N/A"), htmlEscape(payoutNumber), orderString(row, "created_at", "N/A"))
		if reason := orderString(row, "reject_reason", ""); reason != "" {
			body += "\n💬 Reject Reason: <b>" + htmlEscape(reason) + "</b>"
		}
		buttons := [][]models.InlineKeyboardButton{}
		switch orderString(row, "status", "") {
		case "pending_verification":
			buttons = append(buttons, []models.InlineKeyboardButton{{Text: "✅ Approve", CallbackData: fmt.Sprintf("approve:%d", id)}, {Text: "❌ Reject Request", CallbackData: fmt.Sprintf("reject:%d", id)}})
		case "approved_pending_payment":
			buttons = append(buttons, []models.InlineKeyboardButton{{Text: "💸 Confirm Payment Sent", CallbackData: fmt.Sprintf("paid:%d", id)}})
		case "rejected":
			buttons = append(buttons, []models.InlineKeyboardButton{{Text: "♻️ Approve Again", CallbackData: fmt.Sprintf("reapprove:%d", id)}, {Text: "🗑️ Reject Permanently", CallbackData: fmt.Sprintf("hard_reject:%d", id)}})
		}
		var markup models.ReplyMarkup
		if len(buttons) > 0 {
			markup = &models.InlineKeyboardMarkup{InlineKeyboard: buttons}
		}
		if fileID, ok := withdrawQRFileID(orderString(row, "payout_number", "")); ok {
			_, sendErr := b.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: chatID, Photo: &models.InputFileString{Data: fileID}, Caption: body, ParseMode: models.ParseModeHTML, ReplyMarkup: markup})
			if sendErr == nil {
				continue
			}
		}
		a.sendText(ctx, b, chatID, body, markup)
	}
	if count == 0 {
		a.sendText(ctx, b, chatID, "কোনো সেল অর্ডার পাওয়া যায়নি।", nil)
	}
	po, pp, pw, ps, _ := a.AdminCounts(ctx)
	a.sendText(ctx, b, chatID, "📊 Admin Menu", AdminMenuWithCounts(chatID, a.Config.AdminID, po, pp, pw, ps))
}

func optionsFindNewest25() *options.FindOptions {
	return options.Find().SetSort(bson.D{{Key: "id", Value: -1}}).SetLimit(25)
}

func (a *BotApp) AdminOrderCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	if update == nil || update.CallbackQuery == nil {
		return false
	}
	q := update.CallbackQuery
	if strings.HasPrefix(q.Data, "rate:") {
		parts := strings.Split(q.Data, ":")
		if len(parts) == 3 {
			rating, e1 := strconv.Atoi(parts[1])
			orderID, e2 := strconv.ParseInt(parts[2], 10, 64)
			if e1 == nil && e2 == nil && rating >= 1 && rating <= 5 {
				var order bson.M
				if a.DB.Orders.FindOne(ctx, bson.M{"id": orderID, "user_id": q.From.ID, "status": "paid"}).Decode(&order) == nil {
					count, _ := a.DB.Reviews.CountDocuments(ctx, bson.M{"user_id": q.From.ID, "order_id": orderID})
					if count == 0 {
						_, _ = a.DB.Reviews.InsertOne(ctx, bson.M{"user_id": q.From.ID, "order_id": orderID, "rating": rating, "text": "User Star Rating", "timestamp": BangladeshTime()})
						_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "⭐ আপনার মূল্যবান রেটিংয়ের জন্য ধন্যবাদ!", ShowAlert: true})
					} else {
						_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "আপনি এই অর্ডারে ইতোমধ্যে রেটিং দিয়েছেন।", ShowAlert: true})
					}
				} else {
					_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "এই অর্ডারের রেটিং দেওয়ার অনুমতি আপনার নেই।", ShowAlert: true})
				}
				return true
			}
		}
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "রেটিং তথ্য সঠিক নয়।", ShowAlert: true})
		return true
	}
	if a.handlePayoutCallback(ctx, b, update) {
		return true
	}
	if a.handleSellOperatorCallback(ctx, b, update) {
		return true
	}
	data := q.Data
	parts := strings.Split(data, ":")
	if len(parts) != 2 {
		return false
	}
	action := parts[0]
	if action != "approve" && action != "reject" && action != "paid" && action != "reapprove" && action != "hard_reject" {
		return false
	}
	isAdmin, err := IsAdmin(ctx, a.DB, a.Config, q.From.ID)
	if err != nil || !isAdmin {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "এই কাজের অনুমতি নেই।", ShowAlert: true})
		return true
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Invalid order ID", ShowAlert: true})
		return true
	}
	var row bson.M
	if err = a.DB.Orders.FindOne(ctx, bson.M{"id": id}).Decode(&row); err != nil {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "অর্ডারটি পাওয়া যায়নি।", ShowAlert: true})
		return true
	}
	status := orderString(row, "status", "")
	switch action {
	case "reject":
		adminRejectSessions.Store(q.From.ID, id)
		_ = saveWorkflowSession(ctx, a.DB, "admin_reject", q.From.ID, id, 24*time.Hour)
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
		a.sendText(ctx, b, q.From.ID, fmt.Sprintf("❌ <b>Order Reject Reason Required</b>\n\nঅর্ডার <code>#%d</code> রিজেক্ট করার কারণটি লিখে পাঠান:", id), nil)
		return true
	case "approve", "reapprove":
		if action == "approve" && status != "pending_verification" {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "এই অর্ডারটি আর Pending Verification-এ নেই।", ShowAlert: true})
			return true
		}
		if action == "reapprove" && (status != "rejected" || q.From.ID != a.Config.AdminID) {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "শুধু Permanent Admin rejected order পুনরায় approve করতে পারবেন।", ShowAlert: true})
			return true
		}
		res, e := a.DB.Orders.UpdateOne(ctx, bson.M{"id": id, "status": status}, bson.M{"$set": bson.M{"status": "approved_pending_payout", "approved_at": BangladeshTime(), "reject_reason": ""}})
		if e != nil || res.ModifiedCount != 1 {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Order update করা যায়নি।", ShowAlert: true})
			return true
		}
		_ = LogAdminAction(ctx, a.DB, q.From.ID, q.From.Username, strconv.FormatInt(id, 10), orderString(row, "txid", "N/A"), map[bool]string{true: "Re-approved", false: "Approved"}[action == "reapprove"], orderString(row, "amount", "0"), orderString(row, "total_bdt", "0"))
		markup, menuErr := CreatePayoutMenu(ctx, a.DB, "payout", id, orderInt64(row, "user_id"))
		if menuErr != nil {
			markup = nil
		}
		msg := "💬 <b>ⒹⓇ Reply</b>\n\n✅ Order Approved Successfully!\n💳 কোন নম্বরে টাকা নিতে চান? নিচের অপশন থেকে নির্বাচন করুন..."
		if action == "reapprove" {
			msg = "💬 <b>ⒹⓇ Reply</b>\n\n✅ আপনার বাতিল হওয়া অর্ডারটি পুনরায় অনুমোদন করা হয়েছে!\n💳 Payout method নির্বাচন করুন..."
		}
		a.sendText(ctx, b, orderInt64(row, "user_id"), msg, markup)
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Order approved"})
		a.sendText(ctx, b, q.From.ID, fmt.Sprintf("✅ Order #%d Approved.", id), nil)
		return true
	case "hard_reject":
		if q.From.ID != a.Config.AdminID {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "শুধুমাত্র Permanent Admin এই কাজ করতে পারবেন।", ShowAlert: true})
			return true
		}
		res, e := a.DB.Orders.UpdateOne(ctx, bson.M{"id": id, "status": "rejected"}, bson.M{"$set": bson.M{"status": "permanently_rejected"}})
		if e != nil || res.ModifiedCount != 1 {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Order update করা যায়নি।", ShowAlert: true})
			return true
		}
		_ = LogAdminAction(ctx, a.DB, q.From.ID, q.From.Username, strconv.FormatInt(id, 10), orderString(row, "txid", "N/A"), "Permanently Rejected", orderString(row, "amount", "0"), orderString(row, "total_bdt", "0"))
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Permanently rejected"})
		a.sendText(ctx, b, q.From.ID, fmt.Sprintf("🗑️ Order #%d permanently rejected.", id), nil)
		return true
	case "paid":
		if status == "paid" {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "অর্ডারটি ইতিমধ্যে Paid হয়েছে।", ShowAlert: true})
			return true
		}
		if status != "approved_pending_payment" && status != "approved_pending_payout" && status != "pending" {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "এই Status থেকে Paid করা যাবে না।", ShowAlert: true})
			return true
		}
		if orderString(row, "payout_number", "") == "" {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Payment number আগে সেট করতে হবে।", ShowAlert: true})
			return true
		}
		if err := a.completeSellOrder(ctx, b, &q.From, id, row); err != nil {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Payment completion ব্যর্থ: " + err.Error(), ShowAlert: true})
			return true
		}
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Order marked as Paid"})
		a.sendText(ctx, b, q.From.ID, fmt.Sprintf("✅ Order #%d marked as Paid.", id), nil)
		return true
	}
	return false
}

func (a *BotApp) completeSellOrder(ctx context.Context, b *bot.Bot, admin *models.User, id int64, row bson.M) error {
	status := orderString(row, "status", "")
	res, err := a.DB.Orders.UpdateOne(ctx, bson.M{"id": id, "status": status}, bson.M{"$set": bson.M{"status": "paid", "paid_at": BangladeshTime()}})
	if err != nil {
		return err
	}
	if res.ModifiedCount != 1 {
		return fmt.Errorf("order status changed; reload the order")
	}
	userID := orderInt64(row, "user_id")
	usedBonus, _ := decimal.NewFromString(strings.ReplaceAll(orderString(row, "ref_bonus_used", "0"), ",", ""))
	if usedBonus.IsPositive() {
		var u bson.M
		if a.DB.Users.FindOne(ctx, bson.M{"user_id": userID}).Decode(&u) == nil {
			curr, _ := decimal.NewFromString(strings.ReplaceAll(orderString(u, "referral_bonus_bdt", "0"), ",", ""))
			next := curr.Sub(usedBonus)
			if next.IsNegative() {
				next = decimal.Zero
			}
			_, _ = a.DB.Users.UpdateOne(ctx, bson.M{"user_id": userID}, bson.M{"$set": bson.M{"referral_bonus_bdt": next.String(), "withdraw_requested": false}})
		}
	}
	bdt, _ := decimal.NewFromString(strings.ReplaceAll(orderString(row, "bdt", orderString(row, "total_bdt", "0")), ",", ""))
	var user bson.M
	if a.DB.Users.FindOne(ctx, bson.M{"user_id": userID}).Decode(&user) == nil {
		refID := int64(0)
		switch v := user["referred_by"].(type) {
		case int64:
			refID = v
		case int32:
			refID = int64(v)
		case int:
			refID = int64(v)
		case float64:
			refID = int64(v)
		}
		if refID > 0 {
			paidCount, _ := a.DB.Orders.CountDocuments(ctx, bson.M{"user_id": userID, "status": "paid", "type": bson.M{"$ne": "admin_add"}})
			var ref bson.M
			if a.DB.Users.FindOne(ctx, bson.M{"user_id": refID}).Decode(&ref) == nil {
				firstPct, lifePct := decimal.NewFromFloat(.05), decimal.NewFromFloat(.005)
				if raw, ok, e := GetSetting(ctx, a.DB, "ref_percentages"); e == nil && ok {
					var p map[string]string
					if json.Unmarshal([]byte(raw), &p) == nil {
						if v, e := decimal.NewFromString(p["first_sale"]); e == nil {
							firstPct = v
						}
						if v, e := decimal.NewFromString(p["lifetime"]); e == nil {
							lifePct = v
						}
					}
				}
				commission := decimal.Zero
				lines := ""
				if paidCount == 1 {
					first := bdt.Mul(firstPct).Truncate(0)
					if first.LessThan(decimal.NewFromInt(1)) {
						first = decimal.NewFromInt(1)
					}
					commission = commission.Add(first)
					lines += fmt.Sprintf("• 1st Sale Bonus: <b>৳%s</b>\n", Money(first))
					_, _ = a.DB.Users.UpdateOne(ctx, bson.M{"user_id": refID}, bson.M{"$inc": bson.M{"valid_referrals": 1}})
				}
				life := bdt.Mul(lifePct).Truncate(0)
				if life.IsPositive() {
					commission = commission.Add(life)
					lines += fmt.Sprintf("• Lifetime/Transaction Commission: <b>৳%s</b>\n", Money(life))
				}
				if commission.IsPositive() {
					old, _ := decimal.NewFromString(orderString(ref, "referral_bonus_bdt", "0"))
					_, _ = a.DB.Users.UpdateOne(ctx, bson.M{"user_id": refID}, bson.M{"$set": bson.M{"referral_bonus_bdt": old.Add(commission).String()}})
					a.sendText(ctx, b, refID, "🎉 <b>Referral Commission Received!</b>\n\nআপনার রেফার করা ইউজার সফলভাবে সেল সম্পন্ন করেছেন।\n"+lines+"মোট যুক্ত হয়েছে: <b>৳"+Money(commission)+"</b>", nil)
				}
			}
		}
	}
	_ = LogAdminAction(ctx, a.DB, admin.ID, admin.Username, strconv.FormatInt(id, 10), orderString(row, "txid", "N/A"), "Paid / Completed", orderString(row, "amount", "0"), orderString(row, "total_bdt", "0"))
	payout := orderString(row, "payout", orderString(row, "method", "Wallet"))
	payoutType := strings.TrimSpace(strings.Split(payout, "(")[0])
	number := orderString(row, "payout_number", "")
	wallet := payoutType
	if number != "" {
		wallet += " (`" + htmlEscape(MaskNumber(number)) + "`)"
	}
	paidMsg := fmt.Sprintf("✅ <b>Order Status Update (Payment Sent)</b>\n\n📦 ID : <code>#%d</code>\n💵 Amount : ৳<code>%s</code>\n💳 Payout : <b>%s</b>\n📌 Status : <code>Completed / Paid ✅</code>\n⏱️ Time : <i>%s</i>", id, orderString(row, "total_bdt", orderString(row, "amount", "0")), wallet, BangladeshTime())
	if strings.TrimSpace(a.Config.ChannelID) != "" {
		channelID := any(a.Config.ChannelID)
		if n, e := strconv.ParseInt(a.Config.ChannelID, 10, 64); e == nil {
			channelID = n
		}
		sent, e := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: channelID, Text: paidMsg, ParseMode: models.ParseModeHTML})
		if e == nil && sent != nil {
			_, _ = a.DB.Orders.UpdateOne(ctx, bson.M{"id": id}, bson.M{"$set": bson.M{"channel_msg_id": sent.ID}})
		}
	}
	a.sendPhotoOrText(ctx, b, userID, a.Config.PhotoPaymentSent, fmt.Sprintf("🎉 <b>আপনার পেমেন্ট সফলভাবে পাঠানো হয়েছে!</b>\n\n📦 Order <code>#%d</code>\n💵 Amount: ৳<b>%s</b>\n💳 Payout: <b>%s</b>", id, orderString(row, "total_bdt", orderString(row, "amount", "0")), htmlEscape(payout)), MainMenu())
	reviewButtons := []models.InlineKeyboardButton{}
	for i := 1; i <= 5; i++ {
		reviewButtons = append(reviewButtons, models.InlineKeyboardButton{Text: fmt.Sprintf("⭐ %d", i), CallbackData: fmt.Sprintf("rate:%d:%d", i, id)})
	}
	a.sendText(ctx, b, userID, fmt.Sprintf("🌟 <b>Your order #%d has been completed!</b>\nআপনার অভিজ্ঞতা কেমন ছিল? নিচে ১–৫ স্টার দিয়ে সার্ভিসটি রেট করুন।", id), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{reviewButtons}})
	// Resolve open tickets associated with this order, matching the Python workflow.
	_, _ = a.DB.Tickets.UpdateMany(ctx, bson.M{"order_id": strconv.FormatInt(id, 10), "status": "open"}, bson.M{"$set": bson.M{"status": "resolved"}})
	return nil
}

type payoutEntrySession struct {
	OrderID int64
	Method  string
	Amount  string
	Step    string
}

var payoutEntrySessions sync.Map // map[user ID]payoutEntrySession

func (a *BotApp) handlePayoutCallback(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	q := update.CallbackQuery
	if !strings.HasPrefix(q.Data, "payout:") {
		return false
	}
	parts := strings.SplitN(strings.TrimPrefix(q.Data, "payout:"), ":", 2)
	if len(parts) != 2 {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Invalid payout selection", ShowAlert: true})
		return true
	}
	method := parts[0]
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Invalid order ID", ShowAlert: true})
		return true
	}
	var row bson.M
	if a.DB.Orders.FindOne(ctx, bson.M{"id": id, "user_id": q.From.ID, "status": "approved_pending_payout"}).Decode(&row) != nil {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "এই অর্ডারটি আর payout selection-এর জন্য সক্রিয় নেই।", ShowAlert: true})
		return true
	}
	valid := map[string]bool{"bKash": true, "Nagad": true, "Rocket": true, "Upay": true, "Recharge": true, "Bangla QR": true}
	if !valid[method] {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Invalid payment method", ShowAlert: true})
		return true
	}
	active, e := GetActivePayoutMethods(ctx, a.DB)
	if e != nil {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Payment methods লোড করা যায়নি।", ShowAlert: true})
		return true
	}
	allowed := false
	for _, m := range active {
		if m == method {
			allowed = true
		}
	}
	if !allowed {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "এই payment method বর্তমানে বন্ধ।", ShowAlert: true})
		return true
	}
	amount, _ := decimal.NewFromString(orderString(row, "amount", "0"))
	rate, _ := decimal.NewFromString(orderString(row, "rate", "0"))
	if !rate.IsPositive() {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Order rate পাওয়া যায়নি; Admin-কে জানান।", ShowAlert: true})
		return true
	}
	raw := amount.Mul(rate).Truncate(2)
	fee := decimal.NewFromInt(5)
	if strings.EqualFold(method, "Recharge") {
		fee = decimal.Zero
	}
	net := raw.Sub(fee)
	if net.IsNegative() {
		net = decimal.Zero
	}
	bonus, _ := decimal.NewFromString(strings.ReplaceAll(orderString(row, "ref_bonus_used", "0"), ",", ""))
	withBonus := net.Add(bonus)
	whole := withBonus.Truncate(0)
	rem := whole.Mod(decimal.NewFromInt(10))
	rounded := whole.Sub(rem)
	diff := rem
	walletOperation := fmt.Sprintf("order:%d:rounding_wallet_credit", id)
	applied, txErr := ApplyWalletDeltaAnd(ctx, a.DB, q.From.ID, diff, walletOperation,
		"order_payout_rounding_credit", strconv.FormatInt(id, 10),
		func(tx context.Context) error {
			res, updateErr := a.DB.Orders.UpdateOne(tx, bson.M{"id": id, "user_id": q.From.ID, "status": "approved_pending_payout", "payout": bson.M{"$in": []interface{}{"", nil}}}, bson.M{"$set": bson.M{"payout": method, "bdt": Money(net), "total_bdt": Money(rounded), "wallet_diff": func() string {
				if diff.IsPositive() {
					return "+" + diff.String()
				}
				return "0"
			}(), "payout_selected_at": BangladeshTime()}})
			if updateErr != nil {
				return updateErr
			}
			if res.ModifiedCount != 1 {
				return errors.New("payout already selected or order is no longer pending")
			}
			return nil
		})
	if txErr != nil || !applied {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "এই অর্ডারের payout method নির্বাচন করা যায়নি; Wallet balance পরিবর্তন হয়নি।", ShowAlert: true})
		return true
	}
	var user bson.M
	_ = a.DB.Users.FindOne(ctx, bson.M{"user_id": q.From.ID}).Decode(&user)
	if method != "Recharge" && method != "Bangla QR" {
		if nums, ok := user["payment_numbers"].(bson.M); ok {
			if saved := strings.TrimSpace(fmt.Sprint(nums[method])); saved != "" && saved != "<nil>" {
				a.submitSellPayoutNumber(ctx, b, q.From.ID, id, method, saved)
				_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
				return true
			}
		}
	}
	session := payoutEntrySession{OrderID: id, Method: method, Step: "number"}
	if method == "Recharge" {
		session.Step = "recharge_number"
	}
	if method == "Bangla QR" {
		session.Step = "bangla_qr_photo"
	}
	payoutEntrySessions.Store(q.From.ID, session)
	_ = saveWorkflowSession(ctx, a.DB, "payout_entry", q.From.ID, session, 7*24*time.Hour)
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
	prompt := fmt.Sprintf("📦 Order ID <code>#%d</code>\n🌐 Method: <b>%s</b>\n💰 Amount: <code>%s USDT</code>\n💵 You receive: <b>%s BDT</b>\n💳 Wallet adjustment: <b>%s৳</b>\n\n👉 আপনার %s নম্বরটি দিন:", id, htmlEscape(method), orderString(row, "amount", "0"), Money(rounded), diff, htmlEscape(method))
	if method == "Recharge" {
		prompt += "\n🔸 আপনার Recharge নম্বরটি দিন:"
	}
	if method == "Bangla QR" {
		prompt = fmt.Sprintf("📦 Order ID <code>#%d</code>\n💳 Payout: <b>Bangla QR</b>\n💵 You receive: <b>%s BDT</b>\n\n👉 আপনার Bangla QR-এর পরিষ্কার ছবি আপলোড করুন:", id, Money(rounded))
	}
	a.sendText(ctx, b, q.From.ID, prompt, nil)
	return true
}

func (a *BotApp) handlePayoutNumberMessage(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return false
	}
	uid := update.Message.From.ID
	value, ok := payoutEntrySessions.Load(uid)
	if !ok {
		var restored payoutEntrySession
		if found, err := loadWorkflowSession(ctx, a.DB, "payout_entry", uid, &restored); err == nil && found {
			value, ok = restored, true
			payoutEntrySessions.Store(uid, restored)
		}
	}
	if !ok {
		return false
	}
	session := value.(payoutEntrySession)
	msg := update.Message
	number := strings.TrimSpace(msg.Text)
	if session.Method == "Bangla QR" {
		if len(msg.Photo) == 0 {
			a.sendText(ctx, b, uid, "❌ দয়া করে আপনার Bangla QR কোডের একটি পরিষ্কার ছবি বা স্ক্রিনশট দিন।", nil)
			return true
		}
		fileID := msg.Photo[len(msg.Photo)-1].FileID
		stored := "Photo [File ID: " + fileID + "]"
		res, updateErr := a.DB.Orders.UpdateOne(ctx, bson.M{"id": session.OrderID, "user_id": uid, "status": "approved_pending_payout", "payout": "Bangla QR"}, bson.M{"$set": bson.M{"payout_number": stored, "status": "approved_pending_payment"}})
		if updateErr != nil || res.ModifiedCount != 1 {
			a.sendText(ctx, b, uid, "❌ Order update করা যায়নি। Support-এ যোগাযোগ করুন।", MainMenu())
			return true
		}
		payoutEntrySessions.Delete(uid)
		_ = deleteWorkflowSession(ctx, a.DB, "payout_entry", uid)
		var row bson.M
		_ = a.DB.Orders.FindOne(ctx, bson.M{"id": session.OrderID}).Decode(&row)
		adminText := fmt.Sprintf("💸 <b>Pending Payment (Bangla QR) — Order <code>#%d</code></b>\n👤 User ID: <code>%d</code>\n💰 Amount: <code>%s USDT</code>\n💵 Total: ৳<code>%s</code>\n🌐 Payout: <b>Bangla QR</b>\n⏱️ Time: <i>%s</i>", session.OrderID, uid, orderString(row, "amount", "0"), orderString(row, "total_bdt", "0"), orderString(row, "created_at", "N/A"))
		markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "💸 Confirm Payment Sent", CallbackData: fmt.Sprintf("paid:%d", session.OrderID)}, {Text: "📱 Request New Payment Details", CallbackData: fmt.Sprintf("req_alt_%d_%d", uid, session.OrderID)}}}}
		_, _ = b.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: a.Config.AdminID, Photo: &models.InputFileString{Data: fileID}, Caption: adminText, ParseMode: models.ParseModeHTML, ReplyMarkup: markup})
		_, _ = b.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: uid, Photo: &models.InputFileString{Data: fileID}, Caption: fmt.Sprintf("📦 Sell Order ID: <code>#%d</code>\n💳 Payout: <b>Bangla QR</b> (🖼️ QR Code)\n\n✅ Successfully submitted.\n⏳ Waiting for admin's Payment...", session.OrderID), ParseMode: models.ParseModeHTML, ReplyMarkup: MainMenu()})
		return true
	}
	clean := strings.NewReplacer(" ", "", "-", "").Replace(number)
	if strings.HasPrefix(clean, "+88") {
		clean = strings.TrimPrefix(clean, "+88")
	}
	if strings.HasPrefix(clean, "88") && len(clean) == 13 {
		clean = strings.TrimPrefix(clean, "88")
	}
	prefixOK := len(clean) == 11
	if prefixOK {
		prefixOK = false
		for _, prefix := range []string{"013", "014", "015", "016", "017", "018", "019"} {
			if strings.HasPrefix(clean, prefix) {
				prefixOK = true
				break
			}
		}
	}
	if !prefixOK {
		if session.Method == "Recharge" {
			a.sendText(ctx, b, uid, "😡 সঠিক BD number দিন...", nil)
		} else {
			a.sendText(ctx, b, uid, "❌ সঠিক বাংলাদেশি মোবাইল নম্বর দিন। উদাহরণ: <code>01712345678</code>", nil)
		}
		return true
	}
	number = clean
	if session.Method == "Recharge" {
		_, updateErr := a.DB.Orders.UpdateOne(ctx, bson.M{"id": session.OrderID, "user_id": uid, "status": "approved_pending_payout", "payout": "Recharge"}, bson.M{"$set": bson.M{"payout_number": number}})
		if updateErr != nil {
			a.sendText(ctx, b, uid, "❌ Recharge number save করা যায়নি।", nil)
			return true
		}
		session.Step = "recharge_operator"
		payoutEntrySessions.Store(uid, session)
		_ = saveWorkflowSession(ctx, a.DB, "payout_entry", uid, session, 7*24*time.Hour)
		a.sendText(ctx, b, uid, "🔸 আপনার সিমটি সিলেক্ট করুন নিচে থেকে...", sellOperatorMenu(session.OrderID))
		return true
	}
	payoutEntrySessions.Delete(uid)
	_ = deleteWorkflowSession(ctx, a.DB, "payout_entry", uid)
	if session.Method == "bKash" || session.Method == "Nagad" || session.Method == "Rocket" || session.Method == "Upay" {
		_, _ = a.DB.Users.UpdateOne(ctx, bson.M{"user_id": uid}, bson.M{"$set": bson.M{"payment_numbers." + session.Method: number}})
	}
	a.submitSellPayoutNumber(ctx, b, uid, session.OrderID, session.Method, number)
	return true
}

func (a *BotApp) submitSellPayoutNumber(ctx context.Context, b *bot.Bot, userID, orderID int64, method, number string) {
	res, err := a.DB.Orders.UpdateOne(ctx, bson.M{"id": orderID, "user_id": userID, "status": "approved_pending_payout", "payout": method}, bson.M{"$set": bson.M{"payout_number": number, "status": "approved_pending_payment"}})
	if err != nil || res.ModifiedCount != 1 {
		a.sendText(ctx, b, userID, "❌ Order update করা যায়নি। Support-এ যোগাযোগ করুন।", MainMenu())
		return
	}
	var row bson.M
	_ = a.DB.Orders.FindOne(ctx, bson.M{"id": orderID}).Decode(&row)
	adminText := fmt.Sprintf("💸 <b>Pending Payment — Order <code>#%d</code></b>\n👤 User ID: <code>%d</code>\n💰 Amount: <code>%s USDT</code>\n💵 Total: ৳<code>%s</code>\n💳 Payout: <b>%s</b>\n📱 Number: <code>%s</code>\n⏱️ Time: <i>%s</i>", orderID, userID, orderString(row, "amount", "0"), orderString(row, "total_bdt", "0"), htmlEscape(orderString(row, "payout", method)), htmlEscape(number), orderString(row, "created_at", "N/A"))
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "💸 Confirm Payment Sent", CallbackData: fmt.Sprintf("paid:%d", orderID)}}}}
	a.sendText(ctx, b, a.Config.AdminID, adminText, markup)
	a.sendPhotoOrText(ctx, b, userID, a.Config.PhotoNumberSubmitted, fmt.Sprintf("📦 Sell Order ID: <code>#%d</code>\n💳 Payout: <b>%s</b> (<code>%s</code>)\n\n✅ আপনার অর্ডার সফলভাবে জমা হয়েছে।\n⏳ অ্যাডমিন পেমেন্ট পাঠানো পর্যন্ত অপেক্ষা করুন।", orderID, htmlEscape(method), htmlEscape(number)), MainMenu())
}

func sellOperatorMenu(orderID int64) *models.InlineKeyboardMarkup {
	operators := []string{"GP", "Skito", "Teletalk", "Robi", "Banglalink", "Circle"}
	rows := [][]models.InlineKeyboardButton{}
	for i := 0; i < len(operators); i += 3 {
		end := i + 3
		if end > len(operators) {
			end = len(operators)
		}
		row := []models.InlineKeyboardButton{}
		for _, op := range operators[i:end] {
			row = append(row, models.InlineKeyboardButton{Text: op, CallbackData: fmt.Sprintf("op:%s:%d", op, orderID)})
		}
		rows = append(rows, row)
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (a *BotApp) handleSellOperatorCallback(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	if update == nil || update.CallbackQuery == nil || !strings.HasPrefix(update.CallbackQuery.Data, "op:") {
		return false
	}
	q := update.CallbackQuery
	parts := strings.Split(q.Data, ":")
	if len(parts) != 3 {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Invalid operator", ShowAlert: true})
		return true
	}
	allowed := map[string]bool{"GP": true, "Skito": true, "Teletalk": true, "Robi": true, "Banglalink": true, "Circle": true}
	if !allowed[parts[1]] {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Invalid operator", ShowAlert: true})
		return true
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Invalid order", ShowAlert: true})
		return true
	}
	var row bson.M
	if err = a.DB.Orders.FindOne(ctx, bson.M{"id": id, "user_id": q.From.ID, "status": "approved_pending_payout", "payout": "Recharge"}).Decode(&row); err != nil {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "এই Order-এর Recharge operator selection আর সক্রিয় নেই।", ShowAlert: true})
		return true
	}
	updatedPayout := "Recharge (" + parts[1] + ")"
	res, err := a.DB.Orders.UpdateOne(ctx, bson.M{"id": id, "user_id": q.From.ID, "status": "approved_pending_payout", "payout": "Recharge"}, bson.M{"$set": bson.M{"payout": updatedPayout, "status": "approved_pending_payment"}})
	if err != nil || res.ModifiedCount != 1 {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Order update করা যায়নি।", ShowAlert: true})
		return true
	}
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
	if q.Message.Message != nil {
		_, _ = b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{ChatID: q.Message.Message.Chat.ID, MessageID: q.Message.Message.ID, ReplyMarkup: nil})
	}
	adminText := fmt.Sprintf("💸 <b>Pending Payment — Order <code>#%d</code></b>\n👤 User ID: <code>%d</code>\n💰 Amount: <code>%s USDT</code>\n💵 Total: ৳<code>%s</code>\n💳 Payout: <b>%s</b>\n📱 Number: <code>%s</code>\n⏱️ Time: <i>%s</i>", id, q.From.ID, orderString(row, "amount", "0"), orderString(row, "total_bdt", "0"), htmlEscape(updatedPayout), htmlEscape(orderString(row, "payout_number", "N/A")), orderString(row, "created_at", "N/A"))
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "💸 Confirm Payment Sent", CallbackData: fmt.Sprintf("paid:%d", id)}, {Text: "📱 Request New Payment Details", CallbackData: fmt.Sprintf("req_alt_%d_%d", q.From.ID, id)}}}}
	a.sendText(ctx, b, a.Config.AdminID, adminText, markup)
	a.sendPhotoOrText(ctx, b, q.From.ID, a.Config.PhotoNumberSubmitted, fmt.Sprintf("📦 Sell Order ID: <code>#%d</code>\n💳 Payout: <b>%s</b> (<code>%s</code>)\n\n✅ Successfully submitted.\n⏳ Waiting for admin's Payment...", id, htmlEscape(updatedPayout), htmlEscape(orderString(row, "payout_number", "N/A"))), MainMenu())
	payoutEntrySessions.Delete(q.From.ID)
	_ = deleteWorkflowSession(ctx, a.DB, "payout_entry", q.From.ID)
	return true
}

// ---- merged from admin_tools_handlers.go ----

var adminToolSessions sync.Map // user id -> "wallet_adjust" | "add_admin"

type sellerStat struct {
	UserID int64
	Name   string
	Amount decimal.Decimal
}

func (a *BotApp) Leaderboard(ctx context.Context, b *bot.Bot, userID int64) {
	cur, err := a.DB.Orders.Find(ctx, bson.M{"status": "paid", "type": bson.M{"$ne": "admin_add"}})
	if err != nil {
		a.sendText(ctx, b, userID, "❌ Leaderboard লোড করা যায়নি।", MainMenu())
		return
	}
	stats := map[int64]decimal.Decimal{}
	for cur.Next(ctx) {
		var row bson.M
		if cur.Decode(&row) == nil {
			uid := orderInt64(row, "user_id")
			amt, _ := decimal.NewFromString(orderString(row, "amount", "0"))
			if uid > 0 {
				stats[uid] = stats[uid].Add(amt)
			}
		}
	}
	_ = cur.Close(ctx)
	sellers := make([]sellerStat, 0)
	isAdmin, _ := IsAdmin(ctx, a.DB, a.Config, userID)
	for uid, amt := range stats {
		if amt.LessThan(decimal.NewFromInt(25)) {
			continue
		}
		var u bson.M
		_ = a.DB.Users.FindOne(ctx, bson.M{"user_id": uid}).Decode(&u)
		name := orderString(u, "full_name", fmt.Sprintf("User %d", uid))
		sellers = append(sellers, sellerStat{uid, name, amt})
	}
	sort.Slice(sellers, func(i, j int) bool { return sellers[i].Amount.GreaterThan(sellers[j].Amount) })
	if len(sellers) > 10 {
		sellers = sellers[:10]
	}
	var out strings.Builder
	out.WriteString("🏆 <b>Top Sellers — Minimum 25 USDT</b>\n\n")
	if len(sellers) == 0 {
		out.WriteString("<i>এখনো কোনো সেল লিডারবোর্ড ডাটা নেই।</i>")
	} else {
		for i, s := range sellers {
			if isAdmin {
				fmt.Fprintf(&out, "%d. ID: <code>%d</code> | <b>%s</b> — <b>%s$</b>\n", i+1, s.UserID, htmlEscape(s.Name), s.Amount.String())
			} else {
				fmt.Fprintf(&out, "%d. <b>%s</b> — <b>%s$</b>\n", i+1, htmlEscape(s.Name), s.Amount.String())
			}
		}
	}
	out.WriteString("\n\n🏆 <b>Top Referrers</b>\n\n")
	refCur, e := a.DB.Users.Find(ctx, bson.M{"valid_referrals": bson.M{"$gt": 0}}, options.Find().SetSort(bson.D{{Key: "valid_referrals", Value: -1}}).SetLimit(10))
	if e != nil {
		out.WriteString("<i>রেফারেল ডাটা পাওয়া যায়নি।</i>")
	} else {
		defer refCur.Close(ctx)
		idx := 0
		for refCur.Next(ctx) {
			var u bson.M
			if refCur.Decode(&u) != nil {
				continue
			}
			idx++
			uid := orderInt64(u, "user_id")
			name := orderString(u, "full_name", fmt.Sprintf("User %d", uid))
			refs := orderInt64(u, "valid_referrals")
			if isAdmin {
				fmt.Fprintf(&out, "%d. ID: <code>%d</code> | <b>%s</b> — <b>%d</b> Referrals\n", idx, uid, htmlEscape(name), refs)
			} else {
				fmt.Fprintf(&out, "%d. <b>%s</b> — <b>%d</b> Referrals\n", idx, htmlEscape(name), refs)
			}
		}
		if idx == 0 {
			out.WriteString("<i>এখনো কোনো রেফার লিডারবোর্ড ডাটা নেই।</i>")
		}
	}
	a.sendText(ctx, b, userID, out.String(), MainMenu())
}

func (a *BotApp) AdminReviews(ctx context.Context, b *bot.Bot, chatID int64) {
	isOwner := chatID == a.Config.AdminID
	if !isOwner {
		a.sendText(ctx, b, chatID, "❌ শুধুমাত্র পার্মানেন্ট অ্যাডমিনের জন্য।", nil)
		return
	}
	cur, err := a.DB.Reviews.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: -1}}).SetLimit(15))
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Reviews লোড করা যায়নি।", nil)
		return
	}
	defer cur.Close(ctx)
	var out strings.Builder
	out.WriteString("⭐ <b>User Feedback & Rating Summary</b>\n\n")
	n := 0
	for cur.Next(ctx) {
		var r bson.M
		if cur.Decode(&r) != nil {
			continue
		}
		n++
		rating := int(orderInt64(r, "rating"))
		if rating < 1 || rating > 5 {
			rating = 5
		}
		oid := orderInt64(r, "order_id")
		uid := orderInt64(r, "user_id")
		feedback := orderString(r, "text", "No comment")
		ts := orderString(r, "timestamp", "N/A")
		fmt.Fprintf(&out, "📦 Order <code>#%d</code> | User: <code>%d</code>\nRating: %s (<b>%d/5</b>)\nFeedback: <b>%s</b>\nTime: <i>%s</i>\n──────────────────────────\n", oid, uid, strings.Repeat("⭐", rating), rating, htmlEscape(feedback), htmlEscape(ts))
	}
	if n == 0 {
		out.WriteString("⭐ কোনো ইউজার রেটিং বা রিভিউ এখনো পাওয়া যায়নি।")
	}
	a.sendText(ctx, b, chatID, out.String(), PermanentAdminOthersMenu())
}

func (a *BotApp) AdminUsers(ctx context.Context, b *bot.Bot, chatID int64) {
	isAdmin, err := IsAdmin(ctx, a.DB, a.Config, chatID)
	if err != nil || !isAdmin {
		a.sendText(ctx, b, chatID, "❌ এই কমান্ডটি শুধুমাত্র অ্যাডমিনদের জন্য!", nil)
		return
	}
	total, _ := a.DB.Users.CountDocuments(ctx, bson.M{})
	banned, _ := a.DB.Users.CountDocuments(ctx, bson.M{"is_banned": true})
	cur, e := a.DB.Users.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: -1}}).SetLimit(50))
	if e != nil {
		a.sendText(ctx, b, chatID, "❌ User list লোড করা যায়নি।", nil)
		return
	}
	defer cur.Close(ctx)
	var out strings.Builder
	fmt.Fprintf(&out, "👥 <b>User Statistics & Recent Registrations</b>\n\n• <b>Total Users:</b> %d\n• <b>Active Users:</b> %d\n• <b>Banned Users:</b> %d\n\n📋 <b>Recent Users List (Last 50):</b>\n", total, total-banned, banned)
	idx := 0
	for cur.Next(ctx) {
		var u bson.M
		if cur.Decode(&u) != nil {
			continue
		}
		idx++
		uid := orderInt64(u, "user_id")
		status := "🟢 [Active]"
		if v, _ := u["is_banned"].(bool); v {
			status = "🔴 [Banned]"
		}
		name := orderString(u, "full_name", "N/A")
		fmt.Fprintf(&out, "%d. %s (ID: <code>%d</code>) — <b>%s</b>\n", idx, status, uid, htmlEscape(name))
	}
	txt := out.String()
	if len(txt) > 3900 {
		txt = txt[:3850] + "\n...[List truncated]"
	}
	a.sendText(ctx, b, chatID, txt, AdminMenuWithCounts(chatID, a.Config.AdminID, 0, 0, 0, 0))
}

func (a *BotApp) adminToolsMessage(ctx context.Context, b *bot.Bot, u *models.User, chatID int64, text string) bool {
	isAdmin, err := IsAdmin(ctx, a.DB, a.Config, u.ID)
	if err != nil || !isAdmin {
		return false
	}
	raw, toolOK := adminToolSessions.Load(u.ID)
	if !toolOK {
		var restored string
		if found, loadErr := loadWorkflowSession(ctx, a.DB, "admin_tool", u.ID, &restored); loadErr == nil && found {
			raw, toolOK = restored, true
			adminToolSessions.Store(u.ID, restored)
		}
	}
	if toolOK {
		step := raw.(string)
		switch step {
		case "wallet_adjust":
			parts := strings.Fields(text)
			if len(parts) != 2 {
				a.sendText(ctx, b, chatID, "❌ ফরম্যাট: <code>123456789 500</code>\nপ্রথমে User ID, পরে BDT amount দিন।", nil)
				return true
			}
			uid, e1 := strconv.ParseInt(parts[0], 10, 64)
			amount, e2 := decimal.NewFromString(parts[1])
			if e1 != nil || e2 != nil || uid <= 0 || !amount.IsPositive() {
				a.sendText(ctx, b, chatID, "❌ সঠিক User ID এবং positive amount দিন।", nil)
				return true
			}
			var target bson.M
			if e := a.DB.Users.FindOne(ctx, bson.M{"user_id": uid}).Decode(&target); e != nil {
				a.sendText(ctx, b, chatID, "❌ এই User ID ডাটাবেজে পাওয়া যায়নি।", nil)
				adminToolSessions.Delete(u.ID)
				_ = deleteWorkflowSession(ctx, a.DB, "admin_tool", u.ID)
				return true
			}
			orderID, e := GenerateUniqueOrderID(ctx, a.DB)
			if e != nil {
				a.sendText(ctx, b, chatID, "❌ Wallet adjustment ID তৈরি করা যায়নি।", nil)
				return true
			}
			applied, txErr := ApplyWalletDeltaAnd(ctx, a.DB, uid, amount,
				fmt.Sprintf("admin_wallet_add:%d", orderID), "admin_wallet_credit", strconv.FormatInt(orderID, 10),
				func(tx context.Context) error {
					_, insertErr := a.DB.Orders.InsertOne(tx, bson.M{"id": orderID, "user_id": uid, "type": "admin_add", "amount": "0", "total_bdt": amount.String(), "status": "paid", "created_at": BangladeshTime()})
					return insertErr
				})
			if txErr != nil || !applied {
				a.sendText(ctx, b, chatID, "❌ Wallet adjustment নিরাপদে সম্পন্ন হয়নি; balance পরিবর্তন করা হয়নি।", nil)
				return true
			}
			var updatedUser bson.M
			_ = a.DB.Users.FindOne(ctx, bson.M{"user_id": uid}).Decode(&updatedUser)
			newBal, _ := decimal.NewFromString(orderString(updatedUser, "mini_wallet", "0"))
			_ = LogAdminAction(ctx, a.DB, u.ID, u.Username, "N/A", "N/A", "Admin Add Wallet", "0", amount.String())
			adminToolSessions.Delete(u.ID)
			_ = deleteWorkflowSession(ctx, a.DB, "admin_tool", u.ID)
			a.sendText(ctx, b, uid, fmt.Sprintf("💳 <b>Wallet Deposit Notification</b>\n\nঅ্যাডমিন কর্তৃক আপনার ওয়ালেটে ৳<b>%s</b> যোগ করা হয়েছে!\nবর্তমান ওয়ালেট ব্যালেন্স: ৳<b>%s</b>", Money(amount), Money(newBal)), MainMenu())
			a.sendText(ctx, b, chatID, fmt.Sprintf("✅ User ID <code>%d</code> এর ওয়ালেটে ৳<b>%s</b> যোগ হয়েছে।\nনতুন ব্যালেন্স: ৳<b>%s</b>", uid, Money(amount), Money(newBal)), PermanentAdminOthersMenu())
			return true
		case "add_admin":
			uid, e := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
			if e != nil || uid <= 0 || uid == a.Config.AdminID {
				a.sendText(ctx, b, chatID, "❌ সঠিক Telegram User ID দিন।", nil)
				return true
			}
			_, e = a.DB.Admins.UpdateOne(ctx, bson.M{"admin_id": uid}, bson.M{"$set": bson.M{"status": "active", "role": "sub_admin"}, "$setOnInsert": bson.M{"added_at": BangladeshTime(), "added_by": u.ID}}, options.Update().SetUpsert(true))
			if e != nil {
				a.sendText(ctx, b, chatID, "❌ Admin যুক্ত করা যায়নি।", nil)
				return true
			}
			adminToolSessions.Delete(u.ID)
			_ = deleteWorkflowSession(ctx, a.DB, "admin_tool", u.ID)
			_ = LogAdminAction(ctx, a.DB, u.ID, u.Username, "N/A", "N/A", "Add Admin", "0", "0")
			a.sendText(ctx, b, chatID, fmt.Sprintf("✅ User ID <code>%d</code> কে Admin হিসেবে যুক্ত করা হয়েছে।", uid), PermanentAdminOthersMenu())
			return true
		}
	}
	switch text {
	case "🏆 Leaderboard":
		a.Leaderboard(ctx, b, chatID)
		return true
	case "⭐ Customer Reviews":
		a.AdminReviews(ctx, b, chatID)
		return true
	case "👥 Manage Administrators":
		if u.ID != a.Config.AdminID {
			a.sendText(ctx, b, chatID, "❌ শুধুমাত্র Permanent Admin এই কাজ করতে পারবেন।", nil)
			return true
		}
		a.showAdminManagement(ctx, b, chatID)
		return true
	case "➕ Adjust User Wallet":
		if u.ID != a.Config.AdminID {
			a.sendText(ctx, b, chatID, "❌ শুধুমাত্র Permanent Admin এই কাজ করতে পারবেন।", nil)
			return true
		}
		adminToolSessions.Store(u.ID, "wallet_adjust")
		_ = saveWorkflowSession(ctx, a.DB, "admin_tool", u.ID, "wallet_adjust", 24*time.Hour)
		a.sendText(ctx, b, chatID, "💳 <b>Adjust User Wallet</b>\nUser ID এবং BDT amount পাঠান। উদাহরণ: <code>123456789 500</code>", nil)
		return true
	case "👥 Users":
		a.AdminUsers(ctx, b, chatID)
		return true
	}
	return false
}

func (a *BotApp) showAdminManagement(ctx context.Context, b *bot.Bot, chatID int64) {
	cur, err := a.DB.Admins.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "admin_id", Value: 1}}))
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Admin list লোড করা যায়নি।", nil)
		return
	}
	defer cur.Close(ctx)

	var out strings.Builder
	fmt.Fprintf(&out, "👥 <b>Admin Management Dashboard</b>\n\n🔐 <b>Permanent Admin (Owner)</b>\n• ID: <code>%d</code>\n• Status: 🟢 Permanent / Full Access\n\n👤 <b>Sub-Administrators</b>\n", a.Config.AdminID)

	rows := [][]models.InlineKeyboardButton{}
	count := 0
	for cur.Next(ctx) {
		var ad bson.M
		if cur.Decode(&ad) != nil {
			continue
		}
		id := orderInt64(ad, "admin_id")
		if id <= 0 || id == a.Config.AdminID {
			continue
		}
		count++
		status := orderString(ad, "status", "active")
		role := orderString(ad, "role", "sub_admin")
		if status == "active" {
			fmt.Fprintf(&out, "• ID: <code>%d</code> — 🟢 Active (%s)\n", id, htmlEscape(role))
			rows = append(rows, []models.InlineKeyboardButton{
				{Text: fmt.Sprintf("👤 %d", id), CallbackData: "adm_none"},
				{Text: "🔴 Suspend", CallbackData: fmt.Sprintf("adm_toggle:%d", id)},
			})
		} else {
			fmt.Fprintf(&out, "• ID: <code>%d</code> — 🔴 Suspended (%s)\n", id, htmlEscape(role))
			rows = append(rows, []models.InlineKeyboardButton{
				{Text: fmt.Sprintf("👤 %d", id), CallbackData: "adm_none"},
				{Text: "🟢 Activate", CallbackData: fmt.Sprintf("adm_toggle:%d", id)},
			})
		}
	}
	if count == 0 {
		out.WriteString("<i>কোনো Sub-Admin যুক্ত করা হয়নি।</i>\n")
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: "➕ Add Sub-Admin", CallbackData: "adm_add"}})
	rows = append(rows, []models.InlineKeyboardButton{{Text: "🔙 Back to Dashboard", CallbackData: "admin_back"}})
	a.sendText(ctx, b, chatID, out.String(), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (a *BotApp) AdminToolsCallback(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	q := update.CallbackQuery
	if q == nil {
		return false
	}
	data := q.Data
	uid := q.From.ID
	if data != "adm_add" && data != "add_admin_prompt" && !strings.HasPrefix(data, "adm_toggle:") && !strings.HasPrefix(data, "toggle_admin:") && data != "adm_none" && data != "admin_back" {
		return false
	}
	if uid != a.Config.AdminID {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "শুধুমাত্র Permanent Admin", ShowAlert: true})
		return true
	}
	if data == "admin_back" {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
		po, pp, pw, ps, _ := a.AdminCounts(ctx)
		a.sendText(ctx, b, uid, "🔐 <b>Administrator Dashboard</b>", AdminMenuWithCounts(uid, a.Config.AdminID, po, pp, pw, ps))
		return true
	}
	if data == "adm_none" {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
		return true
	}
	if data == "adm_add" || data == "add_admin_prompt" {
		adminToolSessions.Store(uid, "add_admin")
		_ = saveWorkflowSession(ctx, a.DB, "admin_tool", uid, "add_admin", 24*time.Hour)
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
		a.sendText(ctx, b, uid, "➕ নতুন Admin-এর Telegram User ID পাঠান।", nil)
		return true
	}
	togglePrefix := "adm_toggle:"
	if strings.HasPrefix(data, "toggle_admin:") {
		togglePrefix = "toggle_admin:"
	}
	target, e := strconv.ParseInt(strings.TrimPrefix(data, togglePrefix), 10, 64)
	if e != nil || target == a.Config.AdminID {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Invalid admin", ShowAlert: true})
		return true
	}
	var row bson.M
	if e = a.DB.Admins.FindOne(ctx, bson.M{"admin_id": target}).Decode(&row); e != nil {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Admin পাওয়া যায়নি", ShowAlert: true})
		return true
	}
	status := orderString(row, "status", "active")
	next := "active"
	if status == "active" {
		next = "suspended"
	}
	_, e = a.DB.Admins.UpdateOne(ctx, bson.M{"admin_id": target}, bson.M{"$set": bson.M{"status": next}})
	if e == nil {
		_ = LogAdminAction(ctx, a.DB, uid, q.From.Username, "N/A", "N/A", "Toggle Admin "+strconv.FormatInt(target, 10), status, next)
	}
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Admin status updated"})
	a.showAdminManagement(ctx, b, uid)
	return true
}

func (a *BotApp) AdminBanCommand(ctx context.Context, b *bot.Bot, update *models.Update, ban bool) {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return
	}
	uid := update.Message.From.ID
	ok, _ := IsAdmin(ctx, a.DB, a.Config, uid)
	if !ok {
		a.sendText(ctx, b, update.Message.Chat.ID, "❌ এই কমান্ডটি শুধুমাত্র অ্যাডমিনদের জন্য!", nil)
		return
	}
	parts := strings.Fields(update.Message.Text)
	if len(parts) < 2 {
		if ban {
			n, _ := a.DB.Users.CountDocuments(ctx, bson.M{"is_banned": true})
			a.sendText(ctx, b, update.Message.Chat.ID, fmt.Sprintf("🚫 বর্তমানে মোট ব্যানড ইউজার: <b>%d</b> জন\nব্যবহার: <code>/ban USER_ID</code>", n), nil)
		} else {
			a.sendText(ctx, b, update.Message.Chat.ID, "ব্যবহার: <code>/unban USER_ID</code>", nil)
		}
		return
	}
	target, e := strconv.ParseInt(parts[1], 10, 64)
	if e != nil || target <= 0 || (ban && (target == a.Config.AdminID || target == uid)) {
		a.sendText(ctx, b, update.Message.Chat.ID, "❌ সঠিক User ID দিন।", nil)
		return
	}
	value := !ban
	_, e = a.DB.Users.UpdateOne(ctx, bson.M{"user_id": target}, bson.M{"$set": bson.M{"is_banned": value}}, options.Update().SetUpsert(true))
	if e != nil {
		a.sendText(ctx, b, update.Message.Chat.ID, "❌ পরিবর্তন করা যায়নি।", nil)
		return
	}
	action := "Unban User "
	label := "ব্যান তুলে নেওয়া হয়েছে"
	if ban {
		action = "Ban User "
		label = "ব্যান করা হয়েছে"
	}
	_ = LogAdminAction(ctx, a.DB, uid, update.Message.From.Username, "N/A", "N/A", action+strconv.FormatInt(target, 10), "0", "0")
	a.sendText(ctx, b, update.Message.Chat.ID, fmt.Sprintf("✅ User ID <code>%d</code> কে %s।", target, label), nil)
}

// Keep time imported in this module for audit/session timestamps.
var _ = time.Now
var _ = mongo.ErrNoDocuments

// ---- merged from advanced_admin_handlers.go ----

// Advanced admin input states are kept per admin, matching the original
// python context.user_data workflow. They are cleared after successful input.
var advancedAdminSessions sync.Map // int64 -> string

func (a *BotApp) setAdvancedAdminState(ctx context.Context, userID int64, state string) {
	advancedAdminSessions.Store(userID, state)
	_, _ = a.DB.Sessions.UpdateOne(ctx, bson.M{"session_key": fmt.Sprintf("admin:%d", userID)}, bson.M{"$set": bson.M{"session_key": fmt.Sprintf("admin:%d", userID), "kind": "admin", "state": state, "updated_at": time.Now(), "expires_at": time.Now().Add(24 * time.Hour)}}, options.Update().SetUpsert(true))
}

func (a *BotApp) clearAdvancedAdminState(ctx context.Context, userID int64) {
	advancedAdminSessions.Delete(userID)
	_, _ = a.DB.Sessions.DeleteOne(ctx, bson.M{"session_key": fmt.Sprintf("admin:%d", userID)})
}

func (a *BotApp) beginAdvancedAdminInput(ctx context.Context, b *bot.Bot, chatID int64, userID int64, state, prompt string) {
	if userID != a.Config.AdminID {
		a.sendText(ctx, b, chatID, "❌ এই কাজটি শুধুমাত্র Permanent Admin করতে পারবেন।", nil)
		return
	}
	a.setAdvancedAdminState(ctx, userID, state)
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✖️ Cancel", CallbackData: "adv_cancel"}}}}
	a.sendText(ctx, b, chatID, prompt, kb)
}

func (a *BotApp) advancedAdminMessage(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return false
	}
	uid, chatID := update.Message.From.ID, update.Message.Chat.ID
	if uid != a.Config.AdminID {
		return false
	}
	raw, ok := advancedAdminSessions.Load(uid)
	if !ok {
		var saved struct {
			State string `bson:"state"`
		}
		if err := a.DB.Sessions.FindOne(ctx, bson.M{"session_key": fmt.Sprintf("admin:%d", uid), "expires_at": bson.M{"$gt": time.Now()}}).Decode(&saved); err != nil {
			return false
		}
		raw = saved.State
		advancedAdminSessions.Store(uid, saved.State)
	}
	state := raw.(string)
	input := strings.TrimSpace(update.Message.Text)
	if input == "" {
		input = strings.TrimSpace(update.Message.Caption)
	}
	if input == "" {
		a.sendText(ctx, b, chatID, "❌ অনুগ্রহ করে Text Message পাঠান।", nil)
		return true
	}

	switch state {
	case "rate_text":
		if len(input) > 3500 {
			a.sendText(ctx, b, chatID, "❌ Rate text ৩৫০০ অক্ষরের মধ্যে রাখুন।", nil)
			return true
		}
		_, err := a.DB.Settings.UpdateOne(ctx, bson.M{"setting_key": "raw_rate_text"}, bson.M{"$set": bson.M{"setting_value": input}}, options.Update().SetUpsert(true))
		if err != nil {
			a.sendText(ctx, b, chatID, "❌ Rate update করা যায়নি।", nil)
			return true
		}
		a.clearAdvancedAdminState(ctx, uid)
		_ = LogAdminAction(ctx, a.DB, uid, update.Message.From.Username, "N/A", "N/A", "Change Rates", "0", "custom text")
		a.sendText(ctx, b, chatID, "✅ লাইভ রেট লিস্ট আপডেট হয়েছে।\n\n"+htmlEscape(input), PermanentAdminOthersMenu())
		return true

	case "ref_percent":
		parts := strings.Fields(input)
		if len(parts) != 2 {
			a.sendText(ctx, b, chatID, "❌ ফরম্যাট: <code>5 0.5</code>\nপ্রথম Sale Bonus %, দ্বিতীয় Lifetime Commission %।", nil)
			return true
		}
		firstPct, e1 := decimal.NewFromString(parts[0])
		lifePct, e2 := decimal.NewFromString(parts[1])
		if e1 != nil || e2 != nil || firstPct.IsNegative() || lifePct.IsNegative() || firstPct.GreaterThan(decimal.NewFromInt(100)) || lifePct.GreaterThan(decimal.NewFromInt(100)) {
			a.sendText(ctx, b, chatID, "❌ উভয় Percentage ০ থেকে ১০০-এর মধ্যে দিন। যেমন: <code>5 0.5</code>", nil)
			return true
		}
		value, _ := json.Marshal(map[string]string{"first_sale": firstPct.Div(decimal.NewFromInt(100)).String(), "lifetime": lifePct.Div(decimal.NewFromInt(100)).String()})
		_, err := a.DB.Settings.UpdateOne(ctx, bson.M{"setting_key": "ref_percentages"}, bson.M{"$set": bson.M{"setting_value": string(value)}}, options.Update().SetUpsert(true))
		if err != nil {
			a.sendText(ctx, b, chatID, "❌ Referral settings update করা যায়নি।", nil)
			return true
		}
		a.clearAdvancedAdminState(ctx, uid)
		_ = LogAdminAction(ctx, a.DB, uid, update.Message.From.Username, "N/A", "N/A", "Change Refer Percent", parts[0], parts[1])
		a.sendText(ctx, b, chatID, fmt.Sprintf("✅ Referral commission আপডেট হয়েছে।\n• First Sale Bonus: <b>%s%%</b>\n• Lifetime Commission: <b>%s%%</b>", firstPct, lifePct), PermanentAdminOthersMenu())
		return true

	case "user_search":
		var user bson.M
		var findErr error
		target, parseErr := strconv.ParseInt(strings.TrimPrefix(input, "@"), 10, 64)
		if parseErr == nil && target > 0 {
			findErr = a.DB.Users.FindOne(ctx, bson.M{"user_id": target}).Decode(&user)
		} else {
			username := strings.TrimPrefix(strings.TrimSpace(input), "@")
			if username == "" || strings.ContainsAny(username, " \t\n") {
				a.sendText(ctx, b, chatID, "❌ Telegram User ID অথবা @username দিন।", nil)
				return true
			}
			findErr = a.DB.Users.FindOne(ctx, bson.M{"username": bson.M{"$regex": "^" + regexp.QuoteMeta(username) + "$", "$options": "i"}}).Decode(&user)
			target = orderInt64(user, "user_id")
		}
		if findErr != nil || target <= 0 {
			a.clearAdvancedAdminState(ctx, uid)
			a.sendText(ctx, b, chatID, "❌ এই User পাওয়া যায়নি। User ID অথবা username যাচাই করুন।", nil)
			return true
		}
		a.clearAdvancedAdminState(ctx, uid)
		banStatus := "Active"
		if banned, _ := user["is_banned"].(bool); banned {
			banStatus = "Banned"
		}
		name := orderString(user, "full_name", "N/A")
		username := orderString(user, "username", "N/A")
		wallet := orderString(user, "mini_wallet", "0")
		bonus := orderString(user, "referral_bonus_bdt", "0")
		a.sendText(ctx, b, chatID, fmt.Sprintf("👤 <b>User Details</b>\n\nID: <code>%d</code>\nName: <b>%s</b>\nUsername: @%s\nStatus: <b>%s</b>\nWallet: ৳<b>%s</b>\nReferral Bonus: ৳<b>%s</b>\nValid Referrals: <b>%d</b>", target, htmlEscape(name), htmlEscape(username), banStatus, htmlEscape(wallet), htmlEscape(bonus), orderInt64(user, "valid_referrals")), PermanentAdminOthersMenu())
		return true

	case "minmax":
		parts := strings.Fields(input)
		if len(parts) != 2 {
			a.sendText(ctx, b, chatID, "❌ ফরম্যাট: <code>0.2 25</code> (Minimum Maximum)", nil)
			return true
		}
		min, e1 := decimal.NewFromString(parts[0])
		max, e2 := decimal.NewFromString(parts[1])
		if e1 != nil || e2 != nil || !min.IsPositive() || max.LessThan(min) {
			a.sendText(ctx, b, chatID, "❌ সঠিক Minimum ও Maximum দিন।", nil)
			return true
		}
		value, _ := json.Marshal(map[string]string{"min": min.String(), "max": max.String()})
		_, err := a.DB.Settings.UpdateOne(ctx, bson.M{"setting_key": "min_max_limits"}, bson.M{"$set": bson.M{"setting_value": string(value)}}, options.Update().SetUpsert(true))
		if err != nil {
			a.sendText(ctx, b, chatID, "❌ Limit update ব্যর্থ হয়েছে।", nil)
			return true
		}
		a.clearAdvancedAdminState(ctx, uid)
		_ = LogAdminAction(ctx, a.DB, uid, update.Message.From.Username, "N/A", "N/A", "Change Min/Max Limits", min.String(), max.String())
		a.sendText(ctx, b, chatID, fmt.Sprintf("✅ Sell Amount Limit আপডেট হয়েছে।\nMin: <b>%s USDT</b>\nMax: <b>%s USDT</b>", min, max), PermanentAdminOthersMenu())
		return true

	case "broadcast":
		mediaType, mediaID := "", ""
		if len(update.Message.Photo) > 0 {
			mediaType = "photo"
			mediaID = update.Message.Photo[len(update.Message.Photo)-1].FileID
		}
		if update.Message.Video != nil {
			mediaType = "video"
			mediaID = update.Message.Video.FileID
		}
		if update.Message.Document != nil {
			mediaType = "document"
			mediaID = update.Message.Document.FileID
		}
		if input == "" && mediaID == "" {
			a.sendText(ctx, b, chatID, "❌ Text, Photo, Video অথবা Document পাঠান।", nil)
			return true
		}
		if len(input) > 4000 {
			a.sendText(ctx, b, chatID, "❌ Caption/Text ৪০০০ অক্ষরের মধ্যে রাখুন।", nil)
			return true
		}
		a.clearAdvancedAdminState(ctx, uid)
		a.sendText(ctx, b, chatID, "🚀 Broadcast শুরু হয়েছে...", nil)
		cur, err := a.DB.Users.Find(ctx, bson.M{}, options.Find().SetProjection(bson.M{"user_id": 1}))
		if err != nil {
			a.sendText(ctx, b, chatID, "❌ User list পাওয়া যায়নি।", nil)
			return true
		}
		defer cur.Close(ctx)
		success, failed := 0, 0
		for cur.Next(ctx) {
			var row bson.M
			if cur.Decode(&row) != nil {
				failed++
				continue
			}
			target := orderInt64(row, "user_id")
			if target <= 0 {
				failed++
				continue
			}
			var sent *models.Message
			var sendErr error
			switch mediaType {
			case "photo":
				sent, sendErr = b.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: target, Photo: &models.InputFileString{Data: mediaID}, Caption: input, ParseMode: models.ParseModeHTML})
			case "video":
				sent, sendErr = b.SendVideo(ctx, &bot.SendVideoParams{ChatID: target, Video: &models.InputFileString{Data: mediaID}, Caption: input, ParseMode: models.ParseModeHTML})
			case "document":
				sent, sendErr = b.SendDocument(ctx, &bot.SendDocumentParams{ChatID: target, Document: &models.InputFileString{Data: mediaID}, Caption: input, ParseMode: models.ParseModeHTML})
			default:
				sent, sendErr = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: target, Text: input, ParseMode: models.ParseModeHTML})
			}
			if sendErr != nil {
				failed++
			} else {
				success++
				if sent != nil {
					_, _ = b.PinChatMessage(ctx, &bot.PinChatMessageParams{ChatID: target, MessageID: sent.ID, DisableNotification: true})
				}
			}
			if (success+failed)%20 == 0 {
				time.Sleep(1 * time.Second)
			}
		}
		_ = LogAdminAction(ctx, a.DB, uid, update.Message.From.Username, "N/A", "N/A", "Broadcast Message", strconv.Itoa(success), strconv.Itoa(failed))
		a.sendText(ctx, b, chatID, fmt.Sprintf("✅ <b>Broadcast & Auto-Pin Completed</b>\n\n• Successfully sent & pinned: <b>%d</b>\n• Failed/Blocked: <b>%d</b>", success, failed), PermanentAdminOthersMenu())
		return true
	}
	return false
}

func (a *BotApp) AdvancedAdminMessage(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return false
	}
	uid, chatID := update.Message.From.ID, update.Message.Chat.ID
	if uid != a.Config.AdminID {
		return false
	}
	if a.advancedAdminMessage(ctx, b, update) {
		return true
	}
	switch strings.TrimSpace(update.Message.Text) {
	case "💱 Update Exchange Rates":
		a.beginAdvancedAdminInput(ctx, b, chatID, uid, "rate_text", "💱 <b>Rate Change</b>\nইউজারদের দেখানোর জন্য নতুন Rate List পাঠান। উদাহরণ:\n<code>$0.50 - $0.99 ➔ ($1 = ৳115)\n$1.00 - $2.49 ➔ ($1 = ৳120)</code>")
		return true
	case "👥 Referral Commission":
		var row bson.M
		_ = a.DB.Settings.FindOne(ctx, bson.M{"setting_key": "ref_percentages"}).Decode(&row)
		current := orderString(row, "setting_value", `{"first_sale":"0.05","lifetime":"0.005"}`)
		var rates map[string]string
		_ = json.Unmarshal([]byte(current), &rates)
		first, _ := decimal.NewFromString(rates["first_sale"])
		life, _ := decimal.NewFromString(rates["lifetime"])
		a.beginAdvancedAdminInput(ctx, b, chatID, uid, "ref_percent", fmt.Sprintf("👥 <b>Referral Commission Settings</b>\nবর্তমান First Sale: <b>%s%%</b>\nবর্তমান Lifetime: <b>%s%%</b>\n\nনতুন দুইটি Percentage স্পেস দিয়ে পাঠান। উদাহরণ: <code>5 0.5</code>", first.Mul(decimal.NewFromInt(100)), life.Mul(decimal.NewFromInt(100))))
		return true
	case "📢 Broadcast Message":
		a.beginAdvancedAdminInput(ctx, b, chatID, uid, "broadcast", "📢 <b>Broadcast Mode</b>\nসকল User-কে পাঠানোর জন্য Text, Photo, Video অথবা Document পাঠান। Caption-ও সমর্থিত। পাঠানো কনটেন্ট স্বয়ংক্রিয়ভাবে Pin করার চেষ্টা করা হবে।")
		return true
	case "⚙️ Set Transaction Limits":
		min, max, _ := GetMinMaxLimits(ctx, a.DB)
		daily, _ := GetDailyLimit(ctx, a.DB)
		a.beginAdvancedAdminInput(ctx, b, chatID, uid, "minmax", fmt.Sprintf("⚙️ <b>Sell Amount Limits</b>\nবর্তমান Min: <b>%s USDT</b> | Max: <b>%s USDT</b>\nDaily limit: <b>%s USDT</b> (/dailylimit দিয়ে পরিবর্তন করুন)\n\nনতুন Minimum এবং Maximum স্পেস দিয়ে পাঠান। উদাহরণ: <code>0.2 25</code>", min, max, daily))
		return true
	case "🛠️ Manage Payment Methods":
		a.showPaymentMethodSettings(ctx, b, chatID)
		return true
	case "🔍 Search User":
		a.beginAdvancedAdminInput(ctx, b, chatID, uid, "user_search", "🔍 Telegram User ID অথবা @username পাঠান।")
		return true
	case "⚙️ More Settings":
		a.sendText(ctx, b, chatID, "⚙️ <b>Advanced Admin Settings</b>", PermanentAdminOthersMenu())
		return true
	case "🔙 Admin Dashboard":
		po, pp, pw, ps, _ := a.AdminCounts(ctx)
		a.sendText(ctx, b, chatID, "🔐 <b>Administrator Dashboard</b>", AdminMenuWithCounts(uid, a.Config.AdminID, po, pp, pw, ps))
		return true
	}
	return false
}

func (a *BotApp) showPaymentMethodSettings(ctx context.Context, b *bot.Bot, chatID int64) {
	methods := []string{"bKash", "Nagad", "Rocket", "Upay", "Recharge", "Bangla QR"}
	active := map[string]bool{}
	var row bson.M
	_ = a.DB.Settings.FindOne(ctx, bson.M{"setting_key": "active_payout_methods"}).Decode(&row)
	var names []string
	_ = json.Unmarshal([]byte(orderString(row, "setting_value", "[]")), &names)
	for _, name := range names {
		active[name] = true
	}
	buttons := make([][]models.InlineKeyboardButton, 0, len(methods)+1)
	for _, name := range methods {
		mark := "❌"
		if active[name] {
			mark = "✅"
		}
		buttons = append(buttons, []models.InlineKeyboardButton{{Text: mark + " " + name, CallbackData: "adv_method:" + name}})
	}
	buttons = append(buttons, []models.InlineKeyboardButton{{Text: "🔙 Back", CallbackData: "adv_back"}})
	a.sendText(ctx, b, chatID, "🛠️ <b>Payment Method Settings</b>\nToggle করতে একটি Method চাপুন।", &models.InlineKeyboardMarkup{InlineKeyboard: buttons})
}

func (a *BotApp) AdvancedAdminCallback(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	if update == nil || update.CallbackQuery == nil {
		return false
	}
	q := update.CallbackQuery
	data, uid := q.Data, q.From.ID
	if data != "adv_cancel" && data != "adv_back" && data != "admin_back" && !strings.HasPrefix(data, "adv_method:") && !strings.HasPrefix(data, "toggle_method:") && !strings.HasPrefix(data, "accept_limit:") && !strings.HasPrefix(data, "reject_limit:") {
		return false
	}
	if uid != a.Config.AdminID {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "শুধুমাত্র Permanent Admin", ShowAlert: true})
		return true
	}
	switch {
	case strings.HasPrefix(data, "accept_limit:"):
		parts := strings.Split(data, ":")
		if len(parts) != 3 {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Invalid request", ShowAlert: true})
			return true
		}
		target, e1 := strconv.ParseInt(parts[1], 10, 64)
		amount, e2 := decimal.NewFromString(parts[2])
		if e1 != nil || e2 != nil || target <= 0 || !amount.IsPositive() {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Invalid request", ShowAlert: true})
			return true
		}
		day := time.Now().In(dhakaLocation).Format("2006-01-02")
		field := "allowed_extra_limit_" + day
		_, err := a.DB.Users.UpdateOne(ctx, bson.M{"user_id": target}, bson.M{"$set": bson.M{field: amount.String()}}, options.Update().SetUpsert(true))
		if err != nil {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Database update failed", ShowAlert: true})
			return true
		}
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Extra limit accepted", ShowAlert: true})
		a.sendText(ctx, b, target, fmt.Sprintf("✅ অ্যাডমিন আপনার Daily Limit request গ্রহণ করেছেন। আপনি আজ অতিরিক্ত <b>%s USDT</b> পর্যন্ত Sell করতে পারবেন।", amount), MainMenu())
		if q.Message.Message != nil {
			_, _ = b.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: q.Message.Message.Chat.ID, MessageID: q.Message.Message.ID, Text: q.Message.Message.Text + "\n\n[Accepted: " + amount.String() + " USDT ✅]"})
		}
		_ = LogAdminAction(ctx, a.DB, uid, q.From.Username, "N/A", "N/A", "Accept Daily Limit "+strconv.FormatInt(target, 10), "0", amount.String())
	case strings.HasPrefix(data, "reject_limit:"):
		target, err := strconv.ParseInt(strings.TrimPrefix(data, "reject_limit:"), 10, 64)
		if err != nil || target <= 0 {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Invalid request", ShowAlert: true})
			return true
		}
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Request rejected", ShowAlert: true})
		a.sendText(ctx, b, target, "❌ দুঃখিত, আপনার দৈনিক লিমিট বাড়ানোর অনুরোধটি অ্যাডমিন বাতিল করেছেন।", MainMenu())
		if q.Message.Message != nil {
			_, _ = b.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: q.Message.Message.Chat.ID, MessageID: q.Message.Message.ID, Text: q.Message.Message.Text + "\n\n[Rejected ❌]"})
		}
		_ = LogAdminAction(ctx, a.DB, uid, q.From.Username, "N/A", "N/A", "Reject Daily Limit "+strconv.FormatInt(target, 10), "0", "0")
	case data == "adv_cancel":
		a.clearAdvancedAdminState(ctx, uid)
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Cancelled"})
		a.sendText(ctx, b, uid, "❎ কাজটি বাতিল করা হয়েছে।", PermanentAdminOthersMenu())
	case data == "adv_back" || data == "admin_back":
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
		a.sendText(ctx, b, uid, "Admin Settings", PermanentAdminOthersMenu())
	case strings.HasPrefix(data, "adv_method:") || strings.HasPrefix(data, "toggle_method:"):
		method := strings.TrimPrefix(data, "adv_method:")
		if strings.HasPrefix(data, "toggle_method:") {
			method = strings.TrimPrefix(data, "toggle_method:")
		}
		allowed := map[string]bool{"bKash": true, "Nagad": true, "Rocket": true, "Upay": true, "Recharge": true, "Bangla QR": true}
		if !allowed[method] {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Invalid method", ShowAlert: true})
			return true
		}
		var row bson.M
		_ = a.DB.Settings.FindOne(ctx, bson.M{"setting_key": "active_payout_methods"}).Decode(&row)
		var names []string
		_ = json.Unmarshal([]byte(orderString(row, "setting_value", "[]")), &names)
		found := false
		out := make([]string, 0, len(names)+1)
		for _, name := range names {
			if name == method {
				found = true
				continue
			}
			out = append(out, name)
		}
		status := "Disabled"
		if !found {
			out = append(out, method)
			status = "Enabled"
		}
		encoded, _ := json.Marshal(out)
		_, err := a.DB.Settings.UpdateOne(ctx, bson.M{"setting_key": "active_payout_methods"}, bson.M{"$set": bson.M{"setting_value": string(encoded)}}, options.Update().SetUpsert(true))
		if err != nil {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Update failed", ShowAlert: true})
			return true
		}
		_ = LogAdminAction(ctx, a.DB, uid, q.From.Username, "N/A", "N/A", "Toggle Payment Method "+method, "", status)
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: method + " " + status})
		a.showPaymentMethodSettings(ctx, b, uid)
	}
	return true
}

// ---- merged from admin_parity_handlers.go ----

var adminNumericLookup = regexp.MustCompile(`^#?\d{3,18}$`)

// adminParityMessage ports remaining admin menu/history/search workflows from
// the supplied Python handlers.py.
func (a *BotApp) adminParityMessage(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return false
	}
	uid, chatID := update.Message.From.ID, update.Message.Chat.ID
	isAdmin, err := IsAdmin(ctx, a.DB, a.Config, uid)
	if err != nil || !isAdmin {
		return false
	}
	text := strings.TrimSpace(update.Message.Text)
	if text == "" {
		text = strings.TrimSpace(update.Message.Caption)
	}

	// Persistent universal search input for both Permanent Admin and Sub-Admin.
	raw, sessionOK := adminToolSessions.Load(uid)
	if !sessionOK {
		var restoredSearch string
		if found, loadErr := loadWorkflowSession(ctx, a.DB, "admin_tool", uid, &restoredSearch); loadErr == nil && found {
			raw, sessionOK = restoredSearch, true
			adminToolSessions.Store(uid, restoredSearch)
		}
	}
	if sessionOK && raw == "admin_universal_search" {
		adminToolSessions.Delete(uid)
		_ = deleteWorkflowSession(ctx, a.DB, "admin_tool", uid)
		a.searchAdminRecords(ctx, b, chatID, text)
		return true
	}
	// The reward input is stored in the same persistent admin-tool session store.
	raw, sessionOK = adminToolSessions.Load(uid)
	if sessionOK && raw == "leaderboard_reward" {
		return a.handleLeaderboardReward(ctx, b, update, text)
	}
	if !sessionOK {
		var restored string
		if found, loadErr := loadWorkflowSession(ctx, a.DB, "admin_tool", uid, &restored); loadErr == nil && found && restored == "leaderboard_reward" {
			adminToolSessions.Store(uid, restored)
			return a.handleLeaderboardReward(ctx, b, update, text)
		}
	}

	switch text {
	case "🔄 Refresh Dashboard":
		po, pp, pw, ps, countErr := a.AdminCounts(ctx)
		if countErr != nil {
			a.sendText(ctx, b, chatID, "❌ Dashboard refresh করা যায়নি।", nil)
		} else {
			a.sendText(ctx, b, chatID, "✅ <b>Dashboard refreshed successfully.</b>", AdminMenuWithCounts(uid, a.Config.AdminID, po, pp, pw, ps))
		}
		return true
	case "📋 Admin Activity Log":
		a.showAdminActivityLogs(ctx, b, chatID)
		return true
	case "🏧 Withdrawal History":
		if uid != a.Config.AdminID {
			a.sendText(ctx, b, chatID, "❌ এই অপশনটি শুধুমাত্র পার্মানেন্ট অ্যাডমিনের জন্য।", nil)
			return true
		}
		a.showAllWithdrawalHistory(ctx, b, chatID)
		return true
	case "📚 Complete Transaction History":
		if uid == a.Config.AdminID {
			a.sendText(ctx, b, chatID, "ℹ️ পার্মানেন্ট অ্যাডমিন হিসেবে '📦 Sell Order History' অথবা '🏧 Withdrawal History' ব্যবহার করুন।", nil)
			return true
		}
		a.showCombinedHistory(ctx, b, chatID)
		return true
	case "📦 Sell Order History":
		if uid != a.Config.AdminID {
			a.sendText(ctx, b, chatID, "❌ এই অপশনটি শুধুমাত্র পার্মানেন্ট অ্যাডমিনের জন্য।", nil)
			return true
		}
		a.sendAdminOrderList(ctx, b, chatID, "history")
		return true
	case "🔎 Search Records":
		adminToolSessions.Store(uid, "admin_universal_search")
		_ = saveWorkflowSession(ctx, a.DB, "admin_tool", uid, "admin_universal_search", 24*time.Hour)
		a.sendText(ctx, b, chatID, "🔎 <b>Universal Admin Search</b>\nনিচের যেকোনো তথ্য পাঠান:\n• Sell Order ID\n• Withdrawal ID\n• Telegram User ID\n• Mobile Number\n• Telegram File ID\n• Transaction / TRX ID\n• Support Ticket ID", nil)
		return true
	case "🏆 Leaderboard Rewards":
		if uid != a.Config.AdminID {
			a.sendText(ctx, b, chatID, "❌ এই অপশনটি শুধুমাত্র পার্মানেন্ট অ্যাডমিনের জন্য!", nil)
			return true
		}
		a.beginLeaderboardReward(ctx, b, chatID, uid)
		return true
	}

	if strings.HasPrefix(text, "#TICK-") || adminNumericLookup.MatchString(text) || strings.HasPrefix(strings.ToLower(text), "trx:") || strings.HasPrefix(strings.ToLower(text), "file:") || strings.HasPrefix(strings.ToLower(text), "order:") || strings.HasPrefix(strings.ToLower(text), "withdraw:") || strings.HasPrefix(strings.ToLower(text), "user:") {
		a.searchAdminRecords(ctx, b, chatID, text)
		return true
	}
	return false
}

func (a *BotApp) beginLeaderboardReward(ctx context.Context, b *bot.Bot, chatID, adminID int64) {
	cur, err := a.DB.Orders.Find(ctx, bson.M{"status": "paid", "type": bson.M{"$ne": "admin_add"}})
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Leaderboard ডাটা লোড করা যায়নি।", PermanentAdminOthersMenu())
		return
	}
	defer cur.Close(ctx)
	totals := map[int64]decimal.Decimal{}
	for cur.Next(ctx) {
		var row bson.M
		if cur.Decode(&row) == nil {
			uid := orderInt64(row, "user_id")
			amount, parseErr := decimal.NewFromString(orderString(row, "amount", "0"))
			if uid > 0 && parseErr == nil {
				totals[uid] = totals[uid].Add(amount)
			}
		}
	}
	rows := make([]sellerStat, 0, len(totals))
	for uid, amount := range totals {
		if amount.LessThan(decimal.NewFromInt(25)) {
			continue
		}
		var user bson.M
		if a.DB.Users.FindOne(ctx, bson.M{"user_id": uid}).Decode(&user) == nil {
			rows = append(rows, sellerStat{UserID: uid, Name: orderString(user, "full_name", fmt.Sprintf("User %d", uid)), Amount: amount})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Amount.GreaterThan(rows[j].Amount) })
	if len(rows) > 10 {
		rows = rows[:10]
	}
	var out strings.Builder
	out.WriteString("🏆 <b>Top Sell Leaderboard (Reward Panel)</b>\n\n")
	if len(rows) == 0 {
		out.WriteString("<i>এখনো কোনো সেল লিডারবোর্ড ডাটা নেই।</i>\n\n")
	} else {
		for i, row := range rows {
			fmt.Fprintf(&out, "%d. ID: <code>%d</code> | <b>%s</b> — <b>%s$</b>\n", i+1, row.UserID, htmlEscape(row.Name), row.Amount.String())
		}
	}
	out.WriteString("───────────────────────────\n🔸 <b>রিওয়ার্ড পাঠানোর নিয়ম:</b>\nউপরের তালিকা থেকে User ID এবং টাকার পরিমাণ একসাথে স্পেস দিয়ে লিখুন।\nউদাহরণ: <code>1234567890 200৳</code>")
	adminToolSessions.Store(adminID, "leaderboard_reward")
	if err := saveWorkflowSession(ctx, a.DB, "admin_tool", adminID, "leaderboard_reward", 24*time.Hour); err != nil {
		a.sendText(ctx, b, chatID, "❌ Reward session সংরক্ষণ করা যায়নি।", PermanentAdminOthersMenu())
		return
	}
	a.sendText(ctx, b, chatID, out.String(), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✖️ Cancel", CallbackData: "cancel"}}}})
}

func (a *BotApp) handleLeaderboardReward(ctx context.Context, b *bot.Bot, update *models.Update, text string) bool {
	adminID, chatID := update.Message.From.ID, update.Message.Chat.ID
	parts := strings.Fields(text)
	if len(parts) < 2 {
		a.sendText(ctx, b, chatID, "❌ ভুল ফরম্যাট। উদাহরণ: <code>1234567890 20৳</code>", nil)
		return true
	}
	targetID, e1 := strconv.ParseInt(parts[0], 10, 64)
	amountText := strings.NewReplacer("৳", "", "BDT", "", "bdt", "", ",", "").Replace(parts[1])
	reward, e2 := decimal.NewFromString(amountText)
	if e1 != nil || targetID <= 0 || e2 != nil || !reward.IsPositive() {
		a.sendText(ctx, b, chatID, "❌ সঠিক User ID এবং positive BDT amount দিন। উদাহরণ: <code>1234567890 20৳</code>", nil)
		return true
	}
	var target bson.M
	if err := a.DB.Users.FindOne(ctx, bson.M{"user_id": targetID}).Decode(&target); err != nil {
		a.sendText(ctx, b, chatID, fmt.Sprintf("❌ User ID (<code>%d</code>) ডাটাবেজে পাওয়া যায়নি!", targetID), PermanentAdminOthersMenu())
		adminToolSessions.Delete(adminID)
		_ = deleteWorkflowSession(ctx, a.DB, "admin_tool", adminID)
		return true
	}
	orderID, err := GenerateUniqueOrderID(ctx, a.DB)
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Reward transaction ID তৈরি করা যায়নি।", nil)
		return true
	}
	applied, err := ApplyWalletDeltaAnd(ctx, a.DB, targetID, reward,
		fmt.Sprintf("leaderboard_reward:%d", orderID), "leaderboard_reward", strconv.FormatInt(orderID, 10),
		func(tx context.Context) error {
			_, insertErr := a.DB.Orders.InsertOne(tx, bson.M{"id": orderID, "user_id": targetID, "type": "admin_add", "amount": "0", "total_bdt": reward.String(), "status": "paid", "created_at": BangladeshTime()})
			return insertErr
		})
	if err != nil || !applied {
		a.sendText(ctx, b, chatID, "❌ Reward নিরাপদে জমা হয়নি; Wallet অপরিবর্তিত রাখা হয়েছে।", nil)
		return true
	}
	var updated bson.M
	_ = a.DB.Users.FindOne(ctx, bson.M{"user_id": targetID}).Decode(&updated)
	balance, _ := decimal.NewFromString(orderString(updated, "mini_wallet", "0"))
	_ = LogAdminAction(ctx, a.DB, adminID, update.Message.From.Username, "N/A", "N/A", "Leaderboard Reward", "0", reward.String())
	adminToolSessions.Delete(adminID)
	_ = deleteWorkflowSession(ctx, a.DB, "admin_tool", adminID)
	a.sendText(ctx, b, targetID, fmt.Sprintf("🎉 <b>অভিনন্দন!</b>\n\n🏆 Leaderboard-এ থাকার জন্য অ্যাডমিন আপনাকে ৳<b>%s</b> রিওয়ার্ড দিয়েছেন।\nএটি আপনার My Wallet-এ যুক্ত হয়েছে।\nবর্তমান ব্যালেন্স: ৳<b>%s</b>", Money(reward), Money(balance)), MainMenu())
	a.sendText(ctx, b, chatID, fmt.Sprintf("✅ User ID (<code>%d</code>) কে ৳<b>%s</b> Reward দেওয়া হয়েছে।\nনতুন Wallet Balance: ৳<b>%s</b>", targetID, Money(reward), Money(balance)), PermanentAdminOthersMenu())
	return true
}

func (a *BotApp) showAdminActivityLogs(ctx context.Context, b *bot.Bot, chatID int64) {
	cur, err := a.DB.AuditLogs.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: -1}}).SetLimit(15))
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Admin Activity Log লোড করা যায়নি।", nil)
		return
	}
	defer cur.Close(ctx)
	var out strings.Builder
	out.WriteString("📋 <b>Administrator Activity Log — Latest 15 Actions</b>\n\n")
	count := 0
	for cur.Next(ctx) {
		var row bson.M
		if cur.Decode(&row) != nil {
			continue
		}
		count++
		username := orderString(row, "admin_username", "N/A")
		if username != "N/A" {
			username = "@" + username
		}
		fmt.Fprintf(&out, "👤 Admin ID: <code>%s</code> (%s)\n📦 Ref ID: <code>#%s</code> | Trx: <code>%s</code>\n📌 Status: <code>%s</code>\n💰 Amount: <b>%s USDT = ৳%s</b>\n⏱️ Time: <i>%s</i>\n───────────────────────────\n",
			orderString(row, "admin_id", "N/A"), htmlEscape(username), htmlEscape(orderString(row, "order_id", "N/A")), htmlEscape(orderString(row, "trx_id", "N/A")), htmlEscape(orderString(row, "order_status", "N/A")), htmlEscape(orderString(row, "amount_usdt", "0")), htmlEscape(orderString(row, "amount_bdt", "0")), htmlEscape(orderString(row, "timestamp", "N/A")))
		if out.Len() > 3500 {
			out.WriteString("… আরও Log রয়েছে।\n")
			break
		}
	}
	if count == 0 {
		out.WriteString("📋 কোনো অ্যাডমিন অ্যাক্টিভিটি রেকর্ড নেই।")
	}
	a.sendText(ctx, b, chatID, out.String(), AdminMenuWithCounts(chatID, a.Config.AdminID, 0, 0, 0, 0))
}

func (a *BotApp) showAllWithdrawalHistory(ctx context.Context, b *bot.Bot, chatID int64) {
	cur, err := a.DB.WithdrawRequests.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "id", Value: -1}}).SetLimit(25))
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Withdrawal History লোড করা যায়নি।", nil)
		return
	}
	defer cur.Close(ctx)
	var out strings.Builder
	out.WriteString("📜 <b>All Withdrawal History (Last 25) [Permanent Admin]</b>\n\n")
	count := 0
	for cur.Next(ctx) {
		var row bson.M
		if cur.Decode(&row) != nil {
			continue
		}
		count++
		fmt.Fprintf(&out, "🏧 ID: <code>#%d</code> | User: <code>%d</code>\n💰 Amount: ৳<b>%s</b> | Method: <b>%s</b>\n📱 Number: <code>%s</code>\n📌 Status: <code>%s</code> | Time: <i>%s</i>\n───────────────────────────\n",
			orderInt64(row, "id"), orderInt64(row, "user_id"), htmlEscape(orderString(row, "amount", "0")), htmlEscape(orderString(row, "method", "N/A")), htmlEscape(orderString(row, "number", "N/A")), htmlEscape(orderString(row, "status", "unknown")), htmlEscape(orderString(row, "created_at", "N/A")))
	}
	if count == 0 {
		out.WriteString("📜 কোনো উইথড্র হিস্ট্রি পাওয়া যায়নি।")
	}
	if out.Len() > 3900 {
		outString := out.String()
		out.Reset()
		out.WriteString(outString[:3800] + "\n… তালিকা সংক্ষিপ্ত করা হয়েছে।")
	}
	a.sendText(ctx, b, chatID, out.String(), AdminMenuWithCounts(chatID, a.Config.AdminID, 0, 0, 0, 0))
}

type combinedHistoryItem struct {
	ID   int64
	Kind string
	Row  bson.M
}

func (a *BotApp) showCombinedHistory(ctx context.Context, b *bot.Bot, chatID int64) {
	items := make([]combinedHistoryItem, 0, 50)
	orderCur, err := a.DB.Orders.Find(ctx, bson.M{"type": bson.M{"$ne": "admin_add"}}, options.Find().SetSort(bson.D{{Key: "id", Value: -1}}).SetLimit(25))
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Sell history লোড করা যায়নি।", nil)
		return
	}
	for orderCur.Next(ctx) {
		var row bson.M
		if orderCur.Decode(&row) == nil {
			items = append(items, combinedHistoryItem{ID: orderInt64(row, "id"), Kind: "order", Row: row})
		}
	}
	_ = orderCur.Close(ctx)
	withdrawCur, err := a.DB.WithdrawRequests.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "id", Value: -1}}).SetLimit(25))
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Withdrawal history লোড করা যায়নি।", nil)
		return
	}
	for withdrawCur.Next(ctx) {
		var row bson.M
		if withdrawCur.Decode(&row) == nil {
			items = append(items, combinedHistoryItem{ID: orderInt64(row, "id"), Kind: "withdraw", Row: row})
		}
	}
	_ = withdrawCur.Close(ctx)
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	if len(items) > 25 {
		items = items[:25]
	}
	if len(items) == 0 {
		a.sendText(ctx, b, chatID, "কোনো হিস্ট্রি পাওয়া যায়নি।", AdminMenuWithCounts(chatID, a.Config.AdminID, 0, 0, 0, 0))
		return
	}
	a.sendText(ctx, b, chatID, "📜 <b>Combined Sell & Withdrawal History (Last 25) [Sub-Admin]</b>", nil)
	for _, item := range items {
		row := item.Row
		var text string
		if item.Kind == "order" {
			wallet := orderString(row, "wallet_diff", "")
			walletLine := ""
			if wallet != "" {
				walletLine = " | Wallet: " + htmlEscape(wallet) + "৳"
			}
			text = fmt.Sprintf("📦 <b>[Sell Order] <code>#%d</code></b>\n👤 User: <code>%d</code> | 💰 <code>%s USDT</code>\n💵 Total: ৳<code>%s</code>%s | Method: <b>%s</b>\n📌 Status: <code>%s</code> | Time: <i>%s</i>", item.ID, orderInt64(row, "user_id"), htmlEscape(orderString(row, "amount", "0")), htmlEscape(orderString(row, "total_bdt", "0")), walletLine, htmlEscape(orderString(row, "network", "N/A")), htmlEscape(orderString(row, "status", "unknown")), htmlEscape(orderString(row, "created_at", "N/A")))
			if reason := orderString(row, "reject_reason", ""); reason != "" {
				text += "\n💬 Reason: " + htmlEscape(reason)
			}
		} else {
			wallet := orderString(row, "wallet_diff", "")
			walletLine := ""
			if wallet != "" {
				walletLine = " | Wallet: " + htmlEscape(wallet) + "৳"
			}
			text = fmt.Sprintf("🏧 <b>[Withdrawal] <code>#%d</code></b>\n👤 User: <code>%d</code> | Amount: ৳<b>%s</b>%s\n🌐 Method: <b>%s</b> | Status: <code>%s</code>\n📱 Number: <code>%s</code>", item.ID, orderInt64(row, "user_id"), htmlEscape(orderString(row, "amount", "0")), walletLine, htmlEscape(orderString(row, "method", "N/A")), htmlEscape(orderString(row, "status", "unknown")), htmlEscape(orderString(row, "number", "N/A")))
			if reason := orderString(row, "reject_reason", ""); reason != "" {
				text += "\n💬 Reason: " + htmlEscape(reason)
			}
		}
		a.sendText(ctx, b, chatID, text, nil)
	}
	a.sendText(ctx, b, chatID, "📊 Admin Menu", AdminMenuWithCounts(chatID, a.Config.AdminID, 0, 0, 0, 0))
}

// searchAdminRecords searches all operational collections using one input.
// Supported identifiers: order ID, withdrawal ID, user ID, mobile number,
// Telegram file_id, transaction/TRX ID, and support ticket ID.
func (a *BotApp) searchAdminRecords(ctx context.Context, b *bot.Bot, chatID int64, raw string) {
	input := strings.TrimSpace(raw)
	if input == "" {
		a.sendText(ctx, b, chatID, "❌ Search value খালি রাখা যাবে না।", nil)
		return
	}

	kind := ""
	value := input
	if i := strings.IndexByte(input, ':'); i > 0 {
		prefix := strings.ToLower(strings.TrimSpace(input[:i]))
		switch prefix {
		case "order", "withdraw", "user", "trx", "file", "ticket", "mobile":
			kind = prefix
			value = strings.TrimSpace(input[i+1:])
		}
	}
	value = strings.TrimSpace(strings.TrimPrefix(value, "#"))
	if value == "" {
		a.sendText(ctx, b, chatID, "❌ Search value সঠিক নয়।", nil)
		return
	}

	// Ticket IDs are alphanumeric and should be searched directly.
	if kind == "ticket" || strings.HasPrefix(strings.ToUpper(value), "TICK-") {
		ticketID := value
		if !strings.HasPrefix(ticketID, "#") {
			ticketID = "#" + ticketID
		}
		var ticket bson.M
		if err := a.DB.Tickets.FindOne(ctx, bson.M{"ticket_id": ticketID}).Decode(&ticket); err == nil {
			a.renderAdminSearchTicket(ctx, b, chatID, ticket)
			return
		}
	}

	digits := strings.NewReplacer(" ", "", "-", "", "+", "").Replace(value)
	numericID, numericErr := strconv.ParseInt(digits, 10, 64)
	isMobile := regexp.MustCompile(`^(?:88)?01[3-9]\d{8}$`).MatchString(digits)
	regexValue := regexp.QuoteMeta(value)
	if isMobile {
		// Match Bangladesh numbers even if stored with spaces or hyphens.
		parts := strings.Split(digits, "")
		regexValue = strings.Join(parts, `[\s-]*`)
	}
	rx := bson.M{"$regex": regexValue, "$options": "i"}

	orderOr := bson.A{
		bson.M{"txid": rx}, bson.M{"trx_id": rx}, bson.M{"txid_normalized": rx}, bson.M{"transaction_id": rx}, bson.M{"payout_number": rx},
		bson.M{"proof_file_id": rx}, bson.M{"file_id": rx}, bson.M{"photo_file_id": rx}, bson.M{"media_file_id": rx}, bson.M{"payout_file_id": rx}, bson.M{"qr_file_id": rx},
	}
	withdrawOr := bson.A{
		bson.M{"number": rx}, bson.M{"txid": rx}, bson.M{"trx_id": rx}, bson.M{"transaction_id": rx},
		bson.M{"file_id": rx}, bson.M{"proof_file_id": rx}, bson.M{"media_file_id": rx}, bson.M{"photo_file_id": rx}, bson.M{"qr_file_id": rx},
	}
	ticketOr := bson.A{
		bson.M{"ticket_id": rx}, bson.M{"media_file_id": rx},
		bson.M{"details": rx},
	}
	userOr := bson.A{
		bson.M{"username": rx}, bson.M{"full_name": rx},
		bson.M{"phone": rx}, bson.M{"mobile": rx},
		bson.M{"payment_numbers.bKash": rx}, bson.M{"payment_numbers.Nagad": rx},
		bson.M{"payment_numbers.Rocket": rx}, bson.M{"payment_numbers.Upay": rx},
	}
	if numericErr == nil && numericID > 0 {
		if kind == "" || kind == "order" {
			orderOr = append(orderOr, bson.M{"id": numericID})
		}
		if kind == "" || kind == "user" {
			userOr = append(userOr, bson.M{"user_id": numericID})
		}
		if kind == "" || kind == "withdraw" {
			withdrawOr = append(withdrawOr, bson.M{"id": numericID})
		}
		if kind == "" || kind == "user" {
			orderOr = append(orderOr, bson.M{"user_id": numericID})
			withdrawOr = append(withdrawOr, bson.M{"user_id": numericID})
			ticketOr = append(ticketOr, bson.M{"user_id": numericID}, bson.M{"order_id": digits})
		}
	}
	if kind == "trx" {
		orderOr = bson.A{bson.M{"txid": rx}, bson.M{"trx_id": rx}, bson.M{"txid_normalized": rx}, bson.M{"transaction_id": rx}}
		withdrawOr = bson.A{bson.M{"txid": rx}, bson.M{"trx_id": rx}, bson.M{"transaction_id": rx}}
	}
	if kind == "file" {
		orderOr = bson.A{bson.M{"txid": rx}, bson.M{"payout_number": rx}, bson.M{"proof_file_id": rx}, bson.M{"file_id": rx}, bson.M{"photo_file_id": rx}, bson.M{"media_file_id": rx}, bson.M{"payout_file_id": rx}, bson.M{"qr_file_id": rx}}
		withdrawOr = bson.A{bson.M{"number": rx}, bson.M{"file_id": rx}, bson.M{"proof_file_id": rx}, bson.M{"media_file_id": rx}, bson.M{"photo_file_id": rx}, bson.M{"qr_file_id": rx}}
		ticketOr = bson.A{bson.M{"media_file_id": rx}}
	}

	var out strings.Builder
	fmt.Fprintf(&out, "🔎 <b>Admin Search Results</b>\nQuery: <code>%s</code>\n\n", htmlEscape(input))
	total := 0
	appendResult := func(label, body string) {
		if total >= 12 {
			return
		}
		total++
		fmt.Fprintf(&out, "🔹 <b>%s</b>\n%s\n────────────────────\n", label, body)
	}

	// Orders
	if kind == "" || kind == "order" || kind == "user" || kind == "mobile" || kind == "trx" || kind == "file" {
		cur, err := a.DB.Orders.Find(ctx, bson.M{"$or": orderOr, "type": bson.M{"$ne": "admin_add"}}, options.Find().SetSort(bson.D{{Key: "id", Value: -1}}).SetLimit(5))
		if err == nil {
			for cur.Next(ctx) {
				var row bson.M
				if cur.Decode(&row) != nil {
					continue
				}
				id := orderInt64(row, "id")
				body := fmt.Sprintf("Order ID: <code>#%d</code>\nUser ID: <code>%d</code>\nAmount: <b>%s USDT</b> | Total: <b>৳%s</b>\nNetwork: <b>%s</b>\nTRX/Proof: <code>%s</code>\nPayout: <b>%s</b> | Number/File: <code>%s</code>\nStatus: <code>%s</code>\nTime: <i>%s</i>",
					id, orderInt64(row, "user_id"), htmlEscape(orderString(row, "amount", "0")), htmlEscape(orderString(row, "total_bdt", "0")),
					htmlEscape(orderString(row, "network", "N/A")), htmlEscape(orderString(row, "txid", orderString(row, "trx_id", "N/A"))),
					htmlEscape(orderString(row, "payout", "N/A")), htmlEscape(orderString(row, "payout_number", "N/A")),
					htmlEscape(orderString(row, "status", "unknown")), htmlEscape(orderString(row, "created_at", "N/A")))
				appendResult(fmt.Sprintf("SELL ORDER #%d", id), body)
			}
			cur.Close(ctx)
		}
	}

	// Withdrawals
	if kind == "" || kind == "withdraw" || kind == "user" || kind == "mobile" || kind == "trx" || kind == "file" {
		cur, err := a.DB.WithdrawRequests.Find(ctx, bson.M{"$or": withdrawOr}, options.Find().SetSort(bson.D{{Key: "id", Value: -1}}).SetLimit(5))
		if err == nil {
			for cur.Next(ctx) {
				var row bson.M
				if cur.Decode(&row) != nil {
					continue
				}
				id := orderInt64(row, "id")
				body := fmt.Sprintf("Withdrawal ID: <code>#%d</code>\nUser ID: <code>%d</code>\nAmount: <b>৳%s</b>\nMethod: <b>%s</b>\nMobile / File ID: <code>%s</code>\nStatus: <code>%s</code>\nTime: <i>%s</i>",
					id, orderInt64(row, "user_id"), htmlEscape(orderString(row, "amount", "0")), htmlEscape(orderString(row, "method", "N/A")),
					htmlEscape(orderString(row, "number", "N/A")), htmlEscape(orderString(row, "status", "unknown")), htmlEscape(orderString(row, "created_at", "N/A")))
				appendResult(fmt.Sprintf("WITHDRAWAL #%d", id), body)
			}
			cur.Close(ctx)
		}
	}

	// User records, including numeric user ID and mobile/username search.
	if kind == "" || kind == "user" || kind == "mobile" {
		cur, err := a.DB.Users.Find(ctx, bson.M{"$or": userOr}, options.Find().SetLimit(5))
		if err == nil {
			for cur.Next(ctx) {
				var row bson.M
				if cur.Decode(&row) != nil {
					continue
				}
				uid := orderInt64(row, "user_id")
				body := fmt.Sprintf("User ID: <code>%d</code>\nName: <b>%s</b>\nUsername: @%s\nWallet: <b>৳%s</b>\nReferral Bonus: <b>৳%s</b>\nStatus: <b>%s</b>",
					uid, htmlEscape(orderString(row, "full_name", "N/A")), htmlEscape(orderString(row, "username", "N/A")),
					htmlEscape(orderString(row, "mini_wallet", "0")), htmlEscape(orderString(row, "referral_bonus_bdt", "0")),
					map[bool]string{true: "Banned", false: "Active"}[row["is_banned"] == true])
				appendResult(fmt.Sprintf("USER #%d", uid), body)
			}
			cur.Close(ctx)
		}
	}

	// Audit log lookup is especially useful for TRX ID and historical order references.
	if kind == "" || kind == "trx" || kind == "order" || kind == "user" {
		auditOr := bson.A{bson.M{"trx_id": rx}, bson.M{"order_id": rx}}
		if numericErr == nil && numericID > 0 && kind == "" {
			auditOr = append(auditOr, bson.M{"admin_id": numericID})
		}
		cur, err := a.DB.AuditLogs.Find(ctx, bson.M{"$or": auditOr}, options.Find().SetSort(bson.D{{Key: "timestamp", Value: -1}}).SetLimit(5))
		if err == nil {
			for cur.Next(ctx) {
				var row bson.M
				if cur.Decode(&row) != nil {
					continue
				}
				body := fmt.Sprintf("Admin ID: <code>%s</code>\nOrder/Reference ID: <code>%s</code>\nTRX ID: <code>%s</code>\nAction: <b>%s</b>\nStatus: <code>%s</code>\nAmount: <b>%s USDT = ৳%s</b>\nTime: <i>%s</i>",
					htmlEscape(orderString(row, "admin_id", "N/A")), htmlEscape(orderString(row, "order_id", "N/A")),
					htmlEscape(orderString(row, "trx_id", "N/A")), htmlEscape(orderString(row, "action", "N/A")),
					htmlEscape(orderString(row, "order_status", "N/A")), htmlEscape(orderString(row, "amount_usdt", "0")),
					htmlEscape(orderString(row, "amount_bdt", "0")), htmlEscape(orderString(row, "timestamp", "N/A")))
				appendResult("ADMIN AUDIT LOG", body)
			}
			cur.Close(ctx)
		}
	}

	// Tickets: ticket ID, media file ID, or a numeric user/order ID.
	if kind == "" || kind == "ticket" || kind == "user" || kind == "file" {
		cur, err := a.DB.Tickets.Find(ctx, bson.M{"$or": ticketOr}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(5))
		if err == nil {
			for cur.Next(ctx) {
				var row bson.M
				if cur.Decode(&row) != nil {
					continue
				}
				body := fmt.Sprintf("Ticket ID: <code>%s</code>\nUser ID: <code>%d</code>\nOrder ID: <code>%s</code>\nStatus: <code>%s</code>\nDetails: %s\nMedia Type: <code>%s</code>\nFile ID: <code>%s</code>",
					htmlEscape(orderString(row, "ticket_id", "N/A")), orderInt64(row, "user_id"), htmlEscape(orderString(row, "order_id", "N/A")),
					htmlEscape(orderString(row, "status", "unknown")), htmlEscape(orderString(row, "details", "")),
					htmlEscape(orderString(row, "media_type", "N/A")), htmlEscape(orderString(row, "media_file_id", "N/A")))
				appendResult("SUPPORT TICKET", body)
			}
			cur.Close(ctx)
		}
	}

	if total == 0 {
		a.sendText(ctx, b, chatID, fmt.Sprintf("❌ <code>%s</code> দিয়ে কোনো Order, Withdrawal, User, Ticket বা File পাওয়া যায়নি।", htmlEscape(input)), AdminMenuWithCounts(chatID, a.Config.AdminID, 0, 0, 0, 0))
		return
	}
	out.WriteString(fmt.Sprintf("\nমোট Result দেখানো হয়েছে: <b>%d</b>", total))
	if out.Len() > 3900 {
		outText := out.String()
		out.Reset()
		out.WriteString(outText[:3800] + "\n… আরও Result থাকতে পারে।")
	}
	a.sendText(ctx, b, chatID, out.String(), AdminMenuWithCounts(chatID, a.Config.AdminID, 0, 0, 0, 0))
}

func (a *BotApp) renderAdminSearchTicket(ctx context.Context, b *bot.Bot, chatID int64, ticket bson.M) {
	ticketID := orderString(ticket, "ticket_id", "")
	status := orderString(ticket, "status", "unknown")
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "💬 View Support Chat", CallbackData: "chat_backup:" + ticketID}}}}
	if status == "open" {
		markup.InlineKeyboard[0] = append(markup.InlineKeyboard[0], models.InlineKeyboardButton{Text: "✍️ Reply", CallbackData: fmt.Sprintf("support_reply:%d:%s", orderInt64(ticket, "user_id"), ticketID)})
	}
	body := fmt.Sprintf("🎫 <b>Support Ticket Found</b>\nTicket ID: <code>%s</code>\nUser ID: <code>%d</code>\nOrder ID: <code>%s</code>\nStatus: <code>%s</code>\nDetails: %s\nFile ID: <code>%s</code>",
		htmlEscape(ticketID), orderInt64(ticket, "user_id"), htmlEscape(orderString(ticket, "order_id", "N/A")),
		htmlEscape(status), htmlEscape(orderString(ticket, "details", "")), htmlEscape(orderString(ticket, "media_file_id", "N/A")))
	a.sendText(ctx, b, chatID, body, markup)
}

func (a *BotApp) lookupAdminRecord(ctx context.Context, b *bot.Bot, chatID int64, rawID string) {
	if strings.HasPrefix(rawID, "TICK-") {
		rawID = "#" + rawID
	}
	if strings.HasPrefix(rawID, "#TICK-") {
		var ticket bson.M
		if err := a.DB.Tickets.FindOne(ctx, bson.M{"ticket_id": rawID}).Decode(&ticket); err != nil {
			a.sendText(ctx, b, chatID, fmt.Sprintf("❌ Ticket ID (<code>%s</code>) পাওয়া যায়নি।", htmlEscape(rawID)), nil)
			return
		}
		status := orderString(ticket, "status", "unknown")
		text := fmt.Sprintf("🔍 <b>[Support Ticket Found]</b>\n\n🎫 Ticket ID: <code>%s</code>\n👤 User ID: <code>%d</code>\n📌 Order ID: <code>#%s</code>\n📌 Status: <code>%s</code>\n💬 Problem/Details: <b>%s</b>\n⏱️ Time: <i>%s</i>", htmlEscape(rawID), orderInt64(ticket, "user_id"), htmlEscape(orderString(ticket, "order_id", "N/A")), htmlEscape(status), htmlEscape(orderString(ticket, "details", "")), htmlEscape(orderString(ticket, "created_at", "N/A")))
		var markup models.ReplyMarkup
		if status == "open" {
			markup = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✍️ Reply to User", CallbackData: fmt.Sprintf("support_reply:%d:%s", orderInt64(ticket, "user_id"), rawID)}, {Text: "💬 View Support Chat", CallbackData: "chat_backup:" + rawID}, {Text: "✅ Mark as Resolved", CallbackData: "resolve_ticket:" + rawID}}}}
		} else {
			markup = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "💬 View Support Chat", CallbackData: "chat_backup:" + rawID}}}}
		}
		mediaID := orderString(ticket, "media_file_id", "")
		mediaType := orderString(ticket, "media_type", "")
		if mediaID == "" || !a.sendTicketAttachment(ctx, b, chatID, mediaType, mediaID, text, markup) {
			a.sendText(ctx, b, chatID, text, markup)
		}
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(rawID, "#"), 10, 64)
	if err != nil {
		return
	}
	var order bson.M
	if a.DB.Orders.FindOne(ctx, bson.M{"id": id}).Decode(&order) == nil {
		uid := orderInt64(order, "user_id")
		payoutNumber := orderString(order, "payout_number", "N/A")
		if strings.HasPrefix(payoutNumber, "Photo [File ID:") {
			payoutNumber = "🖼️ [QR Code Image Attached]"
		}
		text := fmt.Sprintf("🔍 <b>[Sell Order Found] — <code>#%d</code></b>\n\n👤 User ID: <code>%d</code>\n🏷️ Username: <b>@%s</b>\n💰 Amount: <code>%s USDT</code>\n💵 Total BDT: ৳<code>%s</code>\n🌐 Network: <b>%s</b>\n📌 Status: <code>%s</code>\n💳 Payout: <b>%s</b> (%s)\n⏱️ Time: <i>%s</i>\n\n📝 Proof / TxID:\n<code>%s</code>", id, uid, htmlEscape(orderString(order, "username", "N/A")), htmlEscape(orderString(order, "amount", "0")), htmlEscape(orderString(order, "total_bdt", "0")), htmlEscape(orderString(order, "network", "N/A")), htmlEscape(orderString(order, "status", "unknown")), htmlEscape(orderString(order, "payout", "N/A")), htmlEscape(payoutNumber), htmlEscape(orderString(order, "created_at", "N/A")), htmlEscape(orderString(order, "txid", "N/A")))
		buttons := [][]models.InlineKeyboardButton{}
		switch orderString(order, "status", "") {
		case "pending_verification":
			buttons = append(buttons, []models.InlineKeyboardButton{{Text: "✅ Approve", CallbackData: fmt.Sprintf("approve:%d", id)}, {Text: "❌ Reject Request", CallbackData: fmt.Sprintf("reject:%d", id)}})
		case "rejected":
			buttons = append(buttons, []models.InlineKeyboardButton{{Text: "♻️ Re-Approve", CallbackData: fmt.Sprintf("reapprove:%d", id)}, {Text: "🗑️ Reject Permanently", CallbackData: fmt.Sprintf("hard_reject:%d", id)}})
		case "approved_pending_payment", "approved_pending_payout":
			buttons = append(buttons, []models.InlineKeyboardButton{{Text: "💸 Paid", CallbackData: fmt.Sprintf("paid:%d", id)}, {Text: "📱 Request New Payment Details", CallbackData: fmt.Sprintf("req_alt_%d_%d", uid, id)}})
		}
		var markup models.ReplyMarkup
		if len(buttons) > 0 {
			markup = &models.InlineKeyboardMarkup{InlineKeyboard: buttons}
		}
		mediaID := extractEmbeddedPhotoFileID(orderString(order, "txid", ""))
		if mediaID == "" {
			mediaID, _ = withdrawQRFileID(orderString(order, "payout_number", ""))
		}
		if mediaID != "" {
			if _, sendErr := b.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: chatID, Photo: &models.InputFileString{Data: mediaID}, Caption: text, ParseMode: models.ParseModeHTML, ReplyMarkup: markup}); sendErr == nil {
				return
			}
		}
		a.sendText(ctx, b, chatID, text, markup)
		return
	}
	var withdrawal bson.M
	if a.DB.WithdrawRequests.FindOne(ctx, bson.M{"id": id}).Decode(&withdrawal) == nil {
		text := fmt.Sprintf("🔍 <b>[Withdraw Request Found] — <code>#%d</code></b>\n\n👤 User ID: <code>%d</code>\n🏷️ Username: <b>@%s</b>\n💰 Amount: ৳<b>%s</b>\n🌐 Method: <b>%s</b>\n📱 Number/Address: <code>%s</code>\n📌 Status: <code>%s</code>\n⏱️ Time: <i>%s</i>", id, orderInt64(withdrawal, "user_id"), htmlEscape(orderString(withdrawal, "username", "N/A")), htmlEscape(orderString(withdrawal, "amount", "0")), htmlEscape(orderString(withdrawal, "method", "N/A")), htmlEscape(orderString(withdrawal, "number", "N/A")), htmlEscape(orderString(withdrawal, "status", "unknown")), htmlEscape(orderString(withdrawal, "created_at", "N/A")))
		var markup models.ReplyMarkup
		if orderString(withdrawal, "status", "") == "pending" {
			markup = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✅ Paid", CallbackData: fmt.Sprintf("w_paid:%d", id)}, {Text: "❌ Reject Request", CallbackData: fmt.Sprintf("w_reject:%d", id)}}}}
		}
		if fileID, ok := withdrawQRFileID(orderString(withdrawal, "number", "")); ok {
			if _, sendErr := b.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: chatID, Photo: &models.InputFileString{Data: fileID}, Caption: text, ParseMode: models.ParseModeHTML, ReplyMarkup: markup}); sendErr == nil {
				return
			}
		}
		a.sendText(ctx, b, chatID, text, markup)
		return
	}
	var user bson.M
	if a.DB.Users.FindOne(ctx, bson.M{"user_id": id}).Decode(&user) == nil {
		total, _ := a.DB.Orders.CountDocuments(ctx, bson.M{"user_id": id, "type": bson.M{"$ne": "admin_add"}})
		completed, _ := a.DB.Orders.Find(ctx, bson.M{"user_id": id, "status": "paid", "type": bson.M{"$ne": "admin_add"}})
		defer completed.Close(ctx)
		count := int64(0)
		totalUSDT, totalBDT := decimal.Zero, decimal.Zero
		for completed.Next(ctx) {
			var row bson.M
			if completed.Decode(&row) == nil {
				count++
				if v, e := decimal.NewFromString(orderString(row, "amount", "0")); e == nil {
					totalUSDT = totalUSDT.Add(v)
				}
				if v, e := decimal.NewFromString(strings.ReplaceAll(orderString(row, "total_bdt", "0"), ",", "")); e == nil {
					totalBDT = totalBDT.Add(v)
				}
			}
		}
		rate := decimal.Zero
		if total > 0 {
			rate = decimal.NewFromInt(count).Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(total))
		}
		openTickets, _ := a.DB.Tickets.CountDocuments(ctx, bson.M{"user_id": id, "status": "open"})
		text := fmt.Sprintf("🔍 <b>[User Profile Found] — <code>%d</code></b>\n\n🏷️ Username: <b>@%s</b>\n💳 Wallet Balance: ৳<code>%s</code>\n👥 Valid Referrals: <b>%d</b>\n🎁 Referral Bonus: ৳<code>%s</code>\n📅 Joined: <i>%s</i>\n\n📊 <b>Account Activity</b>\n• Total Orders: <b>%d</b>\n• Successful Orders: <b>%d</b>\n• Success Rate: <b>%s%%</b>\n• Total USDT Sold: <code>%s</code> USDT\n• Total BDT Received: ৳<code>%s</code>\n• Active Support Tickets: <b>%d</b>", id, htmlEscape(orderString(user, "username", "N/A")), MoneyString(orderString(user, "mini_wallet", "0")), orderInt64(user, "valid_referrals"), MoneyString(orderString(user, "referral_bonus_bdt", "0")), htmlEscape(orderString(user, "created_at", "N/A")), total, count, rate.StringFixed(1), totalUSDT, Money(totalBDT), openTickets)
		a.sendText(ctx, b, chatID, text, nil)
		return
	}
	a.sendText(ctx, b, chatID, fmt.Sprintf("❌ ID (<code>%s</code>) দিয়ে সিস্টেমে কোনো অর্ডার, উইথড্র বা ইউজার ডাটা পাওয়া যায়নি।", htmlEscape(rawID)), nil)
}

func MoneyString(v string) string {
	parsed, err := decimal.NewFromString(strings.ReplaceAll(v, ",", ""))
	if err != nil {
		return "0"
	}
	return Money(parsed)
}

func (a *BotApp) lookupMobileNumber(ctx context.Context, b *bot.Bot, chatID int64, raw string) {
	number := strings.NewReplacer(" ", "", "-", "").Replace(raw)
	pattern := regexp.QuoteMeta(number)
	orders, err := a.DB.Orders.Find(ctx, bson.M{"payout_number": bson.M{"$regex": pattern}, "type": bson.M{"$ne": "admin_add"}})
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Mobile number search করা যায়নি।", nil)
		return
	}
	type matched struct {
		ID     int64
		UserID int64
		Amount decimal.Decimal
		Method string
		Kind   string
	}
	items := []matched{}
	total := decimal.Zero
	methods := map[string]decimal.Decimal{}
	for orders.Next(ctx) {
		var row bson.M
		if orders.Decode(&row) != nil {
			continue
		}
		amount, e := decimal.NewFromString(strings.ReplaceAll(orderString(row, "total_bdt", "0"), ",", ""))
		if e != nil {
			amount = decimal.Zero
		}
		method := strings.TrimSpace(strings.Split(orderString(row, "payout", "Unknown"), "(")[0])
		total = total.Add(amount)
		methods[method] = methods[method].Add(amount)
		items = append(items, matched{orderInt64(row, "id"), orderInt64(row, "user_id"), amount, method, "Order"})
	}
	_ = orders.Close(ctx)
	withdrawals, err := a.DB.WithdrawRequests.Find(ctx, bson.M{"number": bson.M{"$regex": pattern}})
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Withdrawal number search করা যায়নি।", nil)
		return
	}
	for withdrawals.Next(ctx) {
		var row bson.M
		if withdrawals.Decode(&row) != nil {
			continue
		}
		amount, e := decimal.NewFromString(strings.ReplaceAll(orderString(row, "amount", "0"), ",", ""))
		if e != nil {
			amount = decimal.Zero
		}
		method := orderString(row, "method", "Unknown")
		total = total.Add(amount)
		methods[method] = methods[method].Add(amount)
		items = append(items, matched{orderInt64(row, "id"), orderInt64(row, "user_id"), amount, method, "Withdraw"})
	}
	_ = withdrawals.Close(ctx)
	if len(items) == 0 {
		a.sendText(ctx, b, chatID, fmt.Sprintf("🔍 মোবাইল নম্বর <code>%s</code> দিয়ে কোনো অর্ডার বা উইথড্র পাওয়া যায়নি।", htmlEscape(number)), nil)
		return
	}
	var out strings.Builder
	fmt.Fprintf(&out, "🔍 <b>Mobile Number Search Report</b>\n📱 Number: <code>%s</code>\n\n💰 <b>Total BDT Transacted:</b> ৳<b>%s</b>\n\n📊 <b>Method Breakdown:</b>\n", htmlEscape(number), Money(total))
	for method, amount := range methods {
		fmt.Fprintf(&out, "• %s: <b>৳%s</b>\n", htmlEscape(method), Money(amount))
	}
	out.WriteString("\n📜 <b>Transactions List:</b>\n")
	for _, item := range items {
		fmt.Fprintf(&out, "• %s ID: <code>#%d</code> | User ID: <code>%d</code> | ৳<b>%s</b> (%s)\n", item.Kind, item.ID, item.UserID, Money(item.Amount), htmlEscape(item.Method))
	}
	a.sendText(ctx, b, chatID, out.String(), AdminMenuWithCounts(chatID, a.Config.AdminID, 0, 0, 0, 0))
}

func extractEmbeddedPhotoFileID(value string) string {
	match := regexp.MustCompile(`Photo \[File ID: ([A-Za-z0-9_-]+)\]`).FindStringSubmatch(value)
	if len(match) == 2 {
		return match[1]
	}
	return ""
}
