package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

const maxTelegramUpdateBytes = 1 << 20

const (
	menuMarketplace        = "🛍 Marketplace"
	menuProfile            = "👤 My Profile"
	menuOrders             = "📦 My Orders"
	menuEarnings           = "💰 My Earnings"
	menuWithdraw           = "🏦 Withdraw"
	menuSupport            = "🎧 Support"
	menuCreateTask         = "➕ Create Task"
	menuMyTasks            = "📋 My Tasks"
	menuWorkerTasks        = "🧰 Available Tasks"
	menuMyApplications     = "📋 My Applications"
	menuMyWork             = "🧰 My Work"
	menuSellerApplications = "👥 Applications"
	menuSellerSubmissions  = "📨 Submissions"
	menuAdmin              = "🛡 Admin Panel"
	menuAdminDashboard     = "📊 Dashboard"
	menuPending            = "🕒 Pending Tasks"
	menuAdminUsers         = "👥 Manage Users"
	menuAdminSellers       = "🏪 Manage Sellers"
	menuAdminWorkers       = "🧰 Manage Workers"
	menuAdminPayments      = "💳 Payment History"
	menuAdminWithdrawals   = "🏦 Withdrawals"
	menuAdminHistory       = "🧾 Transaction History"
	menuAdminMarketplace   = "🛍 Manage Marketplace"
	menuAdminSettings      = "⚙️ Settings"
	menuMain               = "🏠 Main Menu"
	menuCancel             = "❌ Cancel"
)

type TelegramUpdate struct {
	UpdateID         int64                  `json:"update_id"`
	Message          *TelegramMessage       `json:"message,omitempty"`
	CallbackQuery    *TelegramCallbackQuery `json:"callback_query,omitempty"`
	PreCheckoutQuery *PreCheckoutQuery      `json:"pre_checkout_query,omitempty"`
}

type TelegramCallbackQuery struct {
	ID      string           `json:"id"`
	From    TelegramUser     `json:"from"`
	Data    string           `json:"data"`
	Message *TelegramMessage `json:"message,omitempty"`
}

type PreCheckoutQuery struct {
	ID             string       `json:"id"`
	From           TelegramUser `json:"from"`
	Currency       string       `json:"currency"`
	TotalAmount    int64        `json:"total_amount"`
	InvoicePayload string       `json:"invoice_payload"`
}

type SuccessfulPayment struct {
	Currency                string `json:"currency"`
	TotalAmount             int64  `json:"total_amount"`
	InvoicePayload          string `json:"invoice_payload"`
	TelegramPaymentChargeID string `json:"telegram_payment_charge_id"`
}

type TelegramMessage struct {
	Text              string              `json:"text,omitempty"`
	Caption           string              `json:"caption,omitempty"`
	Photo             []TelegramPhotoSize `json:"photo,omitempty"`
	Chat              TelegramChat        `json:"chat"`
	From              *TelegramUser       `json:"from,omitempty"`
	SuccessfulPayment *SuccessfulPayment  `json:"successful_payment,omitempty"`
}

type TelegramPhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type TelegramChat struct {
	ID int64 `json:"id"`
}
type TelegramUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
}

type App struct {
	Config      Config
	DB          *Database
	Bot         *TelegramClient
	Users       *UserStore
	Marketplace *MarketplaceStore
	Payments    *PaymentStore
	TaskFlow    *TaskFlowStore
}

func NewApp(cfg Config, db *Database, bot *TelegramClient) *App {
	return &App{
		Config:      cfg,
		DB:          db,
		Bot:         bot,
		Users:       NewUserStore(db),
		Marketplace: NewMarketplaceStore(db),
		Payments:    NewPaymentStore(db),
		TaskFlow:    NewTaskFlowStore(db),
	}
}

func MainMenuKeyboard(isAdmin, isSeller bool) *ReplyKeyboardMarkup {
	if isAdmin {
		return AdminKeyboard()
	}
	if isSeller {
		return SellerKeyboard(false)
	}
	return UserKeyboard()
}

// UserKeyboard never exposes Seller registration or Seller tools.
func UserKeyboard() *ReplyKeyboardMarkup {
	return &ReplyKeyboardMarkup{
		Keyboard: [][]ReplyKeyboardButton{
			{{Text: menuMarketplace}, {Text: menuMyApplications}},
			{{Text: menuMyWork}, {Text: menuProfile}},
			{{Text: menuOrders}, {Text: menuEarnings}},
			{{Text: menuWithdraw}, {Text: menuSupport}},
		},
		ResizeKeyboard: true, IsPersistent: true,
		InputFieldPlaceholder: "Choose an option...",
	}
}

// WorkerKeyboard is separate from User, Seller, and Admin menus.
func WorkerKeyboard() *ReplyKeyboardMarkup {
	return &ReplyKeyboardMarkup{
		Keyboard: [][]ReplyKeyboardButton{
			{{Text: menuMarketplace}, {Text: menuWorkerTasks}},
			{{Text: menuMyApplications}, {Text: menuMyWork}},
			{{Text: menuOrders}, {Text: menuEarnings}},
			{{Text: menuWithdraw}, {Text: menuSupport}},
			{{Text: menuProfile}},
		},
		ResizeKeyboard: true, IsPersistent: true,
	}
}

