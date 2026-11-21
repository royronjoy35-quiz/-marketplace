package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/shopspring/decimal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- merged from user_handlers.go ----

// BotApp groups the runtime dependencies used by Telegram update handlers.
type BotApp struct {
	Config Config
	DB     *Database
}

func messageChatID(update *models.Update) (int64, bool) {
	if update == nil || update.Message == nil {
		return 0, false
	}
	return update.Message.Chat.ID, true
}

func telegramFullName(user *models.User) string {
	if user == nil {
		return ""
	}
	name := strings.TrimSpace(user.FirstName + " " + user.LastName)
	return name
}

func (a *BotApp) sendText(ctx context.Context, b *bot.Bot, chatID int64, text string, markup models.ReplyMarkup) {
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: markup,
	})
}

// StartHandler ports the supplied Python start() workflow, including referral capture,
// first-time user creation, ban checks, and the distinct administrator dashboard.
func (a *BotApp) StartHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID, ok := messageChatID(update)
	if !ok || update.Message.From == nil {
		return
	}
	user := update.Message.From
	banned, err := IsUserBanned(ctx, a.DB, user.ID)
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ সাময়িক সমস্যা হয়েছে। কিছুক্ষণ পর আবার চেষ্টা করুন।", nil)
		return
	}
	if banned {
		a.sendText(ctx, b, chatID, "❌ আপনি এই বট থেকে ব্যান (Banned) হয়েছেন।", nil)
		return
	}

	var refID int64
	parts := strings.Fields(update.Message.Text)
	if len(parts) > 1 {
		if _, scanErr := fmt.Sscan(parts[1], &refID); scanErr != nil || refID == user.ID || refID < 0 {
			refID = 0
		}
	}

	var existing bson.M
	err = a.DB.Users.FindOne(ctx, bson.M{"user_id": user.ID}).Decode(&existing)
	if err == mongo.ErrNoDocuments {
		_, err = a.DB.Users.InsertOne(ctx, bson.M{
			"user_id":            user.ID,
			"username":           user.Username,
			"full_name":          telegramFullName(user),
			"referred_by":        refID,
			"referral_bonus_bdt": "0",
			"mini_wallet":        "0",
			"withdraw_requested": false,
			"valid_referrals":    0,
			"is_banned":          false,
			"created_at":         BangladeshTime(),
		})
		if err != nil {
			a.sendText(ctx, b, chatID, "❌ আপনার অ্যাকাউন্ট তৈরি করা যায়নি। আবার /start দিন।", nil)
			return
		}
	} else if err != nil {
		a.sendText(ctx, b, chatID, "❌ ডেটাবেসে সমস্যা হয়েছে। কিছুক্ষণ পর আবার চেষ্টা করুন।", nil)
		return
	}

	isAdmin, err := IsAdmin(ctx, a.DB, a.Config, user.ID)
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ অ্যাডমিন তথ্য যাচাই করা যায়নি।", nil)
		return
	}
	if isAdmin {
		po, pp, pw, ps, countErr := a.AdminCounts(ctx)
		if countErr != nil {
			a.sendText(ctx, b, chatID, "🔐 <b>Administrator Dashboard</b>", AdminMenuWithCounts(user.ID, a.Config.AdminID, 0, 0, 0, 0))
			return
		}
		a.sendText(ctx, b, chatID, "🔐 <b>Administrator Dashboard</b>", AdminMenuWithCounts(user.ID, a.Config.AdminID, po, pp, pw, ps))
		return
	}

	minAmount, maxAmount, err := GetMinMaxLimits(ctx, a.DB)
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ লেনদেনের সীমা লোড করা যায়নি।", MainMenu())
		return
	}
	welcome := fmt.Sprintf(
		"🇧🇩 <b>Welcome to DR Sell Bot</b>\n\n"+
			"👋 <b>আমাদের প্ল্যাটফর্মে আপনাকে স্বাগতম!</b>\n"+
			"💎 সহজে ও নিরাপদে USDT বিক্রি করুন এবং BDT গ্রহণ করুন।\n\n"+
			"💰 <b>Transaction Limits</b>\n"+
			"🔹 Minimum: <code>%s USDT</code>\n"+
			"🔹 Maximum: <code>%s USDT</code>\n\n"+
			"💳 <b>Payment Fees</b>\n"+
			"🔹 Recharge ব্যতীত অন্যান্য পেমেন্ট মেথডে <b>Fee 5৳</b> (প্রতি লেনদেনের জন্য fixed)।\n\n"+
			"🔐 <b>Security & Verification</b>\n"+
			"🔹 প্রতিটি লেনদেন নিরাপত্তার স্বার্থে ম্যানুয়ালি যাচাই করা হয়।\n"+
			"🔹 Round Figure Amount হিসাবের কারণে <b>1৳</b> পার্থক্য হতে পারে।\n\n"+
			"🆘 পেমেন্ট-সংক্রান্ত সহায়তার জন্য <b>⚠️ Payment Support</b> অপশনটি ব্যবহার করুন।\n\n"+
			"🔰 <b>ⒹⓇ sell bot</b>\nFast • Secure • Reliable",
		minAmount.String(), maxAmount.String(),
	)
	a.sendText(ctx, b, chatID, welcome, MainMenu())
}

// InfoHandler ports info_command(): live limits, current rate text and enabled payout methods.
func (a *BotApp) InfoHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID, ok := messageChatID(update)
	if !ok {
		return
	}
	minAmount, maxAmount, err := GetMinMaxLimits(ctx, a.DB)
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ তথ্য লোড করা যায়নি।", MainMenu())
		return
	}
	methods, err := GetActivePayoutMethods(ctx, a.DB)
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ পেমেন্ট মেথড লোড করা যায়নি।", MainMenu())
		return
	}
	rateText, err := RatesText(ctx, a.DB)
	if err != nil {
		rateText = "বর্তমান রেট পাওয়া যায়নি।"
	}
	methodLines := "• কোনো মেথড সচল নেই"
	if len(methods) > 0 {
		lines := make([]string, 0, len(methods))
		for _, method := range methods {
			lines = append(lines, "• <b>"+method+"</b>")
		}
		methodLines = strings.Join(lines, "\n")
	}
	text := fmt.Sprintf(
		"ℹ️ <b>Platform Information & Transaction Limits</b>\n\n"+
			"🔸 <b>USDT Limits :</b> <code>%s USDT</code> থেকে <code>%s USDT</code>\n\n"+
			"📈 <b>Current Exchange Rates</b>\n%s\n\n"+
			"📥 <b>Available Payment Methods</b>\n%s\n"+
			"• পেমেন্ট-সংক্রান্ত সহায়তার জন্য Help & Support মেনু ব্যবহার করুন।",
		minAmount.String(), maxAmount.String(), rateText, methodLines,
	)
	a.sendText(ctx, b, chatID, text, MainMenu())
}

// AdminCounts mirrors the four status counts used by get_admin_counts() in Python.
func (a *BotApp) AdminCounts(ctx context.Context) (pendingOrders, pendingPayments, pendingWithdraws, pendingSupport int64, err error) {
	pendingOrders, err = a.DB.Orders.CountDocuments(ctx, bson.M{"status": "pending_verification"})
	if err != nil {
		return
	}
	pendingPayments, err = a.DB.Orders.CountDocuments(ctx, bson.M{"status": "approved_pending_payment"})
	if err != nil {
		return
	}
	pendingWithdraws, err = a.DB.WithdrawRequests.CountDocuments(ctx, bson.M{"status": "pending"})
	if err != nil {
		return
	}
	pendingSupport, err = a.DB.Tickets.CountDocuments(ctx, bson.M{"status": "open"})
	return
}

