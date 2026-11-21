package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/shopspring/decimal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---- merged from utils.go ----

var dhakaLocation = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Dhaka")
	if err != nil {
		return time.FixedZone("Asia/Dhaka", 6*60*60)
	}
	return loc
}()

func BangladeshTime() string { return time.Now().In(dhakaLocation).Format("2006-01-02 03:04:05 PM") }

func MaskNumber(value string) string {
	if strings.HasPrefix(value, "Photo [File ID:") {
		return "🖼️ [QR Code Image]"
	}
	if len(value) > 6 {
		return value[:3] + "******" + value[len(value)-2:]
	}
	return value
}

func IsAdmin(ctx context.Context, db *Database, cfg Config, userID int64) (bool, error) {
	if userID == cfg.AdminID {
		return true, nil
	}
	count, err := db.Admins.CountDocuments(ctx, bson.M{"admin_id": userID, "status": "active"})
	return count > 0, err
}

func IsUserBanned(ctx context.Context, db *Database, userID int64) (bool, error) {
	var user bson.M
	err := db.Users.FindOne(ctx, bson.M{"user_id": userID}).Decode(&user)
	if err == mongo.ErrNoDocuments {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	banned, _ := user["is_banned"].(bool)
	return banned, nil
}

func GetSetting(ctx context.Context, db *Database, key string) (string, bool, error) {
	var row struct {
		Value string `bson:"setting_value"`
	}
	err := db.Settings.FindOne(ctx, bson.M{"setting_key": key}).Decode(&row)
	if err == mongo.ErrNoDocuments {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return row.Value, true, nil
}

func GetMinMaxLimits(ctx context.Context, db *Database) (decimal.Decimal, decimal.Decimal, error) {
	fallbackMin, fallbackMax := decimal.NewFromFloat(0.5), decimal.NewFromInt(50)
	raw, ok, err := GetSetting(ctx, db, "min_max_limits")
	if err != nil || !ok {
		return fallbackMin, fallbackMax, err
	}
	var limits struct {
		Min string `json:"min"`
		Max string `json:"max"`
	}
	if json.Unmarshal([]byte(raw), &limits) != nil {
		return fallbackMin, fallbackMax, nil
	}
	min, e1 := decimal.NewFromString(limits.Min)
	max, e2 := decimal.NewFromString(limits.Max)
	if e1 != nil || e2 != nil || !min.IsPositive() || max.LessThan(min) {
		return fallbackMin, fallbackMax, nil
	}
	return min, max, nil
}

func GetDailyLimit(ctx context.Context, db *Database) (decimal.Decimal, error) {
	raw, ok, err := GetSetting(ctx, db, "daily_amount_limit")
	if err != nil {
		return decimal.Zero, err
	}
	if !ok {
		return decimal.NewFromInt(10000), nil
	}
	v, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.NewFromInt(10000), nil
	}
	return v, nil
}

func GenerateUniqueOrderID(ctx context.Context, db *Database) (int64, error) {
	for attempts := 0; attempts < 100; attempts++ {
		n, err := rand.Int(rand.Reader, big.NewInt(9999900))
		if err != nil {
			return 0, err
		}
		id := n.Int64() + 100
		var found bson.M
		err = db.Orders.FindOne(ctx, bson.M{"id": id}).Decode(&found)
		if err != nil && err != mongo.ErrNoDocuments {
			return 0, err
		}
		if err == nil {
			continue
		}
		err = db.WithdrawRequests.FindOne(ctx, bson.M{"id": id}).Decode(&found)
		if err == mongo.ErrNoDocuments {
			return id, nil
		}
		if err != nil {
			return 0, err
		}
	}
	return 0, fmt.Errorf("could not allocate a unique order id after 100 attempts")
}

type RateTier struct {
	Min  string `json:"min"`
	Max  string `json:"max"`
	Rate string `json:"rate"`
}

func GetRateTiers(ctx context.Context, db *Database) ([]RateTier, error) {
	raw, ok, err := GetSetting(ctx, db, "rate_tiers")
	if err != nil {
		return nil, err
	}
	if !ok {
		min, max, e := GetMinMaxLimits(ctx, db)
		if e != nil {
			return nil, e
		}
		return []RateTier{{Min: min.String(), Max: max.String(), Rate: "120"}}, nil
	}
	var tiers []RateTier
	if json.Unmarshal([]byte(raw), &tiers) != nil {
		return []RateTier{}, nil
	}
	return tiers, nil
}

func RatesText(ctx context.Context, db *Database) (string, error) {
	raw, ok, err := GetSetting(ctx, db, "raw_rate_text")
	if err != nil {
		return "", err
	}
	if ok && strings.TrimSpace(raw) != "" {
		return "📊 <b>Live Exchange Rates</b>\n\n" + raw, nil
	}
	tiers, err := GetRateTiers(ctx, db)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(tiers))
	for _, tier := range tiers {
		lines = append(lines, fmt.Sprintf("🔹 <code>$%s - $%s</code> ➔ <i>($1 = ৳%s)</i>", tier.Min, tier.Max, tier.Rate))
	}
	return "📊 <b>Live Exchange Rates</b>\n\n" + strings.Join(lines, "\n"), nil
}