// SellerKeyboard is shown only after a verified Seller registration payment.
func SellerKeyboard(_ bool) *ReplyKeyboardMarkup {
	return &ReplyKeyboardMarkup{
		Keyboard: [][]ReplyKeyboardButton{
			{{Text: menuCreateTask}, {Text: menuMyTasks}},
			{{Text: menuSellerApplications}, {Text: menuSellerSubmissions}},
			{{Text: menuMarketplace}, {Text: menuProfile}},
			{{Text: menuMain}},
		},
		ResizeKeyboard: true, IsPersistent: true,
	}
}

func AdminKeyboard() *ReplyKeyboardMarkup {
	return &ReplyKeyboardMarkup{
		Keyboard: [][]ReplyKeyboardButton{
			{{Text: menuAdminDashboard}, {Text: menuPending}},
			{{Text: menuAdminUsers}, {Text: menuAdminSellers}},
			{{Text: menuAdminWorkers}, {Text: menuAdminPayments}},
			{{Text: menuAdminWithdrawals}, {Text: menuAdminHistory}},
			{{Text: menuSellerSubmissions}, {Text: menuAdminMarketplace}},
			{{Text: menuAdminSettings}},
			{{Text: menuCreateTask}, {Text: menuMyTasks}},
			{{Text: menuMain}},
		},
		ResizeKeyboard:        true,
		IsPersistent:          true,
		InputFieldPlaceholder: "Admin controls...",
	}
}

func (a *App) TelegramWebhookHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != a.Config.WebhookSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxTelegramUpdateBytes)
		defer r.Body.Close()
		var update TelegramUpdate
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			http.Error(w, "invalid Telegram update", http.StatusBadRequest)
			return
		}
		// Payment updates are processed before acknowledging the webhook so a successful
		// payment is not lost if the process restarts immediately after an early HTTP 200.
		if update.PreCheckoutQuery != nil || (update.Message != nil && update.Message.SuccessfulPayment != nil) {
			a.ProcessUpdate(update)
			w.WriteHeader(http.StatusOK)
			return
		}

		w.WriteHeader(http.StatusOK)
		go a.ProcessUpdate(update)
	})
}