// DailyLimitHandler is the permanent-admin-only /dailylimit command.
func (a *BotApp) DailyLimitHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID, ok := messageChatID(update)
	if !ok || update.Message.From == nil {
		return
	}
	if update.Message.From.ID != a.Config.AdminID {
		a.sendText(ctx, b, chatID, "❌ এই কমান্ডটি শুধুমাত্র পার্মানেন্ট অ্যাডমিনের জন্য!", nil)
		return
	}
	current, err := GetDailyLimit(ctx, a.DB)
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ বর্তমান লিমিট পড়া যায়নি।", nil)
		return
	}
	parts := strings.Fields(update.Message.Text)
	if len(parts) < 2 {
		a.sendText(ctx, b, chatID, fmt.Sprintf("📅 <b>Daily Amount Limit Settings</b>\n\nবর্তমানে দৈনিক লিমিট সেট আছে: <b>%s USDT</b>\n\n🔸 নতুন লিমিট সেট করতে এভাবে লিখুন:\n<code>/dailylimit 100</code>", current), nil)
		return
	}
	newValue := strings.TrimSpace(parts[1])
	if _, parseErr := decimalFromPositive(newValue); parseErr != nil {
		a.sendText(ctx, b, chatID, "❌ সঠিক সংখ্যা দিন! যেমন: <code>/dailylimit 10000</code>", nil)
		return
	}
	_, err = a.DB.Settings.UpdateOne(ctx, bson.M{"setting_key": "daily_amount_limit"}, bson.M{"$set": bson.M{"setting_value": newValue}}, options.Update().SetUpsert(true))
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ লিমিট আপডেট করা যায়নি।", nil)
		return
	}
	username := update.Message.From.Username
	if username == "" {
		username = telegramFullName(update.Message.From)
	}
	_ = LogAdminAction(ctx, a.DB, update.Message.From.ID, username, "N/A", "N/A", "Change Daily Limit", "0", newValue)
	a.sendText(ctx, b, chatID, fmt.Sprintf("✅ সফলভাবে দৈনিক লিমিট আপডেট হয়েছে!\n• নতুন Daily Amount Limit: <b>%s USDT</b>", newValue), nil)
}

func decimalFromPositive(value string) (string, error) {
	// Decimal validation is delegated to the existing shopspring/decimal dependency.
	v, err := decimal.NewFromString(value)
	if err != nil || !v.IsPositive() {
		return "", fmt.Errorf("value must be positive")
	}
	return v.String(), nil
}

// RegisterCoreUserHandlers registers the first migrated command set.
func (a *BotApp) RegisterCoreUserHandlers(b *bot.Bot) {
	b.RegisterHandler(bot.HandlerTypeMessageText, "start", bot.MatchTypeCommand, a.StartHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "info", bot.MatchTypeCommand, a.InfoHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "dailylimit", bot.MatchTypeCommand, a.DailyLimitHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "today", bot.MatchTypeCommand, a.TodayReportHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "panel", bot.MatchTypeCommand, a.PanelHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "calc", bot.MatchTypeCommand, a.CalculatorCommandHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "cancel", bot.MatchTypeCommand, a.CancelHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "leaderboard", bot.MatchTypeCommand, func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update != nil && update.Message != nil {
			a.Leaderboard(ctx, b, update.Message.From.ID)
		}
	})
	b.RegisterHandler(bot.HandlerTypeMessageText, "users", bot.MatchTypeCommand, func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update != nil && update.Message != nil {
			a.AdminUsers(ctx, b, update.Message.Chat.ID)
		}
	})
	b.RegisterHandler(bot.HandlerTypeMessageText, "ban", bot.MatchTypeCommand, func(ctx context.Context, b *bot.Bot, update *models.Update) { a.AdminBanCommand(ctx, b, update, true) })
	b.RegisterHandler(bot.HandlerTypeMessageText, "unban", bot.MatchTypeCommand, func(ctx context.Context, b *bot.Bot, update *models.Update) { a.AdminBanCommand(ctx, b, update, false) })
	b.RegisterHandler(bot.HandlerTypeMessageText, "profile", bot.MatchTypeCommand, func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update != nil && update.Message != nil {
			a.Profile(ctx, b, update.Message.From)
		}
	})
	b.RegisterHandler(bot.HandlerTypeMessageText, "", bot.MatchTypePrefix, a.MessageHandler)
	b.RegisterHandler(bot.HandlerTypePhotoCaption, "", bot.MatchTypePrefix, a.MessageHandler)
	b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		return update != nil && update.Message != nil && len(update.Message.Photo) > 0
	}, a.MessageHandler)
	b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		return update != nil && update.Message != nil && update.Message.Video != nil
	}, a.MessageHandler)
	b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		return update != nil && update.Message != nil && update.Message.Document != nil
	}, a.MessageHandler)
	b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		return update != nil && update.Message != nil && hasAudioOrVoice(update.Message)
	}, a.MessageHandler)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "", bot.MatchTypePrefix, a.CallbackHandler)
}

// ---- merged from user_menu_handlers.go ----

// userMenuMessage dispatches reply-keyboard actions before conversation input,
// matching the supplied Python handler's menu-first routing order.
func (a *BotApp) userMenuMessage(ctx context.Context, b *bot.Bot, update *models.Update, text string) bool {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return false
	}
	user := update.Message.From
	uid, chatID := user.ID, update.Message.Chat.ID
	switch text {
	case "🏠 Back to Main Menu":
		a.cancelAllSessions(ctx, uid)
		a.sendText(ctx, b, chatID, "🏠 আপনি মূল মেনুতে ফিরে এসেছেন...", MainMenu())
		return true
	case "📈 Live Rates":
		rateText, err := RatesText(ctx, a.DB)
		if err != nil {
			a.sendText(ctx, b, chatID, "❌ Live Rates লোড করা যায়নি।", MainMenu())
		} else {
			a.sendText(ctx, b, chatID, rateText, MainMenu())
		}
		return true
	case "📥 Payment Methods":
		methods, err := GetActivePayoutMethods(ctx, a.DB)
		if err != nil {
			a.sendText(ctx, b, chatID, "❌ Payment Methods লোড করা যায়নি।", MainMenu())
			return true
		}
		lines := "দুঃখিত, বর্তমানে কোনো পেমেন্ট মেথড সচল নেই।"
		if len(methods) > 0 {
			parts := make([]string, 0, len(methods))
			for _, method := range methods {
				parts = append(parts, "• <b>"+htmlEscape(method)+"</b>")
			}
			lines = strings.Join(parts, "\n")
		}
		a.sendText(ctx, b, chatID, "📥 <b>বর্তমানে সচল পেমেন্ট মেথডসমূহ:</b>\n\n"+lines+"\n\n🔸 আপনি এই মাধ্যমগুলোর মাধ্যমে টাকা গ্রহণ করতে পারবেন।", MainMenu())
		return true
	case "💸 Live Transactions":
		markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "💸 Live Transactions Channel", URL: a.Config.ChannelInviteLink}}}}
		a.sendText(ctx, b, chatID, "অফিশিয়াল লাইভ ট্রানজ্যাকশন আপডেট দেখতে নিচের বাটনে ক্লিক করুন।", markup)
		return true
	case "❓ Help Center (FAQ)":
		markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "🤖 কীভাবে Bot এর Support কাজ করে?", URL: a.Config.FAQHowBotWorksLink}},
			{{Text: "⏱️ টাকা পেতে কত সময় লাগে?", URL: a.Config.FAQPaymentTimeLink}},
			{{Text: "💡 কীভাবে sell দিতে হয়?", URL: a.Config.FAQHowToSellLink}},
			{{Text: "📌 Payment prove TxID/Order id কী?", URL: a.Config.FAQPaymentProofLink}},
			{{Text: "🔙 Close Help Center", CallbackData: "close_faq"}},
		}}
		a.sendText(ctx, b, chatID, "❓ <b>Help Center — Frequently Asked Questions</b>\n\nআপনার প্রশ্নের উত্তর জানতে নিচের প্রাসঙ্গিক বিষয়টি নির্বাচন করুন।", markup)
		return true
	case "📜 Rules & Information":
		a.sendRules(ctx, b, chatID)
		return true
	case "💳 My Wallet":
		a.showWallet(ctx, b, uid, chatID)
		return true
	case "💰 Sell USDT":
		a.cancelAllSessions(ctx, uid)
		min, max, err := GetMinMaxLimits(ctx, a.DB)
		if err != nil {
			a.sendText(ctx, b, chatID, "❌ Sell limit লোড করা যায়নি।", MainMenu())
			return true
		}
		session := &SellSession{Step: "amount"}
		sellSessions.Store(uid, session)
		_ = saveWorkflowSession(ctx, a.DB, "sell", uid, session, 7*24*time.Hour)
		a.sendText(ctx, b, chatID, fmt.Sprintf("🔸 আপনি কত পরিমাণ USDT বিক্রি করতে চান তা লিখুন।\n\nMinimum: <code>%s USDT</code> | Max: <code>%s USDT</code>", min, max), MainMenu())
		return true
	case "📦 Order History":
		a.OrderHistory(ctx, b, uid)
		return true
	case "👥 Referral Program":
		a.ReferralDashboard(ctx, b, uid)
		return true
	case "👤 My Profile":
		a.Profile(ctx, b, user)
		return true
	case "🏆 Leaderboard":
		a.Leaderboard(ctx, b, uid)
		return true
	case "📞 Help & Support":
		a.sendText(ctx, b, chatID, "📞 <b>Help & Support Center</b>\n\nআপনার প্রয়োজন অনুযায়ী নিচের একটি অপশন নির্বাচন করুন।", SupportMenu())
		return true
	case "🧮 Rate Calculator":
		a.CalculatorCommandHandler(ctx, b, update)
		return true
	}
	return false
}

