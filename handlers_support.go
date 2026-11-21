package main

import (
	"context"
	"fmt"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- merged from support_handlers.go ----

type supportSession struct {
	Step     string
	TargetID string
	TicketID string
}

var supportSessions sync.Map      // Telegram user ID -> supportSession
var supportReplySessions sync.Map // admin Telegram user ID -> ticket ID

func (a *BotApp) SupportMessageHandler(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	if update == nil || update.Message == nil || update.Message.From == nil {
		return false
	}
	msg, user := update.Message, update.Message.From
	uid, chatID := user.ID, msg.Chat.ID
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		text = strings.TrimSpace(msg.Caption)
	}

	// Admin's next message is routed to the selected open ticket.
	v, replyOK := supportReplySessions.Load(uid)
	if !replyOK {
		var restored string
		if found, err := loadWorkflowSession(ctx, a.DB, "support_reply", uid, &restored); err == nil && found {
			v, replyOK = restored, true
			supportReplySessions.Store(uid, restored)
		}
	}
	if replyOK {
		ticketID, _ := v.(string)
		var ticket bson.M
		if err := a.DB.Tickets.FindOne(ctx, bson.M{"ticket_id": ticketID, "status": "open"}).Decode(&ticket); err != nil {
			supportReplySessions.Delete(uid)
			_ = deleteWorkflowSession(ctx, a.DB, "support_reply", uid)
			a.sendText(ctx, b, chatID, "⚠️ এই টিকিটটি ইতিমধ্যে বন্ধ বা সমাধান করা হয়েছে!", nil)
			return true
		}
		if text == "" && len(msg.Photo) == 0 && msg.Video == nil && msg.Document == nil && !hasAudioOrVoice(msg) {
			a.sendText(ctx, b, chatID, "দয়া করে Text, Photo, Video অথবা Document রিপ্লাই পাঠান।", nil)
			return true
		}
		replyBody := text
		if replyBody == "" {
			replyBody = "Media/File"
		}
		_, err := a.DB.Tickets.UpdateOne(ctx, bson.M{"ticket_id": ticketID, "status": "open"}, bson.M{"$push": bson.M{"chat_history": "admin: " + replyBody}})
		if err != nil {
			a.sendText(ctx, b, chatID, "❌ Reply সংরক্ষণ করা যায়নি।", nil)
			return true
		}
		targetUID := orderInt64(ticket, "user_id")
		var sendErr error
		captionText := text
		if captionText == "" {
			captionText = "Media/File"
		}
		caption := fmt.Sprintf("💬 <b>ⒹⓇ Reply (Ticket: <code>%s</code>)</b>\n\n🔸 <b>%s</b>", htmlEscape(ticketID), htmlEscape(captionText))
		if len(msg.Photo) > 0 {
			photoID := msg.Photo[len(msg.Photo)-1].FileID
			_, sendErr = b.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: targetUID, Photo: &models.InputFileString{Data: photoID}, Caption: caption, ParseMode: models.ParseModeHTML})
		} else if msg.Video != nil {
			_, sendErr = b.SendVideo(ctx, &bot.SendVideoParams{ChatID: targetUID, Video: &models.InputFileString{Data: msg.Video.FileID}, Caption: caption, ParseMode: models.ParseModeHTML})
		} else if msg.Document != nil {
			_, sendErr = b.SendDocument(ctx, &bot.SendDocumentParams{ChatID: targetUID, Document: &models.InputFileString{Data: msg.Document.FileID}, Caption: caption, ParseMode: models.ParseModeHTML})
		} else {
			_, sendErr = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: targetUID, Text: caption, ParseMode: models.ParseModeHTML})
		}
		if sendErr != nil {
			a.sendText(ctx, b, chatID, "❌ ইউজারকে মেসেজ পাঠানো যায়নি: "+sendErr.Error(), nil)
			return true
		}
		supportReplySessions.Delete(uid)
		_ = deleteWorkflowSession(ctx, a.DB, "support_reply", uid)
		_ = LogAdminAction(ctx, a.DB, uid, user.Username, orderString(ticket, "order_id", "N/A"), ticketID, "Support Reply", "0", "0")
		a.sendText(ctx, b, chatID, "✅ ইউজারের কাছে উত্তর সফলভাবে পাঠানো হয়েছে।", nil)
		return true
	}

	if isAdmin, _ := IsAdmin(ctx, a.DB, a.Config, uid); isAdmin {
		return false
	}
	banned, banErr := IsUserBanned(ctx, a.DB, uid)
	if banErr != nil || banned {
		if banned {
			a.sendText(ctx, b, chatID, "❌ আপনি এই বট থেকে ব্যান (Banned) হয়েছেন।", nil)
		}
		return true
	}
	if text == "⚠️ Payment Support" {
		a.cancelAllSessions(ctx, uid)
		supportSessions.Store(uid, supportSession{Step: "waiting_for_payment_issue_id"})
		_ = saveWorkflowSession(ctx, a.DB, "support", uid, supportSession{Step: "waiting_for_payment_issue_id"}, 7*24*time.Hour)
		a.sendText(ctx, b, chatID, "⚠️ <b>Payment Support Request</b>\n\nদয়া করে সমস্যার সংশ্লিষ্ট <b>Order/Withdraw ID</b> শুধু সংখ্যায় লিখুন (যেমন: <code>1234567</code>):", &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✖️ Cancel", CallbackData: "cancel"}}}})
		return true
	}
	if text == "🔍 Track Support Ticket" {
		a.cancelAllSessions(ctx, uid)
		supportSessions.Store(uid, supportSession{Step: "waiting_for_ticket_search"})
		_ = saveWorkflowSession(ctx, a.DB, "support", uid, supportSession{Step: "waiting_for_ticket_search"}, 7*24*time.Hour)
		a.sendText(ctx, b, chatID, "🔍 <b>Support Ticket Tracking</b>\nআপনার Ticket ID লিখে পাঠান (যেমন: <code>#TICK-...</code>)।", &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✖️ Cancel", CallbackData: "cancel"}}}})
		return true
	}
	v, ok := supportSessions.Load(uid)
	if !ok {
		var restored supportSession
		if found, err := loadWorkflowSession(ctx, a.DB, "support", uid, &restored); err == nil && found {
			v, ok = restored, true
			supportSessions.Store(uid, restored)
		}
	}
	if !ok {
		return false
	}
	s, ok := v.(supportSession)
	if !ok {
		supportSessions.Delete(uid)
		_ = deleteWorkflowSession(ctx, a.DB, "support", uid)
		return false
	}
	switch s.Step {
	case "waiting_for_ticket_search":
		supportSessions.Delete(uid)
		_ = deleteWorkflowSession(ctx, a.DB, "support", uid)
		ticketID := text
		if !strings.HasPrefix(ticketID, "#") {
			ticketID = "#" + ticketID
		}
		var ticket bson.M
		if err := a.DB.Tickets.FindOne(ctx, bson.M{"ticket_id": ticketID, "user_id": uid}).Decode(&ticket); err != nil {
			a.sendText(ctx, b, chatID, fmt.Sprintf("❌ আপনার একাউন্টে %s দিয়ে কোনো টিকিট পাওয়া যায়নি।", htmlEscape(ticketID)), SupportMenu())
			return true
		}
		status := map[string]string{"open": "সচল/অপেক্ষমাণ (Open) ⏳", "resolved": "সমাধানকৃত (Resolved) ✅", "cancelled": "বাতিলকৃত (Cancelled) ❌"}[orderString(ticket, "status", "")]
		a.sendText(ctx, b, chatID, fmt.Sprintf("🔍 <b>Ticket Status Result</b>\n\n🎫 Ticket ID: <code>%s</code>\n📌 Order ID: <code>#%s</code>\n📌 Status: <code>%s</code>\n💬 Problem: <b>%s</b>\n⏱️ Time: <i>%s</i>", htmlEscape(ticketID), htmlEscape(orderString(ticket, "order_id", "N/A")), htmlEscape(status), htmlEscape(orderString(ticket, "details", "")), htmlEscape(orderString(ticket, "created_at", ""))), SupportMenu())
		return true
	case "waiting_for_payment_issue_id":
		fields := strings.FieldsFunc(text, func(r rune) bool { return r < '0' || r > '9' })
		if len(fields) == 0 {
			a.sendText(ctx, b, chatID, "⚠️ সঠিক Order/Withdraw ID দিন।", nil)
			return true
		}
		id, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			a.sendText(ctx, b, chatID, "⚠️ ID সঠিক নয়। আবার দিন।", nil)
			return true
		}
		var order, withdrawal bson.M
		oErr := a.DB.Orders.FindOne(ctx, bson.M{"id": id, "user_id": uid}).Decode(&order)
		wErr := a.DB.WithdrawRequests.FindOne(ctx, bson.M{"id": id, "user_id": uid}).Decode(&withdrawal)
		if oErr != nil && wErr != nil {
			a.sendText(ctx, b, chatID, "⚠️ আপনার নিজের কোনো Valid Order ID বা Withdraw ID পাওয়া যায়নি। আবার দিন।", nil)
			return true
		}
		if oErr == nil && orderString(order, "status", "") == "paid" {
			supportSessions.Delete(uid)
			_ = deleteWorkflowSession(ctx, a.DB, "support", uid)
			a.sendText(ctx, b, chatID, "⚠️ এই অর্ডারের পেমেন্ট ইতিমধ্যে সম্পন্ন হয়েছে; নতুন টিকিট খোলা যাবে না।", SupportMenu())
			return true
		}
		count, _ := a.DB.Tickets.CountDocuments(ctx, bson.M{"order_id": strconv.FormatInt(id, 10)})
		if count >= 2 {
			supportSessions.Delete(uid)
			_ = deleteWorkflowSession(ctx, a.DB, "support", uid)
			a.sendText(ctx, b, chatID, "⚠️ এই ID দিয়ে সর্বাধিক ২টি সাপোর্ট টিকিট তৈরি করা হয়েছে।", SupportMenu())
			return true
		}
		s.TargetID = strconv.FormatInt(id, 10)
		s.Step = "waiting_for_payment_issue_details"
		supportSessions.Store(uid, s)
		_ = saveWorkflowSession(ctx, a.DB, "support", uid, s, 7*24*time.Hour)
		a.sendText(ctx, b, chatID, fmt.Sprintf("✅ ID: <code>#%s</code> সমর্থিত হয়েছে।\nএখন সমস্যার বিস্তারিত বিবরণ লিখে পাঠান।", s.TargetID), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✖️ Cancel", CallbackData: "cancel"}}}})
		return true
	case "waiting_for_payment_issue_details":
		if text == "" && len(msg.Photo) == 0 && msg.Video == nil && msg.Document == nil && !hasAudioOrVoice(msg) {
			a.sendText(ctx, b, chatID, "দয়া করে সমস্যার বিস্তারিত, Screenshot, Video অথবা Document পাঠান।", nil)
			return true
		}
		if text == "" {
			text = "Media/Proof Attached"
		}
		ticketID := fmt.Sprintf("#TICK-%d-%d", uid, msg.ID)
		mediaFileID, mediaType := "", ""
		if len(msg.Photo) > 0 {
			mediaFileID = msg.Photo[len(msg.Photo)-1].FileID
			mediaType = "photo"
		}
		if msg.Video != nil {
			mediaFileID = msg.Video.FileID
			mediaType = "video"
		}
		if msg.Document != nil {
			mediaFileID = msg.Document.FileID
			mediaType = "document"
		}
		doc := bson.M{"user_id": uid, "ticket_id": ticketID, "order_id": s.TargetID, "details": text, "media_file_id": mediaFileID, "media_type": mediaType, "status": "open", "replied_admin_msg_ids": bson.A{}, "chat_history": []string{"user: " + text}, "created_at": BangladeshTime()}
		if _, err := a.DB.Tickets.InsertOne(ctx, doc); err != nil {
			a.sendText(ctx, b, chatID, "❌ টিকিট তৈরি করা যায়নি। আবার চেষ্টা করুন।", SupportMenu())
			supportSessions.Delete(uid)
			_ = deleteWorkflowSession(ctx, a.DB, "support", uid)
			return true
		}
		name := telegramFullName(user)
		if name == "" {
			name = "N/A"
		}
		username := user.Username
		if username == "" {
			username = "N/A"
		}
		adminText := fmt.Sprintf("🚨 <b>New Payment Issue Ticket Created!</b>\n\n🎫 Ticket ID: <code>%s</code>\n📌 ID / Order ID: <code>#%s</code>\n👤 User Name: <b>%s</b>\n🏷️ Username: <b>@%s</b>\n🆔 User Telegram ID: <code>%d</code>\n\n📝 User Details:\n<b>%s</b>", htmlEscape(ticketID), s.TargetID, htmlEscape(name), htmlEscape(username), uid, htmlEscape(text))
		kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✍️ Reply to User", CallbackData: fmt.Sprintf("support_reply:%d:%s", uid, ticketID)}, {Text: "💬 View Support Chat", CallbackData: "chat_backup:" + ticketID}, {Text: "✅ Mark as Resolved", CallbackData: "resolve_ticket:" + ticketID}}, {{Text: "✖️ Cancel", CallbackData: "cancel_ticket:" + ticketID}}}}
		var sendErr error
		switch mediaType {
		case "photo":
			_, sendErr = b.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: a.Config.AdminID, Photo: &models.InputFileString{Data: mediaFileID}, Caption: adminText, ParseMode: models.ParseModeHTML, ReplyMarkup: kb})
		case "video":
			_, sendErr = b.SendVideo(ctx, &bot.SendVideoParams{ChatID: a.Config.AdminID, Video: &models.InputFileString{Data: mediaFileID}, Caption: adminText, ParseMode: models.ParseModeHTML, ReplyMarkup: kb})
		case "document":
			_, sendErr = b.SendDocument(ctx, &bot.SendDocumentParams{ChatID: a.Config.AdminID, Document: &models.InputFileString{Data: mediaFileID}, Caption: adminText, ParseMode: models.ParseModeHTML, ReplyMarkup: kb})
		default:
			_, sendErr = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: a.Config.AdminID, Text: adminText, ParseMode: models.ParseModeHTML, ReplyMarkup: kb})
		}
		supportSessions.Delete(uid)
		_ = deleteWorkflowSession(ctx, a.DB, "support", uid)
		if sendErr != nil {
			a.sendText(ctx, b, chatID, "⚠️ টিকিট তৈরি হয়েছে, তবে অ্যাডমিন নোটিফিকেশন পাঠানো যায়নি।", SupportMenu())
			return true
		}
		a.sendText(ctx, b, chatID, fmt.Sprintf("✅ আপনার তথ্য জমা হয়েছে।\n🎫 Ticket ID: <code>%s</code>\n📌 ID: <code>#%s</code>\nসাপোর্ট টিম যাচাই করছে।", htmlEscape(ticketID), s.TargetID), SupportMenu())
		return true
	}
	return false
}

