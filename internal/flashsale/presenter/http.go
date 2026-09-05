package presenter

import (
	"strconv"
	"time"

	"github.com/red060324/XiaoLanHe/internal/flashsale/entity"
	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
)

type ActivityRequest struct {
	Code                  string `json:"code"`
	EditionID             string `json:"editionId"`
	Region                string `json:"region"`
	Currency              string `json:"currency"`
	SalePriceMinor        int64  `json:"salePriceMinor"`
	TotalStock            int64  `json:"totalStock"`
	StartsAt              string `json:"startsAt"`
	EndsAt                string `json:"endsAt"`
	PaymentTimeoutSeconds int64  `json:"paymentTimeoutSeconds"`
}

func (r ActivityRequest) Activity(id int64) (entity.Activity, error) {
	editionID, err := strconv.ParseInt(r.EditionID, 10, 64)
	if err != nil || editionID <= 0 {
		return entity.Activity{}, flashsale.ErrInvalidInput
	}
	startsAt, err := time.Parse(time.RFC3339Nano, r.StartsAt)
	if err != nil {
		return entity.Activity{}, flashsale.ErrInvalidInput
	}
	endsAt, err := time.Parse(time.RFC3339Nano, r.EndsAt)
	if err != nil {
		return entity.Activity{}, flashsale.ErrInvalidInput
	}
	return entity.Activity{
		ID: id, Code: r.Code, EditionID: editionID, Region: r.Region, Currency: r.Currency,
		SalePriceMinor: r.SalePriceMinor, TotalStock: r.TotalStock, StartsAt: startsAt.UTC(), EndsAt: endsAt.UTC(),
		PaymentTimeout: time.Duration(r.PaymentTimeoutSeconds) * time.Second,
	}, nil
}

type ActivityResponse struct {
	ID                    string `json:"id"`
	Code                  string `json:"code"`
	GameSlug              string `json:"gameSlug"`
	GameName              string `json:"gameName"`
	EditionID             string `json:"editionId"`
	EditionName           string `json:"editionName"`
	Region                string `json:"region"`
	Currency              string `json:"currency"`
	SalePriceMinor        int64  `json:"salePriceMinor"`
	TotalStock            int64  `json:"totalStock,omitempty"`
	Status                string `json:"status"`
	StartsAt              string `json:"startsAt"`
	EndsAt                string `json:"endsAt"`
	PaymentTimeoutSeconds int64  `json:"paymentTimeoutSeconds,omitempty"`
	Availability          string `json:"availability"`
}

func PresentActivity(value entity.Activity, admin bool, now time.Time) ActivityResponse {
	result := ActivityResponse{
		ID: strconv.FormatInt(value.ID, 10), Code: value.Code, GameSlug: value.GameSlug, GameName: value.GameName,
		EditionID: strconv.FormatInt(value.EditionID, 10), EditionName: value.EditionName, Region: value.Region, Currency: value.Currency,
		SalePriceMinor: value.SalePriceMinor, Status: string(value.Status), StartsAt: formatTime(value.StartsAt), EndsAt: formatTime(value.EndsAt),
		Availability: availability(value, now.UTC()),
	}
	if admin {
		result.TotalStock = value.TotalStock
		result.PaymentTimeoutSeconds = int64(value.PaymentTimeout / time.Second)
	}
	return result
}

type RequestResponse struct {
	RequestID        string `json:"requestId"`
	ActivityID       string `json:"activityId"`
	Status           string `json:"status"`
	OrderNo          string `json:"orderNo"`
	FailureCode      string `json:"failureCode"`
	PaymentExpiresAt string `json:"paymentExpiresAt"`
}

func PresentRequest(value flashsale.Request) RequestResponse {
	result := RequestResponse{
		RequestID: value.RequestID, ActivityID: strconv.FormatInt(value.ActivityID, 10), Status: string(value.Status),
		OrderNo: value.OrderNo, FailureCode: value.FailureCode,
	}
	if !value.PaymentExpiresAt.IsZero() {
		result.PaymentExpiresAt = formatTime(value.PaymentExpiresAt)
	}
	return result
}

func availability(value entity.Activity, now time.Time) string {
	switch value.Status {
	case entity.StatusCancelled:
		return "cancelled"
	case entity.StatusEnded:
		return "ended"
	case entity.StatusActive:
		if now.Before(value.StartsAt) {
			return "upcoming"
		}
		if !now.Before(value.EndsAt) {
			return "ended"
		}
		if value.AllocatedStock >= value.TotalStock {
			return "exhausted"
		}
		return "available"
	default:
		return "unavailable"
	}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