func (a *App) ProcessUpdate(update TelegramUpdate) {
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()

	if update.PreCheckoutQuery != nil {
		a.handlePreCheckout(ctx, update.PreCheckoutQuery)
		return
	}
	if update.CallbackQuery != nil {
		a.handleCallbackQuery(ctx, update.CallbackQuery)
		return
	}
	if update.Message == nil || update.Message.From == nil {
		return
	}
	msg := update.Message
	if err := a.Users.UpsertTelegramUser(ctx, msg.From.ID, msg.From.Username, msg.From.FirstName); err != nil {
		slog.Error("user registration failed", "error", err, "telegram_id", msg.From.ID)
		return
	}

	if msg.SuccessfulPayment != nil {
		a.handleSuccessfulSellerPayment(ctx, msg.Chat.ID, msg.From.ID, msg.SuccessfulPayment)
		return
	}

	// Block suspended/banned accounts before processing any menu or command.
	// Admin access is controlled separately by the server-side ADMIN_IDS allowlist.
	if !a.IsAdmin(msg.From.ID) {
		user, userErr := a.Users.GetByTelegramID(ctx, msg.From.ID)
		if userErr == nil && (user.Status == "banned" || user.Status == "suspended") {
			statusText := "suspended"
			if user.Status == "banned" {
				statusText = "banned"
			}
			a.sendPlain(ctx, msg.Chat.ID, "Your account is currently "+statusText+". Please contact support if you believe this is a mistake.")
			return
		}
	}

	text := strings.TrimSpace(msg.Text)
	// Worker proof submissions accept either text/URL or a Telegram photo.
	if len(msg.Photo) > 0 {
		if appID, sessionErr := a.TaskFlow.GetSubmissionSession(ctx, msg.From.ID); sessionErr == nil {
			best := msg.Photo[len(msg.Photo)-1]
			proof := "photo_file_id:" + best.FileID
			if msg.Caption != "" {
				proof += "\nCaption: " + msg.Caption
			}
			app, _ := a.TaskFlow.GetApplication(ctx, appID)
			if err := a.TaskFlow.Submit(ctx, msg.From.ID, appID, proof); err != nil {
				a.sendPlain(ctx, msg.Chat.ID, "Photo submission was not saved: "+err.Error())
				return
			}
			_ = a.TaskFlow.ClearSubmissionSession(ctx, msg.From.ID)
			if app != nil {
				a.sendPlain(ctx, app.SellerID, fmt.Sprintf("📥 New photo work submission received from Worker %d. Open Submissions to review it.", msg.From.ID))
			}
			a.sendWithKeyboard(ctx, msg.Chat.ID, "✅ Your photo proof has been submitted to the Seller for review.", a.mainMenuKeyboard(ctx, msg.From.ID))
			return
		}
	}
	if text == "" {
		return
	}

	// A worker proof submission session takes priority over menu navigation.
	if !strings.HasPrefix(text, "/") && text != menuCancel {
		if appID, sessionErr := a.TaskFlow.GetSubmissionSession(ctx, msg.From.ID); sessionErr == nil {
			app, _ := a.TaskFlow.GetApplication(ctx, appID)
			if err := a.TaskFlow.Submit(ctx, msg.From.ID, appID, text); err != nil {
				a.sendPlain(ctx, msg.Chat.ID, "Submission was not saved: "+err.Error()+"\nPlease send the proof again, or /cancel.")
				return
			}
			_ = a.TaskFlow.ClearSubmissionSession(ctx, msg.From.ID)
			if app != nil {
				a.sendPlain(ctx, app.SellerID, fmt.Sprintf("📥 New work submission received from Worker %d. Open Submissions to review it.", msg.From.ID))
			}
			a.sendWithKeyboard(ctx, msg.Chat.ID, "✅ Your work proof has been submitted to the Seller for review.", a.mainMenuKeyboard(ctx, msg.From.ID))
			return
		}
	}

	// A pending task draft takes priority over normal menu navigation.
	draft, draftErr := a.Marketplace.GetDraft(ctx, msg.From.ID)
	if draftErr == nil && !strings.HasPrefix(text, "/") && text != menuCancel {
		a.handleTaskDraft(ctx, msg, draft)
		return
	}
	if draftErr != nil && draftErr != mongo.ErrNoDocuments {
		slog.Error("task draft lookup failed", "error", draftErr, "telegram_id", msg.From.ID)
		return
	}

	fields := strings.Fields(text)
	command := strings.ToLower(fields[0])
	switch command {
	case "/start", "/menu":
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, fmt.Sprintf("Welcome, %s! 🚀\n\nWelcome to ⒹⓇ Marketplace.\nYour account is registered.\n\nChoose an option from the menu below.", safeName(msg.From.FirstName)))
		return
	case "/help":
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, fmt.Sprintf("ⒹⓇ Marketplace Help\n\nUse the menu buttons to navigate.\nSend /seller to request Seller registration (%d Telegram Star(s)). Use /cancel to cancel an active task creation.", SellerRegistrationStars))
		return
	case "/status":
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "ⒹⓇ Marketplace is online.")
		return
	case "/seller":
		a.becomeSeller(ctx, msg.Chat.ID, msg.From.ID)
		return
	case "/admin":
		if !a.requireAdmin(msg.Chat.ID, msg.From.ID) {
			return
		}
		a.sendAdminDashboard(ctx, msg.Chat.ID, msg.From.ID)
		return
	case "/cancel":
		_ = a.Marketplace.DeleteDraft(ctx, msg.From.ID)
		_ = a.TaskFlow.ClearSubmissionSession(ctx, msg.From.ID)
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "Current action cancelled.")
		return
	case "/approve_task":
		a.handleTaskModeration(ctx, msg, fields, TaskStatusActive)
		return
	case "/reject_task":
		a.handleTaskModeration(ctx, msg, fields, TaskStatusRejected)
		return
	case "/ban":
		a.handleAccountStatusCommand(ctx, msg, fields, "banned")
		return
	case "/unban", "/restore":
		a.handleAccountStatusCommand(ctx, msg, fields, "active")
		return
	case "/suspend":
		a.handleAccountStatusCommand(ctx, msg, fields, "suspended")
		return
	case "/pause_task":
		a.handleTaskOwnerCommand(ctx, msg, fields, TaskStatusPaused)
		return
	case "/resume_task":
		a.handleTaskOwnerCommand(ctx, msg, fields, TaskStatusActive)
		return
	case "/delete_task":
		a.handleTaskOwnerCommand(ctx, msg, fields, TaskStatusDeleted)
		return
	case "/approve_withdraw":
		a.handleWithdrawalCommand(ctx, msg, fields, "approved")
		return
	case "/reject_withdraw":
		a.handleWithdrawalCommand(ctx, msg, fields, "rejected")
		return
	case "/mark_withdraw_paid":
		a.handleWithdrawalCommand(ctx, msg, fields, "paid")
		return
	case "/set_auto_withdraw_threshold":
		a.handleThresholdCommand(ctx, msg, fields)
		return
	case "/mark_task_funded":
		a.handleMarkTaskFunded(ctx, msg, fields)
		return
	}

	switch text {
	case menuCancel:
		_ = a.Marketplace.DeleteDraft(ctx, msg.From.ID)
		_ = a.TaskFlow.ClearSubmissionSession(ctx, msg.From.ID)
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "Current action cancelled.")
	case menuMain:
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "Main menu")
	case menuProfile, "My Profile":
		a.sendProfile(ctx, msg.Chat.ID, msg.From.ID)
	case menuMarketplace, "Marketplace":
		a.sendTaskList(ctx, msg.Chat.ID, msg.From.ID)
	case menuWorkerTasks, "Available Tasks":
		a.sendTaskList(ctx, msg.Chat.ID, msg.From.ID)
	case menuMyApplications:
		a.sendMyApplications(ctx, msg.Chat.ID, msg.From.ID)
	case menuMyWork:
		a.sendMyWork(ctx, msg.Chat.ID, msg.From.ID)
	case menuSellerApplications:
		a.sendSellerApplications(ctx, msg.Chat.ID, msg.From.ID)
	case menuSellerSubmissions:
		a.sendSellerSubmissions(ctx, msg.Chat.ID, msg.From.ID)
	case menuOrders, "My Orders":
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "📦 My Orders\n\nOrder processing will be enabled in the next development phase.")
	case menuEarnings, "My Earnings":
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "💰 My Earnings\n\nEarnings ledger is not active yet. No withdrawable balance is calculated.")
	case menuWithdraw, "Withdraw":
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "🏦 Withdraw\n\nUSDT withdrawals are not active yet. Please do not send funds to the bot.")
	case menuSupport, "Support":
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "🎧 Support\n\nSupport tickets will be added in a later phase.")
	case menuCreateTask, "Create Task":
		a.beginTaskCreation(ctx, msg.Chat.ID, msg.From.ID)
	case menuMyTasks, "My Tasks":
		a.sendMyTasks(ctx, msg.Chat.ID, msg.From.ID)
	case menuAdmin, "Admin Panel", menuAdminDashboard, "Dashboard":
		if !a.requireAdmin(msg.Chat.ID, msg.From.ID) {
			return
		}
		a.sendAdminDashboard(ctx, msg.Chat.ID, msg.From.ID)
	case menuPending, "Pending Tasks":
		a.sendPendingTasks(ctx, msg.Chat.ID, msg.From.ID)
	case menuAdminUsers, "Manage Users":
		a.sendAdminUserList(ctx, msg.Chat.ID, msg.From.ID, "user")
	case menuAdminSellers, "Manage Sellers":
		a.sendAdminUserList(ctx, msg.Chat.ID, msg.From.ID, "seller")
	case menuAdminWorkers, "Manage Workers":
		a.sendAdminUserList(ctx, msg.Chat.ID, msg.From.ID, "worker")
	case menuAdminPayments, "Payment History":
		a.sendAdminPayments(ctx, msg.Chat.ID, msg.From.ID)
	case menuAdminWithdrawals, "Withdrawals":
		a.sendAdminWithdrawals(ctx, msg.Chat.ID, msg.From.ID)
	case menuAdminHistory, "Transaction History":
		a.sendAdminHistory(ctx, msg.Chat.ID, msg.From.ID)
	case menuAdminMarketplace, "Manage Marketplace":
		a.sendAdminTaskList(ctx, msg.Chat.ID, msg.From.ID)
	case menuAdminSettings, "Settings":
		a.sendAdminSettings(ctx, msg.Chat.ID, msg.From.ID)
	default:
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "I couldn't recognise that option. Please choose a button from the menu.")
	}
}

