package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func (a *App) handleCallbackQuery(ctx context.Context, q *TelegramCallbackQuery) {
	if q == nil {
		return
	}
	chatID := q.From.ID
	if q.Message != nil {
		chatID = q.Message.Chat.ID
	}
	_ = a.Bot.AnswerCallbackQuery(ctx, q.ID, "")
	if !a.IsAdmin(q.From.ID) {
		u, err := a.Users.GetByTelegramID(ctx, q.From.ID)
		if err != nil || u.Status == "banned" || u.Status == "suspended" {
			a.sendPlain(ctx, chatID, "Your account is not permitted to use this action.")
			return
		}
	}
	parts := strings.Split(q.Data, ":")
	if len(parts) != 2 {
		return
	}
	action, rawID := parts[0], parts[1]
	if strings.HasPrefix(action, "acct") {
		if !a.IsAdmin(q.From.ID) {
			a.sendPlain(ctx, chatID, "Admin access required.")
			return
		}
		targetID, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || targetID <= 0 || a.IsAdmin(targetID) {
			a.sendPlain(ctx, chatID, "Invalid or protected account.")
			return
		}
		status := map[string]string{"acctban": "banned", "acctunban": "active", "acctsuspend": "suspended", "acctrestore": "active"}[action]
		if status == "" {
			return
		}
		if err := a.Users.SetStatus(ctx, targetID, status); err != nil {
			a.sendPlain(ctx, chatID, "Account status could not be updated.")
			return
		}
		a.sendPlain(ctx, chatID, fmt.Sprintf("Account %d status updated: %s", targetID, status))
		return
	}
	id, err := primitive.ObjectIDFromHex(rawID)
	if err != nil {
		a.sendPlain(ctx, chatID, "Invalid action reference.")
		return
	}

	switch action {
	case "apply":
		user, err := a.Users.GetByTelegramID(ctx, q.From.ID)
		if err != nil {
			a.sendPlain(ctx, chatID, "Account could not be loaded.")
			return
		}
		if a.IsAdmin(q.From.ID) || user.Role == "seller" {
			a.sendPlain(ctx, chatID, "Seller/Admin accounts cannot apply to worker tasks from this account.")
			return
		}
		if err := a.TaskFlow.Apply(ctx, id, q.From.ID); err != nil {
			a.sendPlain(ctx, chatID, "Application failed: "+err.Error())
			return
		}
		if task, taskErr := a.TaskFlow.GetTask(ctx, id); taskErr == nil {
			a.sendPlain(ctx, task.CreatorID, fmt.Sprintf("📨 New worker application received for “%s”. Open Applications in Seller Panel to review.", task.Title))
		}
		a.sendPlain(ctx, chatID, "✅ Application sent to the Seller. You will see the result in My Applications.")
	case "funding":
		if !a.IsAdmin(q.From.ID) {
			a.sendPlain(ctx, chatID, "Admin access required.")
			return
		}
		a.sendPlain(ctx, chatID, "To unlock worker applications, first verify the Seller's BEP-20 escrow transfer independently. Then send:\n/mark_task_funded "+id.Hex()+" TX_HASH")
	case "details":
		task, err := a.TaskFlow.GetTask(ctx, id)
		if err != nil {
			a.sendPlain(ctx, chatID, "Task not found.")
			return
		}
		a.sendPlain(ctx, chatID, fmt.Sprintf("%s\n\n%s\nReward: %s USDT\nSlots: %d", task.Title, task.Description, task.RewardUSDT, task.Slots))
	case "apps":
		if !a.isTaskOwnerOrAdmin(ctx, id, q.From.ID) {
			a.sendPlain(ctx, chatID, "Only the task owner or Admin can view applicants.")
			return
		}
		sellerID := q.From.ID
		if a.IsAdmin(q.From.ID) {
			sellerID = 0
		}
		a.showTaskApplications(ctx, chatID, id, sellerID)
	case "accept", "reject":
		var app TaskApplication
		if err := a.TaskFlow.applications.FindOne(ctx, bson.M{"_id": id}).Decode(&app); err != nil {
			a.sendPlain(ctx, chatID, "Application not found.")
			return
		}
		if !a.IsAdmin(q.From.ID) && app.SellerID != q.From.ID {
			a.sendPlain(ctx, chatID, "Only the task owner can review this application.")
			return
		}
		status := AppStatusAccepted
		if action == "reject" {
			status = AppStatusRejected
		}
		if err := a.TaskFlow.SetApplicationStatus(ctx, id, app.SellerID, status); err != nil {
			a.sendPlain(ctx, chatID, "This application has already been reviewed.")
			return
		}
		if status == AppStatusAccepted {
			// A user becomes a Worker only after a Seller accepts their application.
			worker, getErr := a.Users.GetByTelegramID(ctx, app.WorkerID)
			if getErr == nil && worker.Role == "user" {
				_ = a.Users.SetRole(ctx, app.WorkerID, "worker")
			}
			a.sendPlain(ctx, app.WorkerID, "🎉 Your task application was accepted. Open My Work and submit your proof when finished.")
			a.sendPlain(ctx, chatID, "✅ Worker application accepted.")
		} else {
			a.sendPlain(ctx, app.WorkerID, "Your task application was not accepted.")
			a.sendPlain(ctx, chatID, "Application rejected.")
		}
	case "submit":
		var app TaskApplication
		if err := a.TaskFlow.applications.FindOne(ctx, bson.M{"_id": id, "worker_id": q.From.ID, "status": AppStatusAccepted}).Decode(&app); err != nil {
			a.sendPlain(ctx, chatID, "This accepted application is not available for submission.")
			return
		}
		_ = a.TaskFlow.SetSubmissionSession(ctx, q.From.ID, id)
		a.sendWithKeyboard(ctx, chatID, "📝 Send your work proof as text or a URL (5–3000 characters). Do not send passwords or private keys. Send /cancel to stop.", CancelKeyboard())
	case "pause", "resume", "delete":
		task, err := a.TaskFlow.GetTask(ctx, id)
		if err != nil {
			a.sendPlain(ctx, chatID, "Task not found.")
			return
		}
		if !a.IsAdmin(q.From.ID) && task.CreatorID != q.From.ID {
			a.sendPlain(ctx, chatID, "You can only manage your own tasks.")
			return
		}
		status := TaskStatusPaused
		if action == "resume" {
			status = TaskStatusActive
		}
		if action == "delete" {
			status = TaskStatusDeleted
		}
		owner := task.CreatorID
		if a.IsAdmin(q.From.ID) {
			owner = 0
		}
		if err := a.Marketplace.ChangeTaskStatus(ctx, id, owner, status); err != nil {
			a.sendPlain(ctx, chatID, "Task status could not be changed.")
			return
		}
		a.sendPlain(ctx, chatID, "Task updated: "+status)
	case "modapprove", "modreject":
		if !a.IsAdmin(q.From.ID) {
			a.sendPlain(ctx, chatID, "Admin access required.")
			return
		}
		status := TaskStatusActive
		if action == "modreject" {
			status = TaskStatusRejected
		}
		if err := a.Marketplace.UpdateTaskStatus(ctx, id, status); err != nil {
			a.sendPlain(ctx, chatID, "Task could not be moderated. It may already have been reviewed.")
			return
		}
		a.sendPlain(ctx, chatID, "Task moderation complete: "+status)
	case "reviewok", "reviewrev", "reviewno":
		var sub TaskSubmission
		if err := a.TaskFlow.submissions.FindOne(ctx, bson.M{"_id": id}).Decode(&sub); err != nil {
			a.sendPlain(ctx, chatID, "Submission not found.")
			return
		}
		if !a.IsAdmin(q.From.ID) && sub.SellerID != q.From.ID {
			a.sendPlain(ctx, chatID, "Only the Seller or Admin can review this work.")
			return
		}
		actionName := "approved"
		if action == "reviewrev" {
			actionName = "revision_requested"
		}
		if action == "reviewno" {
			actionName = "rejected"
		}
		task, taskErr := a.TaskFlow.GetTask(ctx, sub.TaskID)
		reward := "0"
		if taskErr == nil {
			reward = task.RewardUSDT
		}
		if err := a.TaskFlow.ReviewSubmission(ctx, id, sub.SellerID, actionName, "", reward); err != nil {
			a.sendPlain(ctx, chatID, "Review could not be saved.")
			return
		}
		if actionName == "approved" {
			a.sendPlain(ctx, sub.WorkerID, "✅ Your work was approved. Reward is recorded as pending funding; it is not withdrawable until the marketplace funding/ledger process completes.")
			a.sendPlain(ctx, chatID, "Work approved. Reward recorded as pending funding.")
		} else if actionName == "revision_requested" {
			a.sendPlain(ctx, sub.WorkerID, "✏️ Seller requested a revision. Please contact the Seller for the required changes.")
			a.sendPlain(ctx, chatID, "Revision requested.")
		} else {
			a.sendPlain(ctx, sub.WorkerID, "Your submitted work was rejected.")
			a.sendPlain(ctx, chatID, "Submission rejected.")
		}
	}
}