func GetActivePayoutMethods(ctx context.Context, db *Database) ([]string, error) {
	raw, ok, err := GetSetting(ctx, db, "active_payout_methods")
	if err != nil {
		return nil, err
	}
	fallback := []string{"bKash", "Nagad", "Recharge", "Bangla QR"}
	if !ok {
		return fallback, nil
	}
	var methods []string
	if json.Unmarshal([]byte(raw), &methods) != nil {
		return fallback, nil
	}
	return methods, nil
}

func CalculatePayout(ctx context.Context, db *Database, amount decimal.Decimal, payoutMethod string) (net, rate, fee decimal.Decimal, err error) {
	tiers, err := GetRateTiers(ctx, db)
	if err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, err
	}
	sort.SliceStable(tiers, func(i, j int) bool {
		a, _ := decimal.NewFromString(tiers[i].Min)
		b, _ := decimal.NewFromString(tiers[j].Min)
		return a.LessThan(b)
	})
	found := false
	for _, tier := range tiers {
		lo, e1 := decimal.NewFromString(tier.Min)
		hi, e2 := decimal.NewFromString(tier.Max)
		r, e3 := decimal.NewFromString(tier.Rate)
		if e1 == nil && e2 == nil && e3 == nil && !amount.LessThan(lo) && !amount.GreaterThan(hi) {
			rate = r
			found = true
			break
		}
	}
	if !found && len(tiers) > 0 {
		rate, err = decimal.NewFromString(tiers[len(tiers)-1].Rate)
		if err != nil {
			return decimal.Zero, decimal.Zero, decimal.Zero, err
		}
	}
	if !found && len(tiers) == 0 {
		rate = decimal.NewFromInt(120)
	}
	raw := amount.Mul(rate).Truncate(2)
	net = raw
	fee = decimal.Zero
	if payoutMethod != "" && !strings.EqualFold(payoutMethod, "recharge") {
		fee = raw.Mul(decimal.NewFromFloat(0.01)).Truncate(2)
		net = raw.Sub(fee)
	}
	return net.Truncate(2), rate, fee, nil
}

func Money(value decimal.Decimal) string {
	rounded := value.RoundBank(0).IntPart()
	s := strconv.FormatInt(rounded, 10)
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign = "-"
		s = s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return sign + s
}

// Wallet balance mutations must use ApplyWalletDeltaAnd in wallet_atomic.go.
// That helper couples the balance update to an idempotency ledger and the
// associated business-state transition in one MongoDB transaction.

func LogAdminAction(ctx context.Context, db *Database, adminID int64, username, orderID, trxID, status, amountUSDT, amountBDT string) error {
	if username == "" {
		username = "N/A"
	}
	if trxID == "" {
		trxID = "N/A"
	}
	_, err := db.AuditLogs.InsertOne(ctx, bson.M{"admin_id": strconv.FormatInt(adminID, 10), "admin_username": username, "order_id": orderID, "trx_id": trxID, "order_status": status, "amount_usdt": amountUSDT, "amount_bdt": amountBDT, "timestamp": BangladeshTime()})
	return err
}

// ---- merged from menus.go ----