func (a *App) handleTaskDraft(ctx context.Context, msg *TelegramMessage, draft *TaskDraft) {
	input := strings.TrimSpace(msg.Text)
	if input == "" {
		a.sendPlain(ctx, msg.Chat.ID, "Please send text, or /cancel to stop.")
		return
	}

	switch draft.Step {
	case "title":
		if len([]rune(input)) < 4 || len([]rune(input)) > 80 {
			a.sendPlain(ctx, msg.Chat.ID, "Task title must be 4–80 characters. Please try again.")
			return
		}
		draft.Title = input
		draft.Step = "description"
		_ = a.Marketplace.SaveDraft(ctx, draft)
		a.sendPlain(ctx, msg.Chat.ID, "Step 2/4: Send the task description (20–1000 characters).")
	case "description":
		if len([]rune(input)) < 20 || len([]rune(input)) > 1000 {
			a.sendPlain(ctx, msg.Chat.ID, "Description must be 20–1000 characters. Please try again.")
			return
		}
		draft.Description = input
		draft.Step = "reward"
		_ = a.Marketplace.SaveDraft(ctx, draft)
		a.sendPlain(ctx, msg.Chat.ID, "Step 3/4: Enter the reward per completed task in USDT (example: 0.50). Maximum 6 decimal places.")
	case "reward":
		if !ValidateUSDTAmount(input) {
			a.sendPlain(ctx, msg.Chat.ID, "Invalid reward. Enter a positive USDT amount, for example 0.50 (up to 6 decimal places).")
			return
		}
		draft.RewardUSDT = input
		draft.Step = "slots"
		_ = a.Marketplace.SaveDraft(ctx, draft)
		a.sendPlain(ctx, msg.Chat.ID, "Step 4/4: How many workers can complete this task? Enter a whole number from 1 to 500.")
	case "slots":
		slots, err := strconv.Atoi(input)
		if err != nil || slots < 1 || slots > 500 {
			a.sendPlain(ctx, msg.Chat.ID, "Enter a whole number between 1 and 500.")
			return
		}
		draft.Slots = slots
		user, err := a.Users.GetByTelegramID(ctx, msg.From.ID)
		if err != nil {
			a.sendPlain(ctx, msg.Chat.ID, "Could not verify your account. Please try again.")
			return
		}
		if !a.IsAdmin(msg.From.ID) {
			paid, paymentErr := a.Payments.HasPaidSellerRegistration(ctx, msg.From.ID)
			if paymentErr != nil || !paid || user.Role != "seller" {
				if paymentErr == nil && !paid {
					_ = a.Users.SetRole(ctx, msg.From.ID, "user")
				}
				_ = a.Marketplace.DeleteDraft(ctx, msg.From.ID)
				a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, fmt.Sprintf("A verified %d-Star Seller registration is required. Send /seller to receive the payment invoice.", SellerRegistrationStars))
				return
			}
		}
		creatorRole := user.Role
		status := TaskStatusPendingReview
		if a.IsAdmin(msg.From.ID) {
			creatorRole = "admin"
			status = TaskStatusActive
		}
		task := &Task{CreatorID: msg.From.ID, CreatorRole: creatorRole, Title: draft.Title, Description: draft.Description, RewardUSDT: draft.RewardUSDT, BudgetUSDT: CalculateTaskBudget(draft.RewardUSDT, draft.Slots), Slots: draft.Slots, Status: status, FundingStatus: "unfunded"}
		if err := a.Marketplace.CreateTask(ctx, task); err != nil {
			slog.Error("task creation failed", "error", err, "telegram_id", msg.From.ID)
			a.sendPlain(ctx, msg.Chat.ID, "Task could not be saved. Please try again later.")
			return
		}
		_ = a.Marketplace.DeleteDraft(ctx, msg.From.ID)
		if status == TaskStatusActive {
			a.sendWithKeyboard(ctx, msg.Chat.ID, fmt.Sprintf("✅ Task created and published.\n\nTitle: %s\nReward: %s USDT\nSlots: %d\nTask ID: %s\n\nNote: Worker applications and payment escrow are not active yet.", task.Title, task.RewardUSDT, task.Slots, task.ID.Hex()), a.mainMenuKeyboard(ctx, msg.From.ID))
		} else {
			a.sendWithKeyboard(ctx, msg.Chat.ID, fmt.Sprintf("✅ Task submitted for Admin review.\n\nTitle: %s\nReward: %s USDT\nSlots: %d\nTask ID: %s\n\nIt will appear in Marketplace only after approval.", task.Title, task.RewardUSDT, task.Slots, task.ID.Hex()), SellerKeyboard(false))
		}
	default:
		_ = a.Marketplace.DeleteDraft(ctx, msg.From.ID)
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "The saved task draft was invalid and has been cancelled. Please start again.")
	}
}

