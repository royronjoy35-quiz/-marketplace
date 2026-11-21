package main

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

type Database struct {
	Client *mongo.Client
	DB     *mongo.Database
}

func ConnectDatabase(ctx context.Context, uri, databaseName string) (*Database, error) {
	client, err := mongo.Connect(ctx, options.Client().
		ApplyURI(uri).
		SetServerSelectionTimeout(10*time.Second).
		SetConnectTimeout(10*time.Second))
	if err != nil {
		return nil, err
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, readpref.Primary()); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	return &Database{Client: client, DB: client.Database(databaseName)}, nil
}

// EnsureIndexes creates the uniqueness constraints required by task applications
// and earnings records. Safe to run on every deployment.
func EnsureIndexes(ctx context.Context, db *Database) error {
	_, err := db.DB.Collection("task_applications").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "task_id", Value: 1}, {Key: "worker_id", Value: 1}},
		Options: options.Index().SetUnique(true).SetName("uniq_task_worker_application"),
	})
	if err != nil {
		return err
	}
	_, err = db.DB.Collection("earnings_ledger").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "submission_id", Value: 1}},
		Options: options.Index().SetUnique(true).SetName("uniq_earning_submission"),
	})
	if err != nil {
		return err
	}
	_, err = db.DB.Collection("task_submissions").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "proof_hash", Value: 1}},
		Options: options.Index().SetUnique(true).SetSparse(true).SetName("uniq_task_submission_proof_hash"),
	})
	if err != nil {
		return err
	}
	_, err = db.DB.Collection("verification_receipts").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "nonce", Value: 1}},
		Options: options.Index().SetUnique(true).SetName("uniq_verification_receipt_nonce"),
	})
	if err != nil {
		return err
	}
	_, err = db.DB.Collection("verification_receipts").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "created_at", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(30 * 24 * 60 * 60).SetName("ttl_verification_receipts_30d"),
	})
	if err != nil {
		return err
	}
	indexes := []struct {
		collection string
		model      mongo.IndexModel
	}{
		{"task_wizards", mongo.IndexModel{Keys: bson.D{{Key: "telegram_id", Value: 1}}, Options: options.Index().SetUnique(true).SetName("uniq_task_wizard_user")}},
		{"wallet_topup_payments", mongo.IndexModel{Keys: bson.D{{Key: "telegram_id", Value: 1}, {Key: "created_at", Value: -1}}, Options: options.Index().SetName("wallet_topup_user_created")}},
		{"wallet_topup_payments", mongo.IndexModel{Keys: bson.D{{Key: "telegram_charge_id", Value: 1}}, Options: options.Index().SetUnique(true).SetSparse(true).SetName("uniq_wallet_telegram_charge")}},
		{"task_escrows", mongo.IndexModel{Keys: bson.D{{Key: "task_id", Value: 1}}, Options: options.Index().SetUnique(true).SetName("uniq_task_escrow")}},
	}
	for _, item := range indexes {
		if _, err := db.DB.Collection(item.collection).Indexes().CreateOne(ctx, item.model); err != nil {
			return err
		}
	}
	return nil
}
