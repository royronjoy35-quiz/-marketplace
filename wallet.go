package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	StarsPerUSD = int64(1000) // 1,000 Telegram Stars = 12 USDT/USD accounting units.
	MicroUSDT   = int64(1_000_000)
)

type WalletTopupPayment struct {
	Payload          string    `bson:"_id"`
	TelegramID       int64     `bson:"telegram_id"`
	Stars            int64     `bson:"stars"`
	AmountMicroUSDT  int64     `bson:"amount_micro_usdt"`
	Currency         string    `bson:"currency"`
	Status           string    `bson:"status"`
	TelegramChargeID string    `bson:"telegram_charge_id,omitempty"`
	CreatedAt        time.Time `bson:"created_at"`
	ExpiresAt        time.Time `bson:"expires_at"`
	PaidAt           time.Time `bson:"paid_at,omitempty"`
}

type WalletStore struct {
	db       *Database
	wallets  *mongo.Collection
	payments *mongo.Collection
	ledger   *mongo.Collection
	treasury *mongo.Collection
	escrows  *mongo.Collection
	tasks    *mongo.Collection
}

func NewWalletStore(db *Database) *WalletStore {
	return &WalletStore{
		db:       db,
		wallets:  db.DB.Collection("seller_wallets"),
		payments: db.DB.Collection("wallet_topup_payments"),
		ledger:   db.DB.Collection("wallet_ledger"),
		treasury: db.DB.Collection("owner_treasury"),
		escrows:  db.DB.Collection("task_escrows"),
		tasks:    db.DB.Collection("tasks"),
	}
}

func (s *WalletStore) InitializeTreasury(ctx context.Context, initialUSDT string) error {
	micro, err := usdtToMicro(initialUSDT)
	if err != nil {
		return err
	}
	_, err = s.treasury.UpdateOne(ctx, bson.M{"_id": "owner"}, bson.M{"$setOnInsert": bson.M{
		"balance_micro": micro, "created_at": time.Now().UTC(), "updated_at": time.Now().UTC(),
	}}, options.Update().SetUpsert(true))
	return err
}

func (s *WalletStore) Balance(ctx context.Context, telegramID int64) (int64, error) {
	var doc struct {
		Balance int64 `bson:"balance_micro"`
	}
	err := s.wallets.FindOne(ctx, bson.M{"_id": telegramID}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return 0, nil
	}
	return doc.Balance, err
}

func (s *WalletStore) TreasuryBalance(ctx context.Context) (int64, error) {
	var doc struct {
		Balance int64 `bson:"balance_micro"`
	}
	err := s.treasury.FindOne(ctx, bson.M{"_id": "owner"}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return 0, nil
	}
	return doc.Balance, err
}

func (s *WalletStore) TreasurySummary(ctx context.Context) (available, ownerRevenue, pendingRevenue int64, err error) {
	var doc struct {
		Available int64 `bson:"balance_micro"`
		Revenue   int64 `bson:"owner_revenue_micro"`
		Pending   int64 `bson:"owner_revenue_pending_micro"`
	}
	err = s.treasury.FindOne(ctx, bson.M{"_id": "owner"}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return 0, 0, 0, nil
	}
	return doc.Available, doc.Revenue, doc.Pending, err
}

func (s *WalletStore) CreateTopup(ctx context.Context, userID, stars int64) (*WalletTopupPayment, error) {
	if userID <= 0 || stars < 10 || stars > 100000 || stars%10 != 0 {
		return nil, fmt.Errorf("top-up must be 10–100000 Stars in multiples of 10")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	payload := fmt.Sprintf("wallet:%d:%s", userID, hex.EncodeToString(random))
	// 1 Star = 0.012 USDT; use integer micro-USDT to avoid floating-point errors.
	amount := stars * 12_000
	p := &WalletTopupPayment{Payload: payload, TelegramID: userID, Stars: stars, AmountMicroUSDT: amount,
		Currency: "XTR", Status: "pending", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(30 * time.Minute)}
	_, err := s.payments.InsertOne(ctx, p)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *WalletStore) GetTopup(ctx context.Context, payload string, userID int64) (*WalletTopupPayment, error) {
	var p WalletTopupPayment
	err := s.payments.FindOne(ctx, bson.M{"_id": payload, "telegram_id": userID}).Decode(&p)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *WalletStore) ConfirmTopup(ctx context.Context, payload string, userID, stars int64, chargeID string) error {
	if strings.TrimSpace(chargeID) == "" {
		return fmt.Errorf("Telegram charge ID is missing")
	}
	session, err := s.db.Client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(sc mongo.SessionContext) (any, error) {
		p, err := s.GetTopup(sc, payload, userID)
		if err != nil {
			return nil, err
		}
		if p.Currency != "XTR" || p.Stars != stars || p.Status != "pending" || time.Now().UTC().After(p.ExpiresAt) {
			if p.Status == "paid" && p.TelegramChargeID == chargeID {
				return nil, nil
			}
			return nil, fmt.Errorf("wallet invoice is invalid, expired, or already processed")
		}
		res, err := s.payments.UpdateOne(sc, bson.M{"_id": payload, "telegram_id": userID, "status": "pending"}, bson.M{"$set": bson.M{"status": "paid", "telegram_charge_id": chargeID, "paid_at": time.Now().UTC()}})
		if err != nil {
			return nil, err
		}
		if res.ModifiedCount != 1 {
			return nil, fmt.Errorf("wallet payment was already processed")
		}
		_, err = s.wallets.UpdateOne(sc, bson.M{"_id": userID}, bson.M{
			"$inc":         bson.M{"balance_micro": p.AmountMicroUSDT},
			"$set":         bson.M{"updated_at": time.Now().UTC()},
			"$setOnInsert": bson.M{"created_at": time.Now().UTC()},
		}, options.Update().SetUpsert(true))
		if err != nil {
			return nil, err
		}
		_, err = s.ledger.InsertOne(sc, bson.M{"_id": "topup:" + payload, "telegram_id": userID, "type": "topup", "stars": stars,
			"amount_micro": p.AmountMicroUSDT, "currency": "USDT", "charge_id": chargeID, "created_at": time.Now().UTC()})
		return nil, err
	})
	return err
}

