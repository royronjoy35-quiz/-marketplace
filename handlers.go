package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	menuMarketplace  = "🛍 Marketplace"
	menuProfile      = "👤 My Profile"
	menuOrders       = "📦 My Orders"
	menuEarnings     = "💰 My Earnings"
	menuWithdraw     = "🏦 Withdraw"
	menuSupport      = "🎧 Support"
	menuSeller       = "🏪 Seller Center"
	menuCreateTask   = "➕ Create Task"
	menuMyTasks      = "📋 My Tasks"
	menuBecomeSeller = "🤝 Become Seller"
	menuAdmin        = "🛡 Admin Panel"
	menuPending      = "🕒 Pending Tasks"
	menuMain         = "🏠 Main Menu"
	menuCancel       = "❌ Cancel"
)

type TelegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *TelegramMessage `json:"message,omitempty"`
}

type TelegramMessage struct {
	Text string        `json:"text,omitempty"`
	Chat TelegramChat  `json:"chat"`
	From *TelegramUser `json:"from,omitempty"`
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
}

func NewApp(cfg Config, db *Database, bot *TelegramClient) *App {
	return &App{
		Config:      cfg,
		DB:          db,
		Bot:         bot,
		Users:       NewUserStore(db),
		Marketplace: NewMarketplaceStore(db),
	}
}

func MainMenuKeyboard(isAdmin bool) *ReplyKeyboardMarkup {
	rows := [][]ReplyKeyboardButton{
		{{Text: menuMarketplace}, {Text: menuProfile}},
		{{Text: menuOrders}, {Text: menuEarnings}},
		{{Text: menuCreateTask}, {Text: menuSeller}},
		{{Text: menuWithdraw}, {Text: menuSupport}},
	}
	if isAdmin {
		rows = append(rows, []ReplyKeyboardButton{{Text: menuAdmin}})
	}
	return &ReplyKeyboardMarkup{Keyboard: rows, ResizeKeyboard: true, IsPersistent: true, InputFieldPlaceholder: "Choose an option..."}
}

func SellerKeyboard(isAdmin bool) *ReplyKeyboardMarkup {
	rows := [][]ReplyKeyboardButton{
		{{Text: menuCreateTask}, {Text: menuMyTasks}},
		{{Text: menuBecomeSeller}, {Text: menuMain}},
	}
	if isAdmin {
		rows = append(rows, []ReplyKeyboardButton{{Text: menuAdmin}})
	}
	return &ReplyKeyboardMarkup{Keyboard: rows, ResizeKeyboard: true, IsPersistent: true}
}

func AdminKeyboard() *ReplyKeyboardMarkup {
	return &ReplyKeyboardMarkup{
		Keyboard: [][]ReplyKeyboardButton{
			{{Text: menuCreateTask}, {Text: menuPending}},
			{{Text: menuMyTasks}, {Text: menuMain}},
		},
		ResizeKeyboard: true,
		IsPersistent:   true,
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
		w.WriteHeader(http.StatusOK)
		go a.ProcessUpdate(update)
	})
}

