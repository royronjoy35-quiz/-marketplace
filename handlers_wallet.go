package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/shopspring/decimal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- merged from wallet_handlers.go ----

// WalletSession is the Go equivalent of the Python context.user_data fields used
// by wallet withdrawal and saved payout-number workflows.
type WalletSession struct {
	Step       string
	Amount     string
	WithdrawID int64
	Method     string
	Number     string
	Final      string
	WalletDiff string
}

var walletSessions sync.Map         // map[Telegram user ID]WalletSession
var withdrawRejectSessions sync.Map // map[admin ID]withdraw ID

var bdMobilePattern = regexp.MustCompile(`^(?:\+?88)?01[3-9][0-9]{8}$`)

func (a *BotApp) walletSession(ctx context.Context, userID int64) WalletSession {
	if v, ok := walletSessions.Load(userID); ok {
		if s, ok := v.(WalletSession); ok {
			return s
		}
	}
	var restored WalletSession
	if ok, err := loadWorkflowSession(ctx, a.DB, "wallet", userID, &restored); err == nil && ok {
		walletSessions.Store(userID, restored)
		return restored
	}
	return WalletSession{}
}

func walletKeyboard(balance decimal.Decimal) *models.InlineKeyboardMarkup {
	row := []models.InlineKeyboardButton{}
	if balance.GreaterThanOrEqual(decimal.NewFromInt(50)) {
		row = append(row, models.InlineKeyboardButton{Text: "🪙 BDT Withdraw", CallbackData: "wallet_withdraw_menu"})
	}
	row = append(row, models.InlineKeyboardButton{Text: "📜 History", CallbackData: "user_wallet_history"})
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{row, []models.InlineKeyboardButton{{Text: "💳 My Payment Numbers", CallbackData: "my_payment_numbers"}}}}
}

func (a *BotApp) showWallet(ctx context.Context, b *bot.Bot, userID, chatID int64) {
	var user bson.M
	if err := a.DB.Users.FindOne(ctx, bson.M{"user_id": userID}).Decode(&user); err != nil && err != mongo.ErrNoDocuments {
		a.sendText(ctx, b, chatID, "❌ Wallet তথ্য লোড করা যায়নি।", MainMenu())
		return
	}
	balance, _ := decimal.NewFromString(strings.ReplaceAll(fmt.Sprint(user["mini_wallet"]), ",", ""))
	text := fmt.Sprintf("💳 <b>Wallet Dashboard</b>\n\n• বর্তমান ওয়ালেট ব্যালেন্স: ৳<b>%s</b>\n• সর্বনিম্ন withdraw ৳৫০\n\n", Money(balance))
	a.sendPhotoOrText(ctx, b, chatID, a.Config.PhotoWallet, text, walletKeyboard(balance))
}

