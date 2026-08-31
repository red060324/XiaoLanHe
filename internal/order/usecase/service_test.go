package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	catalogentity "github.com/red060324/XiaoLanHe/internal/catalog/entity"
	catalog "github.com/red060324/XiaoLanHe/internal/catalog/usecase"
	"github.com/red060324/XiaoLanHe/internal/order/entity"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	promotionentity "github.com/red060324/XiaoLanHe/internal/promotion/entity"
)

const testOrderNo = "ord_0123456789abcdef0123456789abcdef"

func TestServiceCreate(t *testing.T) {
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	store := &orderStore{findErr: ErrNotFound}
	catalogClient := &catalogClient{offer: catalogentity.PurchaseOffer{GameID: 3, GameSlug: "demo", GameName: "Demo", EditionID: 12, EditionCode: "standard", EditionName: "Standard", AmountMinor: 1999, Currency: "USD", Region: "GLOBAL"}}
	promotionClient := &promotionClient{quote: promotionentity.Quote{ClaimID: 8, CouponID: 4, CouponCode: "WELCOME20", DiscountMinor: 399}}
	service := NewService(store, catalogClient, promotionClient)
	service.now = func() time.Time { return now }
	service.newOrderNo = func() (string, error) { return testOrderNo, nil }
	principal := auth.Principal{UserID: 9, Role: auth.RoleUser}

	t.Run("creates a price and coupon snapshot", func(t *testing.T) {
		store.createFn = func(command CreateCommand) (CreateResult, error) {
			store.created = command
			return CreateResult{Order: entity.Order{OrderNo: command.OrderNo}}, nil
		}
		result, err := service.Create(context.Background(), principal, CreateInput{EditionID: 12, Region: " cn ", Currency: " usd ", CouponClaimID: 8, IdempotencyKey: "order-key.01"})
		if err != nil || result.Order.OrderNo != testOrderNo {
			t.Fatalf("result=%+v error=%v", result, err)
		}
		if store.created.Offer.Region != "CN" || store.created.Offer.AmountMinor != 1999 || store.created.TotalMinor != 1600 || store.created.Now != now {
			t.Fatalf("command=%+v", store.created)
		}
	})

	t.Run("replays before reading current price", func(t *testing.T) {
		store.findErr = nil
		store.existing = entity.Order{OrderNo: testOrderNo, Currency: "USD", CouponClaimID: 8, Item: entity.Item{EditionID: 12, Region: "CN"}}
		catalogClient.calls = 0
		result, err := service.Create(context.Background(), principal, CreateInput{EditionID: 12, Region: "CN", Currency: "USD", CouponClaimID: 8, IdempotencyKey: "order-key.01"})
		if err != nil || !result.Replayed || catalogClient.calls != 0 {
			t.Fatalf("result=%+v catalog calls=%d error=%v", result, catalogClient.calls, err)
		}
		store.existing.Item.EditionID = 13
		if _, err := service.Create(context.Background(), principal, CreateInput{EditionID: 12, Region: "CN", Currency: "USD", CouponClaimID: 8, IdempotencyKey: "order-key.01"}); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("conflict error=%v", err)
		}
		store.findErr = ErrNotFound
	})

	t.Run("rejects invalid and unavailable inputs", func(t *testing.T) {
		if _, err := service.Create(context.Background(), auth.Principal{}, CreateInput{}); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("authentication error=%v", err)
		}
		if _, err := service.Create(context.Background(), principal, CreateInput{EditionID: 12, IdempotencyKey: "short"}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("input error=%v", err)
		}
		catalogClient.err = catalog.ErrNotFound
		if _, err := service.Create(context.Background(), principal, CreateInput{EditionID: 12, IdempotencyKey: "order-key.02"}); !errors.Is(err, ErrPriceUnavailable) {
			t.Fatalf("price error=%v", err)
		}
		catalogClient.err = nil
		promotionClient.err = promotionentity.ErrIneligible
		if _, err := service.Create(context.Background(), principal, CreateInput{EditionID: 12, CouponClaimID: 8, IdempotencyKey: "order-key.03"}); !errors.Is(err, ErrCouponIneligible) {
			t.Fatalf("coupon error=%v", err)
		}
		promotionClient.err = nil
	})
}