func (a *App) beginTaskCreation(ctx context.Context, chatID, userID int64) {
	user, err := a.Users.GetByTelegramID(ctx, userID)
	if err != nil {
		a.sendPlain(ctx, chatID, "Unable to verify your account.")
		return
	}
	if !a.IsAdmin(userID) {
		paid, paymentErr := a.Payments.HasPaidSellerRegistration(ctx, userID)
		if paymentErr != nil || !paid || user.Role != "seller" {
			if paymentErr == nil && user.Role == "seller" && !paid {
				_ = a.Users.SetRole(ctx, userID, "user")
			}
			a.sendMainMenu(ctx, chatID, userID, fmt.Sprintf("Seller access requires a verified one-time payment of %d Telegram Star(s). Send /seller to receive the official payment invoice.", SellerRegistrationStars))
			return
		}
	}
	if err := a.Marketplace.StartDraft(ctx, userID); err != nil {
		slog.Error("could not start task draft", "error", err)
		a.sendPlain(ctx, chatID, "Could not start task creation. Please try again.")
		return
	}
	a.sendWithKeyboard(ctx, chatID, "➕ CREATE TASK\n\nStep 1/4: Send a short task title (4–80 characters).\n\nSend /cancel to stop.", CancelKeyboard())
}

func (a *App) becomeSeller(ctx context.Context, chatID, userID int64) {
	if a.IsAdmin(userID) {
		a.sendWithKeyboard(ctx, chatID, "You already have Admin permissions. Seller registration is not required for Admin task management.", AdminKeyboard())
		return
	}
	user, err := a.Users.GetByTelegramID(ctx, userID)
	if err != nil {
		a.sendPlain(ctx, chatID, "Unable to load your account. Please try again.")
		return
	}
	paid, paymentErr := a.Payments.HasPaidSellerRegistration(ctx, userID)
	if paymentErr != nil {
		a.sendPlain(ctx, chatID, "Could not verify your Seller payment history. Please try again.")
		return
	}
	if paid {
		if user.Role != "seller" {
			if err := a.Users.SetRole(ctx, userID, "seller"); err != nil {
				a.sendPlain(ctx, chatID, "Your previous payment is confirmed, but account activation is delayed. Please try again; do not pay again.")
				return
			}
		}
		a.sendWithKeyboard(ctx, chatID, fmt.Sprintf("Your %d-Star Seller registration is already verified. Here is your separate Seller menu.", SellerRegistrationStars), SellerKeyboard(false))
		return
	}
	if user.Role == "seller" {
		_ = a.Users.SetRole(ctx, userID, "user")
	}

	payment, err := a.Payments.CreateSellerPayment(ctx, userID)
	if err != nil {
		slog.Error("seller payment record creation failed", "error", err, "telegram_id", userID)
		a.sendPlain(ctx, chatID, "Could not create a Seller registration invoice. Please try again.")
		return
	}

	err = a.Bot.SendInvoice(ctx, chatID, "DR Marketplace Seller Access", "One-time Seller registration fee. Seller access activates only after Telegram confirms successful payment.", payment.Payload, SellerRegistrationStars)
	if err != nil {
		_ = a.Payments.ExpirePendingSellerPayment(ctx, payment.Payload, userID)
		slog.Error("seller invoice send failed", "error", err, "telegram_id", userID)
		a.sendPlain(ctx, chatID, "Could not send the Stars invoice. Please try again later.")
		return
	}
	a.sendPlain(ctx, chatID, fmt.Sprintf("⭐ Your %d Telegram Star(s) Seller registration invoice is ready above.\n\nComplete the payment in Telegram. Your separate Seller menu will appear automatically only after Telegram confirms successful payment.\n\nTo request a new invoice later, send /seller again.", SellerRegistrationStars))
}