func (a *BotApp) showPaymentNumbers(ctx context.Context, b *bot.Bot, userID int64) {
	var user bson.M
	_ = a.DB.Users.FindOne(ctx, bson.M{"user_id": userID}).Decode(&user)
	numbers := map[string]interface{}{}
	switch value := user["payment_numbers"].(type) {
	case bson.M:
		for k, v := range value {
			numbers[k] = v
		}
	case map[string]interface{}:
		numbers = value
	}
	methods := []string{"bKash", "Nagad", "Rocket", "Upay"}
	lines := []string{"💳 <b>My Payment Numbers</b>", "", "আপনার প্রতিটি পেমেন্ট মেথডের নম্বর আলাদাভাবে সংরক্ষণ করুন।", ""}
	rows := [][]models.InlineKeyboardButton{}
	for _, method := range methods {
		value := strings.TrimSpace(fmt.Sprint(numbers[method]))
		if value == "<nil>" {
			value = ""
		}
		display := "<i>Not added</i>"
		if value != "" {
			display = "<code>" + htmlEscape(value) + "</code>"
		}
		lines = append(lines, "• <b>"+method+":</b> "+display)
		rows = append(rows, []models.InlineKeyboardButton{{Text: "✏️ Edit " + method, CallbackData: "payment_number_edit:" + method}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: "✖️ Close", CallbackData: "payment_numbers_close"}})
	a.sendText(ctx, b, userID, strings.Join(lines, "\n"), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func normalizeBDMobile(value string) (string, bool) {
	clean := strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(value))
	if !bdMobilePattern.MatchString(clean) {
		return "", false
	}
	clean = strings.TrimPrefix(clean, "+88")
	if strings.HasPrefix(clean, "88") && len(clean) == 13 {
		clean = strings.TrimPrefix(clean, "88")
	}
	return clean, len(clean) == 11
}

func roundDownTen(value decimal.Decimal) (decimal.Decimal, decimal.Decimal) {
	whole := value.Truncate(0)
	diff := whole.Mod(decimal.NewFromInt(10))
	return whole.Sub(diff), diff
}

func (a *BotApp) WalletMessageHandler(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return false
	}
	msg, user := update.Message, update.Message.From
	uid, chatID := user.ID, msg.Chat.ID
	text := strings.TrimSpace(msg.Text)
	if text == "💳 My Wallet" {
		a.showWallet(ctx, b, uid, chatID)
		return true
	}

	s := a.walletSession(ctx, uid)
	if s.Step == "withdraw_amount" {
		amount, err := decimal.NewFromString(strings.ReplaceAll(text, ",", ""))
		if err != nil || amount.LessThan(decimal.NewFromInt(50)) {
			deleteIncomingUserMessage(ctx, b, update)
			a.editWorkflowPrompt(ctx, b, chatID, "❌ <b>Minimum Withdrawal ৳৫০।</b>\nসঠিক Amount লিখে আবার চেষ্টা করুন:", nil)
			return true
		}
		var userDoc bson.M
		if err := a.DB.Users.FindOne(ctx, bson.M{"user_id": uid}).Decode(&userDoc); err != nil {
			a.sendText(ctx, b, chatID, "❌ Wallet তথ্য পাওয়া যায়নি।", MainMenu())
			return true
		}
		balance, err := decimal.NewFromString(strings.ReplaceAll(fmt.Sprint(userDoc["mini_wallet"]), ",", ""))
		if err != nil || balance.LessThan(amount) {
			deleteIncomingUserMessage(ctx, b, update)
			a.editWorkflowPrompt(ctx, b, chatID, fmt.Sprintf("❌ পর্যাপ্ত Wallet Balance নেই।\nবর্তমান ব্যালেন্স: <code>৳%s</code>\nকম Amount লিখে আবার চেষ্টা করুন।", Money(balance)), nil)
			return true
		}
		id, err := GenerateUniqueOrderID(ctx, a.DB)
		if err != nil {
			a.sendText(ctx, b, chatID, "❌ Withdraw ID তৈরি করা যায়নি। আবার চেষ্টা করুন।", MainMenu())
			return true
		}
		deleteIncomingUserMessage(ctx, b, update)
		s = WalletSession{Step: "choose_method", Amount: amount.String(), WithdrawID: id}
		walletSessions.Store(uid, s)
		_ = saveWorkflowSession(ctx, a.DB, "wallet", uid, s, 7*24*time.Hour)
		markup, err := CreatePayoutMenu(ctx, a.DB, "wpayout", id, uid)
		if err != nil {
			a.sendText(ctx, b, chatID, "❌ Payment methods লোড করা যায়নি।", MainMenu())
			return true
		}
		a.editWorkflowPrompt(ctx, b, chatID, fmt.Sprintf("💸 <b>Wallet BDT Withdrawal</b>\n\n• উইথড্র পরিমাণ: ৳<b>%s</b>\n\n💳 কোন নম্বরে টাকা নিতে চান? পেমেন্ট মেথড নির্বাচন করুন:", Money(amount)), markup)
		return true
	}

	if s.Step == "payment_number" {
		method := s.Method
		number, ok := normalizeBDMobile(text)
		if !ok {
			deleteIncomingUserMessage(ctx, b, update)
			a.editWorkflowPrompt(ctx, b, chatID, "❌ <b>সঠিক বাংলাদেশি মোবাইল নম্বর দিন।</b>\nউদাহরণ: <code>01712345678</code>", nil)
			return true
		}
		_, err := a.DB.Users.UpdateOne(ctx, bson.M{"user_id": uid}, bson.M{"$set": bson.M{"payment_numbers." + method: number}})
		if err != nil {
			a.sendText(ctx, b, chatID, "❌ নম্বরটি সেভ করা যায়নি। আবার চেষ্টা করুন।", nil)
			return true
		}
		if s.WithdrawID > 0 {
			deleteIncomingUserMessage(ctx, b, update)
			a.clearWorkflowPrompt(ctx, b, chatID)
			s.Number = number
			s.Step = ""
			walletSessions.Store(uid, s)
			_ = saveWorkflowSession(ctx, a.DB, "wallet", uid, s, 7*24*time.Hour)
			a.submitWalletWithdraw(ctx, b, uid, user.Username, s, method, number)
			return true
		}
		deleteIncomingUserMessage(ctx, b, update)
		walletSessions.Delete(uid)
		_ = deleteWorkflowSession(ctx, a.DB, "wallet", uid)
		a.editWorkflowPrompt(ctx, b, chatID, "✅ <b>"+htmlEscape(method)+" নম্বর সফলভাবে সেভ হয়েছে।</b>", MainMenu())
		return true
	}

	if s.Step == "withdraw_number" {
		if s.WithdrawID == 0 || s.Method == "" {
			walletSessions.Delete(uid)
			_ = deleteWorkflowSession(ctx, a.DB, "wallet", uid)
			a.sendText(ctx, b, chatID, "❌ কোনো সক্রিয় উইথড্র সেশন পাওয়া যায়নি। আবার শুরু করুন।", MainMenu())
			return true
		}
		number := strings.NewReplacer(" ", "", "-", "").Replace(text)
		if s.Method != "Recharge" {
			clean, ok := normalizeBDMobile(number)
			if !ok {
				deleteIncomingUserMessage(ctx, b, update)
				a.editWorkflowPrompt(ctx, b, chatID, "❌ <b>সঠিক নম্বর দিন।</b>\nউদাহরণ: <code>01712345678</code>", nil)
				return true
			}
			number = clean
		}
		if s.Method == "Recharge" {
			deleteIncomingUserMessage(ctx, b, update)
			s.Number, s.Step = number, "withdraw_operator"
			walletSessions.Store(uid, s)
			_ = saveWorkflowSession(ctx, a.DB, "wallet", uid, s, 7*24*time.Hour)
			a.editWorkflowPrompt(ctx, b, chatID, "📱 <b>Recharge Number:</b> <code>"+htmlEscape(number)+"</code>\n\n🔸 আপনার অপারেটরটি নির্বাচন করুন:", operatorMenu(s.WithdrawID))
			return true
		}
		deleteIncomingUserMessage(ctx, b, update)
		a.clearWorkflowPrompt(ctx, b, chatID)
		a.submitWalletWithdraw(ctx, b, uid, user.Username, s, s.Method, number)
		return true
	}

	if s.Step == "withdraw_qr" {
		if len(msg.Photo) == 0 {
			a.sendText(ctx, b, chatID, "❌ আপনার Bangla QR-এর পরিষ্কার ছবি আপলোড করুন।", nil)
			return true
		}
		fileID := msg.Photo[len(msg.Photo)-1].FileID
		deleteIncomingUserMessage(ctx, b, update)
		a.clearWorkflowPrompt(ctx, b, chatID)
		number := "Photo [File ID: " + fileID + "]"
		a.submitWalletWithdraw(ctx, b, uid, user.Username, s, "Bangla QR", number)
		return true
	}
	return false
}