func (s *WalletStore) CreditTreasury(ctx context.Context, adminID int64, amount string, note string) error {
	micro, err := usdtToMicro(amount)
	if err != nil || micro <= 0 {
		return fmt.Errorf("invalid treasury credit amount")
	}
	if len(strings.TrimSpace(note)) < 4 {
		return fmt.Errorf("provide a short funding reference or note (at least 4 characters)")
	}
	refHash := sha256.Sum256([]byte(strings.TrimSpace(note)))
	ref := "admin-credit:" + hex.EncodeToString(refHash[:])
	// This command records funds already deposited by the owner; it does not itself move or verify cryptocurrency.
	session, err := s.db.Client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(sc mongo.SessionContext) (any, error) {
		now := time.Now().UTC()
		if _, e := s.ledger.InsertOne(sc, bson.M{"_id": ref, "type": "treasury_credit", "admin_id": adminID, "amount_micro": micro, "note": note, "created_at": now}); e != nil {
			return nil, fmt.Errorf("funding reference already recorded or could not be saved: %w", e)
		}
		if _, e := s.treasury.UpdateOne(sc, bson.M{"_id": "owner"}, bson.M{"$inc": bson.M{"balance_micro": micro}, "$set": bson.M{"updated_at": now}}, options.Update().SetUpsert(true)); e != nil {
			return nil, e
		}
		return nil, nil
	})
	return err
}

// CreateFundedTask reserves Seller wallet or Owner Treasury and inserts the funded task
// in one MongoDB transaction. Amounts are integer micro-USDT.
func (s *WalletStore) CreateFundedTask(ctx context.Context, task *Task, investment, ownerShare, workerPool int64, admin bool) error {
	if investment <= 0 || ownerShare < 0 || workerPool <= 0 || ownerShare+workerPool != investment {
		return fmt.Errorf("invalid task funding split")
	}
	session, err := s.db.Client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(sc mongo.SessionContext) (any, error) {
		now := time.Now().UTC()
		if admin {
			res, err := s.treasury.UpdateOne(sc, bson.M{"_id": "owner", "balance_micro": bson.M{"$gte": investment}}, bson.M{"$inc": bson.M{"balance_micro": -investment, "owner_revenue_micro": ownerShare}, "$set": bson.M{"updated_at": now}})
			if err != nil {
				return nil, err
			}
			if res.MatchedCount == 0 {
				return nil, fmt.Errorf("Owner Treasury has insufficient available balance")
			}
			task.FundingSource = "owner_treasury"
		} else {
			res, err := s.wallets.UpdateOne(sc, bson.M{"_id": task.CreatorID, "balance_micro": bson.M{"$gte": investment}}, bson.M{"$inc": bson.M{"balance_micro": -investment}, "$set": bson.M{"updated_at": now}})
			if err != nil {
				return nil, err
			}
			if res.MatchedCount == 0 {
				return nil, fmt.Errorf("insufficient Seller Wallet balance")
			}
			_, err = s.treasury.UpdateOne(sc, bson.M{"_id": "owner"}, bson.M{"$inc": bson.M{"owner_revenue_pending_micro": ownerShare}, "$set": bson.M{"updated_at": now}}, options.Update().SetUpsert(true))
			if err != nil {
				return nil, err
			}
			task.FundingSource = "seller_wallet"
		}
		task.ID = primitive.NewObjectID()
		task.FundingStatus = "funded"
		task.FundedBy = task.CreatorID
		fundedAt := now
		task.FundedAt = &fundedAt
		task.CreatedAt, task.UpdatedAt = now, now
		if _, err := s.tasks.InsertOne(sc, task); err != nil {
			return nil, err
		}
		if _, err := s.escrows.InsertOne(sc, bson.M{"task_id": task.ID, "creator_id": task.CreatorID, "funding_source": task.FundingSource,
			"investment_micro": investment, "owner_share_micro": ownerShare, "worker_pool_micro": workerPool,
			"remaining_worker_pool_micro": workerPool, "status": "reserved", "created_at": now, "updated_at": now}); err != nil {
			return nil, err
		}
		ledgerID := fmt.Sprintf("task-investment:%s", task.ID.Hex())
		_, err := s.ledger.InsertOne(sc, bson.M{"_id": ledgerID, "telegram_id": task.CreatorID, "task_id": task.ID, "type": "task_investment",
			"funding_source": task.FundingSource, "amount_micro": investment, "owner_share_micro": ownerShare,
			"worker_pool_micro": workerPool, "created_at": now})
		if err != nil {
			return nil, err
		}
		if ownerShare > 0 {
			_, err = s.ledger.InsertOne(sc, bson.M{"_id": "owner-revenue:" + task.ID.Hex(), "type": "owner_revenue", "task_id": task.ID,
				"amount_micro": ownerShare, "created_at": now})
		}
		return nil, err
	})
	return err
}

