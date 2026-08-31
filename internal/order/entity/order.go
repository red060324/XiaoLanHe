package entity

import (
	"errors"
	"time"
)

var (
	ErrInvalidTotals = errors.New("invalid order totals")
	ErrInvalidState  = errors.New("invalid order state")
)

type Status string

const (
	StatusPendingPayment Status = "pending_payment"
	StatusPaid           Status = "paid"
	StatusCancelled      Status = "cancelled"
	StatusExpired        Status = "expired"
)

type Item struct {
	EditionID      int64
	GameID         int64
	GameSlug       string
	GameName       string
	EditionCode    string
	EditionName    string
	UnitPriceMinor int64
	Region         string
}

type Payment struct {
	ID                int64
	Provider          string
	ProviderReference string
	Status            string
	AmountMinor       int64
	CreatedAt         time.Time
}

type Order struct {
	ID            int64
	OrderNo       string
	UserID        int64
	Status        Status
	Currency      string
	SubtotalMinor int64
	DiscountMinor int64
	TotalMinor    int64
	CouponClaimID int64
	Item          Item
	Payment       *Payment
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func CalculateTotals(subtotal, discount int64) (int64, error) {
	if subtotal < 0 || discount < 0 || discount > subtotal {
		return 0, ErrInvalidTotals
	}
	return subtotal - discount, nil
}

func (o Order) ValidatePay() error {
	if o.Status != StatusPendingPayment {
		return ErrInvalidState
	}
	return nil
}
