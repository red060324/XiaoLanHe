package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/promotion/entity"
)

func TestServiceList(t *testing.T) {
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.FixedZone("test", 8*60*60))
	items := make([]entity.Coupon, 21)
	for i := range items {
		items[i].ID = int64(100 - i)
	}
	store := &fakeStore{listItems: items}
	service := NewService(store)
	service.now = func() time.Time { return now }

	page, err := service.List(context.Background(), ListInput{GameID: 7, ViewerID: 9})
	if err != nil || len(page.Items) != 20 || page.NextCursor == "" {
		t.Fatalf("page=%+v error=%v", page, err)
	}
	if store.listFilter.Limit != 21 || store.listFilter.GameID != 7 || store.listFilter.ViewerID != 9 || !store.listFilter.Now.Equal(now.UTC()) {
		t.Fatalf("filter=%+v", store.listFilter)
	}
	before, err := decodeCursor(page.NextCursor)
	if err != nil || before != page.Items[19].ID {
		t.Fatalf("cursor=%q before=%d error=%v", page.NextCursor, before, err)
	}

	for _, in := range []ListInput{{Cursor: "bad"}, {GameID: -1}, {ViewerID: -1}, {Limit: -1}, {Limit: 51}} {
		if _, err := service.List(context.Background(), in); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("input=%+v error=%v", in, err)
		}
	}
}

func TestServiceClaim(t *testing.T) {
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	store := &fakeStore{claimResult: ClaimResult{Claim: entity.Claim{ID: 3, CouponCode: "WELCOME20"}}}
	service := NewService(store)
	service.now = func() time.Time { return now }
	principal := auth.Principal{UserID: 9, Role: auth.RoleUser}

	t.Run("normalizes coupon code and preserves idempotency key", func(t *testing.T) {
		result, err := service.Claim(context.Background(), principal, " welcome20 ", "claim-key.01")
		if err != nil || result.Claim.ID != 3 {
			t.Fatalf("result=%+v error=%v", result, err)
		}
		if store.claimCommand != (ClaimCommand{UserID: 9, Code: "WELCOME20", IdempotencyKey: "claim-key.01", Now: now}) {
			t.Fatalf("command=%+v", store.claimCommand)
		}
	})

	t.Run("requires authentication", func(t *testing.T) {
		store.claimCalls = 0
		if _, err := service.Claim(context.Background(), auth.Principal{}, "WELCOME20", "claim-key.02"); !errors.Is(err, ErrUnauthenticated) || store.claimCalls != 0 {
			t.Fatalf("error=%v calls=%d", err, store.claimCalls)
		}
	})

	t.Run("rejects malformed inputs", func(t *testing.T) {
		for _, input := range []struct{ code, key string }{{"NO", "claim-key.03"}, {"WELCOME20", "short"}, {"WELCOME20", "claim key 03"}} {
			store.claimCalls = 0
			if _, err := service.Claim(context.Background(), principal, input.code, input.key); !errors.Is(err, ErrInvalidInput) || store.claimCalls != 0 {
				t.Fatalf("input=%+v error=%v calls=%d", input, err, store.claimCalls)
			}
		}
	})

	t.Run("rejects whitespace around idempotency key", func(t *testing.T) {
		store.claimCalls = 0
		if _, err := service.Claim(context.Background(), principal, "WELCOME20", " claim-key.04 "); !errors.Is(err, ErrInvalidInput) || store.claimCalls != 0 {
			t.Fatalf("error=%v calls=%d", err, store.claimCalls)
		}
	})

	t.Run("propagates store error", func(t *testing.T) {
		store.claimErr = ErrClaimLimit
		defer func() { store.claimErr = nil }()
		if _, err := service.Claim(context.Background(), principal, "WELCOME20", "claim-key.05"); !errors.Is(err, ErrClaimLimit) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestServiceQuoteClaim(t *testing.T) {
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	store := &fakeStore{
		foundClaim:  entity.Claim{ID: 8, CouponID: 4, UserID: 9, Status: "claimed"},
		foundCoupon: entity.Coupon{ID: 4, Code: "WELCOME20", DiscountType: entity.DiscountPercentage, PercentageBps: 2000, Currency: "USD", CampaignStatus: "active", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)},
	}
	service := NewService(store)
	service.now = func() time.Time { return now }

	quote, err := service.QuoteClaim(context.Background(), 9, 8, 1999, "USD", 3, 12)
	if err != nil || quote.ClaimID != 8 || quote.CouponCode != "WELCOME20" || quote.DiscountMinor != 399 || store.foundUserID != 9 || store.foundClaimID != 8 {
		t.Fatalf("quote=%+v user=%d claim=%d error=%v", quote, store.foundUserID, store.foundClaimID, err)
	}
	store.foundClaim.Status = "redeemed"
	if _, err := service.QuoteClaim(context.Background(), 9, 8, 1999, "USD", 3, 12); !errors.Is(err, entity.ErrIneligible) {
		t.Fatalf("redeemed claim error=%v", err)
	}
	if _, err := service.QuoteClaim(context.Background(), 0, 8, 1999, "USD", 3, 12); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid input error=%v", err)
	}
}

type fakeStore struct {
	listItems    []entity.Coupon
	listErr      error
	listFilter   ListFilter
	claimResult  ClaimResult
	claimErr     error
	claimCommand ClaimCommand
	claimCalls   int
	foundClaim   entity.Claim
	foundCoupon  entity.Coupon
	foundErr     error
	foundUserID  int64
	foundClaimID int64
}

func (s *fakeStore) List(_ context.Context, filter ListFilter) ([]entity.Coupon, error) {
	s.listFilter = filter
	return s.listItems, s.listErr
}

func (s *fakeStore) Claim(_ context.Context, command ClaimCommand) (ClaimResult, error) {
	s.claimCalls++
	s.claimCommand = command
	return s.claimResult, s.claimErr
}

func (s *fakeStore) FindClaimCoupon(_ context.Context, userID, claimID int64) (entity.Claim, entity.Coupon, error) {
	s.foundUserID, s.foundClaimID = userID, claimID
	return s.foundClaim, s.foundCoupon, s.foundErr
}