func usdtToMicro(value string) (int64, error) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(value))
	if !ok || r.Sign() < 0 {
		return 0, fmt.Errorf("invalid USDT amount")
	}
	r.Mul(r, big.NewRat(MicroUSDT, 1))
	if !r.IsInt() {
		return 0, fmt.Errorf("USDT amount supports up to 6 decimal places")
	}
	if !r.Num().IsInt64() {
		return 0, fmt.Errorf("USDT amount is too large")
	}
	return r.Num().Int64(), nil
}

func microToUSDT(value int64) string {
	whole := value / MicroUSDT
	fraction := value % MicroUSDT
	return fmt.Sprintf("%d.%06d", whole, fraction)
}

func calculateTaskInvestment(reward string, slots int) (investment, ownerShare, workerPool int64, err error) {
	if slots < 1 || slots > 500 {
		return 0, 0, 0, fmt.Errorf("slots must be between 1 and 500")
	}
	rewardMicro, err := usdtToMicro(reward)
	if err != nil {
		return 0, 0, 0, err
	}
	minReward, _ := usdtToMicro("0.045")
	if rewardMicro < minReward {
		return 0, 0, 0, fmt.Errorf("minimum reward per task is 0.045 USDT")
	}
	workerPool = rewardMicro * int64(slots)
	// Investment = worker pool / 70%; round up to the nearest micro-USDT.
	n := new(big.Int).Mul(big.NewInt(workerPool), big.NewInt(10))
	n.Add(n, big.NewInt(6))
	n.Div(n, big.NewInt(7))
	if !n.IsInt64() {
		return 0, 0, 0, fmt.Errorf("task investment is too large")
	}
	investment = n.Int64()
	ownerShare = investment - workerPool
	return investment, ownerShare, workerPool, nil
}

// ApproveFundedTask releases the Seller's 30% owner share from pending revenue
// only after Admin moderation approves the task. Worker pool remains escrowed.
func (s *WalletStore) ApproveFundedTask(ctx context.Context, taskID primitive.ObjectID) error {
	session, err := s.db.Client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(sc mongo.SessionContext) (any, error) {
		var escrow struct {
			FundingSource    string `bson:"funding_source"`
			OwnerShare       int64  `bson:"owner_share_micro"`
			Status           string `bson:"status"`
			ModerationStatus string `bson:"moderation_status"`
		}
		if e := s.escrows.FindOne(sc, bson.M{"task_id": taskID}).Decode(&escrow); e != nil {
			if e == mongo.ErrNoDocuments {
				_, updateErr := s.tasks.UpdateOne(sc, bson.M{"_id": taskID, "status": TaskStatusPendingReview}, bson.M{"$set": bson.M{"status": TaskStatusActive, "updated_at": time.Now().UTC()}})
				if updateErr != nil {
					return nil, updateErr
				}
				return nil, nil
			}
			return nil, e
		}
		if escrow.Status != "reserved" || escrow.ModerationStatus == "approved" {
			return nil, fmt.Errorf("task escrow is not awaiting moderation")
		}
		now := time.Now().UTC()
		if escrow.FundingSource == "seller_wallet" && escrow.OwnerShare > 0 {
			if _, e := s.treasury.UpdateOne(sc, bson.M{"_id": "owner"}, bson.M{"$inc": bson.M{
				"owner_revenue_pending_micro": -escrow.OwnerShare,
				"owner_revenue_micro":         escrow.OwnerShare,
				"balance_micro":               escrow.OwnerShare,
			}, "$set": bson.M{"updated_at": now}}); e != nil {
				return nil, e
			}
		}
		if _, e := s.escrows.UpdateOne(sc, bson.M{"task_id": taskID, "status": "reserved"}, bson.M{"$set": bson.M{"moderation_status": "approved", "updated_at": now}}); e != nil {
			return nil, e
		}
		res, e := s.tasks.UpdateOne(sc, bson.M{"_id": taskID, "status": TaskStatusPendingReview}, bson.M{"$set": bson.M{"status": TaskStatusActive, "updated_at": now}})
		if e != nil {
			return nil, e
		}
		if res.MatchedCount == 0 {
			return nil, mongo.ErrNoDocuments
		}
		return nil, nil
	})
	return err
}