func (a *BotApp) sendRules(ctx context.Context, b *bot.Bot, chatID int64) {
	min, max, err := GetMinMaxLimits(ctx, a.DB)
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Rules লোড করা যায়নি।", SupportMenu())
		return
	}
	first, life := decimal.NewFromFloat(.05), decimal.NewFromFloat(.005)
	if raw, ok, e := GetSetting(ctx, a.DB, "ref_percentages"); e == nil && ok {
		var rates map[string]string
		if json.Unmarshal([]byte(raw), &rates) == nil {
			if v, e := decimal.NewFromString(rates["first_sale"]); e == nil {
				first = v
			}
			if v, e := decimal.NewFromString(rates["lifetime"]); e == nil {
				life = v
			}
		}
	}
	firstText := first.Mul(decimal.NewFromInt(100)).String() + "%"
	lifeText := life.Mul(decimal.NewFromInt(100)).String() + "%"
	text := fmt.Sprintf("📜 <b>ⒹⓇ sell bot</b> 📜\n\n🌟 <b>সার্ভিস নীতিমালা ও শর্তাবলী</b> 🌟\n\n<i>আমাদের বটের মাধ্যমে নিরাপদ ও স্বাচ্ছন্দ্যে USDT বিক্রি করতে নিচের নিয়ম ও শর্তাবলী মনোযোগ দিয়ে পড়ে নিন।</i>\n\n━━━━━━━━━━━━━━━━━━\n💰 <b>১. রেট ও লেনদেনের সীমা</b>\n<i>(Limits &amp; Rates)</i>\n\n🔹 সর্বনিম্ন লেনদেন: <code>$%s USDT</code>\n🔹 সর্বোচ্চ লেনদেন: <code>$%s USDT</code>\n🔹 সর্বশেষ রেট জানতে Live Rates মেনু দেখুন।\n\n━━━━━━━━━━━━━━━━━━\n💳 <b>২. পেমেন্ট পদ্ধতি ও নেটওয়ার্ক</b>\n🔹 Binance Pay, Bybit Pay এবং BEP-20 সাপোর্ট করা হয়।\n🔹 বট থেকে প্রদত্ত QR Code অথবা Address-এই পেমেন্ট পাঠাবেন।\n⚠️ ভুল Network অথবা Address-এ পেমেন্ট পাঠালে কর্তৃপক্ষ দায়ী থাকবে না।\n\n━━━━━━━━━━━━━━━━━━\n🏦 <b>৩. টাকা গ্রহণ ও পেমেন্ট সময়সীমা</b>\n🔹 bKash/Nagad/Rocket/Upay/Recharge ইত্যাদির মাধ্যমে টাকা গ্রহণ করা যাবে।\n⏱️ সঠিক Personal নম্বর হলে সাধারণত ৫–২০ মিনিটের মধ্যে পেমেন্ট সম্পন্ন করা হয়।\n🔸 Recharge ব্যতীত অন্যান্য মেথডে fixed Fee 5৳।\n🔸 BDT পেমেন্টে কোনো দশমিক বা পয়সা দেওয়া হবে না।\n\n━━━━━━━━━━━━━━━━━━\n🧾 <b>৪. TxID ও পেমেন্ট প্রমাণ</b>\n🔹 পেমেন্টের পর সঠিক TxID/Order No. অথবা স্পষ্ট Screenshot জমা দিতে হবে।\n🚫 Fake অথবা পূর্বে ব্যবহৃত TxID/প্রমাণ জমা দেওয়া নিষিদ্ধ।\n\n━━━━━━━━━━━━━━━━━━\n🎁 <b>৫. রেফারেল বোনাস নীতি</b>\n💎 প্রথম সফল সেলে এককালীন বোনাস: <b>%s</b>।\n💎 পরবর্তী সফল লেনদেনে কমিশন: <b>%s</b>।\n📌 Referral balance আগে Wallet-এ transfer করতে হবে।\n🚫 Fake Account তৈরি করে রেফারেল বাড়ানো নিষিদ্ধ।\n\n━━━━━━━━━━━━━━━━━━\n🗨️ <b>৬. সহায়তা ও কাস্টমার সাপোর্ট</b>\nপেমেন্ট অথবা অর্ডার সমস্যায় Payment Support ব্যবহার করুন।\n\n━━━━━━━━━━━━━━━━━━\n⚠️ <b>বিশেষ সতর্কতা</b>\n🔸 Round Figure হিসাবের কারণে 1৳ পার্থক্য হতে পারে।\n🔸 নিরাপত্তার স্বার্থে সব লেনদেন অফিসিয়াল বটের মাধ্যমেই সম্পন্ন করুন।\n\n🤝 <b>ⒹⓇ sell bot</b>-এর সাথে থাকার জন্য ধন্যবাদ!", min, max, firstText, lifeText)
	a.sendText(ctx, b, chatID, text, SupportMenu())
}

// ---- merged from order_handlers.go ----

// Sell workflow state is cached in-process and persisted in MongoDB for restart recovery.
type SellSession struct {
	Step         string
	Amount       string
	Rate         string
	BDT          string
	RefBonusUsed string
	TotalBDT     string
	Network      string
	Address      string
}

var sellSessions sync.Map // map[int64]*SellSession, keyed by Telegram user ID

func (a *BotApp) clearSession(ctx context.Context, userID int64) {
	sellSessions.Delete(userID)
	if err := deleteWorkflowSession(ctx, a.DB, "sell", userID); err != nil {
		log.Printf("delete sell session for %d: %v", userID, err)
	}
}