func (a *App) handlePreCheckout(ctx context.Context, query *PreCheckoutQuery) {
	if query == nil {
		return
	}
	payment, err := a.Payments.GetSellerPayment(ctx, query.InvoicePayload, query.From.ID)
	if err != nil || payment.Status != sellerPaymentPending || time.Now().UTC().After(payment.ExpiresAt) || query.Currency != "XTR" || query.TotalAmount != SellerRegistrationStars || payment.Currency != "XTR" || payment.Amount != SellerRegistrationStars {
		if err := a.Bot.AnswerPreCheckoutQuery(ctx, query.ID, false, "This Seller invoice is invalid or expired. Please request a new invoice from Seller Center."); err != nil {
			slog.Error("pre-checkout rejection failed", "error", err, "telegram_id", query.From.ID)
		}
		return
	}
	if err := a.Bot.AnswerPreCheckoutQuery(ctx, query.ID, true, ""); err != nil {
		slog.Error("pre-checkout approval failed", "error", err, "telegram_id", query.From.ID)
	}
}

func (a *App) handleSuccessfulSellerPayment(ctx context.Context, chatID, userID int64, payment *SuccessfulPayment) {
	if payment == nil || payment.Currency != "XTR" || payment.TotalAmount != SellerRegistrationStars || payment.TelegramPaymentChargeID == "" {
		slog.Error("invalid seller successful payment update", "telegram_id", userID)
		a.sendPlain(ctx, chatID, "Payment details could not be verified. Please contact support and do not pay again.")
		return
	}
	if err := a.Payments.MarkSellerPaymentPaid(ctx, payment.InvoicePayload, userID, payment.TelegramPaymentChargeID); err != nil {
		slog.Error("seller payment verification failed", "error", err, "telegram_id", userID)
		a.sendPlain(ctx, chatID, "Your payment could not be matched to a valid Seller invoice. Please contact support.")
		return
	}
	if err := a.Users.SetRole(ctx, userID, "seller"); err != nil {
		slog.Error("seller role activation after payment failed", "error", err, "telegram_id", userID)
		a.sendPlain(ctx, chatID, fmt.Sprintf("Your %d-Star payment is confirmed, but account activation is delayed. Please contact support; do not pay again.", SellerRegistrationStars))
		return
	}
	a.sendWithKeyboard(ctx, chatID, fmt.Sprintf("🎉 Payment confirmed: %d Telegram Star(s). Your Seller account is now active. Your separate Seller menu is now enabled.", SellerRegistrationStars), SellerKeyboard(false))
}

func (a *App) sendSellerCenter(ctx context.Context, chatID, userID int64) {
	// Seller Center is no longer a menu button. /seller is the only entry point.
	a.becomeSeller(ctx, chatID, userID)
}

func (a *App) sendTaskList(ctx context.Context, chatID, userID int64) {
	tasks, err := a.Marketplace.ListTasks(ctx, TaskStatusActive, 0, 10)
	if err != nil {
		slog.Error("task list failed", "error", err)
		a.sendPlain(ctx, chatID, "Could not load Marketplace tasks.")
		return
	}
	if len(tasks) == 0 {
		a.sendMainMenu(ctx, chatID, userID, "🛍 No active tasks are available right now. Please check again later.")
		return
	}
	for _, task := range tasks {
		text := fmt.Sprintf("🛍 %s\n\n%s\n\nReward: %s USDT / worker\nSlots: %d\nTotal escrow budget: %s USDT\nTask ID: %s",
			task.Title, task.Description, task.RewardUSDT, task.Slots, task.BudgetUSDT, task.ID.Hex())
		var rows [][]InlineKeyboardButton
		user, _ := a.Users.GetByTelegramID(ctx, userID)
		if task.FundingStatus == "funded" && !a.IsAdmin(userID) && (user == nil || (user.Role != "seller" && user.Role != "admin")) && task.CreatorID != userID {
			rows = append(rows, []InlineKeyboardButton{{Text: "✅ Apply for this task", CallbackData: "apply:" + task.ID.Hex()}})
		} else if task.FundingStatus != "funded" {
			text += "\n\n🔒 Applications locked: Seller escrow funding is not verified yet."
			rows = append(rows, []InlineKeyboardButton{{Text: "Funding not verified", CallbackData: "details:" + task.ID.Hex()}})
		}
		if len(rows) == 0 {
			rows = append(rows, []InlineKeyboardButton{{Text: "Task details", CallbackData: "details:" + task.ID.Hex()}})
		}
		_ = a.Bot.SendMessageWithInlineKeyboard(ctx, chatID, text, &InlineKeyboardMarkup{InlineKeyboard: rows})
	}
	a.sendWithKeyboard(ctx, chatID, "Choose a task and press Apply. After the Seller accepts, it will appear in My Work.", a.mainMenuKeyboard(ctx, userID))
}