func (a *BotApp) SupportCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	if update == nil || update.CallbackQuery == nil {
		return false
	}
	q := update.CallbackQuery
	uid := q.From.ID
	data := q.Data
	if data == "close_faq" {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
		if q.Message != nil {
			_, _ = b.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: q.Message.Chat.ID, MessageID: q.Message.ID, Text: "❓ Help Center বন্ধ করা হয়েছে।"})
		}
		return true
	}
	answer := func(t string, alert bool) {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: t, ShowAlert: alert})
	}
	if data == "cancel" {
		_, inMemory := supportSessions.Load(uid)
		var restored supportSession
		restoredOK := false
		if !inMemory {
			restoredOK, _ = loadWorkflowSession(ctx, a.DB, "support", uid, &restored)
		}
		if inMemory || restoredOK {
			supportSessions.Delete(uid)
			_ = deleteWorkflowSession(ctx, a.DB, "support", uid)
			answer("Operation cancelled", false)
			a.sendText(ctx, b, uid, "🚫 <b>Operation Cancelled.</b>", MainMenu())
			return true
		}
	}
	if data == "chat_backup_back" {
		answer("", false)
		return true
	}
	if strings.HasPrefix(data, "support_reply:") {
		admin, err := IsAdmin(ctx, a.DB, a.Config, uid)
		if err != nil || !admin {
			answer("এই কাজের অনুমতি নেই।", true)
			return true
		}
		parts := strings.SplitN(data, ":", 3)
		if len(parts) != 3 {
			answer("Invalid ticket", true)
			return true
		}
		var ticket bson.M
		if err := a.DB.Tickets.FindOne(ctx, bson.M{"ticket_id": parts[2], "status": "open"}).Decode(&ticket); err != nil {
			answer("টিকিটটি Open নেই।", true)
			return true
		}
		supportReplySessions.Store(uid, parts[2])
		_ = saveWorkflowSession(ctx, a.DB, "support_reply", uid, parts[2], 24*time.Hour)
		answer("এখন আপনার রিপ্লাই লিখে পাঠান।", true)
		a.sendText(ctx, b, uid, "✍️ <b>Support Reply Mode</b>\nনিচে ইউজারের জন্য রিপ্লাই লিখুন।", nil)
		return true
	}
	if strings.HasPrefix(data, "resolve_ticket:") || strings.HasPrefix(data, "cancel_ticket:") {
		admin, err := IsAdmin(ctx, a.DB, a.Config, uid)
		if err != nil || !admin {
			answer("এই কাজের অনুমতি নেই।", true)
			return true
		}
		parts := strings.SplitN(data, ":", 2)
		ticketID := parts[1]
		status := "resolved"
		label := "Ticket Resolved"
		if strings.HasPrefix(data, "cancel_ticket:") {
			status = "cancelled"
			label = "Ticket Cancelled"
		}
		var ticket bson.M
		if err := a.DB.Tickets.FindOne(ctx, bson.M{"ticket_id": ticketID, "status": "open"}).Decode(&ticket); err != nil {
			answer("টিকিটটি ইতিমধ্যে বন্ধ।", true)
			return true
		}
		res, err := a.DB.Tickets.UpdateOne(ctx, bson.M{"ticket_id": ticketID, "status": "open"}, bson.M{"$set": bson.M{"status": status}})
		if err != nil || res.ModifiedCount != 1 {
			answer("টিকিট আপডেট করা যায়নি।", true)
			return true
		}
		_ = LogAdminAction(ctx, a.DB, uid, q.From.Username, orderString(ticket, "order_id", "N/A"), ticketID, label, "0", "0")
		target := orderInt64(ticket, "user_id")
		a.sendText(ctx, b, target, fmt.Sprintf("%s আপনার সাপোর্ট টিকিট <code>%s</code>।", map[string]string{"resolved": "✅ সমাধান করা হয়েছে", "cancelled": "❌ বাতিল করা হয়েছে"}[status], htmlEscape(ticketID)), SupportMenu())
		answer("Ticket updated", false)
		return true
	}
	if strings.HasPrefix(data, "chat_backup:") {
		ticketID := strings.TrimPrefix(data, "chat_backup:")
		var ticket bson.M
		if err := a.DB.Tickets.FindOne(ctx, bson.M{"ticket_id": ticketID}).Decode(&ticket); err != nil {
			answer("❌ কোনো চ্যাট হিস্ট্রি পাওয়া যায়নি।", true)
			return true
		}
		admin, err := IsAdmin(ctx, a.DB, a.Config, uid)
		if err != nil || (!admin && orderInt64(ticket, "user_id") != uid) {
			answer("❌ আপনার এই চ্যাট দেখার অনুমতি নেই।", true)
			return true
		}
		answer("Chat history", false)
		a.ChatBackup(ctx, b, uid, ticketID)
		return true
	}
	return false
}