func (a *BotApp) MessageHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return
	}
	msg := update.Message
	user := msg.From
	chatID := msg.Chat.ID
	if a.AdminMessageHandler(ctx, b, update) {
		return
	}
	banned, err := IsUserBanned(ctx, a.DB, user.ID)
	if err != nil || banned {
		if banned {
			a.sendText(ctx, b, chatID, "❌ আপনি এই বট থেকে ব্যান (Banned) হয়েছেন।", nil)
		} else {
			a.sendText(ctx, b, chatID, "❌ সাময়িক সমস্যা হয়েছে। আবার চেষ্টা করুন।", nil)
		}
		return
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		text = strings.TrimSpace(msg.Caption)
	}
	if a.userMenuMessage(ctx, b, update, text) {
		return
	}
	if a.SupportMessageHandler(ctx, b, update) {
		return
	}
	if a.handlePayoutNumberMessage(ctx, b, update) {
		return
	}
	if a.WalletMessageHandler(ctx, b, update) {
		return
	}
	if a.handleCalculatorInput(ctx, b, update, text) {
		return
	}
	if text == "🏆 Leaderboard" {
		a.Leaderboard(ctx, b, user.ID)
		return
	}
	if text == "📦 Order History" {
		a.OrderHistory(ctx, b, user.ID)
		return
	}
	if text == "👥 Referral Program" {
		a.ReferralDashboard(ctx, b, user.ID)
		return
	}
	if text == "👤 My Profile" {
		a.Profile(ctx, b, user)
		return
	}
	if text == "📞 Help & Support" {
		a.sendText(ctx, b, chatID, "📞 <b>Help & Support Center</b>\n\nআপনার প্রয়োজন অনুযায়ী নিচের একটি অপশন নির্বাচন করুন।", SupportMenu())
		return
	}
	if text == "💰 Sell USDT" {
		minAmount, maxAmount, e := GetMinMaxLimits(ctx, a.DB)
		if e != nil {
			a.sendText(ctx, b, chatID, "❌ Sell limit লোড করা যায়নি।", MainMenu())
			return
		}
		sellSessions.Store(user.ID, &SellSession{Step: "amount"})
		_ = saveWorkflowSession(ctx, a.DB, "sell", user.ID, &SellSession{Step: "amount"}, 7*24*time.Hour)
		a.sendText(ctx, b, chatID, fmt.Sprintf("🔸 আপনি কত পরিমাণ USDT বিক্রি করতে চান তা লিখুন।\n\nMinimum: <code>%s USDT</code> | Max: <code>%s USDT</code>", minAmount, maxAmount), MainMenu())
		return
	}

	sessionValue, exists := sellSessions.Load(user.ID)
	if !exists {
		var restored SellSession
		if ok, err := loadWorkflowSession(ctx, a.DB, "sell", user.ID, &restored); err == nil && ok {
			sessionValue, exists = &restored, true
			sellSessions.Store(user.ID, &restored)
		}
	}
	if !exists {
		return
	}
	session, ok := sessionValue.(*SellSession)
	if !ok {
		a.clearSession(ctx, user.ID)
		return
	}

	switch session.Step {
	case "amount":
		a.handleSellAmount(ctx, b, update, session, text)
	case "txid":
		a.handleSellProof(ctx, b, update, session, text)
	}
}

func (a *BotApp) handleSellAmount(ctx context.Context, b *bot.Bot, update *models.Update, s *SellSession, text string) {
	msg := update.Message
	userID, chatID := msg.From.ID, msg.Chat.ID
	amount, err := decimal.NewFromString(text)
	if err != nil || !amount.IsPositive() {
		a.sendText(ctx, b, chatID, "😡 একটি সঠিক USDT amount দিন।", nil)
		return
	}
	minAmount, maxAmount, err := GetMinMaxLimits(ctx, a.DB)
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Sell limit যাচাই করা যাচ্ছে না।", nil)
		return
	}
	if amount.LessThan(minAmount) || amount.GreaterThan(maxAmount) {
		a.sendText(ctx, b, chatID, fmt.Sprintf("🔸 USDT amount অবশ্যই <code>%s</code> থেকে <code>%s</code> এর মধ্যে হতে হবে।", minAmount, maxAmount), nil)
		return
	}

	dailyLimit, err := GetDailyLimit(ctx, a.DB)
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Daily limit যাচাই করা যাচ্ছে না।", nil)
		return
	}
	today := time.Now().In(dhakaLocation).Format("2006-01-02")
	cursor, err := a.DB.Orders.Find(ctx, bson.M{"created_at": bson.M{"$regex": "^" + regexp.QuoteMeta(today)}, "status": "paid", "type": bson.M{"$ne": "admin_add"}})
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Daily sell history লোড করা যায়নি।", nil)
		return
	}
	totalToday := decimal.Zero
	for cursor.Next(ctx) {
		var row bson.M
		if cursor.Decode(&row) == nil {
			if value, e := decimal.NewFromString(fmt.Sprint(row["amount"])); e == nil {
				totalToday = totalToday.Add(value)
			}
		}
	}
	_ = cursor.Close(ctx)
	remaining := dailyLimit.Sub(totalToday)
	if remaining.IsNegative() {
		remaining = decimal.Zero
	}
	var userDoc bson.M
	_ = a.DB.Users.FindOne(ctx, bson.M{"user_id": userID}).Decode(&userDoc)
	extraAllowed := decimal.Zero
	if userDoc != nil {
		if v, e := decimal.NewFromString(fmt.Sprint(userDoc["allowed_extra_limit_"+today])); e == nil {
			extraAllowed = v
		}
	}
	if amount.GreaterThan(remaining) && amount.GreaterThan(extraAllowed) {
		markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "📨 অ্যাডমিনকে লিমিট বাড়ানোর অনুরোধ পাঠান", CallbackData: "req_limit_boost:" + strconv.FormatInt(userID, 10) + ":" + amount.String()}}}}
		a.sendText(ctx, b, chatID, fmt.Sprintf("⚠️ <b>Admin এর দৈনিক লিমিট সংক্রান্ত বার্তা!</b>\n\n• আজকের দৈনিক লিমিট: <b>%s USDT</b>\n• ইতিমধ্যেই বিক্রি হয়েছে: <b>%s USDT</b>\n• বাকি আছে: <b>%s USDT</b>\n\nআপনি সেল করতে চাচ্ছেন: <b>%s USDT</b>\n\n❌ আপনি সর্বোচ্চ <b>%s USDT</b> sell করতে পারবেন অথবা অ্যাডমিনকে লিমিট বাড়ানোর অনুরোধ পাঠান।", dailyLimit, totalToday, remaining, amount, remaining), markup)
		return
	}

	tiers, err := GetRateTiers(ctx, a.DB)
	if err != nil || len(tiers) == 0 {
		a.sendText(ctx, b, chatID, "🚫 এই amount-এর জন্য কোনো rate নির্ধারিত নেই।", nil)
		return
	}
	var rate decimal.Decimal
	for _, tier := range tiers {
		lo, e1 := decimal.NewFromString(tier.Min)
		hi, e2 := decimal.NewFromString(tier.Max)
		r, e3 := decimal.NewFromString(tier.Rate)
		if e1 == nil && e2 == nil && e3 == nil && !amount.LessThan(lo) && !amount.GreaterThan(hi) {
			rate = r
			break
		}
	}
	if !rate.IsPositive() {
		a.sendText(ctx, b, chatID, "🚫 এই amount-এর জন্য কোনো rate নির্ধারিত নেই।", nil)
		return
	}
	baseBDT := amount.Mul(rate).Truncate(0)
	s.Amount, s.Rate, s.BDT = amount.String(), rate.String(), Money(baseBDT)
	s.RefBonusUsed, s.TotalBDT, s.Step = "0", Money(baseBDT), "network"
	sellSessions.Store(userID, s)
	_ = saveWorkflowSession(ctx, a.DB, "sell", userID, s, 7*24*time.Hour)
	textOut := fmt.Sprintf("💰 <b>Amount:</b> <code>%s$ = %s৳</code>\n📈 <b>Rate:</b> ৳<code>%s</code>\n💵 <b>Total Receivable:</b> <b>৳<code>%s</code></b>\n\n🔸 নিচে থেকে পেমেন্ট নেটওয়ার্ক নির্বাচন করুন...", amount, s.BDT, Money(rate), s.TotalBDT)
	a.sendText(ctx, b, chatID, textOut, NetworkMenu(amount.String()))
}

