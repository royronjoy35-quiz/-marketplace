package main

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type stores struct {
	mongo *mongo.Client
	db    *mongo.Database
	redis *redis.Client
}

func connectStores(ctx context.Context, cfg Config) (*stores, error) {
	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	mongoOpts := options.Client().
		ApplyURI(cfg.MongoURI).
		SetMaxPoolSize(100).
		SetMinPoolSize(5).
		SetConnectTimeout(8 * time.Second).
		SetServerSelectionTimeout(8 * time.Second)

	mc, err := mongo.Connect(connectCtx, mongoOpts)
	if err != nil {
		return nil, fmt.Errorf("connect MongoDB: %w", err)
	}
	if err := mc.Ping(connectCtx, nil); err != nil {
		_ = mc.Disconnect(context.Background())
		return nil, fmt.Errorf("ping MongoDB: %w", err)
	}

	ropts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		_ = mc.Disconnect(context.Background())
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	ropts.PoolSize = 50
	ropts.MinIdleConns = 5
	rc := redis.NewClient(ropts)
	if err := rc.Ping(connectCtx).Err(); err != nil {
		_ = rc.Close()
		_ = mc.Disconnect(context.Background())
		return nil, fmt.Errorf("ping Redis: %w", err)
	}
	return &stores{mongo: mc, db: mc.Database(cfg.MongoDatabase), redis: rc}, nil
}

func (s *stores) close(ctx context.Context) {
	if s.redis != nil {
		_ = s.redis.Close()
	}
	if s.mongo != nil {
		_ = s.mongo.Disconnect(ctx)
	}
}