func ReplyMenu(rows ...[]string) *models.ReplyKeyboardMarkup {
	keyboard := make([][]models.KeyboardButton, 0, len(rows))
	for _, row := range rows {
		buttons := make([]models.KeyboardButton, 0, len(row))
		for _, label := range row {
			buttons = append(buttons, models.KeyboardButton{Text: label})
		}
		keyboard = append(keyboard, buttons)
	}
	return &models.ReplyKeyboardMarkup{Keyboard: keyboard, ResizeKeyboard: true}
}

func MainMenu() *models.ReplyKeyboardMarkup {
	return ReplyMenu([]string{"💳 My Wallet", "💰 Sell USDT"}, []string{"📈 Live Rates", "👤 My Profile"}, []string{"📦 Order History", "👥 Referral Program"}, []string{"🏆 Leaderboard"}, []string{"📞 Help & Support"})
}
func SupportMenu() *models.ReplyKeyboardMarkup {
	return ReplyMenu([]string{"⚠️ Payment Support", "🧮 Rate Calculator"}, []string{"💸 Live Transactions", "📜 Rules & Information"}, []string{"🔍 Track Support Ticket", "📥 Payment Methods"}, []string{"❓ Help Center (FAQ)"}, []string{"🏠 Back to Main Menu"})
}
func PermanentAdminOthersMenu() *models.ReplyKeyboardMarkup {
	return ReplyMenu([]string{"💱 Update Exchange Rates", "👥 Referral Commission"}, []string{"🛠️ Manage Payment Methods", "⚙️ Set Transaction Limits"}, []string{"👥 Manage Administrators", "🏆 Leaderboard Rewards"}, []string{"📋 Admin Activity Log", "⭐ Customer Reviews"}, []string{"🔍 Search User"}, []string{"🔙 Admin Dashboard"})
}
func AdminMenuWithCounts(userID, ownerID int64, pendingOrders, pendingPayments, pendingWithdraws, pendingSupport int64) *models.ReplyKeyboardMarkup {
	if userID == ownerID {
		return ReplyMenu([]string{"🔄 Refresh Dashboard"}, []string{fmt.Sprintf("💸 Pending Payments (%d)", pendingPayments), fmt.Sprintf("⏳ Pending Orders (%d)", pendingOrders)}, []string{fmt.Sprintf("🏧 Withdrawal Requests (%d)", pendingWithdraws), fmt.Sprintf("🛠️ Support Tickets (%d)", pendingSupport)}, []string{"📢 Broadcast Message", "➕ Adjust User Wallet"}, []string{"📦 Sell Order History", "🏧 Withdrawal History"}, []string{"⚙️ More Settings"})
	}
	return ReplyMenu([]string{"🔄 Refresh Dashboard"}, []string{fmt.Sprintf("💸 Pending Payments (%d)", pendingPayments), fmt.Sprintf("⏳ Pending Orders (%d)", pendingOrders)}, []string{"📋 Admin Activity Log", fmt.Sprintf("🛠️ Support Tickets (%d)", pendingSupport)}, []string{fmt.Sprintf("🏧 Withdrawal Requests (%d)", pendingWithdraws)}, []string{"📚 Complete Transaction History"})
}

func NetworkMenu(amountText string) *models.InlineKeyboardMarkup {
	amount, _ := decimal.NewFromString(amountText)
	row := []models.InlineKeyboardButton{{Text: "💎 Binance Pay", CallbackData: "net:Binance Pay"}}
	if amount.GreaterThanOrEqual(decimal.NewFromInt(1)) {
		row = append(row, models.InlineKeyboardButton{Text: "🚀 Bybit Pay", CallbackData: "net:Bybit Pay"})
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{row, {{Text: "🌐 BEP-20 Network", CallbackData: "net:BEP-20"}}, {{Text: "✖️ Cancel", CallbackData: "cancel"}}}}
}