func (a *BotApp) CallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.CallbackQuery == nil {
		return
	}
	q := update.CallbackQuery
	if q.Data == "cancel" || q.Data == "cancel_qr_action" {
		userID := q.From.ID
		a.cancelAllSessions(ctx, userID)
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Operation cancelled"})
		isAdmin, _ := IsAdmin(ctx, a.DB, a.Config, userID)
		if isAdmin {
			po, pp, pw, ps, countErr := a.AdminCounts(ctx)
			if countErr == nil {
				a.sendText(ctx, b, userID, "🚫 <b>Operation Cancelled.</b>", AdminMenuWithCounts(userID, a.Config.AdminID, po, pp, pw, ps))
			} else {
				a.sendText(ctx, b, userID, "🚫 <b>Operation Cancelled.</b>", PermanentAdminOthersMenu())
			}
		} else {
			a.sendText(ctx, b, userID, "🚫 <b>Operation Cancelled.</b>", MainMenu())
		}
		return
	}
	if a.SupportCallbackHandler(ctx, b, update) {
		return
	}
	if a.ReferralCallbackHandler(ctx, b, update) {
		return
	}
	if a.WalletCallbackHandler(ctx, b, update) {
		return
	}
	if a.AdminOrderCallbackHandler(ctx, b, update) {
		return
	}
	if a.AdvancedAdminCallback(ctx, b, update) {
		return
	}
	if a.AdminToolsCallback(ctx, b, update) {
		return
	}
	userID := q.From.ID
	// The bot's sell flow is a private-chat workflow, so the callback sender ID
	// is the correct destination even for inaccessible callback messages.
	chatID := userID
	data := q.Data
	if data == "done" {
		value, ok := sellSessions.Load(userID)
		if !ok {
			var restored SellSession
			if found, err := loadWorkflowSession(ctx, a.DB, "sell", userID, &restored); err == nil && found {
				value, ok = &restored, true
				sellSessions.Store(userID, &restored)
			}
		}
		if !ok || value.(*SellSession).Step != "await_done" {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "এই ধাপটি আর সক্রিয় নেই।", ShowAlert: true})
			return
		}
		s := value.(*SellSession)
		s.Step = "txid"
		sellSessions.Store(userID, s)
		_ = saveWorkflowSession(ctx, a.DB, "sell", userID, s, 7*24*time.Hour)
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
		a.sendText(ctx, b, chatID, "🔸 দয়া করে আপনার পেমেন্টের TxID/Order no/Tx hash অথবা স্পষ্ট স্ক্রিনশট দিন...", nil)
		return
	}
	if strings.HasPrefix(data, "net:") {
		value, ok := sellSessions.Load(userID)
		if !ok {
			var restored SellSession
			if found, err := loadWorkflowSession(ctx, a.DB, "sell", userID, &restored); err == nil && found {
				value, ok = &restored, true
				sellSessions.Store(userID, &restored)
			}
		}
		if !ok {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "আবার Sell USDT থেকে শুরু করুন।", ShowAlert: true})
			return
		}
		s := value.(*SellSession)
		if s.Step != "network" {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "এই ধাপটি সক্রিয় নেই।", ShowAlert: true})
			return
		}
		network := strings.TrimPrefix(data, "net:")
		amount, _ := decimal.NewFromString(s.Amount)
		if network != "Binance Pay" && network != "BEP-20" && network != "Bybit Pay" {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Invalid network", ShowAlert: true})
			return
		}
		if network == "Bybit Pay" && amount.LessThan(decimal.NewFromInt(1)) {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Bybit Pay-তে minimum 1 USDT প্রয়োজন।", ShowAlert: true})
			return
		}
		address := a.Config.ReceivingAddresses[network]
		if strings.TrimSpace(address) == "" {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "এই network-এর payment details সেট করা নেই। Admin-কে জানান।", ShowAlert: true})
			return
		}
		s.Network, s.Address, s.Step = network, address, "await_done"
		sellSessions.Store(userID, s)
		_ = saveWorkflowSession(ctx, a.DB, "sell", userID, s, 7*24*time.Hour)
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
		instruction := paymentInstruction(network, amount.String(), address)
		markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✅ Done", CallbackData: "done"}}, {{Text: "✖️ Cancel", CallbackData: "cancel"}}}}
		a.sendText(ctx, b, chatID, instruction, markup)
		return
	}
	if strings.HasPrefix(data, "req_limit_boost:") {
		parts := strings.SplitN(strings.TrimPrefix(data, "req_limit_boost:"), ":", 2)
		if len(parts) != 2 {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "❌ অনুরোধটি সঠিক নয়।", ShowAlert: true})
			return
		}
		targetID, e1 := strconv.ParseInt(parts[0], 10, 64)
		requested, e2 := decimal.NewFromString(parts[1])
		if e1 != nil || e2 != nil || targetID != userID || !requested.IsPositive() {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "❌ অনুরোধটি সঠিক নয়।", ShowAlert: true})
			return
		}
		daily, _ := GetDailyLimit(ctx, a.DB)
		today := time.Now().In(dhakaLocation).Format("2006-01-02")
		cur, findErr := a.DB.Orders.Find(ctx, bson.M{"created_at": bson.M{"$regex": "^" + regexp.QuoteMeta(today)}, "status": "paid", "type": bson.M{"$ne": "admin_add"}})
		sold := decimal.Zero
		if findErr == nil {
			for cur.Next(ctx) {
				var row bson.M
				if cur.Decode(&row) == nil {
					if v, e := decimal.NewFromString(fmt.Sprint(row["amount"])); e == nil {
						sold = sold.Add(v)
					}
				}
			}
			_ = cur.Close(ctx)
		}
		remaining := daily.Sub(sold)
		if remaining.IsNegative() {
			remaining = decimal.Zero
		}
		fullName := telegramFullName(q.From)
		adminNotice := fmt.Sprintf("📨 <b>Daily Sell Limit Increase Request</b>\n\n👤 User: <b>%s</b>\n🏷️ Username: <b>@%s</b>\n🆔 User ID: <code>%d</code>\n📅 আজকের Daily Limit: <b>%s USDT</b>\n✅ আজ ইতিমধ্যে বিক্রি: <b>%s USDT</b>\n📉 বর্তমানে বাকি: <b>%s USDT</b>\n💰 User-এর চাওয়া Sell Amount: <b>%s USDT</b>\n\nঅনুগ্রহ করে সিদ্ধান্ত নিন:", safeText(fullName), safeText(q.From.Username), userID, daily, sold, remaining, requested)
		limitKB := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✅ Receive this", CallbackData: fmt.Sprintf("accept_limit:%d:%s", userID, requested)}, {Text: "❌ Reject Request", CallbackData: fmt.Sprintf("reject_limit:%d", userID)}}}}
		if _, sendErr := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: a.Config.AdminID, Text: adminNotice, ParseMode: models.ParseModeHTML, ReplyMarkup: limitKB}); sendErr != nil {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "❌ অ্যাডমিনের কাছে অনুরোধ পাঠানো যায়নি।", ShowAlert: true})
			return
		}
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "✅ আপনার অনুরোধ অ্যাডমিনের কাছে পাঠানো হয়েছে।"})
		a.sendText(ctx, b, chatID, "✅ আপনার লিমিট বাড়ানোর অনুরোধ অ্যাডমিন বরাবর পাঠানো হয়েছে।", nil)
		return
	}
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
}