func (a *BotApp) ShowAdminSupportTickets(ctx context.Context, b *bot.Bot, chatID int64) {
	cursor, err := a.DB.Tickets.Find(ctx, bson.M{"status": "open"}, options.Find().SetSort(bson.D{{Key: "id", Value: -1}}).SetLimit(15))
	if err != nil {
		a.sendText(ctx, b, chatID, "❌ Support tickets লোড করা যায়নি।", nil)
		return
	}
	defer cursor.Close(ctx)
	found := false
	for cursor.Next(ctx) {
		var r bson.M
		if cursor.Decode(&r) != nil {
			continue
		}
		found = true
		id := orderString(r, "ticket_id", "")
		orderLine := ""
		if orderString(r, "order_id", "") != "" {
			orderLine = "📌 <b>Order ID:</b> <code>#" + htmlEscape(orderString(r, "order_id", "")) + "</code>\n"
		}
		txt := fmt.Sprintf("🎫 Ticket ID: <code>%s</code>\n%s📌 Status: <code>open</code>\n💬 Problem: <b>%s</b>\n⏱️ Time: <i>%s</i>", htmlEscape(id), orderLine, htmlEscape(orderString(r, "details", "")), htmlEscape(orderString(r, "created_at", "")))
		kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✍️ Reply to User", CallbackData: fmt.Sprintf("support_reply:%d:%s", orderInt64(r, "user_id"), id)}, {Text: "💬 View Support Chat", CallbackData: "chat_backup:" + id}, {Text: "✅ Mark as Resolved", CallbackData: "resolve_ticket:" + id}}, {{Text: "✖️ Cancel", CallbackData: "cancel_ticket:" + id}}}}
		mediaID := orderString(r, "media_file_id", "")
		mediaType := orderString(r, "media_type", "")
		if mediaID != "" && a.sendTicketAttachment(ctx, b, chatID, mediaType, mediaID, txt, kb) {
			continue
		}
		a.sendText(ctx, b, chatID, txt, kb)
	}
	if !found {
		a.sendText(ctx, b, chatID, "কোনো ওপেন সাপোর্ট টিকিট নেই।", AdminMenuWithCounts(chatID, a.Config.AdminID, 0, 0, 0, 0))
	}
}