func (a *App) isTaskOwnerOrAdmin(ctx context.Context, taskID primitive.ObjectID, userID int64) bool {
	if a.IsAdmin(userID) {
		return true
	}
	task, err := a.TaskFlow.GetTask(ctx, taskID)
	return err == nil && task.CreatorID == userID
}

func (a *App) showTaskApplications(ctx context.Context, chatID int64, taskID primitive.ObjectID, sellerID int64) {
	if sellerID > 0 && !a.IsAdmin(sellerID) {
		paid, err := a.Payments.HasPaidSellerRegistration(ctx, sellerID)
		if err != nil || !paid {
			a.sendPlain(ctx, chatID, "Verified Seller access is required.")
			return
		}
	}
	apps, err := a.TaskFlow.ListApplications(ctx, taskID, sellerID)
	if err != nil {
		a.sendPlain(ctx, chatID, "Could not load applications.")
		return
	}
	if len(apps) == 0 {
		a.sendPlain(ctx, chatID, "No applications yet.")
		return
	}
	for _, app := range apps {
		text := fmt.Sprintf("Application\nWorker ID: %d\nStatus: %s\nApplication ID: %s", app.WorkerID, app.Status, app.ID.Hex())
		var rows [][]InlineKeyboardButton
		if app.Status == AppStatusApplied {
			rows = append(rows, []InlineKeyboardButton{{Text: "✅ Accept", CallbackData: "accept:" + app.ID.Hex()}, {Text: "❌ Reject", CallbackData: "reject:" + app.ID.Hex()}})
		}
		_ = a.Bot.SendMessageWithInlineKeyboard(ctx, chatID, text, &InlineKeyboardMarkup{InlineKeyboard: rows})
	}
}

