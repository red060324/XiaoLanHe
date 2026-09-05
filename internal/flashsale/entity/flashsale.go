package entity

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

var (
	ErrInvalidActivity = errors.New("invalid flash sale activity")
	ErrInvalidState    = errors.New("invalid flash sale state")
)

var (
	activityCodePattern = regexp.MustCompile("^[A-Z0-9-]{3,64}$")
	regionPattern       = regexp.MustCompile("^[A-Z0-9-]{2,16}$")
	currencyPattern     = regexp.MustCompile("^[A-Z]{3}$")
)

type ActivityStatus string

const (
	StatusDraft     ActivityStatus = "draft"
	StatusActive    ActivityStatus = "active"
	StatusCancelled ActivityStatus = "cancelled"
	StatusEnded     ActivityStatus = "ended"
)

type Activity struct {
	ID                   int64
	Code                 string
	GameSlug             string
	GameName             string
	EditionID            int64
	EditionName          string
	Region               string
	Currency             string
	SalePriceMinor       int64
	TotalStock           int64
	AllocatedStock       int64
	Status               ActivityStatus
	StartsAt             time.Time
	EndsAt               time.Time
	PaymentTimeout       time.Duration
	Version              int64
	CreatedBy            int64
	ActivatedAt          time.Time
	CancelledAt          time.Time
	CreatedAt, UpdatedAt time.Time
}

func (a *Activity) Normalize() {
	a.Code = strings.ToUpper(strings.TrimSpace(a.Code))
	a.Region = strings.ToUpper(strings.TrimSpace(a.Region))
	a.Currency = strings.ToUpper(strings.TrimSpace(a.Currency))
	a.StartsAt = a.StartsAt.UTC()
	a.EndsAt = a.EndsAt.UTC()
}

func (a Activity) ValidateDraft(now time.Time, catalogPriceMinor int64) error {
	a.Normalize()
	if !activityCodePattern.MatchString(a.Code) || a.EditionID <= 0 ||
		!regionPattern.MatchString(a.Region) || !currencyPattern.MatchString(a.Currency) ||
		a.SalePriceMinor < 0 || catalogPriceMinor < 0 || a.SalePriceMinor > catalogPriceMinor ||
		a.TotalStock <= 0 || a.AllocatedStock != 0 || !a.StartsAt.Before(a.EndsAt) ||
		!now.UTC().Before(a.EndsAt) || a.PaymentTimeout < time.Minute || a.PaymentTimeout > 24*time.Hour {
		return ErrInvalidActivity
	}
	return nil
}

func (a *Activity) Activate(now time.Time) error {
	if a.Status != StatusDraft || !now.UTC().Before(a.EndsAt) {
		return ErrInvalidState
	}
	a.Status = StatusActive
	a.Version++
	if a.Version <= 0 {
		a.Version = 1
	}
	a.ActivatedAt = now.UTC()
	a.UpdatedAt = now.UTC()
	return nil
}

func (a *Activity) Cancel(now time.Time) error {
	if a.Status == StatusCancelled {
		return nil
	}
	if a.Status != StatusActive {
		return ErrInvalidState
	}
	a.Status = StatusCancelled
	a.CancelledAt = now.UTC()
	a.UpdatedAt = now.UTC()
	return nil
}

func (a Activity) AcceptsReservationTime(value time.Time) bool {
	value = value.UTC()
	if value.Before(a.StartsAt) || !value.Before(a.EndsAt) {
		return false
	}
	switch a.Status {
	case StatusActive:
		return true
	case StatusCancelled:
		return !a.CancelledAt.IsZero() && !value.After(a.CancelledAt)
	default:
		return false
	}
}

type ReservationStatus string

const (
	ReservationReserved   ReservationStatus = "reserved"
	ReservationOrderReady ReservationStatus = "order_ready"
	ReservationFailed     ReservationStatus = "failed"
	ReservationExpired    ReservationStatus = "expired"
)

type Reservation struct {
	RequestID         string
	ActivityID        int64
	UserID            int64
	IdempotencyDigest string
	Status            ReservationStatus
	OrderID           int64
	OrderNo           string
	FailureCode       string
	ReservedAt        time.Time
	PaymentExpiresAt  time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}