// sendTicketAttachment mirrors Python's photo/video/document ticket preview.
func (a *BotApp) sendTicketAttachment(ctx context.Context, b *bot.Bot, chatID int64, mediaType, fileID, caption string, markup models.ReplyMarkup) bool {
	input := &models.InputFileString{Data: fileID}
	var err error
	switch mediaType {
	case "photo":
		_, err = b.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: chatID, Photo: input, Caption: caption, ParseMode: models.ParseModeHTML, ReplyMarkup: markup})
	case "video":
		_, err = b.SendVideo(ctx, &bot.SendVideoParams{ChatID: chatID, Video: input, Caption: caption, ParseMode: models.ParseModeHTML, ReplyMarkup: markup})
	case "document":
		_, err = b.SendDocument(ctx, &bot.SendDocumentParams{ChatID: chatID, Document: input, Caption: caption, ParseMode: models.ParseModeHTML, ReplyMarkup: markup})
	default:
		return false
	}
	return err == nil
}

// hasAudioOrVoice detects the two Telegram message variants accepted by the
// Python global update filter. The source Python handler records their text
// fallback but does not persist or forward the underlying media file.
func hasAudioOrVoice(msg *models.Message) bool {
	if msg == nil {
		return false
	}
	v := reflect.ValueOf(msg)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	for _, name := range []string{"Audio", "Voice"} {
		f := v.FieldByName(name)
		if !f.IsValid() {
			continue
		}
		switch f.Kind() {
		case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice:
			if !f.IsNil() {
				return true
			}
		default:
			if !f.IsZero() {
				return true
			}
		}
	}
	return false
}