func TestServiceList(t *testing.T) {
	created := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	store := &orderStore{list: []entity.Order{{ID: 3, CreatedAt: created}, {ID: 2, CreatedAt: created.Add(-time.Second)}}}
	service := NewService(store, &catalogClient{}, &promotionClient{})
	page, err := service.List(context.Background(), auth.Principal{UserID: 9}, ListInput{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" || store.filter.Limit != 2 || store.filter.UserID != 9 {
		t.Fatalf("page=%+v filter=%+v error=%v", page, store.filter, err)
	}
	cursor, limit, err := pageInput(page.NextCursor, 1)
	if err != nil || cursor.ID != 3 || !cursor.CreatedAt.Equal(created) || limit != 1 {
		t.Fatalf("cursor=%+v limit=%d error=%v", cursor, limit, err)
	}
	if _, err := service.List(context.Background(), auth.Principal{}, ListInput{}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("authentication error=%v", err)
	}
	if _, err := service.List(context.Background(), auth.Principal{UserID: 9}, ListInput{Cursor: "bad"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cursor error=%v", err)
	}
}

func TestServiceGet(t *testing.T) {
	store := &orderStore{got: entity.Order{OrderNo: testOrderNo, UserID: 9}}
	service := NewService(store, &catalogClient{}, &promotionClient{})
	if _, err := service.Get(context.Background(), auth.Principal{UserID: 9}, testOrderNo); err != nil {
		t.Fatalf("owner error=%v", err)
	}
	if _, err := service.Get(context.Background(), auth.Principal{UserID: 8, Role: auth.RoleAdmin}, testOrderNo); err != nil {
		t.Fatalf("admin error=%v", err)
	}
	if _, err := service.Get(context.Background(), auth.Principal{UserID: 8}, testOrderNo); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-user error=%v", err)
	}
	if _, err := service.Get(context.Background(), auth.Principal{UserID: 9}, "bad"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad order number error=%v", err)
	}
}

func TestServicePay(t *testing.T) {
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	store := &orderStore{got: entity.Order{OrderNo: testOrderNo, UserID: 9, Status: entity.StatusPendingPayment}}
	store.payFn = func(command PayCommand) (PayResult, error) {
		store.paid = command
		return PayResult{Order: store.got}, nil
	}
	service := NewService(store, &catalogClient{}, &promotionClient{})
	service.now = func() time.Time { return now }
	result, err := service.Pay(context.Background(), auth.Principal{UserID: 9}, testOrderNo, "payment-key.01")
	if err != nil || result.Order.OrderNo != testOrderNo || store.paid.ProviderReference != "sandbox:"+testOrderNo || store.paid.Now != now {
		t.Fatalf("result=%+v command=%+v error=%v", result, store.paid, err)
	}
	if _, err := service.Pay(context.Background(), auth.Principal{UserID: 8}, testOrderNo, "payment-key.02"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-user error=%v", err)
	}
	if _, err := service.Pay(context.Background(), auth.Principal{UserID: 9}, testOrderNo, "short"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("key error=%v", err)
	}
}

type orderStore struct {
	existing entity.Order
	findErr  error
	created  CreateCommand
	createFn func(CreateCommand) (CreateResult, error)
	list     []entity.Order
	filter   ListFilter
	got      entity.Order
	getErr   error
	paid     PayCommand
	payFn    func(PayCommand) (PayResult, error)
}

func (s *orderStore) FindByIdempotency(context.Context, int64, string) (entity.Order, error) {
	return s.existing, s.findErr
}
func (s *orderStore) Create(_ context.Context, command CreateCommand) (CreateResult, error) {
	return s.createFn(command)
}
func (s *orderStore) List(_ context.Context, filter ListFilter) ([]entity.Order, error) {
	s.filter = filter
	return s.list, nil
}
func (s *orderStore) Get(context.Context, string) (entity.Order, error) { return s.got, s.getErr }
func (s *orderStore) Pay(_ context.Context, command PayCommand) (PayResult, error) {
	return s.payFn(command)
}

type catalogClient struct {
	offer catalogentity.PurchaseOffer
	err   error
	calls int
}

func (c *catalogClient) PurchaseOffer(context.Context, int64, string, string) (catalogentity.PurchaseOffer, error) {
	c.calls++
	return c.offer, c.err
}

type promotionClient struct {
	quote promotionentity.Quote
	err   error
}

func (p *promotionClient) QuoteClaim(context.Context, int64, int64, int64, string, int64, int64) (promotionentity.Quote, error) {
	return p.quote, p.err
}