func (a *App) sendMyTasks(ctx context.Context, chatID, userID int64) {
	if !a.IsAdmin(userID) {
		user, err := a.Users.GetByTelegramID(ctx, userID)
		if err != nil || user.Role != "seller" {
			a.sendMainMenu(ctx, chatID, userID, "My Tasks is available only to verified Sellers and Admins.")
			return
		}
		paid, err := a.Payments.HasPaidSellerRegistration(ctx, userID)
		if err != nil || !paid {
			a.sendMainMenu(ctx, chatID, userID, "Verified Seller access is required. Send /seller to check your registration.")
			return
		}
	}
	tasks, err := a.Marketplace.ListTasks(ctx, "", userID, 20)
	if err != nil {
		a.sendPlain(ctx, chatID, "Could not load your tasks.")
		return
	}
	if len(tasks) == 0 {
		a.sendWithKeyboard(ctx, chatID, "You have not created any tasks yet. Press Create Task to begin.", SellerKeyboard(false))
		return
	}
	for _, task := range tasks {
		text := fmt.Sprintf("📋 %s\nStatus: %s\nFunding: %s\nReward: %s USDT / worker\nSlots: %d | Total budget: %s USDT\nTask ID: %s",
			task.Title, task.Status, task.FundingStatus, task.RewardUSDT, task.Slots, task.BudgetUSDT, task.ID.Hex())
		rows := [][]InlineKeyboardButton{
			{{Text: "👥 View applicants", CallbackData: "apps:" + task.ID.Hex()}},
		}
		if task.Status == TaskStatusActive {
			rows = append(rows, []InlineKeyboardButton{{Text: "⏸ Pause", CallbackData: "pause:" + task.ID.Hex()}, {Text: "🗑 Delete", CallbackData: "delete:" + task.ID.Hex()}})
		}
		if task.Status == TaskStatusPaused {
			rows = append(rows, []InlineKeyboardButton{{Text: "▶️ Resume", CallbackData: "resume:" + task.ID.Hex()}, {Text: "🗑 Delete", CallbackData: "delete:" + task.ID.Hex()}})
		}
		_ = a.Bot.SendMessageWithInlineKeyboard(ctx, chatID, text, &InlineKeyboardMarkup{InlineKeyboard: rows})
	}
	a.sendWithKeyboard(ctx, chatID, "Your task controls are available directly on each task card.", SellerKeyboard(false))
}

func (a *App) sendPendingTasks(ctx context.Context, chatID, userID int64) {
	if !a.requireAdmin(chatID, userID) {
		return
	}
	tasks, err := a.Marketplace.ListTasks(ctx, TaskStatusPendingReview, 0, 20)
	if err != nil {
		a.sendPlain(ctx, chatID, "Could not load pending tasks.")
		return
	}
	if len(tasks) == 0 {
		a.sendWithKeyboard(ctx, chatID, "No tasks are waiting for review.", AdminKeyboard())
		return
	}
	for _, task := range tasks {
		text := fmt.Sprintf("🕒 %s\n\n%s\n\nReward: %s USDT | Slots: %d\nCreator: %d\nID: %s",
			task.Title, task.Description, task.RewardUSDT, task.Slots, task.CreatorID, task.ID.Hex())
		kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
			{{Text: "✅ Approve", CallbackData: "modapprove:" + task.ID.Hex()}, {Text: "❌ Reject", CallbackData: "modreject:" + task.ID.Hex()}},
		}}
		_ = a.Bot.SendMessageWithInlineKeyboard(ctx, chatID, text, kb)
	}
	a.sendWithKeyboard(ctx, chatID, "Review each task using the buttons above.", AdminKeyboard())
}

func (a *App) handleTaskModeration(ctx context.Context, msg *TelegramMessage, fields []string, status string) {
	if !a.IsAdmin(msg.From.ID) {
		a.sendPlain(ctx, msg.Chat.ID, "Admin access required.")
		return
	}
	if len(fields) != 2 {
		a.sendPlain(ctx, msg.Chat.ID, "Usage: /approve_task TASK_ID or /reject_task TASK_ID")
		return
	}
	id, err := primitive.ObjectIDFromHex(fields[1])
	if err != nil {
		a.sendPlain(ctx, msg.Chat.ID, "Invalid Task ID.")
		return
	}
	if err := a.Marketplace.UpdateTaskStatus(ctx, id, status); err != nil {
		if err == mongo.ErrNoDocuments {
			a.sendPlain(ctx, msg.Chat.ID, "Task not found or it is no longer pending review.")
			return
		}
		a.sendPlain(ctx, msg.Chat.ID, "Could not update task status.")
		return
	}
	a.sendWithKeyboard(ctx, msg.Chat.ID, "Task status updated: "+status, AdminKeyboard())
}

func (a *App) handleAccountStatusCommand(ctx context.Context, msg *TelegramMessage, fields []string, status string) {
	if !a.requireAdmin(msg.Chat.ID, msg.From.ID) {
		return
	}
	if len(fields) != 2 {
		a.sendWithKeyboard(ctx, msg.Chat.ID, "Usage: /ban USER_ID, /unban USER_ID, /suspend USER_ID, or /restore USER_ID", AdminKeyboard())
		return
	}
	targetID, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || targetID <= 0 {
		a.sendPlain(ctx, msg.Chat.ID, "Invalid Telegram User ID.")
		return
	}
	if a.IsAdmin(targetID) {
		a.sendPlain(ctx, msg.Chat.ID, "For safety, Admin accounts cannot be changed from this command.")
		return
	}
	if err := a.Users.SetStatus(ctx, targetID, status); err != nil {
		a.sendPlain(ctx, msg.Chat.ID, "Could not update account status: "+err.Error())
		return
	}
	label := status
	if status == "active" {
		label = "restored / active"
	}
	a.sendWithKeyboard(ctx, msg.Chat.ID, fmt.Sprintf("Account %d status updated: %s", targetID, label), AdminKeyboard())
}

