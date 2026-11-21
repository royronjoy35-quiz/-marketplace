package main

import "time"

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
