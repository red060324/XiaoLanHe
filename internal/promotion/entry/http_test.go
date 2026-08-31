package entry

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
	"github.com/red060324/XiaoLanHe/internal/promotion/entity"
	promotion "github.com/red060324/XiaoLanHe/internal/promotion/usecase"
)

func TestHTTP(t *testing.T) {
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	store := &httpStore{coupon: entity.Coupon{
		ID: 2, Code: "WELCOME20", Name: "Welcome", DiscountType: entity.DiscountPercentage,
		PercentageBps: 2000, Currency: "USD", MinimumMinor: 1000, TotalStock: 5, ClaimedStock: 1,
		PerUserLimit: 1, CampaignStatus: "active", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour),
	}}
	service := promotion.NewService(store)
	router := server.Default()
	router.Use(httpx.RequestIDMiddleware)
	NewHTTP(service, httpAuthenticator{}, "https://play.example").Register(router)

	list := ut.PerformRequest(router.Engine, "GET", "/api/deals", nil)
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"remainingStock":4`) || !strings.Contains(list.Body.String(), `"code":"WELCOME20"`) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}

	unauthenticated := ut.PerformRequest(router.Engine, "POST", "/api/coupons/WELCOME20/claims", nil,
		ut.Header{Key: "Idempotency-Key", Value: "claim-new.01"})
	if unauthenticated.Code != 401 {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}

	wrongOrigin := claimRequest(router, "claim-new.01", "https://evil.example", nil)
	if wrongOrigin.Code != 403 {
		t.Fatalf("wrong origin status=%d body=%s", wrongOrigin.Code, wrongOrigin.Body.String())
	}

	withBody := &ut.Body{Body: bytes.NewBufferString(`{}`), Len: -1}
	nonEmpty := claimRequest(router, "claim-new.01", "https://play.example", withBody)
	if nonEmpty.Code != 400 {
		t.Fatalf("non-empty status=%d body=%s", nonEmpty.Code, nonEmpty.Body.String())
	}

	invalid := claimRequest(router, "short", "https://play.example", nil)
	if invalid.Code != 400 || !strings.Contains(invalid.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("invalid status=%d body=%s", invalid.Code, invalid.Body.String())
	}

	created := claimRequest(router, "claim-new.01", "https://play.example", nil)
	if created.Code != 201 || !strings.Contains(created.Body.String(), `"replayed":false`) {
		t.Fatalf("created status=%d body=%s", created.Code, created.Body.String())
	}

	replayed := claimRequest(router, "claim-replay.01", "https://play.example", nil)
	if replayed.Code != 200 || !strings.Contains(replayed.Body.String(), `"replayed":true`) {
		t.Fatalf("replayed status=%d body=%s", replayed.Code, replayed.Body.String())
	}
}

func claimRequest(router *server.Hertz, key, origin string, body *ut.Body) *ut.ResponseRecorder {
	return ut.PerformRequest(router.Engine, "POST", "/api/coupons/WELCOME20/claims", body,
		ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=user"},
		ut.Header{Key: "Origin", Value: origin},
		ut.Header{Key: "Idempotency-Key", Value: key})
}

type httpStore struct{ coupon entity.Coupon }

func (s *httpStore) List(context.Context, promotion.ListFilter) ([]entity.Coupon, error) {
	return []entity.Coupon{s.coupon}, nil
}

func (s *httpStore) Claim(_ context.Context, command promotion.ClaimCommand) (promotion.ClaimResult, error) {
	return promotion.ClaimResult{Claim: entity.Claim{
		ID: 9, CouponID: s.coupon.ID, CouponCode: s.coupon.Code, UserID: command.UserID,
		Status: "claimed", IdempotencyKey: command.IdempotencyKey, ClaimedAt: command.Now,
	}, Replayed: command.IdempotencyKey == "claim-replay.01"}, nil
}

func (s *httpStore) FindClaimCoupon(context.Context, int64, int64) (entity.Claim, entity.Coupon, error) {
	return entity.Claim{}, entity.Coupon{}, nil
}

type httpAuthenticator struct{}

func (httpAuthenticator) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	if token != "user" {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	return auth.Principal{UserID: 7, Role: auth.RoleUser}, nil
}