func operatorMenu(id int64) *models.InlineKeyboardMarkup {
	operators := []string{"GP", "Skito", "Teletalk", "Robi", "Banglalink", "Circle"}
	buttons := [][]models.InlineKeyboardButton{}
	for i := 0; i < len(operators); i += 3 {
		row := []models.InlineKeyboardButton{}
		for j := i; j < i+3 && j < len(operators); j++ {
			row = append(row, models.InlineKeyboardButton{Text: operators[j], CallbackData: fmt.Sprintf("w_op:%s:%d", operators[j], id)})
		}
		buttons = append(buttons, row)
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: buttons}
}

func (a *BotApp) submitWalletWithdraw(ctx context.Context, b *bot.Bot, userID int64, username string, s WalletSession, method, number string) {
	requested, err := decimal.NewFromString(s.Amount)
	if err != nil {
		a.sendText(ctx, b, userID, "❌ Withdraw amount সঠিক নয়। আবার চেষ্টা করুন।", MainMenu())
		walletSessions.Delete(userID)
		_ = deleteWorkflowSession(ctx, a.DB, "wallet", userID)
		return
	}
	diff := decimal.Zero
	if s.WalletDiff != "" && s.WalletDiff != "0" {
		diff, _ = decimal.NewFromString(strings.TrimPrefix(s.WalletDiff, "+"))
	}
	reserved := requested.Sub(diff)
	if !reserved.IsPositive() {
		a.sendText(ctx, b, userID, "❌ Withdraw amount সঠিক নয়।", MainMenu())
		walletSessions.Delete(userID)
		_ = deleteWorkflowSession(ctx, a.DB, "wallet", userID)
		return
	}
	requestDoc := bson.M{
		"id": s.WithdrawID, "user_id": userID, "username": username, "amount": s.Final,
		"requested_amount": requested.String(), "method": method, "number": number,
		"wallet_diff": s.WalletDiff, "reserved_amount": reserved.String(), "balance_reserved": true,
		"status": "pending", "created_at": BangladeshTime(),
	}
	applied, err := ApplyWalletDeltaAnd(ctx, a.DB, userID, reserved.Neg(),
		fmt.Sprintf("withdraw:%d:reserve", s.WithdrawID), "withdraw_reserve", strconv.FormatInt(s.WithdrawID, 10),
		func(tx context.Context) error {
			_, insertErr := a.DB.WithdrawRequests.InsertOne(tx, requestDoc)
			return insertErr
		})
	if err != nil || !applied {
		a.sendText(ctx, b, userID, "❌ Withdraw request নিরাপদে তৈরি করা যায়নি। Wallet balance পরিবর্তন করা হয়নি। আবার চেষ্টা করুন।", MainMenu())
		walletSessions.Delete(userID)
		_ = deleteWorkflowSession(ctx, a.DB, "wallet", userID)
		return
	}
	methodDisplay := htmlEscape(method)
	numberDisplay := htmlEscape(number)
	if strings.HasPrefix(number, "Photo [File ID:") {
		numberDisplay = "🖼️ [Bangla QR Photo]"
	}
	adminText := fmt.Sprintf("🏧 <b>Wallet Withdraw Request <code>#%d</code>!</b>\n\n👤 User ID: <code>%d</code>\n🏷️ Username: <b>@%s</b>\n💰 Amount: ৳<b>%s BDT</b>\n🌐 Method: <b>%s</b>\n📱 Number: <code>%s</code>\n📌 Status: <code>pending</code>\n⏱️ Time: <i>%s</i>", s.WithdrawID, userID, htmlEscape(username), s.Final, methodDisplay, numberDisplay, BangladeshTime())
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✅ Paid", CallbackData: fmt.Sprintf("w_paid:%d", s.WithdrawID)}, {Text: "❌ Reject Request", CallbackData: fmt.Sprintf("w_reject:%d", s.WithdrawID)}}}}
	if fileID, ok := withdrawQRFileID(number); ok {
		_, _ = b.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: a.Config.AdminID, Photo: &models.InputFileString{Data: fileID}, Caption: adminText, ParseMode: models.ParseModeHTML, ReplyMarkup: markup})
	} else {
		a.sendText(ctx, b, a.Config.AdminID, adminText, markup)
	}
	a.sendText(ctx, b, a.Config.AdminID, "📊 Admin menu refreshed.", nil)
	walletSessions.Delete(userID)
	_ = deleteWorkflowSession(ctx, a.DB, "wallet", userID)
	if strings.HasPrefix(number, "Photo [File ID:") {
		a.sendPhotoOrText(ctx, b, userID, a.Config.PhotoWithdrawSubmitted, fmt.Sprintf("🏧 Withdraw ID: <code>#%d</code>\n💳 Payout: <b>%s</b>\n🖼️ Bangla QR photo received.\n\n✅ আপনার Withdraw request সফলভাবে জমা হয়েছে।\n⏳ অ্যাডমিন পেমেন্ট পাঠানো পর্যন্ত অপেক্ষা করুন।", s.WithdrawID, methodDisplay), MainMenu())
	} else {
		a.sendPhotoOrText(ctx, b, userID, a.Config.PhotoWithdrawSubmitted, fmt.Sprintf("🏧 Withdraw ID: <code>#%d</code>\n💳 Payout: <b>%s</b> (<code>%s</code>)\n\n✅ আপনার Withdraw request সফলভাবে জমা হয়েছে।\n⏳ অ্যাডমিন পেমেন্ট পাঠানো পর্যন্ত অপেক্ষা করুন।", s.WithdrawID, methodDisplay, numberDisplay), MainMenu())
	}
}