func (a *App) ProcessUpdate(update TelegramUpdate) {
	if update.Message == nil || update.Message.From == nil {
		return
	}
	msg := update.Message
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := a.Users.UpsertTelegramUser(ctx, msg.From.ID, msg.From.Username, msg.From.FirstName); err != nil {
		slog.Error("user registration failed", "error", err, "telegram_id", msg.From.ID)
		return
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	// A pending task draft takes priority over normal menu navigation.
	draft, draftErr := a.Marketplace.GetDraft(ctx, msg.From.ID)
	if draftErr == nil && text != menuCancel && text != "/cancel" {
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
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "ⒹⓇ Marketplace Help\n\nUse the menu buttons to navigate.\nUse /cancel to cancel an active task creation.")
		return
	case "/status":
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "ⒹⓇ Marketplace is online.")
		return
	case "/cancel":
		_ = a.Marketplace.DeleteDraft(ctx, msg.From.ID)
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "Task creation cancelled.")
		return
	case "/approve_task":
		a.handleTaskModeration(ctx, msg, fields, TaskStatusActive)
		return
	case "/reject_task":
		a.handleTaskModeration(ctx, msg, fields, TaskStatusRejected)
		return
	}

	switch text {
	case menuCancel:
		_ = a.Marketplace.DeleteDraft(ctx, msg.From.ID)
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "Task creation cancelled.")
	case menuMain:
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "Main menu")
	case menuProfile, "My Profile":
		a.sendProfile(ctx, msg.Chat.ID, msg.From.ID)
	case menuMarketplace, "Marketplace":
		a.sendTaskList(ctx, msg.Chat.ID, 0)
	case menuOrders, "My Orders":
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "📦 My Orders\n\nOrder processing will be enabled in the next development phase.")
	case menuEarnings, "My Earnings":
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "💰 My Earnings\n\nEarnings ledger is not active yet. No withdrawable balance is calculated.")
	case menuWithdraw, "Withdraw":
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "🏦 Withdraw\n\nUSDT withdrawals are not active yet. Please do not send funds to the bot.")
	case menuSupport, "Support":
		a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "🎧 Support\n\nSupport tickets will be added in a later phase.")
	case menuSeller, "Seller Center":
		a.sendSellerCenter(ctx, msg.Chat.ID, msg.From.ID)
	case menuBecomeSeller, "Become Seller":
		a.becomeSeller(ctx, msg.Chat.ID, msg.From.ID)
	case menuCreateTask, "Create Task":
		a.beginTaskCreation(ctx, msg.Chat.ID, msg.From.ID)
	case menuMyTasks, "My Tasks":
		a.sendMyTasks(ctx, msg.Chat.ID, msg.From.ID)
	case menuAdmin, "Admin Panel":
		if !a.IsAdmin(msg.From.ID) {
			a.sendMainMenu(ctx, msg.Chat.ID, msg.From.ID, "You are not authorized to access the Admin Panel.")
			return
		}
		a.sendWithKeyboard(ctx, msg.Chat.ID, "🛡 Admin Panel\n\nCreate tasks or review seller submissions.\n\nSeller-created tasks remain hidden until approved.", AdminKeyboard())
	case menuPending, "Pending Tasks":
		a.sendPendingTasks(ctx, msg.Chat.ID, msg.From.ID)
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
		creatorRole := user.Role
		status := TaskStatusPendingReview
		if a.IsAdmin(msg.From.ID) {
			creatorRole = "admin"
			status = TaskStatusActive
		}
		task := &Task{CreatorID: msg.From.ID, CreatorRole: creatorRole, Title: draft.Title, Description: draft.Description, RewardUSDT: draft.RewardUSDT, Slots: draft.Slots, Status: status}
		if err := a.Marketplace.CreateTask(ctx, task); err != nil {
			slog.Error("task creation failed", "error", err, "telegram_id", msg.From.ID)
			a.sendPlain(ctx, msg.Chat.ID, "Task could not be saved. Please try again later.")
			return
		}
		_ = a.Marketplace.DeleteDraft(ctx, msg.From.ID)
		if status == TaskStatusActive {
			a.sendWithKeyboard(ctx, msg.Chat.ID, fmt.Sprintf("✅ Task created and published.\n\nTitle: %s\nReward: %s USDT\nSlots: %d\nTask ID: %s\n\nNote: Worker applications and payment escrow are not active yet.", task.Title, task.RewardUSDT, task.Slots, task.ID.Hex()), MainMenuKeyboard(a.IsAdmin(msg.From.ID)))
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
	if !a.IsAdmin(userID) && user.Role != "seller" {
		a.sendWithKeyboard(ctx, chatID, "Only Admins and registered Sellers can create tasks. Open Seller Center and choose Become Seller first.", SellerKeyboard(false))
		return
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
		a.sendWithKeyboard(ctx, chatID, "You already have Admin permissions and can create tasks.", AdminKeyboard())
		return
	}
	user, err := a.Users.GetByTelegramID(ctx, userID)
	if err != nil {
		a.sendPlain(ctx, chatID, "Unable to load your account.")
		return
	}
	if user.Role == "seller" {
		a.sendWithKeyboard(ctx, chatID, "Your Seller account is already active. You can create and manage your tasks.", SellerKeyboard(false))
		return
	}
	if err := a.Users.SetRole(ctx, userID, "seller"); err != nil {
		a.sendPlain(ctx, chatID, "Could not activate Seller access. Please try again.")
		return
	}
	a.sendWithKeyboard(ctx, chatID, "🎉 Seller access activated.\n\nYou can now create tasks. New Seller tasks require Admin approval before appearing in the Marketplace.", SellerKeyboard(false))
}

