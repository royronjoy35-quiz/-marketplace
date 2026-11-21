package main

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type UserStore struct{ collection *mongo.Collection }

func NewUserStore(db *Database) *UserStore {
	return &UserStore{collection: db.DB.Collection("users")}
}

func (s *UserStore) UpsertTelegramUser(ctx context.Context, id int64, username, firstName string) error {
	now := time.Now().UTC()
	filter := bson.M{"telegram_id": id}
	update := bson.M{
		"$set":         bson.M{"username": username, "first_name": firstName, "updated_at": now},
		"$setOnInsert": bson.M{"telegram_id": id, "role": "user", "status": "active", "created_at": now},
	}
	_, err := s.collection.UpdateOne(ctx, filter, update, options.Update().SetUpsert(true))
	return err
}

func (s *UserStore) GetByTelegramID(ctx context.Context, id int64) (*User, error) {
	var user User
	err := s.collection.FindOne(ctx, bson.M{"telegram_id": id}).Decode(&user)
	if err != nil {
		return nil, err
	}
	return &user, nil
}