func paymentInstruction(network, amount, address string) string {
	switch network {
	case "Bybit Pay":
		return fmt.Sprintf("<b>🚀 ⒹⓇ Bybit Pay</b>\n\n<b>Amount to Pay:</b> <code>%s USDT</code>\n<b>Method:</b> <b>Bybit Pay</b>\n\n📲 <b>কিভাবে পাঠাবেন:</b>\n১. আপনার Bybit অ্যাপের scanner ওপেন করুন।\n২. QR scan করুন অথবা QR upload করুন।\n৩. Amount <code>%s USDT</code> Confirm করে পাঠিয়ে দিন।\n\n⚠️ 1 USDT-এর নিচে Bybit Pay send/pay হয় না। Exact amount পাঠাবেন।", amount, amount)
	case "Binance Pay":
		return fmt.Sprintf("<b>💎 ⒹⓇ Binance Pay</b>\n\n<b>Amount to Pay:</b> <code>%s USDT</code>\n<b>Method:</b> <b>Binance Pay</b>\n\n📲 Binance Pay scanner দিয়ে QR scan করুন অথবা Gallery থেকে QR upload করুন। Exact amount <code>%s USDT</code> পাঠিয়ে নিচের Done চাপুন।\n\n<b>Payment details:</b> <code>%s</code>", amount, amount, address)
	default:
		return fmt.Sprintf("<b>🌐 ⒹⓇ BEP-20 Network</b>\n\n<b>Amount to Pay:</b> <code>%s USDT</code>\n<b>Network:</b> <b>BSC (BEP-20)</b>\n\n<b>Address (BEP-20):</b>\n<code>%s</code>\n\n১. Address copy করুন।\n২. Wallet/Exchange-এর Send/Withdraw অপশনে যান।\n৩. USDT এবং BNB (BEP-20) Network নির্বাচন করুন।\n৪. Exact amount <code>%s USDT</code> পাঠান।\n\n⚠️ Network fee আপনার। Exchange থেকে পাঠালে minimum withdrawal প্রযোজ্য হতে পারে।", amount, address, amount)
	}
}

func (a *BotApp) handleSellProof(ctx context.Context, b *bot.Bot, update *models.Update, s *SellSession, proofText string) {
	msg := update.Message
	user := msg.From
	chatID := msg.Chat.ID
	photoID := ""
	photoHash := ""
	if len(msg.Photo) > 0 {
		photoID = msg.Photo[len(msg.Photo)-1].FileID
		file, fileErr := b.GetFile(ctx, &bot.GetFileParams{FileID: photoID})
		if fileErr != nil {
			a.sendText(ctx, b, chatID, "❌ স্ক্রিনশট যাচাই করা যাচ্ছে না। অনুগ্রহ করে আবার ছবিটি পাঠান।", nil)
			return
		}
		if file.FileSize > 10*1024*1024 {
			a.sendText(ctx, b, chatID, "❌ স্ক্রিনশটটি ১০ MB-এর বেশি। ছোট/কমপ্রেস করা ছবি পাঠান।", nil)
			return
		}
		request, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, b.FileDownloadLink(file), nil)
		if reqErr != nil {
			a.sendText(ctx, b, chatID, "❌ স্ক্রিনশট যাচাই করা যাচ্ছে না। আবার পাঠান।", nil)
			return
		}
		response, downloadErr := http.DefaultClient.Do(request)
		if downloadErr != nil {
			a.sendText(ctx, b, chatID, "❌ স্ক্রিনশট ডাউনলোড করা যায়নি। আবার পাঠান।", nil)
			return
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			a.sendText(ctx, b, chatID, "❌ স্ক্রিনশট যাচাই করা যাচ্ছে না। আবার পাঠান।", nil)
			return
		}
		content, readErr := io.ReadAll(io.LimitReader(response.Body, 10*1024*1024+1))
		_ = response.Body.Close()
		if readErr != nil || len(content) > 10*1024*1024 {
			a.sendText(ctx, b, chatID, "❌ স্ক্রিনশটটি ১০ MB-এর বেশি অথবা পড়া যাচ্ছে না।", nil)
			return
		}
		hash := sha256.Sum256(content)
		photoHash = hex.EncodeToString(hash[:])
	}
	if photoID == "" && strings.TrimSpace(proofText) == "" {
		a.sendText(ctx, b, chatID, "❌ বৈধ TxID/প্রমাণ দিন অথবা পরিষ্কার স্ক্রিনশট আপলোড করুন।", nil)
		return
	}
	normalized := regexp.MustCompile(`[^a-z0-9]`).ReplaceAllString(strings.ToLower(proofText), "")
	if photoID == "" && len(normalized) < 6 {
		a.sendText(ctx, b, chatID, "❌ বৈধ TxID/প্রমাণ দিন অথবা পরিষ্কার স্ক্রিনশট আপলোড করুন।", nil)
		return
	}
	proof := strings.TrimSpace(proofText)
	if photoID != "" {
		if proof == "" {
			proof = "Only Screenshot"
		}
		proof = fmt.Sprintf("Photo [File ID: %s] | TxID/Ref: %s", photoID, proof)
	}
	conditions := bson.A{bson.M{"txid": proof}}
	if normalized != "" {
		conditions = append(conditions, bson.M{"txid_normalized": normalized})
	}
	if photoHash != "" {
		conditions = append(conditions, bson.M{"proof_image_hash": photoHash})
	}
	count, err := a.DB.Orders.CountDocuments(ctx, bson.M{"$or": conditions})
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Proof যাচাই করা যাচ্ছে না। আবার চেষ্টা করুন।", nil)
		return
	}
	if count > 0 {
		a.clearSession(ctx, user.ID)
		a.sendText(ctx, b, chatID, "⚠️ এই TxID/স্ক্রিনশটটি ইতিপূর্বে ব্যবহার করা হয়েছে!", MainMenu())
		return
	}
	oid, err := GenerateUniqueOrderID(ctx, a.DB)
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Order ID তৈরি করা যায়নি। আবার চেষ্টা করুন।", nil)
		return
	}
	order := bson.M{
		"id": oid, "user_id": user.ID, "username": user.Username, "full_name": telegramFullName(user),
		"amount": s.Amount, "rate": s.Rate, "bdt": s.BDT, "ref_bonus_used": s.RefBonusUsed,
		"total_bdt": s.TotalBDT, "network": s.Network, "address": s.Address, "txid": proof,
		"status": "pending_verification", "created_at": BangladeshTime(), "payout": "",
		"payout_number": "", "channel_msg_id": 0, "fast_payment": false,
	}
	if normalized != "" {
		order["txid_normalized"] = normalized
	}
	if photoID != "" {
		order["proof_photo_file_id"] = photoID
	}
	if photoHash != "" {
		order["proof_image_hash"] = photoHash
	}
	if _, err = a.DB.Orders.InsertOne(ctx, order); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			a.clearSession(ctx, user.ID)
			a.sendText(ctx, b, chatID, "😡 এই TxID বা প্রমাণটি ইতোমধ্যে অন্য কোনো অর্ডারে ব্যবহার করা হয়েছে!\n🚫 Order cancelled.", MainMenu())
			return
		}
		a.sendText(ctx, b, chatID, "❌ Order সংরক্ষণ করা যায়নি। আবার চেষ্টা করুন।", nil)
		return
	}
	adminText := fmt.Sprintf("🤝 <b>New Order <code>#%d</code></b>\n👤 User ID: <code>%d</code>\n🏷️ Username: <b>@%s</b>\n💰 Amount: <code>%s USDT</code>\n💵 Total: ৳<code>%s</code> (Base: ৳%s + Ref: ৳%s)\n🌐 Method: <b>%s</b>\n🔑 Proof/TxID: <code>%s</code>\n⏱️ Time: <i>%s</i>", oid, user.ID, safeText(user.Username), s.Amount, s.TotalBDT, s.BDT, s.RefBonusUsed, s.Network, safeText(proof), BangladeshTime())
	adminMarkup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✅ Approve", CallbackData: fmt.Sprintf("approve:%d", oid)}, {Text: "❌ Reject Request", CallbackData: fmt.Sprintf("reject:%d", oid)}}}}
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: a.Config.AdminID, Text: adminText, ParseMode: models.ParseModeHTML, ReplyMarkup: adminMarkup})
	a.clearSession(ctx, user.ID)
	a.sendText(ctx, b, chatID, fmt.Sprintf("📦 Order <code>#%d</code>\n\n✅ Successfully submitted.\n⏳ Waiting for admin approval...\n🔸 দয়া করে কিছুক্ষণ অপেক্ষা করুন 🙏", oid), MainMenu())
}

