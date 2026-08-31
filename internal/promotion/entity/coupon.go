package entity

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrUnavailable = errors.New("coupon unavailable")
	ErrExhausted   = errors.New("coupon exhausted")
	ErrIneligible  = errors.New("coupon ineligible")
)

type DiscountType string

const (
	DiscountFixed      DiscountType = "fixed"
	DiscountPercentage DiscountType = "percentage"
)

type Coupon struct {
	ID               int64
	Code             string
	Name             string
	DiscountType     DiscountType
	FixedMinor       int64
	PercentageBps    int64
	Currency         string
	MinimumMinor     int64
	TotalStock       int64
	ClaimedStock     int64
	PerUserLimit     int
	GameID           int64
	EditionID        int64
	CampaignStatus   string
	StartsAt         time.Time
	EndsAt           time.Time
	ViewerClaimCount int
}

func (c Coupon) RemainingStock() int64 {
	if c.ClaimedStock >= c.TotalStock {
		return 0
	}
	return c.TotalStock - c.ClaimedStock
}

func (c Coupon) ValidateClaim(now time.Time) error {
	if err := c.ValidateUse(now); err != nil {
		return err
	}
	if c.RemainingStock() == 0 {
		return ErrExhausted
	}
	return nil
}

func (c Coupon) ValidateUse(now time.Time) error {
	if c.CampaignStatus != "active" || now.Before(c.StartsAt) || !now.Before(c.EndsAt) {
		return ErrUnavailable
	}
	return nil
}

func (c Coupon) Discount(subtotal int64, currency string, gameID, editionID int64) (int64, error) {
	if subtotal < c.MinimumMinor || !strings.EqualFold(currency, c.Currency) || (c.GameID != 0 && c.GameID != gameID) || (c.EditionID != 0 && c.EditionID != editionID) {
		return 0, ErrIneligible
	}
	var discount int64
	switch c.DiscountType {
	case DiscountFixed:
		discount = c.FixedMinor
	case DiscountPercentage:
		discount = subtotal/10000*c.PercentageBps + subtotal%10000*c.PercentageBps/10000
	default:
		return 0, ErrIneligible
	}
	if discount > subtotal {
		return subtotal, nil
	}
	return discount, nil
}

type Claim struct {
	ID             int64
	CouponID       int64
	CouponCode     string
	UserID         int64
	Status         string
	IdempotencyKey string
	ClaimedAt      time.Time
}

type Quote struct {
	ClaimID       int64
	CouponID      int64
	CouponCode    string
	DiscountMinor int64
}
