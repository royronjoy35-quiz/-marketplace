package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

const (
	SellerRegistrationStars int64 = 100
	sellerPaymentPending          = "pending"
	sellerPaymentPaid             = "paid"
	sellerPaymentExpired          = "expired"
)

type PaymentStore struct {
	payments *mongo.Collection
}

func NewPaymentStore(db *Database) *PaymentStore {
	return &PaymentStore{payments: db.DB.Collection("seller_payments")}
}

func (s *PaymentStore) CreateSellerPayment(ctx context.Context, telegramID int64) (*SellerPayment, error) {
	// Invalidate older unpaid invoices so a user cannot accidentally pay multiple open invoices.
	_, err := s.payments.UpdateMany(ctx,
		bson.M{"telegram_id": telegramID, "status": sellerPaymentPending},
		bson.M{"$set": bson.M{"status": sellerPaymentExpired}},
	)
	if err != nil {
		return nil, err
	}

	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	payload := fmt.Sprintf("seller:%d:%s", telegramID, hex.EncodeToString(random))
	now := time.Now().UTC()
	payment := &SellerPayment{
		Payload:    payload,
		TelegramID: telegramID,
		Currency:   "XTR",
		Amount:     SellerRegistrationStars,
		Status:     sellerPaymentPending,
		CreatedAt:  now,
		ExpiresAt:  now.Add(30 * time.Minute),
	}
	_, err = s.payments.InsertOne(ctx, payment)
	if err != nil {
		return nil, err
	}
	return payment, nil
}

func (s *PaymentStore) GetSellerPayment(ctx context.Context, payload string, telegramID int64) (*SellerPayment, error) {
	var payment SellerPayment
	err := s.payments.FindOne(ctx, bson.M{"_id": payload, "telegram_id": telegramID}).Decode(&payment)
	if err != nil {
		return nil, err
	}
	return &payment, nil
}

func (s *PaymentStore) MarkSellerPaymentPaid(ctx context.Context, payload string, telegramID int64, chargeID string) error {
	existing, err := s.GetSellerPayment(ctx, payload, telegramID)
	if err != nil {
		return err
	}
	if existing.Currency != "XTR" || existing.Amount != SellerRegistrationStars {
		return fmt.Errorf("stored seller invoice amount or currency mismatch")
	}
	// Telegram can redeliver the same successful-payment update. Keep this operation idempotent.
	if existing.Status == sellerPaymentPaid && existing.TelegramChargeID == chargeID {
		return nil
	}
	if existing.Status != sellerPaymentPending {
		return fmt.Errorf("seller payment is not pending or charge does not match")
	}

	now := time.Now().UTC()
	result, err := s.payments.UpdateOne(ctx,
		bson.M{"_id": payload, "telegram_id": telegramID, "status": sellerPaymentPending},
		bson.M{"$set": bson.M{"status": sellerPaymentPaid, "telegram_charge_id": chargeID, "paid_at": now}},
	)
	if err != nil {
		return err
	}
	if result.ModifiedCount == 0 {
		// A concurrent duplicate update may have completed it; verify the exact charge.
		latest, findErr := s.GetSellerPayment(ctx, payload, telegramID)
		if findErr != nil {
			return findErr
		}
		if latest.Status == sellerPaymentPaid && latest.TelegramChargeID == chargeID {
			return nil
		}
		return fmt.Errorf("seller payment could not be finalized")
	}
	return nil
}

func (s *PaymentStore) ExpirePendingSellerPayment(ctx context.Context, payload string, telegramID int64) error {
	_, err := s.payments.UpdateOne(ctx,
		bson.M{"_id": payload, "telegram_id": telegramID, "status": sellerPaymentPending},
		bson.M{"$set": bson.M{"status": sellerPaymentExpired}},
	)
	return err
}

func (s *PaymentStore) HasPaidSellerRegistration(ctx context.Context, telegramID int64) (bool, error) {
	count, err := s.payments.CountDocuments(ctx, bson.M{
		"telegram_id": telegramID,
		"currency":    "XTR",
		"amount":      SellerRegistrationStars,
		"status":      sellerPaymentPaid,
	})
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