// ---- merged from session_store.go ----

// Workflow sessions are stored separately from business records. Payload is
// BSON (not JSON) so int64 IDs and nested values retain their native types.
// User workflows live for seven days; admin input workflows use 24 hours.
func workflowSessionKey(kind string, userID int64) string {
	return fmt.Sprintf("workflow:%s:%d", kind, userID)
}

func saveWorkflowSession(ctx context.Context, db *Database, kind string, userID int64, value any, ttl time.Duration) error {
	if db == nil || db.Sessions == nil || userID <= 0 {
		return fmt.Errorf("session store is not initialized")
	}
	payload, err := bson.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s session: %w", kind, err)
	}
	now := time.Now().UTC()
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	_, err = db.Sessions.UpdateOne(ctx,
		bson.M{"session_key": workflowSessionKey(kind, userID)},
		bson.M{"$set": bson.M{
			"session_key": workflowSessionKey(kind, userID),
			"kind":        kind,
			"user_id":     userID,
			"payload":     bson.Raw(payload),
			"updated_at":  now,
			"expires_at":  now.Add(ttl),
		}}, options.Update().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("persist %s session: %w", kind, err)
	}
	return nil
}

func loadWorkflowSession(ctx context.Context, db *Database, kind string, userID int64, target any) (bool, error) {
	if db == nil || db.Sessions == nil || userID <= 0 {
		return false, fmt.Errorf("session store is not initialized")
	}
	var row struct {
		Payload bson.Raw `bson:"payload"`
	}
	err := db.Sessions.FindOne(ctx, bson.M{
		"session_key": workflowSessionKey(kind, userID),
		"expires_at":  bson.M{"$gt": time.Now().UTC()},
	}).Decode(&row)
	if err == mongo.ErrNoDocuments {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(row.Payload) == 0 {
		return false, fmt.Errorf("empty %s session payload", kind)
	}
	if err := bson.Unmarshal(row.Payload, target); err != nil {
		return false, fmt.Errorf("decode %s session: %w", kind, err)
	}
	return true, nil
}

func deleteWorkflowSession(ctx context.Context, db *Database, kind string, userID int64) error {
	if db == nil || db.Sessions == nil || userID <= 0 {
		return nil
	}
	_, err := db.Sessions.DeleteOne(ctx, bson.M{"session_key": workflowSessionKey(kind, userID)})
	return err
}