func safeText(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(value)
}

// ---- merged from history_handlers.go ----

// OrderHistory ports my_orders(): newest 25 sell orders, with related ticket
// IDs and chat-backup actions. Admin wallet adjustments are excluded.
func (a *BotApp) OrderHistory(ctx context.Context, b *bot.Bot, userID int64) {
	cursor, err := a.DB.Orders.Find(ctx, bson.M{"user_id": userID, "type": bson.M{"$ne": "admin_add"}}, options.Find().SetSort(bson.D{{Key: "id", Value: -1}}).SetLimit(25))
	if err != nil {
		a.sendText(ctx, b, userID, "❌ Order History লোড করা যায়নি।", MainMenu())
		return
	}
	defer cursor.Close(ctx)
	found := false
	statusLabels := map[string]string{"pending_verification": "Pending Verification ⏳", "approved_pending_payout": "Pending Payout Method ⏳", "approved_pending_payment": "Pending Payment 💸", "paid": "Completed ✅", "rejected": "Rejected ❌", "permanently_rejected": "Permanently Rejected ❌"}
	ticketLabels := map[string]string{"open": "Open ⏳", "resolved": "Resolved ✅", "cancelled": "Cancelled ❌"}
	for cursor.Next(ctx) {
		var row bson.M
		if cursor.Decode(&row) != nil {
			continue
		}
		found = true
		id := orderInt64(row, "id")
		status := orderString(row, "status", "unknown")
		if label, ok := statusLabels[status]; ok {
			status = label
		}
		text := fmt.Sprintf("📦 <b>Order <code>#%d</code></b> | <code>%s USDT</code> ➔ ৳<code>%s</code>\n🌐 Method: <b>%s</b>\n📌 Status: <i>%s</i>\n", id, htmlEscape(orderString(row, "amount", "0")), htmlEscape(orderString(row, "total_bdt", "0")), htmlEscape(orderString(row, "network", "N/A")), htmlEscape(status))
		if reason := orderString(row, "reject_reason", ""); reason != "" {
			text += "💬 Reject Reason: <b>" + htmlEscape(reason) + "</b>\n"
		}
		text += "⏱️ Time: <i>" + htmlEscape(orderString(row, "created_at", "N/A")) + "</i>"
		tickets, e := a.DB.Tickets.Find(ctx, bson.M{"user_id": userID, "order_id": fmt.Sprint(id)})
		buttons := [][]models.InlineKeyboardButton{}
		if e == nil {
			for tickets.Next(ctx) {
				var ticket bson.M
				if tickets.Decode(&ticket) != nil {
					continue
				}
				tid := orderString(ticket, "ticket_id", "")
				ts := orderString(ticket, "status", "unknown")
				if v, ok := ticketLabels[ts]; ok {
					ts = v
				}
				text += fmt.Sprintf("\n🎫 Token: <code>%s</code> (%s)", htmlEscape(tid), htmlEscape(ts))
				buttons = append(buttons, []models.InlineKeyboardButton{{Text: "💬 Chat Backup (" + tid + ")", CallbackData: "chat_backup:" + tid}})
			}
			_ = tickets.Close(ctx)
		}
		var markup models.ReplyMarkup
		if len(buttons) > 0 {
			markup = &models.InlineKeyboardMarkup{InlineKeyboard: buttons}
		}
		a.sendText(ctx, b, userID, text, markup)
	}
	if !found {
		a.sendText(ctx, b, userID, "📦 <b>Order History</b>\n\nℹ️ আপনার এখনো কোনো Sell Order History নেই।\nপ্রথম USDT Sell Order সম্পন্ন করলে সেটি এখানে দেখা যাবে।", MainMenu())
		return
	}
	a.sendText(ctx, b, userID, "📋 আপনার সর্বশেষ সেল অর্ডার ও সাপোর্ট টিকিট তালিকা উপরে দেখানো হলো।", MainMenu())
}

func (a *BotApp) ChatBackup(ctx context.Context, b *bot.Bot, userID int64, ticketID string) {
	var ticket bson.M
	if err := a.DB.Tickets.FindOne(ctx, bson.M{"ticket_id": ticketID}).Decode(&ticket); err != nil {
		a.sendText(ctx, b, userID, "❌ কোনো চ্যাট হিস্ট্রি পাওয়া যায়নি।", nil)
		return
	}
	admin, err := IsAdmin(ctx, a.DB, a.Config, userID)
	if err != nil || (!admin && orderInt64(ticket, "user_id") != userID) {
		a.sendText(ctx, b, userID, "❌ আপনার এই চ্যাট দেখার অনুমতি নেই।", nil)
		return
	}
	history, _ := ticket["chat_history"].(bson.A)
	lines := []string{fmt.Sprintf("💬 <b>Chat Backup & History (Ticket: <code>%s</code>)</b>", htmlEscape(ticketID)), ""}
	for _, raw := range history {
		item := fmt.Sprint(raw)
		switch {
		case strings.HasPrefix(item, "user:"):
			lines = append(lines, "👤 <b>User:</b> "+htmlEscape(strings.TrimSpace(strings.TrimPrefix(item, "user:"))))
		case strings.HasPrefix(item, "admin:"):
			lines = append(lines, "🛡️ <b>Admin:</b> "+htmlEscape(strings.TrimSpace(strings.TrimPrefix(item, "admin:"))))
		default:
			lines = append(lines, "▫️ "+htmlEscape(item))
		}
		lines = append(lines, "───────────────────────────")
	}
	out := strings.Join(lines, "\n")
	if len(out) > 3950 {
		out = out[:3950] + "\n...[Truncated]"
	}
	a.sendText(ctx, b, userID, out, &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "🔙 Go Back", CallbackData: "chat_backup_back"}}}})
}

// ---- merged from referral_handlers.go ----

