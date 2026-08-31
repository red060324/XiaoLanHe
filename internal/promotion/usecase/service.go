package usecase

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/promotion/entity"
)

var (
	ErrInvalidInput        = errors.New("invalid promotion input")
	ErrUnauthenticated     = auth.ErrUnauthenticated
	ErrNotFound            = errors.New("coupon not found")
	ErrClaimLimit          = errors.New("coupon claim limit reached")
	ErrIdempotencyConflict = errors.New("idempotency key conflict")
)

var (
	couponCodePattern  = regexp.MustCompile(`^[A-Z0-9-]{3,32}$`)
	idempotencyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
)

type ListFilter struct {
	BeforeID int64
	GameID   int64
	ViewerID int64
	Limit    int
	Now      time.Time
}

type ClaimCommand struct {
	UserID         int64
	Code           string
	IdempotencyKey string
	Now            time.Time
}

type ClaimResult struct {
	Claim    entity.Claim
	Replayed bool
}

type Store interface {
	List(context.Context, ListFilter) ([]entity.Coupon, error)
	Claim(context.Context, ClaimCommand) (ClaimResult, error)
}

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service { return &Service{store: store, now: time.Now} }

type ListInput struct {
	Cursor   string
	GameID   int64
	ViewerID int64
	Limit    int
}

type Page struct {
	Items      []entity.Coupon
	NextCursor string
}

func (s *Service) List(ctx context.Context, in ListInput) (Page, error) {
	beforeID, err := decodeCursor(in.Cursor)
	if err != nil || in.GameID < 0 || in.ViewerID < 0 {
		return Page{}, ErrInvalidInput
	}
	limit := in.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return Page{}, ErrInvalidInput
	}
	items, err := s.store.List(ctx, ListFilter{BeforeID: beforeID, GameID: in.GameID, ViewerID: in.ViewerID, Limit: limit + 1, Now: s.now().UTC()})
	if err != nil {
		return Page{}, err
	}
	result := Page{Items: items}
	if len(items) > limit {
		result.Items = items[:limit]
		result.NextCursor = encodeCursor(result.Items[len(result.Items)-1].ID)
	}
	return result, nil
}

func (s *Service) Claim(ctx context.Context, principal auth.Principal, code, idempotencyKey string) (ClaimResult, error) {
	if principal.UserID <= 0 {
		return ClaimResult{}, ErrUnauthenticated
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	if !couponCodePattern.MatchString(code) || !idempotencyPattern.MatchString(idempotencyKey) {
		return ClaimResult{}, ErrInvalidInput
	}
	return s.store.Claim(ctx, ClaimCommand{UserID: principal.UserID, Code: code, IdempotencyKey: idempotencyKey, Now: s.now().UTC()})
}

func decodeCursor(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(string(decoded), 10, 64)
	if err != nil || id <= 0 {
		return 0, ErrInvalidInput
	}
	return id, nil
}

func encodeCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}