// RefundRejectedTask reverses the full reservation if an Admin rejects a Seller task.
// The owner fee and seller debit are reversed in the same transaction.
func (s *WalletStore) RefundRejectedTask(ctx context.Context, taskID primitive.ObjectID) error {
	session, err := s.db.Client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(sc mongo.SessionContext) (any, error) {
		var escrow struct {
			CreatorID        int64  `bson:"creator_id"`
			FundingSource    string `bson:"funding_source"`
			Investment       int64  `bson:"investment_micro"`
			OwnerShare       int64  `bson:"owner_share_micro"`
			Status           string `bson:"status"`
			ModerationStatus string `bson:"moderation_status"`
		}
		if e := s.escrows.FindOne(sc, bson.M{"task_id": taskID}).Decode(&escrow); e != nil {
			if e == mongo.ErrNoDocuments {
				// Backward compatibility for older, unfunded tasks created before Wallet Escrow.
				var legacy Task
				if findErr := s.tasks.FindOne(sc, bson.M{"_id": taskID, "funding_status": bson.M{"$ne": "funded"}}).Decode(&legacy); findErr != nil {
					return nil, e
				}
				_, updateErr := s.tasks.UpdateOne(sc, bson.M{"_id": taskID, "status": TaskStatusPendingReview}, bson.M{"$set": bson.M{"status": TaskStatusRejected, "updated_at": time.Now().UTC()}})
				return nil, updateErr
			}
			return nil, e
		}
		if escrow.Status == "refunded" {
			return nil, nil
		}
		if escrow.Status != "reserved" || escrow.ModerationStatus == "approved" {
			return nil, fmt.Errorf("task escrow is not refundable")
		}
		now := time.Now().UTC()
		switch escrow.FundingSource {
		case "seller_wallet":
			if _, e := s.wallets.UpdateOne(sc, bson.M{"_id": escrow.CreatorID}, bson.M{"$inc": bson.M{"balance_micro": escrow.Investment}, "$set": bson.M{"updated_at": now}}, options.Update().SetUpsert(true)); e != nil {
				return nil, e
			}
			if _, e := s.treasury.UpdateOne(sc, bson.M{"_id": "owner"}, bson.M{"$inc": bson.M{"owner_revenue_pending_micro": -escrow.OwnerShare}, "$set": bson.M{"updated_at": now}}); e != nil {
				return nil, e
			}
		case "owner_treasury":
			if _, e := s.treasury.UpdateOne(sc, bson.M{"_id": "owner"}, bson.M{"$inc": bson.M{"balance_micro": escrow.Investment, "owner_revenue_micro": -escrow.OwnerShare}, "$set": bson.M{"updated_at": now}}); e != nil {
				return nil, e
			}
		default:
			return nil, fmt.Errorf("unknown task funding source")
		}
		if _, e := s.escrows.UpdateOne(sc, bson.M{"task_id": taskID, "status": "reserved"}, bson.M{"$set": bson.M{"status": "refunded", "refunded_at": now, "updated_at": now}}); e != nil {
			return nil, e
		}
		if _, e := s.tasks.UpdateOne(sc, bson.M{"_id": taskID, "status": TaskStatusPendingReview}, bson.M{"$set": bson.M{"funding_status": "refunded", "status": TaskStatusRejected, "updated_at": now}}); e != nil {
			return nil, e
		}
		_, e := s.ledger.InsertOne(sc, bson.M{"_id": "task-refund:" + taskID.Hex(), "type": "task_refund", "task_id": taskID, "creator_id": escrow.CreatorID, "amount_micro": escrow.Investment, "created_at": now})
		return nil, e
	})
	return err
}
