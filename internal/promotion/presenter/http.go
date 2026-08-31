package presenter

import (
	"strconv"
	"time"

	"github.com/red060324/XiaoLanHe/internal/promotion/entity"
	promotion "github.com/red060324/XiaoLanHe/internal/promotion/usecase"
)

type CouponResponse struct {
	ID               string `json:"id"`
	Code             string `json:"code"`
	Name             string `json:"name"`
	DiscountType     string `json:"discountType"`
	FixedMinor       int64  `json:"fixedMinor,omitempty"`
	PercentageBps    int64  `json:"percentageBps,omitempty"`
	Currency         string `json:"currency"`
	MinimumMinor     int64  `json:"minimumMinor"`
	RemainingStock   int64  `json:"remainingStock"`
	PerUserLimit     int    `json:"perUserLimit"`
	GameID           string `json:"gameId,omitempty"`
	EditionID        string `json:"editionId,omitempty"`
	StartsAt         string `json:"startsAt"`
	EndsAt           string `json:"endsAt"`
	ViewerClaimCount int    `json:"viewerClaimCount"`
}

type ClaimResponse struct {
	ID         string `json:"id"`
	CouponCode string `json:"couponCode"`
	Status     string `json:"status"`
	ClaimedAt  string `json:"claimedAt"`
}

func PresentPage(page promotion.Page) map[string]any {
	items := make([]CouponResponse, len(page.Items))
	for i := range page.Items {
		items[i] = PresentCoupon(page.Items[i])
	}
	return map[string]any{"items": items, "nextCursor": page.NextCursor}
}

func PresentClaimPage(page promotion.ClaimPage) map[string]any {
	items := make([]ClaimResponse, len(page.Items))
	for i := range page.Items {
		items[i] = PresentClaim(page.Items[i])
	}
	return map[string]any{"items": items, "nextCursor": page.NextCursor}
}

func PresentCoupon(coupon entity.Coupon) CouponResponse {
	result := CouponResponse{
		ID: strconv.FormatInt(coupon.ID, 10), Code: coupon.Code, Name: coupon.Name,
		DiscountType: string(coupon.DiscountType), FixedMinor: coupon.FixedMinor, PercentageBps: coupon.PercentageBps,
		Currency: coupon.Currency, MinimumMinor: coupon.MinimumMinor, RemainingStock: coupon.RemainingStock(),
		PerUserLimit: coupon.PerUserLimit, StartsAt: formatTime(coupon.StartsAt), EndsAt: formatTime(coupon.EndsAt), ViewerClaimCount: coupon.ViewerClaimCount,
	}
	if coupon.GameID > 0 {
		result.GameID = strconv.FormatInt(coupon.GameID, 10)
	}
	if coupon.EditionID > 0 {
		result.EditionID = strconv.FormatInt(coupon.EditionID, 10)
	}
	return result
}

func PresentClaim(claim entity.Claim) ClaimResponse {
	return ClaimResponse{ID: strconv.FormatInt(claim.ID, 10), CouponCode: claim.CouponCode, Status: claim.Status, ClaimedAt: formatTime(claim.ClaimedAt)}
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
