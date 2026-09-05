package usecase

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	catalogentity "github.com/red060324/XiaoLanHe/internal/catalog/entity"
	catalog "github.com/red060324/XiaoLanHe/internal/catalog/usecase"
	"github.com/red060324/XiaoLanHe/internal/order/entity"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	promotionentity "github.com/red060324/XiaoLanHe/internal/promotion/entity"
	promotion "github.com/red060324/XiaoLanHe/internal/promotion/usecase"
)

var (
	ErrInvalidInput        = errors.New("invalid order input")
	ErrUnauthenticated     = auth.ErrUnauthenticated
	ErrForbidden           = errors.New("order forbidden")
	ErrNotFound            = errors.New("order not found")
	ErrPriceUnavailable    = errors.New("price unavailable")
	ErrCouponIneligible    = errors.New("coupon ineligible")
	ErrAlreadyOwned        = errors.New("edition already owned")
	ErrIdempotencyConflict = errors.New("order idempotency conflict")
)

var (
	idempotencyPattern  = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
	flashRequestPattern = regexp.MustCompile(`^fsr_[1-9a-z][0-9a-z]{0,12}_[a-f0-9]{32}$`)
	regionPattern       = regexp.MustCompile(`^[A-Z0-9-]{2,16}$`)
	currencyPattern     = regexp.MustCompile(`^[A-Z]{3}$`)
	orderNoPattern      = regexp.MustCompile(`^ord_[a-f0-9]{32}$`)
)

type Catalog interface {
	PurchaseOffer(context.Context, int64, string, string) (catalogentity.PurchaseOffer, error)
}

type Promotion interface {
	QuoteClaim(context.Context, int64, int64, int64, string, int64, int64) (promotionentity.Quote, error)
}

type Cursor struct {
	CreatedAt time.Time
	ID        int64
}

type ListFilter struct {
	UserID int64
	Cursor Cursor
	Limit  int
}

type CreateCommand struct {
	OrderNo        string
	UserID         int64
	IdempotencyKey string
	Offer          catalogentity.PurchaseOffer
	Quote          promotionentity.Quote
	TotalMinor     int64
	Now            time.Time
}

type CreateResult struct {
	Order    entity.Order
	Replayed bool
}

type FlashSaleCreateCommand struct {
	OrderNo          string
	RequestID        string
	UserID           int64
	Offer            catalogentity.PurchaseOffer
	SalePriceMinor   int64
	PaymentExpiresAt time.Time
}

type PayCommand struct {
	OrderNo           string
	UserID            int64
	IdempotencyKey    string
	ProviderReference string
	Now               time.Time
}

type PayResult struct {
	Order    entity.Order
	Replayed bool
}

type Store interface {
	FindByIdempotency(context.Context, int64, string) (entity.Order, error)
	Create(context.Context, CreateCommand) (CreateResult, error)
	CreateFromFlashSale(context.Context, FlashSaleCreateCommand) (CreateResult, error)
	List(context.Context, ListFilter) ([]entity.Order, error)
	Get(context.Context, string) (entity.Order, error)
	Pay(context.Context, PayCommand) (PayResult, error)
}

type Service struct {
	store      Store
	catalog    Catalog
	promotion  Promotion
	now        func() time.Time
	newOrderNo func() (string, error)
}

func NewService(store Store, catalog Catalog, promotion Promotion) *Service {
	return &Service{store: store, catalog: catalog, promotion: promotion, now: time.Now, newOrderNo: randomOrderNo}
}

type CreateInput struct {
	EditionID      int64
	Region         string
	Currency       string
	CouponClaimID  int64
	IdempotencyKey string
}

func (s *Service) Create(ctx context.Context, principal auth.Principal, in CreateInput) (CreateResult, error) {
	if principal.UserID <= 0 {
		return CreateResult{}, ErrUnauthenticated
	}
	region, currency, err := normalizePricing(in.Region, in.Currency)
	if err != nil || in.EditionID <= 0 || in.CouponClaimID < 0 || !idempotencyPattern.MatchString(in.IdempotencyKey) {
		return CreateResult{}, ErrInvalidInput
	}
	existing, err := s.store.FindByIdempotency(ctx, principal.UserID, in.IdempotencyKey)
	switch {
	case err == nil:
		if !sameRequest(existing, in.EditionID, in.CouponClaimID, region, currency) {
			return CreateResult{}, ErrIdempotencyConflict
		}
		return CreateResult{Order: existing, Replayed: true}, nil
	case !errors.Is(err, ErrNotFound):
		return CreateResult{}, err
	}

	offer, err := s.catalog.PurchaseOffer(ctx, in.EditionID, region, currency)
	if err != nil {
		if errors.Is(err, catalog.ErrNotFound) || errors.Is(err, catalog.ErrInvalidInput) {
			return CreateResult{}, ErrPriceUnavailable
		}
		return CreateResult{}, err
	}
	offer.Region = region
	quote := promotionentity.Quote{}
	if in.CouponClaimID > 0 {
		quote, err = s.promotion.QuoteClaim(ctx, principal.UserID, in.CouponClaimID, offer.AmountMinor, offer.Currency, offer.GameID, offer.EditionID)
		if err != nil {
			if errors.Is(err, promotion.ErrNotFound) || errors.Is(err, promotion.ErrInvalidInput) || errors.Is(err, promotionentity.ErrUnavailable) || errors.Is(err, promotionentity.ErrIneligible) {
				return CreateResult{}, ErrCouponIneligible
			}
			return CreateResult{}, err
		}
	}
	total, err := entity.CalculateTotals(offer.AmountMinor, quote.DiscountMinor)
	if err != nil {
		return CreateResult{}, ErrCouponIneligible
	}
	orderNo, err := s.newOrderNo()
	if err != nil {
		return CreateResult{}, err
	}
	return s.store.Create(ctx, CreateCommand{OrderNo: orderNo, UserID: principal.UserID, IdempotencyKey: in.IdempotencyKey, Offer: offer, Quote: quote, TotalMinor: total, Now: s.now().UTC()})
}