func CreatePayoutMenu(ctx context.Context, db *Database, prefix string, identifier int64, userID int64) (*models.InlineKeyboardMarkup, error) {
	active, err := GetActivePayoutMethods(ctx, db)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, v := range active {
		allowed[v] = true
	}
	var user bson.M
	if userID != 0 {
		_ = db.Users.FindOne(ctx, bson.M{"user_id": userID}).Decode(&user)
	}
	saved := map[string]interface{}{}
	switch v := user["payment_numbers"].(type) {
	case bson.M:
		for k, x := range v {
			saved[k] = x
		}
	case map[string]interface{}:
		saved = v
	}
	methods := []string{"bKash", "Nagad", "Rocket", "Upay", "Recharge", "Bangla QR"}
	buttons := []models.InlineKeyboardButton{}
	for _, name := range methods {
		if !allowed[name] {
			continue
		}
		label := name
		if v, ok := saved[name]; ok && (name == "bKash" || name == "Nagad" || name == "Rocket" || name == "Upay") && strings.TrimSpace(fmt.Sprint(v)) != "" {
			label += " - " + fmt.Sprint(v)
		}
		buttons = append(buttons, models.InlineKeyboardButton{Text: label, CallbackData: fmt.Sprintf("%s:%s:%d", prefix, name, identifier)})
	}
	rows := [][]models.InlineKeyboardButton{}
	for i := 0; i < len(buttons); i += 2 {
		end := i + 2
		if end > len(buttons) {
			end = len(buttons)
		}
		rows = append(rows, buttons[i:end])
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

// ---- merged from integration_handlers.go ----

type calculatorSession struct {
	Step string `bson:"step"`
}

func (a *BotApp) PanelHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return
	}
	chatID, userID := update.Message.Chat.ID, update.Message.From.ID
	isAdmin, err := IsAdmin(ctx, a.DB, a.Config, userID)
	if err != nil || !isAdmin {
		a.sendText(ctx, b, chatID, "⛔ You are not authorized to access this section.", nil)
		return
	}
	po, pp, pw, ps, err := a.AdminCounts(ctx)
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Admin dashboard count লোড করা যায়নি।", nil)
		return
	}
	a.sendText(ctx, b, chatID, "🔐 <b>Administrator Dashboard</b>", AdminMenuWithCounts(userID, a.Config.AdminID, po, pp, pw, ps))
}

