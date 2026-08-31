package entry

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	catalogentity "github.com/red060324/XiaoLanHe/internal/catalog/entity"
	"github.com/red060324/XiaoLanHe/internal/order/entity"
	order "github.com/red060324/XiaoLanHe/internal/order/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
	promotionentity "github.com/red060324/XiaoLanHe/internal/promotion/entity"
)

const httpOrderNo = "ord_0123456789abcdef0123456789abcdef"

func TestHTTP(t *testing.T) {
	store := &httpStore{}
	service := order.NewService(store, httpCatalog{}, httpPromotion{})
	router := server.Default()
	router.Use(httpx.RequestIDMiddleware)
	NewHTTP(service, httpAuthenticator{}, "https://play.example").Register(router)

	unknown := createRequest(router, "user", "order-key.01", `{"editionId":"12","unknown":true}`)
	if unknown.Code != 400 {
		t.Fatalf("unknown field status=%d body=%s", unknown.Code, unknown.Body.String())
	}
	created := createRequest(router, "user", "order-key.01", `{"editionId":"12","region":"CN","currency":"USD","couponClaimId":"8"}`)
	if created.Code != 201 || !strings.Contains(created.Body.String(), `"totalMinor":1600`) || !strings.Contains(created.Body.String(), `"region":"CN"`) {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	replayed := createRequest(router, "user", "order-key.01", `{"editionId":"12","region":"CN","currency":"USD","couponClaimId":"8"}`)
	if replayed.Code != 200 || !strings.Contains(replayed.Body.String(), `"replayed":true`) {
		t.Fatalf("replay status=%d body=%s", replayed.Code, replayed.Body.String())
	}

	list := ut.PerformRequest(router.Engine, "GET", "/api/orders", nil, userCookie("user"))
	if list.Code != 200 || !strings.Contains(list.Body.String(), httpOrderNo) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	other := ut.PerformRequest(router.Engine, "GET", "/api/orders/"+httpOrderNo, nil, userCookie("other"))
	if other.Code != 403 {
		t.Fatalf("cross-user status=%d body=%s", other.Code, other.Body.String())
	}

	nonEmpty := payRequest(router, "payment-key.01", &ut.Body{Body: bytes.NewBufferString(`{}`), Len: -1})
	if nonEmpty.Code != 400 {
		t.Fatalf("non-empty payment status=%d body=%s", nonEmpty.Code, nonEmpty.Body.String())
	}
	paid := payRequest(router, "payment-key.01", nil)
	if paid.Code != 200 || !strings.Contains(paid.Body.String(), `"status":"paid"`) || !strings.Contains(paid.Body.String(), `"replayed":false`) {
		t.Fatalf("payment status=%d body=%s", paid.Code, paid.Body.String())
	}
	paymentReplay := payRequest(router, "payment-key.01", nil)
	if paymentReplay.Code != 200 || !strings.Contains(paymentReplay.Body.String(), `"replayed":true`) {
		t.Fatalf("payment replay status=%d body=%s", paymentReplay.Code, paymentReplay.Body.String())
	}
	differentKey := payRequest(router, "payment-key.02", nil)
	if differentKey.Code != 409 || !strings.Contains(differentKey.Body.String(), `"code":"invalid_order_state"`) {
		t.Fatalf("different payment key status=%d body=%s", differentKey.Code, differentKey.Body.String())
	}
}

func createRequest(router *server.Hertz, token, key, body string) *ut.ResponseRecorder {
	requestBody := &ut.Body{Body: bytes.NewBufferString(body), Len: -1}
	return ut.PerformRequest(router.Engine, "POST", "/api/orders", requestBody, userCookie(token),
		ut.Header{Key: "Origin", Value: "https://play.example"}, ut.Header{Key: "Idempotency-Key", Value: key})
}

func payRequest(router *server.Hertz, key string, body *ut.Body) *ut.ResponseRecorder {
	return ut.PerformRequest(router.Engine, "POST", "/api/orders/"+httpOrderNo+"/payments/sandbox", body, userCookie("user"),
		ut.Header{Key: "Origin", Value: "https://play.example"}, ut.Header{Key: "Idempotency-Key", Value: key})
}

func userCookie(token string) ut.Header {
	return ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=" + token}
}

type httpStore struct {
	value      entity.Order
	orderKey   string
	paymentKey string
}

func (s *httpStore) FindByIdempotency(_ context.Context, userID int64, key string) (entity.Order, error) {
	if s.value.UserID == userID && s.orderKey == key {
		return s.value, nil
	}
	return entity.Order{}, order.ErrNotFound
}

func (s *httpStore) Create(_ context.Context, command order.CreateCommand) (order.CreateResult, error) {
	s.orderKey = command.IdempotencyKey
	s.value = entity.Order{
		ID: 1, OrderNo: httpOrderNo, UserID: command.UserID, Status: entity.StatusPendingPayment,
		Currency: command.Offer.Currency, SubtotalMinor: command.Offer.AmountMinor, DiscountMinor: command.Quote.DiscountMinor,
		TotalMinor: command.TotalMinor, CouponClaimID: command.Quote.ClaimID,
		Item:      entity.Item{EditionID: command.Offer.EditionID, GameID: command.Offer.GameID, GameSlug: command.Offer.GameSlug, GameName: command.Offer.GameName, EditionCode: command.Offer.EditionCode, EditionName: command.Offer.EditionName, UnitPriceMinor: command.Offer.AmountMinor, Region: command.Offer.Region},
		CreatedAt: command.Now, UpdatedAt: command.Now,
	}
	return order.CreateResult{Order: s.value}, nil
}

func (s *httpStore) List(_ context.Context, filter order.ListFilter) ([]entity.Order, error) {
	if s.value.UserID != filter.UserID {
		return nil, nil
	}
	return []entity.Order{s.value}, nil
}

func (s *httpStore) Get(_ context.Context, orderNo string) (entity.Order, error) {
	if s.value.OrderNo != orderNo {
		return entity.Order{}, order.ErrNotFound
	}
	return s.value, nil
}

func (s *httpStore) Pay(_ context.Context, command order.PayCommand) (order.PayResult, error) {
	if s.value.Status == entity.StatusPaid {
		if s.paymentKey == command.IdempotencyKey {
			return order.PayResult{Order: s.value, Replayed: true}, nil
		}
		return order.PayResult{}, entity.ErrInvalidState
	}
	s.paymentKey = command.IdempotencyKey
	s.value.Status = entity.StatusPaid
	s.value.UpdatedAt = command.Now
	s.value.Payment = &entity.Payment{ID: 1, Provider: "sandbox", ProviderReference: command.ProviderReference, Status: "paid", AmountMinor: s.value.TotalMinor, CreatedAt: command.Now}
	return order.PayResult{Order: s.value}, nil
}

type httpCatalog struct{}

func (httpCatalog) PurchaseOffer(context.Context, int64, string, string) (catalogentity.PurchaseOffer, error) {
	return catalogentity.PurchaseOffer{GameID: 3, GameSlug: "demo", GameName: "Demo", EditionID: 12, EditionCode: "standard", EditionName: "Standard", AmountMinor: 1999, Currency: "USD", Region: "GLOBAL"}, nil
}

type httpPromotion struct{}

func (httpPromotion) QuoteClaim(context.Context, int64, int64, int64, string, int64, int64) (promotionentity.Quote, error) {
	return promotionentity.Quote{ClaimID: 8, CouponID: 4, CouponCode: "WELCOME20", DiscountMinor: 399}, nil
}

type httpAuthenticator struct{}

func (httpAuthenticator) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	switch token {
	case "user":
		return auth.Principal{UserID: 7, Role: auth.RoleUser}, nil
	case "other":
		return auth.Principal{UserID: 8, Role: auth.RoleUser}, nil
	default:
		return auth.Principal{}, errors.New("invalid token")
	}
}