func (a *BotApp) ReferralDashboard(ctx context.Context, b *bot.Bot, userID int64) {
	var user bson.M
	if err := a.DB.Users.FindOne(ctx, bson.M{"user_id": userID}).Decode(&user); err != nil {
		a.sendText(ctx, b, userID, "❌ আপনার প্রোফাইল তথ্য পাওয়া যায়নি।", MainMenu())
		return
	}
	valid := orderInt64(user, "valid_referrals")
	bonus, _ := decimal.NewFromString(strings.ReplaceAll(orderString(user, "referral_bonus_bdt", "0"), ",", ""))
	cursor, err := a.DB.Users.Find(ctx, bson.M{"referred_by": userID})
	pending := int64(0)
	if err == nil {
		defer cursor.Close(ctx)
		for cursor.Next(ctx) {
			var referred bson.M
			if cursor.Decode(&referred) != nil {
				continue
			}
			count, _ := a.DB.Orders.CountDocuments(ctx, bson.M{"user_id": orderInt64(referred, "user_id"), "status": "paid", "type": bson.M{"$ne": "admin_add"}})
			if count == 0 {
				pending++
			}
		}
	}
	firstPct, lifePct := decimal.NewFromFloat(.05), decimal.NewFromFloat(.005)
	if raw, ok, e := GetSetting(ctx, a.DB, "ref_percentages"); e == nil && ok {
		var p map[string]string
		if e := json.Unmarshal([]byte(raw), &p); e == nil {
			if v, e := decimal.NewFromString(p["first_sale"]); e == nil {
				firstPct = v
			}
			if v, e := decimal.NewFromString(p["lifetime"]); e == nil {
				lifePct = v
			}
		}
	}
	link := fmt.Sprintf("https://t.me/%s?start=%d", a.Config.BotUsername, userID)
	text := fmt.Sprintf("👥 <b>Referral Program Dashboard</b>\n\n🔗 <b>Your Invite Link:</b>\n<code>%s</code>\n\n📊 <b>Referral Statistics:</b>\n• <b>Successful Referrals:</b> %d users\n• <b>Pending Referrals:</b> %d users\n• <b>Accumulated Commission:</b> ৳<code>%s</code>\n\n<blockquote>১. ১ম সফল সেলে %s এককালীন বোনাস।\n২. প্রতি লেনদেনে অতিরিক্ত %s লাইফটাইম কমিশন।</blockquote>", htmlEscape(link), valid, pending, Money(bonus), firstPct.Mul(decimal.NewFromInt(100)).String()+"%", lifePct.Mul(decimal.NewFromInt(100)).String()+"%")
	var rows [][]models.InlineKeyboardButton
	if bonus.IsPositive() {
		rows = append(rows, []models.InlineKeyboardButton{{Text: "💳 Transfer Referral Balance to Wallet", CallbackData: "withdraw_refer_to_wallet"}})
	}
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: rows}
	a.sendText(ctx, b, userID, text, markup)
}

func (a *BotApp) ReferralCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	if update == nil || update.CallbackQuery == nil || update.CallbackQuery.Data != "withdraw_refer_to_wallet" {
		return false
	}
	q := update.CallbackQuery
	uid := q.From.ID
	answer := func(t string, alert bool) {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: t, ShowAlert: alert})
	}
	var user bson.M
	if err := a.DB.Users.FindOne(ctx, bson.M{"user_id": uid}).Decode(&user); err != nil {
		answer("❌ ইউজার ডাটা পাওয়া যায়নি।", true)
		return true
	}
	bonus, err := decimal.NewFromString(strings.ReplaceAll(orderString(user, "referral_bonus_bdt", "0"), ",", ""))
	if err != nil || !bonus.IsPositive() {
		answer("❌ স্থানান্তরের মতো কোনো referral balance নেই।", true)
		return true
	}
	rawBonus := orderString(user, "referral_bonus_bdt", "0")
	applied, txErr := ApplyWalletDeltaAnd(ctx, a.DB, uid, bonus,
		"referral_transfer:"+strconv.FormatInt(uid, 10)+":"+q.ID,
		"referral_balance_transfer", q.ID,
		func(tx context.Context) error {
			res, updateErr := a.DB.Users.UpdateOne(tx, bson.M{"user_id": uid, "referral_bonus_bdt": rawBonus}, bson.M{"$set": bson.M{"referral_bonus_bdt": "0", "withdraw_requested": false}})
			if updateErr != nil {
				return updateErr
			}
			if res.ModifiedCount != 1 {
				return errors.New("referral balance already transferred or changed")
			}
			return nil
		})
	if txErr != nil || !applied {
		answer("⚠️ Referral balance ইতোমধ্যে স্থানান্তর হয়েছে অথবা Wallet update ব্যর্থ হয়েছে।", true)
		return true
	}
	var updated bson.M
	_ = a.DB.Users.FindOne(ctx, bson.M{"user_id": uid}).Decode(&updated)
	wallet, _ := decimal.NewFromString(strings.ReplaceAll(orderString(updated, "mini_wallet", "0"), ",", ""))
	answer("✅ Referral balance Wallet-এ যোগ হয়েছে!", true)
	a.sendText(ctx, b, uid, fmt.Sprintf("🎉 <b>Referral Balance Transferred!</b>\n\nসফলভাবে ৳<b>%s</b> আপনার মূল 💳 <b>Wallet</b>-এ যুক্ত হয়েছে।\nবর্তমান Wallet Balance: ৳<b>%s</b>", Money(bonus), Money(wallet)), MainMenu())
	return true
}

func (a *BotApp) Profile(ctx context.Context, b *bot.Bot, user *models.User) {
	if user == nil {
		return
	}
	uid := user.ID
	var u bson.M
	if err := a.DB.Users.FindOne(ctx, bson.M{"user_id": uid}).Decode(&u); err != nil {
		a.sendText(ctx, b, uid, "❌ আপনার প্রোফাইল তথ্য পাওয়া যায়নি।", nil)
		return
	}
	cursor, err := a.DB.Orders.Find(ctx, bson.M{"user_id": uid, "status": "paid", "type": bson.M{"$ne": "admin_add"}})
	if err != nil {
		a.sendText(ctx, b, uid, "❌ Profile history লোড করা যায়নি।", nil)
		return
	}
	defer cursor.Close(ctx)
	count := int64(0)
	totalUSDT, totalBDT := decimal.Zero, decimal.Zero
	for cursor.Next(ctx) {
		var o bson.M
		if cursor.Decode(&o) != nil {
			continue
		}
		count++
		if v, e := decimal.NewFromString(orderString(o, "amount", "0")); e == nil {
			totalUSDT = totalUSDT.Add(v)
		}
		if v, e := decimal.NewFromString(strings.ReplaceAll(orderString(o, "total_bdt", "0"), ",", "")); e == nil {
			totalBDT = totalBDT.Add(v)
		}
	}
	openTickets, _ := a.DB.Tickets.CountDocuments(ctx, bson.M{"user_id": uid, "status": "open"})
	name := telegramFullName(user)
	username := user.Username
	if username == "" {
		username = "N/A"
	}
	wallet, _ := decimal.NewFromString(orderString(u, "mini_wallet", "0"))
	bonus := orderString(u, "referral_bonus_bdt", "0")
	text := fmt.Sprintf("👤 <b>Your Account Overview</b>\n\n🆔 <b>User ID:</b> <code>%d</code>\n🏷️ <b>Name:</b> <b>%s</b>\n🔗 <b>Username:</b> <b>@%s</b>\n💳 <b>Wallet Balance:</b> ৳<code>%s</code>\n📅 <b>Joined Date:</b> <i>%s</i>\n\n📊 <b>Account Activity</b>\n• <b>Successful Orders:</b> %d Times\n• <b>Total USDT Sold:</b> <code>%s</code> USDT\n• <b>Total BDT Received:</b> ৳<code>%s</code> BDT\n• <b>Active Support Tickets:</b> %d\n\n👥 <b>Referral Summary</b>\n• <b>Valid Referrals:</b> %d Users\n• <b>Current Refer Bonus:</b> ৳<code>%s</code>", uid, htmlEscape(name), htmlEscape(username), Money(wallet), htmlEscape(orderString(u, "created_at", "N/A")), count, totalUSDT.String(), Money(totalBDT), openTickets, orderInt64(u, "valid_referrals"), htmlEscape(bonus))
	a.sendText(ctx, b, uid, text, &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "💳 My Payment Numbers", CallbackData: "my_payment_numbers"}}}})
}