func (a *App) sendMyApplications(ctx context.Context, chatID, userID int64) {
	apps, err := a.TaskFlow.ListWorkerApplications(ctx, userID)
	if err != nil {
		a.sendPlain(ctx, chatID, "Could not load your applications.")
		return
	}
	if len(apps) == 0 {
		a.sendMainMenu(ctx, chatID, userID, "You have not applied to any tasks yet.")
		return
	}
	for _, app := range apps {
		task, taskErr := a.TaskFlow.GetTask(ctx, app.TaskID)
		title := "Task"
		if taskErr == nil {
			title = task.Title
		}
		_ = a.Bot.SendMessage(ctx, chatID, fmt.Sprintf("📨 %s\nStatus: %s\nApplication: %s", title, app.Status, app.ID.Hex()))
	}
	a.sendWithKeyboard(ctx, chatID, "Your application statuses are shown above.", a.mainMenuKeyboard(ctx, userID))
}

func (a *App) sendMyWork(ctx context.Context, chatID, userID int64) {
	apps, err := a.TaskFlow.ListWorkerApplications(ctx, userID)
	if err != nil {
		a.sendPlain(ctx, chatID, "Could not load your work.")
		return
	}
	count := 0
	for _, app := range apps {
		if app.Status != AppStatusAccepted && app.Status != AppStatusRevision && app.Status != AppStatusSubmitted {
			continue
		}
		task, taskErr := a.TaskFlow.GetTask(ctx, app.TaskID)
		if taskErr != nil {
			continue
		}
		count++
		var kb *InlineKeyboardMarkup
		if app.Status == AppStatusAccepted || app.Status == AppStatusRevision {
			kb = &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
				{{Text: "📝 Submit / Resubmit Work", CallbackData: "submit:" + app.ID.Hex()}},
			}}
		}
		_ = a.Bot.SendMessageWithInlineKeyboard(ctx, chatID, fmt.Sprintf("🧰 %s\nReward: %s USDT\nStatus: %s", task.Title, task.RewardUSDT, app.Status), kb)
	}
	if count == 0 {
		a.sendWithKeyboard(ctx, chatID, "No accepted tasks are waiting for your work.", a.mainMenuKeyboard(ctx, userID))
		return
	}
}