func (a *BotApp) WalletCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	if update == nil || update.CallbackQuery == nil {
		return false
	}
	q := update.CallbackQuery
	uid, data := q.From.ID, q.Data
	answer := func(message string, alert bool) {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: message, ShowAlert: alert})
	}

	switch {
	case data == "wallet_withdraw_menu":
		a.cancelAllSessions(ctx, uid)
		var user bson.M
		if err := a.DB.Users.FindOne(ctx, bson.M{"user_id": uid}).Decode(&user); err != nil {
			answer("Wallet পাওয়া যায়নি।", true)
			return true
		}
		balance, _ := decimal.NewFromString(strings.ReplaceAll(fmt.Sprint(user["mini_wallet"]), ",", ""))
		if balance.LessThan(decimal.NewFromInt(50)) {
			answer("❌ আপনার ওয়ালেটে ন্যূনতম ৫০ টাকা থাকতে হবে উইথড্র করার জন্য।", true)
			return true
		}
		walletSessions.Store(uid, WalletSession{Step: "withdraw_amount"})
		_ = saveWorkflowSession(ctx, a.DB, "wallet", uid, WalletSession{Step: "withdraw_amount"}, 7*24*time.Hour)
		answer("", false)
		a.editCallbackWorkflowPrompt(ctx, b, q, fmt.Sprintf("💸 <b>Wallet BDT Withdrawal</b>\n\n• বর্তমান ওয়ালেট ব্যালেন্স: ৳<b>%s</b>\n\n🔸 আপনি কত টাকা উইথড্র করতে চান তা সংখ্যায় লিখে পাঠান...", Money(balance)), nil)
		return true

	case data == "my_payment_numbers":
		answer("", false)
		a.showPaymentNumbers(ctx, b, uid)
		return true
	case data == "payment_numbers_close":
		answer("", false)
		return true
	case data == "cancel":
		session := a.walletSession(ctx, uid)
		if session.Step != "" {
			walletSessions.Delete(uid)
			_ = deleteWorkflowSession(ctx, a.DB, "wallet", uid)
			answer("Operation cancelled", false)
			a.sendText(ctx, b, uid, "🚫 <b>Operation Cancelled.</b>", MainMenu())
			return true
		}
		return false
	case data == "payment_number_cancel":
		walletSessions.Delete(uid)
		_ = deleteWorkflowSession(ctx, a.DB, "wallet", uid)
		answer("পরিবর্তন বাতিল করা হয়েছে।", false)
		a.showPaymentNumbers(ctx, b, uid)
		return true
	case strings.HasPrefix(data, "payment_number_edit:"):
		method := strings.TrimPrefix(data, "payment_number_edit:")
		if !isSavedNumberMethod(method) {
			answer("Payment method সঠিক নয়।", true)
			return true
		}
		walletSessions.Store(uid, WalletSession{Step: "payment_number", Method: method})
		_ = saveWorkflowSession(ctx, a.DB, "wallet", uid, WalletSession{Step: "payment_number", Method: method}, 7*24*time.Hour)
		answer("", false)
		a.editCallbackWorkflowPrompt(ctx, b, q, fmt.Sprintf("📱 <b>%s</b>-এর নম্বরটি পাঠান।\n\nউদাহরণ: <code>01712345678</code>\nনম্বরটি আপনার প্রোফাইলে সেভ হবে এবং Sell ও Withdrawal—দুই ক্ষেত্রেই ব্যবহার হবে।", method), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✖️ Cancel", CallbackData: "payment_number_cancel"}}}})
		return true
	case strings.HasPrefix(data, "payment_number_add_order:withdraw:"):
		parts := strings.Split(data, ":")
		if len(parts) != 4 {
			answer("অনুরোধটি সঠিক নয়।", true)
			return true
		}
		method := parts[2]
		id, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil || !isSavedNumberMethod(method) {
			answer("Payment method সঠিক নয়।", true)
			return true
		}
		s := a.walletSession(ctx, uid)
		s.Step, s.Method, s.WithdrawID = "payment_number", method, id
		walletSessions.Store(uid, s)
		_ = saveWorkflowSession(ctx, a.DB, "wallet", uid, s, 7*24*time.Hour)
		answer("", false)
		a.editCallbackWorkflowPrompt(ctx, b, q, fmt.Sprintf("📱 আপনার <b>%s</b> Number টি দিন\n\nউদাহরণ: <code>01712345678</code>", method), nil)
		return true
	case strings.HasPrefix(data, "wpayout:"):
		parts := strings.SplitN(strings.TrimPrefix(data, "wpayout:"), ":", 2)
		if len(parts) != 2 {
			answer("Invalid withdrawal selection", true)
			return true
		}
		method := parts[0]
		id, err := strconv.ParseInt(parts[1], 10, 64)
		s := a.walletSession(ctx, uid)
		if err != nil || s.Step != "choose_method" || s.WithdrawID != id {
			answer("সেশন মেয়াদোত্তীর্ণ হয়েছে। আবার চেষ্টা করুন।", true)
			return true
		}
		active, e := GetActivePayoutMethods(ctx, a.DB)
		allowed := false
		for _, m := range active {
			if m == method {
				allowed = true
			}
		}
		if e != nil || !allowed {
			answer("এই payment method বর্তমানে বন্ধ।", true)
			return true
		}
		requested, _ := decimal.NewFromString(s.Amount)
		fee := decimal.Zero
		if !strings.EqualFold(method, "Recharge") {
			fee = decimal.NewFromInt(5)
		}
		net := requested.Sub(fee)
		if net.IsNegative() {
			net = decimal.Zero
		}
		rounded, diff := roundDownTen(net)
		s.Method, s.Final, s.WalletDiff = method, Money(rounded), "0"
		if diff.IsPositive() {
			s.WalletDiff = "+" + diff.String()
		}
		walletSessions.Store(uid, s)
		_ = saveWorkflowSession(ctx, a.DB, "wallet", uid, s, 7*24*time.Hour)
		answer("", false)
		prompt := fmt.Sprintf("🏧 <b>Withdraw ID:</b> <code>#%d</code>\n🌐 <b>Method:</b> <b>%s</b>\n💰 <b>Requested:</b> ৳<code>%s</code>\nFee 5৳ : %s\n💵 <b>You will get:</b> <b>৳%s BDT</b>\n💳 <b>Wallet Adjustment:</b> <b>%s ৳</b>\n\n", id, htmlEscape(method), Money(requested), func() string {
			if fee.IsZero() {
				return "0৳"
			}
			return "-" + Money(fee) + "৳"
		}(), s.Final, s.WalletDiff)
		switch method {
		case "Recharge":
			s.Step = "withdraw_number"
			walletSessions.Store(uid, s)
			_ = saveWorkflowSession(ctx, a.DB, "wallet", uid, s, 7*24*time.Hour)
			a.editCallbackWorkflowPrompt(ctx, b, q, prompt+"🔸 আপনার Recharge নম্বরটি দিন:", nil)
		case "Bangla QR":
			s.Step = "withdraw_qr"
			walletSessions.Store(uid, s)
			_ = saveWorkflowSession(ctx, a.DB, "wallet", uid, s, 7*24*time.Hour)
			a.editCallbackWorkflowPrompt(ctx, b, q, prompt+"🔸 আপনার Bangla QR-এর পরিষ্কার ছবি আপলোড করুন:", nil)
		default:
			var user bson.M
			_ = a.DB.Users.FindOne(ctx, bson.M{"user_id": uid}).Decode(&user)
			numbers, _ := user["payment_numbers"].(bson.M)
			saved := strings.TrimSpace(fmt.Sprint(numbers[method]))
			if saved != "" && saved != "<nil>" {
				a.clearWorkflowPrompt(ctx, b, uid)
				a.submitWalletWithdraw(ctx, b, uid, q.From.Username, s, method, saved)
				return true
			}
			s.Step = "payment_number"
			walletSessions.Store(uid, s)
			_ = saveWorkflowSession(ctx, a.DB, "wallet", uid, s, 7*24*time.Hour)
			markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "➕ Add " + method, CallbackData: fmt.Sprintf("payment_number_add_order:withdraw:%s:%d", method, id)}}}}
			a.editCallbackWorkflowPrompt(ctx, b, q, prompt+fmt.Sprintf("⚠️ আপনার <b>%s</b> নম্বরটি প্রোফাইলে সেভ করা নেই।\nনিচের Add বাটনে চাপ দিয়ে নম্বরটি যুক্ত করুন:", method), markup)
		}
		return true
	case strings.HasPrefix(data, "w_op:"):
		parts := strings.Split(data, ":")
		if len(parts) != 3 {
			answer("Invalid operator", true)
			return true
		}
		allowedOperators := map[string]bool{"GP": true, "Skito": true, "Teletalk": true, "Robi": true, "Banglalink": true, "Circle": true}
		if !allowedOperators[parts[1]] {
			answer("Invalid recharge operator", true)
			return true
		}
		id, err := strconv.ParseInt(parts[2], 10, 64)
		s := a.walletSession(ctx, uid)
		if err != nil || s.Step != "withdraw_operator" || s.WithdrawID != id {
			answer("সেশন মেয়াদোত্তীর্ণ হয়েছে।", true)
			return true
		}
		answer("", false)
		a.clearWorkflowPrompt(ctx, b, uid)
		a.submitWalletWithdraw(ctx, b, uid, q.From.Username, s, s.Method+" ("+parts[1]+")", s.Number)
		return true
	case data == "user_wallet_history":
		answer("", false)
		a.showWalletHistory(ctx, b, uid)
		return true
	case strings.HasPrefix(data, "w_paid:"):
		isAdmin, err := IsAdmin(ctx, a.DB, a.Config, uid)
		if err != nil || !isAdmin {
			answer("এই কাজের অনুমতি নেই।", true)
			return true
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(data, "w_paid:"), 10, 64)
		if err != nil {
			answer("Invalid withdrawal ID", true)
			return true
		}
		var row bson.M
		if err := a.DB.WithdrawRequests.FindOne(ctx, bson.M{"id": id}).Decode(&row); err != nil {
			answer("উইথড্র রিকোয়েস্টটি পাওয়া যায়নি।", true)
			return true
		}
		userID := orderInt64(row, "user_id")
		legacyDebit := decimal.Zero
		if row["balance_reserved"] != true {
			requested, parseErr := decimal.NewFromString(orderString(row, "requested_amount", orderString(row, "amount", "0")))
			diff := decimal.Zero
			if raw := orderString(row, "wallet_diff", "0"); raw != "0" {
				diff, _ = decimal.NewFromString(strings.TrimPrefix(raw, "+"))
			}
			if parseErr != nil {
				answer("পুরোনো withdrawal amount যাচাই করা যায়নি।", true)
				return true
			}
			legacyDebit = requested.Sub(diff)
			if legacyDebit.IsNegative() {
				answer("Withdrawal amount invalid; payment mark করা হয়নি।", true)
				return true
			}
		}
		applied, txErr := ApplyWalletDeltaAnd(ctx, a.DB, userID, legacyDebit.Neg(),
			fmt.Sprintf("withdraw:%d:paid", id), "withdraw_paid", strconv.FormatInt(id, 10),
			func(tx context.Context) error {
				res, updateErr := a.DB.WithdrawRequests.UpdateOne(tx, bson.M{"id": id, "status": "pending"}, bson.M{"$set": bson.M{"status": "paid", "paid_at": BangladeshTime(), "paid_by": uid}})
				if updateErr != nil {
					return updateErr
				}
				if res.ModifiedCount != 1 {
					return errors.New("withdrawal is no longer pending")
				}
				return nil
			})
		if txErr != nil || !applied {
			answer("রিকোয়েস্টটি ইতিমধ্যে প্রসেস হয়েছে অথবা Wallet যাচাই ব্যর্থ হয়েছে।", true)
			return true
		}
		amount := orderString(row, "amount", "0")
		method := orderString(row, "method", "Wallet")
		answer("Withdraw marked as Paid", false)
		a.sendPhotoOrText(ctx, b, userID, a.Config.PhotoWithdrawSent, fmt.Sprintf("🏧 Withdraw ID <code>#%d</code>\n\n🎉 আপনার উইথড্র পেমেন্ট সফলভাবে পাঠানো হয়েছে!\n💵 Amount: ৳<b>%s</b>\n💳 Method: <b>%s</b>", id, amount, htmlEscape(method)), MainMenu())
		_ = LogAdminAction(ctx, a.DB, uid, q.From.Username, strconv.FormatInt(id, 10), "N/A", "Withdraw Paid", "0", amount)
		if strings.TrimSpace(a.Config.ChannelID) != "" {
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: a.Config.ChannelID, Text: fmt.Sprintf("✅ <b>Withdraw Status Update (Payment Sent)</b>\n\n🏧 Withdraw ID: <code>#%d</code>\n💵 Amount: ৳<code>%s</code>\n💳 Method: <b>%s</b>\n📌 Status: <code>Completed / Paid ✅</code>\n⏱️ Time: <i>%s</i>", id, amount, htmlEscape(method), BangladeshTime()), ParseMode: models.ParseModeHTML})
		}
		return true
	case strings.HasPrefix(data, "w_reject:"):
		isAdmin, err := IsAdmin(ctx, a.DB, a.Config, uid)
		if err != nil || !isAdmin {
			answer("এই কাজের অনুমতি নেই।", true)
			return true
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(data, "w_reject:"), 10, 64)
		if err != nil {
			answer("Invalid withdrawal ID", true)
			return true
		}
		withdrawRejectSessions.Store(uid, id)
		_ = saveWorkflowSession(ctx, a.DB, "withdraw_reject", uid, id, 24*time.Hour)
		answer("", false)
		a.sendText(ctx, b, uid, fmt.Sprintf("❌ <b>Withdraw Reject Reason Required</b>\n\nউইথড্র <code>#%d</code> রিজেক্ট করার কারণটি লিখে পাঠান:", id), nil)
		return true
	}
	return false
}