func (a *App) sendSellerCenter(ctx context.Context, chatID, userID int64) {
	user, err := a.Users.GetByTelegramID(ctx, userID)
	if err != nil {
		a.sendPlain(ctx, chatID, "Unable to load Seller Center.")
		return
	}
	if a.IsAdmin(userID) {
		a.sendWithKeyboard(ctx, chatID, "🏪 Seller Center\n\nAdmin task creation is available. Seller submissions require review.", AdminKeyboard())
		return
	}
	if user.Role == "seller" {
		a.sendWithKeyboard(ctx, chatID, "🏪 Seller Center\n\nYour Seller account is active. You can create tasks and view your submissions.", SellerKeyboard(false))
	} else {
		a.sendWithKeyboard(ctx, chatID, "🏪 Seller Center\n\nBecome a Seller to submit tasks to the Marketplace. All Seller tasks are reviewed by Admin before publication.", SellerKeyboard(false))
	}
}

func (a *App) sendTaskList(ctx context.Context, chatID, creatorID int64) {
	tasks, err := a.Marketplace.ListTasks(ctx, TaskStatusActive, creatorID, 10)
	if err != nil {
		slog.Error("task list failed", "error", err)
		a.sendPlain(ctx, chatID, "Could not load Marketplace tasks.")
		return
	}
	message := FormatTaskList(tasks, "🛍 No active tasks are available right now. Please check again later.")
	message = "🛍 MARKETPLACE — ACTIVE TASKS\n\n" + message + "\n\nTask applications and escrow payments will be enabled in a later phase."
	a.sendMainMenu(ctx, chatID, chatID, message)
}

func (a *App) sendMyTasks(ctx context.Context, chatID, userID int64) {
	tasks, err := a.Marketplace.ListTasks(ctx, "", userID, 10)
	if err != nil {
		slog.Error("my tasks lookup failed", "error", err)
		a.sendPlain(ctx, chatID, "Could not load your tasks.")
		return
	}
	// Show all statuses for the creator.
	if len(tasks) == 0 {
		// Query pending and active separately because the store filters by status.
		active, _ := a.Marketplace.ListTasks(ctx, TaskStatusActive, userID, 10)
		pending, _ := a.Marketplace.ListTasks(ctx, TaskStatusPendingReview, userID, 10)
		tasks = append(active, pending...)
	}
	message := FormatTaskList(tasks, "You have not created any tasks yet.")
	a.sendWithKeyboard(ctx, chatID, "📋 MY TASKS\n\n"+message, SellerKeyboard(a.IsAdmin(userID)))
}

func (a *App) sendPendingTasks(ctx context.Context, chatID, userID int64) {
	if !a.IsAdmin(userID) {
		a.sendMainMenu(ctx, chatID, userID, "Admin access required.")
		return
	}
	tasks, err := a.Marketplace.ListTasks(ctx, TaskStatusPendingReview, 0, 10)
	if err != nil {
		a.sendPlain(ctx, chatID, "Could not load pending tasks.")
		return
	}
	if len(tasks) == 0 {
		a.sendWithKeyboard(ctx, chatID, "No tasks are waiting for review.", AdminKeyboard())
		return
	}
	message := "🕒 PENDING TASKS\n\n" + FormatTaskList(tasks, "No pending tasks.") + "\n\nApprove: /approve_task TASK_ID\nReject: /reject_task TASK_ID"
	a.sendWithKeyboard(ctx, chatID, message, AdminKeyboard())
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
	}
	profile := fmt.Sprintf("👤 MY PROFILE\n\nName: %s\nUsername: %s\nTelegram ID: %d\nAccount Type: %s\nStatus: %s\nJoined: %s", safeName(user.FirstName), username, user.TelegramID, role, user.Status, user.CreatedAt.UTC().Format("02 Jan 2006"))
	a.sendMainMenu(ctx, chatID, telegramID, profile)
}

func (a *App) sendMainMenu(ctx context.Context, chatID, userID int64, text string) {
	a.sendWithKeyboard(ctx, chatID, text, MainMenuKeyboard(a.IsAdmin(userID)))
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