func (a *App) handleTaskOwnerCommand(ctx context.Context, msg *TelegramMessage, fields []string, status string) {
	if len(fields) != 2 {
		a.sendPlain(ctx, msg.Chat.ID, "Usage: /pause_task TASK_ID, /resume_task TASK_ID, or /delete_task TASK_ID")
		return
	}
	id, err := primitive.ObjectIDFromHex(fields[1])
	if err != nil {
		a.sendPlain(ctx, msg.Chat.ID, "Invalid Task ID.")
		return
	}
	if !a.IsAdmin(msg.From.ID) {
		user, userErr := a.Users.GetByTelegramID(ctx, msg.From.ID)
		if userErr != nil || user.Role != "seller" {
			a.sendPlain(ctx, msg.Chat.ID, "Only the task owner or an Admin can manage this task.")
			return
		}
		paid, payErr := a.Payments.HasPaidSellerRegistration(ctx, msg.From.ID)
		if payErr != nil || !paid {
			a.sendPlain(ctx, msg.Chat.ID, "Verified Seller access is required.")
			return
		}
	}
	ownerID := int64(0)
	if !a.IsAdmin(msg.From.ID) {
		ownerID = msg.From.ID
	}
	if err := a.Marketplace.ChangeTaskStatus(ctx, id, ownerID, status); err != nil {
		a.sendPlain(ctx, msg.Chat.ID, "Task could not be updated. Check the Task ID, ownership, and current status.")
		return
	}
	a.sendWithKeyboard(ctx, msg.Chat.ID, "Task status updated: "+status, a.mainMenuKeyboard(ctx, msg.From.ID))
}

func (a *App) sendProfile(ctx context.Context, chatID, telegramID int64) {
	user, err := a.Users.GetByTelegramID(ctx, telegramID)
	if err != nil {
		a.sendMainMenu(ctx, chatID, telegramID, "Unable to load your profile. Please try again.")
		return
	}
	username := "Not set"
	if user.Username != "" {
		username = "@" + user.Username
	}
	role := user.Role
	if a.IsAdmin(telegramID) {
		role = "admin"
	} else if role == "seller" {
		paid, paymentErr := a.Payments.HasPaidSellerRegistration(ctx, telegramID)
		if paymentErr != nil || !paid {
			role = "user"
			if paymentErr == nil {
				_ = a.Users.SetRole(ctx, telegramID, "user")
			}
		}
	}
	profile := fmt.Sprintf("👤 MY PROFILE\n\nName: %s\nUsername: %s\nTelegram ID: %d\nAccount Type: %s\nStatus: %s\nJoined: %s", safeName(user.FirstName), username, user.TelegramID, role, user.Status, user.CreatedAt.UTC().Format("02 Jan 2006"))
	a.sendMainMenu(ctx, chatID, telegramID, profile)
}

func (a *App) sendMainMenu(ctx context.Context, chatID, userID int64, text string) {
	a.sendWithKeyboard(ctx, chatID, text, a.mainMenuKeyboard(ctx, userID))
}

func (a *App) mainMenuKeyboard(ctx context.Context, userID int64) *ReplyKeyboardMarkup {
	if a.IsAdmin(userID) {
		return AdminKeyboard()
	}

	user, err := a.Users.GetByTelegramID(ctx, userID)
	if err != nil {
		return UserKeyboard()
	}

	if user.Role == "seller" {
		paid, paymentErr := a.Payments.HasPaidSellerRegistration(ctx, userID)
		if paymentErr != nil {
			return SellerKeyboard(false)
		}
		if paid {
			return SellerKeyboard(false)
		}
		_ = a.Users.SetRole(ctx, userID, "user")
	}

	if user.Role == "worker" {
		return WorkerKeyboard()
	}
	return UserKeyboard()
}

func (a *App) sendWithKeyboard(ctx context.Context, chatID int64, text string, keyboard *ReplyKeyboardMarkup) {
	if err := a.Bot.SendMessageWithKeyboard(ctx, chatID, text, keyboard); err != nil {
		slog.Error("Telegram message failed", "error", err, "chat_id", chatID)
	}
}

func (a *App) sendPlain(ctx context.Context, chatID int64, text string) {
	if err := a.Bot.SendMessage(ctx, chatID, text); err != nil {
		slog.Error("Telegram message failed", "error", err, "chat_id", chatID)
	}
}

func CancelKeyboard() *ReplyKeyboardMarkup {
	return &ReplyKeyboardMarkup{Keyboard: [][]ReplyKeyboardButton{{{Text: menuCancel}}}, ResizeKeyboard: true, IsPersistent: true}
}

func safeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "there"
	}
	return name
}

func (a *App) handleMarkTaskFunded(ctx context.Context, msg *TelegramMessage, fields []string) {
	if !a.requireAdmin(msg.Chat.ID, msg.From.ID) {
		return
	}
	if len(fields) != 3 {
		a.sendWithKeyboard(ctx, msg.Chat.ID, "Usage: /mark_task_funded TASK_ID TX_HASH\nOnly use this after independently verifying the Seller's BEP-20 escrow transfer in a blockchain explorer.", AdminKeyboard())
		return
	}
	id, err := primitive.ObjectIDFromHex(fields[1])
	if err != nil {
		a.sendPlain(ctx, msg.Chat.ID, "Invalid Task ID.")
		return
	}
	if err := a.Marketplace.MarkTaskFunded(ctx, id, msg.From.ID, fields[2]); err != nil {
		a.sendPlain(ctx, msg.Chat.ID, "Task funding could not be recorded. Confirm the task is active, not already funded, and the transaction hash is correct.")
		return
	}
	a.sendWithKeyboard(ctx, msg.Chat.ID, "✅ Funding attestation recorded. The bot stores the transaction hash but does not independently verify the blockchain transfer.", AdminKeyboard())
}