func (a *App) sendSellerApplications(ctx context.Context, chatID, userID int64) {
	if !a.IsAdmin(userID) {
		user, err := a.Users.GetByTelegramID(ctx, userID)
		if err != nil || user.Role != "seller" {
			a.sendPlain(ctx, chatID, "Seller access required.")
			return
		}
		paid, err := a.Payments.HasPaidSellerRegistration(ctx, userID)
		if err != nil || !paid {
			a.sendPlain(ctx, chatID, "Verified Seller access required.")
			return
		}
	}
	tasks, err := a.Marketplace.ListTasks(ctx, "", userID, 20)
	if err != nil {
		a.sendPlain(ctx, chatID, "Could not load your tasks.")
		return
	}
	for _, task := range tasks {
		kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{{Text: "👥 View applicants: " + task.Title, CallbackData: "apps:" + task.ID.Hex()}}}}
		_ = a.Bot.SendMessageWithInlineKeyboard(ctx, chatID, "Task: "+task.Title, kb)
	}
	if len(tasks) == 0 {
		a.sendPlain(ctx, chatID, "You have no tasks yet.")
		return
	}
}

func (a *App) sendSellerSubmissions(ctx context.Context, chatID, userID int64) {
	if !a.IsAdmin(userID) {
		user, err := a.Users.GetByTelegramID(ctx, userID)
		if err != nil || user.Role != "seller" {
			a.sendPlain(ctx, chatID, "Seller access required.")
			return
		}
		paid, err := a.Payments.HasPaidSellerRegistration(ctx, userID)
		if err != nil || !paid {
			a.sendPlain(ctx, chatID, "Verified Seller access required.")
			return
		}
	}
	sellerID := userID
	if a.IsAdmin(userID) {
		sellerID = 0
	}
	subs, err := a.TaskFlow.ListSellerSubmissions(ctx, sellerID)
	if err != nil {
		a.sendPlain(ctx, chatID, "Could not load submissions.")
		return
	}
	if len(subs) == 0 {
		a.sendWithKeyboard(ctx, chatID, "No submissions are waiting for review.", a.mainMenuKeyboard(ctx, userID))
		return
	}
	for _, sub := range subs {
		text := fmt.Sprintf("📥 WORK SUBMISSION\nWorker: %d\nStatus: %s\nProof:\n%s\nSubmission ID: %s", sub.WorkerID, sub.Status, sub.Proof, sub.ID.Hex())
		if sub.PrecheckStatus != "" {
			text += "\nPre-check: " + sub.PrecheckStatus
		}
		if sub.Status == "flagged_review" && sub.ReviewNote != "" {
			text += "\n⚠️ Strict risk flags: " + sub.ReviewNote
		}
		if strings.HasPrefix(sub.Proof, "photo_file_id:") {
			fileID := strings.TrimPrefix(strings.SplitN(sub.Proof, "\n", 2)[0], "photo_file_id:")
			_ = a.Bot.SendPhoto(ctx, chatID, fileID, fmt.Sprintf("Worker %d — submitted proof", sub.WorkerID))
			text = fmt.Sprintf("📥 WORK SUBMISSION\nWorker: %d\nStatus: %s\nPhoto proof attached above\nPre-check: %s\nSubmission ID: %s", sub.WorkerID, sub.Status, sub.PrecheckStatus, sub.ID.Hex())
			if sub.Status == "flagged_review" && sub.ReviewNote != "" {
				text += "\n⚠️ Strict risk flags: " + sub.ReviewNote
			}
		}
		kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
			{{Text: "✅ Approve", CallbackData: "reviewok:" + sub.ID.Hex()}, {Text: "✏️ Revision", CallbackData: "reviewrev:" + sub.ID.Hex()}},
			{{Text: "❌ Reject", CallbackData: "reviewno:" + sub.ID.Hex()}},
		}}
		_ = a.Bot.SendMessageWithInlineKeyboard(ctx, chatID, text, kb)
	}
}

func (a *App) taskFlowHealth() string { return time.Now().UTC().Format(time.RFC3339) }