func withdrawQRFileID(value string) (string, bool) {
	const prefix = "Photo [File ID: "
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, "]") {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(value, prefix), "]")
	return id, id != ""
}

func isSavedNumberMethod(method string) bool {
	return method == "bKash" || method == "Nagad" || method == "Rocket" || method == "Upay"
}

func (a *BotApp) showWalletHistory(ctx context.Context, b *bot.Bot, userID int64) {
	type historyItem struct {
		when string
		line string
	}
	items := []historyItem{}
	wCursor, err := a.DB.WithdrawRequests.Find(ctx, bson.M{"user_id": userID}, options.Find().SetSort(bson.D{{Key: "id", Value: -1}}).SetLimit(20))
	if err != nil {
		a.sendText(ctx, b, userID, "❌ Wallet history লোড করা যায়নি।", MainMenu())
		return
	}
	for wCursor.Next(ctx) {
		var row bson.M
		if wCursor.Decode(&row) == nil {
			items = append(items, historyItem{orderString(row, "created_at", ""), fmt.Sprintf("• [Withdraw] ID: <code>#%d</code> | ৳%s (%s) | %s", orderInt64(row, "id"), orderString(row, "amount", "0"), htmlEscape(orderString(row, "method", "")), orderString(row, "status", "unknown"))})
		}
	}
	_ = wCursor.Close(ctx)
	oCursor, err := a.DB.Orders.Find(ctx, bson.M{"user_id": userID}, options.Find().SetSort(bson.D{{Key: "id", Value: -1}}).SetLimit(20))
	if err == nil {
		for oCursor.Next(ctx) {
			var row bson.M
			if oCursor.Decode(&row) != nil {
				continue
			}
			status, typ := orderString(row, "status", ""), orderString(row, "type", "")
			line := ""
			if typ == "admin_add" {
				line = fmt.Sprintf("• [Admin add] | ৳%s (wallet)", orderString(row, "total_bdt", "0"))
			} else if diff := orderString(row, "wallet_diff", "0"); diff != "0" && diff != "+0" {
				line = fmt.Sprintf("• [Order Adjust] ID: <code>#%d</code> | ৳%s (%s) | %s", orderInt64(row, "id"), diff, orderString(row, "network", "USDT"), status)
			} else if status == "paid" {
				payout := orderString(row, "payout", orderString(row, "network", "Wallet"))
				line = fmt.Sprintf("• [Order Paid] ID: <code>#%d</code> | ৳%s (%s) | paid", orderInt64(row, "id"), orderString(row, "total_bdt", "0"), htmlEscape(payout))
			}
			if line != "" {
				items = append(items, historyItem{orderString(row, "created_at", ""), line})
			}
		}
		_ = oCursor.Close(ctx)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].when > items[j].when })
	lines := []string{"📜 <b>Wallet History (Last 20)</b>", ""}
	for i, item := range items {
		if i >= 20 {
			break
		}
		lines = append(lines, item.line)
	}
	if len(lines) == 2 {
		lines = append(lines, "<i>কোনো লেনদেন হিস্ট্রি নেই।</i>")
	}
	a.sendText(ctx, b, userID, strings.Join(lines, "\n"), MainMenu())
}