func (a *BotApp) TodayReportHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return
	}
	chatID, userID := update.Message.Chat.ID, update.Message.From.ID
	isAdmin, err := IsAdmin(ctx, a.DB, a.Config, userID)
	if err != nil || !isAdmin {
		a.sendText(ctx, b, chatID, "❌ এই কমান্ডটি শুধুমাত্র অ্যাডমিনদের জন্য!", nil)
		return
	}
	loc, err := time.LoadLocation("Asia/Dhaka")
	if err != nil {
		loc = time.FixedZone("Asia/Dhaka", 6*60*60)
	}
	today := time.Now().In(loc).Format("2006-01-02")
	filter := bson.M{"created_at": bson.M{"$regex": "^" + today}, "status": "paid", "type": bson.M{"$ne": "admin_add"}}
	cur, err := a.DB.Orders.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "id", Value: -1}}))
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ আজকের Order Report লোড করা যায়নি।", nil)
		return
	}
	defer cur.Close(ctx)
	totalUSDT, totalBDT := decimal.Zero, decimal.Zero
	sellerTotals := make(map[int64]decimal.Decimal)
	orderIDs := make([]string, 0, 10)
	ordersCount := 0
	for cur.Next(ctx) {
		var row bson.M
		if cur.Decode(&row) != nil {
			continue
		}
		ordersCount++
		uid := orderInt64(row, "user_id")
		amount, parseErr := decimal.NewFromString(orderString(row, "amount", "0"))
		if parseErr == nil {
			totalUSDT = totalUSDT.Add(amount)
			if uid > 0 {
				sellerTotals[uid] = sellerTotals[uid].Add(amount)
			}
		}
		bdt, parseErr := decimal.NewFromString(strings.ReplaceAll(orderString(row, "total_bdt", "0"), ",", ""))
		if parseErr == nil {
			totalBDT = totalBDT.Add(bdt)
		}
		if len(orderIDs) < 10 {
			orderIDs = append(orderIDs, fmt.Sprintf("<code>#%d</code>", orderInt64(row, "id")))
		}
	}
	if err := cur.Err(); err != nil {
		a.sendText(ctx, b, chatID, "❌ Order Report পড়তে সমস্যা হয়েছে।", nil)
		return
	}
	withdrawCount, err := a.DB.WithdrawRequests.CountDocuments(ctx, bson.M{"created_at": bson.M{"$regex": "^" + today}, "status": "paid"})
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Withdrawal Report লোড করা যায়নি।", nil)
		return
	}
	var out strings.Builder
	fmt.Fprintf(&out, "📊 <b>Daily Performance Report — %s</b>\n\n", today)
	fmt.Fprintf(&out, "💎 <b>Total USDT Sold:</b> <code>%s USDT</code>\n", totalUSDT.String())
	fmt.Fprintf(&out, "💵 <b>Total Paid BDT:</b> ৳<code>%s</code>\n", Money(totalBDT))
	fmt.Fprintf(&out, "👥 <b>Total Unique Sellers:</b> <b>%d</b> জন\n", len(sellerTotals))
	fmt.Fprintf(&out, "📦 <b>Total Orders Today:</b> <b>%d</b> টি\n", ordersCount)
	fmt.Fprintf(&out, "🏧 <b>Total Withdraws Today:</b> <b>%d</b> টি\n\n", withdrawCount)
	out.WriteString("👤 <b>User-wise Sell Breakdown:</b>\n")
	if len(sellerTotals) == 0 {
		out.WriteString("<i>আজ এখনো কোনো সেল হয়নি।</i>\n")
	} else {
		for uid, amount := range sellerTotals {
			var user bson.M
			name := "User"
			if a.DB.Users.FindOne(ctx, bson.M{"user_id": uid}).Decode(&user) == nil {
				name = orderString(user, "full_name", "User")
			}
			line := fmt.Sprintf("• <code>%d</code> (%s) — <b>%s USDT</b>\n", uid, htmlEscape(name), amount.String())
			if out.Len()+len(line) > 3400 {
				out.WriteString("… আরও Seller রয়েছে; সম্পূর্ণ তালিকার জন্য Database দেখুন।\n")
				break
			}
			out.WriteString(line)
		}
	}
	out.WriteString("\n📦 <b>Recent Order IDs Today:</b>\n")
	if len(orderIDs) == 0 {
		out.WriteString("<i>নেই</i>")
	} else {
		out.WriteString(strings.Join(orderIDs, ", "))
	}
	a.sendText(ctx, b, chatID, out.String(), AdminMenuWithCounts(userID, a.Config.AdminID, 0, 0, 0, 0))
}

func (a *BotApp) CalculatorCommandHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return
	}
	userID, chatID := update.Message.From.ID, update.Message.Chat.ID
	isAdmin, err := IsAdmin(ctx, a.DB, a.Config, userID)
	if err != nil || isAdmin {
		a.sendText(ctx, b, chatID, "❌ এই Calculator অপশনটি User account-এর জন্য।", nil)
		return
	}
	min, max, err := GetMinMaxLimits(ctx, a.DB)
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Sell limit লোড করা যায়নি।", nil)
		return
	}
	s := calculatorSession{Step: "awaiting_calc_amount"}
	if err := saveWorkflowSession(ctx, a.DB, "calculator", userID, s, 7*24*time.Hour); err != nil {
		a.sendText(ctx, b, chatID, "❌ Calculator session সংরক্ষণ করা যায়নি। আবার চেষ্টা করুন।", MainMenu())
		return
	}
	a.sendText(ctx, b, chatID, fmt.Sprintf("🧮 <b>USDT to BDT Calculator</b>\n\nআপনি কত USDT বিক্রি করতে চান, সেই পরিমাণটি লিখে পাঠান।\n(সীমা: <code>%s USDT - %s USDT</code>)।", min, max), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✖️ Cancel", CallbackData: "cancel"}}}})
}

