package main

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type User struct {
	TelegramID int64     `bson:"telegram_id" json:"telegram_id"`
	Username   string    `bson:"username,omitempty" json:"username,omitempty"`
	FirstName  string    `bson:"first_name,omitempty" json:"first_name,omitempty"`
	Role       string    `bson:"role" json:"role"`
	Status     string    `bson:"status" json:"status"`
	CreatedAt  time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt  time.Time `bson:"updated_at" json:"updated_at"`
}

type Listing struct {
	ID          string    `bson:"_id" json:"id"`
	OwnerID     int64     `bson:"owner_id" json:"owner_id"`
	Title       string    `bson:"title" json:"title"`
	Description string    `bson:"description,omitempty" json:"description,omitempty"`
	PriceStars  int64     `bson:"price_stars" json:"price_stars"`
	Status      string    `bson:"status" json:"status"`
	CreatedAt   time.Time `bson:"created_at" json:"created_at"`
}

type Order struct {
	ID        string    `bson:"_id" json:"id"`
	BuyerID   int64     `bson:"buyer_id" json:"buyer_id"`
	SellerID  int64     `bson:"seller_id" json:"seller_id"`
	ListingID string    `bson:"listing_id" json:"listing_id"`
	Status    string    `bson:"status" json:"status"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}

type Withdrawal struct {
	ID             string    `bson:"_id" json:"id"`
	UserID         int64     `bson:"user_id" json:"user_id"`
	AmountUSDT     string    `bson:"amount_usdt" json:"amount_usdt"`
	Network        string    `bson:"network" json:"network"`
	WalletAddress  string    `bson:"wallet_address" json:"wallet_address"`
	Status         string    `bson:"status" json:"status"`
	RequiresReview bool      `bson:"requires_review" json:"requires_review"`
	CreatedAt      time.Time `bson:"created_at" json:"created_at"`
}

type Task struct {
	ID                      primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	CreatorID               int64              `bson:"creator_id" json:"creator_id"`
	CreatorRole             string             `bson:"creator_role" json:"creator_role"`
	Title                   string             `bson:"title" json:"title"`
	Description             string             `bson:"description" json:"description"`
	RewardUSDT              string             `bson:"reward_usdt" json:"reward_usdt"`
	BudgetUSDT              string             `bson:"budget_usdt,omitempty" json:"budget_usdt,omitempty"`
	Slots                   int                `bson:"slots" json:"slots"`
	Status                  string             `bson:"status" json:"status"`
	FundingStatus           string             `bson:"funding_status,omitempty" json:"funding_status,omitempty"`
	FundingTxHash           string             `bson:"funding_tx_hash,omitempty" json:"funding_tx_hash,omitempty"`
	VerificationMode        string             `bson:"verification_mode,omitempty" json:"verification_mode,omitempty"`
	VerificationValue       string             `bson:"verification_value,omitempty" json:"verification_value,omitempty"`
	VerificationProvider    string             `bson:"verification_provider,omitempty" json:"verification_provider,omitempty"`
	VerificationProviderRef string             `bson:"verification_provider_ref,omitempty" json:"verification_provider_ref,omitempty"`
	TaskType                string             `bson:"task_type,omitempty" json:"task_type,omitempty"`
	TargetURL               string             `bson:"target_url,omitempty" json:"target_url,omitempty"`
	InvestmentUSDT          string             `bson:"investment_usdt,omitempty" json:"investment_usdt,omitempty"`
	OwnerShareUSDT          string             `bson:"owner_share_usdt,omitempty" json:"owner_share_usdt,omitempty"`
	WorkerPoolUSDT          string             `bson:"worker_pool_usdt,omitempty" json:"worker_pool_usdt,omitempty"`
	FundingSource           string             `bson:"funding_source,omitempty" json:"funding_source,omitempty"`
	FundedBy                int64              `bson:"funded_by,omitempty" json:"funded_by,omitempty"`
	FundedAt                *time.Time         `bson:"funded_at,omitempty" json:"funded_at,omitempty"`
	CreatedAt               time.Time          `bson:"created_at" json:"created_at"`
	UpdatedAt               time.Time          `bson:"updated_at" json:"updated_at"`
}

type TaskDraft struct {
	TelegramID  int64     `bson:"telegram_id"`
	Step        string    `bson:"step"`
	Title       string    `bson:"title,omitempty"`
	Description string    `bson:"description,omitempty"`
	RewardUSDT  string    `bson:"reward_usdt,omitempty"`
	Slots       int       `bson:"slots,omitempty"`
	CreatedAt   time.Time `bson:"created_at"`
	UpdatedAt   time.Time `bson:"updated_at"`
}

type SellerPayment struct {
	Payload          string     `bson:"_id" json:"payload"`
	TelegramID       int64      `bson:"telegram_id" json:"telegram_id"`
	Currency         string     `bson:"currency" json:"currency"`
	Amount           int64      `bson:"amount" json:"amount"`
	Status           string     `bson:"status" json:"status"`
	TelegramChargeID string     `bson:"telegram_charge_id,omitempty" json:"telegram_charge_id,omitempty"`
	CreatedAt        time.Time  `bson:"created_at" json:"created_at"`
	ExpiresAt        time.Time  `bson:"expires_at" json:"expires_at"`
	PaidAt           *time.Time `bson:"paid_at,omitempty" json:"paid_at,omitempty"`
}