func (a *BotApp) SendPendingWithdrawals(ctx context.Context, b *bot.Bot, chatID int64) {
	cursor, err := a.DB.WithdrawRequests.Find(ctx, bson.M{"status": "pending"}, options.Find().SetSort(bson.D{{Key: "id", Value: -1}}).SetLimit(50))
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Pending withdrawal লোড করা যায়নি।", nil)
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
		number := orderString(row, "number", "N/A")
		if strings.HasPrefix(number, "Photo [File ID:") {
			number = "🖼️ [Bangla QR Photo; File ID stored]"
		}
		body := fmt.Sprintf("🏧 <b>Withdraw Request <code>#%d</code></b>\n👤 User ID: <code>%d</code>\n🏷️ Username: <b>@%s</b>\n💰 Amount: ৳<b>%s BDT</b>\n🌐 Method: <b>%s</b>\n📱 Number/Address: <code>%s</code>\n📌 Status: <code>%s</code>\n⏱️ Time: <i>%s</i>", id, orderInt64(row, "user_id"), htmlEscape(orderString(row, "username", "N/A")), orderString(row, "amount", "0"), htmlEscape(orderString(row, "method", "N/A")), htmlEscape(number), orderString(row, "status", "pending"), orderString(row, "created_at", "N/A"))
		markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✅ Confirm Payment", CallbackData: fmt.Sprintf("w_paid:%d", id)}, {Text: "❌ Reject Request", CallbackData: fmt.Sprintf("w_reject:%d", id)}}}}
		if fileID, ok := withdrawQRFileID(orderString(row, "number", "")); ok {
			_, _ = b.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: chatID, Photo: &models.InputFileString{Data: fileID}, Caption: body, ParseMode: models.ParseModeHTML, ReplyMarkup: markup})
		} else {
			a.sendText(ctx, b, chatID, body, markup)
		}
	}
	if count == 0 {
		a.sendText(ctx, b, chatID, "কোনো পেন্ডিং উইথড্র রিকোয়েস্ট নেই।", nil)
	}
	po, pp, pw, ps, _ := a.AdminCounts(ctx)
	a.sendText(ctx, b, chatID, "📊 Admin Menu", AdminMenuWithCounts(chatID, a.Config.AdminID, po, pp, pw, ps))
}