func (a *BotApp) handleCalculatorInput(ctx context.Context, b *bot.Bot, update *models.Update, text string) bool {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return false
	}
	uid, chatID := update.Message.From.ID, update.Message.Chat.ID
	isAdmin, err := IsAdmin(ctx, a.DB, a.Config, uid)
	if err != nil || isAdmin {
		return false
	}
	var session calculatorSession
	found, err := loadWorkflowSession(ctx, a.DB, "calculator", uid, &session)
	if err != nil || !found || session.Step != "awaiting_calc_amount" {
		return false
	}
	amount, parseErr := decimal.NewFromString(strings.TrimSpace(text))
	if parseErr != nil || !amount.IsPositive() {
		a.sendText(ctx, b, chatID, "❌ সঠিক সংখ্যা (Amount) টাইপ করুন।", nil)
		return true
	}
	min, max, err := GetMinMaxLimits(ctx, a.DB)
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Sell limit যাচাই করা যায়নি।", nil)
		return true
	}
	if amount.LessThan(min) || amount.GreaterThan(max) {
		a.sendText(ctx, b, chatID, fmt.Sprintf("❌ অ্যামাউন্ট অবশ্যই <code>%s</code> থেকে <code>%s</code> USDT-এর মধ্যে হতে হবে। পুনরায় সঠিক অ্যামাউন্ট লিখুন:", min, max), nil)
		return true
	}
	gross, rate, _, err := CalculatePayout(ctx, a.DB, amount, "")
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ বর্তমান Rate দিয়ে হিসাব করা যায়নি।", nil)
		return true
	}
	fixedFee := decimal.NewFromInt(5)
	otherNet := gross.Sub(fixedFee)
	if otherNet.IsNegative() {
		otherNet = decimal.Zero
	}
	otherRounded, otherDiff := roundDownTen(otherNet)
	rechargeRounded, rechargeDiff := roundDownTen(gross)
	_ = deleteWorkflowSession(ctx, a.DB, "calculator", uid)
	result := fmt.Sprintf("🧮 <b>Live Rate Calculation Preview</b>\n\n💰 <b>Amount:</b> <code>%s USDT</code>\n📈 <b>Rate:</b> ৳<code>%s</code> BDT / USDT\n💵 <b>Gross BDT:</b> ৳<code>%s</code>\n\n📥 <b>Net Receivable Amounts:</b>\n• <b>Other Methods (Fee 5৳):</b> <b>৳<code>%s</code> BDT</b> (Wallet Adj: +%s৳)\n• <b>Mobile Recharge (0৳ fee):</b> <b>৳<code>%s</code> BDT</b> (Wallet Adj: +%s৳)", amount, Money(rate), Money(gross), Money(otherRounded), Money(otherDiff), Money(rechargeRounded), Money(rechargeDiff))
	a.sendText(ctx, b, chatID, result, MainMenu())
	return true
}

func (a *BotApp) CancelHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return
	}
	uid, chatID := update.Message.From.ID, update.Message.Chat.ID
	a.cancelAllSessions(ctx, uid)
	isAdmin, _ := IsAdmin(ctx, a.DB, a.Config, uid)
	if isAdmin {
		po, pp, pw, ps, err := a.AdminCounts(ctx)
		if err == nil {
			a.sendText(ctx, b, chatID, "🚫 <b>Operation cancelled.</b>", AdminMenuWithCounts(uid, a.Config.AdminID, po, pp, pw, ps))
			return
		}
		a.sendText(ctx, b, chatID, "🚫 <b>Operation cancelled.</b>", PermanentAdminOthersMenu())
		return
	}
	a.sendText(ctx, b, chatID, "🚫 <b>Operation cancelled.</b>", MainMenu())
}

func (a *BotApp) cancelAllSessions(ctx context.Context, userID int64) {
	sellSessions.Delete(userID)
	walletSessions.Delete(userID)
	withdrawRejectSessions.Delete(userID)
	supportSessions.Delete(userID)
	supportReplySessions.Delete(userID)
	adminRejectSessions.Delete(userID)
	payoutEntrySessions.Delete(userID)
	adminToolSessions.Delete(userID)
	advancedAdminSessions.Delete(userID)
	for _, kind := range []string{"sell", "wallet", "support", "support_reply", "admin_reject", "payout_entry", "withdraw_reject", "admin_tool", "calculator"} {
		if err := deleteWorkflowSession(ctx, a.DB, kind, userID); err != nil {
			// Cancellation must still clear in-memory state; persistent cleanup is retried on expiry.
			continue
		}
	}
	_, _ = a.DB.Sessions.DeleteOne(ctx, bson.M{"session_key": fmt.Sprintf("admin:%d", userID)})
}
