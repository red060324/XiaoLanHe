package presenter

import (
	"strconv"
	"time"

	"github.com/red060324/XiaoLanHe/internal/order/entity"
	order "github.com/red060324/XiaoLanHe/internal/order/usecase"
)

type CreateRequest struct {
	EditionID     string `json:"editionId"`
	Region        string `json:"region"`
	Currency      string `json:"currency"`
	CouponClaimID string `json:"couponClaimId"`
}

func (r CreateRequest) Input(idempotencyKey string) (order.CreateInput, error) {
	editionID, err := requiredID(r.EditionID)
	if err != nil {
		return order.CreateInput{}, err
	}
	claimID, err := optionalID(r.CouponClaimID)
	if err != nil {
		return order.CreateInput{}, err
	}
	return order.CreateInput{EditionID: editionID, Region: r.Region, Currency: r.Currency, CouponClaimID: claimID, IdempotencyKey: idempotencyKey}, nil
}

type ItemResponse struct {
	EditionID      string `json:"editionId"`
	GameSlug       string `json:"gameSlug"`
	GameName       string `json:"gameName"`
	EditionCode    string `json:"editionCode"`
	EditionName    string `json:"editionName"`
	UnitPriceMinor int64  `json:"unitPriceMinor"`
	Region         string `json:"region"`
}

type PaymentResponse struct {
	Provider    string `json:"provider"`
	Reference   string `json:"reference"`
	Status      string `json:"status"`
	AmountMinor int64  `json:"amountMinor"`
	CreatedAt   string `json:"createdAt"`
}

type OrderResponse struct {
	OrderNo       string           `json:"orderNo"`
	Status        string           `json:"status"`
	Currency      string           `json:"currency"`
	SubtotalMinor int64            `json:"subtotalMinor"`
	DiscountMinor int64            `json:"discountMinor"`
	TotalMinor    int64            `json:"totalMinor"`
	CouponClaimID string           `json:"couponClaimId,omitempty"`
	Item          ItemResponse     `json:"item"`
	Payment       *PaymentResponse `json:"payment,omitempty"`
	CreatedAt     string           `json:"createdAt"`
	UpdatedAt     string           `json:"updatedAt"`
}

func PresentOrder(value entity.Order) OrderResponse {
	result := OrderResponse{
		OrderNo: value.OrderNo, Status: string(value.Status), Currency: value.Currency,
		SubtotalMinor: value.SubtotalMinor, DiscountMinor: value.DiscountMinor, TotalMinor: value.TotalMinor,
		Item: ItemResponse{
			EditionID: strconv.FormatInt(value.Item.EditionID, 10), GameSlug: value.Item.GameSlug, GameName: value.Item.GameName,
			EditionCode: value.Item.EditionCode, EditionName: value.Item.EditionName, UnitPriceMinor: value.Item.UnitPriceMinor, Region: value.Item.Region,
		},
		CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt),
	}
	if value.CouponClaimID > 0 {
		result.CouponClaimID = strconv.FormatInt(value.CouponClaimID, 10)
	}
	if value.Payment != nil {
		result.Payment = &PaymentResponse{Provider: value.Payment.Provider, Reference: value.Payment.ProviderReference, Status: value.Payment.Status, AmountMinor: value.Payment.AmountMinor, CreatedAt: formatTime(value.Payment.CreatedAt)}
	}
	return result
}

func PresentPage(page order.Page) map[string]any {
	items := make([]OrderResponse, len(page.Items))
	for i := range page.Items {
		items[i] = PresentOrder(page.Items[i])
	}
	return map[string]any{"items": items, "nextCursor": page.NextCursor}
}

func requiredID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, order.ErrInvalidInput
	}
	return id, nil
}

func optionalID(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	return requiredID(value)
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