func (a *BotApp) AdminWithdrawMessageHandler(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return false
	}
	uid := update.Message.From.ID
	value, ok := withdrawRejectSessions.Load(uid)
	if !ok {
		var restored int64
		if found, err := loadWorkflowSession(ctx, a.DB, "withdraw_reject", uid, &restored); err == nil && found {
			value, ok = restored, true
			withdrawRejectSessions.Store(uid, restored)
		}
	}
	if !ok {
		return false
	}
	reason := strings.TrimSpace(update.Message.Text)
	if reason == "" {
		a.sendText(ctx, b, update.Message.Chat.ID, "❌ অনুগ্রহ করে Reject Reason লিখে পাঠান।", nil)
		return true
	}
	id := value.(int64)
	var row bson.M
	if err := a.DB.WithdrawRequests.FindOne(ctx, bson.M{"id": id, "status": "pending"}).Decode(&row); err != nil {
		withdrawRejectSessions.Delete(uid)
		_ = deleteWorkflowSession(ctx, a.DB, "withdraw_reject", uid)
		a.sendText(ctx, b, update.Message.Chat.ID, "❌ উইথড্র রিকোয়েস্টটি পাওয়া যায়নি বা ইতিমধ্যে প্রসেস হয়েছে।", nil)
		return true
	}
	refund := decimal.Zero
	if row["balance_reserved"] == true {
		var parseErr error
		refund, parseErr = decimal.NewFromString(orderString(row, "reserved_amount", "0"))
		if parseErr != nil || refund.IsNegative() {
			withdrawRejectSessions.Delete(uid)
			_ = deleteWorkflowSession(ctx, a.DB, "withdraw_reject", uid)
			a.sendText(ctx, b, update.Message.Chat.ID, "❌ Reserved wallet amount যাচাই করা যায়নি; Admin-কে database check করতে হবে।", nil)
			return true
		}
	}
	userID := orderInt64(row, "user_id")
	applied, txErr := ApplyWalletDeltaAnd(ctx, a.DB, userID, refund,
		fmt.Sprintf("withdraw:%d:refund", id), "withdraw_rejected_refund", strconv.FormatInt(id, 10),
		func(tx context.Context) error {
			res, updateErr := a.DB.WithdrawRequests.UpdateOne(tx, bson.M{"id": id, "status": "pending"}, bson.M{"$set": bson.M{"status": "rejected", "reject_reason": reason, "rejected_at": BangladeshTime(), "rejected_by": uid}})
			if updateErr != nil {
				return updateErr
			}
			if res.ModifiedCount != 1 {
				return errors.New("withdrawal is no longer pending")
			}
			return nil
		})
	withdrawRejectSessions.Delete(uid)
	_ = deleteWorkflowSession(ctx, a.DB, "withdraw_reject", uid)
	if txErr != nil || !applied {
		a.sendText(ctx, b, update.Message.Chat.ID, "❌ Withdraw reject/refund নিরাপদে সম্পন্ন হয়নি। কোনো পরিবর্তন commit হয়নি।", nil)
		return true
	}
	a.sendText(ctx, b, userID, fmt.Sprintf("❌ <b>Withdraw Request Rejected!</b>\n\nআপনার উইথড্র রিকোয়েস্ট <code>#%d</code> বাতিল করা হয়েছে।\n💬 <b>কারণ:</b> %s", id, htmlEscape(reason)), MainMenu())
	_ = LogAdminAction(ctx, a.DB, uid, update.Message.From.Username, strconv.FormatInt(id, 10), "N/A", "Withdraw Rejected with Reason", "0", orderString(row, "amount", "0"))
	a.sendText(ctx, b, update.Message.Chat.ID, fmt.Sprintf("Withdraw <code>#%d</code> rejected with reason.", id), nil)
	return true
}

// ---- merged from wallet_atomic.go ----

var errWalletCASConflict = errors.New("wallet balance changed concurrently")