type FlashSaleCreateInput struct {
	RequestID        string
	UserID           int64
	Offer            catalogentity.PurchaseOffer
	SalePriceMinor   int64
	PaymentExpiresAt time.Time
}

func (s *Service) CreateFromFlashSale(ctx context.Context, in FlashSaleCreateInput) (CreateResult, error) {
	if in.UserID <= 0 || !flashRequestPattern.MatchString(in.RequestID) ||
		in.Offer.EditionID <= 0 || in.SalePriceMinor < 0 || in.PaymentExpiresAt.IsZero() {
		return CreateResult{}, ErrInvalidInput
	}
	orderNo, err := s.newOrderNo()
	if err != nil {
		return CreateResult{}, err
	}
	return s.store.CreateFromFlashSale(ctx, FlashSaleCreateCommand{
		OrderNo: orderNo, RequestID: in.RequestID, UserID: in.UserID, Offer: in.Offer,
		SalePriceMinor: in.SalePriceMinor, PaymentExpiresAt: in.PaymentExpiresAt.UTC(),
	})
}

type ListInput struct {
	Cursor string
	Limit  int
}

type Page struct {
	Items      []entity.Order
	NextCursor string
}

func (s *Service) List(ctx context.Context, principal auth.Principal, in ListInput) (Page, error) {
	if principal.UserID <= 0 {
		return Page{}, ErrUnauthenticated
	}
	cursor, limit, err := pageInput(in.Cursor, in.Limit)
	if err != nil {
		return Page{}, err
	}
	items, err := s.store.List(ctx, ListFilter{UserID: principal.UserID, Cursor: cursor, Limit: limit + 1})
	if err != nil {
		return Page{}, err
	}
	result := Page{Items: items}
	if len(items) > limit {
		result.Items = items[:limit]
		last := result.Items[len(result.Items)-1]
		result.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return result, nil
}

func (s *Service) Get(ctx context.Context, principal auth.Principal, orderNo string) (entity.Order, error) {
	if principal.UserID <= 0 {
		return entity.Order{}, ErrUnauthenticated
	}
	if !orderNoPattern.MatchString(orderNo) {
		return entity.Order{}, ErrNotFound
	}
	order, err := s.store.Get(ctx, orderNo)
	if err != nil {
		return entity.Order{}, err
	}
	if order.UserID != principal.UserID && !principal.IsAdmin() {
		return entity.Order{}, ErrForbidden
	}
	return order, nil
}

func (s *Service) Pay(ctx context.Context, principal auth.Principal, orderNo, idempotencyKey string) (PayResult, error) {
	if principal.UserID <= 0 {
		return PayResult{}, ErrUnauthenticated
	}
	if !orderNoPattern.MatchString(orderNo) {
		return PayResult{}, ErrNotFound
	}
	if !idempotencyPattern.MatchString(idempotencyKey) {
		return PayResult{}, ErrInvalidInput
	}
	order, err := s.store.Get(ctx, orderNo)
	if err != nil {
		return PayResult{}, err
	}
	if order.UserID != principal.UserID {
		return PayResult{}, ErrForbidden
	}
	return s.store.Pay(ctx, PayCommand{OrderNo: orderNo, UserID: principal.UserID, IdempotencyKey: idempotencyKey, ProviderReference: "sandbox:" + orderNo, Now: s.now().UTC()})
}

func sameRequest(order entity.Order, editionID, claimID int64, region, currency string) bool {
	return order.Item.EditionID == editionID && order.CouponClaimID == claimID && order.Item.Region == region && order.Currency == currency
}

func normalizePricing(region, currency string) (string, string, error) {
	region = strings.ToUpper(strings.TrimSpace(region))
	if region == "" {
		region = "GLOBAL"
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "USD"
	}
	if !regionPattern.MatchString(region) || !currencyPattern.MatchString(currency) {
		return "", "", ErrInvalidInput
	}
	return region, currency, nil
}

func pageInput(value string, limit int) (Cursor, int, error) {
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return Cursor{}, 0, ErrInvalidInput
	}
	if value == "" {
		return Cursor{}, limit, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Cursor{}, 0, ErrInvalidInput
	}
	parts := strings.Split(string(decoded), ":")
	if len(parts) != 2 {
		return Cursor{}, 0, ErrInvalidInput
	}
	micros, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || micros <= 0 {
		return Cursor{}, 0, ErrInvalidInput
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return Cursor{}, 0, ErrInvalidInput
	}
	return Cursor{CreatedAt: time.UnixMicro(micros).UTC(), ID: id}, limit, nil
}

func encodeCursor(createdAt time.Time, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(createdAt.UnixMicro(), 10) + ":" + strconv.FormatInt(id, 10)))
}

func randomOrderNo() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "ord_" + hex.EncodeToString(value[:]), nil
}
