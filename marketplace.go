package main

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	TaskStatusActive        = "active"
	TaskStatusPendingReview = "pending_review"
	TaskStatusRejected      = "rejected"
)

type MarketplaceStore struct {
	tasks  *mongo.Collection
	drafts *mongo.Collection
}

func NewMarketplaceStore(db *Database) *MarketplaceStore {
	return &MarketplaceStore{
		tasks:  db.DB.Collection("tasks"),
		drafts: db.DB.Collection("task_drafts"),
	}
}

func (s *MarketplaceStore) StartDraft(ctx context.Context, telegramID int64) error {
	now := time.Now().UTC()
	_, err := s.drafts.UpdateOne(
		ctx,
		bson.M{"telegram_id": telegramID},
		bson.M{"$set": bson.M{"step": "title", "updated_at": now}, "$setOnInsert": bson.M{"telegram_id": telegramID, "created_at": now}, "$unset": bson.M{"title": "", "description": "", "reward_usdt": "", "slots": ""}},
		options.Update().SetUpsert(true),
	)
	return err
}

func (s *MarketplaceStore) GetDraft(ctx context.Context, telegramID int64) (*TaskDraft, error) {
	var draft TaskDraft
	err := s.drafts.FindOne(ctx, bson.M{"telegram_id": telegramID}).Decode(&draft)
	if err != nil {
		return nil, err
	}
	return &draft, nil
}

func (s *MarketplaceStore) SaveDraft(ctx context.Context, draft *TaskDraft) error {
	draft.UpdatedAt = time.Now().UTC()
	_, err := s.drafts.ReplaceOne(ctx, bson.M{"telegram_id": draft.TelegramID}, draft, options.Replace().SetUpsert(true))
	return err
}

func (s *MarketplaceStore) DeleteDraft(ctx context.Context, telegramID int64) error {
	_, err := s.drafts.DeleteOne(ctx, bson.M{"telegram_id": telegramID})
	return err
}

func (s *MarketplaceStore) CreateTask(ctx context.Context, task *Task) error {
	now := time.Now().UTC()
	task.ID = primitive.NewObjectID()
	task.CreatedAt = now
	task.UpdatedAt = now
	_, err := s.tasks.InsertOne(ctx, task)
	return err
}

func (s *MarketplaceStore) ListTasks(ctx context.Context, status string, creatorID int64, limit int64) ([]Task, error) {
	filter := bson.M{}
	if status != "" {
		filter["status"] = status
	}
	if creatorID > 0 {
		filter["creator_id"] = creatorID
	}
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	findOptions := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(limit)
	cursor, err := s.tasks.Find(ctx, filter, findOptions)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var tasks []Task
	if err := cursor.All(ctx, &tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (s *MarketplaceStore) UpdateTaskStatus(ctx context.Context, id primitive.ObjectID, status string) error {
	result, err := s.tasks.UpdateOne(
		ctx,
		bson.M{"_id": id, "status": TaskStatusPendingReview},
		bson.M{"$set": bson.M{"status": status, "updated_at": time.Now().UTC()}},
	)
	if err != nil {
		return err
	}
	if result.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
}

func ValidateUSDTAmount(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		return false
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return false
			}
		}
	}
	if len(parts) == 2 && len(parts[1]) > 6 {
		return false
	}
	r, ok := new(big.Rat).SetString(value)
	return ok && r.Sign() > 0
}

func FormatTaskList(tasks []Task, emptyMessage string) string {
	if len(tasks) == 0 {
		return emptyMessage
	}
	var b strings.Builder
	for i, task := range tasks {
		fmt.Fprintf(&b, "%d. %s\n", i+1, task.Title)
		fmt.Fprintf(&b, "   Reward: %s USDT | Slots: %d\n", task.RewardUSDT, task.Slots)
		fmt.Fprintf(&b, "   Status: %s\n", task.Status)
		fmt.Fprintf(&b, "   Task ID: %s\n\n", task.ID.Hex())
	}
	return strings.TrimSpace(b.String())
}