// ApplyWalletDeltaAnd atomically applies a wallet change, writes an idempotency
// ledger entry, and executes the related business-record mutation. It requires
// MongoDB transactions (replica set or sharded cluster) and fails closed if
// transactions are unavailable.
//
// operationID must be stable for the logical operation, e.g. "withdraw:123:reserve".
// A repeated operationID is a no-op and returns applied=false.
func ApplyWalletDeltaAnd(
	ctx context.Context,
	db *Database,
	userID int64,
	delta decimal.Decimal,
	operationID, reason, reference string,
	mutate func(context.Context) error,
) (applied bool, err error) {
	if db == nil || db.Client == nil || db.Users == nil || db.WalletLedger == nil {
		return false, errors.New("wallet transaction dependencies are not initialized")
	}
	if userID <= 0 || strings.TrimSpace(operationID) == "" {
		return false, errors.New("invalid wallet transaction identity")
	}

	for attempt := 0; attempt < 5; attempt++ {
		session, startErr := db.Client.StartSession()
		if startErr != nil {
			return false, fmt.Errorf("start wallet transaction: %w", startErr)
		}
		applied = false
		_, txErr := session.WithTransaction(ctx, func(sc mongo.SessionContext) (interface{}, error) {
			applied = false
			var existing bson.M
			err := db.WalletLedger.FindOne(sc, bson.M{"operation_id": operationID}).Decode(&existing)
			if err == nil {
				return nil, nil // already committed by an earlier delivery/retry
			}
			if err != mongo.ErrNoDocuments {
				return nil, err
			}

			if !delta.IsZero() {
				var user bson.M
				if err := db.Users.FindOne(sc, bson.M{"user_id": userID}).Decode(&user); err != nil {
					return nil, err
				}
				oldRaw, exists := user["mini_wallet"]
				oldText := "0"
				if exists && oldRaw != nil {
					oldText = strings.ReplaceAll(fmt.Sprint(oldRaw), ",", "")
				}
				current, parseErr := decimal.NewFromString(oldText)
				if parseErr != nil {
					return nil, fmt.Errorf("invalid mini_wallet for user %d: %w", userID, parseErr)
				}
				next := current.Add(delta)
				if next.IsNegative() {
					return nil, errors.New("insufficient wallet balance")
				}
				filter := bson.M{"user_id": userID}
				if exists {
					filter["mini_wallet"] = oldRaw
				} else {
					filter["mini_wallet"] = bson.M{"$exists": false}
				}
				res, updateErr := db.Users.UpdateOne(sc, filter, bson.M{"$set": bson.M{"mini_wallet": next.String(), "wallet_updated_at": time.Now().UTC()}})
				if updateErr != nil {
					return nil, updateErr
				}
				if res.ModifiedCount != 1 {
					return nil, errWalletCASConflict
				}
			}

			if mutate != nil {
				if err := mutate(sc); err != nil {
					return nil, err
				}
			}
			_, err = db.WalletLedger.InsertOne(sc, bson.M{
				"operation_id": operationID,
				"user_id":      userID,
				"delta":        delta.String(),
				"reason":       reason,
				"reference":    reference,
				"created_at":   time.Now().UTC(),
			})
			if err != nil {
				return nil, err
			}
			applied = true
			return nil, nil
		}, options.Transaction().SetReadConcern(readconcern.Snapshot()).SetWriteConcern(writeconcern.Majority()))
		session.EndSession(ctx)

		if errors.Is(txErr, errWalletCASConflict) {
			continue
		}
		if txErr != nil {
			return false, fmt.Errorf("wallet transaction %q failed (no balance change committed): %w", operationID, txErr)
		}
		return applied, nil
	}
	return false, errWalletCASConflict
}

// ---- merged from background_worker.go ----

// AutoWalletTransferWorker mirrors the Python worker: an order in
// approved_pending_payout is credited to the user's wallet after 3 hours.
// The order transition and wallet ledger credit are committed atomically, so
// overlapping workers or restarts cannot credit the same order twice.
func (a *BotApp) AutoWalletTransferWorker(ctx context.Context, b *bot.Bot) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		if err := a.processExpiredApprovedPayouts(ctx, b); err != nil && ctx.Err() == nil {
			log.Printf("auto wallet transfer worker: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *BotApp) processExpiredApprovedPayouts(ctx context.Context, b *bot.Bot) error {
	loc, err := time.LoadLocation("Asia/Dhaka")
	if err != nil {
		loc = time.FixedZone("Asia/Dhaka", 6*60*60)
	}
	cursor, err := a.DB.Orders.Find(ctx, bson.M{"status": "approved_pending_payout"})
	if err != nil {
		return err
	}
	defer cursor.Close(ctx)
	now := time.Now().In(loc)
	for cursor.Next(ctx) {
		var row bson.M
		if err := cursor.Decode(&row); err != nil {
			continue
		}
		approvedAt := orderString(row, "approved_at", orderString(row, "created_at", ""))
		approvedTime, parseErr := time.ParseInLocation("2006-01-02 03:04:05 PM", approvedAt, loc)
		if parseErr != nil || now.Sub(approvedTime) < 3*time.Hour {
			continue
		}
		id, userID := orderInt64(row, "id"), orderInt64(row, "user_id")
		amount, parseErr := decimal.NewFromString(strings.ReplaceAll(orderString(row, "total_bdt", "0"), ",", ""))
		if parseErr != nil || !amount.IsPositive() {
			log.Printf("invalid total_bdt on timed-out order #%d", id)
			continue
		}
		applied, txErr := ApplyWalletDeltaAnd(ctx, a.DB, userID, amount,
			fmt.Sprintf("order:%d:auto_wallet_credit", id), "order_timeout_auto_wallet_credit", strconv.FormatInt(id, 10),
			func(tx context.Context) error {
				res, updateErr := a.DB.Orders.UpdateOne(tx, bson.M{"id": id, "status": "approved_pending_payout"}, bson.M{"$set": bson.M{"status": "paid", "payout": "Auto Wallet 3h timeout", "paid_at": BangladeshTime(), "wallet_credited_at": time.Now().UTC()}})
				if updateErr != nil {
					return updateErr
				}
				if res.ModifiedCount != 1 {
					return fmt.Errorf("order #%d is no longer eligible for auto wallet transfer", id)
				}
				return nil
			})
		if txErr != nil || !applied {
			if txErr != nil {
				log.Printf("atomic auto wallet transfer failed for order #%d: %v", id, txErr)
			}
			continue
		}
		a.sendText(ctx, b, userID, "⏳ <b>Order Timeout Notification</b>\n\nঅর্ডার <code>#"+strconv.FormatInt(id, 10)+"</code> অ্যাডমিন কর্তৃক অনুমোদনের পর নির্ধারিত ৩ ঘণ্টার মধ্যে আপনি পেমেন্ট মেথড ও নম্বর প্রদান করেননি।\nতাই মোট ৳<b>"+Money(amount)+"</b> স্বয়ংক্রিয়ভাবে আপনার 💳 <b>Wallet</b>-এ যুক্ত করা হয়েছে।", nil)
		log.Printf("auto credited ৳%s to user %d for timed-out order #%d", amount.String(), userID, id)
	}
	return cursor.Err()
}
